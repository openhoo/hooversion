import { readFileSync } from "node:fs";
import { basename, join } from "node:path";
import { HooversionError } from "./errors";
import { getOriginRepository } from "./git";
import type { NormalizedConfig, PackageRelease } from "./types";

interface GitHubReleaseResponse {
  id: number;
  html_url: string;
  upload_url: string;
}

export async function publishGitHubRelease(
  cwd: string,
  config: NormalizedConfig,
  release: PackageRelease,
): Promise<string | undefined> {
  if (config.github === false || !config.github.releases) return undefined;

  const token = process.env.GITHUB_TOKEN || process.env.GH_TOKEN;
  if (!token) {
    throw new HooversionError("GITHUB_TOKEN or GH_TOKEN is required to create GitHub releases.");
  }

  const repository = config.github.repository || getOriginRepository(cwd);
  if (!repository) {
    throw new HooversionError("Could not determine GitHub repository. Set github.repository in hooversion config.");
  }

  const apiUrl = config.github.apiUrl.replace(/\/$/, "");
  const response = await githubFetch<GitHubReleaseResponse>(
    `${apiUrl}/repos/${repository}/releases`,
    token,
    {
      method: "POST",
      body: JSON.stringify({
        tag_name: release.tag,
        name: `${release.package.name} ${release.nextVersion}`,
        body: release.notes,
        draft: false,
        prerelease: false,
      }),
      headers: {
        "content-type": "application/json",
      },
    },
  );

  for (const asset of release.package.assets) {
    await uploadAsset(response.upload_url, token, join(cwd, asset));
  }

  return response.html_url;
}

async function uploadAsset(uploadUrlTemplate: string, token: string, path: string): Promise<void> {
  const uploadUrl = uploadUrlTemplate.replace(/\{.*$/, "");
  const name = basename(path);
  const data = readFileSync(path);
  await githubFetch(`${uploadUrl}?name=${encodeURIComponent(name)}`, token, {
    method: "POST",
    body: data,
    headers: {
      "content-type": "application/octet-stream",
    },
  });
}

async function githubFetch<T = unknown>(url: string, token: string, init: RequestInit): Promise<T> {
  const response = await fetch(url, {
    ...init,
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${token}`,
      "x-github-api-version": "2022-11-28",
      ...(init.headers ?? {}),
    },
  });

  if (!response.ok) {
    const body = await response.text();
    throw new HooversionError(`GitHub API request failed (${response.status} ${response.statusText}): ${body}`);
  }

  return (await response.json()) as T;
}
