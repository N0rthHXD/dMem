package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

// Client-local error codes. Server responses keep their own stable wire codes;
// these cover failures detected before a dMem response exists.
const (
	// CodeInvalidInput reports a request rejected against the tool contract
	// before it reached dMem.
	CodeInvalidInput = "invalid_input"
	// CodeCapabilityUnavailable reports an unreachable or unavailable dMem
	// application endpoint.
	CodeCapabilityUnavailable = "capability_unavailable"
	// CodeCanceled reports caller cancellation.
	CodeCanceled = "canceled"
	// CodeDeadlineExceeded reports an expired caller deadline.
	CodeDeadlineExceeded = "deadline_exceeded"
	// CodeInternal reports a malformed or unreadable dMem response.
	CodeInternal = "internal"
)

// Error is a structured dMem application error. It preserves the stable wire
// code, retryability, retry hint, and safe string details so callers branch on
// data instead of parsing messages.
type Error struct {
	// Code is the stable error code.
	Code string
	// Message is a human-readable, non-stable description.
	Message string
	// RequestID echoes the application request identity when dMem supplied one.
	RequestID string
	// Retryable reports whether the same request may be retried.
	Retryable bool
	// RetryAfter is a server-provided retry hint.
	RetryAfter time.Duration
	// Details carries stable string details.
	Details map[string]string
	// Status is the HTTP status code, or zero when no response was received.
	Status int
	cause  error
}

// Error implements the error interface and never includes a request body,
// memory content, or query text.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Status != 0 {
		return fmt.Sprintf("client: dMem error %s (status %d): %s", e.Code, e.Status, e.Message)
	}
	return fmt.Sprintf("client: dMem error %s: %s", e.Code, e.Message)
}

// Unwrap exposes the underlying transport or context cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// CodeOf returns the stable error code of any error, defaulting to internal.
func CodeOf(err error) string {
	var clientErr *Error
	if errors.As(err, &clientErr) {
		return clientErr.Code
	}
	return CodeInternal
}

// IsRetryable reports whether err is a retryable structured dMem error.
func IsRetryable(err error) bool {
	var clientErr *Error
	if errors.As(err, &clientErr) {
		return clientErr.Retryable
	}
	return false
}

// ToContractError converts any error into the wire error shape so MCP and other
// adapters can expose dMem structured errors verbatim. Unknown errors collapse
// to a generic internal error, so no internal cause is ever serialized.
func ToContractError(err error, requestID string) toolcontract.ContractError {
	var clientErr *Error
	if errors.As(err, &clientErr) {
		result := toolcontract.ContractError{
			Code:      clientErr.Code,
			Message:   clientErr.Message,
			RequestID: clientErr.RequestID,
			Retryable: clientErr.Retryable,
			Details:   clientErr.Details,
		}
		if result.RequestID == "" {
			result.RequestID = requestID
		}
		if result.Message == "" {
			result.Message = "dMem request failed"
		}
		if clientErr.RetryAfter > 0 {
			millis := int64(clientErr.RetryAfter / time.Millisecond)
			if millis <= 0 {
				millis = 1
			}
			result.RetryAfterMS = &millis
		}
		return result
	}
	return toolcontract.ContractError{Code: CodeInternal, Message: "dMem request failed", RequestID: requestID}
}

func localError(message string) *Error {
	return &Error{Code: CodeInvalidInput, Message: message}
}

func contextError(err error) *Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return &Error{Code: CodeCanceled, Message: "request canceled", cause: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Code: CodeDeadlineExceeded, Message: "request deadline exceeded", Retryable: true, cause: err}
	default:
		return &Error{Code: CodeInternal, Message: "request failed", cause: err}
	}
}

// parseError maps a non-2xx response to a structured error. It prefers the
// contract error body and otherwise falls back to the HTTP status class.
func parseError(status int, body []byte) *Error {
	var response toolcontract.ErrorResponse
	if err := json.Unmarshal(body, &response); err == nil && response.Error.Code != "" {
		result := &Error{
			Code:      response.Error.Code,
			Message:   response.Error.Message,
			RequestID: response.Error.RequestID,
			Retryable: response.Error.Retryable,
			Details:   response.Error.Details,
			Status:    status,
		}
		if response.Error.RetryAfterMS != nil && *response.Error.RetryAfterMS > 0 {
			result.RetryAfter = time.Duration(*response.Error.RetryAfterMS) * time.Millisecond
		}
		return result
	}
	return &Error{
		Code:      fallbackCode(status),
		Message:   fallbackMessage(status),
		Retryable: status == http.StatusTooManyRequests || status >= 500,
		Status:    status,
	}
}

func fallbackCode(status int) string {
	switch {
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusConflict:
		return "conflict"
	case status == http.StatusForbidden:
		return "permission_denied"
	case status == http.StatusServiceUnavailable:
		return CodeCapabilityUnavailable
	default:
		return CodeInternal
	}
}

func fallbackMessage(status int) string {
	if text := http.StatusText(status); text != "" {
		return "dMem returned " + text
	}
	return fmt.Sprintf("dMem returned status %d", status)
}
