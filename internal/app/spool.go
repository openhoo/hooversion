package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultWebhookSpoolMaxBytes   int64 = 64 * 1024 * 1024
	defaultWebhookSpoolMaxRecords       = 4096
	maxWebhookSpoolPathBytes            = 4096
	maxWebhookSpoolKeyBytes             = 2048
	maxWebhookDeliveryBytes             = 256
	webhookSpoolRecordVersion           = 1
	webhookSpoolSequenceWidth           = 20
	webhookSpoolPollInterval            = time.Second
	webhookSpoolOwnerName               = ".owner.lock"
	webhookSpoolTerminalSuffix          = ".terminal"
	webhookSpoolTempSuffix              = ".tmp"
	webhookSpoolTerminalVersion         = 1
)

var ErrWebhookSpoolFull = errors.New("webhook spool is full")

// WebhookSpoolRecord is the non-secret, validated webhook data persisted before
// a workflow_run is acknowledged. Payload is copied and stored as base64 by
// encoding/json, so records are one bounded JSON object per file.
type WebhookSpoolRecord struct {
	DeliveryKey string
	WorkflowKey string
	QueueKey    string
	Payload     []byte
}

type persistedWebhookSpoolRecord struct {
	Version     int    `json:"version"`
	DeliveryKey string `json:"delivery_key"`
	WorkflowKey string `json:"workflow_key"`
	QueueKey    string `json:"queue_key"`
	Payload     []byte `json:"payload"`
}

type persistedWebhookSpoolTerminal struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
}

const (
	webhookSpoolStatusCompleted = "completed"
	webhookSpoolStatusFailed    = "failed"
)

// WebhookSpoolEntry identifies one durable record. Path is always a generated
// child of the configured spool directory; callers must not construct it.
type WebhookSpoolEntry struct {
	Path   string
	Record WebhookSpoolRecord
}

type WebhookSpoolConsumer func(WebhookSpoolEntry) (func() error, func(error))

type WebhookSpool struct {
	mu             sync.Mutex
	dir            string
	root           *os.Root
	owner          *os.File
	queue          *ReleaseTaskQueue
	maxBodyBytes   int64
	maxRecordBytes int64
	maxBytes       int64
	maxRecords     int
	bytes          int64
	artifactBytes  int64
	records        int
	next           uint64
	claimed        map[string]bool
	wake           chan struct{}
	stop           chan struct{}
	done           chan struct{}
	started        bool
	stopped        bool
	closed         bool
}

// NewWebhookSpool creates a private, bounded file-backed spool. Existing
// records are scanned in sequence order; malformed records are quarantined so
// one damaged file cannot prevent later workflow runs from draining.
func NewWebhookSpool(dir string, maxBodyBytes int, maxBytes ...int64) (*WebhookSpool, error) {
	if dir == "" || strings.IndexByte(dir, 0) >= 0 {
		return nil, errors.New("webhook spool directory path is unsafe")
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultWebhookMaxBodyBytes
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve webhook spool directory: %w", err)
	}
	if absolute == "" || len(absolute) > maxWebhookSpoolPathBytes || strings.IndexByte(absolute, 0) >= 0 {
		return nil, errors.New("webhook spool directory path is unsafe")
	}
	if err := prepareWebhookSpoolDir(absolute); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, fmt.Errorf("open webhook spool root: %w", err)
	}
	owner, err := acquireWebhookSpoolOwner(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	limit := DefaultWebhookSpoolMaxBytes
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = maxBytes[0]
	}
	bodyLimit := int64(maxBodyBytes)
	// JSON/base64 overhead is bounded below two body lengths for all practical
	// payloads; leave room for keys and object syntax as well.
	recordLimit := bodyLimit + bodyLimit/2 + 4096
	if recordLimit < bodyLimit || recordLimit <= 0 {
		_ = releaseWebhookSpoolOwner(owner)
		_ = root.Close()
		return nil, errors.New("webhook spool record size overflow")
	}
	spool := &WebhookSpool{
		dir:            absolute,
		root:           root,
		owner:          owner,
		maxBodyBytes:   bodyLimit,
		maxRecordBytes: recordLimit,
		maxBytes:       limit,
		maxRecords:     defaultWebhookSpoolMaxRecords,
		claimed:        make(map[string]bool),
		wake:           make(chan struct{}, 1),
	}
	if err := spool.recount(); err != nil {
		_ = releaseWebhookSpoolOwner(owner)
		_ = root.Close()
		return nil, err
	}
	return spool, nil
}
func (s *WebhookSpool) readDirLocked() ([]os.DirEntry, error) {
	directory, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return nil, readErr
	}
	return entries, closeErr
}

