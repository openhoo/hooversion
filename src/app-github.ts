import { validateGitHubApiUrl, validateRepositoryFullName } from "./app-auth";
import { HooversionError } from "./errors";
import type { VersionhooReleaseResult } from "./app-runner";

export type CheckConclusion = "success" | "failure" | "neutral";

export interface CheckRun {
  id: number;
  htmlUrl?: string;
}

interface GitHubCheckRunResponse {
  id: number;
  html_url?: string;
}

const checkName = "Versionhoo Release";
const githubApiVersion = "2022-11-28";
export async function createReleaseCheckRun(
  apiUrl: string,
  repository: string,
  token: string,
  headSha: string,
  expectedRepository: string,
  trustedApiUrls: readonly string[] = [],
): Promise<CheckRun> {
  const response = await githubFetch<GitHubCheckRunResponse>(
    apiUrl,
    `/repos/${validatedRepository(repository, expectedRepository)}/check-runs`,
    token,
    {
      method: "POST",
      body: JSON.stringify({
        name: checkName,
        head_sha: headSha,
        status: "in_progress",
        started_at: new Date().toISOString(),
        output: {
          title: "Versionhoo release started",
          summary: "Versionhoo accepted this workflow run and started release processing.",
        },
      }),
    },
    trustedApiUrls,
  );

  return { id: response.id, htmlUrl: response.html_url };
}

export async function completeReleaseCheckRun(
  apiUrl: string,
  repository: string,
  token: string,
  checkRunId: number,
  conclusion: CheckConclusion,
  title: string,
  summary: string,
  expectedRepository: string,
  trustedApiUrls: readonly string[] = [],
): Promise<void> {
  await githubFetch(
    apiUrl,
    `/repos/${validatedRepository(repository, expectedRepository)}/check-runs/${checkRunId}`,
    token,
    {
      method: "PATCH",
      body: JSON.stringify({
        status: "completed",
        conclusion,
        completed_at: new Date().toISOString(),
        output: { title, summary },
      }),
    },
    trustedApiUrls,
  );
}

export function releaseCheckResult(result: VersionhooReleaseResult): {
  conclusion: CheckConclusion;
  title: string;
  summary: string;
} {
  if (result.outcome === "stale") {
    return {
      conclusion: "neutral",
      title: "Versionhoo skipped a stale workflow run",
      summary: result.message ?? "The release branch moved after this workflow run completed.",
    };
  }

  if (!result.published) {
    return {
      conclusion: "neutral",
      title: "Versionhoo found no release",
      summary: "No release-worthy commits were found for this workflow run.",
    };
  }

  const releases = result.releases.map((release) => `- ${release.name} ${release.version} (${release.tag})`).join("\n");
  return {
    conclusion: "success",
    title: "Versionhoo published releases",
    summary: releases || "Versionhoo published releases.",
  };
}

export function releaseFailureCheckResult(error: unknown): {
  conclusion: CheckConclusion;
  title: string;
  summary: string;
} {
  const message = error instanceof Error ? error.message : String(error);
  return {
    conclusion: "failure",
    title: "Versionhoo release failed",
    summary: truncate(message, 60_000),
  };
}

async function githubFetch<T = unknown>(
  apiUrl: string,
  path: string,
  token: string,
  init: RequestInit,
  trustedApiUrls: readonly string[],
): Promise<T> {
  const trustedApiUrl = validateGitHubApiUrl(apiUrl, trustedApiUrls);
  const response = await fetch(`${trustedApiUrl}${path}`, {
    ...init,
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${token}`,
      "content-type": "application/json",
      "user-agent": "versionhoo-app",
      "x-github-api-version": githubApiVersion,
      ...(init.headers ?? {}),
    },
  });

  if (!response.ok) {
    const body = await response.text();
    throw new HooversionError(`GitHub API request failed (${response.status} ${response.statusText}): ${body}`);
  }

  return (await response.json()) as T;
}
function validatedRepository(repository: string, expectedRepository: string): string {
  const validated = validateRepositoryFullName(repository);
  const expected = validateRepositoryFullName(expectedRepository);
  if (validated.toLowerCase() !== expected.toLowerCase()) {
    throw new HooversionError(`GitHub repository mismatch: expected ${expected}, got ${validated}`);
  }
  return validated;
}

function truncate(value: string, maxLength: number): string {
  return value.length <= maxLength ? value : `${value.slice(0, maxLength - 3)}...`;
}
