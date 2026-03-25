package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Exit codes for aperturecli. These are semantic exit codes that agents
// can parse for control flow, going beyond the typical 0/1 binary.
const (
	// ExitSuccess indicates the command completed successfully.
	ExitSuccess = 0

	// ExitGeneralError indicates an unclassified error.
	ExitGeneralError = 1

	// ExitInvalidArgs indicates invalid arguments or validation failure.
	ExitInvalidArgs = 2

	// ExitConnectionError indicates a gRPC connection failure.
	ExitConnectionError = 3

	// ExitAuthFailure indicates a macaroon authentication failure.
	ExitAuthFailure = 4

	// ExitNotFound indicates a requested resource was not found.
	ExitNotFound = 5

	// ExitDryRunPassed indicates a dry-run completed successfully
	// with no action taken.
	ExitDryRunPassed = 10
)

// CLIError is a structured error that carries a semantic exit code and
// a machine-readable error kind string. The CLI uses this to emit
// structured JSON error objects on stderr and return the appropriate
// exit code.
type CLIError struct {
	// Code is the process exit code.
	Code int

	// Kind is a machine-readable error classifier such as
	// "invalid_args" or "connection_error".
	Kind string

	// Message is the human-readable error description.
	Message string

	// Inner is the wrapped cause, if any.
	Inner error
}

// Error implements the error interface.
func (e *CLIError) Error() string {
	if e.Inner != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Inner)
	}

	return e.Message
}

// Unwrap returns the underlying cause for errors.Is/As chains.
func (e *CLIError) Unwrap() error {
	return e.Inner
}

// NewCLIError creates a new CLIError with the given code, kind, and
// message.
func NewCLIError(code int, kind, message string) *CLIError {
	return &CLIError{
		Code:    code,
		Kind:    kind,
		Message: message,
	}
}

// WrapCLIError creates a new CLIError that wraps an existing error.
func WrapCLIError(code int, kind string, inner error) *CLIError {
	return &CLIError{
		Code:    code,
		Kind:    kind,
		Message: inner.Error(),
		Inner:   inner,
	}
}

// ErrInvalidArgsf creates an invalid-arguments error with a formatted
// message.
func ErrInvalidArgsf(format string, args ...any) *CLIError {
	return NewCLIError(
		ExitInvalidArgs, "invalid_args",
		fmt.Sprintf(format, args...),
	)
}

// ErrConnectionWrap wraps a connection error with the connection_error
// exit code.
func ErrConnectionWrap(inner error) *CLIError {
	return WrapCLIError(
		ExitConnectionError, "connection_error", inner,
	)
}

// ErrAuthWrap wraps an authentication error with the auth_failure exit
// code.
func ErrAuthWrap(inner error) *CLIError {
	return WrapCLIError(ExitAuthFailure, "auth_failure", inner)
}

// ErrNotFoundf creates a not-found error with a formatted message.
func ErrNotFoundf(format string, args ...any) *CLIError {
	return NewCLIError(
		ExitNotFound, "not_found",
		fmt.Sprintf(format, args...),
	)
}

// ErrDryRunPassedNew creates a dry-run-passed indicator. This is not
// a real error; it signals that the dry-run preview completed
// successfully with no mutations.
func ErrDryRunPassedNew() *CLIError {
	return NewCLIError(
		ExitDryRunPassed, "dry_run_passed",
		"dry run completed successfully",
	)
}

// ExitCode extracts the exit code from an error. If the error is a
// CLIError, its Code field is returned. Otherwise 1 (general error)
// is returned.
func ExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}

	var cliErr *CLIError
	if errors.As(err, &cliErr) {
		return cliErr.Code
	}

	return ExitGeneralError
}

// ErrorKind extracts the machine-readable kind string from an error.
// If the error is not a CLIError, "general_error" is returned.
func ErrorKind(err error) string {
	var cliErr *CLIError
	if errors.As(err, &cliErr) {
		return cliErr.Kind
	}

	return "general_error"
}

// jsonErrorPayload is the JSON structure emitted to stderr for
// structured error reporting.
type jsonErrorPayload struct {
	Error    bool   `json:"error"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	ExitCode int    `json:"exit_code"`
}

// WriteErrorJSON writes a structured JSON error object to the given
// writer. This is used by the main entrypoint to emit machine-readable
// errors on stderr.
func WriteErrorJSON(w io.Writer, err error) {
	code := ExitCode(err)
	kind := ErrorKind(err)

	payload := jsonErrorPayload{
		Error:    true,
		Code:     kind,
		Message:  err.Error(),
		ExitCode: code,
	}

	encoder := json.NewEncoder(w)

	// Encoding errors are silently dropped since we're already in
	// the error-reporting path and have no fallback.
	_ = encoder.Encode(payload)
}
