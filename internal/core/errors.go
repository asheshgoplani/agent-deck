package core

import (
	"errors"
	"fmt"
)

// Stable error codes. They appear verbatim in the envelope; the CLI adapter
// maps them onto its legacy codes and exit statuses.
const (
	CodeNotFound       = "NOT_FOUND"
	CodeAmbiguous      = "AMBIGUOUS"
	CodeInvalid        = "INVALID_OPERATION"
	CodeStorage        = "STORAGE_UNAVAILABLE"
	CodeNoActive       = "NO_ACTIVE_SESSIONS"
	CodeAlreadyRunning = "ALREADY_RUNNING"
	CodeNotRunning     = "NOT_RUNNING"
	CodeSpawnFailed    = "SPAWN_FAILED"
	CodeMissingArg     = "MISSING_ARGUMENT"
	CodeInvalidInput   = "INVALID_INPUT"
	CodeUnknownCommand = "UNKNOWN_COMMAND"
	CodeInternal       = "INTERNAL"
)

// Error is a command failure with a stable code. Data carries typed detail a
// surface may render (for example *SpawnFailure); Cause keeps the underlying
// error for errors.Is/As.
type Error struct {
	Code    string
	Message string
	Data    any
	Cause   error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Cause }

// Errorf builds an *Error with a formatted message.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsError returns err as *Error, wrapping a plain error as CodeInternal.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var ce *Error
	if errors.As(err, &ce) {
		return ce
	}
	return &Error{Code: CodeInternal, Message: err.Error(), Cause: err}
}
