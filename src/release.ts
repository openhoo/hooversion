import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { HooversionError } from "./errors";
import {
  createAnnotatedTag,
  createReleaseCommit,
  ensureCleanWorkingTree,
  pushRelease,
  tagExists,
} from "./git";
import { publishGitHubRelease } from "./github";
import { updateChangelog } from "./changelog";
import { updateLocalDependencyVersions, updateManifestVersion } from "./manifest";
import { writeReleaseOutputs } from "./output";
import { runShell } from "./process";
import type { NormalizedConfig, ReleasePlan } from "./types";

export interface ReleaseOptions {
  dryRun?: boolean;
  push?: boolean;
  github?: boolean;
}

export async function executeRelease(
  cwd: string,
  config: NormalizedConfig,
  plan: ReleasePlan,
  options: ReleaseOptions = {},
): Promise<void> {
  validatePlan(cwd, config, plan);

  if (plan.releases.length === 0) {
    if (!options.dryRun) writeReleaseOutputs(cwd, config, plan);
    return;
  }

  if (options.dryRun) return;

  ensureCleanWorkingTree(cwd);
  runHooks(cwd, config.hooks.beforeRelease);

  const releasedVersions = new Map(plan.releases.map((release) => [release.package.name, release.nextVersion]));
  for (const release of plan.releases) {
    updateManifestVersion(cwd, release.package, release.nextVersion);
  }
  updateLocalDependencyVersions(cwd, config.packages, releasedVersions);
  for (const release of plan.releases) {
    updateChangelog(cwd, release);
  }

  runHooks(cwd, config.hooks.afterVersion);

  createReleaseCommit(cwd, releaseCommitMessage(plan));
  for (const release of plan.releases) {
    createAnnotatedTag(cwd, release.tag, `${release.package.name} ${release.nextVersion}`);
  }

  const shouldPush = options.push ?? config.push;
  if (shouldPush) {
    pushRelease(cwd, plan.branch, plan.releases.map((release) => release.tag));
  }

  mkdirSync(join(cwd, config.outputDir), { recursive: true });
  if (options.github ?? true) {
    for (const release of plan.releases) {
      await publishGitHubRelease(cwd, config, release);
    }
  }

  writeReleaseOutputs(cwd, config, plan);
  runHooks(cwd, config.hooks.afterRelease);
}

export function validatePlan(cwd: string, config: NormalizedConfig, plan: ReleasePlan): void {
  if (!config.branches.includes(plan.branch)) {
    throw new HooversionError(
      `Current branch '${plan.branch}' is not a release branch. Allowed branches: ${config.branches.join(", ")}`,
    );
  }

  if (plan.unmatchedCommits.length > 0) {
    const details = plan.unmatchedCommits
      .map((commit) => `${commit.hash.slice(0, 7)} ${commit.subject}`)
      .join("\n");
    throw new HooversionError(`Release-worthy commits could not be assigned to a package:\n${details}`);
  }

  for (const release of plan.releases) {
    if (tagExists(cwd, release.tag)) {
      throw new HooversionError(`Tag already exists: ${release.tag}`);
    }
  }
}

function releaseCommitMessage(plan: ReleasePlan): string {
  if (plan.releases.length === 1) {
    const release = plan.releases[0];
    return `chore(release): ${release.package.name} ${release.nextVersion}\n\n${release.notes}`;
  }

  const summary = plan.releases.map((release) => `${release.package.name}@${release.nextVersion}`).join(", ");
  const notes = plan.releases
    .map((release) => `# ${release.package.name} ${release.nextVersion}\n\n${release.notes}`)
    .join("\n\n");
  return `chore(release): ${summary}\n\n${notes}`;
}

function runHooks(cwd: string, hooks: string[]): void {
  for (const hook of hooks) {
    const result = runShell(hook, cwd);
    if (result.code !== 0) {
      throw new HooversionError(`Hook failed: ${hook}\n${result.stderr || result.stdout}`);
    }
  }
}
