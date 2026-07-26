import { constants as fsConstants, type Stats } from "node:fs";
import * as fs from "node:fs/promises";
import type { FileHandle } from "node:fs/promises";
import { basename, isAbsolute, relative, resolve, sep, win32 } from "node:path";
import { HooversionError } from "./errors";
import { getOriginRepository } from "./git";
import type { NormalizedConfig, PackageRelease } from "./types";

interface GitHubReleaseResponse {
  id: number;
  html_url: string;
  upload_url: string;
  tag_name?: string;
  name?: string;
  body?: string | null;
  draft?: boolean;
  prerelease?: boolean;
  assets?: unknown;
}

export async function publishGitHubRelease(
  cwd: string,
  config: NormalizedConfig,
  release: PackageRelease,
  options: { token?: string } = {},
): Promise<string | undefined> {
  if (config.github === false || !config.github.releases) return undefined;

  const token = options.token || process.env.GITHUB_TOKEN || process.env.GH_TOKEN;
  if (!token) {
    throw new HooversionError("GITHUB_TOKEN or GH_TOKEN is required to create GitHub releases.");
  }

  const repository = config.github.repository || getOriginRepository(cwd);
  if (!repository) {
    throw new HooversionError("Could not determine GitHub repository. Set github.repository in hooversion config.");
  }

  const apiUrl = config.github.apiUrl.replace(/\/$/, "");
  const releaseName = `${release.package.name} ${release.nextVersion}`;
  const existing = await githubFetch<GitHubReleaseResponse>(
    `${apiUrl}/repos/${repository}/releases/tags/${encodeURIComponent(release.tag)}`,
    token,
    { method: "GET" },
    true,
  );
  let response: GitHubReleaseResponse;
  let existingAssetNames: ReadonlySet<string> = new Set<string>();
  if (existing) {
    const matches =
      existing.tag_name === release.tag &&
      existing.name === releaseName &&
      existing.body === release.notes &&
      existing.draft === false &&
      existing.prerelease === false;
    if (!matches) {
      throw new HooversionError(`GitHub release already exists for tag ${release.tag} with different metadata.`);
    }
    response = existing;
    existingAssetNames = releaseAssetNames(existing.assets);
  } else {
    response = await githubFetch<GitHubReleaseResponse>(
      `${apiUrl}/repos/${repository}/releases`,
      token,
      {
        method: "POST",
        body: JSON.stringify({
          tag_name: release.tag,
          name: releaseName,
          body: release.notes,
          draft: false,
          prerelease: false,
        }),
        headers: {
          "content-type": "application/json",
        },
      },
    );
  }

  await uploadMissingAssets(response.upload_url, apiUrl, token, cwd, release.package.assets, existingAssetNames);

  return response.html_url;
}

async function uploadMissingAssets(
  uploadUrlTemplate: unknown,
  apiUrl: string,
  token: string,
  cwd: string,
  assets: readonly string[],
  existingAssetNames: ReadonlySet<string>,
): Promise<void> {
  const missingAssets = new Map<string, string>();
  for (const asset of assets) {
    assertSafeReleaseAssetPath(asset);
    const name = basename(asset);
    if (!existingAssetNames.has(name) && !missingAssets.has(name)) {
      missingAssets.set(name, asset);
    }
  }
  if (missingAssets.size === 0) return;

  const uploadUrl = validateGitHubUploadUrl(uploadUrlTemplate, apiUrl);
  const preparedAssets = await readMissingReleaseAssets(cwd, missingAssets);
  try {
    for (const prepared of preparedAssets.values()) {
      await assertStableReleaseAssetDescriptor(prepared);
    }
    for (const prepared of preparedAssets.values()) {
      await assertStableReleaseAssetDescriptor(prepared);
      await uploadAsset(uploadUrl, token, prepared.data, prepared.name);
    }
  } finally {
    await Promise.all(preparedAssets.map(async ({ file }) => file.close()));
  }
}

const maxReleaseAssetSizeBytes = 100 * 1024 * 1024;

interface ReleaseAssetMetadata {
  dev: number;
  ino: number;
  size: number;
  mtimeMs: number;
  ctimeMs: number;
}

interface PreparedReleaseAsset {
  name: string;
  path: string;
  root: string;
  data: Buffer;
  file: FileHandle;
  metadata: ReleaseAssetMetadata;
}

