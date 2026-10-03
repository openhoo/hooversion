package release

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/openhoo/hooversion/internal/githubapi"
	"github.com/openhoo/hooversion/internal/types"
)

func TestPublicationRecoveryVerifiesContentAndUploadsOnlyMissing(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"existing.bin", "missing.bin"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("payload"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			expected := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("payload")))
			if mismatch {
				expected = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("changed")))
			}
			var uploaded []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/releases/tags/v1.0.1":
					json.NewEncoder(w).Encode(map[string]any{"id": 42, "tag_name": "v1.0.1", "name": "app 1.0.1", "body": "notes", "upload_url": "https://uploads.github.com/upload{?name,label}"})
				case "/repos/o/r/releases/42/assets":
					json.NewEncoder(w).Encode([]githubapi.Asset{{ID: 1, Name: "existing.bin", State: "uploaded", Size: 7, Digest: expected}})
				case "/upload":
					uploaded = append(uploaded, r.URL.Query().Get("name"))
					w.WriteHeader(http.StatusCreated)
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			old := newGitHubClient
			newGitHubClient = func(base, token string) *githubapi.Client {
				c := githubapi.New("https://api.github.com", token)
				target, _ := url.Parse(server.URL)
				c.HTTP = &http.Client{Transport: rewriteTransport{target: target, base: server.Client().Transport}}
				return c
			}
			defer func() { newGitHubClient = old }()
			cfg := singleAppConfig(false)
			cfg.GitHub = types.GitHubSettings{Enabled: true, Releases: true, Repository: "o/r", ApiUrl: server.URL}
			plan := &types.ReleasePlan{Releases: []types.PackageRelease{{Package: types.NormalizedPackageConfig{Name: "app", Assets: []string{"existing.bin", "missing.bin"}}, Tag: "v1.0.1", NextVersion: "1.0.1", Notes: "notes"}}}
			err := publishGitHubReleases(root, cfg, plan, "token")
			if mismatch {
				if err == nil || len(uploaded) != 0 {
					t.Fatalf("mismatch err=%v uploads=%v", err, uploaded)
				}
			} else {
				if err != nil || fmt.Sprint(uploaded) != "[missing.bin]" {
					t.Fatalf("matching err=%v uploads=%v", err, uploaded)
				}
			}
		})
	}
}
