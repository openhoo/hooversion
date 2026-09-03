package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooversion/internal/types"
)

func pkg(name string, typ types.PackageType, manifest string, dependencies []string) types.NormalizedPackageConfig {
	return types.NormalizedPackageConfig{
		Name:         name,
		Path:         ".",
		Type:         typ,
		Manifest:     manifest,
		Changelog:    "CHANGELOG.md",
		Scopes:       []string{name},
		Dependencies: dependencies,
		Assets:       []string{},
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func released(pairs ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

// Port of "updates only declared Node dependency edges".
func TestNodeOnlyDeclaredEdges(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "package.json"),
		`{"name":"owner","version":"1.0.0","dependencies":{"local":"^1.0.0","unrelated":"1.0.0"},"description":"local 1.0.0","scripts":{"local":"1.0.0"}}`)
	mustWrite(t, filepath.Join(cwd, "local.json"), `{"name":"local","version":"1.0.0"}`)

	versions := released("local", "2.0.0")
	if err := UpdateLocalDependencyVersions(cwd, pkg("owner", types.PackageNode, "package.json", []string{"local"}), versions); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLocalDependencyVersions(cwd, pkg("local", types.PackageNode, "local.json", nil), versions); err != nil {
		t.Fatal(err)
	}

	raw := mustRead(t, filepath.Join(cwd, "package.json"))
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
		Description  string            `json:"description"`
		Scripts      map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Dependencies["local"] != "^2.0.0" {
		t.Errorf("local = %q, want ^2.0.0", manifest.Dependencies["local"])
	}
	if manifest.Dependencies["unrelated"] != "1.0.0" {
		t.Errorf("unrelated = %q, want untouched", manifest.Dependencies["unrelated"])
	}
	if manifest.Description != "local 1.0.0" || manifest.Scripts["local"] != "1.0.0" {
		t.Errorf("non-dependency fields mutated: %+v", manifest)
	}

	want := "{\n  \"name\": \"owner\",\n  \"version\": \"1.0.0\",\n  \"dependencies\": {\n    \"local\": \"^2.0.0\",\n    \"unrelated\": \"1.0.0\"\n  },\n  \"description\": \"local 1.0.0\",\n  \"scripts\": {\n    \"local\": \"1.0.0\"\n  }\n}\n"
	if raw != want {
		t.Errorf("byte-exact JSON rewrite mismatch:\ngot:\n%s\nwant:\n%s", raw, want)
	}
}

// Port of "updates Python project and optional dependencies".
func TestPythonProjectAndOptional(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "pyproject.toml"),
		"[project]\nname = \"owner\"\nversion = \"1.0.0\"\ndependencies = [\"local>=1.0.0\"]\n\n[project.optional-dependencies]\ndev = [\"local\"]\n")

	err := UpdateLocalDependencyVersions(cwd, pkg("owner", types.PackagePython, "pyproject.toml", []string{"local"}), released("local", "2.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := mustRead(t, filepath.Join(cwd, "pyproject.toml"))
	if !strings.Contains(manifest, "local>=2.0.0") {
		t.Errorf("constraint not rewritten: %s", manifest)
	}
	if !strings.Contains(manifest, "local==2.0.0") {
		t.Errorf("bare requirement not appended: %s", manifest)
	}
}

// Port of "updates every Rust dependency table but preserves workspace
// inheritance".
func TestRustAllTablesPreserveWorkspaceInheritance(t *testing.T) {
	cwd := t.TempDir()
	cargoToml := `[package]
name = "owner"
version = "1.0.0"

[dependencies]
local = { version = "1.0.0", path = "../local" }
unrelated = "1.0.0"

[dev-dependencies.local]
version = "1.0.0"
path = "../local"

[target.'cfg(unix)'.dependencies.local]
version = "1.0.0"
path = "../local"

[workspace.dependencies.local]
version = "1.0.0"

[build-dependencies]
workspace-local = { workspace = true }
`
	mustWrite(t, filepath.Join(cwd, "Cargo.toml"), cargoToml)

	versions := released("local", "2.0.0", "workspace-local", "3.0.0")
	if err := UpdateLocalDependencyVersions(cwd, pkg("owner", types.PackageRust, "Cargo.toml", []string{"local", "workspace-local"}), versions); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLocalDependencyVersions(cwd, pkg("local", types.PackageRust, filepath.Join("local", "Cargo.toml"), nil), versions); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLocalDependencyVersions(cwd, pkg("workspace-local", types.PackageRust, filepath.Join("workspace-local", "Cargo.toml"), nil), versions); err != nil {
		t.Fatal(err)
	}

	manifest := mustRead(t, filepath.Join(cwd, "Cargo.toml"))
	if got := strings.Count(manifest, "version = \"2.0.0\""); got != 4 {
		t.Errorf("version = \"2.0.0\" count = %d, want 4:\n%s", got, manifest)
	}
	if !strings.Contains(manifest, "workspace-local = { workspace = true }") {
		t.Errorf("workspace inheritance not preserved:\n%s", manifest)
	}
	if !strings.Contains(manifest, "[workspace.dependencies.local]\nversion = \"2.0.0\"") {
		t.Errorf("workspace root dependency not updated:\n%s", manifest)
	}
	if !strings.Contains(manifest, "unrelated = \"1.0.0\"") {
		t.Errorf("unrelated entry mutated:\n%s", manifest)
	}
}

// Port of "updates local Cargo.lock package versions without registry records".
func TestCargoLockUpdatesSourcelessBlocks(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "Cargo.toml"),
		"[package]\nname = \"owner\"\nversion = \"1.0.0\"\n\n[dependencies]\nlocal = { path = \"local\", version = \"1.0.0\" }\n")
	mustWrite(t, filepath.Join(cwd, "Cargo.lock"),
		"[[package]]\nname = \"owner\"\nversion = \"1.0.0\"\ndependencies = [\n \"local 1.0.0\",\n]\n\n"+
			"[[package]]\nname = \"local\"\nversion = \"1.0.0\"\ndependencies = [\n \"other 1.0.0\",\n]\n\n"+
			"[[package]]\nname = \"local\"\nversion = \"1.0.0\"\nsource = \"registry+https://example.invalid\"\n")

	if err := UpdateLocalDependencyVersions(cwd, pkg("owner", types.PackageRust, "Cargo.toml", []string{"local"}), released("local", "2.0.0")); err != nil {
		t.Fatal(err)
	}
	lock := mustRead(t, filepath.Join(cwd, "Cargo.lock"))
	if !strings.Contains(lock, "name = \"local\"\nversion = \"2.0.0\"") {
		t.Errorf("source-less block not updated:\n%s", lock)
	}
	if !strings.Contains(lock, "name = \"local\"\nversion = \"1.0.0\"\nsource") {
		t.Errorf("registry block must keep its version:\n%s", lock)
	}
	if !strings.Contains(lock, " \"local 2.0.0\",") {
		t.Errorf("dependency entry not updated:\n%s", lock)
	}
	if got := strings.Count(lock, "version = \"1.0.0\""); got != 2 {
		t.Errorf("owner and registry blocks must keep 1.0.0, got %d:\n%s", got, lock)
	}
}

