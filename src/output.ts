import { appendFileSync, existsSync, lstatSync, mkdirSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { join, relative, sep } from "node:path";
import type { NormalizedConfig, ReleasePlan } from "./types";

export function getReleaseOutputPaths(cwd: string, outputDir = ".hooversion"): string[] {
  const resolvedOutputDir = join(cwd, outputDir);
  const outputsPath = join(resolvedOutputDir, "outputs.json");
  const paths = new Set<string>([outputsPath, join(cwd, ".release-version")]);

  if (existsSync(outputsPath) && lstatSync(outputsPath).isFile()) {
    try {
      const payload = JSON.parse(readFileSync(outputsPath, "utf8")) as {
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
      // A malformed stale payload is still represented by outputs.json; no user file is inferred.
    }
  }

  return [...paths];
}

export function clearReleaseOutputs(cwd: string, outputDir = ".hooversion"): void {
  for (const outputPath of getReleaseOutputPaths(cwd, outputDir)) {
    if (existsSync(outputPath) && lstatSync(outputPath).isFile()) unlinkSync(outputPath);
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
    const versionPath = join(cwd, ".release-version");
    if (existsSync(versionPath) && lstatSync(versionPath).isFile()) unlinkSync(versionPath);
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
