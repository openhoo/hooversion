package githubapi

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"
	"time"
)

const isoTimestampRE = `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`

func TestCreateCheckRunDefaultsAndHeaders(t *testing.T) {
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"id":17,"html_url":"https://github.com/owner/repo/checks/17"}`))
	}}
	srv := newRecordingServer(t, rec)

	id, err := New(srv.URL, "secret-token").CreateCheckRun("owner/repo", CheckRunInput{HeadSHA: "abc"})
	if err != nil {
		t.Fatalf("CreateCheckRun: %v", err)
	}
	if id != 17 {
		t.Errorf("id = %d, want 17", id)
	}
	req := rec.requests[0]
	if req.Method != http.MethodPost || req.Path != "/repos/owner/repo/check-runs" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if auth := req.Header.Get("Authorization"); auth != "Bearer secret-token" {
		t.Errorf("authorization = %q", auth)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("body json: %v", err)
	}
	if body["name"] != CheckRunName || body["head_sha"] != "abc" || body["status"] != "in_progress" {
		t.Errorf("name/head_sha/status = %v/%v/%v", body["name"], body["head_sha"], body["status"])
	}
	startedAt, _ := body["started_at"].(string)
	if !regexp.MustCompile(isoTimestampRE).MatchString(startedAt) {
		t.Errorf("started_at = %q, want JS toISOString style", startedAt)
	}
	output, ok := body["output"].(map[string]any)
	if !ok || output["title"] != "Versionhoo release started" ||
		output["summary"] != "Versionhoo accepted this workflow run and started release processing." {
		t.Errorf("output = %v", body["output"])
	}
}

func TestCompleteCheckRunBody(t *testing.T) {
	rec := &recorder{}
	srv := newRecordingServer(t, rec)

	err := New(srv.URL, "tok").CompleteCheckRun("owner/repo", 17, CheckRunResult{
		Conclusion:    "failure",
		OutputTitle:   "Versionhoo release failed",
		OutputSummary: "push rejected",
	})
	if err != nil {
		t.Fatalf("CompleteCheckRun: %v", err)
	}
	req := rec.requests[0]
	if req.Method != http.MethodPatch || req.Path != "/repos/owner/repo/check-runs/17" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("body json: %v", err)
	}
	if body["status"] != "completed" || body["conclusion"] != "failure" {
		t.Errorf("status/conclusion = %v/%v", body["status"], body["conclusion"])
	}
	completedAt, _ := body["completed_at"].(string)
	if !regexp.MustCompile(isoTimestampRE).MatchString(completedAt) {
		t.Errorf("completed_at = %q", completedAt)
	}
	output, ok := body["output"].(map[string]any)
	if !ok || output["title"] != "Versionhoo release failed" || output["summary"] != "push rejected" {
		t.Errorf("output = %v (caller-supplied fields must pass through verbatim)", body["output"])
	}
}

// TestCheckRunsAreGeneric pins that callers can override every field — the
// failure-summary truncation (60000) stays with the caller.
func TestCheckRunsAreGeneric(t *testing.T) {
	rec := &recorder{handler: func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 1 {
			_, _ = w.Write([]byte(`{"id":3}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":3}`))
	}}
	srv := newRecordingServer(t, rec)

	client := New(srv.URL, "tok")
	started := time.Unix(1700000000, 0).UTC()
	longSummary := make([]byte, 60003)
	for i := range longSummary {
		longSummary[i] = 'x'
	}
	if _, err := client.CreateCheckRun("o/r", CheckRunInput{
		Name:          "X",
		HeadSHA:       "h",
		Status:        "queued",
		StartedAt:     started,
		OutputTitle:   "T",
		OutputSummary: "S",
	}); err != nil {
		t.Fatalf("CreateCheckRun: %v", err)
	}
	var createBody struct {
		Name      string `json:"name"`
		Status    string `json:"status"`
		StartedAt string `json:"started_at"`
		Output    struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"output"`
	}
	if err := json.Unmarshal([]byte(rec.requests[0].Body), &createBody); err != nil {
		t.Fatal(err)
	}
	if createBody.Name != "X" || createBody.Status != "queued" || createBody.StartedAt != "2023-11-14T22:13:20.000Z" ||
		createBody.Output.Title != "T" || createBody.Output.Summary != "S" {
		t.Errorf("create body = %+v", createBody)
	}

	if err := client.CompleteCheckRun("o/r", 3, CheckRunResult{
		Conclusion:    "failure",
		OutputSummary: string(longSummary),
	}); err != nil {
		t.Fatalf("CompleteCheckRun: %v", err)
	}
	var completeBody struct {
		Output struct {
			Summary string `json:"summary"`
		} `json:"output"`
	}
	if err := json.Unmarshal([]byte(rec.requests[1].Body), &completeBody); err != nil {
		t.Fatal(err)
	}
	if len(completeBody.Output.Summary) != len(longSummary) {
		t.Errorf("summary length = %d, want %d (client must not truncate)", len(completeBody.Output.Summary), len(longSummary))
	}
}