func (s *WebhookSpool) syncDirectoryLocked() error {
	if err := syncDirectoryFn(s.dir); err != nil {
		return err
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (s *WebhookSpool) recount() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.sortedNamesLocked()
	if err != nil {
		return fmt.Errorf("scan webhook spool: %w", err)
	}
	changed, err := s.reconcileTerminalLocked()
	if err != nil {
		return err
	}
	names, err = s.sortedNamesLocked()
	if err != nil {
		return fmt.Errorf("scan webhook spool: %w", err)
	}
	var bytesUsed int64
	var records int
	var next uint64
	for _, name := range names {
		sequence, _ := webhookSpoolSequence(name)
		if sequence >= next {
			if sequence == math.MaxUint64 {
				return errors.New("webhook spool sequence exhausted")
			}
			next = sequence + 1
		}
		_, valid, err := s.readLocked(name)
		if err != nil {
			return err
		}
		if !valid {
			changed = true
			continue
		}
		info, err := s.root.Lstat(name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		bytesUsed += info.Size()
		records++
	}
	artifactBytes, err := s.artifactBytesLocked()
	if err != nil {
		return err
	}
	bytesUsed += artifactBytes
	if changed {
		if err := s.syncDirectoryLocked(); err != nil {
			return fmt.Errorf("sync webhook spool cleanup: %w", err)
		}
	}
	s.bytes, s.records, s.next = bytesUsed, records, next
	s.artifactBytes = artifactBytes
	return nil
}
func spoolArtifactKind(name string) string {
	if strings.HasPrefix(name, ".") && strings.HasSuffix(name, webhookSpoolTempSuffix) {
		core := strings.TrimSuffix(strings.TrimPrefix(name, "."), webhookSpoolTempSuffix)
		if _, ok := webhookSpoolSequence(core); ok {
			return "temp"
		}
		if strings.HasSuffix(core, webhookSpoolTerminalSuffix) {
			if _, ok := webhookSpoolSequence(strings.TrimSuffix(core, webhookSpoolTerminalSuffix)); ok {
				return "temp"
			}
		}
	}
	if strings.HasSuffix(name, webhookSpoolTerminalSuffix) {
		core := strings.TrimSuffix(name, webhookSpoolTerminalSuffix)
		if _, ok := webhookSpoolSequence(core); ok {
			return "terminal"
		}
	}
	const quarantineMarker = ".json.corrupt"
	if marker := strings.Index(name, quarantineMarker); marker >= 0 {
		core := name[:marker+len(".json")]
		if _, ok := webhookSpoolSequence(core); ok {
			suffix := name[marker+len(quarantineMarker):]
			if suffix == "" || (strings.HasPrefix(suffix, ".") && suffix[1:] != "" && strings.Trim(suffix[1:], "0123456789") == "") {
				return "quarantine"
			}
		}
	}
	return ""
}

func (s *WebhookSpool) artifactBytesLocked() (int64, error) {
	entries, err := s.readDirLocked()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		kind := spoolArtifactKind(entry.Name())
		if kind == "" {
			continue
		}
		info, err := s.root.Lstat(entry.Name())
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total, nil
}

func (s *WebhookSpool) writeTerminalMarkerLocked(name string, data []byte, rollbackOnSyncFailure bool) error {
	markerName := name + webhookSpoolTerminalSuffix
	tempName := "." + markerName + webhookSpoolTempSuffix
	file, err := s.root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create webhook spool terminal marker: %w", err)
	}
	if err := writeAndSync(file, data); err != nil {
		_ = s.root.Remove(tempName)
		return err
	}
	if err := s.root.Rename(tempName, markerName); err != nil {
		_ = s.root.Remove(tempName)
		return fmt.Errorf("commit webhook spool terminal marker: %w", err)
	}
	if err := s.syncDirectoryLocked(); err != nil {
		if rollbackOnSyncFailure {
			removeErr := s.root.Remove(markerName)
			if removeErr == nil {
				_ = s.syncDirectoryLocked()
			}
			if removeErr != nil && !os.IsNotExist(removeErr) {
				return fmt.Errorf("sync webhook spool terminal marker: %w (rollback: %v)", err, removeErr)
			}
		}
		return fmt.Errorf("sync webhook spool terminal marker: %w", err)
	}
	return nil
}

func (s *WebhookSpool) removeTerminalMarkerLocked(name string, data []byte) error {
	markerName := name + webhookSpoolTerminalSuffix
	if err := s.root.Remove(markerName); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove webhook spool terminal marker: %w", err)
	}
	if err := s.syncDirectoryLocked(); err != nil {
		restoreErr := s.writeTerminalMarkerLocked(name, data, false)
		if restoreErr != nil {
			return fmt.Errorf("sync webhook spool terminal cleanup: %w (restore: %v)", err, restoreErr)
		}
		return fmt.Errorf("sync webhook spool terminal cleanup: %w", err)
	}
	return nil
}