// Port of "allows a missing Cargo.lock".
func TestMissingCargoLockAllowed(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "Cargo.toml"),
		"[package]\nname = \"owner\"\nversion = \"1.0.0\"\n\n[dependencies]\nlocal = { path = \"local\", version = \"1.0.0\" }\n")
	mustWrite(t, filepath.Join(cwd, "local.Cargo.toml"), "[package]\nname = \"local\"\nversion = \"1.0.0\"\n")

	if err := UpdateLocalDependencyVersions(cwd, pkg("owner", types.PackageRust, "Cargo.toml", []string{"local"}), released("local", "2.0.0")); err != nil {
		t.Fatalf("missing Cargo.lock must be tolerated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "Cargo.lock")); !os.IsNotExist(err) {
		t.Errorf("Cargo.lock must not be created: %v", err)
	}
}

// Port of "rejects a symlinked Cargo.lock".
func TestSymlinkedCargoLockRejected(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "Cargo.toml"),
		"[package]\nname = \"owner\"\nversion = \"1.0.0\"\n\n[dependencies]\nlocal = { path = \"local\", version = \"1.0.0\" }\n")
	mustWrite(t, filepath.Join(cwd, "local.Cargo.toml"), "[package]\nname = \"local\"\nversion = \"1.0.0\"\n")
	mustWrite(t, filepath.Join(cwd, "Cargo.lock.target"), "lock contents\n")
	if err := os.Symlink("Cargo.lock.target", filepath.Join(cwd, "Cargo.lock")); err != nil {
		t.Fatal(err)
	}

	if err := UpdateLocalDependencyVersions(cwd, pkg("owner", types.PackageRust, "Cargo.toml", []string{"local"}), released("local", "2.0.0")); err == nil {
		t.Fatal("symlinked Cargo.lock must be rejected")
	}
	if got := mustRead(t, filepath.Join(cwd, "Cargo.lock.target")); got != "lock contents\n" {
		t.Errorf("symlink target must stay untouched, got %q", got)
	}
}

