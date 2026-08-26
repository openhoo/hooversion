package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooversion/internal/types"
)

// --- fixtures ---------------------------------------------------------------

// runRepoCmd runs a real git command in dir with an isolated config, failing
// the test on error (mirrors the internal/git test fixture conventions).
func runRepoCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=", // isolate from developer/global git config
		"GIT_CONFIG_SYSTEM=",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// initRepo creates a real repository on main with one commit.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runRepoCmd(t, dir, "init", "-q", "-b", "main")
	runRepoCmd(t, dir, "config", "user.email", "test@example.com")
	runRepoCmd(t, dir, "config", "user.name", "Test User")
	writeFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runRepoCmd(t, dir, "add", "--all")
	runRepoCmd(t, dir, "commit", "-q", "-m", "chore: init")
	return dir
}

func nodePkg(name, manifestRel string) types.NormalizedPackageConfig {
	return types.NormalizedPackageConfig{
		Name:     name,
		Path:     filepath.Dir(manifestRel),
		Type:     types.PackageNode,
		Manifest: manifestRel,
	}
}

// baseConfig returns a valid single-package node config.
func baseConfig(manifestRel string) *types.NormalizedConfig {
	return &types.NormalizedConfig{
		Branches:             []string{"main"},
		TagFormat:            "v${version}",
		IndependentTagFormat: "${name}@v${version}",
		Packages:             []types.NormalizedPackageConfig{nodePkg("app", manifestRel)},
		GitHub:               types.GitHubSettings{Enabled: true, Releases: true},
		OutputDir:            ".hooversion",
		Push:                 true,
	}
}

func writePackageJSON(t *testing.T, dir, manifestRel, version string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, manifestRel),
		"{\"name\":\"app\",\"version\":\""+version+"\"}\n")
}

