import { getCurrentBranch, getCommits, getLatestTag } from "./git";
import { readManifest } from "./manifest";
import { bumpVersion, highestReleaseType, minReleaseType } from "./semver";
import { generateReleaseNotes } from "./changelog";
import { parseCommits } from "./commit";
import { directAffectedPackages, propagateDependencies } from "./routing";
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
  if (config.packages.length === 1) return "v[0-9]*";
  return config.independentTagFormat
    .replaceAll("${name}", pkg.name)
    .replaceAll("${version}", "[0-9]*");
}

export function tagForPackage(config: NormalizedConfig, pkg: NormalizedPackageConfig, version: string): string {
  return renderTag(config.packages.length === 1 ? config.tagFormat : config.independentTagFormat, pkg, version);
}

export function createReleasePlan(cwd: string, config: NormalizedConfig): ReleasePlan {
  const branch = getCurrentBranch(cwd);
  const independent = config.packages.length > 1;
  return independent ? createIndependentPlan(cwd, config, branch) : createSinglePackagePlan(cwd, config, branch);
}

function createSinglePackagePlan(cwd: string, config: NormalizedConfig, branch: string): ReleasePlan {
  const pkg = config.packages[0];
  const latestTag = getLatestTag(cwd, tagPatternForPackage(config, pkg));
  const commits = parseCommits(getCommits(cwd, latestTag)).filter((commit) => !commit.ignored);
  const releaseType = highestReleaseType(commits.map((commit) => commit.releaseType));
  const releases = releaseType ? [buildRelease(cwd, config, pkg, commits, releaseType, latestTag, false)] : [];
  return { cwd, branch, independent: false, releases, unmatchedCommits: [] };
}

function createIndependentPlan(cwd: string, config: NormalizedConfig, branch: string): ReleasePlan {
  const latestTags = new Map<string, string | undefined>();
  const parsedCommitsByPackage = new Map<string, ParsedCommit[]>();
  const candidateCommits = new Map<string, ParsedCommit>();
  const directAffectedByCommit = new Map<string, Set<string>>();
  const affectedByCommit = new Map<string, Set<string>>();

  for (const pkg of config.packages) {
    const latestTag = getLatestTag(cwd, tagPatternForPackage(config, pkg));
    latestTags.set(pkg.name, latestTag);
    const commits = parseCommits(getCommits(cwd, latestTag)).filter((commit) => !commit.ignored);
    parsedCommitsByPackage.set(pkg.name, commits);
    for (const commit of commits) {
      candidateCommits.set(commit.hash, commit);
    }
  }

  for (const commit of candidateCommits.values()) {
    const direct = directAffectedPackages(commit, config.packages);
    directAffectedByCommit.set(commit.hash, direct);
    affectedByCommit.set(commit.hash, propagateDependencies(direct, config.packages));
  }

  const unmatchedCommits = Array.from(candidateCommits.values()).filter(
    (commit) => commit.releaseType && (directAffectedByCommit.get(commit.hash)?.size ?? 0) === 0,
  );
  if (unmatchedCommits.length > 0) {
    return { cwd, branch, independent: true, releases: [], unmatchedCommits };
  }

  const releaseTypes = new Map<string, ReleaseType>();
  const releaseCommits = new Map<string, ParsedCommit[]>();
  const dependencyTriggered = new Set<string>();

  for (const pkg of config.packages) {
    const packageCommits = parsedCommitsByPackage.get(pkg.name) ?? [];
    for (const commit of packageCommits) {
      const affected = affectedByCommit.get(commit.hash) ?? new Set<string>();
      if (!affected.has(pkg.name)) continue;
      if (!commit.releaseType) continue;
      const direct = directAffectedByCommit.get(commit.hash) ?? new Set<string>();
      const typeForPackage = direct.has(pkg.name) ? commit.releaseType : "patch";
      if (!direct.has(pkg.name)) dependencyTriggered.add(pkg.name);
      releaseTypes.set(pkg.name, minReleaseType(releaseTypes.get(pkg.name), typeForPackage));
      const commits = releaseCommits.get(pkg.name) ?? [];
      commits.push(commit);
      releaseCommits.set(pkg.name, commits);
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

  return { cwd, branch, independent: true, releases, unmatchedCommits: [] };
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
