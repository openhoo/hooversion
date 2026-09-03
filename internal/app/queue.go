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
	// MaxPending bounds accepted running and waiting tasks. Non-positive values
	// select DefaultQueueMaxPending.
	MaxPending int
}

const DefaultQueueMaxPending = 64

type queuedTask struct {
	task           func() error
	onFinalFailure func(error)
}

type keyQueue struct {
	tasks   []queuedTask
	running bool
}

// ReleaseTaskQueue runs enqueued tasks serially per key.
type ReleaseTaskQueue struct {
	mu             sync.Mutex
	chains         map[string]*keyQueue
	wg             sync.WaitGroup
	lastFailure    error
	onFailure      func(error)
	onTaskComplete func()
	maxAttempts    int
	retryDelayMs   time.Duration
	maxPending     int
	pending        int
}

// NewReleaseTaskQueue mirrors the constructor defaults: maxAttempts 1,
// retryDelayMs 0, maxPending 64, and failures logged. onFailure nil selects
// the default logger.
func NewReleaseTaskQueue(onFailure func(error), options QueueOptions) *ReleaseTaskQueue {
	if onFailure == nil {
		onFailure = func(err error) { log.Print(err) }
	}
	maxAttempts := clampInt(options.MaxAttempts, 1, 3)
	retryDelayMs := clampInt(options.RetryDelayMs, 0, 30000)
	maxPending := options.MaxPending
	if maxPending <= 0 {
		maxPending = DefaultQueueMaxPending
	}
	return &ReleaseTaskQueue{
		chains:       make(map[string]*keyQueue),
		onFailure:    onFailure,
		maxAttempts:  maxAttempts,
		retryDelayMs: time.Duration(retryDelayMs) * time.Millisecond,
		maxPending:   maxPending,
	}
}

// setTaskCompleteNotifier wakes durable backlog drainers whenever a running
// task releases an in-memory admission slot.
func (q *ReleaseTaskQueue) setTaskCompleteNotifier(notify func()) {
	q.mu.Lock()
	q.onTaskComplete = notify
	q.mu.Unlock()
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
// failed. Enqueue never waits for the worker or an earlier task. It returns
// false when the bounded admission limit is saturated; rejected tasks never
// affect the wait group or key FIFO.
func (q *ReleaseTaskQueue) Enqueue(key string, task func() error, onFinalFailure func(error)) bool {
	q.mu.Lock()
	if q.pending >= q.maxPending {
		q.mu.Unlock()
		return false
	}
	state, ok := q.chains[key]
	if !ok {
		state = &keyQueue{running: true}
		q.chains[key] = state
	}
	state.tasks = append(state.tasks, queuedTask{task: task, onFinalFailure: onFinalFailure})
	q.pending++
	q.wg.Add(1)
	startWorker := !ok
	q.mu.Unlock()
	if startWorker {
		go q.drain(key, state)
	}
	return true
}

// Wait blocks until every accepted task finished (test support).
func (q *ReleaseTaskQueue) Wait() { q.wg.Wait() }

// Failure reports the last stored failure (test support).
func (q *ReleaseTaskQueue) Failure() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastFailure
}

func (q *ReleaseTaskQueue) drain(key string, state *keyQueue) {
	for {
		q.mu.Lock()
		current, ok := q.chains[key]
		if !ok || current != state || len(state.tasks) == 0 {
			if ok && current == state {
				delete(q.chains, key)
				state.running = false
			}
			q.mu.Unlock()
			return
		}
		t := state.tasks[0]
		state.tasks[0] = queuedTask{}
		state.tasks = state.tasks[1:]
		q.mu.Unlock()

		err := q.runWithRetry(t.task)
		q.mu.Lock()
		q.pending--
		if err != nil {
			q.lastFailure = err
		}
		notify := q.onTaskComplete
		q.mu.Unlock()
		if notify != nil {
			notify()
		}
		if err != nil {
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
