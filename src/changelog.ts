import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { mkdirSync } from "node:fs";
import { breakingChangeDescription } from "./commit";
import type { PackageRelease, ParsedCommit } from "./types";

const groupTitles: Record<string, string> = {
  major: "Breaking Changes",
  feat: "Features",
  fix: "Bug Fixes",
  perf: "Performance",
};

export function generateReleaseNotes(release: Omit<PackageRelease, "notes">): string {
  const date = new Date().toISOString().slice(0, 10);
  const lines = [`## ${release.nextVersion} (${date})`, ""];
  const groups = groupCommits(release.commits);

  for (const [title, commits] of groups) {
    lines.push(`### ${title}`, "");
    for (const commit of commits) {
      const scope = commit.scope ? `**${commit.scope}:** ` : "";
      lines.push(`- ${scope}${commit.description} (${commit.hash.slice(0, 7)})`);
      if (commit.breaking && commit.body) {
        const breaking = breakingChangeDescription(commit.body);
        if (breaking) lines.push(`  - BREAKING: ${breaking}`);
      }
    }
    lines.push("");
  }

  return lines.join("\n").trimEnd();
}

export function updateChangelog(cwd: string, release: PackageRelease): void {
  const path = join(cwd, release.changelogPath);
  mkdirSync(dirname(path), { recursive: true });
  const existing = existsSync(path) ? readFileSync(path, "utf8") : "";
  const title = `# ${release.package.name} Changelog`;
  const normalizedExisting = existing.trim() ? existing : `${title}\n`;
  const [firstLine, ...rest] = normalizedExisting.split(/\r?\n/);
  const header = firstLine.startsWith("# ") ? firstLine : title;
  const body = firstLine.startsWith("# ") ? rest.join("\n").replace(/^\n+/, "") : normalizedExisting;
  const next = `${header}\n\n${release.notes}\n\n${body.trimEnd()}\n`;
  writeFileSync(path, next);
}

function groupCommits(commits: ParsedCommit[]): [string, ParsedCommit[]][] {
  const buckets = new Map<string, ParsedCommit[]>();
  for (const commit of commits) {
    if (commit.breaking) {
      pushBucket(buckets, groupTitles.major, commit);
    } else if (commit.type in groupTitles) {
      pushBucket(buckets, groupTitles[commit.type], commit);
    } else {
      pushBucket(buckets, "Other Changes", commit);
    }
  }

  const orderedTitles = [...Object.values(groupTitles), "Other Changes"];
  return orderedTitles
    .map((title) => [title, buckets.get(title) ?? []] as [string, ParsedCommit[]])
    .filter(([, values]) => values.length > 0);
}

function pushBucket(map: Map<string, ParsedCommit[]>, key: string, commit: ParsedCommit): void {
  const bucket = map.get(key) ?? [];
  bucket.push(commit);
  map.set(key, bucket);
}
