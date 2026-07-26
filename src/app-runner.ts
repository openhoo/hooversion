import { chmodSync, existsSync, mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { loadConfig } from "./config";
import { HooversionError } from "./errors";
import { validateGitHubApiUrl, validateRepositoryFullName } from "./app-auth";
import { createReleasePlan } from "./plan";
import { executeRelease } from "./release";
import type { GitNetworkAuth } from "./git";
import { runCommand, runShell } from "./process";
const GIT_ASKPASS_FILENAME = ".git-askpass";
const GIT_TOKEN_FILENAME = ".git-token";
const GIT_TOKEN_FILE_ENV = "VERSIONHOO_GIT_TOKEN_FILE";
const GIT_ASKPASS_SCRIPT =
  '#!/bin/sh\ncase "$1" in\n  *[Uu][Ss][Ee][Rr][Nn][Aa][Mm][Ee]*) printf "%s\\n" "x-access-token" ;;\n  *) cat "$VERSIONHOO_GIT_TOKEN_FILE" ;;\nesac\n';

interface GitAuthArtifacts {
  env: GitNetworkAuth;
  cleanup: () => void;
}


export interface VersionhooReleaseJob {
  repositoryFullName: string;
  cloneUrl: string;
  branch: string;
  headSha: string;
  token: string;
  apiUrl?: string;
  trustedApiUrls?: readonly string[];
  trustedCloneHosts?: readonly string[];
  workDir?: string;
  configPath?: string;
  installCommand?: string;
  gitAuthorName?: string;
  gitAuthorEmail?: string;
  keepWorkDir?: boolean;
}

export interface VersionhooReleaseResult {
  repositoryFullName: string;
  branch: string;
  headSha: string;
  workDir: string;
  outcome: "published" | "no_release" | "stale";
  published: boolean;
  message?: string;
  releases: Array<{
    name: string;
    version: string;
    tag: string;
  }>;
}

export async function runVersionhooRelease(job: VersionhooReleaseJob): Promise<VersionhooReleaseResult> {
  const parent = job.workDir ?? join(tmpdir(), "versionhoo");
  mkdirSync(parent, { recursive: true });
  const workDir = mkdtempSync(join(parent, "release-"));
  const repoDir = join(workDir, "repo");
  const repositoryHome = mkdtempSync(join(workDir, ".home-"));
  try {
    return await withRepositoryEnvironment(job, async () => {
      try {
        const cloneUrl = validateCloneUrl(job.cloneUrl, job.repositoryFullName, job.trustedCloneHosts);
        const gitAuth = createGitAuthArtifacts(repositoryHome, job.token);
        try {
          checked(
            "git",
            ["clone", "--branch", job.branch, "--no-single-branch", cloneUrl, repoDir],
            workDir,
            job.token,
            gitAuth.env,
          );
          checked("git", ["config", "user.name", job.gitAuthorName ?? "versionhoo[bot]"], repoDir, job.token);
          checked(
            "git",
            ["config", "user.email", job.gitAuthorEmail ?? "versionhoo[bot]@users.noreply.github.com"],
            repoDir,
            job.token,
          );

          const branchHead = runCommand("git", ["rev-parse", "HEAD"], repoDir).stdout.trim();
          if (branchHead !== job.headSha) {
            return {
              repositoryFullName: job.repositoryFullName,
              branch: job.branch,
              headSha: job.headSha,
              workDir,
              outcome: "stale",
              published: false,
              message: `Skipped stale workflow run for ${job.repositoryFullName}@${job.branch}: branch is ${branchHead}, workflow passed on ${job.headSha}.`,
              releases: [],
            };
          }

          installProjectDependencies(repoDir, job.installCommand, job.token);

          const config = await loadConfig(repoDir, job.configPath);
          const trustedApiUrl = validateGitHubApiUrl(job.apiUrl ?? "https://api.github.com", job.trustedApiUrls);
          if (config.github !== false) {
            config.github.repository = validateRepositoryFullName(job.repositoryFullName);
            config.github.apiUrl = trustedApiUrl;
          }
          const plan = createReleasePlan(repoDir, config);
          const execution = await executeRelease(repoDir, config, plan, {
            push: true,
            github: true,
            githubToken: job.token,
            gitAuth: gitAuth.env,
          });

          return {
            repositoryFullName: job.repositoryFullName,
            branch: job.branch,
            headSha: job.headSha,
            workDir,
            outcome: execution.published ? "published" : "no_release",
            published: execution.published,
            releases: execution.plan.releases.map((release) => ({
              name: release.package.name,
              version: release.nextVersion,
              tag: release.tag,
            })),
          };
        } finally {
          gitAuth.cleanup();
        }
      } finally {
        if (!job.keepWorkDir) {
          rmSync(workDir, { recursive: true, force: true });
        }
      }
    }, repositoryHome);
  } finally {
    rmSync(repositoryHome, { recursive: true, force: true });
  }
}
function createGitAuthArtifacts(authDir: string, token: string): GitAuthArtifacts {
  const tokenPath = join(authDir, GIT_TOKEN_FILENAME);
  const askpassPath = join(authDir, GIT_ASKPASS_FILENAME);
  let tokenCreated = false;
  let askpassCreated = false;
  const cleanup = (): void => {
    if (askpassCreated) {
      rmSync(askpassPath, { force: true });
      askpassCreated = false;
    }
    if (tokenCreated) {
      rmSync(tokenPath, { force: true });
      tokenCreated = false;
    }
  };
  try {
    writeFileSync(tokenPath, `${token}\n`, { encoding: "utf8", flag: "wx", mode: 0o600 });
    tokenCreated = true;
    chmodSync(tokenPath, 0o600);
    writeFileSync(askpassPath, GIT_ASKPASS_SCRIPT, { encoding: "utf8", flag: "wx", mode: 0o700 });
    askpassCreated = true;
    chmodSync(askpassPath, 0o700);
    return {
      env: {
        GIT_ASKPASS: askpassPath,
        GIT_TERMINAL_PROMPT: "0",
        [GIT_TOKEN_FILE_ENV]: tokenPath,
      },
      cleanup,
    };
  } catch (error) {
    cleanup();
    throw error;
  }
}

function installProjectDependencies(repoDir: string, configuredCommand: string | undefined, secret: string): void {
  const command = configuredCommand ?? (existsSync(join(repoDir, "bun.lock")) ? "bun install --frozen-lockfile" : "");
  if (!command) return;

  const result = runShell(command, repoDir);
  if (result.code !== 0) {
    throw new HooversionError(
      `Install command failed: ${redact(command, secret)}\n${redact(result.stderr || result.stdout, secret)}`,
    );
  }
}


function checked(
  command: string,
  args: string[],
  cwd: string,
  secret: string,
  env?: NodeJS.ProcessEnv,
): void {
  const result = runCommand(command, args, cwd, env ? { ...process.env, ...env } : undefined);
  if (result.code !== 0) {
    const rendered = `${command} ${args.map((arg) => redact(arg, secret)).join(" ")}`;
    throw new HooversionError(`${rendered} failed:\n${redact(result.stderr || result.stdout, secret)}`);
  }
}

export function validateCloneUrl(
  cloneUrl: string,
  repositoryFullName: string,
  trustedCloneHosts: readonly string[] = [],
): string {
  const expected = validateRepositoryFullName(repositoryFullName);
  let parsed: URL;
  try {
    parsed = new URL(cloneUrl);
  } catch {
    throw new HooversionError(`Invalid GitHub clone URL: ${cloneUrl}`);
  }
  if (
    parsed.protocol !== "https:" ||
    parsed.username ||
    parsed.password ||
    parsed.port ||
    parsed.search ||
    parsed.hash
  ) {
    throw new HooversionError(`Invalid GitHub clone URL: ${cloneUrl}`);
  }
  const allowedHosts = new Set(["github.com", ...trustedCloneHosts.map((host) => host.toLowerCase())]);
  if (!allowedHosts.has(parsed.hostname.toLowerCase())) {
    throw new HooversionError(`Untrusted GitHub clone host: ${parsed.hostname}`);
  }
  const path = decodeURIComponent(parsed.pathname).replace(/^\/+|\/+$/g, "").replace(/\.git$/i, "");
  if (path.toLowerCase() !== expected.toLowerCase()) {
    throw new HooversionError(`GitHub clone repository mismatch: expected ${expected}, got ${path}`);
  }
  return parsed.toString().replace(/\/+$/, "");
}

let repositoryEnvironmentTail = Promise.resolve();

async function withRepositoryEnvironment<T>(
  job: VersionhooReleaseJob,
  operation: () => Promise<T>,
  repositoryHome: string,
): Promise<T> {
  const previous = repositoryEnvironmentTail;
  let release!: () => void;
  repositoryEnvironmentTail = new Promise<void>((resolve) => {
    release = resolve;
  });
  await previous;
  const original = { ...process.env };

  const allowed: Record<string, string> = {
    PATH: original.PATH ?? "",
    HOME: repositoryHome,
    SHELL: original.SHELL ?? "/bin/sh",
    LANG: original.LANG ?? "C.UTF-8",
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_CONFIG_SYSTEM: "/dev/null",
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_TERMINAL_PROMPT: "0",
    GITHUB_REPOSITORY: job.repositoryFullName,
    GITHUB_REF_NAME: job.branch,
    GITHUB_SHA: job.headSha,
    VERSIONHOO_REPOSITORY: job.repositoryFullName,
    VERSIONHOO_BRANCH: job.branch,
    VERSIONHOO_SHA: job.headSha,
  };
  for (const key of Object.keys(process.env)) delete process.env[key];
  Object.assign(process.env, allowed);
  try {
    return await operation();
  } finally {
    for (const key of Object.keys(process.env)) delete process.env[key];
    Object.assign(process.env, original);
    release();
  }
}

function redact(value: string, secret: string): string {
  return value.replaceAll(secret, "[redacted]");
}
