// Package plan derives the release plan from git history, routing commits to
// packages and propagating dependent bumps. It mirrors src/plan.ts.
package plan

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/openhoo/hooversion/internal/changelog"
	"github.com/openhoo/hooversion/internal/commit"
	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/manifest"
	"github.com/openhoo/hooversion/internal/routing"
	"github.com/openhoo/hooversion/internal/semver"
	"github.com/openhoo/hooversion/internal/types"
)

// renderTag substitutes ${name} and ${version} placeholders in a tag format.
func renderTag(format, name, version string) string {
	format = strings.ReplaceAll(format, "${name}", name)
	return strings.ReplaceAll(format, "${version}", version)
}

func tagFormatFor(config *types.NormalizedConfig) string {
	if len(config.Packages) > 1 {
		return config.IndependentTagFormat
	}
	return config.TagFormat
}

// TagPatternFor returns the describe --match glob used to locate the latest
// release baseline for pkg: the single-package format when exactly one
// package is configured, the independent format otherwise.
func TagPatternFor(config *types.NormalizedConfig, pkg types.NormalizedPackageConfig) string {
	return renderTag(tagFormatFor(config), pkg.Name, "[0-9]*")
}

// TagFor renders the concrete release tag for pkg at version.
func TagFor(config *types.NormalizedConfig, pkg types.NormalizedPackageConfig, version string) string {
	return renderTag(tagFormatFor(config), pkg.Name, version)
}

// CreatePlan collects commits since each package's latest matching tag,
// filters ignored subjects, routes them to packages, derives release types,
// and propagates dependent patch bumps to a fixed point. branch is supplied
// by the caller; policy extends/overrides the default bump map (nil policy =
// defaults only).
func CreatePlan(cwd string, config *types.NormalizedConfig, branch string, policy *types.CommitPolicy) (*types.ReleasePlan, error) {
	return CreatePlanWithEnv(cwd, config, branch, policy, nil)
}

// CreatePlanWithEnv derives a plan using the supplied child environment.
func CreatePlanWithEnv(cwd string, config *types.NormalizedConfig, branch string, policy *types.CommitPolicy, baseEnv []string) (*types.ReleasePlan, error) {
	sourceSha, err := git.HeadShaWithEnv(cwd, baseEnv)
	if err != nil {
		return nil, err
	}

	rules := releaseRules(policy)
	if len(config.Packages) == 1 {
		return createSinglePackagePlan(cwd, config, branch, sourceSha, rules, policy, baseEnv)
	}
	return createIndependentPlan(cwd, config, branch, sourceSha, rules, policy, baseEnv)
}

func createSinglePackagePlan(
	cwd string,
	config *types.NormalizedConfig,
	branch string,
	sourceSha string,
	rules map[string]types.ReleaseType,
	policy *types.CommitPolicy,
	baseEnv []string,
) (*types.ReleasePlan, error) {
	pkg := config.Packages[0]
	latestTag, err := latestTagOrEmpty(cwd, TagPatternFor(config, pkg), baseEnv)
	if err != nil {
		return nil, err
	}
	commits, err := collectCommits(cwd, latestTag, sourceSha, policy, baseEnv)
	if err != nil {
		return nil, err
	}
	releaseType := semver.Highest(releaseTypesOver(commits, rules)...)
	releases := []types.PackageRelease{}
	if releaseType != "" {
		release, err := buildRelease(cwd, config, pkg, commits, releaseType, latestTag, false)
		if err != nil {
			return nil, err
		}
		releases = append(releases, release)
	}
	return &types.ReleasePlan{
		Branch:      branch,
		SourceSha:   sourceSha,
		Independent: false,
		Releases:    releases,
	}, nil
}

