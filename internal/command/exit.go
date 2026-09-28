package command

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Exit codes. CI can tell a bad invocation from a failed run.
const (
	ExitOK          = 0
	ExitFailure     = 1   // Invalid bundle, API error, or failed apply.
	ExitUsage       = 2   // Bad flags or arguments, or missing settings.
	ExitInterrupted = 130 // Stopped by SIGINT or SIGTERM.
)

// usageError marks a mistake in how the command was invoked or configured.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, a ...any) error { return usageError{fmt.Errorf(format, a...)} }

// Usagef returns an error that exits ExitUsage, for wrappers such as the
// GitHub Action adapter.
func Usagef(format string, a ...any) error { return usagef(format, a...) }

// ExitCode maps an error returned by Run to a process exit code. Pass
// interrupted when the process received a shutdown signal: whatever the run
// was doing when it stopped, the signal is the reason.
func ExitCode(err error, interrupted bool) int {
	switch {
	case err == nil:
		return ExitOK
	case interrupted:
		return ExitInterrupted
	case errors.As(err, new(usageError)):
		return ExitUsage
	}
	return ExitFailure
}

func newLogger(level, format string, w interface{ Write([]byte) (int, error) }) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, usagef("--log-level must be debug, info, warn, or error")
	}
	opts := &slog.HandlerOptions{Level: l}
	switch strings.ToLower(format) {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, usagef("--log-format must be text or json")
}