async function readMissingReleaseAssets(
  cwd: string,
  missingAssets: ReadonlyMap<string, string>,
): Promise<PreparedReleaseAsset[]> {
  let root: string;
  try {
    root = await fs.realpath(cwd);
    if (!(await fs.lstat(root)).isDirectory()) {
      throw new HooversionError(`Release asset root is not a directory: ${cwd}`);
    }
  } catch (error) {
    if (error instanceof HooversionError) throw error;
    throw new HooversionError(`Could not resolve release asset root: ${cwd}`);
  }

  const preparedAssets: PreparedReleaseAsset[] = [];
  try {
    for (const [name, asset] of missingAssets) {
      preparedAssets.push(await readValidatedReleaseAsset(root, asset, name));
    }
    return preparedAssets;
  } catch (error) {
    await Promise.all(preparedAssets.map(async ({ file }) => file.close()));
    throw error;
  }
}

function assertSafeReleaseAssetPath(asset: string): void {
  if (
    asset.length === 0 ||
    asset.includes("\0") ||
    isAbsolute(asset) ||
    win32.isAbsolute(asset) ||
    asset.split(/[\\/]+/u).includes("..")
  ) {
    throw new HooversionError(`Release asset path must be relative without parent traversal: ${asset}`);
  }
}

function isContainedPath(root: string, path: string): boolean {
  const pathFromRoot = relative(root, path);
  return (
    pathFromRoot === "" ||
    (pathFromRoot !== ".." && !pathFromRoot.startsWith(`..${sep}`) && !isAbsolute(pathFromRoot))
  );
}

function resolveReleaseAssetPath(root: string, asset: string): string {
  const path = resolve(root, asset);
  if (!isContainedPath(root, path)) {
    throw new HooversionError(`Release asset path escapes the repository: ${asset}`);
  }
  return path;
}

async function readValidatedReleaseAsset(root: string, asset: string, name: string): Promise<PreparedReleaseAsset> {
  const path = resolveReleaseAssetPath(root, asset);
  let file: FileHandle | undefined;
  try {
    const noFollow = fsConstants.O_NOFOLLOW;
    if (typeof noFollow !== "number") {
      throw new HooversionError("Secure release asset uploads require O_NOFOLLOW support.");
    }

    file = await fs.open(path, fsConstants.O_RDONLY | noFollow);
    const descriptorStats = await file.stat();
    assertRegularReleaseAsset(descriptorStats, asset);
    const descriptorMetadata = releaseAssetMetadata(descriptorStats);
    await assertStableReleaseAssetPath(root, path, asset, descriptorMetadata);

    const data = await readReleaseAssetDescriptor(file, descriptorMetadata.size, asset);
    const afterReadMetadata = releaseAssetMetadata(await file.stat());
    if (
      !sameReleaseAssetMetadata(descriptorMetadata, afterReadMetadata) ||
      data.byteLength !== descriptorMetadata.size
    ) {
      throw new HooversionError(`Release asset changed while it was being read: ${asset}`);
    }
    await assertStableReleaseAssetPath(root, path, asset, afterReadMetadata);
    return { name, path, root, data, file, metadata: afterReadMetadata };
  } catch (error) {
    if (file) await file.close();
    if (error instanceof HooversionError) throw error;
    const reason = error instanceof Error ? error.message : "unknown error";
    throw new HooversionError(`Could not securely read release asset ${asset}: ${reason}`);
  }
}

async function assertStableReleaseAssetDescriptor(asset: PreparedReleaseAsset): Promise<void> {
  const descriptorMetadata = releaseAssetMetadata(await asset.file.stat());
  if (!sameReleaseAssetMetadata(asset.metadata, descriptorMetadata)) {
    throw new HooversionError(`Release asset changed while it was being read: ${asset.name}`);
  }
  await assertStableReleaseAssetPath(asset.root, asset.path, asset.name, descriptorMetadata);
}

function assertRegularReleaseAsset(stats: Stats, asset: string): void {
  if (!stats.isFile() || stats.isSymbolicLink()) {
    throw new HooversionError(`Release asset must be a regular file, not a symbolic link: ${asset}`);
  }
  if (!Number.isSafeInteger(stats.size) || stats.size < 0 || stats.size > maxReleaseAssetSizeBytes) {
    throw new HooversionError(`Release asset exceeds the ${maxReleaseAssetSizeBytes} byte upload limit: ${asset}`);
  }
}

function releaseAssetMetadata(stats: Stats): ReleaseAssetMetadata {
  return {
    dev: stats.dev,
    ino: stats.ino,
    size: stats.size,
    mtimeMs: stats.mtimeMs,
    ctimeMs: stats.ctimeMs,
  };
}

function sameReleaseAssetMetadata(left: ReleaseAssetMetadata, right: ReleaseAssetMetadata): boolean {
  return (
    left.dev === right.dev &&
    left.ino === right.ino &&
    left.size === right.size &&
    left.mtimeMs === right.mtimeMs &&
    left.ctimeMs === right.ctimeMs
  );
}

