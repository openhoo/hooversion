package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestValidateCloneURL(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		fullName  string
		trusted   []string
		want      string
		wantError string
	}{
		{"github ok", "https://github.com/octo/hello.git", "octo/hello", nil,
			"https://github.com/octo/hello.git", ""},
		{"no .git suffix", "https://github.com/octo/hello", "octo/hello", nil,
			"https://github.com/octo/hello", ""},
		{"trusted host", "https://ghe.corp.example.com/octo/hello.git", "octo/hello",
			[]string{"GHE.CORP.example.com"}, "https://ghe.corp.example.com/octo/hello.git", ""},
		{"case-insensitive path", "https://github.com/OCTO/Hello.GIT", "octo/hello", nil,
			"https://github.com/OCTO/Hello.GIT", ""},
		{"mismatch", "https://github.com/other/repo.git", "octo/hello", nil, "",
			"GitHub clone repository mismatch: expected octo/hello, got other/repo"},
		{"subdirectory", "https://github.com/octo/hello/extra/repo.git", "octo/hello", nil, "",
			"GitHub clone repository mismatch: expected octo/hello, got octo/hello/extra/repo"},
		{"untrusted host", "https://evil.com/octo/hello.git", "octo/hello", nil, "",
			"Untrusted GitHub clone host: evil.com"},
		{"http scheme", "http://github.com/octo/hello.git", "octo/hello", nil, "",
			"Invalid GitHub clone URL: http://github.com/octo/hello.git"},
		{"credentials", "https://x-access-token:tok@github.com/octo/hello.git", "octo/hello", nil, "",
			"Invalid GitHub clone URL: https://x-access-token:tok@github.com/octo/hello.git"},
		{"port", "https://github.com:443/octo/hello.git", "octo/hello", nil, "",
			"Invalid GitHub clone URL: https://github.com:443/octo/hello.git"},
		{"query", "https://github.com/octo/hello.git?token=x", "octo/hello", nil, "",
			"Invalid GitHub clone URL: https://github.com/octo/hello.git?token=x"},
		{"hash", "https://github.com/octo/hello.git#frag", "octo/hello", nil, "",
			"Invalid GitHub clone URL: https://github.com/octo/hello.git#frag"},
		{"relative", "github.com/octo/hello.git", "octo/hello", nil, "",
			"Invalid GitHub clone URL: github.com/octo/hello.git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateCloneURL(tc.url, tc.fullName, tc.trusted)
			if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError {
					t.Fatalf("want error %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("invalid identity propagates", func(t *testing.T) {
		_, err := ValidateCloneURL("https://github.com/../hello.git", "../hello", nil)
		if err == nil || err.Error() != "Invalid GitHub repository identity: ../hello" {
			t.Fatalf("got %v", err)
		}
	})
}

func TestGitAuthArtifacts(t *testing.T) {
	home := t.TempDir()
	auth, err := createGitAuthArtifacts(home, "tok123")
	if err != nil {
		t.Fatal(err)
	}

	tokenInfo, err := os.Stat(auth.tokenPath)
	if err != nil || tokenInfo.Mode().Perm() != 0o600 {
		t.Fatalf("token mode %v err %v", tokenInfo.Mode(), err)
	}
	content, _ := os.ReadFile(auth.tokenPath)
	if string(content) != "tok123\n" {
		t.Fatalf("token content %q", content)
	}

	askpassInfo, err := os.Stat(auth.askpassPath)
	if err != nil || askpassInfo.Mode().Perm() != 0o700 {
		t.Fatalf("askpass mode %v err %v", askpassInfo.Mode(), err)
	}
	script, _ := os.ReadFile(auth.askpassPath)
	wantScript := "#!/bin/sh\ncase \"$1\" in\n" +
		"  *[Uu][Ss][Ee][Rr][Nn][Aa][Mm][Ee]*) printf \"%s\\n\" \"x-access-token\" ;;\n" +
		"  *) cat \"$VERSIONHOO_GIT_TOKEN_FILE\" ;;\nesac\n"
	if string(script) != wantScript {
		t.Fatalf("askpass script:\n%s\nwant:\n%s", script, wantScript)
	}
	if auth.env["GIT_ASKPASS"] != auth.askpassPath || auth.env["GIT_TERMINAL_PROMPT"] != "0" ||
		auth.env["VERSIONHOO_GIT_TOKEN_FILE"] != auth.tokenPath {
		t.Fatalf("auth env %v", auth.env)
	}

	auth.cleanup()
	for _, path := range []string{auth.tokenPath, auth.askpassPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived cleanup", path)
		}
	}
}

