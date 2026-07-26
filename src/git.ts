import { relative, resolve, sep } from "node:path";
import { HooversionError } from "./errors";
import { runCommand } from "./process";
import type { RawCommit } from "./types";

export type GitRefKind = "branch" | "tag";

/**
 * Validate a short branch or tag name before it is interpolated into a Git
 * command. This mirrors the security-relevant subset of check-ref-format and
 * additionally rejects option-like names.
 */
export function assertValidGitRef(value: string, kind: GitRefKind): void {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value === "@" ||
    value.startsWith("-") ||
    value.startsWith("refs/") ||
    value.startsWith("/") ||
    value.endsWith("/") ||
    value.includes("//") ||
    value.includes("..") ||
    value.includes("@{") ||
    /[\u0000-\u0020\u007f~^:?*\\]/u.test(value) ||
    value.includes("[")
  ) {
    throw new HooversionError(`Invalid Git ${kind} name: ${JSON.stringify(value)}`);
  }

  const components = value.split("/");
  if (
    components.some(
      (component) =>
        component.length === 0 ||
        component === "." ||
        component === ".." ||
        component.startsWith(".") ||
        component.endsWith(".") ||
        component.toLowerCase().endsWith(".lock"),
    )
  ) {
    throw new HooversionError(`Invalid Git ${kind} name: ${JSON.stringify(value)}`);
  }
}

export type GitNetworkAuth = Readonly<Record<string, string>>;

function commandEnv(auth?: GitNetworkAuth): NodeJS.ProcessEnv | undefined {
  return auth ? { ...process.env, ...auth } : undefined;
}

export function git(cwd: string, args: string[], allowFailure = false, auth?: GitNetworkAuth): string {
  const result = runCommand("git", args, cwd, commandEnv(auth));
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

export function ensureCleanWorkingTree(
  cwd: string,
  ignoredPaths: readonly string[] = [],
  scopedOutputDir?: string,
): void {
  const ignored = new Set(ignoredPaths.map((path) => resolve(cwd, path)));
  const unexpected = git(cwd, ["status", "--porcelain", "--untracked-files=all"])
    .split("\n")
    .filter((line) => {
      if (!line.trim()) return false;
      const path = line.slice(3).trim();
      return !ignored.has(resolve(cwd, path));
    });

  if (scopedOutputDir) {
    const resolvedOutputDir = resolve(cwd, scopedOutputDir);
    const relativeOutputDir = relative(cwd, resolvedOutputDir);
    if (relativeOutputDir && !relativeOutputDir.startsWith(`..${sep}`) && relativeOutputDir !== "..") {
      const ignoredOutputFiles = git(
        cwd,
        ["ls-files", "--others", "--ignored", "--exclude-standard", "--", relativeOutputDir],
        true,
      )
        .split("\n")
        .filter((path) => path.trim() && !ignored.has(resolve(cwd, path.trim())));
      unexpected.push(...ignoredOutputFiles.map((path) => `?? ${path}`));
    }
  }

  if (unexpected.length > 0) {
    throw new HooversionError(`Working tree must be clean before release:\n${unexpected.join("\n")}`);
  }
}

export function getLatestTag(cwd: string, pattern: string): string | undefined {
  const output = git(cwd, ["describe", "--tags", "--abbrev=0", "--match", pattern], true).trim();
  return output || undefined;
}

export function tagExists(cwd: string, tag: string): boolean {
  assertValidGitRef(tag, "tag");
  return runCommand("git", ["rev-parse", "--verify", "--quiet", "--", `refs/tags/${tag}`], cwd).code === 0;
}
export function getHeadSha(cwd: string): string {
  return git(cwd, ["rev-parse", "HEAD"]).trim();
}

export function getRefSha(cwd: string, ref: string): string | undefined {
  let commitRef: string;
  if (typeof ref !== "string") {
    throw new HooversionError(`Invalid Git revision: ${JSON.stringify(ref)}`);
  }
  if (ref === "HEAD" || ref === "HEAD^" || /^[0-9a-fA-F]{40}$/u.test(ref)) {
    commitRef = ref;
  } else if (ref.startsWith("refs/tags/")) {
    const tag = ref.slice("refs/tags/".length);
    assertValidGitRef(tag, "tag");
    commitRef = `${ref}^{commit}`;
  } else if (ref.startsWith("refs/heads/")) {
    assertValidGitRef(ref.slice("refs/heads/".length), "branch");
    commitRef = ref;
  } else {
    throw new HooversionError(`Invalid Git revision: ${JSON.stringify(ref)}`);
  }
  const result = runCommand("git", ["rev-parse", "--verify", "--quiet", "--end-of-options", commitRef], cwd);
  return result.code === 0 ? result.stdout.trim() : undefined;
}

export function getRemoteBranchSha(cwd: string, branch: string, auth?: GitNetworkAuth): string | undefined {
  assertValidGitRef(branch, "branch");
  const remote = git(cwd, ["config", "--get", "remote.origin.url"], true).trim();
  if (!remote) return undefined;
  const output = git(cwd, ["ls-remote", "--", "origin", `refs/heads/${branch}`], true, auth).trim();
  return output ? output.split(/\s+/, 1)[0] : "";
}

export function getCommitMessage(cwd: string, ref = "HEAD"): string {
  return git(cwd, ["show", "-s", "--format=%B", ref]).trimEnd();
}

export function pushRelease(cwd: string, branch: string, tags: string[], auth?: GitNetworkAuth): void {
  assertValidGitRef(branch, "branch");
  for (const tag of tags) assertValidGitRef(tag, "tag");
  git(
    cwd,
    [
      "push",
      "--atomic",
      "--no-verify",
      "--",
      "origin",
      `HEAD:refs/heads/${branch}`,
      ...tags.map((tag) => `refs/tags/${tag}`),
    ],
    false,
    auth,
  );
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
  assertValidGitRef(tag, "tag");
  git(cwd, ["tag", "-a", tag, "-m", message]);
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
