// Package release executes the release pipeline. It mirrors src/release.ts
// (this file: options/result/execute/validate/hooks/GitHub publishing) and,
// in resume.go, the resume derivation and drift checks.
package release

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openhoo/hooversion/internal/changelog"
	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/githubapi"
	"github.com/openhoo/hooversion/internal/manifest"
	"github.com/openhoo/hooversion/internal/output"
	"github.com/openhoo/hooversion/internal/types"
)

// Options carries CLI-resolved execution switches. NoPushSet/Push mirror the
// TS `options.push ?? config.push` resolution: when NoPushSet is true Push
// wins over config.Push; otherwise config.Push applies. The GitHub pair works
// the same way against a default of true.
type Options struct {
	DryRun      bool
	NoPushSet   bool
	Push        bool
	NoGitHubSet bool
	GitHub      bool
	GitHubToken string
	GitAuth     types.GitAuth
}

// Result reports what Execute did and the effective plan it acted on (which
// is the reconstructed plan when the run resumed an earlier release).
type Result struct {
	Published bool
	Plan      *types.ReleasePlan
}

// Execute runs the pipeline in the exact src/release.ts step order: resume
// derivation, drift checks, validation, dry-run exit, clean-tree gate with
// managed-output exemption, mutations, atomic push, GitHub publish, outputs.
func Execute(cwd string, config *types.NormalizedConfig, plan *types.ReleasePlan, o Options) (Result, error) {
	effective := plan
	if len(plan.Releases) == 0 {
		if derived, err := DeriveResumable(cwd, config); err != nil {
			return Result{}, err
		} else if derived != nil {
			effective = derived
		}
	}
	resumable := isResumableRelease(cwd, effective)

	if resumable {
		if err := verifyResumableRemote(cwd, effective); err != nil {
			return Result{}, err
		}
	} else if err := verifySource(cwd, effective); err != nil {
		return Result{}, err
	}

	if err := Validate(cwd, config, effective, resumable); err != nil {
		return Result{}, err
	}

	if o.DryRun {
		return Result{Plan: effective}, nil
	}

	store := output.Store{Cwd: cwd, OutputDir: config.OutputDir}
	if err := git.EnsureCleanWorkingTree(cwd, store.Paths()); err != nil {
		return Result{}, err
	}
	if err := store.Clear(); err != nil {
		return Result{}, err
	}

	if len(effective.Releases) == 0 {
		if err := store.Write(effective.Releases, false); err != nil {
			return Result{}, err
		}
		return Result{Plan: effective}, nil
	}

	if !resumable {
		if err := runHooks(cwd, config.Hooks.BeforeRelease); err != nil {
			return Result{}, err
		}
		releasedVersions := make(map[string]string, len(effective.Releases))
		for _, release := range effective.Releases {
			releasedVersions[release.Package.Name] = release.NextVersion
			pkg := release.Package
			pkg.Manifest = filepath.Join(cwd, pkg.Manifest)
			if err := manifest.UpdateVersion(pkg, release.NextVersion); err != nil {
				return Result{}, err
			}
		}
		for _, pkg := range config.Packages {
			if err := manifest.UpdateLocalDependencyVersions(cwd, pkg, releasedVersions); err != nil {
				return Result{}, err
			}
		}
		for _, release := range effective.Releases {
			changelogPath := filepath.Join(cwd, release.Package.Changelog)
			if err := changelog.Update(changelogPath, release.Notes, release.Package.Name); err != nil {
				return Result{}, err
			}
		}

		if err := runHooks(cwd, config.Hooks.AfterVersion); err != nil {
			return Result{}, err
		}

		if err := git.CreateReleaseCommit(cwd, CommitMessage(effective)); err != nil {
			return Result{}, err
		}
		for _, release := range effective.Releases {
			message := fmt.Sprintf("%s %s", release.Package.Name, release.NextVersion)
			if err := git.CreateAnnotatedTag(cwd, release.Tag, message); err != nil {
				return Result{}, err
			}
		}
	}

	shouldPush := config.Push
	if o.NoPushSet {
		shouldPush = o.Push
	}
	if shouldPush {
		tags := make([]string, 0, len(effective.Releases))
		for _, release := range effective.Releases {
			tags = append(tags, release.Tag)
		}
		if err := git.PushRelease(cwd, effective.Branch, tags, o.GitAuth); err != nil {
			return Result{}, err
		}
	}

	if err := os.MkdirAll(filepath.Join(cwd, config.OutputDir), 0o755); err != nil {
		return Result{}, err
	}

	shouldGitHub := true
	if o.NoGitHubSet {
		shouldGitHub = o.GitHub
	}
	if shouldGitHub {
		if err := publishGitHubReleases(cwd, config, effective, o.GitHubToken); err != nil {
			return Result{}, err
		}
	}

	if err := store.Write(effective.Releases, true); err != nil {
		return Result{}, err
	}
	if err := runHooks(cwd, config.Hooks.AfterRelease); err != nil {
		return Result{}, err
	}
	return Result{Published: true, Plan: effective}, nil
}

