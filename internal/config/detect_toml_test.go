package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCargoDiscoveryGlobsExclusionsAndQuotedComments(t *testing.T) {
	dir := t.TempDir()
	root := `# [package] fake declaration
["workspace"] # workspace
members = [
    'crates/*', # "not-a-member"
    "crates/a",
]
exclude=['crates/excluded']
`
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(root), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "excluded"} {
		path := filepath.Join(dir, "crates", name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "Cargo.toml"), []byte("[\"package\"] # package\n'name' = '"+name+"'\nversion='1'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkgs, err := DetectPackages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 || pkgs[0].Name != "a" || pkgs[1].Name != "b" {
		t.Fatal(pkgs)
	}
}
func TestCargoMissingMemberAndInvalidTOMLRejected(t *testing.T) {
	for _, content := range []string{"[workspace]\nmembers=['missing']\n", "[workspace]\nmembers=[1]\n", "[workspace]\nmembers=['crates/*'\n"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := DetectPackages(dir); err == nil {
			t.Fatalf("accepted %s", content)
		}
	}
}
func TestCargoSymlinkMemberRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[workspace]\nmembers=['member']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "member")); err != nil {
		t.Skip(err)
	}
	if _, err := DetectPackages(dir); err == nil {
		t.Fatal("accepted symlink member")
	}
}
func TestTOMLDiscoveryRejectsOversizedInput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("#"+strings.Repeat("a", maxPackageJSONBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DetectPackages(dir); err == nil {
		t.Fatal("accepted oversized TOML")
	}
}

func TestCargoRecursiveGlobDiscovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[workspace]\nmembers=['crates/**/pkg-*']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"crates/pkg-a", "crates/group/pkg-b"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "Cargo.toml"), []byte("[package]\nname='"+filepath.Base(name)+"'\nversion='1'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkgs, err := DetectPackages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatal(pkgs)
	}
}
func TestNestedPythonProjectsAndPoetrySkipEnvironment(t *testing.T) {
	dir := t.TempDir()
	for _, entry := range []struct{ path, content string }{{"services/a/pyproject.toml", "[project]\nname='a'\nversion='1'\n"}, {"services/b/pyproject.toml", "[tool.poetry]\nname='b'\nversion='1'\n"}, {".venv/pkg/pyproject.toml", "invalid toml"}, {"tools/pyproject.toml", "[tool.ruff]\nline-length=100\n"}} {
		path := filepath.Join(dir, entry.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(entry.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkgs, err := DetectPackages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 || pkgs[0].Name != "a" || pkgs[1].Name != "b" {
		t.Fatal(pkgs)
	}
}
