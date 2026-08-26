// Tests for the src/config.ts port: assertions are ported from
// tests/config.test.ts and extended for the Go-only YAML/JSON/migrate paths.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooversion/internal/types"
)

func writeJSON(t *testing.T, dir, filename, name string) {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"name":` + quote(name) + `,"version":"1.0.0"}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func mustNormalize(t *testing.T, cwd string, cfg *types.Config) *types.NormalizedConfig {
	t.Helper()
	got, err := Normalize(cwd, cfg)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return got
}

// --- Ported from tests/config.test.ts --------------------------------------

func TestRejectsDuplicateNamesAfterNormalization(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "one.json", "one")
	writeJSON(t, cwd, "two.json", "two")

	_, err := Normalize(cwd, &types.Config{Packages: []types.PackageConfig{
		{Name: "Package", Path: ".", Type: types.PackageNode, Manifest: "one.json"},
		{Name: " package ", Path: ".", Type: types.PackageNode, Manifest: "two.json"},
	}})
	if err == nil || !strings.Contains(err.Error(), "Duplicate package name") {
		t.Fatalf("want duplicate-name error, got %v", err)
	}
}

func TestRejectsUnknownAndSelfDependencyReferences(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "package.json", "owner")

	_, err := Normalize(cwd, &types.Config{Packages: []types.PackageConfig{
		{Name: "owner", Path: ".", Type: types.PackageNode, Manifest: "package.json", Dependencies: []string{"missing"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "unknown package missing") {
		t.Fatalf("want unknown-package error, got %v", err)
	}

	_, err = Normalize(cwd, &types.Config{Packages: []types.PackageConfig{
		{Name: "owner", Path: ".", Type: types.PackageNode, Manifest: "package.json", Dependencies: []string{"owner"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "cannot depend on itself") {
		t.Fatalf("want self-dependency error, got %v", err)
	}
}

func TestRejectsDependencyCycles(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "a/package.json", "a")
	writeJSON(t, cwd, "b/package.json", "b")

	_, err := Normalize(cwd, &types.Config{Packages: []types.PackageConfig{
		{Name: "a", Path: "a", Type: types.PackageNode, Manifest: "a/package.json", Dependencies: []string{"b"}},
		{Name: "b", Path: "b", Type: types.PackageNode, Manifest: "b/package.json", Dependencies: []string{"a"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "cycle detected") {
		t.Fatalf("want cycle error, got %v", err)
	}
	want := "Package dependency cycle detected: a -> b -> a"
	if err.Error() != want {
		t.Fatalf("cycle text mismatch:\n got %q\nwant %q", err.Error(), want)
	}
}

func TestRejectsMixedCaseDependencyCycles(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "a/package.json", "App")
	writeJSON(t, cwd, "b/package.json", "lib")

	_, err := Normalize(cwd, &types.Config{Packages: []types.PackageConfig{
		{Name: "App", Path: "a", Type: types.PackageNode, Manifest: "a/package.json", Dependencies: []string{"lib"}},
		{Name: "lib", Path: "b", Type: types.PackageNode, Manifest: "b/package.json", Dependencies: []string{"App"}},
	}})
	if err == nil || err.Error() != "Package dependency cycle detected: app -> lib -> app" {
		t.Fatalf("want mixed-case cycle detection, got %v", err)
	}
}

func TestRejectsOptionLikeLeadingDashAndControlCharacterRefs(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "package.json", "app")
	for _, branch := range []string{"--upload-pack=evil", "-release", "release\nbranch"} {
		_, err := Normalize(cwd, &types.Config{
			Branches: []string{branch},
			Packages: []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}},
		})
		if err == nil || !strings.Contains(err.Error(), "Invalid Git branch") {
			t.Fatalf("branch %q: want Invalid Git branch error, got %v", branch, err)
		}
	}

	_, err := Normalize(cwd, &types.Config{
		TagFormat: "--upload-pack=${version}",
		Packages:  []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}},
	})
	if err == nil || !strings.Contains(err.Error(), "Invalid Git tag") {
		t.Fatalf("tagFormat: want Invalid Git tag error, got %v", err)
	}
}

func TestAcceptsMainBranchesAndScopedReleaseTagFormats(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "package.json", "app")
	cfg := mustNormalize(t, cwd, &types.Config{
		Branches:  []string{"main"},
		TagFormat: "release/${version}",
		Packages:  []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}},
	})
	if len(cfg.Branches) != 1 || cfg.Branches[0] != "main" {
		t.Fatalf("branches = %v", cfg.Branches)
	}
	if cfg.TagFormat != "release/${version}" {
		t.Fatalf("tagFormat = %q", cfg.TagFormat)
	}
}

// --- Defaults ----------------------------------------------------------------

func TestNormalizeDefaults(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "package.json", "app")
	cfg := mustNormalize(t, cwd, &types.Config{
		Packages: []types.PackageConfig{{Type: types.PackageNode}}, // name falls back to manifest
	})
	if cfg.TagFormat != "v${version}" {
		t.Errorf("tagFormat = %q", cfg.TagFormat)
	}
	if cfg.IndependentTagFormat != "${name}@v${version}" {
		t.Errorf("independentTagFormat = %q", cfg.IndependentTagFormat)
	}
	if cfg.OutputDir != ".hooversion" {
		t.Errorf("outputDir = %q", cfg.OutputDir)
	}
	if !cfg.Push {
		t.Error("push default = false")
	}
	if !cfg.GitHub.Enabled || !cfg.GitHub.Releases || cfg.GitHub.ApiUrl != "https://api.github.com" || cfg.GitHub.Repository != "" {
		t.Errorf("github = %+v", cfg.GitHub)
	}
	pkg := cfg.Packages[0]
	if pkg.Name != "app" || pkg.Manifest != "package.json" || pkg.Changelog != "CHANGELOG.md" {
		t.Errorf("pkg = %+v", pkg)
	}
	if len(pkg.Scopes) != 1 || pkg.Scopes[0] != "app" {
		t.Errorf("scopes = %v", pkg.Scopes)
	}
	if pkg.Dependencies == nil || pkg.Assets == nil ||
		cfg.Hooks.BeforeRelease == nil || cfg.Hooks.AfterVersion == nil || cfg.Hooks.AfterRelease == nil {
		t.Error("expected non-nil empty slices for defaults")
	}
}

