package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	"github.com/N0rthHXD/dMem/internal/gateway/retrieval"
)

func TestGatewayErrorStatusMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid_input", gateway.NewError(gateway.ErrorInvalidInput, "bad input"), http.StatusUnprocessableEntity, "invalid_input"},
		{"not_found", gateway.NewError(gateway.ErrorNotFound, "missing"), http.StatusNotFound, "not_found"},
		{"conflict", gateway.NewError(gateway.ErrorConflict, "conflict"), http.StatusConflict, "conflict"},
		{"idempotency_conflict", gateway.NewError(gateway.ErrorIdempotencyConflict, "reused"), http.StatusConflict, "idempotency_conflict"},
		{"permission_denied", gateway.NewError(gateway.ErrorPermissionDenied, "denied"), http.StatusForbidden, "permission_denied"},
		{"capability_unavailable", gateway.NewError(gateway.ErrorCapabilityUnavailable, "unavailable"), http.StatusServiceUnavailable, "capability_unavailable"},
		{"resource_exhausted_retryable", gateway.NewError(gateway.ErrorResourceExhausted, "busy"), http.StatusTooManyRequests, "resource_exhausted"},
		{"resource_exhausted_terminal", gateway.NewError(gateway.ErrorResourceExhausted, "full").WithRetryable(false), http.StatusInsufficientStorage, "resource_exhausted"},
		{"internal", gateway.NewError(gateway.ErrorInternal, "failed"), http.StatusInternalServerError, "internal"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newTestGateway()
			fake.memory.getFunc = func(context.Context, gateway.CallContext, memory.GetQuery) (domain.Memory, error) {
				return domain.Memory{}, testCase.err
			}
			handler := newTestHandler(t, fake)
			recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/mem-1", "", callerHeaders())
			assertErrorCode(t, recorder, testCase.status, testCase.code)
		})
	}
}

func TestGatewayRetryMetadataIsPublished(t *testing.T) {
	fake := newTestGateway()
	fake.memory.getFunc = func(context.Context, gateway.CallContext, memory.GetQuery) (domain.Memory, error) {
		return domain.Memory{}, gateway.NewError(gateway.ErrorCapabilityUnavailable, "unavailable").WithRetryAfter(2 * time.Second)
	}
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/mem-1", "", callerHeaders())
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	body := decodeErrorResponse(t, recorder)
	if !body.Retryable {
		t.Errorf("retryable = false, want true")
	}
	if body.RetryAfterMS == nil || *body.RetryAfterMS != 2000 {
		t.Errorf("retry_after_ms = %v, want 2000", body.RetryAfterMS)
	}
}

func TestGatewayErrorDoesNotLeakInternalCause(t *testing.T) {
	const secret = "postgres://user:password@internal-host"
	fake := newTestGateway()
	fake.memory.getFunc = func(context.Context, gateway.CallContext, memory.GetQuery) (domain.Memory, error) {
		return domain.Memory{}, gateway.WrapError(gateway.ErrorInternal, "memory operation failed", errors.New(secret))
	}
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/mem-1", "", callerHeaders())
	assertErrorCode(t, recorder, http.StatusInternalServerError, "internal")
	assertBodyOmits(t, recorder, secret)
}

func TestBareErrorIsReportedGenerically(t *testing.T) {
	const secret = "sqlite: disk I/O error at /var/lib/dmem/secret.db"
	fake := newTestGateway()
	fake.memory.getFunc = func(context.Context, gateway.CallContext, memory.GetQuery) (domain.Memory, error) {
		return domain.Memory{}, errors.New(secret)
	}
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/mem-1", "", callerHeaders())
	assertErrorCode(t, recorder, http.StatusInternalServerError, "internal")
	assertBodyOmits(t, recorder, secret)
	if body := decodeErrorResponse(t, recorder); body.Message != "internal error" {
		t.Errorf("message = %q, want generic internal error", body.Message)
	}
}

