// This file mirrors src/app-runner.ts: clone-URL validation, isolated
// workdir/HOME execution, askpass git auth, the scrubbed child environment,
// stale-guard detection, install handling with secret redaction, and release
// orchestration. It also mirrors the orchestration of src/app-server.ts's
// releaseFromWorkflowRun (token mint + check-run lifecycle).
package app

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/openhoo/hooversion/internal/config"
	hverr "github.com/openhoo/hooversion/internal/errors"
	"github.com/openhoo/hooversion/internal/plan"
	"github.com/openhoo/hooversion/internal/release"
	"github.com/openhoo/hooversion/internal/types"
)

const (
	gitAskpassFilename   = ".git-askpass"
	gitTokenFilename     = ".git-token"
	gitTokenFileEnv      = "VERSIONHOO_GIT_TOKEN_FILE"
	defaultGitAuthorName = "versionhoo[bot]"
	defaultGitAuthorMail = "versionhoo[bot]@users.noreply.github.com"

	// gitAskpassScript mirrors GIT_ASKPASS_SCRIPT byte-for-byte.
	gitAskpassScript = "#!/bin/sh\ncase \"$1\" in\n" +
		"  *[Uu][Ss][Ee][Rr][Nn][Aa][Mm][Ee]*) printf \"%s\\n\" \"x-access-token\" ;;\n" +
		"  *) cat \"$VERSIONHOO_GIT_TOKEN_FILE\" ;;\n" +
		"esac\n"
)

// JobSpec mirrors VersionhooReleaseJob (the webhook context handed to the
// runner). RepoDir is a test seam: when set, cloning is skipped and the
// directory is used as-is without external cleanup.
type JobSpec struct {
	RepositoryFullName string
	CloneURL           string
	Branch             string
	HeadSha            string
	Token              string
	ApiURL             string
	TrustedAPIURLs     []string
	TrustedCloneHosts  []string
	WorkDir            string
	ConfigPath         string
	InstallCommand     string
	GitAuthorName      string
	GitAuthorEmail     string
	KeepWorkDir        bool
	RepoDir            string // test seam: pre-cloned working copy
}

// ReleaseRef is one published package reference.
type ReleaseRef struct {
	Name    string
	Version string
	Tag     string
}

// Outcome mirrors VersionhooReleaseResult; Err carries a failure that maps to
// the failure check result and re-raising into the queue.
type Outcome struct {
	RepositoryFullName string
	Branch             string
	HeadSha            string
	WorkDir            string
	Outcome            string // "published" | "no_release" | "stale"
	Published          bool
	Message            string
	Releases           []ReleaseRef
	Err                error
}

// Runner is the test seam for release execution; production uses
// runVersionhooRelease.
var Runner = func(spec JobSpec) Outcome {
	return runVersionhooRelease(spec)
}

// repositoryEnvironmentMu serializes environment-sensitive child execution
// globally, mirroring the repositoryEnvironmentTail promise chain.
var repositoryEnvironmentMu sync.Mutex

func redact(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[redacted]")
}