func TestNormalizeGitHubDisabledAndPushFalse(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "package.json", "app")
	pushFalse := false
	enabledFalse := false
	cfg := mustNormalize(t, cwd, &types.Config{
		GitHub:   &types.GitHubConfig{Enabled: &enabledFalse},
		Push:     &pushFalse,
		Packages: []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}},
	})
	if cfg.GitHub.Enabled || cfg.GitHub.Releases || cfg.GitHub.ApiUrl != "" {
		t.Errorf("disabled github = %+v", cfg.GitHub)
	}
	if cfg.Push {
		t.Error("push = true")
	}

	cfg = mustNormalize(t, cwd, &types.Config{
		GitHub:   &types.GitHubConfig{Releases: &pushFalse},
		Packages: []types.PackageConfig{{Name: "app", Path: ".", Type: types.PackageNode}},
	})
	if !cfg.GitHub.Enabled || cfg.GitHub.Releases {
		t.Errorf("github releases=false = %+v", cfg.GitHub)
	}
}

func TestNormalizeRequiresPackages(t *testing.T) {
	cwd := t.TempDir()
	_, err := Normalize(cwd, &types.Config{})
	if err == nil || err.Error() != "Config must define at least one package." {
		t.Fatalf("got %v", err)
	}
}

func TestNormalizeRejectsEscapingPath(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "package.json", "app")
	_, err := Normalize(cwd, &types.Config{Packages: []types.PackageConfig{
		{Name: "app", Path: "../outside", Type: types.PackageNode},
	}})
	if err == nil || !strings.Contains(err.Error(), "Path must stay inside the repository") {
		t.Fatalf("got %v", err)
	}
}

// --- FindPath / Load ---------------------------------------------------------

