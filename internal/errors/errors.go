// Package errors defines the single error type used across Hooversion.
// Behavior mirrors src/errors.ts: user-facing failures carry an exit code
// (default 1); the CLI prints the message to stderr and exits with that code.
package errors

import "fmt"

// ExitError is a release/lint/config failure that should terminate the CLI
// with a specific exit code and no stack trace.
type ExitError struct {
	Msg  string
	Code int
}

func (e *ExitError) Error() string { return e.Msg }

// New returns an ExitError with exit code 1.
func New(format string, args ...any) *ExitError {
	return &ExitError{Msg: fmt.Sprintf(format, args...), Code: 1}
}

// Code returns an ExitError with an explicit exit code.
func Code(code int, format string, args ...any) *ExitError {
	return &ExitError{Msg: fmt.Sprintf(format, args...), Code: code}
}
