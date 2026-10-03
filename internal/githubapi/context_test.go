package githubapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientContextInterruptsBlockedResponse(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := New(server.URL, "token")
	client.Context = ctx
	done := make(chan error, 1)
	go func() { _, err := client.ReleaseByTag("owner/repo", "v1.0.0"); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP request ignored cancellation")
	}
}

func TestInstallationTokenHonorsCanceledContext(t *testing.T) {
	_, key := generateTestAppKey(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := MintInstallationToken("https://api.github.com", "123", key, 42, []int64{987}, ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("token mint cancellation: %v", err)
	}
}
