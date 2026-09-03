// Package app mirrors src/app-server.ts, src/app-auth.ts,
// src/app-github.ts, and src/app-runner.ts as the versionhoo-app server:
// env configuration, webhook intake, event filtering, dedupe, per-branch
// release queueing, check runs, and isolated release execution.
package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	hverr "github.com/openhoo/hooversion/internal/errors"
)

// DefaultWebhookMaxBodyBytes mirrors DEFAULT_WEBHOOK_MAX_BODY_BYTES (1 MiB).
const DefaultWebhookMaxBodyBytes = 1024 * 1024

const (
	webhookReadHeaderTimeout = 10 * time.Second
	webhookReadTimeout       = 30 * time.Second
	webhookWriteTimeout      = 30 * time.Second
	webhookIdleTimeout       = 60 * time.Second
	webhookMaxHeaderBytes    = 64 * 1024
)

// AppConfig mirrors VersionhooAppConfig.
type AppConfig struct {
	AppID                string
	PrivateKey           string
	WebhookSecret        string
	ApiURL               string
	TrustedAPIURLs       []string
	TrustedCloneHosts    []string
	Host                 string
	Port                 int
	WorkDir              string
	ConfigPath           string
	InstallCommand       string
	AllowedRepositories  []string
	ReleaseBranches      []string
	CIWorkflowNames      []string
	GitAuthorName        string
	GitAuthorEmail       string
	KeepWorkDir          bool
	WebhookMaxBodyBytes  int
	WebhookSpoolDir      string
	WebhookSpoolMaxBytes int64
}

