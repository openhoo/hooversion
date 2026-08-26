// This file mirrors the WebhookDeduper in-memory deduper of
// src/app-server.ts: dual-key reservations with a 24h TTL, in_flight /
// succeeded states, and release-on-failure retryability.
package app

import (
	"sync"
	"time"
)

const defaultDedupeTTL = 24 * time.Hour

type dedupeState string

const (
	dedupeInFlight  dedupeState = "in_flight"
	dedupeSucceeded dedupeState = "succeeded"
)

type dedupeEntry struct {
	state     dedupeState
	expiresAt time.Time
}

// WebhookDeduper tracks delivery and workflow-run keys. A nil or empty key is
// a no-op reservation (always succeeds), mirroring reserve(undefined).
type WebhookDeduper struct {
	mu   sync.Mutex
	seen map[string]*dedupeEntry
	ttl  time.Duration
	now  func() time.Time
}

// NewWebhookDeduper returns a deduper with the 24h default TTL; ttl <= 0
// selects the default and a nil clock uses time.Now.
func NewWebhookDeduper(ttl time.Duration, now func() time.Time) *WebhookDeduper {
	if ttl <= 0 {
		ttl = defaultDedupeTTL
	}
	if now == nil {
		now = time.Now
	}
	return &WebhookDeduper{seen: make(map[string]*dedupeEntry), ttl: ttl, now: now}
}

func (d *WebhookDeduper) valid(key string) bool { return key != "" }

// Reserve records an in_flight reservation; false means duplicate.
func (d *WebhookDeduper) Reserve(key string) bool {
	if !d.valid(key) {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	if _, exists := d.seen[key]; exists {
		return false
	}
	d.seen[key] = &dedupeEntry{state: dedupeInFlight, expiresAt: d.now().Add(d.ttl)}
	return true
}

// Succeed marks an existing entry succeeded and refreshes its TTL expiry.
func (d *WebhookDeduper) Succeed(key string) {
	if !d.valid(key) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if entry, ok := d.seen[key]; ok {
		entry.state = dedupeSucceeded
		entry.expiresAt = d.now().Add(d.ttl)
	}
}

// Release drops the reservation so redeliveries can retry.
func (d *WebhookDeduper) Release(key string) {
	if !d.valid(key) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, key)
}

// State reports the current state of a key (used by tests).
func (d *WebhookDeduper) State(key string) (dedupeState, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, ok := d.seen[key]
	if !ok {
		return "", false
	}
	return entry.state, true
}

// prune removes expired entries; callers hold mu.
func (d *WebhookDeduper) prune() {
	now := d.now()
	for key, entry := range d.seen {
		if !entry.expiresAt.After(now) {
			delete(d.seen, key)
		}
	}
}
