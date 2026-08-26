package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func getenvFrom(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func TestReadGitHubAppPrivateKey(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyFile, []byte("FILEKEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr string
	}{
		{"inline preferred over alias", map[string]string{"VERSIONHOO_PRIVATE_KEY": "A", "HOOVERSION_PRIVATE_KEY": "B"}, "A", ""},
		{"alias fallback", map[string]string{"HOOVERSION_PRIVATE_KEY": "B"}, "B", ""},
		{"literal newline unescape", map[string]string{"VERSIONHOO_PRIVATE_KEY": `a\nb`}, "a\nb", ""},
		{"path read", map[string]string{"VERSIONHOO_PRIVATE_KEY_PATH": keyFile}, "FILEKEY", ""},
		{"path alias", map[string]string{"HOOVERSION_PRIVATE_KEY_PATH": keyFile}, "FILEKEY", ""},
		{
			"missing key",
			map[string]string{},
			"",
			"VERSIONHOO_PRIVATE_KEY or VERSIONHOO_PRIVATE_KEY_PATH is required to authenticate as a GitHub App.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadGitHubAppPrivateKey(getenvFrom(tc.env))
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("want error %q, got %v", tc.wantErr, err)
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
}

func TestVerifyWebhookSignature(t *testing.T) {
	body := marshalJSON(t, webhookPayloadMap())
	if !VerifyWebhookSignature(testWebhookSecret, body, signBody(t, testWebhookSecret, body)) {
		t.Fatal("valid signature rejected")
	}
	if VerifyWebhookSignature(testWebhookSecret, body, signBody(t, "other-secret", body)) {
		t.Fatal("wrong secret accepted")
	}
	if VerifyWebhookSignature(testWebhookSecret, body, "sha256="+strings.Repeat("0", 64)) {
		t.Fatal("garbage digest accepted")
	}
	if VerifyWebhookSignature(testWebhookSecret, body, "md5=deadbeef") {
		t.Fatal("wrong prefix accepted")
	}
	if VerifyWebhookSignature(testWebhookSecret, body, "") {
		t.Fatal("empty header accepted")
	}
}

func TestValidateGitHubApiURL(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		trusted   []string
		want      string
		wantError string
	}{
		{"default api", "https://api.github.com/", nil, "https://api.github.com", ""},
		{"no path", "https://api.github.com", nil, "https://api.github.com", ""},
		{"ghes trusted", "https://ghe.example.com/api/v3", []string{"https://ghe.example.com/api/v3/"}, "https://ghe.example.com/api/v3", ""},
		{"untrusted host", "https://evil.example.com", nil, "", "Untrusted GitHub API URL: https://evil.example.com"},
		{"untrusted path", "https://api.github.com/other", nil, "", "Untrusted GitHub API URL: https://api.github.com/other"},
		{"http scheme", "http://api.github.com", nil, "", "Invalid GitHub API URL: http://api.github.com"},
		{"userinfo", "https://u@github.com", nil, "", "Invalid GitHub API URL: https://u@github.com"},
		{"port", "https://github.com:8443", nil, "", "Invalid GitHub API URL: https://github.com:8443"},
		{"query", "https://api.github.com?x=1", nil, "", "Invalid GitHub API URL: https://api.github.com?x=1"},
		{"hash", "https://api.github.com#frag", nil, "", "Invalid GitHub API URL: https://api.github.com#frag"},
		{"not a url", ":::", nil, "", "Invalid GitHub API URL: :::"},
		{"bad trusted entry", "https://api.github.com", []string{"http://nope"}, "", "Invalid trusted GitHub API URL: http://nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateGitHubApiURL(tc.url, tc.trusted)
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
}

func TestValidateRepositoryFullName(t *testing.T) {
	cases := []struct {
		input     string
		want      string
		wantError string
	}{
		{"octo/hello", "octo/hello", ""},
		{"Owner_1.Name/hello.zip", "Owner_1.Name/hello.zip", ""},
		{".", "", "Invalid GitHub repository identity: ."},
		{"../hello", "", "Invalid GitHub repository identity: ../hello"},
		{"./hello", "", "Invalid GitHub repository identity: ./hello"},
		{"octo", "", "Invalid GitHub repository identity: octo"},
		{"a/b/c", "", "Invalid GitHub repository identity: a/b/c"},
		{"a//b", "", "Invalid GitHub repository identity: a//b"},
		{"-lead/name", "", "Invalid GitHub repository identity: -lead/name"},
		{"o cto/name", "", "Invalid GitHub repository identity: o cto/name"},
		{"octo/naïme", "", "Invalid GitHub repository identity: octo/naïme"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ValidateRepositoryFullName(tc.input)
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
}