// Port of "keeps first-class version-file updates plain".
func TestVersionFileUpdatePlain(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "version")
	mustWrite(t, path, "1.0.0\n")

	// Read/UpdateVersion open pkg.Manifest exactly as given (no cwd param).
	if err := UpdateVersion(pkg("image", types.PackageVersionFile, path, nil), "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, path); got != "2.0.0\n" {
		t.Errorf("version file = %q, want 2.0.0\\n", got)
	}
}

func TestReadVariants(t *testing.T) {
	dir := t.TempDir()

	nodePath := filepath.Join(dir, "package.json")
	mustWrite(t, nodePath, `{"name":"app","version":"1.2.3","private":true}`)
	name, version, err := Read(pkg("app", types.PackageNode, nodePath, nil))
	if err != nil || name != "app" || version != "1.2.3" {
		t.Fatalf("node read = (%q,%q,%v)", name, version, err)
	}

	rustPath := filepath.Join(dir, "Cargo.toml")
	mustWrite(t, rustPath, "[package]\nname = \"crate\"\nversion = \"0.1.0\"\n\n[dependencies]\nx = \"1\"\n")
	name, version, err = Read(pkg("crate", types.PackageRust, rustPath, nil))
	if err != nil || name != "crate" || version != "0.1.0" {
		t.Fatalf("rust read = (%q,%q,%v)", name, version, err)
	}

	pyPath := filepath.Join(dir, "pyproject.toml")
	mustWrite(t, pyPath, "[project]\nname = \"py\"\nversion = \"2.3.4\"\n")
	name, version, err = Read(pkg("py", types.PackagePython, pyPath, nil))
	if err != nil || name != "py" || version != "2.3.4" {
		t.Fatalf("python read = (%q,%q,%v)", name, version, err)
	}

	vfPath := filepath.Join(dir, "version")
	mustWrite(t, vfPath, "  9.9.9\n")
	name, version, err = Read(pkg("image", types.PackageVersionFile, vfPath, nil))
	if err != nil || name != "image" || version != "9.9.9" {
		t.Fatalf("version-file read = (%q,%q,%v)", name, version, err)
	}

	badPath := filepath.Join(dir, "bad.json")
	mustWrite(t, badPath, `{"name":"x"}`)
	if _, _, err := Read(pkg("x", types.PackageNode, badPath, nil)); err == nil || !strings.Contains(err.Error(), "must contain name and version") {
		t.Fatalf("node missing fields error = %v", err)
	}

	emptyPath := filepath.Join(dir, "empty-version")
	mustWrite(t, emptyPath, "\n \n")
	if _, _, err := Read(pkg("y", types.PackageVersionFile, emptyPath, nil)); err == nil || !strings.Contains(err.Error(), "must contain a version") {
		t.Fatalf("empty version-file error = %v", err)
	}

	noSectionPath := filepath.Join(dir, "no-section.toml")
	mustWrite(t, noSectionPath, "[other]\nname = \"z\"\nversion = \"1\"\n")
	if _, _, err := Read(pkg("z", types.PackageRust, noSectionPath, nil)); err == nil || !strings.Contains(err.Error(), "must contain name and version") {
		t.Fatalf("rust missing section error = %v", err)
	}
}