// prepareRepo creates a repo directory with a single commit and returns its
// HEAD sha.
func prepareRepo(t *testing.T, extra func(dir string)) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main", dir)
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "tester")
	writeFile(t, filepath.Join(dir, "README.md"), "seed\n")
	if extra != nil {
		extra(dir)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "feat: initial")
	return dir, runGit(t, dir, "rev-parse", "HEAD")
}

func TestChildEnvIsExactlyScrubbed(t *testing.T) {
	repoDir, headSha := prepareRepo(t, nil)
	spec := JobSpec{
		RepositoryFullName: "octo/hello", Branch: "main", HeadSha: headSha,
		Token: "TOK", RepoDir: repoDir,
		InstallCommand: "env > envdump.txt",
	}
	out := Runner(spec)
	if out.Err == nil {
		t.Fatal("expected later failure (missing config) after env capture")
	}
	dump, err := os.ReadFile(filepath.Join(repoDir, "envdump.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(dump)), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			got[parts[0]] = parts[1]
		}
	}
	wantKeys := map[string]bool{
		"PATH": true, "HOME": true, "SHELL": true, "LANG": true,
		"GIT_CONFIG_NOSYSTEM": true, "GIT_CONFIG_SYSTEM": true, "GIT_CONFIG_GLOBAL": true,
		"GIT_TERMINAL_PROMPT": true,
		"GITHUB_REPOSITORY":   true, "GITHUB_REF_NAME": true, "GITHUB_SHA": true,
		"VERSIONHOO_REPOSITORY": true, "VERSIONHOO_BRANCH": true, "VERSIONHOO_SHA": true,
	}
	for key := range got {
		// _ / PWD / SHLVL are injected by the child shell itself, not by us.
		if key == "_" || key == "PWD" || key == "SHLVL" {
			continue
		}
		if !wantKeys[key] {
			t.Errorf("unexpected child env key %s=%s", key, got[key])
		}
	}
	if got["HOME"] == os.Getenv("HOME") {
		t.Error("HOME must be the isolated home")
	}
	if got["VERSIONHOO_REPOSITORY"] != "octo/hello" || got["GITHUB_REF_NAME"] != "main" ||
		got["GITHUB_SHA"] != headSha {
		t.Errorf("webhook context vars wrong: %v", got)
	}
}

func TestInstallFailureRedactsToken(t *testing.T) {
	repoDir, headSha := prepareRepo(t, nil)
	spec := JobSpec{
		RepositoryFullName: "octo/hello", Branch: "main", HeadSha: headSha,
		Token: "SECRETTOKEN123", RepoDir: repoDir,
		InstallCommand: "echo leak SECRETTOKEN123 end; exit 3",
	}
	out := Runner(spec)
	if out.Err == nil {
		t.Fatal("expected install failure")
	}
	mustContain(t, out.Err.Error(), "Install command failed:")
	mustContain(t, out.Err.Error(), "[redacted]")
	if strings.Contains(out.Err.Error(), "SECRETTOKEN123") {
		t.Fatalf("token leaked: %s", out.Err.Error())
	}
}

func TestBunLockSelectsDefaultInstall(t *testing.T) {
	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "bunargs.txt")
	writeFile(t, filepath.Join(binDir, "bun"),
		"#!/bin/sh\nprintf '%s\\n' \"$@\" >> "+argsFile+"\nexit 0\n")
	os.Chmod(filepath.Join(binDir, "bun"), 0o755)

	repoDir, headSha := prepareRepo(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "bun.lock"), "{}")
	})
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	spec := JobSpec{
		RepositoryFullName: "octo/hello", Branch: "main", HeadSha: headSha,
		Token: "TOK", RepoDir: repoDir, InstallCommand: "",
	}
	out := Runner(spec)
	_ = out // config load fails afterwards; install must have run
	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("default bun install not invoked: %v", err)
	}
	mustContain(t, string(rawArgs), "install")
	mustContain(t, string(rawArgs), "--frozen-lockfile")
}

