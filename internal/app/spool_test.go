package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func privateSpoolTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func spoolTestRecord(payload []byte, n string) WebhookSpoolRecord {
	return WebhookSpoolRecord{
		DeliveryKey: "delivery:" + n,
		WorkflowKey: "workflow_run:octo/hello:" + n,
		QueueKey:    "octo/hello:main",
		Payload:     payload,
	}
}

func TestWebhookSpoolAdmissionSurvivesQueueSaturation(t *testing.T) {
	dir := privateSpoolTestDir(t)
	stubGitHubFlow(t)
	spool, err := NewWebhookSpool(dir, DefaultWebhookMaxBodyBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	queue := NewReleaseTaskQueue(func(error) {}, QueueOptions{MaxPending: 1})
	deduper := NewWebhookDeduper(0, nil)
	cfg := &AppConfig{
		AppID: "123", WebhookSecret: testWebhookSecret, ApiURL: "https://api.github.com",
		ReleaseBranches: []string{"main"}, CIWorkflowNames: []string{"CI"},
		WebhookMaxBodyBytes: DefaultWebhookMaxBodyBytes,
	}
	handler := NewWebhookHandler(cfg, func(JobSpec) Outcome {
		startedOnce.Do(func() { close(started) })
		<-release
		return Outcome{}
	}, queue, deduper, spool)
	first := marshalJSON(t, webhookPayloadMap())
	if rec := postWebhook(handler, "workflow_run", "spool-first", first, signBody(t, testWebhookSecret, first)); rec.Code != http.StatusAccepted {
		t.Fatalf("first status %d body %s", rec.Code, rec.Body.String())
	}
	<-started

	secondPayload := webhookPayloadMap()
	secondRepo := secondPayload["repository"].(map[string]any)
	secondRepo["id"] = 43
	secondRepo["full_name"] = "octo/other"
	secondRepo["clone_url"] = "https://github.com/octo/other.git"
	secondPayload["workflow_run"].(map[string]any)["head_repository"].(map[string]any)["full_name"] = "octo/other"
	second := marshalJSON(t, secondPayload)
	if rec := postWebhook(handler, "workflow_run", "spool-second", second, signBody(t, testWebhookSecret, second)); rec.Code != http.StatusAccepted {
		t.Fatalf("saturated status %d body %s", rec.Code, rec.Body.String())
	}
	if _, ok := deduper.State("delivery:spool-second"); !ok {
		t.Fatal("durably admitted delivery reservation was released")
	}
	pending, err := spool.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Record.DeliveryKey != "delivery:spool-second" {
		t.Fatalf("pending spool entries: %+v", pending)
	}
	spool.Stop()
	close(release)
	queue.Wait()
}

func TestWebhookSpoolRecoversPersistedBacklog(t *testing.T) {
	dir := privateSpoolTestDir(t)
	payload := []byte(`{"action":"completed"}`)
	first, err := NewWebhookSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	path, err := first.Admit(spoolTestRecord(payload, "one"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("record escaped spool directory: %s", path)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewWebhookSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	pending, err := second.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || string(pending[0].Record.Payload) != string(payload) {
		t.Fatalf("recovered entries: %+v", pending)
	}
}

func TestWebhookSpoolQuarantinesCorruptAndUnsafeRecords(t *testing.T) {
	dir := privateSpoolTestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000000.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000001.json"), []byte(strings.Repeat("x", 5000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outside.json")); !os.IsNotExist(err) {
		t.Fatal("unexpected outside record")
	}
	spool, err := NewWebhookSpool(dir, 128)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := spool.Pending(); err != nil {
		t.Fatal(err)
	} else if len(pending) != 0 {
		t.Fatalf("corrupt entries were admitted: %+v", pending)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.corrupt*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("quarantine files %v", matches)
	}
	if err := spool.Ack(filepath.Join(dir, "..", "outside.json")); err == nil {
		t.Fatal("unsafe acknowledgement path accepted")
	}
}
func TestWebhookSpoolEnforcesExclusiveOwnership(t *testing.T) {
	dir := privateSpoolTestDir(t)
	first, err := NewWebhookSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWebhookSpool(dir, 1024); err == nil {
		t.Fatal("second process acquired spool ownership")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewWebhookSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookSpoolRejectsUnsafeRoots(t *testing.T) {
	parent := privateSpoolTestDir(t)
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWebhookSpool(link, 1024); err == nil {
		t.Fatal("symlink spool root accepted")
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWebhookSpool(target, 1024); err == nil {
		t.Fatal("world-readable spool root accepted")
	}
}

func TestWebhookSpoolDefaultIsNotPredictableTempPath(t *testing.T) {
	dir := resolveWebhookSpoolDir(&AppConfig{})
	temp := filepath.Clean(os.TempDir()) + string(os.PathSeparator)
	if strings.HasPrefix(filepath.Clean(dir)+string(os.PathSeparator), temp) {
		t.Fatalf("default spool path uses predictable temp root: %s", dir)
	}
}

func TestWebhookSpoolRollsBackDirectorySyncFailure(t *testing.T) {
	dir := privateSpoolTestDir(t)
	spool, err := NewWebhookSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	original := syncDirectoryFn
	defer func() { syncDirectoryFn = original }()
	syncDirectoryFn = func(string) error { return errors.New("injected directory sync failure") }
	if _, err := spool.Admit(spoolTestRecord([]byte(`{"ok":true}`), "rollback")); err == nil {
		t.Fatal("admission unexpectedly succeeded")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("record survived failed directory sync: %v", matches)
	}
}

func TestWebhookSpoolCleansArtifactsBeforeAdmissionCap(t *testing.T) {
	dir := privateSpoolTestDir(t)
	quarantine := filepath.Join(dir, "00000000000000000000.json.corrupt")
	if err := os.WriteFile(quarantine, []byte(strings.Repeat("x", 950)), 0o600); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(dir, ".00000000000000000001.json.tmp")
	if err := os.WriteFile(temp, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	spool, err := NewWebhookSpool(dir, 128, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("stale temporary artifact survived startup")
	}
	if _, err := spool.Admit(spoolTestRecord([]byte(`{"ok":true}`), "artifact")); err != nil {
		t.Fatalf("artifact cleanup did not free cap: %v", err)
	}
	if _, err := os.Stat(quarantine); !os.IsNotExist(err) {
		t.Fatal("quarantine artifact survived cap cleanup")
	}
}

func TestWebhookSpoolTerminalMarkerSuppressesRestartReplay(t *testing.T) {
	dir := privateSpoolTestDir(t)
	first, err := NewWebhookSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	path, err := first.Admit(spoolTestRecord([]byte(`{"ok":true}`), "terminal"))
	if err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(persistedWebhookSpoolTerminal{
		Version: webhookSpoolTerminalVersion,
		Status:  webhookSpoolStatusCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+webhookSpoolTerminalSuffix, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewWebhookSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	pending, err := second.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("terminally acknowledged record replayed: %+v", pending)
	}
}

func TestWebhookSpoolTerminalCleanupSyncFailures(t *testing.T) {
	statuses := []string{webhookSpoolStatusCompleted, webhookSpoolStatusFailed}
	for _, status := range statuses {
		for _, failAt := range []int{1, 2, 3} {
			t.Run(status+"/sync-"+strconv.Itoa(failAt), func(t *testing.T) {
				dir := privateSpoolTestDir(t)
				spool, err := NewWebhookSpool(dir, 1024)
				if err != nil {
					t.Fatal(err)
				}
				path, err := spool.Admit(spoolTestRecord([]byte(`{"ok":true}`), status+"-"+strconv.Itoa(failAt)))
				if err != nil {
					t.Fatal(err)
				}
				original := syncDirectoryFn
				defer func() {
					syncDirectoryFn = original
					_ = spool.Close()
				}()
				calls := 0
				syncDirectoryFn = func(dir string) error {
					calls++
					if calls == failAt {
						return errors.New("injected terminal cleanup sync failure")
					}
					return original(dir)
				}
				if err := spool.AckTerminal(path, status); err == nil {
					t.Fatal("terminal acknowledgement unexpectedly succeeded")
				}
				recordExists := false
				if _, err := os.Stat(path); err == nil {
					recordExists = true
				} else if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				markerExists := false
				if _, err := os.Stat(path + webhookSpoolTerminalSuffix); err == nil {
					markerExists = true
				} else if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				switch failAt {
				case 1:
					if !recordExists || markerExists {
						t.Fatalf("marker creation failure left record=%v marker=%v", recordExists, markerExists)
					}
				case 2, 3:
					if recordExists || !markerExists {
						t.Fatalf("cleanup sync failure left record=%v marker=%v", recordExists, markerExists)
					}
				}
				syncDirectoryFn = original
				if err := spool.AckTerminal(path, status); err != nil {
					t.Fatalf("retry terminal acknowledgement: %v", err)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("record remains after retry: %v", err)
				}
				if _, err := os.Stat(path + webhookSpoolTerminalSuffix); !os.IsNotExist(err) {
					t.Fatalf("terminal marker remains after retry: %v", err)
				}
			})
		}
	}
}

func TestWebhookSpoolCloseWaitsBeforeReleasingOwnership(t *testing.T) {
	dir := privateSpoolTestDir(t)
	spool, err := NewWebhookSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	path, err := spool.Admit(spoolTestRecord([]byte(`{"ok":true}`), "close"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	queue := NewReleaseTaskQueue(func(error) {}, QueueOptions{MaxPending: 1})
	spool.Start(queue, func(WebhookSpoolEntry) (func() error, func(error)) {
		return func() error {
			close(started)
			<-release
			return nil
		}, nil
	})
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- spool.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close released ownership while task was blocked: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := NewWebhookSpool(dir, 1024); err == nil {
		t.Fatal("spool ownership was released while queue task was blocked")
	}
	close(release)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	second, err := NewWebhookSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	_ = path
}

func TestWebhookSpoolRejectsAdmissionAfterClose(t *testing.T) {
	dir := privateSpoolTestDir(t)
	spool, err := NewWebhookSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Admit(spoolTestRecord([]byte(`{"ok":true}`), "closed")); err == nil {
		t.Fatal("admission after Close unexpectedly succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, ok := webhookSpoolSequence(entry.Name()); ok {
			t.Fatalf("admission after Close created record %q", entry.Name())
		}
	}
}
