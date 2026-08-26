// This file mirrors the workflow_run payload validation, event filtering, and
// dedupe/queue key helpers of src/app-server.ts.
package app

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
)

// WebhookPayload mirrors WorkflowRunPayload (validated shape).
type WebhookPayload struct {
	Action       string
	Repository   WebhookRepository
	Installation *WebhookInstallation
	WorkflowRun  WebhookWorkflowRun
}

type WebhookRepository struct {
	ID            int64
	FullName      string
	CloneURL      string
	DefaultBranch string
}

type WebhookInstallation struct {
	ID int64
}

type WebhookWorkflowRun struct {
	Name           string
	Event          string
	Conclusion     *string // nil = JSON null
	HeadBranch     *string // nil = JSON null
	HeadSHA        string
	ID             int64
	HasID          bool
	HeadCommitMsg  string // head_commit?.message ?? ""
	HeadRepository string // head_repository?.full_name ?? "" ("" when absent)
}

// WebhookDecision mirrors VersionhooWebhookResult.
type WebhookDecision struct {
	Status string // "accepted" | "ignored"
	Reason string
}

var (
	releaseCommitPrefixRE = regexp.MustCompile(`(?m)^chore\(release\):`)
	skipCIRE              = regexp.MustCompile(`(?i)\[skip ci\]`)
	repoFullNameLooseRE   = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)
)

func isReleaseCommit(message string) bool {
	return releaseCommitPrefixRE.MatchString(message) || skipCIRE.MatchString(message)
}

// isPositiveInteger mirrors isPositiveInteger over decoded JSON numbers.
func isPositiveInteger(v any) bool {
	f, ok := toFloat(v)
	if !ok {
		return false
	}
	if f != math.Trunc(f) || f <= 0 || f > float64(math.MaxInt64) {
		return false
	}
	return true
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok && m != nil
}

// toFloat widens the JSON number space (float64) and Go literals (int,
// int64) so both decoded payloads and hand-built maps validate identically.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func numToInt64(v any) (int64, bool) {
	f, ok := toFloat(v)
	if !ok || f != math.Trunc(f) || f > float64(math.MaxInt64) {
		return 0, false
	}
	return int64(f), true
}

