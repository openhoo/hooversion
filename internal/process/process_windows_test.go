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

func TestWindowsCancellationTerminatesProcessTree(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "child-pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		done <- Run(ctx, Options{Env: append(os.Environ(), "HOOVERSION_PROCESS_TEST_HELPER=tree", "HOOVERSION_PROCESS_TEST_PID="+marker)}, executable, "-test.run=^TestProcessHelper$")
	}()
	var pid int
	deadline := time.After(5 * time.Second)
	for pid == 0 {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if pid == 0 {
			select {
			case <-deadline:
				t.Fatal("descendant did not start")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	cancel()
	select {
	case result := <-done:
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("cancel: %v", result.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process tree cancellation stalled")
	}
	status, err := syscall.WaitForSingleObject(handle, 1000)
	if err != nil || status != syscall.WAIT_OBJECT_0 {
		t.Fatalf("descendant still running: status %d error %v", status, err)
	}
}

func TestWindowsExitedParentCannotLeaveDescendantHoldingPipes(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "orphan-pid")
	result := Run(context.Background(), Options{Env: append(os.Environ(), "HOOVERSION_PROCESS_TEST_HELPER=orphan", "HOOVERSION_PROCESS_TEST_PID="+marker)}, executable, "-test.run=^TestProcessHelper$")
	if result.Err == nil {
		t.Fatal("inherited pipe lifetime was silently accepted")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer syscall.CloseHandle(handle)
	status, err := syscall.WaitForSingleObject(handle, 1000)
	if err != nil || status != syscall.WAIT_OBJECT_0 {
		t.Fatalf("orphan survived command cleanup: status %d error %v", status, err)
	}
}
