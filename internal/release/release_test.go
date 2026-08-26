package release

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/githubapi"
	"github.com/openhoo/hooversion/internal/plan"
	"github.com/openhoo/hooversion/internal/types"
)

// --- shared helpers ---------------------------------------------------------

func gitOut(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	out, err := gitRaw(cwd, args...)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("git %v: %v: %s", args, err, ee.Stderr)
		}
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

func gitRaw(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func makeRepo(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	gitOut(t, cwd, "init", "-b", "main")
	gitOut(t, cwd, "config", "user.email", "test@example.com")
	gitOut(t, cwd, "config", "user.name", "Hooversion Test")
	return cwd
}

func makeBareRemote(t *testing.T) string {
	t.Helper()
	remote := t.TempDir()
	gitOut(t, remote, "init", "--bare")
	return remote
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
	return types.NormalizedPackageConfig{Name: name, Path: path, Type: types.PackageNode, Manifest: manifest, Changelog: "CHANGELOG.md"}
}

type releaseTestConfig struct {
	config *types.NormalizedConfig
}

func singleAppConfig(push bool) *types.NormalizedConfig {
	return &types.NormalizedConfig{
		Branches:             []string{"main"},
		TagFormat:            "v${version}",
		IndependentTagFormat: "${name}@v${version}",
		Packages:             []types.NormalizedPackageConfig{nodePkg("app", ".", "package.json")},
		GitHub:               types.GitHubSettings{Enabled: false, Releases: true, ApiUrl: "https://api.github.com"},
		OutputDir:            ".hooversion",
		Push:                 push,
	}
}

// seedAppRepo creates a repo with package.json v1.0.0 tagged v1.0.0 and one
// follow-up fix commit.
func seedAppRepo(t *testing.T) string {
	t.Helper()
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "package.json"), "{\n  \"name\": \"app\",\n  \"version\": \"1.0.0\"\n}\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	writeFile(t, filepath.Join(cwd, "app.ts"), "export const app = true;\n")
	commitAll(t, cwd, "fix: repair app")
	return cwd
}

