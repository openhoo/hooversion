package githubapi

import (
	"fmt"
	"net/http"
	"regexp"
)

var objectSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type gitObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type gitReference struct {
	Ref    string    `json:"ref"`
	Object gitObject `json:"object"`
}

type gitTag struct {
	SHA          string    `json:"sha"`
	Object       gitObject `json:"object"`
	Verification struct {
		Verified bool   `json:"verified"`
		Reason   string `json:"reason"`
	} `json:"verification"`
}

// ResolvedTag identifies the commit behind a lightweight or recursively
// annotated tag and reports GitHub's verification result for every annotated
// tag object encountered.
type ResolvedTag struct {
	CommitSHA             string
	Annotated             bool
	AllSignaturesVerified bool
	VerificationReasons   []string
}

func (c *Client) ResolveTag(repo, tag string) (ResolvedTag, error) {
	url := fmt.Sprintf("%s/repos/%s/git/ref/tags/%s", c.BaseURL, repo, encodeURIComponent(tag))
	req, err := c.newRequest(http.MethodGet, url, "", nil)
	if err != nil {
		return ResolvedTag{}, err
	}
	resp, err := c.do(req, false)
	if err != nil {
		return ResolvedTag{}, err
	}
	var reference gitReference
	if err := decodeJSONBody(resp, &reference); err != nil {
		return ResolvedTag{}, err
	}
	object := reference.Object
	result := ResolvedTag{AllSignaturesVerified: true}
	for depth := 0; depth < 8; depth++ {
		if !objectSHA.MatchString(object.SHA) {
			return ResolvedTag{}, fmt.Errorf("tag %s resolved to invalid Git object SHA %q", tag, object.SHA)
		}
		switch object.Type {
		case "commit":
			result.CommitSHA = object.SHA
			return result, nil
		case "tag":
			result.Annotated = true
			tagObject, err := c.tagObject(repo, object.SHA)
			if err != nil {
				return ResolvedTag{}, err
			}
			result.AllSignaturesVerified = result.AllSignaturesVerified && tagObject.Verification.Verified
			result.VerificationReasons = append(result.VerificationReasons, tagObject.Verification.Reason)
			object = tagObject.Object
		default:
			return ResolvedTag{}, fmt.Errorf("tag %s points to unsupported Git object type %q", tag, object.Type)
		}
	}
	return ResolvedTag{}, fmt.Errorf("tag %s exceeds annotated-tag resolution limit", tag)
}

func (c *Client) tagObject(repo, sha string) (gitTag, error) {
	url := fmt.Sprintf("%s/repos/%s/git/tags/%s", c.BaseURL, repo, sha)
	req, err := c.newRequest(http.MethodGet, url, "", nil)
	if err != nil {
		return gitTag{}, err
	}
	resp, err := c.do(req, false)
	if err != nil {
		return gitTag{}, err
	}
	var tag gitTag
	if err := decodeJSONBody(resp, &tag); err != nil {
		return gitTag{}, err
	}
	return tag, nil
}
