import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { HooversionError } from "./errors";
import {
  createAnnotatedTag,
  createReleaseCommit,
  ensureCleanWorkingTree,
  getCommitMessage,
  getHeadSha,
  getRefSha,
  getRemoteBranchSha,
  pushRelease,
  tagExists,
  type GitNetworkAuth,
} from "./git";
import { publishGitHubRelease } from "./github";
import { updateChangelog } from "./changelog";
import { readManifest, updateLocalDependencyVersions, updateManifestVersion } from "./manifest";
import { tagForPackage } from "./plan";
import { parseVersion } from "./semver";
import { getReleaseOutputPaths, clearReleaseOutputs, writeReleaseOutputs } from "./output";
import { runShell } from "./process";
import type { NormalizedConfig, PackageRelease, ReleasePlan, ReleaseType } from "./types";

export interface ReleaseOptions {
  dryRun?: boolean;
  push?: boolean;
  github?: boolean;
  githubToken?: string;
  gitAuth?: GitNetworkAuth;
}

export interface ReleaseExecutionResult {
  plan: ReleasePlan;
  published: boolean;
}

export async function executeRelease(
  cwd: string,
  config: NormalizedConfig,
  plan: ReleasePlan,
  options: ReleaseOptions = {},
): Promise<ReleaseExecutionResult> {
  const effectivePlan = deriveResumablePlan(cwd, config, plan) ?? plan;
  const resumable = isResumableRelease(cwd, effectivePlan);

  if (resumable) {
    verifyResumableRemote(cwd, effectivePlan, options.gitAuth);
  } else {
    verifySource(cwd, effectivePlan, options.gitAuth);
  }
  validatePlan(cwd, config, effectivePlan, resumable);

  if (options.dryRun) return { plan: effectivePlan, published: false };

  ensureCleanWorkingTree(cwd, getReleaseOutputPaths(cwd, config.outputDir), config.outputDir);
  clearReleaseOutputs(cwd, config.outputDir);

  if (effectivePlan.releases.length === 0) {
    writeReleaseOutputs(cwd, config, effectivePlan);
    return { plan: effectivePlan, published: false };
  }

  if (!resumable) {
    runHooks(cwd, config.hooks.beforeRelease);
    const releasedVersions = new Map(effectivePlan.releases.map((release) => [release.package.name, release.nextVersion]));
    for (const release of effectivePlan.releases) {
      updateManifestVersion(cwd, release.package, release.nextVersion);
    }
    updateLocalDependencyVersions(cwd, config.packages, releasedVersions);
    for (const release of effectivePlan.releases) {
      updateChangelog(cwd, release);
    }

    runHooks(cwd, config.hooks.afterVersion);

    createReleaseCommit(cwd, releaseCommitMessage(effectivePlan));
    for (const release of effectivePlan.releases) {
      createAnnotatedTag(cwd, release.tag, `${release.package.name} ${release.nextVersion}`);
    }
  }

  const shouldPush = options.push ?? config.push;
  if (shouldPush) {
    pushRelease(cwd, effectivePlan.branch, effectivePlan.releases.map((release) => release.tag), options.gitAuth);
  }

  mkdirSync(join(cwd, config.outputDir), { recursive: true });
  if (options.github ?? true) {
    for (const release of effectivePlan.releases) {
      await publishGitHubRelease(cwd, config, release, { token: options.githubToken });
    }
  }

  writeReleaseOutputs(cwd, config, effectivePlan);
  runHooks(cwd, config.hooks.afterRelease);
  return { plan: effectivePlan, published: true };
}

