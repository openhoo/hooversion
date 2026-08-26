package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/hooversion/internal/githubapi"
)

const testWebhookSecret = "whsec_test"

func marshalJSONRaw(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func signBody(t *testing.T, secret, body string) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// stubGitHubFlow replaces token minting and check-run clients with local
// fakes so handler tests never touch the network.
func stubGitHubFlow(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/app/installations/"):
			fmt.Fprint(w, `{"token":"TESTTOKEN"}`)
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			fmt.Fprint(w, `{"id":77}`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	prevMint, prevClient := mintToken, newCheckRunClient
	mintToken = func(apiURL, appID string, pemKey string, installationID int64, repoIDs []int64) (string, error) {
		return "TESTTOKEN", nil
	}
	newCheckRunClient = func(apiURL, token string, trusted []string) (*githubapi.Client, error) {
		return &githubapi.Client{BaseURL: srv.URL, Token: token}, nil
	}
	t.Cleanup(func() { mintToken, newCheckRunClient = prevMint, prevClient })
}

func postWebhook(handler http.HandlerFunc, event, delivery, body, signature string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	if event != "" {
		req.Header.Set("x-github-event", event)
	}
	if delivery != "" {
		req.Header.Set("x-github-delivery", delivery)
	}
	if signature != "" {
		req.Header.Set("x-hub-signature-256", signature)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func webhookPayloadMap() map[string]any {
	return map[string]any{
		"action": "completed",
		"repository": map[string]any{
			"id":             42,
			"full_name":      "octo/hello",
			"clone_url":      "https://github.com/octo/hello.git",
			"default_branch": "main",
		},
		"installation": map[string]any{"id": 7},
		"workflow_run": map[string]any{
			"name": "CI", "event": "push", "conclusion": "success",
			"head_branch": "main", "head_sha": "abc123def456", "id": 1001,
			"head_commit":     map[string]any{"message": "feat: add thing"},
			"head_repository": map[string]any{"full_name": "octo/hello"},
		},
	}
}

func marshalJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func newTestHandler(t *testing.T, runner func(JobSpec) Outcome) (http.HandlerFunc, *ReleaseTaskQueue, *WebhookDeduper) {
	t.Helper()
	cfg := &AppConfig{
		AppID:               "123",
		WebhookSecret:       testWebhookSecret,
		ApiURL:              "https://api.github.com",
		ReleaseBranches:     []string{"main"},
		CIWorkflowNames:     []string{"CI"},
		WebhookMaxBodyBytes: DefaultWebhookMaxBodyBytes,
	}
	queue := NewReleaseTaskQueue(func(error) {}, QueueOptions{})
	deduper := NewWebhookDeduper(0, nil)
	return NewWebhookHandler(cfg, runner, queue, deduper), queue, deduper
}

// runGit executes a git command and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
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

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected %q to contain %q", haystack, needle)
	}
}

// testPrivateKeyPEM generates a throwaway RSA private key in PEM form.
func testPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// decodeJWTClaims decodes the payload segment of an RS256 JWT without
// verifying the signature.
func decodeJWTClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt segments %d", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}