func (s *WebhookSpool) reconcileTerminalLocked() (bool, error) {
	entries, err := s.readDirLocked()
	if err != nil {
		return false, err
	}
	changed := false
	for _, entry := range entries {
		if spoolArtifactKind(entry.Name()) != "terminal" {
			continue
		}
		markerName := entry.Name()
		info, err := s.root.Lstat(markerName)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return changed, err
		}
		if !info.Mode().IsRegular() {
			if err := s.root.Remove(markerName); err != nil && !os.IsNotExist(err) {
				return changed, err
			}
			changed = true
			continue
		}
		if info.Size() <= 0 || info.Size() > s.maxRecordBytes {
			if err := s.root.Remove(markerName); err != nil && !os.IsNotExist(err) {
				return changed, err
			}
			changed = true
			continue
		}
		data, err := s.root.ReadFile(markerName)
		if err != nil {
			return changed, err
		}
		var marker persistedWebhookSpoolTerminal
		if json.Unmarshal(data, &marker) != nil ||
			marker.Version != webhookSpoolTerminalVersion ||
			(marker.Status != webhookSpoolStatusCompleted && marker.Status != webhookSpoolStatusFailed) {
			if err := s.root.Remove(markerName); err != nil && !os.IsNotExist(err) {
				return changed, err
			}
			changed = true
			continue
		}
		recordName := strings.TrimSuffix(markerName, webhookSpoolTerminalSuffix)
		if _, ok := s.safePath(recordName); ok {
			if err := s.root.Remove(recordName); err != nil && !os.IsNotExist(err) {
				return changed, err
			}
			if err := s.syncDirectoryLocked(); err != nil {
				return changed, fmt.Errorf("sync webhook spool record cleanup: %w", err)
			}
		}
		if err := s.removeTerminalMarkerLocked(recordName, data); err != nil {
			return changed, err
		}
		changed = true
	}
	// A crashed writer can leave a temporary record or terminal marker. Neither
	// is a valid queue item, so remove only names generated by this spool.
	for _, entry := range entries {
		if spoolArtifactKind(entry.Name()) != "temp" {
			continue
		}
		if err := s.root.Remove(entry.Name()); err != nil && !os.IsNotExist(err) {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

func webhookSpoolSequence(name string) (uint64, bool) {
	if len(name) != webhookSpoolSequenceWidth+len(".json") || !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	for _, char := range name[:webhookSpoolSequenceWidth] {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	sequence, err := strconv.ParseUint(name[:webhookSpoolSequenceWidth], 10, 64)
	return sequence, err == nil
}

func (s *WebhookSpool) sortedNamesLocked() ([]string, error) {
	entries, err := s.readDirLocked()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if _, ok := webhookSpoolSequence(name); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *WebhookSpool) safePath(name string) (string, bool) {
	if _, ok := webhookSpoolSequence(name); !ok {
		return "", false
	}
	path := filepath.Join(s.dir, name)
	if filepath.Dir(path) != s.dir || filepath.Base(path) != name {
		return "", false
	}
	return path, true
}

func (s *WebhookSpool) readLocked(name string) (WebhookSpoolEntry, bool, error) {
	path, ok := s.safePath(name)
	if !ok {
		return WebhookSpoolEntry{}, false, nil
	}
	info, err := s.root.Lstat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return WebhookSpoolEntry{}, false, nil
		}
		return WebhookSpoolEntry{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return WebhookSpoolEntry{}, false, nil
	}
	if info.Size() <= 0 || info.Size() > s.maxRecordBytes {
		s.quarantineLocked(path)
		return WebhookSpoolEntry{}, false, nil
	}
	file, err := s.root.Open(name)
	if err != nil {
		if os.IsNotExist(err) {
			return WebhookSpoolEntry{}, false, nil
		}
		return WebhookSpoolEntry{}, false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, s.maxRecordBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return WebhookSpoolEntry{}, false, readErr
	}
	if closeErr != nil {
		return WebhookSpoolEntry{}, false, closeErr
	}
	if int64(len(data)) > s.maxRecordBytes {
		s.quarantineLocked(path)
		return WebhookSpoolEntry{}, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var persisted persistedWebhookSpoolRecord
	if err := decoder.Decode(&persisted); err != nil {
		s.quarantineLocked(path)
		return WebhookSpoolEntry{}, false, nil
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		s.quarantineLocked(path)
		return WebhookSpoolEntry{}, false, nil
	}
	if persisted.Version != webhookSpoolRecordVersion || !validSpoolRecord(persisted.DeliveryKey, persisted.WorkflowKey, persisted.QueueKey, persisted.Payload, s.maxBodyBytes) {
		s.quarantineLocked(path)
		return WebhookSpoolEntry{}, false, nil
	}
	return WebhookSpoolEntry{Path: path, Record: WebhookSpoolRecord{
		DeliveryKey: persisted.DeliveryKey,
		WorkflowKey: persisted.WorkflowKey,
		QueueKey:    persisted.QueueKey,
		Payload:     append([]byte(nil), persisted.Payload...),
	}}, true, nil
}

func validSpoolRecord(deliveryKey, workflowKey, queueKey string, payload []byte, maxBodyBytes int64) bool {
	if deliveryKey != "" && (!strings.HasPrefix(deliveryKey, "delivery:") ||
		!safeSpoolKey(deliveryKey, maxWebhookDeliveryBytes)) {
		return false
	}
	return safeSpoolKey(workflowKey, maxWebhookSpoolKeyBytes) &&
		strings.HasPrefix(workflowKey, "workflow_run:") &&
		safeSpoolKey(queueKey, maxWebhookSpoolKeyBytes) &&
		len(payload) > 0 && int64(len(payload)) <= maxBodyBytes && json.Valid(payload)
}

func safeSpoolKey(value string, maxBytes int) bool {
	return len(value) > 0 && len(value) <= maxBytes &&
		strings.IndexByte(value, 0) < 0 &&
		!strings.ContainsAny(value, "\r\n")
}

func (s *WebhookSpool) quarantineLocked(path string) {
	name, ok := s.nameForPath(path)
	if !ok {
		return
	}
	base := name + ".corrupt"
	candidate := base
	for i := range 100 {
		if i > 0 {
			candidate = fmt.Sprintf("%s.%d", base, i)
		}
		if _, err := s.root.Lstat(candidate); os.IsNotExist(err) {
			if err := s.root.Rename(name, candidate); err == nil {
				log.Printf("quarantined corrupt webhook spool record %s", name)
			}
			return
		}
	}
}

func (s *WebhookSpool) validateAdmission(record WebhookSpoolRecord) error {
	if !validSpoolRecord(record.DeliveryKey, record.WorkflowKey, record.QueueKey, record.Payload, s.maxBodyBytes) {
		return errors.New("invalid webhook spool record")
	}
	return nil
}

func (s *WebhookSpool) refreshArtifactBytesLocked() error {
	current, err := s.artifactBytesLocked()
	if err != nil {
		return err
	}
	s.bytes += current - s.artifactBytes
	s.artifactBytes = current
	return nil
}

func (s *WebhookSpool) cleanupQuarantineLocked(required int64) error {
	if s.bytes <= s.maxBytes-required {
		return nil
	}
	entries, err := s.readDirLocked()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if spoolArtifactKind(entry.Name()) != "quarantine" {
			continue
		}
		info, err := s.root.Lstat(entry.Name())
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := s.root.Remove(entry.Name()); err != nil {
			return err
		}
		s.bytes -= info.Size()
		if s.bytes < 0 {
			s.bytes = 0
		}
		if s.bytes <= s.maxBytes-required {
			break
		}
	}
	if err := s.syncDirectoryLocked(); err != nil {
		return fmt.Errorf("sync webhook spool cleanup: %w", err)
	}
	s.artifactBytes, err = s.artifactBytesLocked()
	if err != nil {
		return err
	}
	return nil
}

// Admit atomically writes and fsyncs one record before returning. The returned
// path is later acknowledged after execution succeeds or reaches final failure.
func (s *WebhookSpool) Admit(record WebhookSpoolRecord) (string, error) {
	if err := s.validateAdmission(record); err != nil {
		return "", err
	}
	persisted, err := json.Marshal(persistedWebhookSpoolRecord{
		Version: webhookSpoolRecordVersion, DeliveryKey: record.DeliveryKey,
		WorkflowKey: record.WorkflowKey, QueueKey: record.QueueKey,
		Payload: record.Payload,
	})
	if err != nil {
		return "", fmt.Errorf("encode webhook spool record: %w", err)
	}
	if int64(len(persisted)) > s.maxRecordBytes {
		return "", errors.New("webhook spool record is too large")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("webhook spool is closed")
	}
	if err := s.refreshArtifactBytesLocked(); err != nil {
		return "", err
	}
	if err := s.cleanupQuarantineLocked(int64(len(persisted))); err != nil {
		return "", err
	}
	if s.records >= s.maxRecords || s.bytes > s.maxBytes-int64(len(persisted)) {
		return "", ErrWebhookSpoolFull
	}
	for {
		if s.next == math.MaxUint64 {
			return "", errors.New("webhook spool sequence exhausted")
		}
		name := fmt.Sprintf("%0*d.json", webhookSpoolSequenceWidth, s.next)
		s.next++
		finalPath, ok := s.safePath(name)
		if !ok {
			return "", errors.New("webhook spool path is unsafe")
		}
		if _, err := s.root.Lstat(name); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		tempName := "." + name + webhookSpoolTempSuffix
		file, err := s.root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", fmt.Errorf("create webhook spool record: %w", err)
		}
		writeErr := writeAndSync(file, persisted)
		if writeErr != nil {
			_ = s.root.Remove(tempName)
			return "", writeErr
		}
		if err := s.root.Rename(tempName, name); err != nil {
			_ = s.root.Remove(tempName)
			return "", fmt.Errorf("commit webhook spool record: %w", err)
		}
		if err := s.syncDirectoryLocked(); err != nil {
			// A rename without a durable directory entry must not become a
			// visible record after a crash.
			removeErr := s.root.Remove(name)
			if removeErr == nil {
				_ = s.syncDirectoryLocked()
			}
			if removeErr != nil && !os.IsNotExist(removeErr) {
				return "", fmt.Errorf("sync webhook spool directory: %w (rollback: %v)", err, removeErr)
			}
			return "", fmt.Errorf("sync webhook spool directory: %w", err)
		}
		s.bytes += int64(len(persisted))
		s.records++
		return finalPath, nil
	}
}

func writeAndSync(file *os.File, data []byte) error {
	written, err := file.Write(data)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("write webhook spool record: %w", err)
	}
	if written != len(data) {
		_ = file.Close()
		return errors.New("short webhook spool record write")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync webhook spool record: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close webhook spool record: %w", err)
	}
	return nil
}

