package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

// Adapter-level wire error codes. Gateway error codes are the gateway.ErrorCode
// values and are mapped separately.
const (
	codeInvalidJSON          = "invalid_json"
	codeUnexpectedBody       = "unexpected_body"
	codeInvalidRequest       = "invalid_request"
	codeRequestTooLarge      = "request_too_large"
	codeUnsupportedMediaType = "unsupported_media_type"
	codeMethodNotAllowed     = "method_not_allowed"
	codeNotFound             = "not_found"
	codeCanceled             = "canceled"
	codeDeadlineExceeded     = "deadline_exceeded"
	codeInternal             = "internal"
)

// requestError is an adapter-level failure detected before or around a gateway
// call. Its message is safe to expose.
type requestError struct {
	status  int
	code    string
	message string
	details map[string]string
}

// Error implements the error interface.
func (e *requestError) Error() string {
	return e.code + ": " + e.message
}

// responseWriter tracks whether a response has started so panic recovery and
// error writing never produce a second header or body.
type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

// WriteHeader records and forwards the first status line.
func (w *responseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write starts an implicit 200 response and forwards the body.
func (w *responseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// Written reports whether the response has started.
func (w *responseWriter) Written() bool {
	return w.wroteHeader
}

// writeJSON serializes value before writing any header so a serialization
// failure can still produce a clean safe error.
func (ex *exchange) writeJSON(status int, value any) {
	if ex.writer.Written() {
		return
	}
	body, err := json.Marshal(value)
	if err != nil {
		ex.writeSafeError(http.StatusInternalServerError, codeInternal, "internal error", false, nil, nil)
		return
	}
	ex.writer.Header().Set("Content-Type", "application/json")
	ex.writer.WriteHeader(status)
	_, _ = ex.writer.Write(body)
}

// writeError maps an error to the frozen wire error model. A gateway error
// contributes only its declared-safe fields; any other error is reported
// generically so an internal cause is never echoed.
func (ex *exchange) writeError(err error) {
	if err == nil {
		return
	}
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) {
		status := httpStatusForCode(gatewayErr.Code, gatewayErr.Retryable)
		retryAfter := retryAfterMillis(gatewayErr)
		ex.writeSafeError(status, string(gatewayErr.Code), gatewayErr.Message, gatewayErr.Retryable, retryAfter, gatewayErr.Details)
		return
	}
	var adapterErr *requestError
	if errors.As(err, &adapterErr) {
		ex.writeSafeError(adapterErr.status, adapterErr.code, adapterErr.message, false, nil, adapterErr.details)
		return
	}
	switch {
	case errors.Is(err, context.Canceled):
		ex.writeSafeError(499, codeCanceled, "request canceled", false, nil, nil)
	case errors.Is(err, context.DeadlineExceeded):
		ex.writeSafeError(http.StatusGatewayTimeout, codeDeadlineExceeded, "request deadline exceeded", false, nil, nil)
	default:
		ex.writeSafeError(http.StatusInternalServerError, codeInternal, "internal error", false, nil, nil)
	}
}

// writeSafeError emits one error response. It is a no-op when the response has
// already started.
func (ex *exchange) writeSafeError(status int, code, message string, retryable bool, retryAfter *int64, details map[string]string) {
	if ex.writer.Written() {
		return
	}
	body, err := json.Marshal(toolcontract.ErrorResponse{Error: toolcontract.ContractError{
		Code:         code,
		Message:      message,
		RequestID:    ex.requestID,
		Retryable:    retryable,
		RetryAfterMS: retryAfter,
		Details:      details,
	}})
	if err != nil {
		body = []byte(`{"error":{"code":"internal","message":"internal error","request_id":"","retryable":false}}`)
		status = http.StatusInternalServerError
	}
	ex.writer.Header().Set("Content-Type", "application/json")
	ex.writer.WriteHeader(status)
	_, _ = ex.writer.Write(body)
}

// httpStatusForCode maps a stable gateway error code to a wire status. The
// retryability flag distinguishes a throttling limit from an exhausted resource
// budget.
func httpStatusForCode(code gateway.ErrorCode, retryable bool) int {
	switch code {
	case gateway.ErrorInvalidInput:
		return http.StatusUnprocessableEntity
	case gateway.ErrorNotFound:
		return http.StatusNotFound
	case gateway.ErrorConflict, gateway.ErrorIdempotencyConflict:
		return http.StatusConflict
	case gateway.ErrorPermissionDenied:
		return http.StatusForbidden
	case gateway.ErrorResourceExhausted:
		if retryable {
			return http.StatusTooManyRequests
		}
		return http.StatusInsufficientStorage
	case gateway.ErrorCapabilityUnavailable:
		return http.StatusServiceUnavailable
	case gateway.ErrorInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// retryAfterMillis converts a gateway retry hint into whole milliseconds.
func retryAfterMillis(gatewayErr *gateway.Error) *int64 {
	if gatewayErr == nil || gatewayErr.RetryAfter == nil {
		return nil
	}
	value := int64(*gatewayErr.RetryAfter / time.Millisecond)
	if value <= 0 {
		value = 1
	}
	return &value
}
