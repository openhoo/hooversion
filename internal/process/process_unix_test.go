//go:build !windows

package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCancellationKillsDescendantsHoldingPipes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marker := filepath.Join(t.TempDir(), "child-pid")
	done := make(chan Result, 1)
	go func() {
		done <- Run(ctx, Options{}, "/bin/sh", "-c", `sleep 60 & echo $! > "$1"; wait`, "test", marker)
	}()
	var pid int
	deadline := time.After(3 * time.Second)
	for pid == 0 {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if pid == 0 {
			select {
			case <-deadline:
				t.Fatal("descendant did not start")
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	cancel()
	select {
	case result := <-done:
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("cancel: %v", result.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command did not reap after tree cancellation")
	}
	// A killed descendant may briefly remain a zombie under its reaper; neither
	// a vanished process nor a zombie can execute or retain an output pipe.
	for attempt := 0; attempt < 100; attempt++ {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		status := Run(context.Background(), Options{}, "ps", "-o", "stat=", "-p", strconv.Itoa(pid))
		if strings.HasPrefix(strings.TrimSpace(status.Stdout), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant %d is still running", pid)
}

func TestExitedParentCannotLeaveDescendantHoldingPipes(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "orphan-pid")
	start := time.Now()
	result := Run(context.Background(), Options{Env: append(os.Environ(), "HOOVERSION_PROCESS_TEST_HELPER=orphan", "HOOVERSION_PROCESS_TEST_PID="+marker)}, executable, "-test.run=^TestProcessHelper$")
	if result.Err == nil {
		t.Fatal("inherited pipe lifetime was silently accepted")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("orphan retained command pipes")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	status := Run(context.Background(), Options{}, "ps", "-o", "stat=", "-p", strconv.Itoa(pid))
	if strings.TrimSpace(status.Stdout) != "" && !strings.HasPrefix(strings.TrimSpace(status.Stdout), "Z") {
		t.Fatalf("orphan survived command cleanup: %s", status.Stdout)
	}
}