// The hook is retained for focused durability tests; production synchronization
// is performed by syncDirectoryLocked through the retained rooted handle.
var syncDirectoryFn = func(string) error { return nil }

// Pending returns valid persisted entries in FIFO order. Corrupt regular files
// are quarantined and skipped; symlinks and unrelated names are ignored.
func (s *WebhookSpool) Pending() ([]WebhookSpoolEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingLocked()
}

func (s *WebhookSpool) hasTerminalMarkerLocked(path string) bool {
	name, ok := s.nameForPath(path)
	if !ok {
		return false
	}
	info, err := s.root.Lstat(name + webhookSpoolTerminalSuffix)
	return err == nil && info.Mode().IsRegular()
}

func (s *WebhookSpool) pendingLocked() ([]WebhookSpoolEntry, error) {
	names, err := s.sortedNamesLocked()
	if err != nil {
		return nil, err
	}
	entries := make([]WebhookSpoolEntry, 0, len(names))
	for _, name := range names {
		path := filepath.Join(s.dir, name)
		if s.claimed[path] || s.hasTerminalMarkerLocked(path) {
			continue
		}
		info, statErr := s.root.Lstat(name)
		countedRecord := statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
		entry, valid, err := s.readLocked(name)
		if err != nil {
			return nil, err
		}
		if !valid {
			if countedRecord {
				s.artifactBytes += info.Size()
				if s.records > 0 {
					s.records--
				}
			}
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *WebhookSpool) claimNext() (WebhookSpoolEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.pendingLocked()
	if err != nil || len(entries) == 0 {
		return WebhookSpoolEntry{}, false, err
	}
	entry := entries[0]
	s.claimed[entry.Path] = true
	return entry, true, nil
}

func (s *WebhookSpool) unclaim(path string) {
	s.mu.Lock()
	delete(s.claimed, path)
	s.mu.Unlock()
}

// Ack removes a completed record after durably recording its terminal state.
func (s *WebhookSpool) Ack(path string) error {
	return s.AckTerminal(path, webhookSpoolStatusCompleted)
}

// AckTerminal durably records a terminal outcome before removing the record.
// If cleanup fails after the marker is synced, the marker suppresses replay on
// restart and the operation remains terminal and idempotent.
func (s *WebhookSpool) AckTerminal(path, status string) error {
	if status != webhookSpoolStatusCompleted && status != webhookSpoolStatusFailed {
		return errors.New("invalid webhook spool terminal status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed && s.root == nil {
		return errors.New("webhook spool is closed")
	}
	name, ok := s.nameForPath(path)
	if !ok {
		return errors.New("webhook spool record path is unsafe")
	}

	markerData, err := json.Marshal(persistedWebhookSpoolTerminal{
		Version: webhookSpoolTerminalVersion,
		Status:  status,
	})
	if err != nil {
		return err
	}
	markerExists := false
	markerName := name + webhookSpoolTerminalSuffix
	if markerInfo, markerErr := s.root.Lstat(markerName); markerErr == nil {
		if markerInfo.Mode().IsRegular() {
			candidate, readErr := s.root.ReadFile(markerName)
			var marker persistedWebhookSpoolTerminal
			if readErr == nil && json.Unmarshal(candidate, &marker) == nil &&
				marker.Version == webhookSpoolTerminalVersion &&
				(marker.Status == webhookSpoolStatusCompleted || marker.Status == webhookSpoolStatusFailed) {
				markerExists = true
				markerData = candidate
			}
		}
		if !markerExists {
			_ = s.root.Remove(markerName)
		}
	} else if !os.IsNotExist(markerErr) {
		return markerErr
	}

	info, err := s.root.Lstat(name)
	if os.IsNotExist(err) {
		if !markerExists {
			delete(s.claimed, path)
			return nil
		}
		// A previous attempt may have removed the record but failed to sync
		// that deletion. Establish the durable absence before removing the
		// replay-suppressing marker.
		if err := s.syncDirectoryLocked(); err != nil {
			return fmt.Errorf("sync webhook spool record cleanup: %w", err)
		}
		if err := s.removeTerminalMarkerLocked(name, markerData); err != nil {
			return err
		}
		delete(s.claimed, path)
		if s.artifactBytes >= int64(len(markerData)) {
			s.artifactBytes -= int64(len(markerData))
		}
		if s.bytes >= int64(len(markerData)) {
			s.bytes -= int64(len(markerData))
		}
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("webhook spool record path is not a regular file")
	}

	if !markerExists {
		if err := s.writeTerminalMarkerLocked(name, markerData, true); err != nil {
			return err
		}
		s.artifactBytes += int64(len(markerData))
		s.bytes += int64(len(markerData))
	}

	removedRecord := false
	if err := s.root.Remove(name); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("remove webhook spool record: %w", err)
		}
	} else {
		removedRecord = true
	}
	if removedRecord {
		delete(s.claimed, path)
		if s.records > 0 {
			s.records--
		}
		if s.bytes >= info.Size() {
			s.bytes -= info.Size()
		} else {
			s.bytes = 0
		}
	}
	// The marker remains in place until this sync proves the record deletion
	// durable. A failure therefore leaves replay suppression intact.
	if err := s.syncDirectoryLocked(); err != nil {
		return fmt.Errorf("sync webhook spool record cleanup: %w", err)
	}
	if err := s.removeTerminalMarkerLocked(name, markerData); err != nil {
		return err
	}
	delete(s.claimed, path)
	if s.artifactBytes >= int64(len(markerData)) {
		s.artifactBytes -= int64(len(markerData))
	}
	if s.bytes >= int64(len(markerData)) {
		s.bytes -= int64(len(markerData))
	}
	return nil
}

func (s *WebhookSpool) nameForPath(path string) (string, bool) {
	if filepath.Dir(path) != s.dir {
		return "", false
	}
	name := filepath.Base(path)
	_, ok := webhookSpoolSequence(name)
	return name, ok
}

// Discard quarantines a semantically invalid record discovered during startup.
func (s *WebhookSpool) Discard(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, ok := s.nameForPath(path)
	if !ok {
		return
	}
	if info, err := s.root.Lstat(name); err == nil {
		s.quarantineLocked(path)
		if s.records > 0 {
			s.records--
		}
		s.artifactBytes += info.Size()
		// Quarantine keeps the same bytes on disk; it remains within the cap.
	}
}

// Start attaches a consumer and continuously drains persisted records into the
// bounded keyed queue. Queue completion notifications avoid polling when a
// saturated queue frees a slot; the ticker recovers from missed notifications.
func (s *WebhookSpool) Start(queue *ReleaseTaskQueue, consumer WebhookSpoolConsumer) {
	if queue == nil || consumer == nil {
		return
	}
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.queue = queue
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	s.mu.Unlock()
	queue.setTaskCompleteNotifier(s.Wake)
	s.Wake()
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(webhookSpoolPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.wake:
				s.drain(queue, consumer)
			case <-ticker.C:
				s.drain(queue, consumer)
			case <-s.stop:
				return
			}
		}
	}()
}

