package plan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooversion/internal/types"
)

func gitOut(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func makeRepo(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	gitOut(t, cwd, "init", "-b", "main")
	gitOut(t, cwd, "config", "user.email", "test@example.com")
	gitOut(t, cwd, "config", "user.name", "Hooversion Test")
	return cwd
}

func commitAll(t *testing.T, cwd, message string) {
	t.Helper()
	gitOut(t, cwd, "add", "--all")
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = cwd
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit %q: %v", message, err)
	}
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

func nodePkg(name, path, manifest string) types.NormalizedPackageConfig {
	return types.NormalizedPackageConfig{Name: name, Path: path, Type: types.PackageNode, Manifest: manifest}
}

func baseConfig(packages ...types.NormalizedPackageConfig) *types.NormalizedConfig {
	return &types.NormalizedConfig{
		Branches:             []string{"main"},
		TagFormat:            "v${version}",
		IndependentTagFormat: "${name}@v${version}",
		Packages:             packages,
		GitHub:               types.GitHubSettings{Enabled: false, Releases: true, ApiUrl: "https://api.github.com"},
		OutputDir:            ".hooversion",
		Push:                 true,
	}
}

func TestPlansSinglePackageSinceLatestTag(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "package.json"), "{\n  \"name\": \"labelhoo\",\n  \"version\": \"0.1.0\"\n}\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "v0.1.0", "-m", "v0.1.0")
	writeFile(t, filepath.Join(cwd, "index.ts"), "export const value = 1;\n")
	commitAll(t, cwd, "feat: add public API")

	config := baseConfig(nodePkg("labelhoo", ".", "package.json"))
	plan, err := CreatePlan(cwd, config, "main", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(plan.Releases))
	}
	release := plan.Releases[0]
	if release.NextVersion != "0.2.0" || release.Tag != "v0.2.0" || release.ReleaseType != types.Minor {
		t.Fatalf("unexpected release: %+v", release)
	}
	wantSha := gitOut(t, cwd, "rev-parse", "HEAD")
	if plan.SourceSha != wantSha {
		t.Fatalf("sourceSha = %q, want %q", plan.SourceSha, wantSha)
	}
}

func TestUsesConfiguredTagFormatForBaseline(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "package.json"), "{\"name\": \"labelhoo\", \"version\": \"0.1.0\"}\n")
	commitAll(t, cwd, "feat: initial import")
	gitOut(t, cwd, "tag", "-a", "release-0.1.0", "-m", "release-0.1.0")
	writeFile(t, filepath.Join(cwd, "index.ts"), "export const value = 1;\n")
	commitAll(t, cwd, "fix: repair public API")

	config := baseConfig(nodePkg("labelhoo", ".", "package.json"))
	config.TagFormat = "release-${version}"
	plan, err := CreatePlan(cwd, config, "main", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(plan.Releases))
	}
	release := plan.Releases[0]
	if release.ReleaseType != types.Patch || release.LatestTag != "release-0.1.0" {
		t.Fatalf("unexpected release: %+v", release)
	}
	if release.Tag != "release-0.1.1" {
		t.Fatalf("tag = %q, want release-0.1.1", release.Tag)
	}
}

func TestPlansFromPlainVersionFile(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "version"), "1.2.3\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "v1.2.3", "-m", "v1.2.3")
	writeFile(t, filepath.Join(cwd, "Dockerfile"), "FROM scratch\n")
	commitAll(t, cwd, "fix(image): repair container metadata")

	config := baseConfig(types.NormalizedPackageConfig{
		Name: "image", Path: ".", Type: types.PackageVersionFile, Manifest: "version",
	})
	plan, err := CreatePlan(cwd, config, "main", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(plan.Releases))
	}
	release := plan.Releases[0]
	if release.CurrentVersion != "1.2.3" || release.NextVersion != "1.2.4" || release.Tag != "v1.2.4" {
		t.Fatalf("unexpected version-file release: %+v", release)
	}
}

