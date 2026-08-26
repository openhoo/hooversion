package app

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestWebhookDeduperLifecycle(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	clock := now
	d := NewWebhookDeduper(0, func() time.Time { return clock })

	if !d.Reserve("") {
		t.Fatal("empty key must always reserve")
	}
	if !d.Reserve("k") {
		t.Fatal("first reserve failed")
	}
	if state, _ := d.State("k"); state != dedupeInFlight {
		t.Fatalf("state %v", state)
	}
	if d.Reserve("k") {
		t.Fatal("second reserve succeeded")
	}

	d.Succeed("k")
	if state, _ := d.State("k"); state != dedupeSucceeded {
		t.Fatalf("state after succeed %v", state)
	}
	if d.Reserve("k") {
		t.Fatal("succeeded key re-reserved")
	}

	// TTL expiry (lazy prune on Reserve).
	clock = now.Add(defaultDedupeTTL + time.Second)
	if !d.Reserve("k") {
		t.Fatal("expired key still reserved")
	}

	d.Release("k")
	if _, ok := d.State("k"); ok {
		t.Fatal("released key still present")
	}
}

func TestQueueClamps(t *testing.T) {
	q := NewReleaseTaskQueue(nil, QueueOptions{MaxAttempts: 99, RetryDelayMs: -5})
	if q.maxAttempts != 3 || q.retryDelayMs != 0 {
		t.Fatalf("clamps high: %d %v", q.maxAttempts, q.retryDelayMs)
	}
	q2 := NewReleaseTaskQueue(nil, QueueOptions{MaxAttempts: -7, RetryDelayMs: 999999})
	if q2.maxAttempts != 1 || q2.retryDelayMs != 30*time.Second {
		t.Fatalf("clamps low: %d %v", q2.maxAttempts, q2.retryDelayMs)
	}
	q3 := NewReleaseTaskQueue(nil, QueueOptions{})
	if q3.maxAttempts != 1 || q3.retryDelayMs != 0 {
		t.Fatalf("defaults: %d %v", q3.maxAttempts, q3.retryDelayMs)
	}
}

func TestQueueSameKeySerialDifferentKeysParallel(t *testing.T) {
	var mu sync.Mutex
	active, maxActive := 0, 0
	order := make([]string, 0, 4)
	done := make(chan struct{}, 4)

	task := func(key string) func() error {
		return func() error {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			order = append(order, key)
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			active--
			mu.Unlock()
			done <- struct{}{}
			return nil
		}
	}

	q := NewReleaseTaskQueue(nil, QueueOptions{})
	for i := 0; i < 4; i++ {
		key := "repo-a:main"
		if i >= 2 {
			key = "repo-b:main"
		}
		q.Enqueue(key, task(key), nil)
	}
	q.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxActive != 2 {
		t.Fatalf("max concurrent %d, want 2 (one per key)", maxActive)
	}
	wantOrder := []string{"repo-a:main", "repo-a:main", "repo-b:main", "repo-b:main"}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order %v, want %v", order, wantOrder)
		}
	}
}

func TestQueueRetryAndFinalFailure(t *testing.T) {
	attempts := 0
	succeededAt := 0
	q := NewReleaseTaskQueue(func(error) {}, QueueOptions{MaxAttempts: 3})
	q.Enqueue("k", func() error {
		attempts++
		if attempts == 2 {
			succeededAt = attempts
			return nil
		}
		return errors.New("transient")
	}, func(error) { t.Fatal("onFinalFailure fired despite recovery") })
	q.Wait()
	if succeededAt != 2 {
		t.Fatalf("attempts %d", attempts)
	}

	finalFailures := 0
	q2 := NewReleaseTaskQueue(func(error) {}, QueueOptions{MaxAttempts: 2})
	q2.Enqueue("k", func() error { return errors.New("permanent") }, func(err error) {
		finalFailures++
		if err.Error() != "permanent" {
			t.Fatalf("err %v", err)
		}
	})
	q2.Wait()
	if finalFailures != 1 || q2.Failure() == nil || q2.Failure().Error() != "permanent" {
		t.Fatalf("final failure handling: calls=%d last=%v", finalFailures, q2.Failure())
	}
}
