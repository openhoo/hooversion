export type PackageType = "node" | "rust" | "python" | "version-file";

export type ReleaseType = "major" | "minor" | "patch";

export type CommitType =
  | "feat"
  | "fix"
  | "perf"
  | "docs"
  | "style"
  | "refactor"
  | "test"
  | "build"
  | "ci"
  | "chore"
  | "revert"
  | string;

export interface PackageConfig {
  name: string;
  path: string;
  type: PackageType;
  manifest?: string;
  changelog?: string;
  scopes?: string[];
  dependencies?: string[];
  assets?: string[];
}

export interface HookConfig {
  beforeRelease?: string[];
  afterVersion?: string[];
  afterRelease?: string[];
}

export interface GitHubConfig {
  releases?: boolean;
  repository?: string;
  apiUrl?: string;
}

export interface HooversionConfig {
  branches?: string[];
  tagFormat?: string;
  independentTagFormat?: string;
  packages: PackageConfig[];
  hooks?: HookConfig;
  github?: GitHubConfig | false;
  outputDir?: string;
  push?: boolean;
}

export interface NormalizedPackageConfig extends PackageConfig {
  path: string;
  manifest: string;
  changelog: string;
  scopes: string[];
  dependencies: string[];
  assets: string[];
}

export interface NormalizedConfig extends HooversionConfig {
  branches: string[];
  tagFormat: string;
  independentTagFormat: string;
  packages: NormalizedPackageConfig[];
  hooks: Required<HookConfig>;
  github: Required<GitHubConfig> | false;
  outputDir: string;
  push: boolean;
}

export interface RawCommit {
  hash: string;
  subject: string;
  body: string;
  files: string[];
}

export interface ParsedCommit extends RawCommit {
  type: CommitType;
  scope?: string;
  description: string;
  breaking: boolean;
  releaseType?: ReleaseType;
  ignored: boolean;
}

export interface CommitLintIssue {
  hash?: string;
  subject: string;
  message: string;
}

export interface PackageRelease {
  package: NormalizedPackageConfig;
  currentVersion: string;
  nextVersion: string;
  releaseType: ReleaseType;
  tag: string;
  latestTag?: string;
  commits: ParsedCommit[];
  notes: string;
  changelogPath: string;
  dependencyTriggered: boolean;
}

export interface ReleasePlan {
  cwd: string;
  branch: string;
  independent: boolean;
  releases: PackageRelease[];
  unmatchedCommits: ParsedCommit[];
}

export interface CommandResult {
  code: number;
  stdout: string;
  stderr: string;
}