func TestPanicIsRecoveredSafely(t *testing.T) {
	const secret = "panic: secret runtime state"
	fake := newTestGateway()
	fake.memory.getFunc = func(context.Context, gateway.CallContext, memory.GetQuery) (domain.Memory, error) {
		panic(secret)
	}
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/mem-1", "", callerHeaders())
	assertErrorCode(t, recorder, http.StatusInternalServerError, "internal")
	assertBodyOmits(t, recorder, secret)
	if recorder.Header().Get(requestIDHeader) == "" {
		t.Errorf("panic response is missing a request id")
	}
}

func TestCanceledRequestIsClassified(t *testing.T) {
	fake := newTestGateway()
	fake.memory.getFunc = func(ctx context.Context, _ gateway.CallContext, _ memory.GetQuery) (domain.Memory, error) {
		<-ctx.Done()
		return domain.Memory{}, ctx.Err()
	}
	handler := newTestHandler(t, fake)
	request := newRequest(t, http.MethodGet, "/v1/memories/mem-1", "", callerHeaders())
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	recorder := serve(handler, request.WithContext(ctx))
	assertErrorCode(t, recorder, 499, codeCanceled)
}

func TestDeadlineExceededIsClassified(t *testing.T) {
	fake := newTestGateway()
	fake.memory.getFunc = func(ctx context.Context, _ gateway.CallContext, _ memory.GetQuery) (domain.Memory, error) {
		<-ctx.Done()
		return domain.Memory{}, ctx.Err()
	}
	handler := newTestHandler(t, fake)
	request := newRequest(t, http.MethodGet, "/v1/memories/mem-1", "", callerHeaders())
	ctx, cancel := context.WithDeadline(request.Context(), time.Now().Add(-time.Second))
	defer cancel()
	recorder := serve(handler, request.WithContext(ctx))
	assertErrorCode(t, recorder, http.StatusGatewayTimeout, codeDeadlineExceeded)
}

func TestRecallCancellationPropagates(t *testing.T) {
	fake := newTestGateway()
	fake.recall.recallFunc = func(ctx context.Context, _ gateway.CallContext, _ retrieval.Query) (retrieval.Result, error) {
		<-ctx.Done()
		return retrieval.Result{}, ctx.Err()
	}
	handler := newTestHandler(t, fake)
	request := newRequest(t, http.MethodPost, "/v1/recall", `{"query":"q","scope_ids":["s"]}`, writeHeaders())
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	recorder := serve(handler, request.WithContext(ctx))
	assertErrorCode(t, recorder, 499, codeCanceled)
}

func TestRequestIDIsEchoedWhenValid(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	headers := callerHeaders()
	headers[requestIDHeader] = "req-echo-1"
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/mem-1", "", headers)
	if got := recorder.Header().Get(requestIDHeader); got != "req-echo-1" {
		t.Errorf("request id = %q, want req-echo-1", got)
	}
}

func TestRequestIDIsGeneratedWhenAbsent(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/mem-1", "", callerHeaders())
	if got := recorder.Header().Get(requestIDHeader); len(got) != 36 {
		t.Errorf("generated request id = %q, want UUID shape", got)
	}
}

func TestInvalidRequestIDIsRejected(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	headers := callerHeaders()
	headers[requestIDHeader] = "bad request id"
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/mem-1", "", headers)
	assertErrorCode(t, recorder, http.StatusUnprocessableEntity, codeInvalidRequest)
	if got := recorder.Header().Get(requestIDHeader); got == "bad request id" || got == "" {
		t.Errorf("response request id = %q, want a generated identifier", got)
	}
}