func appManifestVersion(t *testing.T, cwd string) string {
	t.Helper()
	var pkg struct {
		Version string `json:"version"`
	}
	data, err := os.ReadFile(filepath.Join(cwd, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	return pkg.Version
}

// --- scenario ports ---------------------------------------------------------

func TestUpdatesFilesCommitsTagsAndWritesOutputs(t *testing.T) {
	cwd := seedAppRepo(t)
	config := singleAppConfig(false)

	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(cwd, config, plan, Options{NoPushSet: true, NoGitHubSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Published {
		t.Fatal("expected published result")
	}

	if got := appManifestVersion(t, cwd); got != "1.0.1" {
		t.Fatalf("manifest version = %q, want 1.0.1", got)
	}
	changelog, err := os.ReadFile(filepath.Join(cwd, "CHANGELOG.md"))
	if err != nil || !strings.Contains(string(changelog), "## 1.0.1") {
		t.Fatalf("changelog missing 1.0.1 entry: %v", err)
	}
	if got := gitOut(t, cwd, "tag", "--list", "v1.0.1"); got != "v1.0.1" {
		t.Fatalf("tag missing: %q", got)
	}
	versionData, err := os.ReadFile(filepath.Join(cwd, ".release-version"))
	if err != nil || string(versionData) != "1.0.1\n" {
		t.Fatalf(".release-version = %q (%v)", versionData, err)
	}
	outputs := readFileString(t, cwd, ".hooversion", "outputs.json")
	if !strings.Contains(outputs, `"published": true`) {
		t.Fatalf("outputs.json wrong: %q", outputs)
	}
}

func TestRejectsPlanWhenLocalHeadAdvancedBeforeMutation(t *testing.T) {
	cwd := seedAppRepo(t)
	config := singleAppConfig(false)

	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cwd, "unrelated.txt"), "drift\n")
	commitAll(t, cwd, "chore: unrelated drift")

	_, err = Execute(cwd, config, plan, Options{NoPushSet: true, NoGitHubSet: true})
	if err == nil || !strings.Contains(err.Error(), "Release source changed locally") {
		t.Fatalf("expected local drift rejection, got %v", err)
	}
	if got := appManifestVersion(t, cwd); got != "1.0.0" {
		t.Fatalf("manifest mutated despite drift: %q", got)
	}
	assertMissing(t, filepath.Join(cwd, "CHANGELOG.md"))
	if got := gitOut(t, cwd, "tag", "--list", "v1.0.1"); got != "" {
		t.Fatalf("tag created despite drift: %q", got)
	}
}

func TestAtomicPushFailureLeavesRemoteAtSourceSha(t *testing.T) {
	cwd := seedAppRepo(t)
	remote := makeBareRemote(t)
	gitOut(t, cwd, "remote", "add", "origin", remote)
	writeFile(t, filepath.Join(cwd, "seed.txt"), "seed\n")
	commitAll(t, cwd, "chore: seed")
	sourceSha := gitOut(t, cwd, "rev-parse", "HEAD")
	gitOut(t, cwd, "push", "origin", "main")

	config := singleAppConfig(true)
	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}

	// Conflicting v1.0.1 tag on the remote makes the atomic push fail.
	if _, err := gitRaw(remote, "--git-dir", remote, "tag", "-a", "v1.0.1", "-m", "conflicting tag", sourceSha); err != nil {
		t.Fatal(err)
	}

	if _, err := Execute(cwd, config, plan, Options{NoGitHubSet: true}); err == nil {
		t.Fatal("expected atomic push failure")
	}
	if got, err := gitRaw("", "--git-dir", remote, "rev-parse", "refs/heads/main"); err != nil || got != sourceSha {
		t.Fatalf("remote main moved to %q, want %q", got, sourceSha)
	}
}

func TestResumesExactGeneratedReleaseCommitWithoutSecondCommit(t *testing.T) {
	cwd := seedAppRepo(t)
	config := singleAppConfig(false)

	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Execute(cwd, config, plan, Options{NoPushSet: true, NoGitHubSet: true})
	if err != nil || !first.Published {
		t.Fatalf("first run: %v %+v", err, first)
	}
	releaseHead := gitOut(t, cwd, "rev-parse", "HEAD")
	commitCount := gitOut(t, cwd, "rev-list", "--count", "HEAD")

	rerunPlan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(rerunPlan.Releases) != 0 {
		t.Fatalf("fresh rerun plan should have zero releases: %+v", rerunPlan.Releases)
	}
	rerun, err := Execute(cwd, config, rerunPlan, Options{NoPushSet: true, NoGitHubSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rerun.Published || len(rerun.Plan.Releases) != 1 {
		t.Fatalf("resume result wrong: %+v", rerun)
	}
	if rerun.Plan.SourceSha != plan.SourceSha {
		t.Fatalf("resumed SourceSha = %q, want %q", rerun.Plan.SourceSha, plan.SourceSha)
	}
	if rerun.Plan.Releases[0].CurrentVersion != "1.0.0" ||
		rerun.Plan.Releases[0].NextVersion != "1.0.1" ||
		rerun.Plan.Releases[0].Tag != "v1.0.1" {
		t.Fatalf("reconstructed release wrong: %+v", rerun.Plan.Releases[0])
	}
	if got := gitOut(t, cwd, "rev-parse", "HEAD"); got != releaseHead {
		t.Fatalf("resume created a new HEAD %q (was %q)", got, releaseHead)
	}
	if got := gitOut(t, cwd, "rev-list", "--count", "HEAD"); got != commitCount {
		t.Fatalf("resume created extra commits: %s -> %s", commitCount, got)
	}
}

func TestForeignUntrackedBlocksButManagedOutputsPreserved(t *testing.T) {
	cwd := seedAppRepo(t)
	config := singleAppConfig(false)

	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cwd, ".hooversion", "outputs.json"), `{"published":true,"releases":[]}`+"\n")
	writeFile(t, filepath.Join(cwd, ".release-version"), "1.0.1\n")
	writeFile(t, filepath.Join(cwd, "unrelated.txt"), "keep me\n")

	_, err = Execute(cwd, config, plan, Options{NoPushSet: true, NoGitHubSet: true})
	if err == nil || !strings.Contains(err.Error(), "Working tree must be clean before release") {
		t.Fatalf("expected clean-tree rejection, got %v", err)
	}
	if got := readFileString(t, cwd, "unrelated.txt"); got != "keep me\n" {
		t.Fatalf("foreign file touched: %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(cwd, ".hooversion", "outputs.json")); statErr != nil {
		t.Fatalf("managed outputs cleared by failed run: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(cwd, ".release-version")); statErr != nil {
		t.Fatalf("managed version cleared by failed run: %v", statErr)
	}
}

func TestCustomFileInsideOutputDirBlocksRelease(t *testing.T) {
	cwd := seedAppRepo(t)
	config := singleAppConfig(false)

	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cwd, ".hooversion", "outputs.json"), `{"published":true,"releases":[]}`+"\n")
	writeFile(t, filepath.Join(cwd, ".release-version"), "1.0.1\n")
	writeFile(t, filepath.Join(cwd, ".hooversion", "custom.txt"), "keep me\n")

	_, err = Execute(cwd, config, plan, Options{NoPushSet: true, NoGitHubSet: true})
	if err == nil || !strings.Contains(err.Error(), "Working tree must be clean before release") {
		t.Fatalf("expected clean-tree rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(cwd, ".hooversion", "custom.txt")); statErr != nil {
		t.Fatalf("custom output removed by failed run: %v", statErr)
	}
}

func TestManagedOutputsAloneAreExempt(t *testing.T) {
	cwd := seedAppRepo(t)
	config := singleAppConfig(false)

	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cwd, ".hooversion", "outputs.json"), `{"published":true,"releases":[]}`+"\n")
	writeFile(t, filepath.Join(cwd, ".release-version"), "1.0.1\n")

	result, err := Execute(cwd, config, plan, Options{NoPushSet: true, NoGitHubSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Published {
		t.Fatal("expected published result")
	}
	outputs := readFileString(t, cwd, ".hooversion", "outputs.json")
	if !strings.Contains(outputs, `"published": true`) {
		t.Fatalf("outputs not replaced: %q", outputs)
	}
}

func TestNoReleasePlanIsUnpublished(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "package.json"), "{\"name\": \"app\", \"version\": \"1.0.0\"}\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	writeFile(t, filepath.Join(cwd, "app.ts"), "export const app = true;\n")
	commitAll(t, cwd, "chore: maintain app")

	config := singleAppConfig(false)
	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Releases) != 0 {
		t.Fatalf("expected empty plan, got %+v", plan.Releases)
	}
	result, err := Execute(cwd, config, plan, Options{NoPushSet: true, NoGitHubSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Published || len(result.Plan.Releases) != 0 {
		t.Fatalf("unexpected publication: %+v", result)
	}
}

func TestDryRunDoesNotMutateAnything(t *testing.T) {
	cwd := seedAppRepo(t)
	config := singleAppConfig(false)

	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cwd, ".hooversion", "outputs.json"), "managed outputs\n")
	writeFile(t, filepath.Join(cwd, ".release-version"), "managed version\n")
	outputsBefore := readFileString(t, cwd, ".hooversion", "outputs.json")
	versionBefore := readFileString(t, cwd, ".release-version")

	result, err := Execute(cwd, config, plan, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Published {
		t.Fatal("dry run reported publication")
	}
	if got := readFileString(t, cwd, ".hooversion", "outputs.json"); got != outputsBefore {
		t.Fatalf("dry run mutated outputs.json: %q", got)
	}
	if got := readFileString(t, cwd, ".release-version"); got != versionBefore {
		t.Fatalf("dry run mutated .release-version: %q", got)
	}
	if got := appManifestVersion(t, cwd); got != "1.0.0" {
		t.Fatalf("dry run mutated manifest: %q", got)
	}
	if got := gitOut(t, cwd, "tag", "--list", "v1.0.1"); got != "" {
		t.Fatalf("dry run created tag: %q", got)
	}
}

func TestFailedHookAbortsPipelineWithExactText(t *testing.T) {
	cwd := seedAppRepo(t)
	config := singleAppConfig(false)
	config.Hooks.BeforeRelease = []string{"echo hook-out && exit 3"}

	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Execute(cwd, config, plan, Options{NoPushSet: true, NoGitHubSet: true})
	if err == nil || !strings.HasPrefix(err.Error(), "Hook failed: echo hook-out && exit 3\n") {
		t.Fatalf("hook failure text wrong: %v", err)
	}
	if !strings.Contains(err.Error(), "hook-out") {
		t.Fatalf("hook stdout missing from failure text: %v", err)
	}
	if got := appManifestVersion(t, cwd); got != "1.0.0" {
		t.Fatalf("pipeline mutated after hook failure: %q", got)
	}
}

func TestUpdatesPlainVersionFileManifest(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "version"), "2.4.0\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "v2.4.0", "-m", "v2.4.0")
	writeFile(t, filepath.Join(cwd, "image.txt"), "metadata\n")
	commitAll(t, cwd, "feat(image): add runtime metadata")

	config := singleAppConfig(false)
	pkg := config.Packages[0]
	pkg.Name, pkg.Type, pkg.Manifest, pkg.Changelog = "image", types.PackageVersionFile, "version", "CHANGELOG.md"
	config.Packages[0] = pkg

	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(cwd, config, plan, Options{NoPushSet: true, NoGitHubSet: true}); err != nil {
		t.Fatal(err)
	}

	if got := readFileString(t, cwd, "version"); got != "2.5.0\n" {
		t.Fatalf("version file = %q, want 2.5.0\\n", got)
	}
	if got := gitOut(t, cwd, "tag", "--list", "v2.5.0"); got != "v2.5.0" {
		t.Fatalf("tag missing: %q", got)
	}
}

