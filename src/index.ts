import { lintCommit as lintSingleCommit } from "./commit";
import type { CommitLintIssue, CommitPolicy, RawCommit } from "./types";

/**
 * Lint a commit collection with the same parser and policy as the CLI.
 * Consumers can use this without invoking release planning or mutation.
 */
export function lintCommits(rawCommits: readonly RawCommit[], policy: CommitPolicy = {}): CommitLintIssue[] {
  return rawCommits.flatMap((raw) => lintSingleCommit(raw, policy));
}
export * from "./changelog";
export * from "./commit";
export * from "./config";
export * from "./doctor";
export * from "./errors";
export * from "./git";
export * from "./manifest";
export * from "./plan";
export * from "./release";
export * from "./routing";
export * from "./semver";
export * from "./types";
