package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestAppFreshCheckoutResumesPublicationAfterSuccessfulPush(t *testing.T) {
	var publications atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			http.NotFound(w, r)
		case r.Method == http.MethodPost:
			if publications.Add(1) == 1 {
				http.Error(w, "temporary publication failure", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":9,"tag_name":"v0.1.0","name":"mypkg 0.1.0","body":"","upload_url":"https://uploads.github.com/assets","draft":false,"prerelease":false}`)
		default:
			t.Errorf("unexpected API method: %s", r.Method)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultClient.Transport
	http.DefaultClient.Transport = redirectTransport{target: target, base: server.Client().Transport}
	t.Cleanup(func() { http.DefaultClient.Transport = previous })

	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	runGit(t, dir, "init", "-q", "--bare", "-b", "main", bare)
	first := filepath.Join(dir, "first")
	runGit(t, dir, "clone", "-q", bare, first)
	runGit(t, first, "checkout", "-q", "-b", "main")
	runGit(t, first, "config", "user.email", "test@example.com")
	runGit(t, first, "config", "user.name", "Test")
	writeFile(t, filepath.Join(first, "hooversion.yaml"), "packages:\n  - name: mypkg\n    type: node\n")
	writeFile(t, filepath.Join(first, "package.json"), `{"name":"mypkg","version":"0.0.1"}`)
	runGit(t, first, "add", "-A")
	runGit(t, first, "commit", "-q", "-m", "feat: initial")
	runGit(t, first, "push", "-q", "-u", "origin", "main")
	source := runGit(t, first, "rev-parse", "HEAD")
	workParent := filepath.Join(dir, "work")
	spec := JobSpec{RepositoryFullName: "octo/hello", Branch: "main", HeadSha: source, RepoDir: first, WorkDir: workParent, Token: "test-token"}
	result := runVersionhooRelease(spec)
	if result.Err == nil {
		t.Fatal("first publication did not fail")
	}
	releaseHead := runGit(t, bare, "rev-parse", "refs/heads/main")
	if releaseHead == source {
		t.Fatal("first attempt never pushed release commit")
	}
	if parent := runGit(t, bare, "rev-parse", releaseHead+"^"); parent != source {
		t.Fatalf("release parent=%s want=%s", parent, source)
	}
	if tag := runGit(t, bare, "rev-parse", "v0.1.0^{commit}"); tag != releaseHead {
		t.Fatalf("remote release tag at %s", tag)
	}
	entries, err := os.ReadDir(workParent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed App work directory not cleaned: %v %v", entries, err)
	}

	// A webhook retry gets a new checkout: no local journal survives, but the
	// remote release commit and its exact source parent identify pending work.
	second := filepath.Join(dir, "second")
	runGit(t, dir, "clone", "-q", bare, second)
	spec.RepoDir = second
	result = runVersionhooRelease(spec)
	if result.Err != nil || !result.Published || result.Outcome != "published" {
		t.Fatalf("same source retry failed to resume: %+v", result)
	}
	if publications.Load() != 2 {
		t.Fatalf("publication attempts=%d", publications.Load())
	}
	if head := runGit(t, bare, "rev-parse", "refs/heads/main"); head != releaseHead {
		t.Fatalf("retry created a second release commit: %s != %s", head, releaseHead)
	}
	if count := runGit(t, bare, "rev-list", "--count", "main"); count != "2" {
		t.Fatalf("commit count=%s", count)
	}
	if local := runGit(t, second, "rev-parse", "HEAD"); local != releaseHead {
		t.Fatalf("retry local head changed: %s", local)
	}
	if len(result.Releases) != 1 || result.Releases[0].Version != "0.1.0" {
		t.Fatalf("resumed releases=%+v", result.Releases)
	}
	// A release commit for another source, or another branch, is never enough
	// to bypass the stale guard for the requested webhook.
	wrongSource := spec
	wrongSource.HeadSha = "1111111111111111111111111111111111111111"
	if result := runVersionhooRelease(wrongSource); result.Err != nil || result.Outcome != "stale" {
		t.Fatalf("other source accepted: %+v", result)
	}
	wrongBranch := spec
	wrongBranch.Branch = "release"
	if result := runVersionhooRelease(wrongBranch); result.Err != nil || result.Outcome != "stale" {
		t.Fatalf("other branch accepted: %+v", result)
	}
	if publications.Load() != 2 {
		t.Fatal("stale source or branch triggered publication")
	}
}
