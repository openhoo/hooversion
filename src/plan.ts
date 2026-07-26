import { getCurrentBranch, getCommits, getLatestTag, git } from "./git";
import { readManifest } from "./manifest";
import { bumpVersion, highestReleaseType } from "./semver";
import { generateReleaseNotes } from "./changelog";
import { parseCommits } from "./commit";
import { directAffectedPackages } from "./routing";
import type {
  NormalizedConfig,
  NormalizedPackageConfig,
  PackageRelease,
  ParsedCommit,
  ReleasePlan,
  ReleaseType,
} from "./types";

export function renderTag(format: string, pkg: NormalizedPackageConfig, version: string): string {
  return format.replaceAll("${name}", pkg.name).replaceAll("${version}", version);
}

export function tagPatternForPackage(config: NormalizedConfig, pkg: NormalizedPackageConfig): string {
  const format = config.packages.length === 1 ? config.tagFormat : config.independentTagFormat;
  return format.replaceAll("${name}", pkg.name).replaceAll("${version}", "[0-9]*");
}

export function tagForPackage(config: NormalizedConfig, pkg: NormalizedPackageConfig, version: string): string {
  return renderTag(config.packages.length === 1 ? config.tagFormat : config.independentTagFormat, pkg, version);
}

export function createReleasePlan(cwd: string, config: NormalizedConfig): ReleasePlan {
  const branch = getCurrentBranch(cwd);
  const sourceSha = git(cwd, ["rev-parse", "HEAD"]).trim();
  const independent = config.packages.length > 1;
  return independent
    ? createIndependentPlan(cwd, config, branch, sourceSha)
    : createSinglePackagePlan(cwd, config, branch, sourceSha);
}

function createSinglePackagePlan(
  cwd: string,
  config: NormalizedConfig,
  branch: string,
  sourceSha: string,
): ReleasePlan {
  const pkg = config.packages[0];
  const latestTag = getLatestTag(cwd, tagPatternForPackage(config, pkg));
  const commits = parseCommits(getCommits(cwd, latestTag, sourceSha)).filter((commit) => !commit.ignored);
  const releaseType = highestReleaseType(commits.map((commit) => commit.releaseType));
  const releases = releaseType ? [buildRelease(cwd, config, pkg, commits, releaseType, latestTag, false)] : [];
  return { cwd, branch, sourceSha, independent: false, releases, unmatchedCommits: [] };
}

function createIndependentPlan(cwd: string, config: NormalizedConfig, branch: string, sourceSha: string): ReleasePlan {
  const latestTags = new Map<string, string | undefined>();
  const candidateCommits = new Map<string, ParsedCommit>();

  for (const pkg of config.packages) {
    const latestTag = getLatestTag(cwd, tagPatternForPackage(config, pkg));
    latestTags.set(pkg.name, latestTag);
    const commits = parseCommits(getCommits(cwd, latestTag, sourceSha)).filter((commit) => !commit.ignored);
    for (const commit of commits) {
      candidateCommits.set(commit.hash, commit);
    }
  }

  const directAffectedByCommit = new Map<string, Set<string>>();
  const releaseTypes = new Map<string, ReleaseType>();
  const releaseCommits = new Map<string, ParsedCommit[]>();

  for (const commit of candidateCommits.values()) {
    const direct = directAffectedPackages(commit, config.packages);
    directAffectedByCommit.set(commit.hash, direct);
    if (!commit.releaseType) continue;

    for (const packageName of direct) {
      releaseTypes.set(packageName, highestReleaseType([releaseTypes.get(packageName), commit.releaseType])!);
      const commits = releaseCommits.get(packageName) ?? [];
      commits.push(commit);
      releaseCommits.set(packageName, commits);
    }
  }

  const unmatchedCommits = Array.from(candidateCommits.values()).filter(
    (commit) => commit.releaseType && (directAffectedByCommit.get(commit.hash)?.size ?? 0) === 0,
  );
  if (unmatchedCommits.length > 0) {
    return { cwd, branch, sourceSha, independent: true, releases: [], unmatchedCommits };
  }

  const dependencyTriggered = new Set<string>();
  let changed = true;
  while (changed) {
    changed = false;
    for (const pkg of config.packages) {
      if (releaseTypes.has(pkg.name)) continue;
      if (!pkg.dependencies.some((dependency) => releaseTypes.has(dependency))) continue;
      releaseTypes.set(pkg.name, "patch");
      dependencyTriggered.add(pkg.name);
      changed = true;
    }
  }

  const releases: PackageRelease[] = [];
  for (const pkg of config.packages) {
    const releaseType = releaseTypes.get(pkg.name);
    if (!releaseType) continue;
    releases.push(
      buildRelease(
        cwd,
        config,
        pkg,
        uniqueCommits(releaseCommits.get(pkg.name) ?? []),
        releaseType,
        latestTags.get(pkg.name),
        dependencyTriggered.has(pkg.name),
      ),
    );
  }

  return { cwd, branch, sourceSha, independent: true, releases, unmatchedCommits: [] };
}

function buildRelease(
  cwd: string,
  config: NormalizedConfig,
  pkg: NormalizedPackageConfig,
  commits: ParsedCommit[],
  releaseType: ReleaseType,
  latestTag: string | undefined,
  dependencyTriggered: boolean,
): PackageRelease {
  const currentVersion = readManifest(cwd, pkg).version;
  const nextVersion = bumpVersion(currentVersion, releaseType);
  const tag = tagForPackage(config, pkg, nextVersion);
  const releaseWithoutNotes = {
    package: pkg,
    currentVersion,
    nextVersion,
    releaseType,
    tag,
    latestTag,
    commits,
    notes: "",
    changelogPath: pkg.changelog,
    dependencyTriggered,
  };
  const notes = generateReleaseNotes(releaseWithoutNotes);
  return { ...releaseWithoutNotes, notes };
}

function uniqueCommits(commits: ParsedCommit[]): ParsedCommit[] {
  const seen = new Set<string>();
  return commits.filter((commit) => {
    if (seen.has(commit.hash)) return false;
    seen.add(commit.hash);
    return true;
  });
}
