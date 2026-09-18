package httpapi

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/feedback"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	"github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
)

// frozenRoutes is the exact contract surface. Any addition or change must be
// reflected in docs/api/http-rest-contract.md and in this expectation together.
var frozenRoutes = []RouteInfo{
	{Operation: memory.OperationRemember, Method: http.MethodPost, Path: "/v1/memories", Public: false, Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxMemoryBodyBytes},
	{Operation: OperationMemoryGet, Method: http.MethodGet, Path: "/v1/memories/{memory_id}", Public: false, Access: AccessRead, Idempotency: IdempotencyNotRequired, MaxBodyBytes: 0},
	{Operation: memory.OperationForget, Method: http.MethodPost, Path: "/v1/memories/{memory_id}/forget", Public: false, Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxForgetBodyBytes},
	{Operation: feedback.OperationAppend, Method: http.MethodPost, Path: "/v1/memories/{memory_id}/feedback", Public: false, Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxFeedbackBodyBytes},
	{Operation: retrieval.OperationRecall, Method: http.MethodPost, Path: "/v1/recall", Public: false, Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxRecallBodyBytes},
	{Operation: extraction.OperationIngest, Method: http.MethodPost, Path: "/v1/events", Public: false, Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxEventBodyBytes},
	{Operation: OperationEventGet, Method: http.MethodGet, Path: "/v1/events/{event_id}", Public: false, Access: AccessRead, Idempotency: IdempotencyNotRequired, MaxBodyBytes: 0},
	{Operation: scope.OperationCreate, Method: http.MethodPost, Path: "/v1/scopes", Public: false, Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxScopeBodyBytes},
	{Operation: OperationScopeGet, Method: http.MethodGet, Path: "/v1/scopes/{scope_id}", Public: false, Access: AccessRead, Idempotency: IdempotencyNotRequired, MaxBodyBytes: 0},
}

func TestRoutesMatchFrozenContract(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	if got := handler.Routes(); !reflect.DeepEqual(got, frozenRoutes) {
		t.Errorf("Routes() mismatch\n got: %+v\nwant: %+v", got, frozenRoutes)
	}
}

func TestRouteMetadataInvariants(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	seen := map[string]bool{}
	for _, route := range handler.Routes() {
		key := route.Method + " " + route.Path
		if seen[key] {
			t.Errorf("duplicate route %s", key)
		}
		seen[key] = true

		if route.Operation == "" {
			t.Errorf("%s: operation name is empty", key)
		}
		if route.Method != http.MethodGet && route.Method != http.MethodPost {
			t.Errorf("%s: unexpected method %q", key, route.Method)
		}
		if !strings.HasPrefix(route.Path, "/v1/") {
			t.Errorf("%s: path must be under /v1", key)
		}
		switch route.Access {
		case AccessWrite:
			if route.Idempotency != IdempotencyRequired {
				t.Errorf("%s: every write must require an idempotency key", key)
			}
			if route.MaxBodyBytes <= 0 {
				t.Errorf("%s: every write must declare a body limit", key)
			}
		case AccessRead:
			if route.Idempotency != IdempotencyNotRequired {
				t.Errorf("%s: reads must not require an idempotency key", key)
			}
			if route.MaxBodyBytes != 0 {
				t.Errorf("%s: reads must not declare a body limit", key)
			}
		default:
			t.Errorf("%s: unknown access mode %q", key, route.Access)
		}
	}
}

func TestNoAdministrativeRoutesAreExposed(t *testing.T) {
	forbidden := []string{
		"candidate", "review", "job", "rebuild", "index", "config",
		"graph", "admin", "maintenance", "reload", "replay",
	}
	handler := newTestHandler(t, newTestGateway())
	for _, route := range handler.Routes() {
		path := strings.ToLower(route.Path)
		for _, fragment := range forbidden {
			if strings.Contains(path, fragment) {
				t.Errorf("route %s %s exposes administrative fragment %q", route.Method, route.Path, fragment)
			}
		}
	}
}

// TestNonPublicRoutesRequireCaller proves the caller requirement behaviorally:
// every non-public route rejects a request without caller identity before any
// gateway call.
func TestNonPublicRoutesRequireCaller(t *testing.T) {
	for _, route := range frozenRoutes {
		if route.Public {
			continue
		}
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			fake := newTestGateway()
			handler := newTestHandler(t, fake)
			recorder := doRequest(t, handler, route.Method, requestPath(route.Path), "", nil)
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", recorder.Code, recorder.Body.String())
			}
			if body := decodeErrorResponse(t, recorder); body.Code != codeInvalidRequest {
				t.Errorf("code = %q, want %q", body.Code, codeInvalidRequest)
			}
			if fake.CallCount() != 0 {
				t.Errorf("gateway was called %d times without caller identity", fake.CallCount())
			}
		})
	}
}

// TestWriteRoutesRequireIdempotency proves the idempotency requirement
// behaviorally: every write route rejects a caller-identified request that has
// no idempotency key.
func TestWriteRoutesRequireIdempotency(t *testing.T) {
	for _, route := range frozenRoutes {
		if route.Access != AccessWrite {
			continue
		}
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			fake := newTestGateway()
			handler := newTestHandler(t, fake)
			recorder := doRequest(t, handler, route.Method, requestPath(route.Path), "", callerHeaders())
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", recorder.Code, recorder.Body.String())
			}
			if body := decodeErrorResponse(t, recorder); body.Code != codeInvalidRequest {
				t.Errorf("code = %q, want %q", body.Code, codeInvalidRequest)
			}
			if fake.CallCount() != 0 {
				t.Errorf("gateway was called %d times without an idempotency key", fake.CallCount())
			}
		})
	}
}

// TestReadRoutesDoNotRequireIdempotency proves reads are reachable with caller
// identity alone.
func TestReadRoutesDoNotRequireIdempotency(t *testing.T) {
	for _, route := range frozenRoutes {
		if route.Access != AccessRead {
			continue
		}
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			handler := newTestHandler(t, newTestGateway())
			recorder := doRequest(t, handler, route.Method, requestPath(route.Path), "", callerHeaders())
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// TestPublicRoutePolicySkipsCallerRequirement proves the metadata flag is
// honored even though the frozen surface currently has no public route.
func TestPublicRoutePolicySkipsCallerRequirement(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	publicRoute := &route{RouteInfo: RouteInfo{
		Operation:   "health.check",
		Method:      http.MethodGet,
		Path:        "/v1/health",
		Public:      true,
		Access:      AccessRead,
		Idempotency: IdempotencyNotRequired,
	}}
	request := newRequest(t, http.MethodGet, "/v1/health", "", nil)
	call, err := handler.buildCall(request, publicRoute, "req-1")
	if err != nil {
		t.Fatalf("buildCall() error = %v", err)
	}
	if call.HasCaller() {
		t.Errorf("public route unexpectedly required caller identity")
	}
}

// requestPath substitutes concrete identifiers into a route pattern.
func requestPath(pattern string) string {
	replacer := strings.NewReplacer("{memory_id}", "mem-1", "{event_id}", "evt-1", "{scope_id}", "scope-1")
	return replacer.Replace(pattern)
}
