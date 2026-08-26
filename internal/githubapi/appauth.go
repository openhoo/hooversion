// Package githubapi — this file mirrors the token-minting HTTP parts of
// src/app-auth.ts: createGitHubAppJwt (RS256, iat backdated 60s,
// exp now+9m, iss = app id) and createInstallationAccessToken
// (POST {api}/app/installations/{id}/access_tokens scoped by repository_ids).
package githubapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	hverr "github.com/openhoo/hooversion/internal/errors"
)

const versionhooAppUserAgent = "versionhoo-app"

// installationHTTPClient is a test seam; production uses http.DefaultClient.
var installationHTTPClient = http.DefaultClient

// MintInstallationToken exchanges a GitHub App JWT for an installation access
// token scoped to repositoryIDs. The api URL must be an absolute https URL
// without userinfo, port, query, or fragment; host trust is the caller's
// concern (the app layer validates against its trusted API URLs).
func MintInstallationToken(apiURL, appID, privateKeyPEM string, installationID int64, repositoryIDs []int64) (string, error) {
	if installationID <= 0 {
		return "", hverr.New("GitHub App installation id must be a positive integer.")
	}
	api, err := validateStructuralAPIURL(apiURL)
	if err != nil {
		return "", err
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return "", fmt.Errorf("could not parse GitHub App private key: %w", err)
	}
	jwtToken, err := mintAppJWT(appID, key)
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(struct {
		RepositoryIDs []int64 `json:"repository_ids"`
	}{RepositoryIDs: repositoryIDs})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, api+"/app/installations/"+fmt.Sprintf("%d", installationID)+"/access_tokens", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", githubAccept)
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", versionhooAppUserAgent)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)

	resp, err := installationHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		return "", hverr.New("GitHub App installation token request failed (%d %s): %s", resp.StatusCode, http.StatusText(resp.StatusCode), body)
	}
	var decoded struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", err
	}
	return decoded.Token, nil
}

// mintAppJWT mirrors createGitHubAppJwt in src/app-auth.ts.
func mintAppJWT(appID string, key any) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": appID,
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}

// validateStructuralAPIURL mirrors parseHttpsUrl(value, "GitHub API") in
// src/app-auth.ts: https only, no userinfo, no explicit port, no query, no
// fragment. Trailing slashes are stripped like normalizeOrigin.
func validateStructuralAPIURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil ||
		!parsed.IsAbs() ||
		parsed.Scheme != "https" ||
		parsed.User != nil ||
		parsed.Port() != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return "", hverr.New("Invalid GitHub API URL: %s", value)
	}
	return strings.TrimRight(value, "/"), nil
}
