// Package release — resume derivation and drift checks mirroring the
// lower half of src/release.ts.
package release

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/manifest"
	"github.com/openhoo/hooversion/internal/plan"
	"github.com/openhoo/hooversion/internal/semver"
	"github.com/openhoo/hooversion/internal/types"
)

// DeriveResumable reconstructs a ReleasePlan from an already-executed release
// commit (manifest already bumped). The release tag may either already point at
// HEAD or still be absent after a protected-branch release PR was squash-merged.
// It returns nil when the repository state does not prove a resumable release:
// no HEAD^, wrong commit subject, unresolvable version transition, or a
// byte-exact commit-message mismatch.
// Callers only invoke it for fresh plans with zero releases.
func DeriveResumable(cwd string, config *types.NormalizedConfig) (*types.ReleasePlan, error) {
	head, err := git.HeadSha(cwd)
	if err != nil {
		return nil, err
	}
	sourceSha, err := git.RefSha(cwd, "HEAD^")
	if err != nil {
		return nil, err
	}
	if sourceSha == "" {
		return nil, nil
	}

	type releasePackage struct {
		pkg         types.NormalizedPackageConfig
		nextVersion string
		tag         string
	}
	message, err := git.CommitMessage(cwd, "HEAD")
	if err != nil {
		return nil, err
	}
	subject, body := splitSubjectBody(message)
	const prefix = "chore(release): "
	if !strings.HasPrefix(subject, prefix) {
		return nil, nil
	}

	versions := make(map[string]string, len(config.Packages))
	packages := make(map[string]types.NormalizedPackageConfig, len(config.Packages))
	for _, pkg := range config.Packages {
		nextVersion, err := readManifestVersion(cwd, pkg)
		if err != nil {
			return nil, err
		}
		versions[pkg.Name] = nextVersion
		packages[pkg.Name] = pkg
	}

	var selected []releasePackage
	releaseSubject := strings.TrimPrefix(subject, prefix)
	if name, version, ok := strings.Cut(releaseSubject, " "); ok && !strings.Contains(name, "@") {
		pkg, exists := packages[name]
		if !exists || versions[name] != version {
			return nil, nil
		}
		selected = append(selected, releasePackage{
			pkg: pkg, nextVersion: version, tag: plan.TagFor(config, pkg, version),
		})
	} else {
		for _, part := range strings.Split(releaseSubject, ", ") {
			name, version, ok := strings.Cut(part, "@")
			pkg, exists := packages[name]
			if !ok || !exists || versions[name] != version {
				return nil, nil
			}
			selected = append(selected, releasePackage{
				pkg: pkg, nextVersion: version, tag: plan.TagFor(config, pkg, version),
			})
		}
		if len(selected) < 2 {
			return nil, nil
		}
	}

	for _, entry := range selected {
		ref, err := git.RefSha(cwd, "refs/tags/"+entry.tag)
		if err != nil {
			return nil, err
		}
		if ref != "" && ref != head {
			return nil, errors.New(
				"Release resume found tag drift: expected %s at %s, found %s.", entry.tag, head, ref)
		}
	}

	releases := make([]types.PackageRelease, 0, len(selected))
	for _, entry := range selected {
		currentVersion, releaseType, ok := inferReleaseTransition(cwd, config, entry.pkg, entry.nextVersion)
		if !ok {
			return nil, nil
		}
		notes := body
		if len(selected) > 1 {
			marker := fmt.Sprintf("# %s %s\n\n", entry.pkg.Name, entry.nextVersion)
			start := strings.Index(body, marker)
			if start < 0 {
				notes = ""
			} else {
				rest := body[start+len(marker):]
				if end := strings.Index(rest, "\n\n# "); end >= 0 {
					rest = rest[:end]
				}
				notes = rest
			}
		}
		releases = append(releases, types.PackageRelease{
			Package:             entry.pkg,
			CurrentVersion:      currentVersion,
			NextVersion:         entry.nextVersion,
			ReleaseType:         releaseType,
			LatestTag:           "",
			Tag:                 entry.tag,
			Commits:             nil,
			Notes:               notes,
			DependencyTriggered: false,
		})
	}

	reconstructed := &types.ReleasePlan{
		Branch:      gitBranchOf(cwd),
		SourceSha:   sourceSha,
		Independent: len(config.Packages) > 1,
		Releases:    releases,
	}
	headMessage, err := git.CommitMessage(cwd, "HEAD")
	if err != nil {
		return nil, err
	}
	if headMessage != CommitMessage(reconstructed) {
		return nil, nil
	}
	return reconstructed, nil
}

