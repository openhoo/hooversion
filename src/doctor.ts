import { getCurrentBranch, getLatestTag, git, isGitRepository } from "./git";
import { readManifest } from "./manifest";
import { tagPatternForPackage } from "./plan";
import type { NormalizedConfig } from "./types";

export interface DoctorResult {
  errors: string[];
  warnings: string[];
  info: string[];
}

export function runDoctor(cwd: string, config: NormalizedConfig): DoctorResult {
  const result: DoctorResult = { errors: [], warnings: [], info: [] };

  if (config.branches.length === 0 || config.branches.some((branch) => !branch.trim())) {
    result.errors.push("Config must define at least one non-empty release branch.");
  }
  if (config.packages.length === 0) {
    result.errors.push("Config must define at least one package.");
  }
  if (result.errors.length > 0) return result;

  if (!isGitRepository(cwd)) {
    result.errors.push("Current directory is not a git repository.");
    return result;
  }

  if (!git(cwd, ["rev-parse", "--verify", "--quiet", "HEAD^{commit}"], true).trim()) {
    result.errors.push("Repository has no resolvable HEAD commit.");
    return result;
  }

  const branch = getCurrentBranch(cwd);
  if (!config.branches.includes(branch)) {
    result.warnings.push(`Current branch '${branch}' is not a configured release branch.`);
  } else {
    result.info.push(`Release branch: ${branch}`);
  }

  for (const pkg of config.packages) {
    const manifest = readManifest(cwd, pkg);
    result.info.push(`${pkg.name}: manifest version ${manifest.version}`);
    const latestTag = getLatestTag(cwd, tagPatternForPackage(config, pkg));
    if (!latestTag) {
      result.warnings.push(`${pkg.name}: no release tag found; first release will use full reachable history.`);
      continue;
    }

    const tagVersion = extractTagVersion(latestTag);
    result.info.push(`${pkg.name}: latest tag ${latestTag}`);
    if (tagVersion && tagVersion !== manifest.version) {
      result.warnings.push(
        `${pkg.name}: manifest version ${manifest.version} differs from latest tag version ${tagVersion}.`,
      );
    }
  }

  if (config.github !== false && config.github.releases && !process.env.GITHUB_TOKEN && !process.env.GH_TOKEN) {
    result.warnings.push("GITHUB_TOKEN or GH_TOKEN is not set; `release` cannot create GitHub releases.");
  }

  return result;
}

function extractTagVersion(tag: string): string | undefined {
  return /(?:^|@)v(\d+\.\d+\.\d+(?:[-+][^\s]+)?)$/.exec(tag)?.[1];
}