func TestMultiPackageReleasesUseIndependentTagsAndCommitForm(t *testing.T) {
	cwd := makeRepo(t)
	writeFile(t, filepath.Join(cwd, "packages", "one", "package.json"), "{\"name\": \"one\", \"version\": \"0.1.0\"}\n")
	writeFile(t, filepath.Join(cwd, "packages", "two", "package.json"), "{\"name\": \"two\", \"version\": \"0.3.0\"}\n")
	commitAll(t, cwd, "initial import")
	gitOut(t, cwd, "tag", "-a", "one@v0.1.0", "-m", "one@v0.1.0")
	gitOut(t, cwd, "tag", "-a", "two@v0.3.0", "-m", "two@v0.3.0")
	writeFile(t, filepath.Join(cwd, "packages", "one", "a.ts"), "a\n")
	writeFile(t, filepath.Join(cwd, "packages", "two", "b.ts"), "b\n")
	commitAll(t, cwd, "feat: grow both packages")

	config := &types.NormalizedConfig{
		Branches:             []string{"main"},
		TagFormat:            "v${version}",
		IndependentTagFormat: "${name}@v${version}",
		Packages: []types.NormalizedPackageConfig{
			nodePkg("one", "packages/one", "packages/one/package.json"),
			nodePkg("two", "packages/two", "packages/two/package.json"),
		},
		GitHub:    types.GitHubSettings{Enabled: false, Releases: true, ApiUrl: "https://api.github.com"},
		OutputDir: ".hooversion",
		Push:      false,
	}
	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Releases) != 2 {
		t.Fatalf("expected two releases, got %+v", plan.Releases)
	}

	result, err := Execute(cwd, config, plan, Options{NoPushSet: true, NoGitHubSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Published {
		t.Fatal("expected publication")
	}

	if got := gitOut(t, cwd, "tag", "--list", "one@v0.2.0"); got != "one@v0.2.0" {
		t.Fatalf("one tag missing: %q", got)
	}
	if got := gitOut(t, cwd, "tag", "--list", "two@v0.4.0"); got != "two@v0.4.0" {
		t.Fatalf("two tag missing: %q", got)
	}
	subject := strings.Split(gitOut(t, cwd, "log", "-1", "--format=%B"), "\n")[0]
	if subject != "chore(release): one@0.2.0, two@0.4.0" {
		t.Fatalf("multi-release commit subject = %q", subject)
	}
	if got := readManifestVersionNamed(t, cwd, "packages/two/package.json"); got != "0.4.0" {
		t.Fatalf("two version = %q", got)
	}
	assertMissing(t, filepath.Join(cwd, ".release-version"))
	outputs := readFileString(t, cwd, ".hooversion", "outputs.json")
	if !strings.Contains(outputs, `"tag": "two@v0.4.0"`) {
		t.Fatalf("multi outputs missing second release: %q", outputs)
	}
}

func TestRetriesPublicationAfterPostPushFailureWithoutSecondCommit(t *testing.T) {
	cwd := seedAppRepo(t)
	writeFile(t, filepath.Join(cwd, "artifact.txt"), "artifact\n")
	commitAll(t, cwd, "fix: add artifact")
	remote := makeBareRemote(t)
	gitOut(t, cwd, "remote", "add", "origin", remote)
	gitOut(t, cwd, "push", "origin", "main")

	var getHits, createHits, uploadHits atomic.Int64
	var uploadFail atomic.Bool
	expectedNotes := ""
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/tags/v1.0.1"):
			getHits.Add(1)
			if getHits.Load() > 1 {
				uploadFail.Store(false)
			}
			if getHits.Load() == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"id": 1, "tag_name": "v1.0.1", "name": "app 1.0.1",
				"body": expectedNotes, "draft": false, "prerelease": false,
				"upload_url": "https://uploads.github.com/repos/owner/repo/releases/1/assets{?name,label}", "html_url": "https://github.example.test/r/v1.0.1",
				"assets": []any{},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases"):
			createHits.Add(1)
			uploadFail.Store(true)
			writeJSON(w, http.StatusCreated, map[string]any{
				"id": 1, "tag_name": "v1.0.1", "name": "app 1.0.1",
				"body": expectedNotes, "draft": false, "prerelease": false,
				"upload_url": "https://uploads.github.com/repos/owner/repo/releases/1/assets{?name,label}", "html_url": "https://github.example.test/r/v1.0.1",
				"assets": []any{},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases/1/assets"):
			uploadHits.Add(1)
			if uploadFail.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("upload failed"))
				return
			}
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	target, _ := url.Parse(server.URL)
	fakeClient := githubapi.New("https://api.github.com", "token")
	fakeClient.HTTP = &http.Client{Transport: rewriteTransport{target: target, base: server.Client().Transport}}
	newGitHubClient = func(baseURL, token string) *githubapi.Client {
		return fakeClient
	}

	config := &types.NormalizedConfig{
		Branches:             []string{"main"},
		TagFormat:            "v${version}",
		IndependentTagFormat: "${name}@v${version}",
		Packages: []types.NormalizedPackageConfig{{
			Name: "app", Path: ".", Type: types.PackageNode, Manifest: "package.json",
			Changelog: "CHANGELOG.md",
			Assets:    []string{"artifact.txt"},
		}},
		GitHub: types.GitHubSettings{
			Enabled: true, Releases: true, Repository: "owner/repo", ApiUrl: "https://api.github.com",
		},
		OutputDir: ".hooversion",
		Push:      true,
	}

	plan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	expectedNotes = plan.Releases[0].Notes

	opts := Options{Push: true, NoPushSet: true, GitHub: true, NoGitHubSet: false, GitHubToken: "token"}
	_, firstErr := Execute(cwd, config, plan, opts)
	if firstErr == nil || !strings.Contains(firstErr.Error(), "GitHub API request failed") {
		t.Fatalf("expected GitHub failure on first run, got %v", firstErr)
	}
	releaseHead := gitOut(t, cwd, "rev-parse", "HEAD")
	commitCount := gitOut(t, cwd, "rev-list", "--count", "HEAD")
	remoteHeadAfterFirst, _ := gitRaw("", "--git-dir", remote, "rev-parse", "refs/heads/main")
	if remoteHeadAfterFirst != releaseHead {
		t.Fatalf("push should have landed remotely before GitHub failure")
	}

	rerunPlan, err := CreatePlanForTest(cwd, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(rerunPlan.Releases) != 0 {
		t.Fatalf("rerun fresh plan not empty: %+v", rerunPlan.Releases)
	}
	result, err := Execute(cwd, config, rerunPlan, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Published || len(result.Plan.Releases) != 1 {
		t.Fatalf("retry result wrong: %+v", result)
	}
	release := result.Plan.Releases[0]
	if release.CurrentVersion != "1.0.0" || release.NextVersion != "1.0.1" || release.Tag != "v1.0.1" {
		t.Fatalf("reconstructed retry release wrong: %+v", release)
	}
	if got := gitOut(t, cwd, "rev-parse", "HEAD"); got != releaseHead {
		t.Fatalf("retry created a new HEAD")
	}
	if got := gitOut(t, cwd, "rev-list", "--count", "HEAD"); got != commitCount {
		t.Fatalf("retry created extra commits")
	}
	if createHits.Load() != 1 {
		t.Fatalf("release created %d times, want exactly 1", createHits.Load())
	}
	if uploadHits.Load() != 2 {
		t.Fatalf("asset uploaded %d times, want 2 (fail then success)", uploadHits.Load())
	}
}

// --- plumbing ---------------------------------------------------------------

// CreatePlanForTest plans with the default policy and the checked-out
// branch, mirroring the CLI release command.
func CreatePlanForTest(cwd string, config *types.NormalizedConfig) (*types.ReleasePlan, error) {
	branch, err := git.CurrentBranch(cwd)
	if err != nil {
		return nil, err
	}
	return plan.CreatePlan(cwd, config, branch, nil)
}

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rewritten := req.Clone(context.Background())
	rewritten.URL.Scheme = t.target.Scheme
	rewritten.URL.Host = t.target.Host
	return t.base.RoundTrip(rewritten)
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func readFileString(t *testing.T, elems ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(elems...))
	if err != nil {
		t.Fatalf("read %v: %v", elems, err)
	}
	return string(data)
}

func readManifestVersionNamed(t *testing.T, cwd, manifestPath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cwd, manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	return pkg.Version
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s still exists: %v", path, err)
	}
}
