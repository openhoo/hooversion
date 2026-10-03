package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	mode := os.Getenv("HOOVERSION_PROCESS_TEST_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "wait":
		time.Sleep(time.Minute)
	case "output":
		fmt.Print(strings.Repeat("x", 8192))
	case "tree", "orphan":
		path, _ := os.Executable()
		child := exec.Command(path, "-test.run=^TestProcessHelper$")
		child.Env = append(os.Environ(), "HOOVERSION_PROCESS_TEST_HELPER=wait")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("HOOVERSION_PROCESS_TEST_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(3)
		}
		if mode == "tree" {
			_ = child.Wait()
		}
	case "env":
		fmt.Print(os.Getenv("HOOVERSION_PROCESS_ISOLATED"))
	}
	os.Exit(0)
}

func helper(t *testing.T, mode string, options Options) Result {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	options.Env = append(options.Env, "HOOVERSION_PROCESS_TEST_HELPER="+mode)
	return Run(context.Background(), options, path, "-test.run=^TestProcessHelper$")
}

func TestCommandDeadlineWaitsForTermination(t *testing.T) {
	start := time.Now()
	result := helper(t, "wait", Options{Timeout: 100 * time.Millisecond})
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("deadline error: %v", result.Err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("deadline took %v", time.Since(start))
	}
}

func TestOutputLimitDoesNotSilentlyTruncate(t *testing.T) {
	result := helper(t, "output", Options{OutputLimit: 1024})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "output exceeds") {
		t.Fatalf("output bound: %+v", result)
	}
	if len(result.Stdout) != 1024 {
		t.Fatalf("captured %d bytes", len(result.Stdout))
	}
}

func TestExplicitChildEnvironmentIsIsolated(t *testing.T) {
	t.Setenv("HOOVERSION_PROCESS_ISOLATED", "parent")
	result := helper(t, "env", Options{Env: []string{"HOOVERSION_PROCESS_ISOLATED=child"}})
	if result.Err != nil || result.Stdout != "child" {
		t.Fatalf("isolated env: %+v", result)
	}
	result = helper(t, "env", Options{Env: []string{}})
	if result.Err != nil || result.Stdout != "" {
		t.Fatalf("empty env inherited parent: %+v", result)
	}
}

func TestCanceledContextDoesNotStartCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := Run(ctx, Options{}, "command-that-does-not-exist")
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("canceled error: %v", result.Err)
	}
}
