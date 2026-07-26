import {
  closeSync,
  constants as fsConstants,
  fstatSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  unlinkSync,
  writeSync,
} from "node:fs";
import { dirname, join } from "node:path";
import { HooversionError } from "./errors";
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

  let existing = "";
  let sourceFd: number | undefined;
  try {
    sourceFd = openSync(path, fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW | fsConstants.O_NONBLOCK);
    if (!fstatSync(sourceFd).isFile()) throw new HooversionError(`${path} must be a regular file`);
    existing = readFileSync(sourceFd, "utf8");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  } finally {
    if (sourceFd !== undefined) closeSync(sourceFd);
  }

  const title = `# ${release.package.name} Changelog`;
  const normalizedExisting = existing.trim() ? existing : `${title}\n`;
  const [firstLine, ...rest] = normalizedExisting.split(/\r?\n/);
  const header = firstLine.startsWith("# ") ? firstLine : title;
  const body = firstLine.startsWith("# ") ? rest.join("\n").replace(/^\n+/, "") : normalizedExisting;
  const next = `${header}\n\n${release.notes}\n\n${body.trimEnd()}\n`;
  const tempPath = `${path}.hooversion-${process.pid}-${Math.random().toString(16).slice(2)}.tmp`;
  let tempFd: number | undefined;
  let tempOwned = false;
  try {
    tempFd = openSync(
      tempPath,
      fsConstants.O_WRONLY | fsConstants.O_CREAT | fsConstants.O_EXCL | fsConstants.O_NOFOLLOW,
      0o600,
    );
    tempOwned = true;
    writeChangelogFile(tempFd, next);
    fsyncSync(tempFd);
    closeSync(tempFd);
    tempFd = undefined;
    renameSync(tempPath, path);
    tempOwned = false;
  } finally {
    if (tempFd !== undefined) {
      try {
        closeSync(tempFd);
      } catch {
        // Preserve the original write error while still attempting cleanup.
      }
    }
    if (tempOwned) {
      try {
        unlinkSync(tempPath);
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
      }
    }
  }
}

function writeChangelogFile(fd: number, content: string): void {
  const data = Buffer.from(content);
  let offset = 0;
  while (offset < data.byteLength) {
    const written = writeSync(fd, data, offset, data.byteLength - offset, offset);
    if (written <= 0) throw new HooversionError("Failed to write changelog");
    offset += written;
  }
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