// --- Fake GitHub server -----------------------------------------------------

type capturedRequest struct {
	method string
	path   string
	auth   string
	body   map[string]any
}

type fakeGitHub struct {
	mu     sync.Mutex
	srv    *httptest.Server
	base   *url.URL
	mints  []capturedRequest
	checks []capturedRequest
	repos  []capturedRequest
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{}
	mux := http.NewServeMux()
	record := func(bucket *[]capturedRequest) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			*bucket = append(*bucket, capturedRequest{
				method: r.Method, path: r.URL.Path,
				auth: r.Header.Get("Authorization"), body: body,
			})
			f.mu.Unlock()
		}
	}

	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) {
		record(&f.mints)(w, r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token":"TESTTOKEN","expires_at":"2026-01-01T00:00:00Z"}`)
	})
	mux.HandleFunc("/repos/octo/hello/check-runs", func(w http.ResponseWriter, r *http.Request) {
		record(&f.checks)(w, r)
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":77,"html_url":"https://github.com/octo/hello/runs/77"}`)
		}
	})
	mux.HandleFunc("/repos/octo/hello/check-runs/", func(w http.ResponseWriter, r *http.Request) {
		record(&f.checks)(w, r)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/repos/octo/hello/releases/tags/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/repos/octo/hello/releases", func(w http.ResponseWriter, r *http.Request) {
		record(&f.repos)(w, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":9,"tag_name":"v0.1.0","name":"v0.1.0","body":"","upload_url":"https://x","draft":false,"prerelease":false}`)
	})

	f.srv = httptest.NewTLSServer(mux)
	base, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.base = base

	// Route every DefaultClient request to the fake server for the test's
	// lifetime, trusting its self-signed certificate (same pattern as
	// internal/githubapi's own tests).
	previous := http.DefaultClient.Transport
	http.DefaultClient.Transport = redirectTransport{target: base, base: f.srv.Client().Transport}
	t.Cleanup(func() { http.DefaultClient.Transport = previous })
	t.Cleanup(f.srv.Close)
	return f
}

type redirectTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (tr redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = tr.target.Scheme
	clone.URL.Host = tr.target.Host
	transport := tr.base
	if transport == nil {
		transport = http.DefaultTransport
	}
	return transport.RoundTrip(clone)
}

func (f *fakeGitHub) snapshot() ([]capturedRequest, []capturedRequest, []capturedRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mints, f.checks, f.repos
}

// e2eConfig keeps the production api.github.com URL; the fake server is
// reached through the swapped DefaultClient transport.
func e2eConfig(t *testing.T) *AppConfig {
	return &AppConfig{
		AppID:               "123",
		PrivateKey:          testPrivateKeyPEM(t),
		WebhookSecret:       testWebhookSecret,
		ApiURL:              "https://api.github.com",
		ReleaseBranches:     []string{"main"},
		CIWorkflowNames:     []string{"CI"},
		WebhookMaxBodyBytes: DefaultWebhookMaxBodyBytes,
	}
}

func payloadWithSha(sha string) *WebhookPayload {
	payload, errMsg := DecodeWorkflowRunPayload(webhookPayloadMap(), nil)
	if errMsg != "" {
		panic(errMsg)
	}
	payload.WorkflowRun.HeadSHA = sha
	return payload
}

func withInjectedRepo(t *testing.T, repoDir string) {
	t.Helper()
	previous := buildJobSpec
	buildJobSpec = func(payload *WebhookPayload, cfg *AppConfig, token string) JobSpec {
		spec := previous(payload, cfg, token)
		spec.RepoDir = repoDir
		return spec
	}
	t.Cleanup(func() { buildJobSpec = previous })
}

