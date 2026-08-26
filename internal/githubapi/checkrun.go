// Package githubapi — this file mirrors the check-run HTTP calls of
// src/app-github.ts (createReleaseCheckRun / completeReleaseCheckRun) as a
// generic client: callers supply statuses, conclusions, titles, and summaries
// (including failure-summary truncation).
package githubapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Canonical check-run values from src/app-github.ts.
const (
	CheckRunName             = "Versionhoo Release"
	CheckRunStatusInProgress = "in_progress"
	CheckRunStatusCompleted  = "completed"
	checkRunStartedTitle     = "Versionhoo release started"
	checkRunStartedSummary   = "Versionhoo accepted this workflow run and started release processing."
)

// githubTimestampFormat renders timestamps like JS toISOString
// ("2006-01-02T15:04:05.000Z").
const githubTimestampFormat = "2006-01-02T15:04:05.000Z07:00"

// CheckRunInput carries the create-check-run body; zero-value fields fall
// back to the src/app-github.ts defaults so callers may pass just HeadSHA.
type CheckRunInput struct {
	Name          string
	HeadSHA       string
	Status        string
	StartedAt     time.Time
	OutputTitle   string
	OutputSummary string
}

// CheckRunResult carries the complete-check-run PATCH body; Conclusion is
// passed through verbatim ("success", "failure", or "neutral"), and the
// output title/summary come from the caller (truncation included).
type CheckRunResult struct {
	Status        string
	Conclusion    string
	CompletedAt   time.Time
	OutputTitle   string
	OutputSummary string
}

type checkRunOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// CreateCheckRun starts a check run for repo's head SHA and returns its id.
func (c *Client) CreateCheckRun(repo string, in CheckRunInput) (int64, error) {
	name := in.Name
	if name == "" {
		name = CheckRunName
	}
	status := in.Status
	if status == "" {
		status = CheckRunStatusInProgress
	}
	title := in.OutputTitle
	if title == "" {
		title = checkRunStartedTitle
	}
	summary := in.OutputSummary
	if summary == "" {
		summary = checkRunStartedSummary
	}
	startedAt := in.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}

	payload, err := json.Marshal(struct {
		Name      string         `json:"name"`
		HeadSHA   string         `json:"head_sha"`
		Status    string         `json:"status"`
		StartedAt string         `json:"started_at"`
		Output    checkRunOutput `json:"output"`
	}{name, in.HeadSHA, status, startedAt.UTC().Format(githubTimestampFormat), checkRunOutput{title, summary}})
	if err != nil {
		return 0, err
	}
	url := fmt.Sprintf("%s/repos/%s/check-runs", c.BaseURL, repo)
	req, err := c.newRequest(http.MethodPost, url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	resp, err := c.do(req, false)
	if err != nil {
		return 0, err
	}
	var decoded struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSONBody(resp, &decoded); err != nil {
		return 0, err
	}
	return decoded.ID, nil
}

// CompleteCheckRun finishes a check run with the caller-supplied conclusion
// and output fields.
func (c *Client) CompleteCheckRun(repo string, id int64, in CheckRunResult) error {
	status := in.Status
	if status == "" {
		status = CheckRunStatusCompleted
	}
	completedAt := in.CompletedAt
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}

	payload, err := json.Marshal(struct {
		Status      string         `json:"status"`
		Conclusion  string         `json:"conclusion"`
		CompletedAt string         `json:"completed_at"`
		Output      checkRunOutput `json:"output"`
	}{status, in.Conclusion, completedAt.UTC().Format(githubTimestampFormat), checkRunOutput{in.OutputTitle, in.OutputSummary}})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/repos/%s/check-runs/%d", c.BaseURL, repo, id)
	req, err := c.newRequest(http.MethodPatch, url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	resp, err := c.do(req, false)
	if err != nil {
		return err
	}
	drainAndClose(resp)
	return nil
}