// firstEnv returns the first non-empty value among names, mirroring readEnv.
func firstEnv(getenv func(string) string, names ...string) string {
	for _, name := range names {
		if value := getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func requiredEnv(getenv func(string) string, names ...string) (string, error) {
	value := firstEnv(getenv, names...)
	if value == "" {
		return "", hverr.New("%s is required.", joinOr(names))
	}
	return value, nil
}

// joinOr mirrors names.join(" or ").
func joinOr(names []string) string {
	out := ""
	for i, name := range names {
		if i > 0 {
			out += " or "
		}
		out += name
	}
	return out
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	var out []string
	for _, item := range bytes.Split([]byte(value), []byte(",")) {
		trimmed := string(bytes.TrimSpace(item))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func readBoolean(value string) bool {
	return value == "1" || value == "true" || value == "yes"
}

func positiveIntEnv(getenv func(string) string, names [2]string, defaultValue int, errorMessage string) (int, error) {
	raw := firstEnv(getenv, names[0], names[1])
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, hverr.New("%s", errorMessage)
	}
	return value, nil
}

// LoadAppConfigFromEnv mirrors loadVersionhooAppConfigFromEnv; VERSIONHOO_*
// names win over HOOVERSION_* aliases (first-listed wins).
func LoadAppConfigFromEnv(getenv func(string) string) (*AppConfig, error) {
	appID, err := requiredEnv(getenv, "VERSIONHOO_APP_ID", "HOOVERSION_APP_ID")
	if err != nil {
		return nil, err
	}
	webhookSecret, err := requiredEnv(getenv, "VERSIONHOO_WEBHOOK_SECRET", "HOOVERSION_WEBHOOK_SECRET")
	if err != nil {
		return nil, err
	}
	port, err := positiveIntEnv(getenv, [2]string{"VERSIONHOO_PORT", "HOOVERSION_PORT"}, 3000,
		"VERSIONHOO_PORT must be a positive integer.")
	if err != nil {
		return nil, err
	}
	webhookMaxBodyBytes, err := positiveIntEnv(getenv,
		[2]string{"VERSIONHOO_WEBHOOK_MAX_BODY_BYTES", "HOOVERSION_WEBHOOK_MAX_BODY_BYTES"},
		DefaultWebhookMaxBodyBytes,
		"VERSIONHOO_WEBHOOK_MAX_BODY_BYTES must be a positive integer.")
	if err != nil {
		return nil, err
	}
	webhookSpoolMaxBytes, err := positiveIntEnv(getenv,
		[2]string{"VERSIONHOO_WEBHOOK_SPOOL_MAX_BYTES", "HOOVERSION_WEBHOOK_SPOOL_MAX_BYTES"},
		int(DefaultWebhookSpoolMaxBytes),
		"VERSIONHOO_WEBHOOK_SPOOL_MAX_BYTES must be a positive integer.")
	if err != nil {
		return nil, err
	}
	privateKey, err := ReadGitHubAppPrivateKey(getenv)
	if err != nil {
		return nil, err
	}

	return &AppConfig{
		AppID:                appID,
		PrivateKey:           privateKey,
		WebhookSecret:        webhookSecret,
		ApiURL:               orDefault(firstEnv(getenv, "VERSIONHOO_GITHUB_API_URL", "HOOVERSION_GITHUB_API_URL"), "https://api.github.com"),
		TrustedAPIURLs:       splitCSV(firstEnv(getenv, "VERSIONHOO_TRUSTED_GITHUB_API_URLS", "HOOVERSION_TRUSTED_GITHUB_API_URLS", "VERSIONHOO_TRUSTED_API_URLS", "HOOVERSION_TRUSTED_API_URLS")),
		TrustedCloneHosts:    splitCSV(firstEnv(getenv, "VERSIONHOO_TRUSTED_GITHUB_CLONE_HOSTS", "HOOVERSION_TRUSTED_GITHUB_CLONE_HOSTS", "VERSIONHOO_TRUSTED_CLONE_HOSTS", "HOOVERSION_TRUSTED_CLONE_HOSTS")),
		Host:                 orDefault(firstEnv(getenv, "VERSIONHOO_HOST", "HOOVERSION_HOST"), "0.0.0.0"),
		Port:                 port,
		ReleaseBranches:      splitCSV(orDefault(firstEnv(getenv, "VERSIONHOO_RELEASE_BRANCHES", "HOOVERSION_RELEASE_BRANCHES"), "main")),
		WorkDir:              firstEnv(getenv, "VERSIONHOO_WORKDIR", "HOOVERSION_WORKDIR"),
		ConfigPath:           firstEnv(getenv, "VERSIONHOO_CONFIG", "HOOVERSION_CONFIG"),
		WebhookSpoolDir:      firstEnv(getenv, "VERSIONHOO_WEBHOOK_SPOOL_DIR", "HOOVERSION_WEBHOOK_SPOOL_DIR"),
		InstallCommand:       firstEnv(getenv, "VERSIONHOO_INSTALL_COMMAND", "HOOVERSION_INSTALL_COMMAND"),
		AllowedRepositories:  splitCSV(firstEnv(getenv, "VERSIONHOO_ALLOWED_REPOS", "HOOVERSION_ALLOWED_REPOS")),
		CIWorkflowNames:      splitCSV(orDefault(firstEnv(getenv, "VERSIONHOO_CI_WORKFLOWS", "HOOVERSION_CI_WORKFLOWS"), "CI")),
		GitAuthorName:        firstEnv(getenv, "VERSIONHOO_GIT_AUTHOR_NAME", "HOOVERSION_GIT_AUTHOR_NAME"),
		GitAuthorEmail:       firstEnv(getenv, "VERSIONHOO_GIT_AUTHOR_EMAIL", "HOOVERSION_GIT_AUTHOR_EMAIL"),
		KeepWorkDir:          readBoolean(firstEnv(getenv, "VERSIONHOO_KEEP_WORKDIR", "HOOVERSION_KEEP_WORKDIR")),
		WebhookMaxBodyBytes:  webhookMaxBodyBytes,
		WebhookSpoolMaxBytes: int64(webhookSpoolMaxBytes),
	}, nil
}

// resolveWebhookMaxBodyBytes falls back to the default for non-positive values.
func resolveWebhookMaxBodyBytes(value int) int {
	if value > 0 {
		return value
	}
	return DefaultWebhookMaxBodyBytes
}

// newWebhookServer centralizes the public listener's bounded HTTP behavior.
func newWebhookServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: webhookReadHeaderTimeout,
		ReadTimeout:       webhookReadTimeout,
		WriteTimeout:      webhookWriteTimeout,
		IdleTimeout:       webhookIdleTimeout,
		MaxHeaderBytes:    webhookMaxHeaderBytes,
	}
}

func Run(getenv func(string) string) error {
	cfg, err := LoadAppConfigFromEnv(getenv)
	if err != nil {
		return err
	}
	spool, err := NewWebhookSpool(resolveWebhookSpoolDir(cfg), cfg.WebhookMaxBodyBytes, cfg.WebhookSpoolMaxBytes)
	if err != nil {
		return err
	}
	defer spool.Close()
	queue := NewReleaseTaskQueue(nil, QueueOptions{})
	deduper := NewWebhookDeduper(0, nil)
	handler := NewWebhookHandler(cfg, Runner, queue, deduper, spool)
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("versionhoo app listening on http://%s:%d", cfg.Host, cfg.Port)
	server := newWebhookServer(rootRoutes(handler))
	return server.Serve(listener)
}

func resolveWebhookSpoolDir(cfg *AppConfig) string {
	if cfg.WebhookSpoolDir != "" {
		return cfg.WebhookSpoolDir
	}
	if cfg.WorkDir != "" {
		return filepath.Join(cfg.WorkDir, "webhook-spool")
	}
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "versionhoo", "webhook-spool")
	}
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		return filepath.Join(homeDir, ".cache", "versionhoo", "webhook-spool")
	}
	// This fallback is only used on systems without a discoverable user
	// directory; NewWebhookSpool still enforces private ownership and mode.
	return filepath.Join(os.TempDir(), "versionhoo-webhook-spool")
}

