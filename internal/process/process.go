// Package process runs bounded child commands and waits for cancellation cleanup.
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

const DefaultTimeout = 5 * time.Minute
const DefaultOutputLimit = 32 << 20

// Context resolves optional contexts without weakening an explicit caller deadline.
func Context(contexts []context.Context) context.Context {
	if len(contexts) > 0 && contexts[0] != nil {
		return contexts[0]
	}
	return context.Background()
}

type Options struct {
	Dir         string
	Env         []string
	Input       string
	Timeout     time.Duration
	OutputLimit int
}

type Result struct {
	Stdout, Stderr string
	Code           int
	Err            error
}

type boundedBuffer struct {
	mu       sync.Mutex
	data     bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - b.data.Len()
	if n > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	_, _ = b.data.Write(p)
	return n, nil
}
func (b *boundedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String(), b.exceeded
}

// Run cancels the entire child tree and always waits for command completion.
// A finite command deadline applies even when the caller supplies no deadline.
func Run(ctx context.Context, options Options, command string, args ...string) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	limit := options.OutputLimit
	if limit <= 0 {
		limit = DefaultOutputLimit
	}
	stdout, stderr := &boundedBuffer{limit: limit}, &boundedBuffer{limit: limit}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = options.Dir
	if options.Env != nil {
		cmd.Env = append([]string{}, options.Env...)
	}
	if options.Input != "" {
		cmd.Stdin = bytes.NewBufferString(options.Input)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	activate, cleanup, prepareErr := configureProcess(cmd)
	if prepareErr != nil {
		return Result{Code: 1, Err: prepareErr}
	}
	defer cleanup()
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Start()
	if err == nil {
		if activateErr := activate(); activateErr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			err = activateErr
		} else {
			err = cmd.Wait()
		}
	}
	out, outExceeded := stdout.snapshot()
	detail, detailExceeded := stderr.snapshot()
	result := Result{Stdout: out, Stderr: detail, Err: err}
	if err != nil {
		result.Code = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.Code = exit.ExitCode()
		}
	}
	if ctx.Err() != nil {
		result.Err = ctx.Err()
		result.Code = 1
	}
	if outExceeded || detailExceeded {
		result.Err = fmt.Errorf("command output exceeds %d bytes", limit)
		result.Code = 1
	}
	return result
}

var _ io.Writer = (*boundedBuffer)(nil)
