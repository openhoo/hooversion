import { HooversionError } from "./errors";
import { runCommand } from "./process";
import type { RawCommit } from "./types";

export function git(cwd: string, args: string[], allowFailure = false): string {
  const result = runCommand("git", args, cwd);
  if (result.code !== 0 && !allowFailure) {
    throw new HooversionError(`git ${args.join(" ")} failed:\n${result.stderr || result.stdout}`);
  }
  return result.stdout.trimEnd();
}

export function isGitRepository(cwd: string): boolean {
  return runCommand("git", ["rev-parse", "--is-inside-work-tree"], cwd).code === 0;
}

export function getCurrentBranch(cwd: string): string {
  const branch = git(cwd, ["branch", "--show-current"]).trim();
  if (branch) return branch;
  if (process.env.GITHUB_HEAD_REF) return process.env.GITHUB_HEAD_REF;
  if (process.env.GITHUB_REF_TYPE !== "tag" && process.env.GITHUB_REF_NAME) return process.env.GITHUB_REF_NAME;
  return git(cwd, ["rev-parse", "--abbrev-ref", "HEAD"]).trim();
}

export function ensureCleanWorkingTree(cwd: string): void {
  const status = git(cwd, ["status", "--porcelain"]);
  if (status.trim()) {
    throw new HooversionError(`Working tree must be clean before release:\n${status}`);
  }
}

export function getLatestTag(cwd: string, pattern: string): string | undefined {
  const output = git(cwd, ["describe", "--tags", "--abbrev=0", "--match", pattern], true).trim();
  return output || undefined;
}

export function tagExists(cwd: string, tag: string): boolean {
  return runCommand("git", ["rev-parse", "--verify", "--quiet", `refs/tags/${tag}`], cwd).code === 0;
}

export function getCommits(cwd: string, fromRef?: string, toRef = "HEAD"): RawCommit[] {
  const range = fromRef ? `${fromRef}..${toRef}` : toRef;
  const revList = git(cwd, ["rev-list", "--reverse", range], true).trim();
  if (!revList) return [];

  return revList.split("\n").map((hash) => {
    const subject = git(cwd, ["show", "-s", "--format=%s", hash]);
    const body = git(cwd, ["show", "-s", "--format=%b", hash]);
    const files = git(cwd, ["diff-tree", "--root", "--no-commit-id", "--name-only", "-r", hash], true)
      .split("\n")
      .map((file) => file.trim())
      .filter(Boolean);
    return { hash, subject, body, files };
  });
}

export function getLastCommit(cwd: string): RawCommit {
  const hash = git(cwd, ["rev-parse", "HEAD"]).trim();
  const subject = git(cwd, ["show", "-s", "--format=%s", hash]);
  const body = git(cwd, ["show", "-s", "--format=%b", hash]);
  const files = git(cwd, ["diff-tree", "--root", "--no-commit-id", "--name-only", "-r", hash], true)
    .split("\n")
    .map((file) => file.trim())
    .filter(Boolean);
  return { hash, subject, body, files };
}

export function getCommitRange(cwd: string, fromRef: string, toRef = "HEAD"): RawCommit[] {
  return getCommits(cwd, fromRef, toRef);
}

export function createReleaseCommit(cwd: string, message: string): void {
  git(cwd, ["add", "--all"]);
  const status = git(cwd, ["status", "--porcelain"]);
  if (!status.trim()) return;
  git(cwd, ["commit", "-m", message]);
}

export function createAnnotatedTag(cwd: string, tag: string, message: string): void {
  git(cwd, ["tag", "-a", tag, "-m", message]);
}

export function pushRelease(cwd: string, branch: string, tags: string[]): void {
  git(cwd, ["push", "origin", `HEAD:${branch}`]);
  if (tags.length > 0) {
    git(cwd, ["push", "origin", ...tags]);
  }
}

export function getOriginRepository(cwd: string): string | undefined {
  const remote = git(cwd, ["config", "--get", "remote.origin.url"], true).trim();
  if (!remote) return undefined;

  const sshMatch = /^git@[^:]+:([^/]+\/[^/.]+)(?:\.git)?$/.exec(remote);
  if (sshMatch) return sshMatch[1];

  const httpsMatch = /^https?:\/\/[^/]+\/([^/]+\/[^/.]+)(?:\.git)?$/.exec(remote);
  if (httpsMatch) return httpsMatch[1];

  return undefined;
}