// DecodeWorkflowRunPayload mirrors validateWorkflowRunPayload + the typed
// assertion: it returns the decoded payload or one of the verbatim validation
// error messages.
func DecodeWorkflowRunPayload(value any, trustedCloneHosts []string) (*WebhookPayload, string) {
	if value == nil {
		return nil, "invalid webhook payload: expected an object"
	}
	if _, isArray := value.([]any); isArray {
		// JS typeof [] === "object", so arrays fall through to field checks.
		return nil, "invalid webhook payload: missing workflow_run action"
	}
	root, ok := asObject(value)
	if !ok {
		return nil, "invalid webhook payload: expected an object"
	}

	action, ok := asString(root["action"])
	if !ok {
		return nil, "invalid webhook payload: missing workflow_run action"
	}

	repoValue, present := root["repository"]
	repoMap, repoIsObject := asObject(repoValue)
	if !present || repoValue == nil || !repoIsObject {
		// Arrays pass JS typeof but then fail every metadata check.
		if _, isArray := repoValue.([]any); isArray {
			return nil, "invalid webhook payload: malformed repository metadata"
		}
		return nil, "invalid webhook payload: missing repository"
	}
	fullName, _ := asString(repoMap["full_name"])
	defaultBranch, _ := asString(repoMap["default_branch"])
	cloneURLStr, cloneIsString := asString(repoMap["clone_url"])
	if !isPositiveInteger(repoMap["id"]) ||
		fullName == "" ||
		!repoFullNameLooseRE.MatchString(fullName) ||
		!cloneIsString ||
		defaultBranch == "" {
		return nil, "invalid webhook payload: malformed repository metadata"
	}
	if err := validateCloneMetadata(cloneURLStr, fullName, trustedCloneHosts); err != "" {
		return nil, err
	}
	var installation *WebhookInstallation
	instValue, instPresent := root["installation"]
	if !instPresent || instValue == nil {
		return nil, "invalid webhook payload: missing installation"
	}
	if _, isArray := instValue.([]any); isArray {
		return nil, "invalid webhook payload: malformed installation"
	}
	instMap, ok := asObject(instValue)
	if !ok {
		return nil, "invalid webhook payload: missing installation"
	}
	if !isPositiveInteger(instMap["id"]) {
		return nil, "invalid webhook payload: malformed installation"
	}
	installID, _ := numToInt64(instMap["id"])
	installation = &WebhookInstallation{ID: installID}

	wrValue, wrPresent := root["workflow_run"]
	if !wrPresent || wrValue == nil {
		return nil, "invalid webhook payload: missing workflow_run"
	}
	if _, isArray := wrValue.([]any); isArray {
		return nil, "invalid webhook payload: malformed workflow_run metadata"
	}
	wrMap, ok := asObject(wrValue)
	if !ok {
		return nil, "invalid webhook payload: missing workflow_run"
	}
	name, nameOK := asString(wrMap["name"])
	event, eventOK := asString(wrMap["event"])
	conclusion, conclusionOK := optionalStringField(wrMap, "conclusion")
	headBranch, branchOK := optionalStringField(wrMap, "head_branch")
	headSHA, shaOK := asString(wrMap["head_sha"])
	if !nameOK || !eventOK || !conclusionOK || !branchOK || !shaOK || headSHA == "" {
		return nil, "invalid webhook payload: malformed workflow_run metadata"
	}

	payload := &WebhookPayload{
		Action: action,
		Repository: WebhookRepository{
			ID:            mustRepoID(repoMap["id"]),
			FullName:      fullName,
			CloneURL:      cloneURLStr,
			DefaultBranch: defaultBranch,
		},
		Installation: installation,
		WorkflowRun: WebhookWorkflowRun{
			Name:       name,
			Event:      event,
			Conclusion: conclusion,
			HeadBranch: headBranch,
			HeadSHA:    headSHA,
		},
	}
	if id, ok := numToInt64(wrMap["id"]); ok && id >= 0 {
		payload.WorkflowRun.ID = id
		payload.WorkflowRun.HasID = true
	}

	if hcRaw, ok := wrMap["head_commit"]; ok {
		if hcMap, ok := hcRaw.(map[string]any); ok {
			msg, _ := asString(hcMap["message"])
			payload.WorkflowRun.HeadCommitMsg = msg
		}
	}
	if hrRaw, ok := wrMap["head_repository"]; ok && hrRaw != nil {
		hrMap, isObj := asObject(hrRaw)
		if !isObj {
			return nil, "invalid webhook payload: malformed workflow_run repository metadata"
		}
		hrName, isStr := asString(hrMap["full_name"])
		if !isObj || !isStr {
			return nil, "invalid webhook payload: malformed workflow_run repository metadata"
		}
		payload.WorkflowRun.HeadRepository = hrName
	}
	return payload, ""
}

// optionalStringField mirrors `typeof x !== "string" && x !== null`: absent
// keys fail, explicit null passes.
func optionalStringField(m map[string]any, key string) (*string, bool) {
	v, present := m[key]
	if !present {
		return nil, false
	}
	if v == nil {
		return nil, true
	}
	s, ok := v.(string)
	return &s, ok
}

func scalarButNotMissing(v any, present bool) bool { return !present || v != nil }

// validateCloneMetadata applies the webhook-level clone URL rules of
// validateWorkflowRunPayload and returns a verbatim error message or "".
func validateCloneMetadata(cloneURL, fullName string, trustedCloneHosts []string) string {
	u, ok := parseWebURL(cloneURL)
	if !ok {
		return "invalid webhook payload: malformed clone metadata"
	}
	host := strings.ToLower(u.Hostname())
	allowed := map[string]bool{"github.com": true}
	for _, host := range trustedCloneHosts {
		allowed[strings.ToLower(host)] = true
	}
	if u.Scheme != "https" ||
		!allowed[host] ||
		u.Port() != "" ||
		u.User != nil ||
		u.RawQuery != "" ||
		u.Fragment != "" ||
		u.EscapedPath() != "/"+fullName+".git" {
		return "invalid webhook payload: malformed clone metadata"
	}
	return ""
}

