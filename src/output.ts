import { appendFileSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { NormalizedConfig, ReleasePlan } from "./types";

export function writeReleaseOutputs(cwd: string, config: NormalizedConfig, plan: ReleasePlan): void {
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
