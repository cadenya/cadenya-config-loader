package command

import (
	"errors"
	"fmt"
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

// Usagef returns an error that exits ExitUsage.
func Usagef(format string, a ...any) error { return usageError{fmt.Errorf(format, a...)} }

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
