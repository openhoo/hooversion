package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func waitEvent(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("execution did not reach expected state")
	}
}

func TestQueueBoundsConcurrentRepositories(t *testing.T) {
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	q := NewReleaseTaskQueue(func(error) {}, QueueOptions{MaxConcurrent: 2})
	for i := 0; i < 6; i++ {
		if !q.Enqueue(fmt.Sprintf("repo/%d:main", i), func() error { started <- struct{}{}; <-release; return nil }, nil) {
			t.Fatal("admission rejected")
		}
	}
	waitEvent(t, started)
	waitEvent(t, started)
	select {
	case <-started:
		t.Fatal("worker budget exceeded")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	q.Wait()
	if len(started) != 4 {
		t.Fatalf("waiting jobs did not drain: %d", len(started))
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.repositories) != 0 {
		t.Fatal("repository gates leaked")
	}
}

func TestQueueSerializesRepositoryAcrossBranchesWithoutStarvingOtherRepos(t *testing.T) {
	first := make(chan struct{})
	other := make(chan struct{})
	second := make(chan struct{})
	release := make(chan struct{})
	q := NewReleaseTaskQueue(func(error) {}, QueueOptions{MaxConcurrent: 2})
	q.Enqueue("owner/repo:main", func() error { close(first); <-release; return nil }, nil)
	waitEvent(t, first)
	q.Enqueue("OWNER/REPO:release", func() error { close(second); return nil }, nil)
	q.Enqueue("owner/other:main", func() error { close(other); return nil }, nil)
	waitEvent(t, other)
	select {
	case <-second:
		t.Fatal("branches of one repository overlap")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	q.Wait()
	waitEvent(t, second)
}

func TestQueueCancellationInterruptsRetryDelayAndPendingWork(t *testing.T) {
	firstAttempt := make(chan struct{})
	q := NewReleaseTaskQueue(func(error) {}, QueueOptions{MaxConcurrent: 1, MaxAttempts: 3, RetryDelayMs: 30000})
	attempts := 0
	finalFailures := 0
	q.Enqueue("repo:main", func() error { attempts++; close(firstAttempt); return errors.New("retry") }, func(error) { finalFailures++ })
	waitEvent(t, firstAttempt)
	q.Cancel()
	done := make(chan struct{})
	go func() { q.Wait(); close(done) }()
	waitEvent(t, done)
	if attempts != 1 || finalFailures != 1 {
		t.Fatalf("attempts=%d failures=%d", attempts, finalFailures)
	}
	if q.Enqueue("later", func() error { t.Error("canceled queue executed work"); return nil }, nil) {
		t.Fatal("canceled queue admitted new task")
	}
}

func TestQueueCancellationWaitsForActiveTaskToReturn(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	pendingRan := false
	q := NewReleaseTaskQueue(func(error) {}, QueueOptions{MaxConcurrent: 1})
	q.Enqueue("first", func() error { close(started); <-release; return nil }, nil)
	waitEvent(t, started)
	q.Enqueue("pending", func() error { pendingRan = true; return nil }, nil)
	q.Cancel()
	done := make(chan struct{})
	go func() { q.Wait(); close(done) }()
	select {
	case <-done:
		t.Fatal("queue released active execution prematurely")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	waitEvent(t, done)
	if pendingRan {
		t.Fatal("canceled capacity waiter executed")
	}
}

func TestServerShutdownPreservesDurableJobForRestart(t *testing.T) {
	stubGitHubFlow(t)
	dir := privateSpoolTestDir(t)
	spool, err := NewWebhookSpool(dir, DefaultWebhookMaxBodyBytes)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := NewReleaseTaskQueue(func(error) {}, QueueOptions{Context: ctx, MaxConcurrent: 1})
	cfg := &AppConfig{AppID: "123", WebhookSecret: testWebhookSecret, ApiURL: "https://api.github.com", ReleaseBranches: []string{"main"}, CIWorkflowNames: []string{"CI"}, WebhookMaxBodyBytes: DefaultWebhookMaxBodyBytes}
	started := make(chan struct{})
	terminated := make(chan struct{})
	handler := NewWebhookHandler(cfg, func(spec JobSpec) Outcome {
		close(started)
		<-spec.Context.Done()
		close(terminated)
		return failureOutcome(spec, spec.Context.Err())
	}, queue, NewWebhookDeduper(0, nil), spool)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newWebhookServer(rootRoutes(handler))
	done := make(chan error, 1)
	go func() { done <- serveApp(ctx, server, listener, queue, spool) }()
	body := marshalJSON(t, webhookPayloadMap())
	req, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/webhooks/github", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-github-event", "workflow_run")
	req.Header.Set("x-github-delivery", "cancel-and-replay")
	req.Header.Set("x-hub-signature-256", signBody(t, testWebhookSecret, body))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook status %d", response.StatusCode)
	}
	waitEvent(t, started)
	cancel()
	waitEvent(t, terminated)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server shutdown stalled")
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWebhookSpool(dir, DefaultWebhookMaxBodyBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replayed := make(chan struct{})
	replayQueue := NewReleaseTaskQueue(func(error) {}, QueueOptions{})
	NewWebhookHandler(cfg, func(JobSpec) Outcome { close(replayed); return Outcome{} }, replayQueue, NewWebhookDeduper(0, nil), restarted)
	waitEvent(t, replayed)
	restarted.Stop()
	replayQueue.Wait()
	pending, err := restarted.Pending()
	if err != nil || len(pending) != 0 {
		t.Fatalf("replayed durable job not acknowledged: %+v %v", pending, err)
	}
}

func TestWorkflowJobTimeoutIsAppliedToRunner(t *testing.T) {
	stubGitHubFlow(t)
	cfg := &AppConfig{AppID: "123", ApiURL: "https://api.github.com", ReleaseBranches: []string{"main"}, CIWorkflowNames: []string{"CI"}, JobTimeout: 30 * time.Millisecond}
	payload, validationError := DecodeWorkflowRunPayload(webhookPayloadMap(), nil)
	if validationError != "" {
		t.Fatal(validationError)
	}
	err := ReleaseFromWorkflowRunContext(context.Background(), payload, cfg, func(spec JobSpec) Outcome { <-spec.Context.Done(); return failureOutcome(spec, spec.Context.Err()) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("job deadline: %v", err)
	}
}

func TestExecutionConfigDefaultsAndValidation(t *testing.T) {
	base := map[string]string{"VERSIONHOO_APP_ID": "123", "VERSIONHOO_WEBHOOK_SECRET": "test", "VERSIONHOO_PRIVATE_KEY": "test"}
	cfg, err := LoadAppConfigFromEnv(getenvFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrent != DefaultQueueMaxConcurrent || cfg.JobTimeout != DefaultJobTimeout {
		t.Fatalf("defaults: %+v", cfg)
	}
	for key, value := range map[string]string{"VERSIONHOO_MAX_CONCURRENT": "0", "VERSIONHOO_JOB_TIMEOUT_SECONDS": "86401"} {
		env := cloneMap(base)
		env[key] = value
		if _, err := LoadAppConfigFromEnv(getenvFrom(env)); err == nil {
			t.Fatalf("accepted %s=%s", key, value)
		}
	}
	env := cloneMap(base)
	env["HOOVERSION_MAX_CONCURRENT"] = "2"
	env["HOOVERSION_JOB_TIMEOUT_SECONDS"] = "60"
	cfg, err = LoadAppConfigFromEnv(getenvFrom(env))
	if err != nil || cfg.MaxConcurrent != 2 || cfg.JobTimeout != time.Minute {
		t.Fatalf("aliases: %v %+v", err, cfg)
	}
}
