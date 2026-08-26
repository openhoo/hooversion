// Package doctor runs the read-only repository and configuration health
// checks reported by `hooversion doctor`. Behavior mirrors src/doctor.ts 1:1,
// including check order, early-return groups, and exact message strings. The
// `ok:` / `warning:` / `error:` prefixes are applied by the CLI layer, so the
// messages here carry no prefixes.
//
// Unlike the TS source, which throws on unreadable manifests or git failures,
// those failures surface as the returned error; all advisory findings are
// reported through the DoctorResult slices in TS order.
package doctor

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/manifest"
	"github.com/openhoo/hooversion/internal/plan"
	"github.com/openhoo/hooversion/internal/types"
)

// DoctorResult groups the doctor findings exactly like the TS DoctorResult.
type DoctorResult struct {
	Errors   []string
	Warnings []string
	Infos    []string
}

// tagVersionRE mirrors extractTagVersion's /(?:^|@)v(\d+\.\d+\.\d+(?:[-+][^\s]+)?)$/.
var tagVersionRE = regexp.MustCompile(`(?:^|@)v(\d+\.\d+\.\d+(?:[-+][^\s]+)?)$`)

// RunDoctor performs the doctor checks against cwd using the normalized
// config. getenv abstracts process environment lookups (pass os.Getenv in
// production); a nil getenv falls back to os.Getenv.
func RunDoctor(cwd string, config *types.NormalizedConfig, getenv func(string) string) (DoctorResult, error) {
	result := DoctorResult{Errors: []string{}, Warnings: []string{}, Infos: []string{}}
	if getenv == nil {
		getenv = os.Getenv
	}

	hasBlankBranch := false
	for _, branch := range config.Branches {
		if strings.TrimSpace(branch) == "" {
			hasBlankBranch = true
			break
		}
	}
	if len(config.Branches) == 0 || hasBlankBranch {
		result.Errors = append(result.Errors, "Config must define at least one non-empty release branch.")
	}
	if len(config.Packages) == 0 {
		result.Errors = append(result.Errors, "Config must define at least one package.")
	}
	if len(result.Errors) > 0 {
		return result, nil
	}

	if !git.IsGitRepository(cwd) {
		result.Errors = append(result.Errors, "Current directory is not a git repository.")
		return result, nil
	}

	if !hasResolvableHeadCommit(cwd) {
		result.Errors = append(result.Errors, "Repository has no resolvable HEAD commit.")
		return result, nil
	}

	branch, err := git.CurrentBranch(cwd)
	if err != nil {
		return result, err
	}
	configured := false
	for _, candidate := range config.Branches {
		if candidate == branch {
			configured = true
			break
		}
	}
	if !configured {
		result.Warnings = append(result.Warnings,
			"Current branch '"+branch+"' is not a configured release branch.")
	} else {
		result.Infos = append(result.Infos, "Release branch: "+branch)
	}

	for _, pkg := range config.Packages {
		withAbsManifest := pkg
		withAbsManifest.Manifest = filepath.Join(cwd, pkg.Manifest)
		_, version, err := manifest.Read(withAbsManifest)
		if err != nil {
			return result, err
		}
		result.Infos = append(result.Infos, pkg.Name+": manifest version "+version)

		// TS swallows every getLatestTag failure into "no tag"; LatestTag
		// reports both that case and real git failures as errors.
		tag, err := git.LatestTag(cwd, plan.TagPatternFor(config, pkg))
		if err != nil || strings.TrimSpace(tag) == "" {
			result.Warnings = append(result.Warnings,
				pkg.Name+": no release tag found; first release will use full reachable history.")
			continue
		}

		tagVersion := extractTagVersion(tag)
		result.Infos = append(result.Infos, pkg.Name+": latest tag "+tag)
		if tagVersion != "" && tagVersion != version {
			result.Warnings = append(result.Warnings,
				pkg.Name+": manifest version "+version+" differs from latest tag version "+tagVersion+".")
		}
	}

	if config.GitHub.Releases && getenv("GITHUB_TOKEN") == "" && getenv("GH_TOKEN") == "" {
		result.Warnings = append(result.Warnings,
			"GITHUB_TOKEN or GH_TOKEN is not set; `release` cannot create GitHub releases.")
	}

	return result, nil
}

// hasResolvableHeadCommit mirrors the TS
// `!git(cwd, ["rev-parse", "--verify", "--quiet", "HEAD^{commit}"], true).trim()`
// probe: any failure (unborn HEAD, detached-void, missing git) yields empty
// stdout and therefore false.
func hasResolvableHeadCommit(cwd string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	cmd.Dir = cwd
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()
	return strings.TrimSpace(stdout.String()) != ""
}

// extractTagVersion returns the semver embedded in a release tag, or "" when
// the tag does not carry one (mirrors extractTagVersion).
func extractTagVersion(tag string) string {
	matches := tagVersionRE.FindStringSubmatch(tag)
	if matches == nil {
		return ""
	}
	return matches[1]
}