func createIndependentPlan(
	cwd string,
	config *types.NormalizedConfig,
	branch string,
	sourceSha string,
	rules map[string]types.ReleaseType,
	policy *types.CommitPolicy,
	baseEnv []string,
) (*types.ReleasePlan, error) {
	latestTags := make(map[string]string)
	eligibleByPackage := make(map[string]map[string]bool)
	var candidateOrder []string
	candidateCommits := make(map[string]types.ParsedCommit)

	for _, pkg := range config.Packages {
		latestTag, err := latestTagOrEmpty(cwd, TagPatternFor(config, pkg), baseEnv)
		if err != nil {
			return nil, err
		}
		latestTags[pkg.Name] = latestTag
		commits, err := collectCommits(cwd, latestTag, sourceSha, policy, baseEnv)
		if err != nil {
			return nil, err
		}
		eligible := make(map[string]bool, len(commits))
		for _, parsed := range commits {
			eligible[parsed.Hash] = true
			if _, seen := candidateCommits[parsed.Hash]; !seen {
				candidateOrder = append(candidateOrder, parsed.Hash)
			}
			candidateCommits[parsed.Hash] = parsed
		}
		eligibleByPackage[pkg.Name] = eligible
	}

	candidates := make([]types.ParsedCommit, 0, len(candidateOrder))
	for _, hash := range candidateOrder {
		candidates = append(candidates, candidateCommits[hash])
	}

	affected := routing.DirectAffected(config, candidates)
	// Reverse index: only commits eligible in the package's own post-tag window
	// may be attributed to that package.
	directPackages := make(map[string][]string)
	inAnyDirect := make(map[string]bool)
	for _, pkg := range config.Packages {
		eligible := eligibleByPackage[pkg.Name]
		for _, parsed := range affected[pkg.Name] {
			inAnyDirect[parsed.Hash] = true
			if !eligible[parsed.Hash] {
				continue
			}
			directPackages[parsed.Hash] = append(directPackages[parsed.Hash], pkg.Name)
		}
	}

	var unmatchedCommits []types.ParsedCommit
	for _, parsed := range candidates {
		if releaseTypeOf(parsed, rules) != "" && !inAnyDirect[parsed.Hash] {
			unmatchedCommits = append(unmatchedCommits, parsed)
		}
	}
	if len(unmatchedCommits) > 0 {
		return &types.ReleasePlan{
			Branch:           branch,
			SourceSha:        sourceSha,
			Independent:      true,
			UnmatchedCommits: unmatchedCommits,
		}, nil
	}

	releaseTypes := make(map[string]types.ReleaseType)
	releaseCommits := make(map[string][]types.ParsedCommit)
	for _, parsed := range candidates {
		releaseType := releaseTypeOf(parsed, rules)
		if releaseType == "" {
			continue
		}
		for _, packageName := range directPackages[parsed.Hash] {
			releaseTypes[packageName] = semver.Highest(releaseTypes[packageName], releaseType)
			releaseCommits[packageName] = append(releaseCommits[packageName], parsed)
		}
	}

	dependencyTriggered := make(map[string]bool)
	changed := true
	for changed {
		changed = false
		for _, pkg := range config.Packages {
			if _, planned := releaseTypes[pkg.Name]; planned {
				continue
			}
			dependsOnPlanned := false
			for _, dependency := range pkg.Dependencies {
				if _, planned := releaseTypes[dependency]; planned {
					dependsOnPlanned = true
					break
				}
			}
			if !dependsOnPlanned {
				continue
			}
			releaseTypes[pkg.Name] = types.Patch
			dependencyTriggered[pkg.Name] = true
			changed = true
		}
	}

	releases := []types.PackageRelease{}
	for _, pkg := range config.Packages {
		releaseType, planned := releaseTypes[pkg.Name]
		if !planned {
			continue
		}
		release, err := buildRelease(cwd, config, pkg, uniqueCommits(releaseCommits[pkg.Name]), releaseType,
			latestTags[pkg.Name], dependencyTriggered[pkg.Name])
		if err != nil {
			return nil, err
		}
		releases = append(releases, release)
	}

	return &types.ReleasePlan{
		Branch:      branch,
		SourceSha:   sourceSha,
		Independent: true,
		Releases:    releases,
	}, nil
}

