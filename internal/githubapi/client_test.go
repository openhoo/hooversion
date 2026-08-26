package githubapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hverr "github.com/openhoo/hooversion/internal/errors"
)

// newRecordingServer spins up a plain httptest server whose handler records
// each request (method, path, headers, raw body) into the returned recorder.
type recordedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     string
}

type recorder struct {
	requests []recordedRequest
	handler  func(w http.ResponseWriter, r *http.Request, n int)
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec.requests = append(rec.requests, recordedRequest{
		Method:   r.Method,
		Path:     r.URL.EscapedPath(),
		RawQuery: r.URL.RawQuery,
		Header:   r.Header.Clone(),
		Body:     string(body),
	})
	if rec.handler != nil {
		rec.handler(w, r, len(rec.requests))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func newRecordingServer(t *testing.T, rec *recorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return srv
}

const releaseJSONFixture = `{
  "id": 1,
  "html_url": "https://github.com/owner/repo/releases/v1.0.1",
  "upload_url": "https://uploads.github.com/repos/owner/repo/releases/1/assets{?name,label}",
  "tag_name": "v1.0.1",
  "name": "app 1.0.1",
  "body": null,
  "draft": false,
  "prerelease": false,
  "assets": [{"id": 11, "name": "app.tar.gz"}, {"id": 12, "name": "app.zip"}]
}`

func TestNewStripsTrailingSlashAndSetsStandardHeaders(t *testing.T) {
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(releaseJSONFixture))
	}}
	srv := newRecordingServer(t, rec)

	client := New(srv.URL+"/", "tok") // single trailing slash must be stripped
	if _, err := client.ReleaseByTag("owner/repo", "v1.0.1"); err != nil {
		t.Fatalf("ReleaseByTag: %v", err)
	}
	if len(rec.requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(rec.requests))
	}
	got := rec.requests[0]
	if got.Path != "/repos/owner/repo/releases/tags/v1.0.1" {
		t.Errorf("path = %q", got.Path)
	}
	if accept := got.Header.Get("Accept"); accept != "application/vnd.github+json" {
		t.Errorf("accept = %q", accept)
	}
	if auth := got.Header.Get("Authorization"); auth != "Bearer tok" {
		t.Errorf("authorization = %q", auth)
	}
	if v := got.Header.Get("X-GitHub-Api-Version"); v != "2022-11-28" {
		t.Errorf("x-github-api-version = %q", v)
	}
	if ua := got.Header.Get("User-Agent"); ua != "hooversion" {
		t.Errorf("user-agent = %q", ua)
	}
}

// TestReleaseByTagDecodesIdempotencyInputs pins the decoded fields the
// caller compares when detecting an existing release with different metadata
// (tag_name/name/body/draft/prerelease must round-trip exactly, null body
// becomes "").
func TestReleaseByTagDecodesIdempotencyInputs(t *testing.T) {
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(releaseJSONFixture))
	}}
	srv := newRecordingServer(t, rec)

	rel, err := New(srv.URL, "tok").ReleaseByTag("owner/repo", "v1.0.1")
	if err != nil {
		t.Fatalf("ReleaseByTag: %v", err)
	}
	want := &Release{
		ID:        1,
		TagName:   "v1.0.1",
		Name:      "app 1.0.1",
		UploadURL: "https://uploads.github.com/repos/owner/repo/releases/1/assets{?name,label}",
		Assets:    []Asset{{ID: 11, Name: "app.tar.gz"}, {ID: 12, Name: "app.zip"}},
	}
	if rel.ID != want.ID || rel.TagName != want.TagName || rel.Name != want.Name ||
		rel.Body != "" || rel.UploadURL != want.UploadURL || rel.Draft || rel.Prerelease ||
		len(rel.Assets) != 2 || rel.Assets[0] != want.Assets[0] || rel.Assets[1] != want.Assets[1] {
		t.Errorf("decoded release mismatch: %+v", rel)
	}
}

func TestReleaseByTagTreats404AsAbsence(t *testing.T) {
	srv := newRecordingServer(t, &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusNotFound)
	}})
	rel, err := New(srv.URL, "tok").ReleaseByTag("owner/repo", "missing")
	if err != nil {
		t.Fatalf("expected absence without error, got %v", err)
	}
	if rel != nil {
		t.Fatalf("expected nil release, got %+v", rel)
	}
}

func TestReleaseByTagEncodesTagLikeEncodeURIComponent(t *testing.T) {
	tag := "a b+c~d.e!f'g(h)i*"
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{}`))
	}}
	srv := newRecordingServer(t, rec)

	if _, err := New(srv.URL, "tok").ReleaseByTag("owner/repo", tag); err != nil {
		t.Fatalf("ReleaseByTag: %v", err)
	}
	wantPath := "/repos/owner/repo/releases/tags/" + encodeURIComponent(tag)
	if got := rec.requests[0].Path; got != wantPath {
		t.Errorf("path = %q, want %q", got, wantPath)
	}
	if !strings.Contains(wantPath, "%20") || !strings.Contains(wantPath, "%2B") {
		t.Errorf("encoding sanity failed: %q", wantPath)
	}
}

func TestGitHubErrorMappingExactString(t *testing.T) {
	srv := newRecordingServer(t, &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}})
	_, err := New(srv.URL, "tok").ReleaseByTag("owner/repo", "v1")
	const want = "GitHub API request failed (500 Internal Server Error): boom\n"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	var exitErr *hverr.ExitError
	if !errorsAs(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("want ExitError code 1, got %#v", err)
	}
}

func TestCreateReleaseBody(t *testing.T) {
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"id":9,"tag_name":"v2.0.0","upload_url":"https://uploads.github.com/x"}`))
	}}
	srv := newRecordingServer(t, rec)

	rel, err := New(srv.URL, "tok").CreateRelease("owner/repo", ReleaseInput{
		TagName: "v2.0.0",
		Name:    "app 2.0.0",
		Body:    "notes\nbody",
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if rel.ID != 9 || rel.TagName != "v2.0.0" {
		t.Errorf("release = %+v", rel)
	}
	got := rec.requests[0]
	if got.Method != http.MethodPost || got.Path != "/repos/owner/repo/releases" {
		t.Errorf("request = %s %s", got.Method, got.Path)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(got.Body), &body); err != nil {
		t.Fatalf("body json: %v", err)
	}
	if body["tag_name"] != "v2.0.0" || body["name"] != "app 2.0.0" || body["body"] != "notes\nbody" {
		t.Errorf("body = %v", body)
	}
	if body["draft"] != false || body["prerelease"] != false {
		t.Errorf("draft/prerelease = %v/%v, want false/false", body["draft"], body["prerelease"])
	}
}

// errorsAs avoids importing stdlib errors under a name clash in one line.
func errorsAs(err error, target *(*hverr.ExitError)) bool {
	e, ok := err.(*hverr.ExitError)
	if ok {
		*target = e
	}
	return ok
}
