package githubapi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// generateTestAppKey creates a PKCS#8 PEM RSA key like GitHub App private keys.
func generateTestAppKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// useTLSRewrite points the package-level installation token HTTP client at a
// local TLS server while requests keep their nominal https origin.
func useTLSRewrite(t *testing.T, rec *recorder) {
	t.Helper()
	srv := httptest.NewTLSServer(rec)
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	prev := installationHTTPClient
	installationHTTPClient = &http.Client{Transport: rewriteTransport{target: target, base: srv.Client().Transport}}
	t.Cleanup(func() { installationHTTPClient = prev })
}

func TestMintInstallationTokenScopesRepositoryIDsAndVerifiableJWT(t *testing.T) {
	key, pemKey := generateTestAppKey(t)

	var gotHeader http.Header
	rec := &recorder{handler: func(w http.ResponseWriter, r *http.Request, _ int) {
		gotHeader = r.Header.Clone()
		_, _ = w.Write([]byte(`{"token":"installation-token","expires_at":"tomorrow"}`))
	}}
	useTLSRewrite(t, rec)

	before := time.Now().Unix()
	token, err := MintInstallationToken("https://api.github.com", "12345", pemKey, 42, []int64{987})
	after := time.Now().Unix()
	if err != nil {
		t.Fatalf("MintInstallationToken: %v", err)
	}
	if token != "installation-token" {
		t.Errorf("token = %q", token)
	}
	if len(rec.requests) != 1 {
		t.Fatalf("request count = %d", len(rec.requests))
	}
	req := rec.requests[0]
	if req.Path != "/app/installations/42/access_tokens" {
		t.Errorf("path = %q", req.Path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("body json: %v (%q)", err, req.Body)
	}
	if !reflect.DeepEqual(body["repository_ids"], []any{float64(987)}) {
		t.Errorf("repository_ids = %v, want [987]", body["repository_ids"])
	}

	auth := gotHeader.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("authorization = %q", auth)
	}
	jwtString := strings.TrimPrefix(auth, "Bearer ")
	parsed, err := jwt.Parse(jwtString, func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
		jwt.WithValidMethods([]string{"RS256"}))
	if err != nil || parsed == nil || !parsed.Valid {
		t.Fatalf("jwt parse: err=%v valid=%v", err, parsed != nil && parsed.Valid)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims type %T", parsed.Claims)
	}
	if claims["iss"] != "12345" {
		t.Errorf("iss = %v", claims["iss"])
	}
	iat, exp := int64(claims["iat"].(float64)), int64(claims["exp"].(float64))
	if iat < before-60 || iat > after-60 {
		t.Errorf("iat = %d not in [%d,%d]", iat, before-60, after-60)
	}
	if exp-iat != 600 {
		t.Errorf("exp-iat = %d, want 600", exp-iat)
	}

	if accept := gotHeader.Get("Accept"); accept != "application/vnd.github+json" {
		t.Errorf("accept = %q", accept)
	}
	if ua := gotHeader.Get("User-Agent"); ua != "versionhoo-app" {
		t.Errorf("user-agent = %q", ua)
	}
	if v := gotHeader.Get("X-GitHub-Api-Version"); v != "2022-11-28" {
		t.Errorf("x-github-api-version = %q", v)
	}
	if ct := gotHeader.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
}

func TestMintInstallationTokenTrailingSlashAPIURL(t *testing.T) {
	_, pemKey := generateTestAppKey(t)
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"token":"t","expires_at":"e"}`))
	}}
	useTLSRewrite(t, rec)

	if _, err := MintInstallationToken("https://api.github.com/", "1", pemKey, 7, nil); err != nil {
		t.Fatalf("MintInstallationToken: %v", err)
	}
	if gotPath := rec.requests[0].Path; gotPath != "/app/installations/7/access_tokens" {
		t.Errorf("path = %q (trailing slash must be normalized)", gotPath)
	}
}

func TestMintInstallationTokenRejectsInvalidStructuralAPIURLs(t *testing.T) {
	_, pemKey := generateTestAppKey(t)
	var calls int32
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		calls++
		_, _ = w.Write([]byte(`{}`))
	}}
	useTLSRewrite(t, rec)

	for _, apiURL := range []string{
		"http://api.github.com",
		"https://user@api.github.com",
		"https://user:pass@api.github.com",
		"https://api.github.com:8443",
		"https://api.github.com/?x=1",
		"https://api.github.com/#frag",
	} {
		token, err := MintInstallationToken(apiURL, "1", pemKey, 42, nil)
		if err == nil || !strings.HasPrefix(err.Error(), "Invalid GitHub API URL: ") {
			t.Errorf("MintInstallationToken(%q) error = %v, want Invalid GitHub API URL prefix", apiURL, err)
		}
		if token != "" {
			t.Errorf("token = %q, want empty", token)
		}
	}
	if calls != 0 {
		t.Errorf("requests = %d, want 0", calls)
	}
}

func TestMintInstallationTokenRejectsNonPositiveInstallationID(t *testing.T) {
	for _, id := range []int64{0, -5} {
		_, err := MintInstallationToken("https://api.github.com", "1", "unused", id, nil)
		const want = "GitHub App installation id must be a positive integer."
		if err == nil || err.Error() != want {
			t.Errorf("id=%d error = %v, want %q", id, err, want)
		}
	}
}

func TestMintInstallationTokenInvalidPrivateKeyFailsBeforeRequest(t *testing.T) {
	rec := &recorder{}
	useTLSRewrite(t, rec)

	if _, err := MintInstallationToken("https://api.github.com", "1", "not-a-pem", 42, nil); err == nil ||
		!strings.Contains(err.Error(), "could not parse GitHub App private key") {
		t.Fatalf("err = %v, want private-key parse failure", err)
	}
	if len(rec.requests) != 0 {
		t.Errorf("requests = %d, want 0", len(rec.requests))
	}
}

func TestMintInstallationTokenHTTPErrorExactString(t *testing.T) {
	_, pemKey := generateTestAppKey(t)
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		http.Error(w, "oops", http.StatusInternalServerError)
	}}
	useTLSRewrite(t, rec)

	_, err := MintInstallationToken("https://api.github.com", "1", pemKey, 42, []int64{987})
	const want = "GitHub App installation token request failed (500 Internal Server Error): oops\n"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
