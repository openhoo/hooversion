package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooversion/internal/safefs"
	"github.com/openhoo/hooversion/internal/types"
)

func TestPythonEcosystemFixturePreservesMarkersExtrasAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pyproject.toml")
	content := `["project"] # PEP 621 metadata
"name" = "owner"
version = '1.0.0'
dependencies = [
    'my_pkg[http]>=1.0.0; python_version >= "3.10"', # ] and "my_pkg==9" are comments
    "my.pkg ~= 1.0.0; sys_platform == 'linux'",
]
[project.optional-dependencies]
"test suite" = ["my-pkg"]
[dependency-groups]
qa = ["my_pkg==1.0.0"]
[tool.poetry.dependencies]
"my-pkg" = { version = "^1.0.0", extras = ["http"], optional = true }
[tool.poetry.group.test.dependencies."my_pkg"]
version = '~1.0.0'
path = '../my_pkg'
`
	mustWrite(t, path, content)
	if err := UpdateLocalDependencyVersions(dir, pkg("owner", types.PackagePython, "pyproject.toml", []string{"MY.PKG"}), released("my-pkg", "2.0.0")); err != nil {
		t.Fatal(err)
	}
	out := mustRead(t, path)
	for _, text := range []string{`my_pkg[http]>=2.0.0; python_version >= "3.10"`, `my.pkg~=2.0.0; sys_platform == 'linux'`, `"my-pkg==2.0.0"`, `version = "^2.0.0"`, `version = '~2.0.0'`, `# ] and "my_pkg==9" are comments`, `path = '../my_pkg'`} {
		if !strings.Contains(out, text) {
			t.Errorf("missing %q:\n%s", text, out)
		}
	}
}
func TestPythonCompoundBoundsExclusionsAndParenthesesPinReleasedVersion(t *testing.T) {
	for _, spec := range []string{">=1,<2", "!=1", "<2", "<=2", ">2", "(>=1, !=1.5, <2)", "==1.*"} {
		t.Run(spec, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "pyproject.toml")
			content := "[project]\nname='owner'\nversion='1'\ndependencies=['local[extra]" + spec + `; python_version >= "3.10"']` + "\n"
			mustWrite(t, path, content)
			if err := UpdateLocalDependencyVersions(dir, pkg("owner", types.PackagePython, "pyproject.toml", []string{"local"}), released("local", "3.0.0")); err != nil {
				t.Fatal(err)
			}
			if got := mustRead(t, path); !strings.Contains(got, `local[extra]==3.0.0; python_version >= "3.10"`) {
				t.Fatal(got)
			}
		})
	}
}
func TestPythonMalformedRequirementsRejectBeforeMutation(t *testing.T) {
	for _, spec := range []string{">=1, garbage", ">=1,", "@ https://example.invalid/local.whl"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "pyproject.toml")
		content := "[project]\nname='owner'\nversion='1'\ndependencies=['local" + spec + "']\n"
		mustWrite(t, path, content)
		if err := UpdateLocalDependencyVersions(dir, pkg("owner", types.PackagePython, "pyproject.toml", []string{"local"}), released("local", "3.0.0")); err == nil {
			t.Fatal("accepted malformed/direct requirement")
		}
		if got := mustRead(t, path); got != content {
			t.Fatal("changed on rejection")
		}
	}
}
func TestRustAliasesQuotedTablesAndCommentedInheritance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Cargo.toml")
	content := `[package] # package
name='owner'
version='1.0.0'
[dependencies] # aliases
"renamed" = {package='local', version='1.0.0', path='../local'} # workspace = true
[target.'cfg(unix)'.dev-dependencies."second alias"]
package = 'local'
version = '1.0.0' # { preserve brace
[workspace.dependencies]
"renamed" = {package='local', version='1.0.0', path='../local'}
`
	mustWrite(t, path, content)
	if err := UpdateLocalDependencyVersions(dir, pkg("owner", types.PackageRust, "Cargo.toml", []string{"local"}), released("local", "2.0.0")); err != nil {
		t.Fatal(err)
	}
	out := mustRead(t, path)
	if strings.Count(out, "version='2.0.0'") != 2 || !strings.Contains(out, "version = '2.0.0' # { preserve brace") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "# workspace = true") {
		t.Fatal("lost comment")
	}
}
func TestCargoInheritedVersionCoordinatedAndHistorical(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Cargo.toml"), "[workspace]\nmembers=['crates/a','crates/b']\n[workspace.package]\nversion='1.0.0' # shared\n")
	var packages []types.NormalizedPackageConfig
	for _, name := range []string{"a", "b"} {
		p := pkg(name, types.PackageRust, filepath.Join("crates", name, "Cargo.toml"), nil)
		packages = append(packages, p)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p.Manifest)), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, p.Manifest), "[package]\nname='"+name+"'\nversion.workspace=true\n")
	}
	releases := []types.PackageRelease{{Package: packages[0], NextVersion: "2.0.0"}}
	if err := ValidateVersionUpdates(dir, releases, packages); err == nil {
		t.Fatal("allowed partial shared update")
	}
	releases = append(releases, types.PackageRelease{Package: packages[1], NextVersion: "3.0.0"})
	if err := ValidateVersionUpdates(dir, releases, packages); err == nil {
		t.Fatal("allowed conflicting versions")
	}
	releases[1].NextVersion = "2.0.0"
	if err := ValidateVersionUpdates(dir, releases, packages); err != nil {
		t.Fatal(err)
	}
	root, err := safefs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	p := packages[0]
	if name, version, err := ReadWithFS(p, root); err != nil || name != "a" || version != "1.0.0" {
		t.Fatalf("read %q %q %v", name, version, err)
	}
	if err := UpdateVersionWithFS(p, "2.0.0", root); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, filepath.Join(dir, p.Manifest)); !strings.Contains(got, "version.workspace=true") {
		t.Fatal(got)
	}
	if got := mustRead(t, filepath.Join(dir, "Cargo.toml")); !strings.Contains(got, "version='2.0.0' # shared") {
		t.Fatal(got)
	}
	data := []byte("[package]\nname='a'\nversion={workspace=true}\n")
	if _, version, err := ReadDataWithWorkspace(p, data, []byte("[workspace.package]\nversion='0.5.0'\n")); err != nil || version != "0.5.0" {
		t.Fatalf("historical version=%s err=%v", version, err)
	}
	paths, err := ManagedPaths(dir, packages)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Cargo.toml", "Cargo.lock", "crates/a/Cargo.toml", "crates/b/Cargo.toml"} {
		if !strings.Contains(strings.Join(paths, "\n"), expected) {
			t.Fatal(paths)
		}
	}
}
func TestCargoLockInlineArraysKeepRegistryIdentity(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname='owner'\nversion='1'\n[dependencies]\nlocal='1'\n")
	lock := `version=3
[[package]] # local package
name='local'
version='1'
dependencies=['local 1', 'local 1 (registry+https://example.invalid/index)']
[[package]]
name='local'
version='1'
source='registry+https://example.invalid/index'
`
	mustWrite(t, filepath.Join(dir, "Cargo.lock"), lock)
	if err := UpdateLocalDependencyVersions(dir, pkg("owner", types.PackageRust, "Cargo.toml", []string{"local"}), released("local", "2")); err != nil {
		t.Fatal(err)
	}
	out := mustRead(t, filepath.Join(dir, "Cargo.lock"))
	if strings.Count(out, "version='1'") != 1 || !strings.Contains(out, "dependencies=['local 2', 'local 1 (registry+") {
		t.Fatal(out)
	}
}
func TestRootedManifestRejectsSymlinkMutation(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "pyproject.toml"), "[project]\nname='owner'\nversion='1'\n")
	if err := os.Symlink(outside, filepath.Join(dir, "sub")); err != nil {
		t.Skip(err)
	}
	root, err := safefs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := UpdateVersionWithFS(pkg("owner", types.PackagePython, "sub/pyproject.toml", nil), "2", root); err == nil {
		t.Fatal("followed outside symlink")
	}
}