// rootRoutes mirrors the Bun.serve fetch dispatch: exact method+path matches,
// everything else is a 404. Path matching ignores the query string like
// URL.pathname.
func rootRoutes(webhook http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			writeJSON(w, http.StatusOK, okBody{OK: true})
		case r.Method == http.MethodPost && r.URL.Path == "/webhooks/github":
			webhook(w, r)
		default:
			writeJSON(w, http.StatusNotFound, errorBody{Error: "not found"})
		}
	}
}

// --- Ordered response bodies (JSON.stringify insertion order) --------------

type okBody struct {
	OK bool `json:"ok"`
}

type okDeliveryBody struct {
	OK       bool   `json:"ok"`
	Delivery string `json:"delivery"`
}

type ignoredBody struct {
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type ignoredDeliveryBody struct {
	OK       bool   `json:"ok"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	Delivery string `json:"delivery"`
}

type acceptedBody struct {
	OK       bool   `json:"ok"`
	Status   string `json:"status"`
	Delivery string `json:"delivery"`
}

type errorBody struct {
	Error string `json:"error"`
}

// writeJSON renders pretty-printed JSON exactly like JSON.stringify(v, null, 2)
// (which never HTML-escapes), preserving struct field order.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil { // Encode appends '\n'
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
}

func pendingWebhookEntries(spool *WebhookSpool, trustedCloneHosts []string) []WebhookSpoolEntry {
	entries, err := spool.Pending()
	if err != nil {
		log.Printf("could not scan webhook spool: %v", err)
		return nil
	}
	valid := make([]WebhookSpoolEntry, 0, len(entries))
	for _, entry := range entries {
		var parsed any
		if err := json.Unmarshal(entry.Record.Payload, &parsed); err != nil {
			spool.Discard(entry.Path)
			continue
		}
		payload, validationError := DecodeWorkflowRunPayload(parsed, trustedCloneHosts)
		if validationError != "" || payload == nil {
			spool.Discard(entry.Path)
			continue
		}
		if WorkflowRunKey(payload) == strings.TrimPrefix(entry.Record.WorkflowKey, "workflow_run:") &&
			releaseQueueKey(payload) == entry.Record.QueueKey {
			valid = append(valid, entry)
			continue
		}
		spool.Discard(entry.Path)
	}
	return valid
}

func reserveSpoolEntry(entry WebhookSpoolEntry, deduper *WebhookDeduper) bool {
	if !deduper.Reserve(entry.Record.DeliveryKey) {
		return false
	}
	if !deduper.Reserve(entry.Record.WorkflowKey) {
		deduper.Release(entry.Record.DeliveryKey)
		return false
	}
	return true
}

// NewWebhookHandler mirrors createVersionhooWebhookHandler. The optional
// spool is supplied by Run; omitting it retains the in-memory test seam.
func NewWebhookHandler(
	cfg *AppConfig,
	runner func(JobSpec) Outcome,
	queue *ReleaseTaskQueue,
	deduper *WebhookDeduper,
	spools ...*WebhookSpool,
) http.HandlerFunc {
	var spool *WebhookSpool
	if len(spools) > 0 {
		spool = spools[0]
	}
	if spool != nil {
		for _, entry := range pendingWebhookEntries(spool, cfg.TrustedCloneHosts) {
			if !reserveSpoolEntry(entry, deduper) {
				spool.Discard(entry.Path)
			}
		}
		spool.Start(queue, func(entry WebhookSpoolEntry) (func() error, func(error)) {
			businessComplete := false
			acknowledge := func(status string) error {
				if status == webhookSpoolStatusCompleted {
					return spool.Ack(entry.Path)
				}
				return spool.AckTerminal(entry.Path, status)
			}
			var scheduleAckRetry func(string, func())
			scheduleAckRetry = func(status string, onSuccess func()) {
				go func() {
					timer := time.NewTimer(time.Second)
					defer timer.Stop()
					<-timer.C
					if spool.isClosed() {
						return
					}
					if queue.Enqueue(entry.Record.QueueKey, func() error {
						if err := acknowledge(status); err != nil {
							return err
						}
						onSuccess()
						return nil
					}, func(error) {
						scheduleAckRetry(status, onSuccess)
					}) {
						return
					}
					scheduleAckRetry(status, onSuccess)
				}()
			}
			task := func() error {
				if businessComplete {
					return acknowledge(webhookSpoolStatusCompleted)
				}
				var parsed any
				if err := json.Unmarshal(entry.Record.Payload, &parsed); err != nil {
					return fmt.Errorf("invalid persisted webhook payload: %w", err)
				}
				payload, validationError := DecodeWorkflowRunPayload(parsed, cfg.TrustedCloneHosts)
				if validationError != "" {
					return errors.New(validationError)
				}
				if err := ReleaseFromWorkflowRun(payload, cfg, runner); err != nil {
					return err
				}
				businessComplete = true
				if err := acknowledge(webhookSpoolStatusCompleted); err != nil {
					return fmt.Errorf("durably acknowledge completed webhook: %w", err)
				}
				deduper.Succeed(entry.Record.DeliveryKey)
				deduper.Succeed(entry.Record.WorkflowKey)
				return nil
			}
			onFinalFailure := func(error) {
				if businessComplete {
					if err := acknowledge(webhookSpoolStatusCompleted); err == nil {
						deduper.Succeed(entry.Record.DeliveryKey)
						deduper.Succeed(entry.Record.WorkflowKey)
						return
					}
					scheduleAckRetry(webhookSpoolStatusCompleted, func() {
						deduper.Succeed(entry.Record.DeliveryKey)
						deduper.Succeed(entry.Record.WorkflowKey)
					})
					return
				}
				if err := acknowledge(webhookSpoolStatusFailed); err == nil {
					deduper.Release(entry.Record.DeliveryKey)
					deduper.Release(entry.Record.WorkflowKey)
					return
				}
				scheduleAckRetry(webhookSpoolStatusFailed, func() {
					deduper.Release(entry.Record.DeliveryKey)
					deduper.Release(entry.Record.WorkflowKey)
				})
			}
			return task, onFinalFailure
		})
	}
	return func(w http.ResponseWriter, r *http.Request) {
		event := r.Header.Get("x-github-event")
		delivery := r.Header.Get("x-github-delivery")
		if delivery == "" {
			delivery = "unknown"
		}
		maxBodyBytes := resolveWebhookMaxBodyBytes(cfg.WebhookMaxBodyBytes)
		body, resp := readWebhookBody(r, int64(maxBodyBytes))
		if resp != nil {
			resp(w)
			return
		}

		if !VerifyWebhookSignature(cfg.WebhookSecret, string(body), r.Header.Get("x-hub-signature-256")) {
			writeJSON(w, http.StatusUnauthorized, errorBody{Error: "invalid webhook signature"})
			return
		}

		if event == "ping" {
			writeJSON(w, http.StatusOK, okDeliveryBody{OK: true, Delivery: delivery})
			return
		}

		if event != "workflow_run" {
			name := event
			if name == "" {
				name = "unknown"
			}
			writeJSON(w, http.StatusAccepted, ignoredBody{OK: true, Status: "ignored",
				Reason: fmt.Sprintf("unsupported event: %s", name)})
			return
		}

		var parsed any
		if err := json.Unmarshal(body, &parsed); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid JSON webhook body"})
			return
		}
		payload, validationError := DecodeWorkflowRunPayload(parsed, cfg.TrustedCloneHosts)
		if validationError != "" {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: validationError})
			return
		}
		decision := ShouldHandleWorkflowRun(payload, cfg)
		if decision.Status == "ignored" {
			writeJSON(w, http.StatusAccepted, ignoredDeliveryBody{OK: true, Status: "ignored",
				Reason: decision.Reason, Delivery: delivery})
			return
		}

		var deliveryKey string
		if delivery != "unknown" {
			deliveryKey = "delivery:" + delivery
		}
		workflowKey := "workflow_run:" + WorkflowRunKey(payload)
		if !deduper.Reserve(deliveryKey) {
			writeJSON(w, http.StatusAccepted, ignoredDeliveryBody{OK: true, Status: "ignored",
				Reason: "duplicate delivery", Delivery: delivery})
			return
		}
		if !deduper.Reserve(workflowKey) {
			deduper.Release(deliveryKey)
			writeJSON(w, http.StatusAccepted, ignoredDeliveryBody{OK: true, Status: "ignored",
				Reason: "duplicate workflow run", Delivery: delivery})
			return
		}

		queueKey := releaseQueueKey(payload)
		if spool != nil {
			_, err := spool.Admit(WebhookSpoolRecord{
				DeliveryKey: deliveryKey,
				WorkflowKey: workflowKey,
				QueueKey:    queueKey,
				Payload:     append([]byte(nil), body...),
			})
			if err != nil {
				deduper.Release(deliveryKey)
				deduper.Release(workflowKey)
				if errors.Is(err, ErrWebhookSpoolFull) {
					w.Header().Set("Retry-After", "1")
					writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "webhook spool is full; retry later"})
				} else {
					writeJSON(w, http.StatusInternalServerError, errorBody{Error: "could not durably admit webhook"})
				}
				return
			}
			spool.Wake()
		} else if !queue.Enqueue(queueKey, func() error {
			err := ReleaseFromWorkflowRun(payload, cfg, runner)
			if err != nil {
				return err
			}
			deduper.Succeed(deliveryKey)
			deduper.Succeed(workflowKey)
			return nil
		}, func(error) {
			deduper.Release(deliveryKey)
			deduper.Release(workflowKey)
		}) {
			// The optional in-memory-only test seam retains the old bounded
			// behavior. Production Run always supplies a durable spool.
			deduper.Release(deliveryKey)
			deduper.Release(workflowKey)
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "release queue is full; retry later"})
			return
		}

		writeJSON(w, http.StatusAccepted, acceptedBody{OK: true, Status: "accepted", Delivery: delivery})
	}
}

// readWebhookBody mirrors readWebhookBody: content-length pre-check followed by
// a streamed read capped at maxBytes; both overflows yield a 413 response.
func readWebhookBody(r *http.Request, maxBytes int64) ([]byte, func(http.ResponseWriter)) {
	if declared := r.Header.Get("content-length"); declared != "" {
		if length, err := strconv.ParseInt(declared, 10, 64); err == nil && length > maxBytes {
			return nil, tooLargeResponder
		}
	}
	limit := maxBytes
	if maxBytes < int64(^uint64(0)>>1) {
		limit++
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return nil, func(w http.ResponseWriter) {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid JSON webhook body"})
		}
	}
	if int64(len(body)) > maxBytes {
		return nil, tooLargeResponder
	}
	return body, nil
}

func tooLargeResponder(w http.ResponseWriter) {
	writeJSON(w, http.StatusRequestEntityTooLarge, errorBody{Error: "webhook payload too large"})
}

// orDefault returns fallback when value is empty.
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
