import { readFileSync } from "node:fs";
import { basename, isAbsolute, join } from "node:path";
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
    const name = basename(asset);
    if (!existingAssetNames.has(name) && !missingAssets.has(name)) {
      missingAssets.set(name, isAbsolute(asset) ? asset : join(cwd, asset));
    }
  }
  if (missingAssets.size === 0) return;

  const uploadUrl = validateGitHubUploadUrl(uploadUrlTemplate, apiUrl);
  for (const [name, path] of missingAssets) {
    await uploadAsset(uploadUrl, token, path, name);
  }
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

async function uploadAsset(uploadUrl: string, token: string, path: string, name: string): Promise<void> {
  const data = readFileSync(path);
  await githubFetch(`${uploadUrl}?name=${encodeURIComponent(name)}`, token, {
    method: "POST",
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