func TestUpdateVersionVariants(t *testing.T) {
	dir := t.TempDir()

	nodePath := filepath.Join(dir, "package.json")
	mustWrite(t, nodePath, "{\"a\":1,\"nested\":{\"keep\":\"order\",\"list\":[true,null,\"x\"]},\"version\":\"0.1.0\",\"z\":\"last\"}")
	if err := UpdateVersion(pkg("m", types.PackageNode, nodePath, nil), "5.0.0"); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": 1,\n  \"nested\": {\n    \"keep\": \"order\",\n    \"list\": [\n      true,\n      null,\n      \"x\"\n    ]\n  },\n  \"version\": \"5.0.0\",\n  \"z\": \"last\"\n}\n"
	if got := mustRead(t, nodePath); got != want {
		t.Errorf("node rewrite mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	rustPath := filepath.Join(dir, "Cargo.toml")
	mustWrite(t, rustPath, "[package]\nname = \"c\"\nversion\t= \"0.1.0\"\n\n[dependencies]\nversion = \"1\"\n")
	if err := UpdateVersion(pkg("c", types.PackageRust, rustPath, nil), "1.2.3"); err != nil {
		t.Fatal(err)
	}
	got := mustRead(t, rustPath)
	if !strings.Contains(got, "version\t= \"1.2.3\"") || strings.Count(got, "1.2.3") != 1 {
		t.Errorf("toml version replace wrong:\n%s", got)
	}

	missingPath := filepath.Join(dir, "missing-version.toml")
	mustWrite(t, missingPath, "[package]\nname = \"c\"\n")
	if err := UpdateVersion(pkg("c", types.PackageRust, missingPath, nil), "1.2.3"); err == nil ||
		!strings.Contains(err.Error(), "does not contain a version field") {
		t.Fatalf("toml missing version error = %v", err)
	}
}

func TestNodeUnsupportedSpecifierAndMissingDep(t *testing.T) {
	dir := t.TempDir()

	specPath := "spec.json"
	mustWrite(t, filepath.Join(dir, specPath), `{"name":"o","version":"1.0.0","dependencies":{"local":"file:../local"}}`)
	err := UpdateLocalDependencyVersions(dir, pkg("o", types.PackageNode, specPath, []string{"local"}), released("local", "2.0.0"))
	if err == nil || !strings.Contains(err.Error(), "has unsupported specifier file:../local") {
		t.Fatalf("unsupported specifier error = %v", err)
	}

	missingPath := "missing.json"
	mustWrite(t, filepath.Join(dir, missingPath), `{"name":"o","version":"1.0.0"}`)
	err = UpdateLocalDependencyVersions(dir, pkg("o", types.PackageNode, missingPath, []string{"local"}), released("local", "2.0.0"))
	if err == nil || !strings.Contains(err.Error(), "declares local dependency local, but it was not found") {
		t.Fatalf("missing dependency error = %v", err)
	}
}

func TestPythonDirectURLRejected(t *testing.T) {
	dir := t.TempDir()
	pyPath := "pyproject.toml"
	mustWrite(t, filepath.Join(dir, pyPath), "[project]\nname = \"p\"\nversion = \"1.0.0\"\ndependencies = [\"local @ https://example.invalid/local.whl\"]\n")
	err := UpdateLocalDependencyVersions(dir, pkg("p", types.PackagePython, pyPath, []string{"local"}), released("local", "2.0.0"))
	if err == nil || !strings.Contains(err.Error(), "has unsupported direct URL syntax") {
		t.Fatalf("direct URL error = %v", err)
	}
}

func TestRustShorthandMultilineInlineAndErrors(t *testing.T) {
	dir := t.TempDir()

	shorthand := "shorthand.toml"
	mustWrite(t, filepath.Join(dir, shorthand),
		"[package]\nname = \"r\"\nversion = \"1.0.0\"\n\n[dev-dependencies]\nlocal = \"1.0.0\"\n")
	if err := UpdateLocalDependencyVersions(dir, pkg("r", types.PackageRust, shorthand, []string{"local"}), released("local", "2.0.0")); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, filepath.Join(dir, shorthand)); !strings.Contains(got, "local = \"2.0.0\"") {
		t.Errorf("shorthand not updated:\n%s", got)
	}

	multiline := "multiline.toml"
	mustWrite(t, filepath.Join(dir, multiline),
		"[package]\nname = \"r\"\nversion = \"1.0.0\"\n\n[dependencies]\nlocal = {\n  version = \"1.0.0\",\n  path = \"../local\",\n}\n")
	if err := UpdateLocalDependencyVersions(dir, pkg("r", types.PackageRust, multiline, []string{"local"}), released("local", "2.0.0")); err != nil {
		t.Fatal(err)
	}
	got := mustRead(t, filepath.Join(dir, multiline))
	if !strings.Contains(got, "version = \"2.0.0\"") || strings.Count(got, "version = \"1.0.0\"") != 1 {
		t.Errorf("multiline inline table not updated:\n%s", got)
	}

	dottedNoVersion := "dotted.toml"
	mustWrite(t, filepath.Join(dir, dottedNoVersion),
		"[package]\nname = \"r\"\nversion = \"1.0.0\"\n\n[dependencies.local]\npath = \"../local\"\n")
	err := UpdateLocalDependencyVersions(dir, pkg("r", types.PackageRust, dottedNoVersion, []string{"local"}), released("local", "2.0.0"))
	if err == nil || !strings.Contains(err.Error(), "dependency local has no supported version field") {
		t.Fatalf("dotted no-version error = %v", err)
	}

	caseInsensitive := "case.json"
	mustWrite(t, filepath.Join(dir, caseInsensitive), `{"name":"o","version":"1.0.0","dependencies":{"LOCAL":"^1.0.0"}}`)
	if err := UpdateLocalDependencyVersions(dir, pkg("o", types.PackageNode, caseInsensitive, []string{"LoCaL"}), released("local", "2.0.0")); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(dir, caseInsensitive))), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Dependencies["LOCAL"] != "^2.0.0" {
		t.Errorf("case-insensitive match failed: %+v", parsed.Dependencies)
	}
}

