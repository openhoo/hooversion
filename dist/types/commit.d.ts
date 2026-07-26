import type { CommitLintIssue, CommitPolicy, ParsedCommit, RawCommit } from "./types";
export declare function isIgnoredSubject(subject: string): boolean;
export declare function parseCommit(raw: RawCommit, policy?: CommitPolicy): ParsedCommit;
export declare function lintCommit(raw: RawCommit, policy?: CommitPolicy): CommitLintIssue[];
export declare function parseCommits(rawCommits: RawCommit[], policy?: CommitPolicy): ParsedCommit[];
export declare function breakingChangeDescription(body: string): string | undefined;
