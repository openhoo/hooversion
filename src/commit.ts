import type { CommitLintIssue, ParsedCommit, RawCommit, ReleaseType } from "./types";

const conventionalHeaderPattern = /^([a-z][a-z0-9-]*)(?:\(([^()\r\n]+)\))?(!)?: (.+)$/;
const breakingFooterPattern = /(?:^|\n)BREAKING[ -]CHANGE: /;

const ignoredSubjectPatterns = [
  /^Merge /,
  /^Revert "/,
  /^revert: /i,
  /^chore\(release\)!?: /,
];

const releaseRules: Record<string, ReleaseType | undefined> = {
  feat: "minor",
  fix: "patch",
  perf: "patch",
};

export function isIgnoredSubject(subject: string): boolean {
  return ignoredSubjectPatterns.some((pattern) => pattern.test(subject));
}

export function parseCommit(raw: RawCommit): ParsedCommit {
  const ignored = isIgnoredSubject(raw.subject);
  const match = conventionalHeaderPattern.exec(raw.subject);

  if (!match) {
    return {
      ...raw,
      type: "",
      description: raw.subject,
      breaking: false,
      ignored,
    };
  }

  const [, type, scope, bang, description] = match;
  const breaking = Boolean(bang) || breakingFooterPattern.test(raw.body);
  return {
    ...raw,
    type,
    scope,
    description,
    breaking,
    releaseType: breaking ? "major" : releaseRules[type],
    ignored,
  };
}

export function lintCommit(raw: RawCommit): CommitLintIssue[] {
  if (isIgnoredSubject(raw.subject)) return [];

  const issues: CommitLintIssue[] = [];
  const match = conventionalHeaderPattern.exec(raw.subject);
  if (!match) {
    issues.push({
      hash: raw.hash,
      subject: raw.subject,
      message: "header must match '<type>(optional-scope)!: description'",
    });
    return issues;
  }

  const [, type, , , description] = match;
  if (!type) {
    issues.push({ hash: raw.hash, subject: raw.subject, message: "type is required" });
  }
  if (!description.trim()) {
    issues.push({ hash: raw.hash, subject: raw.subject, message: "description is required" });
  }
  if (raw.subject.length > 100) {
    issues.push({
      hash: raw.hash,
      subject: raw.subject,
      message: "header must not exceed 100 characters",
    });
  }

  return issues;
}

export function parseCommits(rawCommits: RawCommit[]): ParsedCommit[] {
  return rawCommits.map(parseCommit);
}