func TestInvalidProtocolHeadersAreRejected(t *testing.T) {
	cases := []struct {
		name   string
		header string
		value  string
		method string
		target string
		body   string
	}{
		{"correlation", correlationHeader, "bad correlation", http.MethodGet, "/v1/memories/mem-1", ""},
		{"caller subject", callerSubjectHeader, "bad subject", http.MethodGet, "/v1/memories/mem-1", ""},
		{"caller client", callerClientHeader, "bad client", http.MethodGet, "/v1/memories/mem-1", ""},
		{"idempotency", idempotencyHeader, "bad key", http.MethodPost, "/v1/memories", `{"scope_id":"s","content":"c"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			handler := newTestHandler(t, newTestGateway())
			headers := writeHeaders()
			headers[testCase.header] = testCase.value
			recorder := doRequest(t, handler, testCase.method, testCase.target, testCase.body, headers)
			assertErrorCode(t, recorder, http.StatusUnprocessableEntity, codeInvalidRequest)
		})
	}
}

func TestPathIdentifierBounds(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	long := strings.Repeat("a", 129)
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/"+long, "", callerHeaders())
	assertErrorCode(t, recorder, http.StatusUnprocessableEntity, codeInvalidRequest)
}

func TestUnknownRouteIsNotFound(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	recorder := doRequest(t, handler, http.MethodGet, "/v1/unknown", "", callerHeaders())
	assertErrorCode(t, recorder, http.StatusNotFound, codeNotFound)
}

func TestTrailingSlashDoesNotMatch(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/", "", callerHeaders())
	assertErrorCode(t, recorder, http.StatusNotFound, codeNotFound)
}

func TestMethodNotAllowedReportsAllow(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	recorder := doRequest(t, handler, http.MethodDelete, "/v1/memories/mem-1", "", callerHeaders())
	assertErrorCode(t, recorder, http.StatusMethodNotAllowed, codeMethodNotAllowed)
	if got := recorder.Header().Get("Allow"); got != http.MethodGet {
		t.Errorf("Allow = %q, want GET", got)
	}
}

func TestParsePageRequest(t *testing.T) {
	request, err := ParsePageRequest(url.Values{})
	if err != nil || request.Limit != defaultPageLimit || request.Cursor != "" {
		t.Errorf("defaults = %+v, err = %v", request, err)
	}
	request, err = ParsePageRequest(url.Values{"limit": {"25"}, "cursor": {"abc"}})
	if err != nil || request.Limit != 25 || request.Cursor != "abc" {
		t.Errorf("explicit = %+v, err = %v", request, err)
	}
	for _, raw := range []string{"0", "201", "-1", "abc"} {
		if _, err := ParsePageRequest(url.Values{"limit": {raw}}); err == nil {
			t.Errorf("limit %q was accepted", raw)
		}
	}
	if _, err := ParsePageRequest(url.Values{"cursor": {strings.Repeat("c", 513)}}); err == nil {
		t.Errorf("oversized cursor was accepted")
	}
}

func TestDependencyValidation(t *testing.T) {
	fake := newTestGateway()
	cases := []struct {
		name string
		deps Dependencies
	}{
		{"memory", Dependencies{Recall: fake.recall, Feedback: fake.feedback, Events: fake.events, Scopes: fake.scopes}},
		{"recall", Dependencies{Memory: fake.memory, Feedback: fake.feedback, Events: fake.events, Scopes: fake.scopes}},
		{"feedback", Dependencies{Memory: fake.memory, Recall: fake.recall, Events: fake.events, Scopes: fake.scopes}},
		{"events", Dependencies{Memory: fake.memory, Recall: fake.recall, Feedback: fake.feedback, Scopes: fake.scopes}},
		{"scopes", Dependencies{Memory: fake.memory, Recall: fake.recall, Feedback: fake.feedback, Events: fake.events}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := New(testCase.deps); err == nil {
				t.Fatalf("New() accepted a missing %s dependency", testCase.name)
			}
		})
	}
}

// assertBodyOmits fails when the response body contains secret.
func assertBodyOmits(t *testing.T, recorder *httptest.ResponseRecorder, secret string) {
	t.Helper()
	if strings.Contains(recorder.Body.String(), secret) {
		t.Errorf("response body leaked internal detail %q: %s", secret, recorder.Body.String())
	}
}