func TestRoutesIndependentAndPropagatesDependentsAsPatch(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "Cargo.toml"),
		"[package]\nname = \"hoot\"\nversion = \"0.1.0\"\n\n[dependencies]\nhoot-core = { path = \"crates/hoot-core\", version = \"0.1.0\" }\n")
	writeFile(t, filepath.Join(cwd, "crates", "hoot-plugin-sdk", "Cargo.toml"),
		"[package]\nname = \"hoot-plugin-sdk\"\nversion = \"0.1.0\"\n")
	writeFile(t, filepath.Join(cwd, "crates", "hoot-core", "Cargo.toml"),
		"[package]\nname = \"hoot-core\"\nversion = \"0.1.0\"\n\n[dependencies]\nhoot-plugin-sdk = { path = \"../hoot-plugin-sdk\", version = \"0.1.0\" }\n")
	commitAll(t, cwd, "initial import")
	for _, tag := range []string{"hoot@v0.1.0", "hoot-core@v0.1.0", "hoot-plugin-sdk@v0.1.0"} {
		gitOut(t, cwd, "tag", "-a", tag, "-m", tag)
	}
	writeFile(t, filepath.Join(cwd, "crates", "hoot-plugin-sdk", "lib.rs"), "pub fn parse() {}\n")
	commitAll(t, cwd, "feat(hoot-plugin-sdk): add parser")

	config := baseConfig(
		types.NormalizedPackageConfig{
			Name: "hoot-plugin-sdk", Path: "crates/hoot-plugin-sdk", Type: types.PackageRust,
			Manifest: "crates/hoot-plugin-sdk/Cargo.toml",
		},
		types.NormalizedPackageConfig{
			Name: "hoot-core", Path: "crates/hoot-core", Type: types.PackageRust,
			Manifest:     "crates/hoot-core/Cargo.toml",
			Dependencies: []string{"hoot-plugin-sdk"},
		},
		types.NormalizedPackageConfig{
			Name: "hoot", Path: ".", Type: types.PackageRust,
			Manifest:     "Cargo.toml",
			Dependencies: []string{"hoot-core"},
		},
	)
	plan, err := CreatePlan(cwd, config, "main", nil)
	if err != nil {
		t.Fatal(err)
	}

	releases := map[string]types.PackageRelease{}
	for _, release := range plan.Releases {
		releases[release.Package.Name] = release
	}
	sdk, ok := releases["hoot-plugin-sdk"]
	if !ok || sdk.NextVersion != "0.2.0" || sdk.DependencyTriggered {
		t.Fatalf("sdk release wrong: %+v", sdk)
	}
	core, ok := releases["hoot-core"]
	if !ok || core.NextVersion != "0.1.1" || !core.DependencyTriggered || len(core.Commits) != 0 {
		t.Fatalf("core release wrong: %+v", core)
	}
	hoot, ok := releases["hoot"]
	if !ok || hoot.NextVersion != "0.1.1" || !hoot.DependencyTriggered || len(hoot.Commits) != 0 {
		t.Fatalf("root release wrong: %+v", hoot)
	}
	if len(sdk.Commits) != 1 || sdk.Commits[0].Subject != "feat(hoot-plugin-sdk): add parser" {
		t.Fatalf("sdk commits wrong: %+v", sdk.Commits)
	}
}

func TestNoCascadeWithoutDeclaredDependencyEdge(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "packages", "one", "package.json"), "{\"name\": \"one\", \"version\": \"0.1.0\"}\n")
	writeFile(t, filepath.Join(cwd, "packages", "two", "package.json"), "{\"name\": \"two\", \"version\": \"0.1.0\"}\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "one@v0.1.0", "-m", "one@v0.1.0")
	gitOut(t, cwd, "tag", "-a", "two@v0.1.0", "-m", "two@v0.1.0")
	writeFile(t, filepath.Join(cwd, "packages", "one", "index.ts"), "export const one = true;\n")
	commitAll(t, cwd, "feat(one): add one")

	config := baseConfig(nodePkg("one", "packages/one", "packages/one/package.json"),
		nodePkg("two", "packages/two", "packages/two/package.json"))
	plan, err := CreatePlan(cwd, config, "main", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Releases) != 1 || plan.Releases[0].Package.Name != "one" {
		t.Fatalf("expected only package one to release, got %+v", plan.Releases)
	}
}

