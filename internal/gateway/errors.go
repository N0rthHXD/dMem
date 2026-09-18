package gateway

import (
	"errors"
	"fmt"
	"time"
)

// ErrorCode is the stable, protocol-neutral classification of a gateway
// failure. Protocol adapters map these codes to wire status and must never
// inspect storage or provider error values directly.
type ErrorCode string

const (
	// ErrorInvalidInput reports a semantically invalid request.
	ErrorInvalidInput ErrorCode = "invalid_input"
	// ErrorNotFound reports that a requested object does not exist.
	ErrorNotFound ErrorCode = "not_found"
	// ErrorConflict reports that a request conflicts with existing state.
	ErrorConflict ErrorCode = "conflict"
	// ErrorIdempotencyConflict reports that an idempotency key was reused with a
	// different command.
	ErrorIdempotencyConflict ErrorCode = "idempotency_conflict"
	// ErrorPermissionDenied reports that policy denied the operation.
	ErrorPermissionDenied ErrorCode = "permission_denied"
	// ErrorResourceExhausted reports a bounded resource limit such as a full job
	// queue or exhausted disk budget.
	ErrorResourceExhausted ErrorCode = "resource_exhausted"
	// ErrorCapabilityUnavailable reports that a required capability or index is
	// temporarily unavailable.
	ErrorCapabilityUnavailable ErrorCode = "capability_unavailable"
	// ErrorInternal reports an unexpected internal failure.
	ErrorInternal ErrorCode = "internal"
)

// Valid reports whether the code is one of the defined gateway error codes.
func (c ErrorCode) Valid() bool {
	switch c {
	case ErrorInvalidInput, ErrorNotFound, ErrorConflict, ErrorIdempotencyConflict,
		ErrorPermissionDenied, ErrorResourceExhausted, ErrorCapabilityUnavailable, ErrorInternal:
		return true
	default:
		return false
	}
}

// Error is a gateway error. Message and Details are safe to expose to a caller;
// the wrapped cause is internal and must not be serialized.
type Error struct {
	Code       ErrorCode
	Message    string
	Retryable  bool
	RetryAfter *time.Duration
	Details    map[string]string

	cause error
}

// NewError creates a gateway error with retryability derived from the code.
func NewError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message, Retryable: defaultRetryable(code)}
}

// WrapError creates a gateway error that wraps an internal cause. The cause is
// retained for logging and errors.Is/As but is never part of the public message.
func WrapError(code ErrorCode, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Retryable: defaultRetryable(code), cause: cause}
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the internal cause, if any.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// WithDetails returns a copy of the error carrying safe, string-valued details.
func (e *Error) WithDetails(details map[string]string) *Error {
	clone := *e
	clone.Details = details
	return &clone
}

// WithRetryable returns a copy of the error with the given retryability.
func (e *Error) WithRetryable(retryable bool) *Error {
	clone := *e
	clone.Retryable = retryable
	return &clone
}

// WithRetryAfter returns a copy of the error carrying a suggested retry delay.
func (e *Error) WithRetryAfter(delay time.Duration) *Error {
	clone := *e
	clone.RetryAfter = &delay
	return &clone
}

// CodeOf returns the gateway error code of err, or ErrorInternal when err is not
// a gateway error.
func CodeOf(err error) ErrorCode {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ErrorInternal
}

// IsCode reports whether err is a gateway error with the given code.
func IsCode(err error, code ErrorCode) bool {
	return CodeOf(err) == code
}

// IsRetryable reports whether err is a gateway error marked retryable.
func IsRetryable(err error) bool {
	var target *Error
	if errors.As(err, &target) {
		return target.Retryable
	}
	return false
}

// RetryAfter returns the suggested retry delay carried by err, if any.
func RetryAfter(err error) (time.Duration, bool) {
	var target *Error
	if errors.As(err, &target) && target.RetryAfter != nil {
		return *target.RetryAfter, true
	}
	return 0, false
}

// DetailsOf returns a copy of the safe public details carried by err.
func DetailsOf(err error) map[string]string {
	var target *Error
	if !errors.As(err, &target) || len(target.Details) == 0 {
		return nil
	}
	details := make(map[string]string, len(target.Details))
	for key, value := range target.Details {
		details[key] = value
	}
	return details
}

func defaultRetryable(code ErrorCode) bool {
	switch code {
	case ErrorResourceExhausted, ErrorCapabilityUnavailable, ErrorInternal:
		return true
	default:
		return false
	}
}
