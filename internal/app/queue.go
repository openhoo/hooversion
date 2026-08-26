// This file mirrors the ReleaseTaskQueue of src/app-server.ts: per-key
// serialized task chains (different keys run concurrently), attempts clamped
// to 1..3 and retry delay clamped to 0..30000ms, with last-failure tracking.
package app

import (
	"log"
	"sync"
	"time"
)

// QueueOptions mirrors ReleaseTaskQueueOptions.
type QueueOptions struct {
	MaxAttempts  int
	RetryDelayMs int
}

type queuedTask struct {
	task           func() error
	onFinalFailure func(error)
}

// ReleaseTaskQueue runs enqueued tasks serially per key.
type ReleaseTaskQueue struct {
	mu           sync.Mutex
	chains       map[string]chan queuedTask
	wg           sync.WaitGroup
	lastFailure  error
	onFailure    func(error)
	maxAttempts  int
	retryDelayMs time.Duration
}

// NewReleaseTaskQueue mirrors the constructor defaults: maxAttempts 1,
// retryDelayMs 0, failures logged. onFailure nil selects the default logger.
func NewReleaseTaskQueue(onFailure func(error), options QueueOptions) *ReleaseTaskQueue {
	if onFailure == nil {
		onFailure = func(err error) { log.Print(err) }
	}
	maxAttempts := clampInt(options.MaxAttempts, 1, 3)
	retryDelayMs := clampInt(options.RetryDelayMs, 0, 30000)
	return &ReleaseTaskQueue{
		chains:       make(map[string]chan queuedTask),
		onFailure:    onFailure,
		maxAttempts:  maxAttempts,
		retryDelayMs: time.Duration(retryDelayMs) * time.Millisecond,
	}
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// Enqueue mirrors enqueue: the task runs after all previously enqueued tasks
// for the same key complete. onFinalFailure fires once when every attempt
// failed.
func (q *ReleaseTaskQueue) Enqueue(key string, task func() error, onFinalFailure func(error)) {
	q.mu.Lock()
	ch, ok := q.chains[key]
	if !ok {
		ch = make(chan queuedTask)
		q.chains[key] = ch
		go q.drain(ch)
	}
	q.wg.Add(1)
	q.mu.Unlock()
	ch <- queuedTask{task: task, onFinalFailure: onFinalFailure}
}

// Wait blocks until every accepted task finished (test support).
func (q *ReleaseTaskQueue) Wait() { q.wg.Wait() }

// Failure reports the last stored failure (test support).
func (q *ReleaseTaskQueue) Failure() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastFailure
}

func (q *ReleaseTaskQueue) drain(ch <-chan queuedTask) {
	for t := range ch {
		err := q.runWithRetry(t.task)
		if err != nil {
			q.mu.Lock()
			q.lastFailure = err
			q.mu.Unlock()
			if t.onFinalFailure != nil {
				t.onFinalFailure(err)
			}
			q.onFailure(err)
		}
		q.wg.Done()
	}
}

// runWithRetry mirrors runWithRetry: retry only on error, at most
// maxAttempts times, sleeping retryDelayMs between attempts.
func (q *ReleaseTaskQueue) runWithRetry(task func() error) error {
	for attempt := 1; ; attempt++ {
		err := task()
		if err == nil {
			return nil
		}
		if attempt >= q.maxAttempts {
			return err
		}
		if q.retryDelayMs > 0 {
			time.Sleep(q.retryDelayMs)
		}
	}
}
