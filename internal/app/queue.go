// This file mirrors the ReleaseTaskQueue of src/app-server.ts: per-key
// serialized task chains (different keys run concurrently), attempts clamped
// to 1..3 and retry delay clamped to 0..30000ms, with last-failure tracking.
package app

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// QueueOptions mirrors ReleaseTaskQueueOptions.
type QueueOptions struct {
	Context context.Context
	// MaxConcurrent bounds simultaneous repository execution; default is four.
	MaxConcurrent int
	MaxAttempts   int
	RetryDelayMs  int
	// MaxPending bounds accepted running and waiting tasks. Non-positive values
	// select DefaultQueueMaxPending.
	MaxPending int
}

const DefaultQueueMaxPending = 64
const DefaultQueueMaxConcurrent = 4

type queuedTask struct {
	task           func() error
	onFinalFailure func(error)
}

type repositoryGate struct {
	semaphore chan struct{}
	users     int
}

type keyQueue struct {
	tasks   []queuedTask
	running bool
}

// ReleaseTaskQueue runs enqueued tasks serially per key.
type ReleaseTaskQueue struct {
	ctx            context.Context
	cancel         context.CancelFunc
	slots          chan struct{}
	repositories   map[string]*repositoryGate
	mu             sync.Mutex
	chains         map[string]*keyQueue
	wg             sync.WaitGroup
	lastFailure    error
	onFailure      func(error)
	onTaskComplete func()
	maxAttempts    int
	retryDelay     time.Duration
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
	maxConcurrent := options.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultQueueMaxConcurrent
	}
	if maxConcurrent > maxPending {
		maxConcurrent = maxPending
	}
	parent := options.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &ReleaseTaskQueue{
		ctx: ctx, cancel: cancel, slots: make(chan struct{}, maxConcurrent), repositories: make(map[string]*repositoryGate),
		chains:      make(map[string]*keyQueue),
		onFailure:   onFailure,
		maxAttempts: maxAttempts,
		retryDelay:  time.Duration(retryDelayMs) * time.Millisecond,
		maxPending:  maxPending,
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
	if q.ctx.Err() != nil || q.pending >= q.maxPending {
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

// Cancel stops new admissions and interrupts cooperative active execution.
// Wait remains required before releasing resources owned by running tasks.
func (q *ReleaseTaskQueue) Cancel()                  { q.cancel() }
func (q *ReleaseTaskQueue) Context() context.Context { return q.ctx }

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

		err := q.runForRepository(key, t.task)
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

// queueRepository retains branch FIFO keys while excluding concurrent branch
// publication into the same repository. Keys without a branch remain distinct.
func queueRepository(key string) string {
	if pos := strings.LastIndexByte(key, ':'); pos >= 0 {
		return strings.ToLower(key[:pos])
	}
	return key
}

func (q *ReleaseTaskQueue) runForRepository(key string, task func() error) error {
	repository := queueRepository(key)
	q.mu.Lock()
	gate := q.repositories[repository]
	if gate == nil {
		gate = &repositoryGate{semaphore: make(chan struct{}, 1)}
		q.repositories[repository] = gate
	}
	gate.users++
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		gate.users--
		if gate.users == 0 {
			delete(q.repositories, repository)
		}
		q.mu.Unlock()
	}()
	// Acquire repository exclusion first: branches waiting on one repository
	// must not consume the global execution budget.
	select {
	case gate.semaphore <- struct{}{}:
	case <-q.ctx.Done():
		return q.ctx.Err()
	}
	defer func() { <-gate.semaphore }()
	select {
	case q.slots <- struct{}{}:
	case <-q.ctx.Done():
		return q.ctx.Err()
	}
	defer func() { <-q.slots }()
	return q.runWithRetry(task)
}

// runWithRetry keeps the repository reserved across retries; cancellation
// prevents another attempt and interrupts retry delays.
func (q *ReleaseTaskQueue) runWithRetry(task func() error) error {
	for attempt := 1; ; attempt++ {
		if err := q.ctx.Err(); err != nil {
			return err
		}
		err := task()
		if err == nil {
			return nil
		}
		if q.ctx.Err() != nil {
			return q.ctx.Err()
		}
		if attempt >= q.maxAttempts {
			return err
		}
		if q.retryDelay > 0 {
			timer := time.NewTimer(q.retryDelay)
			select {
			case <-timer.C:
			case <-q.ctx.Done():
				timer.Stop()
				return q.ctx.Err()
			}
		}
	}
}
