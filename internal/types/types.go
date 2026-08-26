// Package types holds the shared data model of Hooversion. It mirrors the
// shapes of src/types.ts so behavior ports stay 1:1. Structs here are the
// single source of truth; no package may redefine them.
package types

// PackageType is a supported manifest ecosystem.
type PackageType string

const (
	PackageNode        PackageType = "node"
	PackageRust        PackageType = "rust"
	PackagePython      PackageType = "python"
	PackageVersionFile PackageType = "version-file"
)

// ReleaseType is a semantic-release bump class.
type ReleaseType string

const (
	Major ReleaseType = "major"
	Minor ReleaseType = "minor"
	Patch ReleaseType = "patch"
	None  ReleaseType = "none"
)

// RawCommit is a commit as collected from git, before policy parsing.
type RawCommit struct {
	Hash    string
	Subject string
	Body    string
	Files   []string
}

// ParsedCommit is a Conventional-Commit-parsed commit.
//
// Type is "" when the header does not match the conventional pattern.
// Scope is "" when absent. Breaking reports "!" or a BREAKING CHANGE footer;
// BreakingDescription carries the footer text ("" for "!" alone).
type ParsedCommit struct {
	Hash                string
	Subject             string
	Body                string
	Files               []string
	Type                string
	Scope               string
	Breaking            bool
	BreakingDescription string
	Description         string
	Conforms            bool // header matched the conventional pattern
}

// CommitLintIssue is one lint finding; Message is the exact user-facing text.
type CommitLintIssue struct {
	Message string
}

// CommitPolicy customizes linting and release mapping (README "Commit-policy API").
type CommitPolicy struct {
	AllowedTypes []string               // nil/empty = all types allowed
	ReleaseTypes map[string]ReleaseType // extends/overrides default bump map
}

// --- Raw config file shape (YAML or JSON) ---------------------------------

// Config is the on-disk hooversion.yaml document before normalization.
type Config struct {
	Branches             []string        `yaml:"branches"              json:"branches,omitempty"`
	TagFormat            string          `yaml:"tagFormat"             json:"tagFormat,omitempty"`
	IndependentTagFormat string          `yaml:"independentTagFormat"  json:"independentTagFormat,omitempty"`
	Packages             []PackageConfig `yaml:"packages"              json:"packages"`
	Hooks                HookConfig      `yaml:"hooks"                 json:"hooks,omitempty"`
	GitHub               *GitHubConfig   `yaml:"github"                json:"github,omitempty"`
	OutputDir            string          `yaml:"outputDir"             json:"outputDir,omitempty"`
	Push                 *bool           `yaml:"push"                  json:"push,omitempty"`
}

// PackageConfig is one releasable package in the raw config.
type PackageConfig struct {
	Name         string      `yaml:"name"         json:"name,omitempty"`
	Path         string      `yaml:"path"         json:"path,omitempty"`
	Type         PackageType `yaml:"type"      json:"type"`
	Manifest     string      `yaml:"manifest"     json:"manifest,omitempty"`
	Changelog    string      `yaml:"changelog"    json:"changelog,omitempty"`
	Scopes       []string    `yaml:"scopes"       json:"scopes,omitempty"`
	Dependencies []string    `yaml:"dependencies" json:"dependencies,omitempty"`
	Assets       []string    `yaml:"assets"       json:"assets,omitempty"`
}

// HookConfig lists shell commands per pipeline stage.
type HookConfig struct {
	BeforeRelease []string `yaml:"beforeRelease" json:"beforeRelease,omitempty"`
	AfterVersion  []string `yaml:"afterVersion"  json:"afterVersion,omitempty"`
	AfterRelease  []string `yaml:"afterRelease"  json:"afterRelease,omitempty"`
}

// GitHubConfig is the raw github section; Enabled=false fully disables GitHub
// integration (the TS `github: false` form).
type GitHubConfig struct {
	Enabled    *bool  `yaml:"enabled"    json:"enabled,omitempty"`  // nil = true
	Releases   *bool  `yaml:"releases"   json:"releases,omitempty"` // nil = true
	Repository string `yaml:"repository" json:"repository,omitempty"`
	ApiUrl     string `yaml:"apiUrl"     json:"apiUrl,omitempty"`
}

// --- Normalized config -----------------------------------------------------

// NormalizedPackageConfig is a validated, defaulted package entry.
type NormalizedPackageConfig struct {
	Name         string
	Path         string // cwd-relative, normalized, never ".."-escaping
	Type         PackageType
	Manifest     string
	Changelog    string
	Scopes       []string
	Dependencies []string
	Assets       []string
}

// GitHubSettings is the resolved github section.
type GitHubSettings struct {
	Enabled    bool
	Releases   bool
	Repository string // "" = derive from origin remote
	ApiUrl     string // default https://api.github.com
}

// NormalizedConfig is the fully defaulted, validated configuration.
type NormalizedConfig struct {
	Branches             []string                  // default ["main"]
	TagFormat            string                    // default "v${version}"
	IndependentTagFormat string                    // default "${name}@v${version}"
	Packages             []NormalizedPackageConfig // required non-empty
	Hooks                HookConfig
	GitHub               GitHubSettings
	OutputDir            string // default ".hooversion"
	Push                 bool   // default true
}

// --- Planning --------------------------------------------------------------

// PackageRelease is one planned package version bump.
type PackageRelease struct {
	Package             NormalizedPackageConfig
	CurrentVersion      string
	NextVersion         string
	ReleaseType         ReleaseType
	DependencyTriggered bool
	LatestTag           string // "" when the package has no tag yet
	Tag                 string
	Commits             []ParsedCommit
	Notes               string // rendered release notes body
}

// ReleasePlan binds releases to the checked-out source SHA.
type ReleasePlan struct {
	Branch           string
	SourceSha        string
	Independent      bool // len(config.Packages) > 1
	Releases         []PackageRelease
	UnmatchedCommits []ParsedCommit
}

// GitAuth carries extra environment variables merged into git subprocesses
// (e.g. credential helpers or VERSIONHOO_GIT_TOKEN-style tokens).
type GitAuth map[string]string