// ShouldHandleWorkflowRun mirrors shouldHandleWorkflowRun with verbatim
// ignore reasons in the exact evaluation order.
func ShouldHandleWorkflowRun(payload *WebhookPayload, cfg *AppConfig) WebhookDecision {
	ignored := func(reason string) WebhookDecision { return WebhookDecision{Status: "ignored", Reason: reason} }
	if payload.Action != "completed" {
		return ignored(fmt.Sprintf("workflow_run action is %s", payload.Action))
	}
	if payload.WorkflowRun.Conclusion == nil || *payload.WorkflowRun.Conclusion != "success" {
		value := "missing"
		if payload.WorkflowRun.Conclusion != nil {
			value = *payload.WorkflowRun.Conclusion
		}
		return ignored(fmt.Sprintf("workflow_run conclusion is %s", value))
	}
	if payload.WorkflowRun.Event != "push" {
		return ignored(fmt.Sprintf("workflow_run event is %s", payload.WorkflowRun.Event))
	}
	if !contains(cfg.CIWorkflowNames, payload.WorkflowRun.Name) {
		return ignored(fmt.Sprintf("workflow %s is not configured for releases", payload.WorkflowRun.Name))
	}
	branch := ""
	if payload.WorkflowRun.HeadBranch != nil {
		branch = *payload.WorkflowRun.HeadBranch
	}
	if branch == "" || !contains(cfg.ReleaseBranches, branch) {
		return ignored(fmt.Sprintf("branch %s is not a release branch", orDefault(branch, "missing")))
	}
	if payload.WorkflowRun.HeadRepository != "" &&
		payload.WorkflowRun.HeadRepository != payload.Repository.FullName {
		return ignored("workflow_run came from a fork")
	}
	if len(cfg.AllowedRepositories) > 0 && !contains(cfg.AllowedRepositories, payload.Repository.FullName) {
		return ignored(fmt.Sprintf("repository %s is not allowed", payload.Repository.FullName))
	}
	if isReleaseCommit(payload.WorkflowRun.HeadCommitMsg) {
		return ignored("release commit")
	}
	if payload.Installation == nil {
		return ignored("missing installation id")
	}
	if payload.WorkflowRun.HeadSHA == "" {
		return ignored("missing workflow head sha")
	}
	return WebhookDecision{Status: "accepted"}
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// WorkflowRunKey mirrors workflowRunKey:
// "<fullName>:<id ?? head_sha>:<name>:<head_branch ?? ”>".
func WorkflowRunKey(payload *WebhookPayload) string {
	idOrSha := payload.WorkflowRun.HeadSHA
	if payload.WorkflowRun.HasID {
		idOrSha = fmt.Sprintf("%d", payload.WorkflowRun.ID)
	}
	branch := ""
	if payload.WorkflowRun.HeadBranch != nil {
		branch = *payload.WorkflowRun.HeadBranch
	}
	return strings.Join([]string{payload.Repository.FullName, idOrSha, payload.WorkflowRun.Name, branch}, ":")
}

// releaseQueueKey mirrors releaseQueueKey: "<fullName>:<head_branch ?? ”>".
func releaseQueueKey(payload *WebhookPayload) string {
	branch := ""
	if payload.WorkflowRun.HeadBranch != nil {
		branch = *payload.WorkflowRun.HeadBranch
	}
	return payload.Repository.FullName + ":" + branch
}

// parseWebURL mirrors `new URL(value)`: it rejects relative references,
// whitespace, and control characters that net/url tolerates but WHATWG URL
// parsing does not.
func parseWebURL(value string) (*url.URL, bool) {
	if value == "" {
		return nil, false
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7F {
			return nil, false
		}
	}
	u, err := url.Parse(value)
	if err != nil || !u.IsAbs() || u.Opaque != "" {
		return nil, false
	}
	return u, true
}

func mustRepoID(v any) int64 {
	id, _ := numToInt64(v)
	return id
}