// Validate enforces branch membership, unmatched-commit blocking, and — for
// fresh releases only — that no planned tag exists yet. All checks happen
// before any mutation.
func Validate(cwd string, config *types.NormalizedConfig, plan *types.ReleasePlan, resumable bool) error {
	branchAllowed := false
	for _, branch := range config.Branches {
		if branch == plan.Branch {
			branchAllowed = true
			break
		}
	}
	if !branchAllowed {
		return errors.New(
			"Current branch '%s' is not a release branch. Allowed branches: %s",
			plan.Branch, strings.Join(config.Branches, ", "))
	}

	if len(plan.UnmatchedCommits) > 0 {
		details := make([]string, 0, len(plan.UnmatchedCommits))
		for _, parsed := range plan.UnmatchedCommits {
			details = append(details, fmt.Sprintf("%s %s", hash7(parsed.Hash), parsed.Subject))
		}
		return errors.New(
			"Release-worthy commits could not be assigned to a package:\n%s",
			strings.Join(details, "\n"))
	}

	if resumable {
		return nil
	}
	for _, release := range plan.Releases {
		exists, err := git.TagExists(cwd, release.Tag)
		if err != nil {
			return err
		}
		if exists {
			return errors.New("Tag already exists: %s", release.Tag)
		}
	}
	return nil
}

// CommitMessage renders the byte-exact release commit message for a plan:
// single-release and multi-release forms per contract §3. Resume derivation
// compares this against HEAD to accept or reject a reconstruction.
func CommitMessage(plan *types.ReleasePlan) string {
	if len(plan.Releases) == 1 {
		release := plan.Releases[0]
		return fmt.Sprintf("chore(release): %s %s\n\n%s", release.Package.Name, release.NextVersion, release.Notes)
	}
	summary := make([]string, 0, len(plan.Releases))
	blocks := make([]string, 0, len(plan.Releases))
	for _, release := range plan.Releases {
		summary = append(summary, fmt.Sprintf("%s@%s", release.Package.Name, release.NextVersion))
		blocks = append(blocks, fmt.Sprintf("# %s %s\n\n%s", release.Package.Name, release.NextVersion, release.Notes))
	}
	return fmt.Sprintf("chore(release): %s\n\n%s", strings.Join(summary, ", "), strings.Join(blocks, "\n\n"))
}

func runHooks(cwd string, hooks []string) error {
	for _, hook := range hooks {
		result, err := runShell(hook, cwd)
		if err != nil {
			return err
		}
		if result.code != 0 {
			detail := result.stderr
			if detail == "" {
				detail = result.stdout
			}
			return errors.New("Hook failed: %s\n%s", hook, detail)
		}
	}
	return nil
}

// runShell mirrors src/process.ts runShell: $SHELL (or /bin/sh) -c command
// with cwd; a spawn failure counts as status 1.
type shellResult struct {
	code   int
	stdout string
	stderr string
}

func runShell(command, cwd string) (shellResult, error) {
	interpreter := os.Getenv("SHELL")
	if interpreter == "" {
		interpreter = "/bin/sh"
	}
	cmd := exec.Command(interpreter, "-c", command)
	cmd.Dir = cwd
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	return shellResult{code: code, stdout: stdout.String(), stderr: stderr.String()}, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}

func hash7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// newGitHubClient is a seam allowing tests to install an HTTP transport for
// TLS-faked GitHub endpoints; production uses the plain constructor.
var newGitHubClient = func(baseURL, token string) *githubapi.Client {
	return githubapi.New(baseURL, token)
}

// publishGitHubReleases publishes (or idempotently reuses) one GitHub
// release per planned package, uploading only assets whose basename is missing.
func publishGitHubReleases(cwd string, config *types.NormalizedConfig, plan *types.ReleasePlan, tokenOption string) error {
	if !config.GitHub.Enabled || !config.GitHub.Releases {
		return nil
	}

	token := tokenOption
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		return errors.New("GITHUB_TOKEN or GH_TOKEN is required to create GitHub releases.")
	}

	repository := config.GitHub.Repository
	if repository == "" {
		origin, err := git.OriginRepository(cwd)
		if err != nil {
			return err
		}
		repository = origin
	}
	if repository == "" {
		return errors.New("Could not determine GitHub repository. Set github.repository in hooversion config.")
	}

	// The landed asset reader resolves upload paths against the process CWD;
	// scope it to the repository for the duration of publishing.
	originalWd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(cwd); err != nil {
		return err
	}
	defer func() { _ = os.Chdir(originalWd) }()

	client := newGitHubClient(config.GitHub.ApiUrl, token)
	for _, release := range plan.Releases {
		releaseName := fmt.Sprintf("%s %s", release.Package.Name, release.NextVersion)
		existing, err := client.ReleaseByTag(repository, release.Tag)
		if err != nil {
			return err
		}

		response := existing
		existingAssetNames := make(map[string]bool)
		if existing != nil {
			matches := existing.TagName == release.Tag &&
				existing.Name == releaseName &&
				existing.Body == release.Notes &&
				!existing.Draft &&
				!existing.Prerelease
			if !matches {
				return errors.New(
					"GitHub release already exists for tag %s with different metadata.", release.Tag)
			}
			for _, asset := range existing.Assets {
				existingAssetNames[asset.Name] = true
			}
		} else {
			created, err := client.CreateRelease(repository, githubapi.ReleaseInput{
				TagName: release.Tag,
				Name:    releaseName,
				Body:    release.Notes,
			})
			if err != nil {
				return err
			}
			response = created
		}

		missing := make(map[string]string) // basename -> repo-relative path; first wins
		var order []string
		for _, asset := range release.Package.Assets {
			name := filepath.Base(asset)
			if existingAssetNames[name] {
				continue
			}
			if _, seen := missing[name]; !seen {
				missing[name] = asset
				order = append(order, name)
			}
		}
		for _, name := range order {
			// The process CWD is the repository here; upload.go re-roots
			// uploads at Getwd(), so pass the raw repo-relative asset.
			if err := client.UploadAsset(response.UploadURL, name, missing[name]); err != nil {
				return err
			}
		}
	}
	return nil
}
