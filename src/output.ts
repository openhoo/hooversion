import {
  appendFileSync,
  closeSync,
  constants as fsConstants,
  fstatSync,
  mkdirSync,
  openSync,
  readFileSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { join, relative, sep } from "node:path";
import type { NormalizedConfig, ReleasePlan } from "./types";

export function getReleaseOutputPaths(cwd: string, outputDir = ".hooversion"): string[] {
  const resolvedOutputDir = join(cwd, outputDir);
  const outputsPath = join(resolvedOutputDir, "outputs.json");
  const paths = new Set<string>([outputsPath, join(cwd, ".release-version")]);

  let fd: number | undefined;
  try {
    fd = openSync(
      outputsPath,
      fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW | fsConstants.O_NONBLOCK,
    );
    if (!fstatSync(fd).isFile()) return [...paths];
    const payload = JSON.parse(readFileSync(fd, "utf8")) as {
      releases?: Array<{ tag?: unknown }>;
    };
    for (const release of payload.releases ?? []) {
      if (typeof release.tag !== "string") continue;
      const notePath = join(resolvedOutputDir, `${sanitizeFileName(release.tag)}-notes.md`);
      const noteRelativePath = relative(resolvedOutputDir, notePath);
      if (
        noteRelativePath &&
        noteRelativePath !== ".." &&
        !noteRelativePath.startsWith(`..${sep}`)
      ) {
        paths.add(notePath);
      }
    }
  } catch {
    // Missing, unsafe, or malformed stale output cannot identify managed note paths.
  } finally {
    if (fd !== undefined) {
      try {
        closeSync(fd);
      } catch {
        // The stale payload is advisory; a close failure must not infer paths.
      }
    }
  }

  return [...paths];
}

export function clearReleaseOutputs(cwd: string, outputDir = ".hooversion"): void {
  for (const outputPath of getReleaseOutputPaths(cwd, outputDir)) {
    try {
      unlinkSync(outputPath);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    }
  }
}

export function writeReleaseOutputs(cwd: string, config: NormalizedConfig, plan: ReleasePlan): void {
  clearReleaseOutputs(cwd, config.outputDir);
  const outputDir = join(cwd, config.outputDir);
  mkdirSync(outputDir, { recursive: true });

  for (const release of plan.releases) {
    writeFileSync(join(outputDir, `${sanitizeFileName(release.tag)}-notes.md`), `${release.notes}\n`);
  }

  const payload = {
    published: plan.releases.length > 0,
    releases: plan.releases.map((release) => ({
      name: release.package.name,
      version: release.nextVersion,
      tag: release.tag,
      type: release.releaseType,
      notesPath: `${config.outputDir}/${sanitizeFileName(release.tag)}-notes.md`,
    })),
  };

  writeFileSync(join(outputDir, "outputs.json"), `${JSON.stringify(payload, null, 2)}\n`);

  if (plan.releases.length === 1) {
    writeFileSync(join(cwd, ".release-version"), `${plan.releases[0].nextVersion}\n`);
  } else {
    try {
      unlinkSync(join(cwd, ".release-version"));
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    }
  }
  if (process.env.GITHUB_OUTPUT) {
    const lines = [`published=${payload.published}`, `releases_json=${JSON.stringify(payload.releases)}`];
    if (plan.releases.length === 1) {
      lines.push(`version=${plan.releases[0].nextVersion}`, `tag=${plan.releases[0].tag}`);
    }
    appendFileSync(process.env.GITHUB_OUTPUT, `${lines.join("\n")}\n`);
  }
}

function sanitizeFileName(value: string): string {
  return value.replace(/[^a-zA-Z0-9._@-]/g, "-");
}
