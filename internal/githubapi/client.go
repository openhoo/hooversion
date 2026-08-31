// Package githubapi mirrors src/github.ts HTTP parts plus src/app-auth.ts
// token minting and src/app-github.ts check-run calls.
//
// The Client carries the standard GitHub request headers (accept
// application/vnd.github+json, Bearer authorization, x-github-api-version
// 2022-11-28, user-agent hooversion) and the shared failure mapping
// "GitHub API request failed (<status> <statusText>): <body>"; only the
// get-release-by-tag lookup treats 404 as absence.
package githubapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	hverr "github.com/openhoo/hooversion/internal/errors"
)

const (
	githubAPIVersion = "2022-11-28"
	userAgent        = "hooversion"
	githubAccept     = "application/vnd.github+json"
	maxJSONBody      = 32 << 20
	maxErrorBody     = 1 << 20
)

// Client talks to the GitHub REST API with a fixed bearer credential.
type Client struct {
	// BaseURL is the API root without a trailing slash (normalized by New).
	BaseURL string
	// Token is sent as "Authorization: Bearer <Token>" when non-empty.
	Token string
	// HTTP is the transport; nil selects http.DefaultClient.
	HTTP *http.Client
}

// New returns a Client for baseURL, stripping a single trailing slash like
// apiUrl normalization in src/github.ts.
func New(baseURL, token string) *Client {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &Client{BaseURL: baseURL, Token: token}
}

// newRequest builds a request carrying the standard GitHub headers. A
// non-empty contentType is applied after the defaults, matching the header
// merge order in githubFetch.
func (c *Client) newRequest(method, rawURL, contentType string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", githubAccept)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", userAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

// do sends req and applies the response rules shared by src/github.ts and
// src/app-github.ts: notFoundIsEmpty short-circuits 404 for the caller, and
// every other non-2xx becomes the exact user-facing failure message.
func (c *Client) do(req *http.Request, notFoundIsEmpty bool) (*http.Response, error) {
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if notFoundIsEmpty && resp.StatusCode == http.StatusNotFound {
		return resp, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1))
		resp.Body.Close()
		if len(body) > maxErrorBody {
			body = append(body[:maxErrorBody], []byte("...[truncated]")...)
		}
		return nil, hverr.New("GitHub API request failed (%d %s): %s", resp.StatusCode, http.StatusText(resp.StatusCode), body)
	}
	return resp, nil
}

// drainAndClose discards a fully consumed JSON body so the connection can be
// reused.
func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func decodeJSONBody(resp *http.Response, dst any) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody+1))
	if err != nil {
		return err
	}
	if len(data) > maxJSONBody {
		return fmt.Errorf("GitHub API response exceeds %d bytes", maxJSONBody)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return hverr.New("GitHub API response contains trailing JSON values")
		}
		return err
	}
	return nil
}

// encodeURIComponent percent-encodes exactly like the JS helper used for tag
// names and asset names: unreserved characters survive, everything else
// becomes uppercase %XX bytes.
func encodeURIComponent(s string) string {
	const unreserved = "-_.!~*'()"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z',
			c >= 'a' && c <= 'z',
			c >= '0' && c <= '9',
			strings.IndexByte(unreserved, c) >= 0:
			b.WriteByte(c)
		default:
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}
