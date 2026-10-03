package release

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooversion/internal/manifest"
	"github.com/openhoo/hooversion/internal/types"
)

func TestInheritedCargoReleaseAndHistoricalRetry(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "Cargo.toml"), "[workspace]\nmembers=['crates/a','crates/b']\n[workspace.package]\nversion='1.0.0'\n")
	config := singleAppConfig(false)
	config.Packages = nil
	for _, name := range []string{"a", "b"} {
		path := filepath.Join("crates", name)
		writeFile(t, filepath.Join(cwd, path, "Cargo.toml"), "[package]\nname='"+name+"'\nversion.workspace=true\n")
		config.Packages = append(config.Packages, types.NormalizedPackageConfig{Name: name, Type: types.PackageRust, Path: path, Manifest: filepath.Join(path, "Cargo.toml"), Changelog: filepath.Join(path, "CHANGELOG.md")})
	}
	commitAll(t, cwd, "initial import")
	for _, name := range []string{"a", "b"} {
		gitOut(t, cwd, "tag", "-a", name+"@v1.0.0", "-m", name+"@v1.0.0")
		writeFile(t, filepath.Join(cwd, "crates", name, "src", "lib.rs"), "pub fn repaired() {}\n")
	}
	commitAll(t, cwd, "fix: repair both crates")
	initial, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Releases) != 2 {
		t.Fatalf("plan=%+v", initial)
	}
	result, err := Execute(cwd, config, initial, Options{NoPushSet: true, NoGitHubSet: true})
	if err != nil || !result.Published {
		t.Fatalf("release=%+v error=%v", result, err)
	}
	head := gitOut(t, cwd, "rev-parse", "HEAD")
	// Remove old tags so retry must infer the prior version from HEAD^ bytes.
	for _, name := range []string{"a", "b"} {
		gitOut(t, cwd, "tag", "-d", name+"@v1.0.0")
	}
	for _, p := range config.Packages {
		_, v, err := manifest.ReadAtRef(cwd, p, "HEAD^", nil)
		if err != nil || v != "1.0.0" {
			t.Fatalf("history version=%s error=%v", v, err)
		}
	}
	again, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	rerun, err := Execute(cwd, config, again, Options{NoPushSet: true, NoGitHubSet: true})
	if err != nil || !rerun.Published || len(rerun.Plan.Releases) != 2 {
		t.Fatalf("retry=%+v error=%v", rerun, err)
	}
	if got := gitOut(t, cwd, "rev-parse", "HEAD"); got != head {
		t.Fatal("retry created new commit")
	}
	for _, r := range rerun.Plan.Releases {
		if r.CurrentVersion != "1.0.0" || r.NextVersion != "1.0.1" {
			t.Fatalf("release=%+v", r)
		}
	}
}
func TestHistoricalInheritedReadIgnoresCurrentWorkspaceIdentity(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "Cargo.toml"), "[workspace]\nmembers=['crate']\n[workspace.package]\nversion='1.0.0'\n")
	writeFile(t, filepath.Join(cwd, "crate", "Cargo.toml"), "[package]\nname='crate'\nversion={workspace=true}\nworkspace='..'\n")
	commitAll(t, cwd, "initial import")
	writeFile(t, filepath.Join(cwd, "Cargo.toml"), "[workspace]\nmembers=['crate']\n[workspace.package]\nversion='9.0.0'\n")
	writeFile(t, filepath.Join(cwd, "crate", "Cargo.toml"), "[package]\nname='crate'\nversion='9.0.0'\n")
	commitAll(t, cwd, "change workspace and member")
	p := types.NormalizedPackageConfig{Name: "crate", Type: types.PackageRust, Manifest: "crate/Cargo.toml"}
	if name, version, err := manifest.ReadAtRef(cwd, p, "HEAD^", nil); err != nil || name != "crate" || version != "1.0.0" {
		t.Fatalf("%s %s %v", name, version, err)
	}
	writeFile(t, filepath.Join(cwd, "crate", "Cargo.toml"), "[package]\nname='crate'\nversion.workspace=true\nworkspace='../../../outside'\n")
	commitAll(t, cwd, "invalid workspace escape")
	if _, _, err := manifest.ReadAtRef(cwd, p, "HEAD", nil); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escape error=%v", err)
	}
}