// inferReleaseTransition probes candidate previous versions (major, minor,
// patch) whose tags exist to recover the bump that produced nextVersion.
func inferReleaseTransition(
	cwd string,
	config *types.NormalizedConfig,
	pkg types.NormalizedPackageConfig,
	nextVersion string,
) (string, types.ReleaseType, bool) {
	parsed, err := semver.Parse(nextVersion)
	if err != nil {
		return "", "", false
	}
	candidates := []struct {
		releaseType    types.ReleaseType
		currentVersion string
	}{
		{types.Major, fmt.Sprintf("%d.0.0", parsed.Major-1)},
		{types.Minor, fmt.Sprintf("%d.%d.0", parsed.Major, parsed.Minor-1)},
		{types.Patch, fmt.Sprintf("%d.%d.%d", parsed.Major, parsed.Minor, parsed.Patch-1)},
	}
	for _, candidate := range candidates {
		switch candidate.releaseType {
		case types.Major:
			if parsed.Major < 1 {
				continue
			}
		case types.Minor:
			if parsed.Minor < 1 {
				continue
			}
		case types.Patch:
			if parsed.Patch < 1 {
				continue
			}
		}
		tag := plan.TagFor(config, pkg, candidate.currentVersion)
		ref, err := git.RefSha(cwd, "refs/tags/"+tag)
		if err != nil {
			return "", "", false
		}
		if ref != "" {
			return candidate.currentVersion, candidate.releaseType, true
		}
	}
	return "", "", false
}

// isResumableRelease reports whether HEAD is exactly the release commit of a
// prior partial run: one commit ahead of plan.SourceSha, carrying the exact
// release message, with every planned tag either absent or pointing at HEAD.
func isResumableRelease(cwd string, effective *types.ReleasePlan) bool {
	if len(effective.Releases) == 0 {
		return false
	}
	head, err := git.HeadSha(cwd)
	if err != nil || head == effective.SourceSha {
		return false
	}
	parent, err := git.RefSha(cwd, "HEAD^")
	if err != nil || parent != effective.SourceSha {
		return false
	}
	message, err := git.CommitMessage(cwd, "HEAD")
	if err != nil || message != CommitMessage(effective) {
		return false
	}
	for _, release := range effective.Releases {
		ref, err := git.RefSha(cwd, "refs/tags/"+release.Tag)
		if err != nil || (ref != "" && ref != head) {
			return false
		}
	}
	return true
}

// verifySource blocks when the local checkout moved past the planned source
// or the remote branch drifted from it. The remote lookup is tri-state:
// ErrNoRemote skips the check, "" means the branch is missing remotely.
func verifySource(cwd string, effective *types.ReleasePlan) error {
	head, err := git.HeadSha(cwd)
	if err != nil {
		return err
	}
	if head != effective.SourceSha {
		return errors.New("Release source changed locally: expected %s, found %s.", effective.SourceSha, head)
	}
	remote, err := git.RemoteBranchSha(cwd, effective.Branch)
	if err == git.ErrNoRemote {
		return nil
	}
	if err != nil {
		return err
	}
	if remote != effective.SourceSha {
		found := remote
		if found == "" {
			found = "missing"
		}
		return errors.New("Release source changed remotely: expected %s, found %s.", effective.SourceSha, found)
	}
	return nil
}

// verifyResumableRemote accepts a remote sitting at either HEAD (fully pushed)
// or SourceSha (push failed before anything landed); anything else drifts.
func verifyResumableRemote(cwd string, effective *types.ReleasePlan) error {
	head, err := git.HeadSha(cwd)
	if err != nil {
		return err
	}
	remote, err := git.RemoteBranchSha(cwd, effective.Branch)
	if err == git.ErrNoRemote {
		return nil
	}
	if err != nil {
		return err
	}
	if remote != head && remote != effective.SourceSha {
		found := remote
		if found == "" {
			found = "missing"
		}
		return errors.New("Release resume found remote drift: expected %s, found %s.", head, found)
	}
	return nil
}

func splitSubjectBody(message string) (string, string) {
	separator := strings.Index(message, "\n")
	if separator < 0 {
		return message, ""
	}
	return message[:separator], strings.TrimSpace(message[separator:])
}
func readManifestVersion(cwd string, pkg types.NormalizedPackageConfig) (string, error) {
	pkg.Manifest = filepath.Join(cwd, pkg.Manifest)
	_, version, err := manifest.Read(pkg)
	return version, err
}

func gitBranchOf(cwd string) string {
	branch, err := git.CurrentBranch(cwd)
	if err != nil {
		return ""
	}
	return branch
}