func buildRelease(
	cwd string,
	config *types.NormalizedConfig,
	pkg types.NormalizedPackageConfig,
	commits []types.ParsedCommit,
	releaseType types.ReleaseType,
	latestTag string,
	dependencyTriggered bool,
) (types.PackageRelease, error) {
	currentVersion, err := readManifestVersion(cwd, pkg)
	if err != nil {
		return types.PackageRelease{}, err
	}
	parsedVersion, err := semver.Parse(currentVersion)
	if err != nil {
		return types.PackageRelease{}, err
	}
	nextVersion := semver.Bump(parsedVersion, releaseType).String()
	tag := TagFor(config, pkg, nextVersion)
	notes := changelog.GenerateNotes(nextVersion, time.Now(), commits)
	return types.PackageRelease{
		Package:             pkg,
		CurrentVersion:      currentVersion,
		NextVersion:         nextVersion,
		ReleaseType:         releaseType,
		DependencyTriggered: dependencyTriggered,
		LatestTag:           latestTag,
		Tag:                 tag,
		Commits:             commits,
		Notes:               notes,
	}, nil
}

func uniqueCommits(commits []types.ParsedCommit) []types.ParsedCommit {
	seen := make(map[string]bool)
	result := make([]types.ParsedCommit, 0, len(commits))
	for _, parsed := range commits {
		if seen[parsed.Hash] {
			continue
		}
		seen[parsed.Hash] = true
		result = append(result, parsed)
	}
	return result
}

// --- helpers ---------------------------------------------------------------

// latestTagOrEmpty maps the ErrNoTag sentinel to an empty baseline so a
// first release plans from full reachable history.
func latestTagOrEmpty(cwd, pattern string, baseEnv []string) (string, error) {
	tag, err := git.LatestTagWithEnv(cwd, pattern, baseEnv)
	if err == git.ErrNoTag {
		return "", nil
	}
	return tag, err
}

// collectCommits gathers raw commits since from (whole history when empty),
// drops ignored subjects, and parses what remains under policy.
func collectCommits(cwd, from, to string, policy *types.CommitPolicy, baseEnv []string) ([]types.ParsedCommit, error) {
	raws, err := git.CommitsWithEnv(cwd, from, to, baseEnv)
	if err != nil {
		return nil, err
	}
	parsed := make([]types.ParsedCommit, 0, len(raws))
	for _, raw := range raws {
		if commit.IsIgnoredSubject(raw.Subject) {
			continue
		}
		parsed = append(parsed, commit.Parse(raw, policy))
	}
	return parsed, nil
}

func releaseRules(policy *types.CommitPolicy) map[string]types.ReleaseType {
	rules := map[string]types.ReleaseType{
		"feat": types.Minor,
		"fix":  types.Patch,
		"perf": types.Patch,
	}
	if policy != nil {
		for key, value := range policy.ReleaseTypes {
			rules[key] = value
		}
	}
	return rules
}

func releaseTypeOf(parsed types.ParsedCommit, rules map[string]types.ReleaseType) types.ReleaseType {
	if parsed.Breaking {
		return types.Major
	}
	return rules[parsed.Type]
}

func releaseTypesOver(commits []types.ParsedCommit, rules map[string]types.ReleaseType) []types.ReleaseType {
	result := make([]types.ReleaseType, 0, len(commits))
	for _, parsed := range commits {
		result = append(result, releaseTypeOf(parsed, rules))
	}
	return result
}

// readManifestVersion reads the manifest as given, joining cwd here so the
// manifest package keeps receiving the cwd-relative path from the config.
func readManifestVersion(cwd string, pkg types.NormalizedPackageConfig) (string, error) {
	pkg.Manifest = filepath.Join(cwd, pkg.Manifest)
	_, version, err := manifest.Read(pkg)
	return version, err
}
