// Package release executes the release pipeline. It mirrors src/release.ts
// (this file: options/result/execute/validate/hooks/GitHub publishing) and,
// in resume.go, the resume derivation and drift checks.
package release

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openhoo/hooversion/internal/changelog"
	"github.com/openhoo/hooversion/internal/envutil"
	"github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/githubapi"
	"github.com/openhoo/hooversion/internal/manifest"
	"github.com/openhoo/hooversion/internal/output"
	releaseplan "github.com/openhoo/hooversion/internal/plan"
	"github.com/openhoo/hooversion/internal/process"
	"github.com/openhoo/hooversion/internal/safefs"
	"github.com/openhoo/hooversion/internal/types"
)

// Options carries CLI-resolved execution switches. NoPushSet/Push mirror the
// TS `options.push ?? config.push` resolution: when NoPushSet is true Push
// wins over config.Push; otherwise config.Push applies. The GitHub pair works
// the same way against a default of true.
type Options struct {
	Context     context.Context
	DryRun      bool
	NoPushSet   bool
	Push        bool
	NoGitHubSet bool
	GitHub      bool
	GitHubToken string
	GitAuth     types.GitAuth
	// BaseEnv is the complete environment for release child processes. Nil
	// preserves normal CLI inheritance.
	BaseEnv []string
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
func Execute(cwd string, config *types.NormalizedConfig, plan *types.ReleasePlan, o Options) (result Result, returnErr error) {
	ctx := o.Context
	if ctx == nil {
		ctx = context.Background()
	}
	txn, owner, err := openTransaction(ctx, cwd, o.BaseEnv)
	if err != nil {
		return Result{}, err
	}
	defer owner.Close()
	defer txn.close()
	existing, err := txn.load(config)
	if err != nil {
		return Result{}, err
	}
	recovered := false
	if existing {
		if o.DryRun {
			return Result{}, fmt.Errorf("release recovery is pending; run release without --dry-run to recover or resume")
		}
		recovered, err = txn.recover(ctx)
		if err != nil {
			return Result{}, err
		}
		if !recovered {
			plan = txn.state.Plan
		}
	}
	if recovered {
		plan, err = releaseplan.CreatePlanWithEnv(cwd, config, plan.Branch, nil, o.BaseEnv, ctx)
		if err != nil {
			return Result{}, err
		}
	}
	effective := plan
	if derived, err := DeriveResumableWithEnv(cwd, config, o.BaseEnv, ctx); err != nil {
		return Result{}, err
	} else if derived != nil && (!existing || recovered) {
		effective = derived
	}
	resumable := isResumableReleaseWithEnv(cwd, effective, o.BaseEnv, ctx)

	if resumable {
		if err := verifyResumableRemoteWithAuthEnv(cwd, effective, o.BaseEnv, o.GitAuth, ctx); err != nil {
			return Result{}, err
		}
	} else if err := verifySourceWithAuthEnv(cwd, effective, o.BaseEnv, o.GitAuth, ctx); err != nil {
		return Result{}, err
	}

	if err := ValidateWithEnv(cwd, config, effective, resumable, o.BaseEnv, ctx); err != nil {
		return Result{}, err
	}

	if err := manifest.ValidateVersionUpdatesWithFS(cwd, effective.Releases, config.Packages, txn.files); err != nil {
		return Result{}, err
	}

	if o.DryRun {
		return Result{Plan: effective}, nil
	}

	store := output.Store{Cwd: cwd, OutputDir: config.OutputDir, BaseEnv: o.BaseEnv, Root: txn.files}
	if err := git.EnsureCleanWorkingTreeWithEnv(cwd, store.Paths(), o.BaseEnv, ctx); err != nil {
		return Result{}, err
	}
	if !existing || recovered {
		paths, err := manifest.ManagedPathsWithFS(cwd, config.Packages, txn.files)
		if err != nil {
			return Result{}, err
		}
		for _, pkg := range config.Packages {
			paths = append(paths, pkg.Changelog)
		}
		destinations, err := store.Destinations(effective.Releases)
		if err != nil {
			return Result{}, err
		}
		paths = append(paths, destinations...)
		if err := txn.snapshot(ctx, config, effective, paths); err != nil {
			return Result{}, err
		}
	}
	defer func() {
		if returnErr != nil {
			returnErr = txn.abort(returnErr)
		}
	}()
	if err := store.Clear(); err != nil {
		return Result{}, err
	}

	if len(effective.Releases) == 0 {
		if err := store.Write(effective.Releases, false); err != nil {
			return Result{}, err
		}
		if err := txn.finish(); err != nil {
			return Result{}, err
		}
		return Result{Plan: effective}, nil
	}

	if !resumable {
		if err := txn.advance("mutating"); err != nil {
			return Result{}, err
		}
		if err := runHooksWithEnv(cwd, config.Hooks.BeforeRelease, o.BaseEnv, ctx); err != nil {
			return Result{}, err
		}
		if err := verifyLocalSource(cwd, effective, o.BaseEnv, ctx); err != nil {
			return Result{}, err
		}
		releasedVersions := make(map[string]string, len(effective.Releases))
		for _, release := range effective.Releases {
			releasedVersions[release.Package.Name] = release.NextVersion
			pkg := release.Package
			pkg.Manifest = filepath.Join(cwd, pkg.Manifest)
			if err := manifest.UpdateVersionWithFS(pkg, release.NextVersion, txn.files); err != nil {
				return Result{}, err
			}
		}
		for _, pkg := range config.Packages {
			if err := manifest.UpdateLocalDependencyVersionsWithFS(cwd, pkg, releasedVersions, txn.files); err != nil {
				return Result{}, err
			}
		}
		for _, release := range effective.Releases {
			changelogPath := filepath.Join(cwd, release.Package.Changelog)
			if err := changelog.UpdateWithFS(changelogPath, release.Notes, release.Package.Name, txn.files); err != nil {
				return Result{}, err
			}
		}

		if err := runHooksWithEnv(cwd, config.Hooks.AfterVersion, o.BaseEnv, ctx); err != nil {
			return Result{}, err
		}
		if err := verifyLocalSource(cwd, effective, o.BaseEnv, ctx); err != nil {
			return Result{}, err
		}

		if err := txn.checkIdentity(); err != nil {
			return Result{}, err
		}
		if err := txn.advance("committing"); err != nil {
			return Result{}, err
		}
		if err := git.CreateReleaseCommitWithEnv(cwd, CommitMessage(effective), o.BaseEnv, ctx); err != nil {
			return Result{}, err
		}
		if err := txn.releaseHead(ctx); err != nil {
			return Result{}, err
		}
		if err := txn.advance("tagging"); err != nil {
			return Result{}, err
		}
		for _, release := range effective.Releases {
			if err := txn.checkIdentity(); err != nil {
				return Result{}, err
			}
			message := fmt.Sprintf("%s %s", release.Package.Name, release.NextVersion)
			if err := git.CreateAnnotatedTagWithEnv(cwd, release.Tag, message, o.BaseEnv, ctx); err != nil {
				return Result{}, err
			}
		}
	} else {
		if err := txn.checkIdentity(); err != nil {
			return Result{}, err
		}
		for _, release := range effective.Releases {
			ref, err := git.RefShaWithEnv(cwd, "refs/tags/"+release.Tag, o.BaseEnv, ctx)
			if err != nil {
				return Result{}, err
			}
			if ref == "" {
				message := fmt.Sprintf("%s %s", release.Package.Name, release.NextVersion)
				if err := git.CreateAnnotatedTagWithEnv(cwd, release.Tag, message, o.BaseEnv, ctx); err != nil {
					return Result{}, err
				}
			}
		}
	}

	shouldPush := config.Push
	if o.NoPushSet {
		shouldPush = o.Push
	}
	if shouldPush {
		if err := txn.checkIdentity(); err != nil {
			return Result{}, err
		}
		if err := txn.advance("external"); err != nil {
			return Result{}, err
		}
		tags := make([]string, 0, len(effective.Releases))
		for _, release := range effective.Releases {
			tags = append(tags, release.Tag)
		}
		if err := git.PushReleaseWithEnv(cwd, effective.Branch, tags, o.GitAuth, o.BaseEnv, ctx); err != nil {
			return Result{}, err
		}
	}

	if err := txn.files.MkdirAll(config.OutputDir, 0o755); err != nil {
		return Result{}, err
	}

	shouldGitHub := true
	if o.NoGitHubSet {
		shouldGitHub = o.GitHub
	}
	shouldPublishGitHub := shouldGitHub && config.GitHub.Enabled && config.GitHub.Releases
	if shouldPublishGitHub && !shouldPush {
		head, err := git.HeadShaWithEnv(cwd, o.BaseEnv, ctx)
		if err != nil {
			return Result{}, err
		}
		for _, release := range effective.Releases {
			remote, err := git.RemoteTagShaWithAuthEnv(cwd, release.Tag, o.BaseEnv, o.GitAuth, ctx)
			if err != nil {
				return Result{}, errors.New(
					"GitHub publication with --no-push requires tag %s to resolve remotely; push the tag first or disable GitHub publication: %v",
					release.Tag, err)
			}
			if remote != head {
				found := remote
				if found == "" {
					found = "missing"
				}
				return Result{}, errors.New(
					"GitHub publication with --no-push requires origin tag %s at release commit %s, found %s; push the tag first or disable GitHub publication.",
					release.Tag, head, found)
			}
		}
	}
	if shouldPublishGitHub {
		if err := txn.advance("external"); err != nil {
			return Result{}, err
		}
		if err := publishGitHubReleasesWithEnv(cwd, config, effective, o.GitHubToken, o.BaseEnv, ctx); err != nil {
			return Result{}, err
		}
	}

	if err := store.Write(effective.Releases, true); err != nil {
		return Result{}, err
	}
	if err := txn.advance("published"); err != nil {
		return Result{}, err
	}
	if err := runHooksWithEnv(cwd, config.Hooks.AfterRelease, o.BaseEnv, ctx); err != nil {
		return Result{}, err
	}
	if err := txn.finish(); err != nil {
		return Result{}, err
	}
	return Result{Published: true, Plan: effective}, nil
}

// Validate enforces branch membership, unmatched-commit blocking, and — for
// fresh releases only — that no planned tag exists yet. All checks happen
// before any mutation.
func Validate(cwd string, config *types.NormalizedConfig, plan *types.ReleasePlan, resumable bool) error {
	return ValidateWithEnv(cwd, config, plan, resumable, nil)
}

func ValidateWithEnv(cwd string, config *types.NormalizedConfig, plan *types.ReleasePlan, resumable bool, baseEnv []string, contexts ...context.Context) error {
	// Recheck mutable paths even when callers construct normalized config
	// directly or hooks/output state changed since configuration loading.
	paths := []string{config.OutputDir, ".release-version"}
	for _, pkg := range config.Packages {
		paths = append(paths, pkg.Manifest, pkg.Changelog)
	}
	for _, path := range paths {
		if err := safefs.RequireContainedPath(cwd, path); err != nil {
			return err
		}
	}
	tagOwners := make(map[string]string, len(plan.Releases))
	for _, release := range plan.Releases {
		if previous, exists := tagOwners[release.Tag]; exists {
			return errors.New("Duplicate release tag %s for packages %s and %s.",
				release.Tag, previous, release.Package.Name)
		}
		tagOwners[release.Tag] = release.Package.Name
	}
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

	if !resumable {
		for _, release := range plan.Releases {
			exists, err := git.TagExistsWithEnv(cwd, release.Tag, baseEnv, contexts...)
			if err != nil {
				return err
			}
			if exists {
				return errors.New("Tag already exists: %s", release.Tag)
			}
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

func runHooksWithEnv(cwd string, hooks []string, baseEnv []string, contexts ...context.Context) error {
	for _, hook := range hooks {
		result, err := runShellWithEnv(hook, cwd, baseEnv, contexts...)
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

func runShellWithEnv(command, cwd string, baseEnv []string, contexts ...context.Context) (shellResult, error) {
	interpreter := envutil.Get(baseEnv, "SHELL")
	if interpreter == "" {
		interpreter = "/bin/sh"
	}
	result := process.Run(process.Context(contexts), process.Options{Dir: cwd, Env: baseEnv}, interpreter, "-c", command)
	if result.Err != nil {
		var exitErr *exec.ExitError
		if !asExitError(result.Err, &exitErr) {
			return shellResult{}, result.Err
		}
	}
	return shellResult{code: result.Code, stdout: result.Stdout, stderr: result.Stderr}, nil
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

func publishGitHubReleases(cwd string, config *types.NormalizedConfig, plan *types.ReleasePlan, tokenOption string) error {
	return publishGitHubReleasesWithEnv(cwd, config, plan, tokenOption, nil)
}

// publishGitHubReleasesWithEnv publishes using the supplied child environment
// for repository-origin lookups.
func publishGitHubReleasesWithEnv(cwd string, config *types.NormalizedConfig, plan *types.ReleasePlan, tokenOption string, baseEnv []string, contexts ...context.Context) error {
	if !config.GitHub.Enabled || !config.GitHub.Releases {
		return nil
	}

	token := tokenOption
	if token == "" {
		token = envutil.Get(baseEnv, "GITHUB_TOKEN")
	}
	if token == "" {
		token = envutil.Get(baseEnv, "GH_TOKEN")
	}
	if token == "" {
		return errors.New("GITHUB_TOKEN or GH_TOKEN is required to create GitHub releases.")
	}

	repository := config.GitHub.Repository
	if repository == "" {
		origin, err := git.OriginRepositoryWithEnv(cwd, baseEnv, contexts...)
		if err != nil {
			return err
		}
		repository = origin
	}
	if repository == "" {
		return errors.New("Could not determine GitHub repository. Set github.repository in hooversion config.")
	}

	client := newGitHubClient(config.GitHub.ApiUrl, token)
	client.Context = process.Context(contexts)
	for _, release := range plan.Releases {
		releaseName := fmt.Sprintf("%s %s", release.Package.Name, release.NextVersion)
		existing, err := client.ReleaseByTag(repository, release.Tag)
		if err != nil {
			return err
		}

		response := existing
		existingAssets := make(map[string]githubapi.Asset)
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
			inventory, err := client.ListReleaseAssets(repository, existing.ID)
			if err != nil {
				return err
			}
			for _, asset := range inventory {
				existingAssets[asset.Name] = asset
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
			if existingAsset, present := existingAssets[name]; present {
				if err := client.VerifyExistingAssetFrom(cwd, repository, existingAsset, asset); err != nil {
					return err
				}
				continue
			}
			if _, seen := missing[name]; !seen {
				missing[name] = asset
				order = append(order, name)
			}
		}
		for _, name := range order {
			// Keep the upload rooted at this checkout without a global chdir.
			if err := client.UploadAssetFrom(cwd, response.UploadURL, name, missing[name]); err != nil {
				return err
			}
		}
	}
	return nil
}
