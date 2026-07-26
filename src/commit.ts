import type { CommitLintIssue, CommitPolicy, ParsedCommit, RawCommit, ReleaseType } from "./types";

const conventionalHeaderPattern = /^([a-z][a-z0-9-]*)(?:\(([^()\r\n]+)\))?(!)?: (.+)$/;

const breakingFooterLinePattern = /^BREAKING[ -]CHANGE:\s*\S.*$/;

const ignoredSubjectPatterns = [
  /^Merge /,
  /^Revert "/,
  /^revert: /i,
  /^chore\(release\)!?: /,
];

const defaultReleaseRules: Readonly<Record<string, ReleaseType | undefined>> = {
  feat: "minor",
  fix: "patch",
  perf: "patch",
};

export function isIgnoredSubject(subject: string): boolean {
  return ignoredSubjectPatterns.some((pattern) => pattern.test(subject));
}

export function parseCommit(raw: RawCommit, policy: CommitPolicy = {}): ParsedCommit {
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
  const releaseRules = { ...defaultReleaseRules, ...policy.releaseTypes };
  const breaking = Boolean(breakingChangeDescription(raw.body)) || Boolean(bang);
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

export function lintCommit(raw: RawCommit, policy: CommitPolicy = {}): CommitLintIssue[] {
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
  if (policy.allowedTypes && !policy.allowedTypes.includes(type)) {
    issues.push({ hash: raw.hash, subject: raw.subject, message: `type '${type}' is not allowed` });
  }
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

export function parseCommits(rawCommits: RawCommit[], policy: CommitPolicy = {}): ParsedCommit[] {
  return rawCommits.map((raw) => parseCommit(raw, policy));
}

export function breakingChangeDescription(body: string): string | undefined {
  const lines = body.split(/\r?\n/);
  for (let index = 0; index < lines.length; index += 1) {
    const match = breakingFooterLinePattern.exec(lines[index].trim());
    if (!match) continue;
    if ((index === 0 && lines.length === 1) || lines[index - 1]?.trim() === "") {
      return lines[index].trim().replace(/^BREAKING[ -]CHANGE:\s*/, "").trim();
    }
  }
  return undefined;
}