func TestUnmatchedReleaseWorthyCommitBlocksPlan(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "packages", "one", "package.json"), "{\"name\": \"one\", \"version\": \"0.1.0\"}\n")
	writeFile(t, filepath.Join(cwd, "packages", "two", "package.json"), "{\"name\": \"two\", \"version\": \"0.1.0\"}\n")
	writeFile(t, filepath.Join(cwd, "README.md"), "hello\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "one@v0.1.0", "-m", "one@v0.1.0")
	gitOut(t, cwd, "tag", "-a", "two@v0.1.0", "-m", "two@v0.1.0")
	writeFile(t, filepath.Join(cwd, "outside.txt"), "changed\n")
	commitAll(t, cwd, "fix: update outside package")

	config := baseConfig(nodePkg("one", "packages/one", "packages/one/package.json"),
		nodePkg("two", "packages/two", "packages/two/package.json"))
	plan, err := CreatePlan(cwd, config, "main", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.UnmatchedCommits) != 1 || len(plan.Releases) != 0 {
		t.Fatalf("unmatched=%d releases=%d, want 1/0", len(plan.UnmatchedCommits), len(plan.Releases))
	}
}

func TestIgnoredSubjectsAreExcluded(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "package.json"), "{\"name\": \"app\", \"version\": \"1.0.0\"}\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	writeFile(t, filepath.Join(cwd, "x.txt"), "x\n")
	commitAll(t, cwd, "chore(release): prepare v1.0.1")
	writeFile(t, filepath.Join(cwd, "y.txt"), "y\n")
	commitAll(t, cwd, "Merge branch 'other'")

	plan, err := CreatePlan(cwd, baseConfig(nodePkg("app", ".", "package.json")), "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Releases) != 0 {
		t.Fatalf("ignored subjects produced a release: %+v", plan.Releases)
	}
}

func TestBreakingCommitOverridesToMajor(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "package.json"), "{\"name\": \"app\", \"version\": \"1.2.3\"}\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "v1.2.3", "-m", "v1.2.3")
	writeFile(t, filepath.Join(cwd, "api.ts"), "export const boom = true;\n")
	commitAll(t, cwd, "feat!: drop legacy API\n\nBREAKING CHANGE: legacy API removed")

	plan, err := CreatePlan(cwd, baseConfig(nodePkg("app", ".", "package.json")), "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Releases) != 1 || plan.Releases[0].NextVersion != "2.0.0" ||
		plan.Releases[0].ReleaseType != types.Major {
		t.Fatalf("breaking override wrong: %+v", plan.Releases)
	}
}

func TestPolicyOverridesBumpMap(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "package.json"), "{\"name\": \"app\", \"version\": \"1.0.0\"}\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	writeFile(t, filepath.Join(cwd, "docs.md"), "docs\n")
	commitAll(t, cwd, "docs: rewrite guide")

	policy := &types.CommitPolicy{ReleaseTypes: map[string]types.ReleaseType{"docs": types.Minor}}
	plan, err := CreatePlan(cwd, baseConfig(nodePkg("app", ".", "package.json")), "main", policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Releases) != 1 || plan.Releases[0].NextVersion != "1.1.0" {
		t.Fatalf("policy mapping wrong: %+v", plan.Releases)
	}

	defaultPlan, err := CreatePlan(cwd, baseConfig(nodePkg("app", ".", "package.json")), "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultPlan.Releases) != 0 {
		t.Fatalf("default rules should ignore docs commits: %+v", defaultPlan.Releases)
	}
}