func TestFindPathSearchOrder(t *testing.T) {
	cwd := t.TempDir()
	if path, err := FindPath(cwd); err != nil || path != "" {
		t.Fatalf("empty dir: (%q, %v)", path, err)
	}

	touch := func(names ...string) {
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(cwd, n), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	touch("hooversion.config.json")
	touch("hooversion.json")
	if path, _ := FindPath(cwd); filepath.Base(path) != "hooverversion.config.json" && filepath.Base(path) != "hooversion.config.json" {
		t.Fatalf("json order: %q", path)
	}

	// A YAML variant always wins over JSON.
	touch(".hooversion.yml")
	path, _ := FindPath(cwd)
	if filepath.Base(path) != ".hooversion.yml" {
		t.Fatalf("yaml should win: %q", path)
	}
	touch("hooversion.yml", ".hooversion.yaml")
	path, _ = FindPath(cwd)
	if filepath.Base(path) != "hooversion.yaml" && filepath.Base(path) != ".hooversion.yaml" {
		t.Fatalf("unexpected winner: %q", path)
	}
}

func TestFindPathLegacyConfigError(t *testing.T) {
	cwd := t.TempDir()
	legacy := filepath.Join(cwd, "hooversion.config.ts")
	if err := os.WriteFile(legacy, []byte("export default {};"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := FindPath(cwd)
	var legacyErr *LegacyConfigError
	if !errors.As(err, &legacyErr) {
		t.Fatalf("want LegacyConfigError, got (%q, %v)", path, err)
	}
	if legacyErr.Path != legacy {
		t.Fatalf("path = %q", legacyErr.Path)
	}
	if !strings.Contains(legacyErr.Error(), "migrate") {
		t.Fatalf("hint missing: %v", legacyErr)
	}

	_, err = Load(cwd, "")
	if !errors.As(err, &legacyErr) {
		t.Fatalf("Load should propagate legacy error, got %v", err)
	}
}

func TestLoadJSONAndYAMLDispatch(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "package.json", "app")

	jsonCfg := `{"packages":[{"name":"app","type":"node"}],"outputDir":"custom","push":false}`
	if err := os.WriteFile(filepath.Join(cwd, "hooversion.json"), []byte(jsonCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutputDir != "custom" || cfg.Push {
		t.Errorf("json load = %+v", cfg)
	}

	yamlCfg := "packages:\n  - name: app\n    type: node\nbranches:\n  - main\n  - release\n"
	if err := os.WriteFile(filepath.Join(cwd, "hooversion.yaml"), []byte(yamlCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Branches) != 2 || cfg.OutputDir != ".hooversion" { // yaml wins discovery
		t.Errorf("yaml load = %+v", cfg)
	}

	cfg, err = Load(cwd, "hooversion.json") // explicit wins over yaml
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutputDir != "custom" {
		t.Errorf("explicit load outputDir = %q", cfg.OutputDir)
	}

	missing := t.TempDir()
	_, err = Load(missing, "")
	if err == nil || !strings.Contains(err.Error(), "No hooversion config found") {
		t.Fatalf("missing config error = %v", err)
	}
}

// --- DetectPackages ----------------------------------------------------------

func TestDetectPackagesEcosystemsAndDedupe(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "package.json", "app")
	cargo := "[package]\nname = \"root\"\nversion = \"0.1.0\"\n\n[workspace]\nmembers = [\n  \"crates/one\", # first\n  \"crates/two\",\n]\n"
	if err := os.WriteFile(filepath.Join(cwd, "Cargo.toml"), []byte(cargo), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, cwd, "crates/one/Cargo.toml-not-json", "") // noise; real manifests below
	os.Remove(filepath.Join(cwd, "crates/one/Cargo.toml-not-json"))
	for _, member := range []string{"crates/one", "crates/two"} {
		body := fmt.Sprintf("[package]\nname = %q\nversion = \"0.2.0\"\n", filepath.Base(member))
		if err := os.MkdirAll(filepath.Join(cwd, member), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cwd, member, "Cargo.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, "pyproject.toml"), []byte("[project]\nname = \"py\"\nversion = \"3.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "version"), []byte("9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pkgs, err := DetectPackages(cwd)
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		typ  types.PackageType
		path string
		name string
	}
	var got []key
	for _, p := range pkgs {
		got = append(got, key{p.Type, p.Path, p.Name})
	}
	want := []key{
		{types.PackageNode, ".", "app"},
		{types.PackageRust, ".", "root"},
		{types.PackageRust, "crates/one", "one"},
		{types.PackageRust, "crates/two", "two"},
		{types.PackagePython, ".", "py"},
		{types.PackageVersionFile, ".", filepath.Base(cwd)},
	}
	if len(got) != len(want) {
		t.Fatalf("detected %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDetectPackagesRequiresManifestNames(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "package.json"), []byte(`{"version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := DetectPackages(cwd)
	if err == nil || !strings.Contains(err.Error(), "must contain a name") {
		t.Fatalf("got %v", err)
	}

	cwd = t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "Cargo.toml"), []byte("[dependencies]\nx = \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgs, err := DetectPackages(cwd)
	if err != nil || len(pkgs) != 0 { // no [package], no workspace members
		t.Fatalf("got (%v, %v)", pkgs, err)
	}
}

// --- WriteDefault / DefaultManifestPath -------------------------------------

func TestWriteDefaultRoundTrip(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "package.json", "app")

	path, err := WriteDefault(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(cwd, "hooversion.yaml") {
		t.Fatalf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "releases: true") || !strings.Contains(string(data), "afterVersion: []") {
		t.Fatalf("body missing defaults:\n%s", data)
	}

	cfg, err := Load(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Packages) != 1 || cfg.Packages[0].Name != "app" || cfg.Packages[0].Type != types.PackageNode {
		t.Fatalf("round-trip = %+v", cfg.Packages)
	}
	if cfg.OutputDir != ".hooversion" || !cfg.Push || !cfg.GitHub.Releases {
		t.Errorf("round-trip config = %+v", cfg)
	}
}

func TestWriteDefaultWithoutPackages(t *testing.T) {
	cwd := t.TempDir()
	_, err := WriteDefault(cwd)
	if err == nil || err.Error() != "Could not detect package.json, Cargo.toml, pyproject.toml, or version." {
		t.Fatalf("got %v", err)
	}
}

func TestDefaultManifestPathTable(t *testing.T) {
	cases := []struct {
		typ  types.PackageType
		want string
	}{
		{types.PackageNode, "package.json"},
		{types.PackageRust, "Cargo.toml"},
		{types.PackagePython, "pyproject.toml"},
		{types.PackageVersionFile, "version"},
	}
	for _, c := range cases {
		if got := DefaultManifestPath(c.typ, "."); got != c.want {
			t.Errorf("%s: got %q want %q", c.typ, got, c.want)
		}
	}
	if got := DefaultManifestPath(types.PackageRust, "crates/x"); got != filepath.Join("crates/x", "Cargo.toml") {
		t.Errorf("nested: %q", got)
	}
}

// --- MigrateFromTS -----------------------------------------------------------

func TestMigrateFromTSWithoutBun(t *testing.T) {
	origLook, origRun := bunLookPath, bunRun
	defer func() { bunLookPath, bunRun = origLook, origRun }()

	bunLookPath = func(string) (string, error) { return "", errors.New("exec: not found") }
	bunRun = func(string, string, []string) ([]byte, error) {
		t.Fatal("bunRun must not be called without bun")
		return nil, nil
	}

	cwd := t.TempDir()
	cfg, path, err := MigrateFromTS(cwd, "hooversion.config.ts")
	if err == nil || !strings.Contains(err.Error(), "bun") || !strings.Contains(err.Error(), "manually") {
		t.Fatalf("guidance error = %v", err)
	}
	if cfg != nil || path != "" {
		t.Fatalf("expected nil results, got (%v, %q)", cfg, path)
	}
	if _, statErr := os.Stat(filepath.Join(cwd, "hooversion.yaml")); !os.IsNotExist(statErr) {
		t.Fatal("no yaml should be written on failure")
	}
}

func TestMigrateFromTSConvertsJSONToNormalizedYAML(t *testing.T) {
	origLook, origRun := bunLookPath, bunRun
	defer func() { bunLookPath, bunRun = origLook, origRun }()

	bunLookPath = func(string) (string, error) { return "/usr/local/bin/bun", nil }
	bunRun = func(dir, script string, extraEnv []string) ([]byte, error) {
		if script != migrateScript {
			t.Errorf("unexpected script")
		}
		foundEnv := false
		for _, e := range extraEnv {
			if strings.HasPrefix(e, "HOOVERSION_MIGRATE_PATH=") && strings.HasSuffix(e, "hooversion.config.ts") {
				foundEnv = true
			}
		}
		if !foundEnv {
			t.Errorf("migrate path env missing: %v", extraEnv)
		}
		return []byte(`{"packages":[{"name":"app","type":"node"}],"tagFormat":"release/${version}","github":{"releases":true}}`), nil
	}

	cwd := t.TempDir()
	writeJSON(t, cwd, "package.json", "app")
	cfg, yamlPath, err := MigrateFromTS(cwd, "hooversion.config.ts")
	if err != nil {
		t.Fatal(err)
	}
	if yamlPath != filepath.Join(cwd, "hooversion.yaml") {
		t.Fatalf("yamlPath = %q", yamlPath)
	}
	if cfg.TagFormat != "release/${version}" || cfg.Packages[0].Name != "app" {
		t.Fatalf("normalized = %+v", cfg)
	}

	// The written YAML parses back to the same normalized config.
	reloaded, err := Load(cwd, "hooversion.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.TagFormat != cfg.TagFormat || reloaded.Packages[0].Name != "app" {
		t.Fatalf("reload = %+v", reloaded)
	}
}