// Stop stops the background drainer. Production keeps it for process lifetime;
// tests and embedding callers can release the goroutine explicitly.
func (s *WebhookSpool) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	if !s.stopped {
		s.stopped = true
		close(s.stop)
	}
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Close stops the drainer, waits for claimed queue work, and releases this
// process's exclusive ownership.
func (s *WebhookSpool) Close() error {
	s.Stop()
	s.mu.Lock()
	if s.closed {
		queue := s.queue
		s.mu.Unlock()
		if queue != nil {
			queue.Wait()
		}
		return nil
	}
	s.closed = true
	owner := s.owner
	s.owner = nil
	root := s.root
	queue := s.queue
	s.mu.Unlock()
	if queue != nil {
		queue.Wait()
	}
	ownerErr := releaseWebhookSpoolOwner(owner)
	s.mu.Lock()
	s.root = nil
	s.mu.Unlock()
	return errors.Join(ownerErr, root.Close())
}

// Wake requests an immediate backlog drain. It is intentionally non-blocking.
func (s *WebhookSpool) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *WebhookSpool) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *WebhookSpool) drain(queue *ReleaseTaskQueue, consumer WebhookSpoolConsumer) {
	for {
		entry, ok, err := s.claimNext()
		if err != nil {
			log.Printf("webhook spool scan failed: %v", err)
			return
		}
		if !ok {
			return
		}
		task, onFinalFailure := consumer(entry)
		if !queue.Enqueue(entry.Record.QueueKey, task, func(err error) {
			if onFinalFailure != nil {
				onFinalFailure(err)
			}
		}) {
			s.unclaim(entry.Path)
			return
		}
	}
}
