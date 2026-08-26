package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadAppConfigFromEnv(t *testing.T) {
	t.Run("required vars", func(t *testing.T) {
		if _, err := LoadAppConfigFromEnv(getenvFrom(map[string]string{})); err == nil ||
			err.Error() != "VERSIONHOO_APP_ID or HOOVERSION_APP_ID is required." {
			t.Fatalf("got %v", err)
		}
		env := map[string]string{"VERSIONHOO_APP_ID": "1"}
		if _, err := LoadAppConfigFromEnv(getenvFrom(env)); err == nil ||
			err.Error() != "VERSIONHOO_WEBHOOK_SECRET or HOOVERSION_WEBHOOK_SECRET is required." {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("port validation", func(t *testing.T) {
		base := map[string]string{
			"VERSIONHOO_APP_ID": "1", "VERSIONHOO_WEBHOOK_SECRET": "s",
			"VERSIONHOO_PRIVATE_KEY": "k",
		}
		for _, bad := range []string{"0", "-3", "abc"} {
			env := cloneMap(base)
			env["VERSIONHOO_PORT"] = bad
			_, err := LoadAppConfigFromEnv(getenvFrom(env))
			if err == nil || err.Error() != "VERSIONHOO_PORT must be a positive integer." {
				t.Fatalf("port %q: got %v", bad, err)
			}
		}
		env := cloneMap(base)
		env["HOOVERSION_PORT"] = "8081"
		cfg, err := LoadAppConfigFromEnv(getenvFrom(env))
		if err != nil || cfg.Port != 8081 {
			t.Fatalf("alias port: %+v %v", cfg, err)
		}
	})

	t.Run("first-listed alias wins", func(t *testing.T) {
		env := map[string]string{
			"VERSIONHOO_APP_ID": "primary", "HOOVERSION_APP_ID": "secondary",
			"VERSIONHOO_WEBHOOK_SECRET": "s", "VERSIONHOO_PRIVATE_KEY": "k",
			"VERSIONHOO_TRUSTED_GITHUB_API_URLS": " https://a.example.com , ,https://b.example.com ",
			"HOOVERSION_TRUSTED_API_URLS":        "https://ignored.example.com",
			"VERSIONHOO_KEEP_WORKDIR":            "yes",
		}
		cfg, err := LoadAppConfigFromEnv(getenvFrom(env))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AppID != "primary" {
			t.Fatalf("app id %q", cfg.AppID)
		}
		if len(cfg.TrustedAPIURLs) != 2 || cfg.TrustedAPIURLs[0] != "https://a.example.com" {
			t.Fatalf("trusted urls %v", cfg.TrustedAPIURLs)
		}
		if !cfg.KeepWorkDir {
			t.Fatal("keep workdir")
		}
	})

	t.Run("defaults", func(t *testing.T) {
		env := map[string]string{
			"VERSIONHOO_APP_ID": "1", "VERSIONHOO_WEBHOOK_SECRET": "s", "VERSIONHOO_PRIVATE_KEY": "k",
		}
		cfg, err := LoadAppConfigFromEnv(getenvFrom(env))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Host != "0.0.0.0" || cfg.Port != 3000 || cfg.ApiURL != "https://api.github.com" ||
			cfg.WebhookMaxBodyBytes != DefaultWebhookMaxBodyBytes {
			t.Fatalf("defaults wrong: %+v", cfg)
		}
		if len(cfg.ReleaseBranches) != 1 || cfg.ReleaseBranches[0] != "main" {
			t.Fatalf("branches %v", cfg.ReleaseBranches)
		}
		if len(cfg.CIWorkflowNames) != 1 || cfg.CIWorkflowNames[0] != "CI" {
			t.Fatalf("workflows %v", cfg.CIWorkflowNames)
		}
	})

	t.Run("max body bytes validation", func(t *testing.T) {
		env := map[string]string{
			"VERSIONHOO_APP_ID": "1", "VERSIONHOO_WEBHOOK_SECRET": "s",
			"VERSIONHOO_PRIVATE_KEY":            "k",
			"HOOVERSION_WEBHOOK_MAX_BODY_BYTES": "nope",
		}
		_, err := LoadAppConfigFromEnv(getenvFrom(env))
		if err == nil || err.Error() != "VERSIONHOO_WEBHOOK_MAX_BODY_BYTES must be a positive integer." {
			t.Fatalf("got %v", err)
		}
	})
}

func cloneMap(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func TestRoutesHealthAnd404(t *testing.T) {
	handler, queue, _ := newTestHandler(t, func(JobSpec) Outcome { t.Fatal("runner must not run"); return Outcome{} })
	defer queue.Wait()

	root := rootRoutes(handler)

	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "{\n  \"ok\": true\n}" {
		t.Fatalf("health: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodDelete, "/health", nil),
		httptest.NewRequest(http.MethodGet, "/webhooks/github", nil),
		httptest.NewRequest(http.MethodPost, "/other", nil),
		httptest.NewRequest(http.MethodGet, "/health/extra", nil),
	} {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || rec.Body.String() != "{\n  \"error\": \"not found\"\n}" {
			t.Fatalf("%s %s: %d %q", req.Method, req.URL.Path, rec.Code, rec.Body.String())
		}
	}

	// Query strings are ignored for routing (URL.pathname semantics).
	rec = httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health?probe=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health with query: %d", rec.Code)
	}
}

func payloadValidationCases() []struct {
	name    string
	body    string
	message string
} {
	mutated := func(mutate func(p map[string]any)) string {
		p := webhookPayloadMap()
		mutate(p)
		return marshalJSONRaw(p)
	}
	return []struct {
		name    string
		body    string
		message string
	}{
		{"scalar body", "42", "invalid webhook payload: expected an object"},
		{"array body", "[]", "invalid webhook payload: missing workflow_run action"},
		{"object no action", "{}", "invalid webhook payload: missing workflow_run action"},
		{"no repository", mutated(func(p map[string]any) { delete(p, "repository") }),
			"invalid webhook payload: missing repository"},
		{"repo id zero", mutated(func(p map[string]any) { p["repository"].(map[string]any)["id"] = 0 }),
			"invalid webhook payload: malformed repository metadata"},
		{"repo id float", mutated(func(p map[string]any) { p["repository"].(map[string]any)["id"] = 1.5 }),
			"invalid webhook payload: malformed repository metadata"},
		{"repo full name one part", mutated(func(p map[string]any) { p["repository"].(map[string]any)["full_name"] = "justone" }),
			"invalid webhook payload: malformed repository metadata"},
		{"clone host untrusted", mutated(func(p map[string]any) {
			p["repository"].(map[string]any)["clone_url"] = "https://evil.com/octo/hello.git"
		}),
			"invalid webhook payload: malformed clone metadata"},
		{"clone path mismatch", mutated(func(p map[string]any) {
			p["repository"].(map[string]any)["clone_url"] = "https://github.com/other/repo.git"
		}),
			"invalid webhook payload: malformed clone metadata"},
		{"clone url with port", mutated(func(p map[string]any) {
			p["repository"].(map[string]any)["clone_url"] = "https://github.com:443/octo/hello.git"
		}),
			"invalid webhook payload: malformed clone metadata"},
		{"empty default branch", mutated(func(p map[string]any) { p["repository"].(map[string]any)["default_branch"] = "" }),
			"invalid webhook payload: malformed repository metadata"},
		{"no installation", mutated(func(p map[string]any) { delete(p, "installation") }),
			"invalid webhook payload: missing installation"},
		{"installation id negative", mutated(func(p map[string]any) { p["installation"].(map[string]any)["id"] = -1 }),
			"invalid webhook payload: malformed installation"},
		{"installation null", mutated(func(p map[string]any) { p["installation"] = nil }),
			"invalid webhook payload: missing installation"},
		{"no workflow_run", mutated(func(p map[string]any) { delete(p, "workflow_run") }),
			"invalid webhook payload: missing workflow_run"},
		{"empty head sha", mutated(func(p map[string]any) { p["workflow_run"].(map[string]any)["head_sha"] = "" }),
			"invalid webhook payload: malformed workflow_run metadata"},
		{"absent conclusion", mutated(func(p map[string]any) { delete(p["workflow_run"].(map[string]any), "conclusion") }),
			"invalid webhook payload: malformed workflow_run metadata"},
		{"head repo missing name", mutated(func(p map[string]any) { p["workflow_run"].(map[string]any)["head_repository"] = map[string]any{} }),
			"invalid webhook payload: malformed workflow_run repository metadata"},
	}
}

func TestWebhookHandlerMatrix(t *testing.T) {
	stubGitHubFlow(t)
	var ran []JobSpec
	handler, queue, deduper := newTestHandler(t, func(spec JobSpec) Outcome {
		ran = append(ran, spec)
		return Outcome{RepositoryFullName: spec.RepositoryFullName, Branch: spec.Branch,
			HeadSha: spec.HeadSha, Outcome: "published", Published: true,
			Releases: []ReleaseRef{{Name: "mypkg", Version: "0.1.0", Tag: "v0.1.0"}}}
	})

	payload := marshalJSON(t, webhookPayloadMap())
	tooLarge := "{" + strings.Repeat("a", DefaultWebhookMaxBodyBytes+10) + "}"

	cases := []struct {
		name           string
		event          string
		delivery       string
		body           string
		signature      string // "-" sends none; "" = correct signature over body
		wantStatus     int
		wantBodySubstr []string
	}{
		{"bad signature", "ping", "", payload, "sha256=" + strings.Repeat("0", 64),
			http.StatusUnauthorized, []string{`"error": "invalid webhook signature"`}},
		{"missing signature", "ping", "", payload, "-",
			http.StatusUnauthorized, []string{"invalid webhook signature"}},
		{"oversize content-length", "workflow_run", "d1", tooLarge, "",
			http.StatusRequestEntityTooLarge, []string{`"error": "webhook payload too large"`}},
		{"invalid json", "workflow_run", "d2", "{nope", "",
			http.StatusBadRequest, []string{`"error": "invalid JSON webhook body"`}},
		{"ping echoes delivery", "ping", "del-9", "{}", "",
			http.StatusOK, []string{`"ok": true`, `"delivery": "del-9"`}},
		{"unsupported event", "issues", "del-1", "{}", "",
			http.StatusAccepted, []string{`"status": "ignored"`, `"reason": "unsupported event: issues"`}},
		{"unknown event header", "", "del-2", "{}", "",
			http.StatusAccepted, []string{`"reason": "unsupported event: unknown"`}},
		{"accepted workflow_run", "workflow_run", "del-3", payload, "",
			http.StatusAccepted, []string{`"ok": true`, `"status": "accepted"`, `"delivery": "del-3"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sig := tc.signature
			if sig == "" {
				sig = signBody(t, testWebhookSecret, tc.body)
			} else if sig == "-" {
				sig = ""
			}
			rec := postWebhook(handler, tc.event, tc.delivery, tc.body, sig)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d; body %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			for _, want := range tc.wantBodySubstr {
				mustContain(t, rec.Body.String(), want)
			}
		})
	}
	queue.Wait()

	t.Run("payload validation messages", func(t *testing.T) {
		for _, tc := range payloadValidationCases() {
			t.Run(tc.name, func(t *testing.T) {
				sig := signBody(t, testWebhookSecret, tc.body)
				rec := postWebhook(handler, "workflow_run", "", tc.body, sig)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
				}
				mustContain(t, rec.Body.String(), tc.message)
			})
		}
	})

	t.Run("runner received job context", func(t *testing.T) {
		if len(ran) != 1 {
			t.Fatalf("runner calls %d", len(ran))
		}
		spec := ran[0]
		if spec.RepositoryFullName != "octo/hello" || spec.Branch != "main" ||
			spec.HeadSha != "abc123def456" || spec.CloneURL != "https://github.com/octo/hello.git" ||
			spec.Token == "" {
			t.Fatalf("spec %+v", spec)
		}
	})

	payloadFor := func(repo string) string {
		p := webhookPayloadMap()
		r := p["repository"].(map[string]any)
		r["full_name"] = repo
		r["clone_url"] = "https://github.com/" + repo + ".git"
		p["workflow_run"].(map[string]any)["head_repository"].(map[string]any)["full_name"] = repo
		return marshalJSON(t, p)
	}

	t.Run("duplicate delivery ignored", func(t *testing.T) {
		body := payloadFor("octo/dup-del")
		sig := signBody(t, testWebhookSecret, body)
		rec := postWebhook(handler, "workflow_run", "dup-delivery", body, sig)
		mustContain(t, rec.Body.String(), `"status": "accepted"`)
		queue.Wait()

		rec = postWebhook(handler, "workflow_run", "dup-delivery", body, sig)
		mustContain(t, rec.Body.String(), `"reason": "duplicate delivery"`)
		queue.Wait()
	})

	t.Run("duplicate workflow run releases delivery reservation", func(t *testing.T) {
		first := payloadFor("octo/dup-run")
		rec := postWebhook(handler, "workflow_run", "fresh-delivery-a", first, signBody(t, testWebhookSecret, first))
		mustContain(t, rec.Body.String(), `"status": "accepted"`)
		queue.Wait()

		secondPayload := webhookPayloadMap()
		r := secondPayload["repository"].(map[string]any)
		r["full_name"] = "octo/dup-run"
		r["clone_url"] = "https://github.com/octo/dup-run.git"
		secondPayload["installation"].(map[string]any)["id"] = 8 // same run key → duplicate run, new delivery
		second := marshalJSON(t, secondPayload)
		rec = postWebhook(handler, "workflow_run", "fresh-delivery-b", second, signBody(t, testWebhookSecret, second))
		mustContain(t, rec.Body.String(), `"reason": "duplicate workflow run"`)
		queue.Wait()

		if state, ok := deduper.State("delivery:fresh-delivery-b"); ok {
			t.Fatalf("delivery key not released after duplicate-run rejection: %v", state)
		}
	})
	t.Run("failure releases reservations for retry", func(t *testing.T) {
		failing, queue2, deduper2 := newTestHandler(t, func(spec JobSpec) Outcome {
			return Outcome{Err: errors.New("boom")}
		})
		body := marshalJSON(t, webhookPayloadMap())
		postWebhook(failing, "workflow_run", "retry-delivery", body, signBody(t, testWebhookSecret, body))
		queue2.Wait()
		if _, ok := deduper2.State("delivery:retry-delivery"); ok {
			t.Fatal("delivery key survived final failure")
		}
		runKey := "workflow_run:" + workflowRunKeyForTest()
		if _, ok := deduper2.State(runKey); ok {
			t.Fatal("workflow key survived final failure")
		}
	})
}

// workflowRunKeyForTest decodes the canonical test payload and renders its
// workflow-run dedupe key.
func workflowRunKeyForTest() string {
	payload, errMsg := DecodeWorkflowRunPayload(webhookPayloadMap(), nil)
	if errMsg != "" {
		panic(errMsg)
	}
	return WorkflowRunKey(payload)
}