export function validatePlan(cwd: string, config: NormalizedConfig, plan: ReleasePlan, resumable = isResumableRelease(cwd, plan)): void {
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
    if (resumable) continue;
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
function deriveResumablePlan(cwd: string, config: NormalizedConfig, plan: ReleasePlan): ReleasePlan | undefined {
  if (plan.releases.length > 0) return undefined;
  const head = getHeadSha(cwd);
  const sourceSha = getRefSha(cwd, "HEAD^");
  if (!sourceSha) return undefined;

  const taggedPackages = config.packages
    .map((pkg) => {
      const nextVersion = readManifest(cwd, pkg).version;
      const tag = tagForPackage(config, pkg, nextVersion);
      return getRefSha(cwd, `refs/tags/${tag}`) === head ? { pkg, nextVersion, tag } : undefined;
    })
    .filter((release): release is { pkg: (typeof config.packages)[number]; nextVersion: string; tag: string } =>
      Boolean(release),
    );
  if (taggedPackages.length === 0) return undefined;

  const message = getCommitMessage(cwd);
  const separator = message.indexOf("\n");
  const subject = separator < 0 ? message : message.slice(0, separator);
  const body = separator < 0 ? "" : message.slice(separator).trim();
  const expectedSubject =
    taggedPackages.length === 1
      ? `chore(release): ${taggedPackages[0].pkg.name} ${taggedPackages[0].nextVersion}`
      : `chore(release): ${taggedPackages.map(({ pkg, nextVersion }) => `${pkg.name}@${nextVersion}`).join(", ")}`;
  if (subject !== expectedSubject) return undefined;

  const transitions = taggedPackages.map(({ pkg, nextVersion }) => inferReleaseTransition(cwd, config, pkg, nextVersion));
  if (transitions.some((transition) => !transition)) return undefined;
  const releases: PackageRelease[] = taggedPackages.map(({ pkg, nextVersion, tag }, index) => {
    const transition = transitions[index]!;
    const marker = `# ${pkg.name} ${nextVersion}\n\n`;
    const markerStart = body.indexOf(marker);
    const notes =
      taggedPackages.length === 1
        ? body
        : markerStart < 0
          ? ""
          : body.slice(markerStart + marker.length).split(/\n\n# /, 1)[0];
    return {
      package: pkg,
      currentVersion: transition.currentVersion,
      nextVersion,
      releaseType: transition.releaseType,
      tag,
      commits: [],
      notes,
      changelogPath: pkg.changelog,
      dependencyTriggered: false,
    };
  });
  const reconstructed: ReleasePlan = {
    ...plan,
    sourceSha,
    releases,
    unmatchedCommits: [],
  };
  return getCommitMessage(cwd) === releaseCommitMessage(reconstructed) ? reconstructed : undefined;
}

function inferReleaseTransition(
  cwd: string,
  config: NormalizedConfig,
  pkg: (typeof config.packages)[number],
  nextVersion: string,
): { currentVersion: string; releaseType: ReleaseType } | undefined {
  const parts = parseVersion(nextVersion);
  const candidates: Array<[ReleaseType, string]> = [
    ["major", `${parts.major - 1}.0.0`],
    ["minor", `${parts.major}.${parts.minor - 1}.0`],
    ["patch", `${parts.major}.${parts.minor}.${parts.patch - 1}`],
  ];
  for (const [releaseType, currentVersion] of candidates) {
    if (parts.major < 1 && releaseType === "major") continue;
    if (parts.minor < 1 && releaseType === "minor") continue;
    if (parts.patch < 1 && releaseType === "patch") continue;
    const tag = tagForPackage(config, pkg, currentVersion);
    if (getRefSha(cwd, `refs/tags/${tag}`)) return { currentVersion, releaseType };
  }
  return undefined;
}

function isResumableRelease(cwd: string, plan: ReleasePlan): boolean {
  if (plan.releases.length === 0) return false;
  const head = getHeadSha(cwd);
  if (head === plan.sourceSha) return false;
  if (getRefSha(cwd, "HEAD^") !== plan.sourceSha) return false;
  if (getCommitMessage(cwd) !== releaseCommitMessage(plan)) return false;
  return plan.releases.every((release) => getRefSha(cwd, `refs/tags/${release.tag}`) === head);
}

function verifySource(cwd: string, plan: ReleasePlan, gitAuth?: GitNetworkAuth): void {
  const head = getHeadSha(cwd);
  if (head !== plan.sourceSha) {
    throw new HooversionError(`Release source changed locally: expected ${plan.sourceSha}, found ${head}.`);
  }
  const remote = getRemoteBranchSha(cwd, plan.branch, gitAuth);

  if (remote !== undefined && remote !== plan.sourceSha) {
    throw new HooversionError(`Release source changed remotely: expected ${plan.sourceSha}, found ${remote || "missing"}.`);
  }
}

function verifyResumableRemote(cwd: string, plan: ReleasePlan, gitAuth?: GitNetworkAuth): void {
  const head = getHeadSha(cwd);
  const remote = getRemoteBranchSha(cwd, plan.branch, gitAuth);
  if (remote !== undefined && remote !== head && remote !== plan.sourceSha) {
    throw new HooversionError(`Release resume found remote drift: expected ${head}, found ${remote || "missing"}.`);
  }
}
