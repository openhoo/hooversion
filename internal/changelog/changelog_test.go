package changelog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hooversion/internal/changelog"
	hvcommit "github.com/openhoo/hooversion/internal/commit"
	"github.com/openhoo/hooversion/internal/types"
)

func commit(hash, subject, body string) types.ParsedCommit {
	return hvcommit.Parse(types.RawCommit{Hash: hash, Subject: subject, Body: body}, nil)
}

func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".hooversion-") && strings.HasSuffix(e.Name(), ".tmp") {
			names = append(names, e.Name())
		}
	}
	return names
}

// Ports the golden layout of src/changelog.ts generateReleaseNotes.
func TestGenerateNotesGroupsAndOrder(t *testing.T) {
	date := time.Date(2026, 8, 25, 10, 30, 0, 0, time.UTC)
	breaking := commit("abc1234def", "feat(api): change payload", "BREAKING CHANGE: payload is now nested")
	featScoped := commit("1111111", "feat(ui): add theme switcher", "")
	featPlain := commit("2222222", "feat: add widget", "")
	fix := commit("3333333", "fix: handle hotplug failure", "")
	perf := commit("4444444", "perf: cache results", "")
	docs := commit("5555555", "docs: update readme", "")

	got := changelog.GenerateNotes("1.2.3", date, []types.ParsedCommit{docs, fix, breaking, featPlain, featScoped, perf})
	want := strings.Join([]string{
		"## 1.2.3 (2026-08-25)",
		"",
		"### Breaking Changes",
		"",
		"- **api:** change payload (abc1234)",
		"  - BREAKING: payload is now nested",
		"",
		"### Features",
		"",
		"- add widget (2222222)",
		"- **ui:** add theme switcher (1111111)",
		"",
		"### Bug Fixes",
		"",
		"- handle hotplug failure (3333333)",
		"",
		"### Performance",
		"",
		"- cache results (4444444)",
		"",
		"### Other Changes",
		"",
		"- update readme (5555555)",
	}, "\n")
	if got != want {
		t.Fatalf("notes mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestGenerateNotesDateUTCAndEmpty(t *testing.T) {
	// A date in +05:00 at 01:00 is still the previous day in UTC.
	zoned := time.Date(2026, 8, 26, 1, 0, 0, 0, time.FixedZone("X", 5*3600))
	got := changelog.GenerateNotes("2.0.0", zoned, nil)
	if got != "## 2.0.0 (2026-08-25)" {
		t.Fatalf("UTC date header = %q", got)
	}
}

func TestGenerateNotesBreakingBangWithoutFooterOmitsSubBullet(t *testing.T) {
	bang := commit("abcdef1234567", "change config format", "")
	bang.Breaking = true // "!" alone carries no footer text
	got := changelog.GenerateNotes("1.0.0", time.Unix(0, 0).UTC(), []types.ParsedCommit{bang})
	want := "## 1.0.0 (1970-01-01)\n\n### Breaking Changes\n\n- change config format (abcdef1)"
	if got != want {
		t.Fatalf("notes = %q, want %q", got, want)
	}
}

func TestUpdateCreatesMissingChangelog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CHANGELOG.md")

	if err := changelog.Update(path, "Release notes", "app"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Ported expectation from tests/changelog.test.ts.
	if string(data) != "# app Changelog\n\nRelease notes\n\n\n" {
		t.Fatalf("bootstrap content = %q", string(data))
	}
	if names := tempFiles(t, dir); len(names) != 0 {
		t.Fatalf("temp files left behind: %v", names)
	}
}

func TestUpdatePreservesExistingHeaderAndBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CHANGELOG.md")
	if err := os.WriteFile(path, []byte("# Existing Changelog\n\n## 1.0.0\n\nOlder notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := changelog.Update(path, "Release notes", "app"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	data, _ := os.ReadFile(path)
	// Ported expectation from tests/changelog.test.ts.
	if string(data) != "# Existing Changelog\n\nRelease notes\n\n## 1.0.0\n\nOlder notes\n" {
		t.Fatalf("updated content = %q", string(data))
	}
	if names := tempFiles(t, dir); len(names) != 0 {
		t.Fatalf("temp files left behind: %v", names)
	}
}

func TestUpdateInjectsTitleWhenFirstLineIsNotHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CHANGELOG.md")
	if err := os.WriteFile(path, []byte("Intro paragraph\n\nbody text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := changelog.Update(path, "Notes", "svc"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "# svc Changelog\n\nNotes\n\nIntro paragraph\n\nbody text\n" {
		t.Fatalf("injected content = %q", string(data))
	}
}

func TestUpdateWhitespaceOnlyFileBehavesLikeBootstrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CHANGELOG.md")
	if err := os.WriteFile(path, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := changelog.Update(path, "N", "pkg"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "# pkg Changelog\n\nN\n\n\n" {
		t.Fatalf("whitespace-only content = %q", string(data))
	}
}

func TestUpdateRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.md")
	linkPath := filepath.Join(dir, "CHANGELOG.md")
	if err := os.WriteFile(realPath, []byte("# Existing Changelog\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := changelog.Update(linkPath, "Release notes", "app"); err == nil {
		t.Fatal("symlinked changelog should be rejected")
	}
	data, _ := os.ReadFile(realPath)
	if string(data) != "# Existing Changelog\n" {
		t.Fatalf("symlink target modified: %q", string(data))
	}
	if names := tempFiles(t, dir); len(names) != 0 {
		t.Fatalf("temp files left behind: %v", names)
	}
}

func TestUpdateRejectsDirectoryTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CHANGELOG.md")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	err := changelog.Update(path, "Release notes", "app")
	if err == nil {
		t.Fatal("directory changelog should be rejected")
	}
	if !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("error = %v, want regular-file message", err)
	}
	if names := tempFiles(t, dir); len(names) != 0 {
		t.Fatalf("temp files left behind: %v", names)
	}
}

func TestUpdateCreatesParentDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "CHANGELOG.md")
	if err := changelog.Update(path, "First release", "deep"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "# deep Changelog\n\nFirst release\n\n\n" {
		t.Fatalf("nested content = %q", string(data))
	}
}