func TestRootedStandaloneNestedCargoLockLocation(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "standalone"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "standalone", "Cargo.toml"), "[package]\nname='owner'\nversion='1'\n[dependencies]\nlocal='1'\n")
	mustWrite(t, filepath.Join(dir, "standalone", "Cargo.lock"), "version=3\n[[package]]\nname='owner'\nversion='1'\n[[package]]\nname='local'\nversion='1'\n")
	root, err := safefs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	p := pkg("owner", types.PackageRust, "standalone/Cargo.toml", []string{"local"})
	p.Manifest = filepath.Join(dir, p.Manifest)
	if err := UpdateVersionWithFS(p, "2", root); err != nil {
		t.Fatal(err)
	}
	p.Manifest = "standalone/Cargo.toml"
	if err := UpdateLocalDependencyVersionsWithFS(dir, p, released("local", "3"), root); err != nil {
		t.Fatal(err)
	}
	lock := mustRead(t, filepath.Join(dir, "standalone", "Cargo.lock"))
	if !strings.Contains(lock, "version='2'") || !strings.Contains(lock, "version='3'") {
		t.Fatal(lock)
	}
	paths, err := ManagedPathsWithFS(dir, []types.NormalizedPackageConfig{p}, root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(paths, "\n"), "\nCargo.lock") || !strings.Contains(strings.Join(paths, "\n"), "standalone/Cargo.lock") {
		t.Fatal(paths)
	}
}
func TestInheritedUpdateRejectsUnconfiguredWorkspaceMember(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Cargo.toml"), "[workspace]\nmembers=['a','b']\n[workspace.package]\nversion='1'\n")
	for _, name := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, name, "Cargo.toml"), "[package]\nname='"+name+"'\nversion.workspace=true\n")
	}
	p := pkg("a", types.PackageRust, "a/Cargo.toml", nil)
	if err := ValidateVersionUpdates(dir, []types.PackageRelease{{Package: p, NextVersion: "2"}}, []types.NormalizedPackageConfig{p}); err == nil || !strings.Contains(err.Error(), "unconfigured") {
		t.Fatalf("error=%v", err)
	}
}
func TestLegacyPoetryMetadataReadAndVersionUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pyproject.toml")
	mustWrite(t, path, "[tool.poetry] # old Poetry project\nname='owner'\nversion='1.0.0' # retain\n")
	p := pkg("owner", types.PackagePython, path, nil)
	if name, version, err := Read(p); err != nil || name != "owner" || version != "1.0.0" {
		t.Fatalf("%s %s %v", name, version, err)
	}
	if err := UpdateVersion(p, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, path); !strings.Contains(got, "version='2.0.0' # retain") {
		t.Fatal(got)
	}
}
func TestCargoWorkspaceInheritedDependencyAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "owner"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "Cargo.toml"), "[workspace]\nmembers=['owner']\n[workspace.dependencies]\nrenamed={package='local',version='1',path='local'}\n")
	mustWrite(t, filepath.Join(dir, "owner", "Cargo.toml"), "[package]\nname='owner'\nversion='1'\n[dependencies]\nrenamed={workspace=true}\n")
	if err := UpdateLocalDependencyVersions(dir, pkg("owner", types.PackageRust, "owner/Cargo.toml", []string{"local"}), released("local", "2")); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, filepath.Join(dir, "Cargo.toml")); !strings.Contains(got, "version='2'") {
		t.Fatal(got)
	}
	if got := mustRead(t, filepath.Join(dir, "owner", "Cargo.toml")); !strings.Contains(got, "renamed={workspace=true}") {
		t.Fatal(got)
	}
}
func TestPoetryCompoundVersionsBecomeExactPins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pyproject.toml")
	mustWrite(t, path, "[tool.poetry]\nname='owner'\nversion='1'\n[tool.poetry.dependencies]\nlocal={version='>=1, <2',optional=true}\n[tool.poetry.group.qa.dependencies]\nlocal='^1 || ^2'\n")
	if err := UpdateLocalDependencyVersions(dir, pkg("owner", types.PackagePython, "pyproject.toml", []string{"local"}), released("local", "3.0.0")); err != nil {
		t.Fatal(err)
	}
	got := mustRead(t, path)
	if !strings.Contains(got, "version='3.0.0',optional=true") || !strings.Contains(got, "local='3.0.0'") {
		t.Fatal(got)
	}
}
