package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooversion/internal/types"
)

func newStore(t *testing.T) (Store, string) {
	t.Helper()
	cwd := t.TempDir()
	return Store{Cwd: cwd, OutputDir: ".hooversion"}, cwd
}

func singleRelease() types.PackageRelease {
	return types.PackageRelease{
		Package:     types.NormalizedPackageConfig{Name: "app"},
		NextVersion: "1.0.1",
		Tag:         "v1.0.1",
		Notes:       "notes body",
		ReleaseType: types.Patch,
	}
}

func multiReleases() []types.PackageRelease {
	first := singleRelease()
	second := types.PackageRelease{
		Package:     types.NormalizedPackageConfig{Name: "lib"},
		NextVersion: "2.0.1",
		Tag:         "lib@v2.0.1",
		Notes:       "lib notes",
		ReleaseType: types.Patch,
	}
	return []types.PackageRelease{first, second}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestClearRemovesOnlyManagedFilesAndIsIdempotent(t *testing.T) {
	store, cwd := newStore(t)
	outputDir := filepath.Join(cwd, ".hooversion")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".release-version"), []byte("9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "outputs.json"),
		[]byte(`{"published": true, "releases": [{"tag": "v1.2.3"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "v1.2.3-notes.md"), []byte("generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "user-notes.md"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := store.Clear(); err != nil {
			t.Fatalf("clear: %v", err)
		}
	}

	assertMissing(t, filepath.Join(cwd, ".release-version"))
	assertMissing(t, filepath.Join(outputDir, "outputs.json"))
	assertMissing(t, filepath.Join(outputDir, "v1.2.3-notes.md"))
	if got := mustRead(t, filepath.Join(outputDir, "user-notes.md")); got != "keep\n" {
		t.Fatalf("foreign file clobbered: %q", got)
	}
}

func TestClearTreatsMissingFilesAsClean(t *testing.T) {
	store, _ := newStore(t)
	for range 2 {
		if err := store.Clear(); err != nil {
			t.Fatalf("clear: %v", err)
		}
	}
}

func TestClearIgnoresMalformedStalePayload(t *testing.T) {
	store, cwd := newStore(t)
	outputDir := filepath.Join(cwd, ".hooversion")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "outputs.json"), []byte("{not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "v1.2.3-notes.md"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}

	assertMissing(t, filepath.Join(outputDir, "outputs.json"))
	if got := mustRead(t, filepath.Join(outputDir, "v1.2.3-notes.md")); got != "keep\n" {
		t.Fatalf("note from malformed payload removed: %q", got)
	}
}

func TestPathsDoesNotFollowSymlinkedStalePayload(t *testing.T) {
	store, cwd := newStore(t)
	outputDir := filepath.Join(cwd, ".hooversion")
	outsideDir := t.TempDir()
	payloadPath := filepath.Join(outsideDir, "outputs.json")
	notePath := filepath.Join(outsideDir, "v1.2.3-notes.md")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadPath, []byte(`{"releases": [{"tag": "v1.2.3"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(payloadPath, filepath.Join(outputDir, "outputs.json")); err != nil {
		t.Fatal(err)
	}

	paths := store.Paths()
	wantBase := map[string]bool{
		filepath.Join(cwd, ".release-version"):   false,
		filepath.Join(outputDir, "outputs.json"): false,
		outputDir:                                true,
	}
	if len(paths) != len(wantBase) {
		t.Fatalf("paths = %v, want exactly base set (no outside-derived notes)", paths)
	}
	for path, scope := range wantBase {
		if got, ok := paths[path]; !ok || got != scope {
			t.Fatalf("paths[%s] = %t,%v; want %t", path, got, ok, scope)
		}
	}

	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, filepath.Join(outputDir, "outputs.json"))
	if got := mustRead(t, payloadPath); got == "" {
		t.Fatal("symlink target was destroyed")
	}
	if got := mustRead(t, notePath); got != "outside\n" {
		t.Fatalf("outside note touched: %q", got)
	}
}

func TestClearRejectsDirectoryAtManagedPath(t *testing.T) {
	store, cwd := newStore(t)
	outputDir := filepath.Join(cwd, ".hooversion")
	if err := os.MkdirAll(filepath.Join(outputDir, "outputs.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(); err == nil {
		t.Fatal("expected directory removal to fail")
	}
	info, err := os.Lstat(filepath.Join(outputDir, "outputs.json"))
	if err != nil || !info.IsDir() {
		t.Fatalf("managed directory state changed: %v", err)
	}
}

func TestWriteNoReleaseRemovesStaleSingleReleaseFields(t *testing.T) {
	store, cwd := newStore(t)
	outputDir := filepath.Join(cwd, ".hooversion")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".release-version"), []byte("1.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "outputs.json"),
		[]byte(`{"published":true,"releases":[]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := store.Write(nil, false); err != nil {
		t.Fatal(err)
	}

	assertMissing(t, filepath.Join(cwd, ".release-version"))
	var payload storedPayload
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(outputDir, "outputs.json"))), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Published || len(payload.Releases) != 0 {
		t.Fatalf("unexpected no-release payload: %+v", payload)
	}
}

func TestWriteMultiReleaseRemovesSymlinkedVersionWithoutFollowingTarget(t *testing.T) {
	store, cwd := newStore(t)
	targetPath := filepath.Join(cwd, "version-target")
	if err := os.WriteFile(targetPath, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetPath, filepath.Join(cwd, ".release-version")); err != nil {
		t.Fatal(err)
	}

	if err := store.Write(multiReleases(), true); err != nil {
		t.Fatal(err)
	}

	assertMissing(t, filepath.Join(cwd, ".release-version"))
	if got := mustRead(t, targetPath); got != "keep\n" {
		t.Fatalf("symlink target clobbered: %q", got)
	}
}

func TestWriteSingleOnlyVersionFileSemantics(t *testing.T) {
	store, cwd := newStore(t)
	versionPath := filepath.Join(cwd, ".release-version")

	if err := store.Write(multiReleases(), true); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, versionPath)

	if err := store.Write([]types.PackageRelease{singleRelease()}, true); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, versionPath); got != "1.0.1\n" {
		t.Fatalf(".release-version = %q, want %q", got, "1.0.1\n")
	}
}

func TestWriteOutputsJSONShapeAndTrailingNewline(t *testing.T) {
	store, cwd := newStore(t)
	if err := store.Write(multiReleases(), true); err != nil {
		t.Fatal(err)
	}
	raw := mustRead(t, filepath.Join(cwd, ".hooversion", "outputs.json"))
	if raw[len(raw)-1] != '\n' || raw[len(raw)-2] == '\n' {
		t.Fatalf("outputs.json must end with exactly one newline: %q", raw)
	}
	want := `{
  "published": true,
  "releases": [
    {
      "name": "app",
      "version": "1.0.1",
      "tag": "v1.0.1",
      "type": "patch",
      "notesPath": ".hooversion/v1.0.1-notes.md"
    },
    {
      "name": "lib",
      "version": "2.0.1",
      "tag": "lib@v2.0.1",
      "type": "patch",
      "notesPath": ".hooversion/lib@v2.0.1-notes.md"
    }
  ]
}` + "\n"
	if raw != want {
		t.Fatalf("outputs.json mismatch:\n got %q\nwant %q", raw, want)
	}
	if got := mustRead(t, filepath.Join(cwd, ".hooversion", "v1.0.1-notes.md")); got != "notes body\n" {
		t.Fatalf("notes file = %q", got)
	}
}

func TestWriteSanitizesTagForNoteNames(t *testing.T) {
	store, cwd := newStore(t)
	release := types.PackageRelease{
		Package:     types.NormalizedPackageConfig{Name: "app"},
		NextVersion: "1.0.0",
		Tag:         "release/v1.0.0+meta/x",
		Notes:       "",
		ReleaseType: types.None,
	}
	if err := store.Write([]types.PackageRelease{release}, true); err != nil {
		t.Fatal(err)
	}
	raw := mustRead(t, filepath.Join(cwd, ".hooversion", "outputs.json"))
	if !strings.Contains(raw, `"notesPath": ".hooversion/release-v1.0.0-meta-x-notes.md"`) {
		t.Fatalf("sanitized notesPath missing: %s", raw)
	}
}

func TestGitHubOutputAppendsWithoutTruncating(t *testing.T) {
	store, cwd := newStore(t)
	githubOutput := filepath.Join(cwd, "github-output")
	if err := os.WriteFile(githubOutput, []byte("previous=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_OUTPUT", githubOutput)

	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, githubOutput); got != "previous=value\n" {
		t.Fatalf("clear mutated GitHub output: %q", got)
	}

	if err := store.Write([]types.PackageRelease{singleRelease()}, true); err != nil {
		t.Fatal(err)
	}
	want := "previous=value\n" +
		"published=true\n" +
		`releases_json=[{"name":"app","version":"1.0.1","tag":"v1.0.1","type":"patch","notesPath":".hooversion/v1.0.1-notes.md"}]` + "\n" +
		"version=1.0.1\n" +
		"tag=v1.0.1\n"
	if got := mustRead(t, githubOutput); got != want {
		t.Fatalf("github output =\n%q\nwant\n%q", got, want)
	}
}

func TestGitHubOutputOmittedWhenUnset(t *testing.T) {
	store, cwd := newStore(t)
	t.Setenv("GITHUB_OUTPUT", "")
	if err := os.Unsetenv("GITHUB_OUTPUT"); err != nil {
		t.Fatal(err)
	}
	if err := store.Write([]types.PackageRelease{singleRelease()}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "github-output")); !os.IsNotExist(err) {
		t.Fatalf("GITHUB_OUTPUT file created despite unset env: %v", err)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s still exists: %v", path, err)
	}
}