func TestEndToEndPublishedRelease(t *testing.T) {
	gh := newFakeGitHub(t)

	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	runGit(t, dir, "init", "-q", "--bare", "-b", "main", bare)
	repoDir := filepath.Join(dir, "repo")
	runGit(t, dir, "clone", "-q", bare, repoDir)
	runGit(t, repoDir, "checkout", "-q", "-b", "main")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Hooversion Test")
	writeFile(t, filepath.Join(repoDir, "hooversion.yaml"),
		"packages:\n  - name: mypkg\n    type: node\n")
	writeFile(t, filepath.Join(repoDir, "package.json"),
		`{"name":"mypkg","version":"0.0.1"}`)
	runGit(t, repoDir, "add", "-A")
	runGit(t, repoDir, "commit", "-q", "-m", "feat: initial")
	runGit(t, repoDir, "push", "-q", "-u", "origin", "main")
	headSha := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	withInjectedRepo(t, repoDir)
	cfg := e2eConfig(t)
	err := ReleaseFromWorkflowRun(payloadWithSha(headSha), cfg, Runner)
	if err != nil {
		t.Fatalf("release failed: %v", err)
	}

	mints, checks, repos := gh.snapshot()

	// Token mint: Bearer JWT scoped to the webhook repository id.
	if len(mints) != 1 {
		t.Fatalf("mint calls %d", len(mints))
	}
	if !strings.HasPrefix(mints[0].auth, "Bearer ") {
		t.Fatalf("mint auth %q", mints[0].auth)
	}
	jwtClaims := decodeJWTClaims(t, strings.TrimPrefix(mints[0].auth, "Bearer "))
	if jwtClaims["iss"] != "123" {
		t.Fatalf("jwt iss %v", jwtClaims["iss"])
	}
	iat, exp := jwtClaims["iat"].(float64), jwtClaims["exp"].(float64)
	if exp-iat != 9*60+60 {
		t.Fatalf("jwt window exp-iat=%v", exp-iat)
	}
	mustContain(t, marshalJSON(t, mints[0].body), `"repository_ids":[42]`)

	// Check runs: create then complete.
	var created, completed *capturedRequest
	for i := range checks {
		if checks[i].method == http.MethodPost && created == nil {
			created = &checks[i]
		}
		if checks[i].method == http.MethodPatch {
			completed = &checks[i]
		}
	}
	if created == nil || completed == nil {
		t.Fatalf("check calls %+v", checks)
	}
	if created.body["name"] != "Versionhoo Release" || created.body["head_sha"] != headSha ||
		created.body["status"] != "in_progress" {
		t.Fatalf("create body %v", created.body)
	}
	output := created.body["output"].(map[string]any)
	if output["title"] != "Versionhoo release started" {
		t.Fatalf("created output %v", output)
	}
	if completed.body["conclusion"] != "success" || completed.body["status"] != "completed" {
		t.Fatalf("patch body %v", completed.body)
	}
	completedOutput := completed.body["output"].(map[string]any)
	if completedOutput["title"] != "Versionhoo published releases" {
		t.Fatalf("completed title %v", completedOutput)
	}
	summary, _ := completedOutput["summary"].(string)
	mustContain(t, summary, "- mypkg 0.1.0 (v0.1.0)")

	// Bare remote received the release commit and tag.
	lsRemote := runGit(t, dir, "ls-remote", bare)
	mustContain(t, lsRemote, "refs/tags/v0.1.0")
	mustContain(t, lsRemote, "refs/heads/main")
	pushedMain := ""
	for _, line := range strings.Split(lsRemote, "\n") {
		if strings.HasSuffix(line, "refs/heads/main") {
			pushedMain = strings.Fields(line)[0]
		}
	}
	if pushedMain == headSha {
		t.Fatal("release commit was not pushed")
	}
	runGit(t, dir, "clone", "-q", bare, filepath.Join(dir, "verify"))
	verifyDir := filepath.Join(dir, "verify")
	subject := runGit(t, verifyDir, "log", "-1", "--format=%s|%an|%ae")
	parts := strings.SplitN(subject, "|", 3)
	if parts[0] != "chore(release): mypkg 0.1.0" {
		t.Fatalf("release commit subject %q", parts[0])
	}
	if parts[1] != "versionhoo[bot]" || parts[2] != "versionhoo[bot]@users.noreply.github.com" {
		t.Fatalf("author %q", subject)
	}

	// GitHub release published via the landed client.
	foundReleasePost := false
	for i := range repos {
		if repos[i].method == http.MethodPost {
			foundReleasePost = true
			if repos[i].body["tag_name"] != "v0.1.0" {
				t.Fatalf("release tag_name %v", repos[i].body["tag_name"])
			}
		}
	}
	if !foundReleasePost {
		t.Fatal("GitHub release was not created")
	}
}

