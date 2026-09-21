package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

type failingTools struct{ err error }

func (f failingTools) Recall(context.Context, gateway.CallContext, agenttool.RecallCommand) (agenttool.RecallResult, error) {
	return agenttool.RecallResult{}, f.err
}
func (f failingTools) MemoryCommit(context.Context, gateway.CallContext, agenttool.MemoryCommitCommand) (agenttool.MemoryCommitResult, error) {
	return agenttool.MemoryCommitResult{}, f.err
}
func (f failingTools) Feedback(context.Context, gateway.CallContext, agenttool.FeedbackCommand) (agenttool.FeedbackResult, error) {
	return agenttool.FeedbackResult{}, f.err
}
func (f failingTools) Forget(context.Context, gateway.CallContext, agenttool.ForgetCommand) (agenttool.ForgetResult, error) {
	return agenttool.ForgetResult{}, f.err
}

type stubScopes struct{}

func (stubScopes) Ensure(context.Context, gateway.CallContext, scope.EnsureCommand) (scope.EnsureResult, error) {
	return scope.EnsureResult{}, nil
}
func (stubScopes) Get(context.Context, gateway.CallContext, scope.GetQuery) (domain.Space, error) {
	return domain.Space{}, nil
}

func errorHandler(t *testing.T, err error) *Handler {
	t.Helper()
	handler, newErr := New(Dependencies{AgentTools: failingTools{err: err}, Scopes: stubScopes{}})
	if newErr != nil {
		t.Fatal(newErr)
	}
	return handler
}

func TestGatewayErrorsMapToWireStatusAndBody(t *testing.T) {
	retryAfter := 2 * time.Second
	cases := []struct {
		name      string
		err       error
		status    int
		code      string
		retryable bool
	}{
		{"invalid_input", gateway.NewError(gateway.ErrorInvalidInput, "bad input"), http.StatusUnprocessableEntity, "invalid_input", false},
		{"not_found", gateway.NewError(gateway.ErrorNotFound, "missing"), http.StatusNotFound, "not_found", false},
		{"conflict", gateway.NewError(gateway.ErrorConflict, "conflict"), http.StatusConflict, "conflict", false},
		{"idempotency_conflict", gateway.NewError(gateway.ErrorIdempotencyConflict, "duplicate key"), http.StatusConflict, "idempotency_conflict", false},
		{"permission_denied", gateway.NewError(gateway.ErrorPermissionDenied, "denied"), http.StatusForbidden, "permission_denied", false},
		{"throttled", &gateway.Error{Code: gateway.ErrorResourceExhausted, Message: "slow down", Retryable: true, RetryAfter: &retryAfter}, http.StatusTooManyRequests, "resource_exhausted", true},
		{"exhausted", gateway.NewError(gateway.ErrorResourceExhausted, "full").WithRetryable(false), http.StatusInsufficientStorage, "resource_exhausted", false},
		{"unavailable", gateway.NewError(gateway.ErrorCapabilityUnavailable, "offline"), http.StatusServiceUnavailable, "capability_unavailable", true},
		{"internal", gateway.NewError(gateway.ErrorInternal, "internal"), http.StatusInternalServerError, "internal", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			handler := errorHandler(t, test.err)
			response := request(t, handler, http.MethodPost, "/v1/recall", `{"query":"q","scope_ids":["s"]}`, caller())
			if response.Code != test.status {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var body toolcontract.ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != test.code || body.Error.Retryable != test.retryable {
				t.Fatalf("error = %+v", body.Error)
			}
			if body.Error.RequestID == "" || response.Header().Get(requestIDHeader) == "" {
				t.Fatalf("request id missing: %+v", body.Error)
			}
			if test.name == "throttled" && (body.Error.RetryAfterMS == nil || *body.Error.RetryAfterMS != 2000) {
				t.Fatalf("retry_after_ms = %v", body.Error.RetryAfterMS)
			}
			if err := toolcontract.Validate(toolcontract.SchemaError, response.Body.Bytes()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNonGatewayErrorIsGenericAndDoesNotEchoCause(t *testing.T) {
	handler := errorHandler(t, errors.New("secret internal detail"))
	response := request(t, handler, http.MethodPost, "/v1/recall", `{"query":"q","scope_ids":["s"]}`, caller())
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); body == "" || contains(body, "secret internal detail") {
		t.Fatalf("internal cause leaked: %s", body)
	}
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
