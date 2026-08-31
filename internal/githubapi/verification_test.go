package githubapi

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLatestReleaseDecodesVerificationFieldsWithoutAuthentication(t *testing.T) {
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"id":7,"tag_name":"v2.0.0","target_commitish":"main","html_url":"https://example.test/release","assets":[{"id":8,"name":"app.tar.gz","state":"uploaded","size":4,"digest":"sha256:abcd","browser_download_url":"https://example.test/app"}]}`))
	}}
	srv := newRecordingServer(t, rec)
	release, err := New(srv.URL, "").LatestRelease("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if release.TargetCommitish != "main" || release.HTMLURL != "https://example.test/release" || len(release.Assets) != 1 ||
		release.Assets[0].State != "uploaded" || release.Assets[0].Size != 4 || release.Assets[0].Digest != "sha256:abcd" {
		t.Fatalf("unexpected release: %#v", release)
	}
	if rec.requests[0].Path != "/repos/owner/repo/releases/latest" || rec.requests[0].Header.Get("Authorization") != "" {
		t.Fatalf("unexpected request: %#v", rec.requests[0])
	}
}

func TestDownloadAssetUsesBinaryAcceptAndChecksExactSize(t *testing.T) {
	payload := []byte("artifact")
	rec := &recorder{handler: func(w http.ResponseWriter, request *http.Request, _ int) {
		if request.Header.Get("Accept") != "application/octet-stream" {
			t.Errorf("accept=%q", request.Header.Get("Accept"))
		}
		_, _ = w.Write(payload)
	}}
	srv := newRecordingServer(t, rec)
	destination := filepath.Join(t.TempDir(), "artifact.bin")
	digest, err := New(srv.URL, "token").DownloadAsset("owner/repo", Asset{ID: 9, Name: "artifact.bin", Size: int64(len(payload))}, destination, 100)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if digest != fmt.Sprintf("sha256:%x", sum[:]) || rec.requests[0].Path != "/repos/owner/repo/releases/assets/9" {
		t.Fatalf("digest=%q request=%#v", digest, rec.requests[0])
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != string(payload) {
		t.Fatalf("data=%q err=%v", data, err)
	}

	wrong := filepath.Join(t.TempDir(), "wrong.bin")
	if _, err := New(srv.URL, "token").DownloadAsset("owner/repo", Asset{ID: 9, Name: "artifact.bin", Size: 1}, wrong, 100); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("size mismatch accepted: %v", err)
	}
	if _, err := os.Stat(wrong); !os.IsNotExist(err) {
		t.Fatalf("partial download retained: %v", err)
	}
}

func TestResolveTagHandlesLightweightAndNestedAnnotatedTags(t *testing.T) {
	commit := strings.Repeat("c", 40)
	first := strings.Repeat("a", 40)
	second := strings.Repeat("b", 40)
	rec := &recorder{handler: func(w http.ResponseWriter, request *http.Request, _ int) {
		switch request.URL.EscapedPath() {
		case "/repos/owner/repo/git/ref/tags/v1.0.0":
			_, _ = w.Write([]byte(`{"ref":"refs/tags/v1.0.0","object":{"type":"tag","sha":"` + first + `"}}`))
		case "/repos/owner/repo/git/tags/" + first:
			_, _ = w.Write([]byte(`{"sha":"` + first + `","object":{"type":"tag","sha":"` + second + `"},"verification":{"verified":true,"reason":"valid"}}`))
		case "/repos/owner/repo/git/tags/" + second:
			_, _ = w.Write([]byte(`{"sha":"` + second + `","object":{"type":"commit","sha":"` + commit + `"},"verification":{"verified":false,"reason":"unsigned"}}`))
		default:
			http.NotFound(w, request)
		}
	}}
	srv := newRecordingServer(t, rec)
	resolved, err := New(srv.URL, "token").ResolveTag("owner/repo", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CommitSHA != commit || !resolved.Annotated || resolved.AllSignaturesVerified || strings.Join(resolved.VerificationReasons, ",") != "valid,unsigned" {
		t.Fatalf("unexpected resolved tag: %#v", resolved)
	}

	rec = &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"object":{"type":"commit","sha":"` + commit + `"}}`))
	}}
	srv = newRecordingServer(t, rec)
	resolved, err = New(srv.URL, "token").ResolveTag("owner/repo", "v2")
	if err != nil || resolved.CommitSHA != commit || resolved.Annotated || !resolved.AllSignaturesVerified {
		t.Fatalf("lightweight result=%#v err=%v", resolved, err)
	}
}
