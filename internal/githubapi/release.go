// Package githubapi — this file mirrors the release endpoints of
// src/github.ts (get-release-by-tag with 404-absence and create-release).
package githubapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// Release mirrors the subset of the GitHub release payload that src/github.ts
// consumes when publishing or reusing releases.
type Release struct {
	ID         int64   `json:"id"`
	TagName    string  `json:"tag_name"`
	Name       string  `json:"name"`
	Body       string  `json:"body"`
	UploadURL  string  `json:"upload_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Asset is one binary attached to a release; names drive upload deduplication.
type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ReleaseInput carries the create-release body fields; draft and prerelease
// are always false, matching src/github.ts.
type ReleaseInput struct {
	TagName string
	Name    string
	Body    string
}

// releaseResponse is the wire shape; body is nullable on the API.
type releaseResponse struct {
	ID         int64   `json:"id"`
	TagName    string  `json:"tag_name"`
	Name       string  `json:"name"`
	Body       *string `json:"body"`
	UploadURL  string  `json:"upload_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

func (r releaseResponse) toRelease() *Release {
	rel := &Release{
		ID:         r.ID,
		TagName:    r.TagName,
		Name:       r.Name,
		UploadURL:  r.UploadURL,
		Draft:      r.Draft,
		Prerelease: r.Prerelease,
		Assets:     r.Assets,
	}
	if r.Body != nil {
		rel.Body = *r.Body
	}
	return rel
}

// ReleaseByTag fetches the release for tag. A 404 is treated as absence and
// returns (nil, nil); every other failure uses the shared error mapping.
func (c *Client) ReleaseByTag(repo, tag string) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", c.BaseURL, repo, encodeURIComponent(tag))
	req, err := c.newRequest(http.MethodGet, url, "", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, nil
	}
	var raw releaseResponse
	if err := decodeJSONBody(resp, &raw); err != nil {
		return nil, err
	}
	return raw.toRelease(), nil
}

// CreateRelease publishes a non-draft, non-prerelease release for tag_name.
func (c *Client) CreateRelease(repo string, in ReleaseInput) (*Release, error) {
	payload, err := json.Marshal(struct {
		TagName    string `json:"tag_name"`
		Name       string `json:"name"`
		Body       string `json:"body"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}{TagName: in.TagName, Name: in.Name, Body: in.Body})
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/repos/%s/releases", c.BaseURL, repo)
	req, err := c.newRequest(http.MethodPost, url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req, false)
	if err != nil {
		return nil, err
	}
	var raw releaseResponse
	if err := decodeJSONBody(resp, &raw); err != nil {
		return nil, err
	}
	return raw.toRelease(), nil
}