// ValidateCloneURL mirrors validateCloneUrl: https only, no credentials, port,
// query, or hash; host must be github.com or a trusted clone host; the path
// (sans .git) must equal the repository full name case-insensitively. Returns
// the normalized URL used for cloning.
func ValidateCloneURL(cloneURL, repositoryFullName string, trustedCloneHosts []string) (string, error) {
	expected, err := ValidateRepositoryFullName(repositoryFullName)
	if err != nil {
		return "", err
	}
	parsed, ok := parseWebURL(cloneURL)
	if !ok ||
		parsed.Scheme != "https" ||
		parsed.User != nil ||
		parsed.Port() != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return "", hverr.New("Invalid GitHub clone URL: %s", cloneURL)
	}
	allowed := map[string]bool{"github.com": true}
	for _, host := range trustedCloneHosts {
		allowed[strings.ToLower(host)] = true
	}
	host := parsed.Hostname()
	if !allowed[strings.ToLower(host)] {
		return "", hverr.New("Untrusted GitHub clone host: %s", host)
	}
	decoded, err := unescapePath(parsed.EscapedPath())
	if err != nil {
		return "", hverr.New("Invalid GitHub clone URL: %s", cloneURL)
	}
	path := strings.Trim(decoded, "/")
	path = stripGitSuffix(path)
	if !strings.EqualFold(path, expected) {
		return "", hverr.New("GitHub clone repository mismatch: expected %s, got %s", expected, path)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func stripGitSuffix(path string) string {
	if len(path) >= 4 && strings.EqualFold(path[len(path)-4:], ".git") {
		return path[:len(path)-4]
	}
	return path
}

func unescapePath(path string) (string, error) {
	decoded := new(strings.Builder)
	for i := 0; i < len(path); i++ {
		if path[i] != '%' {
			decoded.WriteByte(path[i])
			continue
		}
		if i+2 >= len(path) {
			return "", fmt.Errorf("truncated escape in %q", path)
		}
		var b byte
		if _, err := fmt.Sscanf(path[i+1:i+3], "%02x", &b); err != nil {
			return "", err
		}
		decoded.WriteByte(b)
		i += 2
	}
	return decoded.String(), nil
}

// gitAuthArtifacts mirrors GitAuthArtifacts: token file plus askpass script.
type gitAuthArtifacts struct {
	tokenPath   string
	askpassPath string
	env         types.GitAuth
	cleanup     func()
}

func createGitAuthArtifacts(authDir, token string) (*gitAuthArtifacts, error) {
	tokenPath := filepath.Join(authDir, gitTokenFilename)
	askpassPath := filepath.Join(authDir, gitAskpassFilename)
	tokenCreated, askpassCreated := false, false
	cleanup := func() {
		if askpassCreated {
			os.Remove(askpassPath)
			askpassCreated = false
		}
		if tokenCreated {
			os.Remove(tokenPath)
			tokenCreated = false
		}
	}
	file, err := os.OpenFile(tokenPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, writeErr := file.WriteString(token + "\n")
		closeErr := file.Close()
		chmodErr := os.Chmod(tokenPath, 0o600)
		if writeErr != nil || closeErr != nil || chmodErr != nil {
			err = firstNonNil(writeErr, closeErr, chmodErr)
		} else {
			tokenCreated = true
		}
	}
	if err == nil {
		err = os.WriteFile(askpassPath, []byte(gitAskpassScript), 0o700)
		if err == nil {
			askpassCreated = true
			if chmodErr := os.Chmod(askpassPath, 0o700); chmodErr != nil {
				err = chmodErr
			}
		}
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	return &gitAuthArtifacts{
		tokenPath:   tokenPath,
		askpassPath: askpassPath,
		env: types.GitAuth{
			"GIT_ASKPASS":         askpassPath,
			"GIT_TERMINAL_PROMPT": "0",
			gitTokenFileEnv:       tokenPath,
		},
		cleanup: cleanup,
	}, nil
}

func firstNonNil(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// childEnv builds the exact scrubbed environment from src/app-runner.ts.
func childEnv(spec JobSpec, home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"SHELL=" + orDefault(os.Getenv("SHELL"), "/bin/sh"),
		"LANG=" + orDefault(os.Getenv("LANG"), "C.UTF-8"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GITHUB_REPOSITORY=" + spec.RepositoryFullName,
		"GITHUB_REF_NAME=" + spec.Branch,
		"GITHUB_SHA=" + spec.HeadSha,
		"VERSIONHOO_REPOSITORY=" + spec.RepositoryFullName,
		"VERSIONHOO_BRANCH=" + spec.Branch,
		"VERSIONHOO_SHA=" + spec.HeadSha,
	}
}

// checkedOutput runs command with env, returning trimmed stdout; on failure it
// renders the verbatim "<command> <args> failed:" error with redaction.
func checkedOutput(env []string, dir, command string, args []string, secret string) (string, error) {
	cmd := exec.Command(command, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr != nil {
		rendered := command
		for _, arg := range args {
			rendered += " " + redact(arg, secret)
		}
		detail := stderr.String()
		if detail == "" {
			detail = stdout.String()
		}
		return "", hverr.New("%s failed:\n%s", rendered, redact(detail, secret))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// installProjectDependencies mirrors installProjectDependencies: the
// configured command wins, else `bun install --frozen-lockfile` when
// bun.lock exists; output is redacted on failure.
func installProjectDependencies(repoDir string, configuredCommand, secret string, env []string) error {
	command := configuredCommand
	if command == "" {
		if _, err := os.Stat(filepath.Join(repoDir, "bun.lock")); err == nil {
			command = "bun install --frozen-lockfile"
		}
	}
	if command == "" {
		return nil
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = repoDir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := stderr.String()
		if detail == "" {
			detail = stdout.String()
		}
		return hverr.New("Install command failed: %s\n%s", redact(command, secret), redact(detail, secret))
	}
	return nil
}

// runVersionhooRelease mirrors runVersionhooRelease.
func runVersionhooRelease(spec JobSpec) Outcome {
	parent := spec.WorkDir
	if parent == "" {
		parent = filepath.Join(os.TempDir(), "versionhoo")
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return failureOutcome(spec, err)
	}
	workDir, err := os.MkdirTemp(parent, "release-")
	if err != nil {
		return failureOutcome(spec, err)
	}
	repositoryHome, err := os.MkdirTemp(workDir, ".home-")
	if err != nil {
		os.RemoveAll(workDir)
		return failureOutcome(spec, err)
	}
	defer os.RemoveAll(repositoryHome)

	removeWorkDir := func() {
		if !spec.KeepWorkDir {
			os.RemoveAll(workDir)
		}
	}

	// Global serialization of environment-sensitive execution.
	repositoryEnvironmentMu.Lock()
	defer repositoryEnvironmentMu.Unlock()

	outcome := func() Outcome {
		env := childEnv(spec, repositoryHome)

		cloneURL := spec.CloneURL
		if spec.RepoDir == "" {
			validated, err := ValidateCloneURL(spec.CloneURL, spec.RepositoryFullName, spec.TrustedCloneHosts)
			if err != nil {
				return failureOutcome(spec, err)
			}
			cloneURL = validated
		}

		auth, err := createGitAuthArtifacts(repositoryHome, spec.Token)
		if err != nil {
			return failureOutcome(spec, err)
		}
		defer auth.cleanup()

		authorName := orDefault(spec.GitAuthorName, defaultGitAuthorName)
		authorEmail := orDefault(spec.GitAuthorEmail, defaultGitAuthorMail)
		repoDir := spec.RepoDir
		if repoDir == "" {
			repoDir = filepath.Join(workDir, "repo")
			cloneEnv := append(append([]string{}, env...), auth.envToSlice()...)
			if _, err := checkedOutput(cloneEnv, workDir, "git", []string{
				"clone", "--branch", spec.Branch, "--no-single-branch", cloneURL, repoDir,
			}, spec.Token); err != nil {
				return failureOutcome(spec, err)
			}
		}
		if _, err := checkedOutput(env, repoDir, "git", []string{"config", "user.name", authorName}, spec.Token); err != nil {
			return failureOutcome(spec, err)
		}
		if _, err := checkedOutput(env, repoDir, "git", []string{"config", "user.email", authorEmail}, spec.Token); err != nil {
			return failureOutcome(spec, err)
		}

		branchHead, _ := checkedOutput(env, repoDir, "git", []string{"rev-parse", "HEAD"}, spec.Token)
		if branchHead != spec.HeadSha {
			return Outcome{
				RepositoryFullName: spec.RepositoryFullName,
				Branch:             spec.Branch,
				HeadSha:            spec.HeadSha,
				WorkDir:            workDir,
				Outcome:            "stale",
				Published:          false,
				Message: fmt.Sprintf(
					"Skipped stale workflow run for %s@%s: branch is %s, workflow passed on %s.",
					spec.RepositoryFullName, spec.Branch, branchHead, spec.HeadSha),
				Releases: []ReleaseRef{},
			}
		}
		if spec.InstallCommand != "" {
			return failureOutcome(spec, fmt.Errorf("Versionhoo App mode rejects dependency installation; use a hook-free, preinstalled repository release"))
		}
		if _, err := os.Stat(filepath.Join(repoDir, "bun.lock")); err == nil {
			return failureOutcome(spec, fmt.Errorf("Versionhoo App mode rejects implicit dependency installation from bun.lock; use a hook-free, preinstalled repository release"))
		}

		cfg, err := config.Load(repoDir, spec.ConfigPath)
		if err != nil {
			return failureOutcome(spec, err)
		}
		if len(cfg.Hooks.BeforeRelease) > 0 || len(cfg.Hooks.AfterVersion) > 0 || len(cfg.Hooks.AfterRelease) > 0 {
			return failureOutcome(spec, fmt.Errorf("Versionhoo App mode rejects repository hooks; use a hook-free repository release"))
		}
		trustedApiURL, err := ValidateGitHubApiURL(orDefault(spec.ApiURL, "https://api.github.com"), spec.TrustedAPIURLs)
		if err != nil {
			return failureOutcome(spec, err)
		}
		if cfg.GitHub.Enabled {
			repoIdentity, err := ValidateRepositoryFullName(spec.RepositoryFullName)
			if err != nil {
				return failureOutcome(spec, err)
			}
			cfg.GitHub.Repository = repoIdentity
			cfg.GitHub.ApiUrl = trustedApiURL
		}
		releasePlan, err := plan.CreatePlanWithEnv(repoDir, cfg, spec.Branch, nil, env)
		if err != nil {
			return failureOutcome(spec, err)
		}
		execution, err := release.Execute(repoDir, cfg, releasePlan, release.Options{
			NoPushSet:   true,
			Push:        true,
			NoGitHubSet: true,
			GitHub:      true,
			GitHubToken: spec.Token,
			GitAuth:     auth.env,
			BaseEnv:     env,
		})
		if err != nil {
			return failureOutcome(spec, err)
		}

		releases := make([]ReleaseRef, 0, len(execution.Plan.Releases))
		for _, r := range execution.Plan.Releases {
			releases = append(releases, ReleaseRef{Name: r.Package.Name, Version: r.NextVersion, Tag: r.Tag})
		}
		out := "no_release"
		if execution.Published {
			out = "published"
		}
		return Outcome{
			RepositoryFullName: spec.RepositoryFullName,
			Branch:             spec.Branch,
			HeadSha:            spec.HeadSha,
			WorkDir:            workDir,
			Outcome:            out,
			Published:          execution.Published,
			Releases:           releases,
		}
	}()
	// finally-style cleanup unless retention was requested.
	removeWorkDir()
	return outcome
}

func failureOutcome(spec JobSpec, err error) Outcome {
	return Outcome{
		RepositoryFullName: spec.RepositoryFullName,
		Branch:             spec.Branch,
		HeadSha:            spec.HeadSha,
		Outcome:            "failed",
		Published:          false,
		Err:                err,
	}
}

// envToSlice flattens the auth map deterministically.
func (a *gitAuthArtifacts) envToSlice() []string {
	keys := make([]string, 0, len(a.env))
	for key := range a.env {
		keys = append(keys, key)
	}
	sortStrings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+a.env[key])
	}
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// ReleaseFromWorkflowRun mirrors releaseFromWorkflowRun: filter, token mint,
// check-run creation (warn-only), runner invocation, check completion, and
// failure re-raising so the queue can release dedupe reservations.
func ReleaseFromWorkflowRun(payload *WebhookPayload, cfg *AppConfig, runner func(JobSpec) Outcome) error {
	decision := ShouldHandleWorkflowRun(payload, cfg)
	if decision.Status == "ignored" {
		return nil
	}

	if payload.Installation == nil {
		return hverr.New("Workflow run payload is missing installation id or branch.")
	}
	if payload.WorkflowRun.HeadBranch == nil {
		return hverr.New("Workflow run payload is missing installation id or branch.")
	}

	headSha := payload.WorkflowRun.HeadSHA
	fullName := payload.Repository.FullName

	// Token minting preconditions mirrored from createInstallationAccessToken.
	if _, err := ValidateRepositoryFullName(fullName); err != nil {
		return err
	}
	if payload.Installation.ID <= 0 {
		return hverr.New("GitHub App installation id must be a positive integer.")
	}
	if payload.Repository.ID <= 0 {
		return hverr.New("GitHub webhook repository id must be a positive integer.")
	}
	apiURL, err := ValidateGitHubApiURL(cfg.ApiURL, cfg.TrustedAPIURLs)
	if err != nil {
		return err
	}
	token, err := mintToken(apiURL, cfg.AppID, cfg.PrivateKey, payload.Installation.ID, []int64{payload.Repository.ID})
	if err != nil {
		return err
	}

	client, err := newCheckRunClient(cfg.ApiURL, token, cfg.TrustedAPIURLs)
	if err != nil {
		return err
	}

	checkRunID, createErr := createReleaseCheckRun(client, fullName, headSha)
	if createErr != nil {
		warnf("Could not create Versionhoo Release check: %v", createErr)
		checkRunID = 0
	}

	spec := buildJobSpec(payload, cfg, token)
	result := runner(spec)

	if result.Err != nil {
		if checkRunID != 0 {
			check := ReleaseFailureCheckResult(result.Err)
			if err := completeReleaseCheckRun(client, fullName, checkRunID, check); err != nil {
				warnf("Could not mark Versionhoo Release check failed: %v", err)
			}
		}
		return result.Err
	}

	if checkRunID != 0 {
		check := ReleaseCheckResult(result)
		if err := completeReleaseCheckRun(client, fullName, checkRunID, check); err != nil {
			warnf("Could not complete Versionhoo Release check: %v", err)
		}
	}
	return nil
}

// buildJobSpec maps the validated webhook context and config into a JobSpec.
// It is a package-level seam so tests can inject a prepared RepoDir.
var buildJobSpec = func(payload *WebhookPayload, cfg *AppConfig, token string) JobSpec {
	branch := ""
	if payload.WorkflowRun.HeadBranch != nil {
		branch = *payload.WorkflowRun.HeadBranch
	}
	return JobSpec{
		RepositoryFullName: payload.Repository.FullName,
		CloneURL:           payload.Repository.CloneURL,
		Branch:             branch,
		HeadSha:            payload.WorkflowRun.HeadSHA,
		Token:              token,
		ApiURL:             cfg.ApiURL,
		TrustedAPIURLs:     cfg.TrustedAPIURLs,
		TrustedCloneHosts:  cfg.TrustedCloneHosts,
		WorkDir:            cfg.WorkDir,
		ConfigPath:         cfg.ConfigPath,
		InstallCommand:     cfg.InstallCommand,
		GitAuthorName:      cfg.GitAuthorName,
		GitAuthorEmail:     cfg.GitAuthorEmail,
		KeepWorkDir:        cfg.KeepWorkDir,
	}
}