async function assertStableReleaseAssetPath(
  root: string,
  path: string,
  asset: string,
  expected: ReleaseAssetMetadata,
): Promise<void> {
  const canonicalPath = await fs.realpath(path);
  if (!isContainedPath(root, canonicalPath)) {
    throw new HooversionError(`Release asset path escapes the repository: ${asset}`);
  }
  if (canonicalPath !== path) {
    throw new HooversionError(`Release asset path must not traverse a symbolic link: ${asset}`);
  }

  const pathStats = await fs.lstat(path);
  assertRegularReleaseAsset(pathStats, asset);
  if (!sameReleaseAssetMetadata(expected, releaseAssetMetadata(pathStats))) {
    throw new HooversionError(`Release asset changed while it was being read: ${asset}`);
  }
}

async function readReleaseAssetDescriptor(file: FileHandle, size: number, asset: string): Promise<Buffer> {
  const data = Buffer.allocUnsafe(size);
  let offset = 0;
  while (offset < data.byteLength) {
    const { bytesRead } = await file.read(data, offset, data.byteLength - offset, offset);
    if (bytesRead === 0) {
      throw new HooversionError(`Release asset changed while it was being read: ${asset}`);
    }
    offset += bytesRead;
  }
  return data;
}

function releaseAssetNames(assets: unknown): ReadonlySet<string> {
  if (assets === undefined) return new Set<string>();
  if (!Array.isArray(assets)) {
    throw new HooversionError("GitHub release response has invalid assets.");
  }

  const assetNames = new Set<string>();
  for (const asset of assets) {
    if (
      typeof asset !== "object" ||
      asset === null ||
      !("name" in asset) ||
      typeof asset.name !== "string"
    ) {
      throw new HooversionError("GitHub release response has invalid assets.");
    }
    assetNames.add(basename(asset.name));
  }
  return assetNames;
}

function validateGitHubUploadUrl(uploadUrlTemplate: unknown, apiUrl: string): string {
  if (typeof uploadUrlTemplate !== "string") {
    throw new HooversionError("Invalid GitHub release upload URL.");
  }

  const uploadUrl = uploadUrlTemplate.replace(/\{\?[^}]*\}$/, "");
  if (uploadUrl.includes("{") || uploadUrl.includes("}")) {
    throw new HooversionError(`Invalid GitHub release upload URL: ${uploadUrlTemplate}`);
  }

  let parsed: URL;
  try {
    parsed = new URL(uploadUrl);
  } catch {
    throw new HooversionError(`Invalid GitHub release upload URL: ${uploadUrlTemplate}`);
  }

  const authority = uploadUrl.slice(uploadUrl.indexOf("//") + 2).split(/[/?#]/, 1)[0] ?? "";
  const host = authority.slice(authority.lastIndexOf("@") + 1);
  const hasExplicitPort = host.startsWith("[") ? host.includes("]:") : host.includes(":");
  if (
    parsed.protocol !== "https:" ||
    parsed.username ||
    parsed.password ||
    parsed.search ||
    parsed.hash ||
    hasExplicitPort
  ) {
    throw new HooversionError(`Invalid GitHub release upload URL: ${uploadUrlTemplate}`);
  }

  const apiOrigin = new URL(apiUrl).origin;
  const trusted =
    parsed.origin === apiOrigin ||
    (apiOrigin === "https://api.github.com" && parsed.origin === "https://uploads.github.com");
  if (!trusted) {
    throw new HooversionError(`Untrusted GitHub release upload URL: ${uploadUrlTemplate}`);
  }

  return parsed.toString();
}

async function uploadAsset(uploadUrl: string, token: string, data: Uint8Array, name: string): Promise<void> {
  await githubFetch(`${uploadUrl}?name=${encodeURIComponent(name)}`, token, {
    method: "POST",
    // codeql[js/file-access-to-http]: The descriptor-only validated reader above enforces containment, no-follow, file type, size, and stability.
    body: data,
    headers: {
      "content-type": "application/octet-stream",
    },
  });
}

async function githubFetch<T = unknown>(
  url: string,
  token: string,
  init: RequestInit,
  notFoundIsEmpty = false,
): Promise<T> {
  const response = await fetch(url, {
    ...init,
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${token}`,
      "x-github-api-version": "2022-11-28",
      ...(init.headers ?? {}),
    },
  });

  if (response.status === 404 && notFoundIsEmpty) return undefined as T;
  if (!response.ok) {
    const body = await response.text();
    throw new HooversionError(`GitHub API request failed (${response.status} ${response.statusText}): ${body}`);
  }

  return (await response.json()) as T;
}