// envStub returns a getenv func backed by m.
func envStub(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

const noTokenWarning = "GITHUB_TOKEN or GH_TOKEN is not set; `release` cannot create GitHub releases."

// neutralizeCIEnv clears the CI variables CurrentBranch and the token probe
// consult so local test runs are deterministic.
func neutralizeCIEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_HEAD_REF", "")
	t.Setenv("GITHUB_REF_NAME", "")
	t.Setenv("GITHUB_REF_TYPE", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
}

// --- config validation ------------------------------------------------------

func TestConfigValidationErrorsAndEarlyReturn(t *testing.T) {
	// Neither branches nor packages: both errors, in order.
	result, err := RunDoctor(t.TempDir(), &types.NormalizedConfig{}, envStub(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantErrors := []string{
		"Config must define at least one non-empty release branch.",
		"Config must define at least one package.",
	}
	if strings.Join(result.Errors, "\n") != strings.Join(wantErrors, "\n") {
		t.Fatalf("errors = %#v, want %#v", result.Errors, wantErrors)
	}
	if len(result.Warnings) != 0 || len(result.Infos) != 0 {
		t.Fatalf("expected empty warnings/infos, got %#v / %#v", result.Warnings, result.Infos)
	}
}

func TestBlankBranchErrorSkipsGitProbe(t *testing.T) {
	// A blank branch entry must short-circuit before the git checks even in a
	// non-git directory.
	cfg := baseConfig("package.json")
	cfg.Branches = []string{"main", "   "}
	result, err := RunDoctor(t.TempDir(), cfg, envStub(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"Config must define at least one non-empty release branch."}
	if strings.Join(result.Errors, "\n") != strings.Join(want, "\n") {
		t.Fatalf("errors = %#v, want %#v", result.Errors, want)
	}
	if len(result.Warnings) != 0 || len(result.Infos) != 0 {
		t.Fatalf("expected early return without further findings, got %#v / %#v", result.Warnings, result.Infos)
	}
}

// --- repository state errors ------------------------------------------------

func TestNotGitRepository(t *testing.T) {
	result, err := RunDoctor(t.TempDir(), baseConfig("package.json"), envStub(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"Current directory is not a git repository."}
	if strings.Join(result.Errors, "\n") != strings.Join(want, "\n") {
		t.Fatalf("errors = %#v, want %#v", result.Errors, want)
	}
	if len(result.Warnings) != 0 || len(result.Infos) != 0 {
		t.Fatalf("expected early return, got %#v / %#v", result.Warnings, result.Infos)
	}
}

func TestUnbornHead(t *testing.T) {
	dir := t.TempDir()
	runRepoCmd(t, dir, "init", "-q", "-b", "main")
	runRepoCmd(t, dir, "config", "user.email", "test@example.com")
	runRepoCmd(t, dir, "config", "user.name", "Test User")

	result, err := RunDoctor(dir, baseConfig("package.json"), envStub(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"Repository has no resolvable HEAD commit."}
	if strings.Join(result.Errors, "\n") != strings.Join(want, "\n") {
		t.Fatalf("errors = %#v, want %#v", result.Errors, want)
	}
}

// --- branch check -------------------------------------------------------------

func TestConfiguredBranchInfo(t *testing.T) {
	neutralizeCIEnv(t)
	dir := initRepo(t)
	writePackageJSON(t, dir, "package.json", "0.1.0")

	result, err := RunDoctor(dir, baseConfig("package.json"), envStub(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, info := range result.Infos {
		if info == "Release branch: main" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing 'Release branch: main' in infos %#v", result.Infos)
	}
	for _, w := range result.Warnings {
		if strings.Contains(w, "not a configured release branch") {
			t.Fatalf("unexpected unconfigured-branch warning %q", w)
		}
	}
}

func TestUnconfiguredBranchWarning(t *testing.T) {
	neutralizeCIEnv(t)
	dir := initRepo(t)
	runRepoCmd(t, dir, "checkout", "-q", "-b", "feature/x")
	writePackageJSON(t, dir, "package.json", "0.1.0")

	cfg := baseConfig("package.json")
	result, err := RunDoctor(dir, cfg, envStub(map[string]string{"GITHUB_TOKEN": "t"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantWarn := "Current branch 'feature/x' is not a configured release branch."
	found := false
	for _, w := range result.Warnings {
		if w == wantWarn {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing warning %q in %#v", wantWarn, result.Warnings)
	}
	for _, info := range result.Infos {
		if strings.HasPrefix(info, "Release branch:") {
			t.Fatalf("branch info must be absent when unconfigured: %#v", result.Infos)
		}
	}
}

// --- per-package checks --------------------------------------------------------

func TestPackageWithoutTag(t *testing.T) {
	neutralizeCIEnv(t)
	dir := initRepo(t)
	writePackageJSON(t, dir, "package.json", "1.2.3")

	result, err := RunDoctor(dir, baseConfig("package.json"), envStub(map[string]string{"GH_TOKEN": "g"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantInfos := []string{
		"Release branch: main",
		"app: manifest version 1.2.3",
	}
	if strings.Join(result.Infos, "\n") != strings.Join(wantInfos, "\n") {
		t.Fatalf("infos = %#v, want %#v", result.Infos, wantInfos)
	}
	wantWarnings := []string{"app: no release tag found; first release will use full reachable history."}
	if strings.Join(result.Warnings, "\n") != strings.Join(wantWarnings, "\n") {
		t.Fatalf("warnings = %#v, want %#v", result.Warnings, wantWarnings)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors %#v", result.Errors)
	}
}

func TestPackageWithMatchingTag(t *testing.T) {
	neutralizeCIEnv(t)
	dir := initRepo(t)
	writePackageJSON(t, dir, "package.json", "1.2.3")
	runRepoCmd(t, dir, "tag", "v1.2.3")

	result, err := RunDoctor(dir, baseConfig("package.json"), envStub(map[string]string{"GITHUB_TOKEN": "t"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantInfos := []string{
		"Release branch: main",
		"app: manifest version 1.2.3",
		"app: latest tag v1.2.3",
	}
	if strings.Join(result.Infos, "\n") != strings.Join(wantInfos, "\n") {
		t.Fatalf("infos = %#v, want %#v", result.Infos, wantInfos)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", result.Warnings)
	}
}

func TestVersionMismatchWarning(t *testing.T) {
	neutralizeCIEnv(t)
	dir := initRepo(t)
	writePackageJSON(t, dir, "package.json", "1.2.3")
	runRepoCmd(t, dir, "tag", "v1.2.3")
	writePackageJSON(t, dir, "package.json", "1.2.4")
	runRepoCmd(t, dir, "add", "--all")
	runRepoCmd(t, dir, "commit", "-q", "-m", "chore: bump")

	result, err := RunDoctor(dir, baseConfig("package.json"), envStub(map[string]string{"GITHUB_TOKEN": "t"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantWarnings := []string{"app: manifest version 1.2.4 differs from latest tag version 1.2.3."}
	if strings.Join(result.Warnings, "\n") != strings.Join(wantWarnings, "\n") {
		t.Fatalf("warnings = %#v, want %#v", result.Warnings, wantWarnings)
	}
}

func TestMultiPackageIndependentTagPattern(t *testing.T) {
	neutralizeCIEnv(t)
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "a", "package.json"), "{\"name\":\"a\",\"version\":\"1.0.0\"}\n")
	writeFile(t, filepath.Join(dir, "b", "package.json"), "{\"name\":\"b\",\"version\":\"2.0.0\"}\n")
	runRepoCmd(t, dir, "add", "--all")
	runRepoCmd(t, dir, "commit", "-q", "-m", "chore: packages")
	runRepoCmd(t, dir, "tag", "a@v1.0.0")

	cfg := baseConfig("a/package.json")
	cfg.Packages = []types.NormalizedPackageConfig{nodePkg("a", "a/package.json"), nodePkg("b", "b/package.json")}

	result, err := RunDoctor(dir, cfg, envStub(map[string]string{"GITHUB_TOKEN": "t"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(result.Infos, "\n")
	for _, want := range []string{"a: latest tag a@v1.0.0", "a: manifest version 1.0.0", "b: manifest version 2.0.0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing info %q in %#v", want, result.Infos)
		}
	}
	wantB := "b: no release tag found; first release will use full reachable history."
	if !strings.Contains(strings.Join(result.Warnings, "\n"), wantB) {
		t.Fatalf("missing warning %q in %#v", wantB, result.Warnings)
	}
	for _, w := range result.Warnings {
		if strings.HasPrefix(w, "a:") && strings.Contains(w, "no release tag") {
			t.Fatalf("tagged package a must not warn about missing tags: %#v", result.Warnings)
		}
	}
}

// --- token check ----------------------------------------------------------------

func TestGitHubTokenWarningVariants(t *testing.T) {
	neutralizeCIEnv(t)
	dir := initRepo(t)
	writePackageJSON(t, dir, "package.json", "1.0.0")
	runRepoCmd(t, dir, "tag", "v1.0.0")
	cfg := baseConfig("package.json")

	cases := []struct {
		name    string
		env     map[string]string
		release bool
		want    bool
	}{
		{"no tokens", map[string]string{}, true, true},
		{"github token", map[string]string{"GITHUB_TOKEN": "t"}, true, false},
		{"gh token", map[string]string{"GH_TOKEN": "g"}, true, false},
		{"both tokens", map[string]string{"GITHUB_TOKEN": "t", "GH_TOKEN": "g"}, true, false},
		{"releases disabled", map[string]string{}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := *cfg
			c.GitHub.Releases = tc.release
			result, err := RunDoctor(dir, &c, envStub(tc.env))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			found := false
			for _, w := range result.Warnings {
				if w == noTokenWarning {
					found = true
				}
			}
			if found != tc.want {
				t.Fatalf("token warning present=%v, want %v (warnings %#v)", found, tc.want, result.Warnings)
			}
		})
	}
}

// --- ordering + extraction -------------------------------------------------------

func TestFullOutputOrdering(t *testing.T) {
	neutralizeCIEnv(t)
	dir := initRepo(t)
	writePackageJSON(t, dir, "package.json", "1.2.3")
	runRepoCmd(t, dir, "tag", "v1.2.3")
	writePackageJSON(t, dir, "package.json", "1.2.4")
	runRepoCmd(t, dir, "add", "--all")
	runRepoCmd(t, dir, "commit", "-q", "-m", "chore: bump")

	result, err := RunDoctor(dir, baseConfig("package.json"), envStub(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantInfos := []string{
		"Release branch: main",
		"app: manifest version 1.2.4",
		"app: latest tag v1.2.3",
	}
	wantWarnings := []string{
		"app: manifest version 1.2.4 differs from latest tag version 1.2.3.",
		noTokenWarning,
	}
	if strings.Join(result.Infos, "\n") != strings.Join(wantInfos, "\n") {
		t.Fatalf("infos = %#v, want %#v", result.Infos, wantInfos)
	}
	if strings.Join(result.Warnings, "\n") != strings.Join(wantWarnings, "\n") {
		t.Fatalf("warnings = %#v, want %#v", result.Warnings, wantWarnings)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors %#v", result.Errors)
	}
}

func TestExtractTagVersion(t *testing.T) {
	cases := map[string]string{
		"v1.2.3":              "1.2.3",
		"a@v1.2.3":            "1.2.3",
		"a@v1.2.3-rc.1":       "1.2.3-rc.1",
		"a@v1.2.3+build.7":    "1.2.3+build.7",
		"a@v01.2.3":           "01.2.3", // \d+ admits leading zeros, like JS
		"v1.2":                "",
		"1.2.3":               "",
		"av1.2.3":             "",
		"a@v1.2.3 extra":      "",
		"a@va@v1.2.3":         "1.2.3",
		"x@v1.2.3-rc.1 extra": "",
	}
	for tag, want := range cases {
		if got := extractTagVersion(tag); got != want {
			t.Errorf("extractTagVersion(%q) = %q, want %q", tag, got, want)
		}
	}
}

// --- error propagation -------------------------------------------------------------

func TestMissingManifestPropagatesError(t *testing.T) {
	neutralizeCIEnv(t)
	dir := initRepo(t)
	// package.json intentionally absent: TS runDoctor throws; Go returns err.
	if _, err := RunDoctor(dir, baseConfig("package.json"), envStub(map[string]string{"GITHUB_TOKEN": "t"})); err == nil {
		t.Fatal("expected an error for a missing manifest, got nil")
	}
}
