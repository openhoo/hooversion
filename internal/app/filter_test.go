package app

import (
	"testing"
)

func strPtr(s string) *string { return &s }

func basePayload() *WebhookPayload {
	return &WebhookPayload{
		Action: "completed",
		Repository: WebhookRepository{
			ID: 42, FullName: "octo/hello",
			CloneURL: "https://github.com/octo/hello.git", DefaultBranch: "main",
		},
		Installation: &WebhookInstallation{ID: 7},
		WorkflowRun: WebhookWorkflowRun{
			Name: "CI", Event: "push", Conclusion: strPtr("success"),
			HeadBranch: strPtr("main"), HeadSHA: "abc123",
			HeadCommitMsg: "feat: add thing", HeadRepository: "octo/hello",
			ID: 1001, HasID: true,
		},
	}
}

func baseFilterConfig() *AppConfig {
	return &AppConfig{
		ReleaseBranches: []string{"main"},
		CIWorkflowNames: []string{"CI"},
	}
}

func TestShouldHandleWorkflowRun(t *testing.T) {
	cfg := baseFilterConfig()

	if got := ShouldHandleWorkflowRun(basePayload(), cfg); got.Status != "accepted" || got.Reason != "" {
		t.Fatalf("baseline should be accepted, got %+v", got)
	}

	cases := []struct {
		name   string
		mutate func(p *WebhookPayload)
		reason string
	}{
		{"action not completed", func(p *WebhookPayload) { p.Action = "requested" },
			"workflow_run action is requested"},
		{"conclusion failure", func(p *WebhookPayload) { c := "failure"; p.WorkflowRun.Conclusion = &c },
			"workflow_run conclusion is failure"},
		{"conclusion missing null", func(p *WebhookPayload) { p.WorkflowRun.Conclusion = nil },
			"workflow_run conclusion is missing"},
		{"event not push", func(p *WebhookPayload) { p.WorkflowRun.Event = "pull_request" },
			"workflow_run event is pull_request"},
		{"unconfigured workflow", func(p *WebhookPayload) { p.WorkflowRun.Name = "Build" },
			"workflow Build is not configured for releases"},
		{"branch not release branch", func(p *WebhookPayload) { b := "feature"; p.WorkflowRun.HeadBranch = &b },
			"branch feature is not a release branch"},
		{"missing branch", func(p *WebhookPayload) { p.WorkflowRun.HeadBranch = nil },
			"branch missing is not a release branch"},
		{"fork", func(p *WebhookPayload) { p.WorkflowRun.HeadRepository = "someone/hello" },
			"workflow_run came from a fork"},
		{"repo not allowed", func(p *WebhookPayload) {},
			"repository octo/hello is not allowed"},
		{"release commit prefix", func(p *WebhookPayload) { p.WorkflowRun.HeadCommitMsg = "x\nchore(release): 1.2.3" },
			"release commit"},
		{"skip ci case-insensitive", func(p *WebhookPayload) { p.WorkflowRun.HeadCommitMsg = "stuff [SKIP CI] end" },
			"release commit"},
		{"missing installation", func(p *WebhookPayload) { p.Installation = nil }, "missing installation id"},
		{"missing head sha", func(p *WebhookPayload) { p.WorkflowRun.HeadSHA = "" }, "missing workflow head sha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := basePayload()
			tc.mutate(p)
			c := baseFilterConfig()
			if tc.name == "repo not allowed" {
				c.AllowedRepositories = []string{"other/repo"}
			}
			got := ShouldHandleWorkflowRun(p, c)
			if got.Status != "ignored" || got.Reason != tc.reason {
				t.Fatalf("got %+v, want ignored %q", got, tc.reason)
			}
		})
	}

	t.Run("reason precedence action before conclusion", func(t *testing.T) {
		p := basePayload()
		p.Action = "requested"
		p.WorkflowRun.Conclusion = strPtr("failure")
		got := ShouldHandleWorkflowRun(p, cfg)
		if got.Reason != "workflow_run action is requested" {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestKeys(t *testing.T) {
	p := basePayload()
	if got := WorkflowRunKey(p); got != "octo/hello:1001:CI:main" {
		t.Fatalf("run key %q", got)
	}
	pNoID := basePayload()
	pNoID.WorkflowRun.HasID = false
	if got := WorkflowRunKey(pNoID); got != "octo/hello:abc123:CI:main" {
		t.Fatalf("sha key %q", got)
	}
	nullBranch := basePayload()
	nullBranch.WorkflowRun.HeadBranch = nil
	if got := releaseQueueKey(nullBranch); got != "octo/hello:" {
		t.Fatalf("queue key %q", got)
	}
	if got := releaseQueueKey(p); got != "octo/hello:main" {
		t.Fatalf("queue key %q", got)
	}
}

func TestDecodeWorkflowRunPayloadTyped(t *testing.T) {
	payload, errMsg := DecodeWorkflowRunPayload(webhookPayloadMap(), nil)
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	if payload.Action != "completed" || payload.Repository.ID != 42 ||
		payload.Installation == nil || payload.Installation.ID != 7 ||
		payload.WorkflowRun.Name != "CI" || payload.WorkflowRun.ID != 1001 || !payload.WorkflowRun.HasID ||
		payload.WorkflowRun.HeadCommitMsg != "feat: add thing" ||
		payload.WorkflowRun.HeadRepository != "octo/hello" ||
		payload.WorkflowRun.Conclusion == nil || *payload.WorkflowRun.Conclusion != "success" {
		t.Fatalf("decoded %+v", payload)
	}

	// Trusted clone hosts admit enterprise URLs at webhook validation level.
	mutated := webhookPayloadMap()
	mutated["repository"].(map[string]any)["clone_url"] = "https://ghe.corp.example.com/octo/hello.git"
	if _, errMsg := DecodeWorkflowRunPayload(mutated, []string{"ghe.corp.example.com"}); errMsg != "" {
		t.Fatalf("trusted host rejected: %s", errMsg)
	}
}