func TestReadTomlAllowsInlineComments(t *testing.T) {
	dir := t.TempDir()
	for _, content := range []string{
		"[package]\r\nname = \"owner#name\" # package\r\nversion = '1.2.3' # release\r\n",
		"[project]\nname = 'owner'\nversion = \"2.3.4\" # release\n",
	} {
		path := filepath.Join(dir, "manifest.toml")
		mustWrite(t, path, content)
		kind := types.PackageRust
		if strings.Contains(content, "[project]") {
			kind = types.PackagePython
		}
		name, version, err := Read(pkg("owner", kind, path, nil))
		if err != nil {
			t.Fatal(err)
		}
		if name == "" || version == "" {
			t.Fatalf("read %q = %q,%q", content, name, version)
		}
	}
}

func TestReadTomlRejectsMultilineQuotedValues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    types.PackageType
		content string
	}{
		{
			name:    "rust double quote",
			kind:    types.PackageRust,
			content: "[package]\nname = \"owner'\nname\"\nversion = \"1.0.0\"\n",
		},
		{
			name:    "rust single quote",
			kind:    types.PackageRust,
			content: "[package]\nname = 'owner\"\nname'\nversion = '1.0.0'\n",
		},
		{
			name:    "python double quote",
			kind:    types.PackagePython,
			content: "[project]\nname = \"owner'\nname\"\nversion = \"1.0.0\"\n",
		},
		{
			name:    "python single quote",
			kind:    types.PackagePython,
			content: "[project]\nname = 'owner\"\nname'\nversion = '1.0.0'\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.toml")
			mustWrite(t, path, tc.content)
			if _, _, err := Read(pkg("owner", tc.kind, path, nil)); err == nil {
				t.Fatalf("accepted multiline quoted value:\n%s", tc.content)
			}
		})
	}
}
