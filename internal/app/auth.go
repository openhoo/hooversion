// This file mirrors src/app-auth.ts: private-key resolution, webhook
// signature verification, and GitHub API URL / repository identity
// validation. Token minting itself lives in internal/githubapi.
package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"regexp"
	"strings"

	hverr "github.com/openhoo/hooversion/internal/errors"
)

// ReadGitHubAppPrivateKey resolves the GitHub App private key from env with
// VERSIONHOO_* names preferred over HOOVERSION_* aliases. An inline key may
// carry literal "\n" sequences which are unescaped; otherwise the key is read
// from the private-key path variable.
func ReadGitHubAppPrivateKey(getenv func(string) string) (string, error) {
	inline := firstEnv(getenv, "VERSIONHOO_PRIVATE_KEY", "HOOVERSION_PRIVATE_KEY")
	if inline != "" {
		return normalizePrivateKey(inline), nil
	}
	path := firstEnv(getenv, "VERSIONHOO_PRIVATE_KEY_PATH", "HOOVERSION_PRIVATE_KEY_PATH")
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return "", hverr.New("VERSIONHOO_PRIVATE_KEY or VERSIONHOO_PRIVATE_KEY_PATH is required to authenticate as a GitHub App.")
}

func normalizePrivateKey(value string) string {
	if strings.Contains(value, "\\n") {
		return strings.ReplaceAll(value, "\\n", "\n")
	}
	return value
}

// VerifyWebhookSignature mirrors verifyGitHubWebhookSignature: the header must
// start with "sha256=" and match the HMAC-SHA256 hex digest of the body under
// a constant-time comparison over the full "sha256=<hex>" strings.
func VerifyWebhookSignature(secret, body, signatureHeader string) bool {
	if !strings.HasPrefix(signatureHeader, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signatureHeader))
}

// ValidateGitHubApiURL mirrors validateGitHubApiUrl: https only, no userinfo,
// port, query, or hash; anything other than api.github.com root must be
// listed in trustedApiURLs. Returns the normalized URL without a trailing
// slash.
func ValidateGitHubApiURL(apiURL string, trustedApiURLs []string) (string, error) {
	parsed, err := parseHTTPSUrl(apiURL, "GitHub API")
	if err != nil {
		return "", err
	}
	normalized := normalizeURLOrigin(parsed)
	trusted := make([]string, 0, len(trustedApiURLs))
	for _, value := range trustedApiURLs {
		t, err := parseHTTPSUrl(value, "trusted GitHub API")
		if err != nil {
			return "", err
		}
		trusted = append(trusted, normalizeURLOrigin(t))
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "api.github.com" || urlPathOf(parsed) != "/" {
		found := false
		for _, t := range trusted {
			if t == normalized {
				found = true
				break
			}
		}
		if !found {
			return "", hverr.New("Untrusted GitHub API URL: %s", apiURL)
		}
	}
	return strings.TrimRight(normalized, "/"), nil
}

var repositoryNamePartRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ValidateRepositoryFullName mirrors validateRepositoryFullName: exactly
// owner/name with each part matching ^[A-Za-z0-9][A-Za-z0-9_.-]*$ and neither
// "." nor "..".
func ValidateRepositoryFullName(repository string) (string, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 {
		return "", hverr.New("Invalid GitHub repository identity: %s", repository)
	}
	for _, part := range parts {
		if !repositoryNamePartRE.MatchString(part) || part == "." || part == ".." {
			return "", hverr.New("Invalid GitHub repository identity: %s", repository)
		}
	}
	return parts[0] + "/" + parts[1], nil
}

// parseHTTPSUrl mirrors parseHttpsUrl(value, label).
func parseHTTPSUrl(value, label string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() {
		return nil, hverr.New("Invalid %s URL: %s", label, value)
	}
	if parsed.Scheme != "https" ||
		parsed.User != nil ||
		parsed.Port() != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, hverr.New("Invalid %s URL: %s", label, value)
	}
	return parsed, nil
}

// normalizeURLOrigin mirrors normalizeOrigin: protocol://host plus the
// pathname with trailing slashes collapsed to a single "/".
func normalizeURLOrigin(u *url.URL) string {
	path := u.Path
	for len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	if path == "" {
		path = "/"
	}
	return u.Scheme + "://" + u.Host + path
}

// urlPathOf mirrors JS URL.pathname, which is always at least "/".
func urlPathOf(u *url.URL) string {
	if u.Path == "" {
		return "/"
	}
	return u.Path
}