func TestStaleGuardNeutralCheck(t *testing.T) {
	gh := newFakeGitHub(t)

	repoDir, headSha := prepareRepo(t, nil)
	withInjectedRepo(t, repoDir)

	cfg := e2eConfig(t)
	staleSha := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	err := ReleaseFromWorkflowRun(payloadWithSha(staleSha), cfg, Runner)
	if err != nil {
		t.Fatalf("stale run surfaced as failure: %v", err)
	}

	_, checks, _ := gh.snapshot()
	var completed *capturedRequest
	for i := range checks {
		if checks[i].method == http.MethodPatch {
			completed = &checks[i]
		}
	}
	if completed == nil {
		t.Fatalf("no completion call: %+v", checks)
	}
	if completed.body["conclusion"] != "neutral" {
		t.Fatalf("conclusion %v", completed.body["conclusion"])
	}
	output := completed.body["output"].(map[string]any)
	if output["title"] != "Versionhoo skipped a stale workflow run" {
		t.Fatalf("title %v", output["title"])
	}
	summary, _ := output["summary"].(string)
	wantPrefix := fmt.Sprintf("Skipped stale workflow run for octo/hello@main: branch is %s, workflow passed on %s.", headSha, staleSha)
	if summary != wantPrefix {
		t.Fatalf("summary %q, want %q", summary, wantPrefix)
	}
}

func TestTruncateSummary(t *testing.T) {
	long := strings.Repeat("a", 60000)
	if got := truncateSummary(long, maxCheckSummaryLength); got != long {
		t.Fatal("at-capacity summary truncated")
	}
	longer := long + "bbb"
	got := truncateSummary(longer, maxCheckSummaryLength)
	if len([]rune(got)) != 60000 || !strings.HasSuffix(got, "...") {
		t.Fatalf("truncate produced %d runes, tail %q", len([]rune(got)), got[len(got)-6:])
	}
}

func TestReleaseCheckResultMapping(t *testing.T) {
	stale := ReleaseCheckResult(Outcome{Outcome: "stale", Message: "custom message"})
	if stale.Conclusion != CheckConclusionNeutral || stale.Title != "Versionhoo skipped a stale workflow run" ||
		stale.Summary != "custom message" {
		t.Fatalf("stale mapping %+v", stale)
	}
	staleDefault := ReleaseCheckResult(Outcome{Outcome: "stale"})
	if staleDefault.Summary != "The release branch moved after this workflow run completed." {
		t.Fatalf("stale default summary %q", staleDefault.Summary)
	}
	noRelease := ReleaseCheckResult(Outcome{Outcome: "no_release"})
	if noRelease.Conclusion != CheckConclusionNeutral || noRelease.Title != "Versionhoo found no release" ||
		noRelease.Summary != "No release-worthy commits were found for this workflow run." {
		t.Fatalf("no_release mapping %+v", noRelease)
	}
	success := ReleaseCheckResult(Outcome{Outcome: "published", Published: true, Releases: []ReleaseRef{
		{Name: "a", Version: "1.0.0", Tag: "v1.0.0"},
		{Name: "b", Version: "2.0.0", Tag: "b-v2.0.0"},
	}})
	if success.Conclusion != CheckConclusionSuccess || success.Title != "Versionhoo published releases" ||
		success.Summary != "- a 1.0.0 (v1.0.0)\n- b 2.0.0 (b-v2.0.0)" {
		t.Fatalf("success mapping %+v", success)
	}
	emptySuccess := ReleaseCheckResult(Outcome{Outcome: "published", Published: true})
	if emptySuccess.Summary != "Versionhoo published releases." {
		t.Fatalf("empty releases fallback %q", emptySuccess.Summary)
	}
	failure := ReleaseFailureCheckResult(errors.New("kaput"))
	if failure.Conclusion != CheckConclusionFailure || failure.Title != "Versionhoo release failed" ||
		failure.Summary != "kaput" {
		t.Fatalf("failure mapping %+v", failure)
	}
}
