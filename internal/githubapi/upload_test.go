package githubapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	hverr "github.com/openhoo/hooversion/internal/errors"
)

// rewriteTransport redirects every request to a local TLS test server while
// keeping the original https URL (no port, real host) visible to the client's
// validation logic — the Go analogue of mocking global fetch.
type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (tr rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rewritten := req.Clone(req.Context())
	rewritten.URL.Scheme = tr.target.Scheme
	rewritten.URL.Host = tr.target.Host
	return tr.base.RoundTrip(rewritten)
}

// newTLSRewriteServer returns a TLS server plus an http.Client that sends all
// requests to it regardless of their nominal https origin.
func newTLSRewriteServer(t *testing.T, rec *recorder) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(rec)
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	client := &http.Client{
		Transport: rewriteTransport{target: target, base: srv.Client().Transport},
	}
	return srv, client
}

func TestUploadAssetHappyPath(t *testing.T) {
	var hits int32
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"id":2}`))
	}}
	srv := httptest.NewTLSServer(rec)
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)

	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "app.zip"), []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := New("https://api.github.com", "token")
	client.HTTP = &http.Client{Transport: rewriteTransport{target: target, base: srv.Client().Transport}}

	err := client.UploadAsset(
		"https://uploads.github.com/repos/owner/repo/releases/1/assets{?name,label}",
		"app.zip",
		"app.zip",
	)
	if err != nil {
		t.Fatalf("UploadAsset: %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("upload requests = %d, want 1", hits)
	}
	got := rec.requests[0]
	if got.Method != http.MethodPost {
		t.Errorf("method = %s", got.Method)
	}
	if got.Path != "/repos/owner/repo/releases/1/assets" || got.RawQuery != "name=app.zip" {
		t.Errorf("url = %s?%s", got.Path, got.RawQuery)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("content-type = %q", ct)
	}
	if auth := got.Header.Get("Authorization"); auth != "Bearer token" {
		t.Errorf("authorization = %q", auth)
	}
	if got.Body != "archive" {
		t.Errorf("body = %q", got.Body)
	}
}

func TestAssertSafeReleaseAssetPath(t *testing.T) {
	cases := []struct{ asset string }{
		{""},
		{"a\x00b"},
		{"/etc/passwd"},
		{"C:\\temp\\a.bin"},
		{"C:/temp/a.bin"},
		{"\\\\server\\share\\a.bin"},
		{".."},
		{"../outside.bin"},
		{"sub/../outside.bin"},
		{"a/b/../../c.bin"},
	}
	for _, tc := range cases {
		err := assertSafeReleaseAssetPath(tc.asset)
		if err == nil {
			t.Errorf("assertSafeReleaseAssetPath(%q) = nil, want rejection", tc.asset)
			continue
		}
		const wantPrefix = "Release asset path must be relative without parent traversal:"
		if !strings.HasPrefix(err.Error(), wantPrefix) {
			t.Errorf("assertSafeReleaseAssetPath(%q) = %v, want prefix %q", tc.asset, err, wantPrefix)
		}
	}
	for _, ok := range []string{"app.tar.gz", "sub/dir/a.bin", "dist/app.zip"} {
		if err := assertSafeReleaseAssetPath(ok); err != nil {
			t.Errorf("assertSafeReleaseAssetPath(%q) = %v, want nil", ok, err)
		}
	}
}

func TestValidateUploadURL(t *testing.T) {
	okCases := []struct {
		baseURL, template, want string
	}{
		{"https://api.github.com",
			"https://uploads.github.com/repos/o/r/releases/1/assets{?name,label}",
			"https://uploads.github.com/repos/o/r/releases/1/assets"},
		{"https://api.github.com",
			"https://api.github.com/repos/o/r/releases/1/assets",
			"https://api.github.com/repos/o/r/releases/1/assets"},
		{"https://ghe.example/api",
			"https://ghe.example/api/repos/o/r/releases/1/assets{?name}",
			"https://ghe.example/api/repos/o/r/releases/1/assets"},
	}
	for _, tc := range okCases {
		got, err := (&Client{BaseURL: tc.baseURL}).validateUploadURL(tc.template)
		if err != nil {
			t.Errorf("validateUploadURL(%q with base %q) = %v, want nil", tc.template, tc.baseURL, err)
			continue
		}
		if got != tc.want {
			t.Errorf("validateUploadURL(%q) = %q, want %q", tc.template, got, tc.want)
		}
	}

	badCases := []struct{ baseURL, template, wantPrefix string }{
		{"https://api.github.com", "https://evil.example/repos/o/r/releases/1/assets{?name}", "Untrusted GitHub release upload URL:"},
		{"https://ghe.example/api", "https://uploads.github.com/x", "Untrusted GitHub release upload URL:"},
		{"https://api.github.com", "http://uploads.github.com/repos/o/r/assets", "Invalid GitHub release upload URL:"},
		{"https://api.github.com", "https://user:pass@uploads.github.com/repos/o/r/assets", "Invalid GitHub release upload URL:"},
		{"https://api.github.com", "https://uploads.github.com:8443/repos/o/r/assets", "Invalid GitHub release upload URL:"},
		{"https://api.github.com", "https://[::1]:443/repos/o/r/assets", "Invalid GitHub release upload URL:"},
		{"https://api.github.com", "https://uploads.github.com/repos/o/r/assets?x=1", "Invalid GitHub release upload URL:"},
		{"https://api.github.com", "https://uploads.github.com/repos/o/r/assets#frag", "Invalid GitHub release upload URL:"},
		{"https://api.github.com", "https://uploads.github.com/repos/{name}", "Invalid GitHub release upload URL:"},
		{"https://api.github.com", "::not a url", "Invalid GitHub release upload URL:"},
	}
	for _, tc := range badCases {
		_, err := (&Client{BaseURL: tc.baseURL}).validateUploadURL(tc.template)
		if err == nil {
			t.Errorf("validateUploadURL(%q with base %q) = nil, want rejection", tc.template, tc.baseURL)
			continue
		}
		if !strings.HasPrefix(err.Error(), tc.wantPrefix) {
			t.Errorf("validateUploadURL(%q) = %v, want prefix %q", tc.template, err, tc.wantPrefix)
		}
	}
}

func TestUploadAssetRejectsUnsafeAssetsBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name       string
		asset      string
		setup      func(root string) error
		wantErrSub string
	}{
		{name: "absolute path", asset: "/etc/passwd", wantErrSub: "must be relative without parent traversal"},
		{name: "parent traversal", asset: "../outside.bin", wantErrSub: "must be relative without parent traversal"},
		{name: "nul byte", asset: "a\x00b", wantErrSub: "must be relative without parent traversal"},
		{name: "symlinked file", asset: "asset-link", setup: func(root string) error {
			return os.Symlink("/etc/passwd", filepath.Join(root, "asset-link"))
		}, wantErrSub: "Could not securely read release asset asset-link"},
		{name: "directory", asset: "asset-dir", setup: func(root string) error {
			return os.Mkdir(filepath.Join(root, "asset-dir"), 0o755)
		}, wantErrSub: "Release asset must be a regular file, not a symbolic link: asset-dir"},
		{name: "oversized file", asset: "asset.bin", setup: func(root string) error {
			p := filepath.Join(root, "asset.bin")
			if err := os.WriteFile(p, []byte(""), 0o644); err != nil {
				return err
			}
			return os.Truncate(p, maxReleaseAssetSizeBytes+1)
		}, wantErrSub: "Release asset exceeds the 104857600 byte upload limit: asset.bin"},
		{name: "symlink dir escape", asset: "jail/outside.bin", setup: func(root string) error {
			parent := filepath.Dir(root)
			outside := filepath.Join(parent, "outside")
			if err := os.Mkdir(outside, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(outside, "outside.bin"), []byte("x"), 0o644); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(root, "jail"))
		}, wantErrSub: "Release asset path escapes the repository: jail/outside.bin"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if tc.setup != nil {
				if err := tc.setup(root); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}

			var hits int32
			rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
				atomic.AddInt32(&hits, 1)
				_, _ = w.Write([]byte(`{}`))
			}}
			_, httpClient := newTLSRewriteServer(t, rec)
			client := New("https://api.github.com", "token")
			client.HTTP = httpClient
			err := client.UploadAsset(
				"https://uploads.github.com/repos/owner/repo/releases/1/assets{?name,label}",
				"asset.bin",
				tc.asset,
			)
			if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErrSub)
			}
			if _, isExit := err.(*hverr.ExitError); !isExit {
				t.Fatalf("want ExitError, got %#v", err)
			}
			if atomic.LoadInt32(&hits) != 0 {
				t.Errorf("upload requests = %d, want 0", hits)
			}
		})
	}
}

func TestReadExactlyDetectsShrinkingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.bin")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	_, err = readExactly(f, 10, "a.bin") // more bytes than exist
	const want = "Release asset changed while it was being read: a.bin"
	if err == nil || err.Error() != want {
		t.Fatalf("readExactly = %v, want %q", err, want)
	}
}

func TestSameReleaseAssetMetadata(t *testing.T) {
	base := releaseAssetMetadataT{dev: 1, ino: 2, size: 3, mtimeSec: 4, mtimeNsec: 5, ctimeSec: 6, ctimeNsec: 7}
	if !sameReleaseAssetMetadata(base, base) {
		t.Error("identical metadata compared unequal")
	}
	for _, mutate := range []func(*releaseAssetMetadataT){
		func(m *releaseAssetMetadataT) { m.dev++ },
		func(m *releaseAssetMetadataT) { m.ino++ },
		func(m *releaseAssetMetadataT) { m.size++ },
		func(m *releaseAssetMetadataT) { m.mtimeSec++ },
		func(m *releaseAssetMetadataT) { m.mtimeNsec++ },
		func(m *releaseAssetMetadataT) { m.ctimeSec++ },
		func(m *releaseAssetMetadataT) { m.ctimeNsec++ },
	} {
		other := base
		mutate(&other)
		if sameReleaseAssetMetadata(base, other) {
			t.Error("mutated metadata compared equal")
		}
	}
}

func TestUploadAssetUntrustedURLRejectedBeforeReading(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	// Note: deliberately do NOT create the asset; validation of the URL must
	// fail first and never touch the filesystem path.

	var hits int32
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{}`))
	}}
	srv, httpClient := newTLSRewriteServer(t, rec)

	client := New("https://api.github.com", "token")
	client.HTTP = httpClient
	err := client.UploadAsset("https://evil.example/repos/o/r/assets{?name}", "x.bin", "x.bin")
	if err == nil || !strings.Contains(err.Error(), "Untrusted GitHub release upload URL") {
		t.Fatalf("err = %v, want untrusted-upload-url rejection", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("requests = %d, want 0", hits)
	}
	_ = srv
}
