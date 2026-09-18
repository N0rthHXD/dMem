package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/feedback"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	"github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
)

// recordedCall is one gateway invocation captured by a fake service.
type recordedCall struct {
	operation string
	call      gateway.CallContext
	command   any
}

// callRecorder is the shared, concurrency-safe invocation log of a test
// gateway.
type callRecorder struct {
	mu    sync.Mutex
	calls []recordedCall
}

func (r *callRecorder) record(operation string, call gateway.CallContext, command any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedCall{operation: operation, call: call, command: command})
}

// Calls returns a copy of the recorded invocations.
func (r *callRecorder) Calls() []recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedCall(nil), r.calls...)
}

// CallCount returns the number of recorded invocations.
func (r *callRecorder) CallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// fakeMemoryService implements MemoryService.
type fakeMemoryService struct {
	recorder *callRecorder

	rememberFunc func(context.Context, gateway.CallContext, memory.RememberCommand) (memory.RememberResult, error)
	getFunc      func(context.Context, gateway.CallContext, memory.GetQuery) (domain.Memory, error)
	forgetFunc   func(context.Context, gateway.CallContext, memory.ForgetCommand) (memory.ForgetResult, error)
}

func (f *fakeMemoryService) Remember(ctx context.Context, call gateway.CallContext, command memory.RememberCommand) (memory.RememberResult, error) {
	f.recorder.record("memory.remember", call, command)
	if f.rememberFunc != nil {
		return f.rememberFunc(ctx, call, command)
	}
	return memory.RememberResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID},
		Memory: domain.Memory{
			MemoryID:     "mem-1",
			SpaceID:      command.SpaceID,
			Kind:         domain.MemoryKindFact,
			CurrentState: "active",
			CreatedAt:    "2024-01-01T00:00:00Z",
			UpdatedAt:    "2024-01-01T00:00:00Z",
		},
		EventID: "evt-1",
	}, nil
}

func (f *fakeMemoryService) Get(ctx context.Context, call gateway.CallContext, query memory.GetQuery) (domain.Memory, error) {
	f.recorder.record("memory.get", call, query)
	if f.getFunc != nil {
		return f.getFunc(ctx, call, query)
	}
	return domain.Memory{MemoryID: query.MemoryID, SpaceID: "scope-1", Kind: domain.MemoryKindFact, CurrentState: "active"}, nil
}

func (f *fakeMemoryService) Forget(ctx context.Context, call gateway.CallContext, command memory.ForgetCommand) (memory.ForgetResult, error) {
	f.recorder.record("memory.forget", call, command)
	if f.forgetFunc != nil {
		return f.forgetFunc(ctx, call, command)
	}
	return memory.ForgetResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID},
		Memory:      domain.Memory{MemoryID: command.MemoryID, SpaceID: "scope-1", CurrentState: "forgotten"},
	}, nil
}

// fakeRecallService implements RecallService.
type fakeRecallService struct {
	recorder   *callRecorder
	recallFunc func(context.Context, gateway.CallContext, retrieval.Query) (retrieval.Result, error)
}

func (f *fakeRecallService) Recall(ctx context.Context, call gateway.CallContext, query retrieval.Query) (retrieval.Result, error) {
	f.recorder.record("retrieval.recall", call, query)
	if f.recallFunc != nil {
		return f.recallFunc(ctx, call, query)
	}
	return retrieval.Result{
		Context:   domain.ContextPackage{Items: []domain.ContextItem{}},
		Execution: domain.RecallExecutionMetadata{OperationID: query.OperationID},
	}, nil
}

// fakeFeedbackService implements FeedbackService.
type fakeFeedbackService struct {
	recorder   *callRecorder
	appendFunc func(context.Context, gateway.CallContext, feedback.AppendCommand) (feedback.AppendResult, error)
}

func (f *fakeFeedbackService) Append(ctx context.Context, call gateway.CallContext, command feedback.AppendCommand) (feedback.AppendResult, error) {
	f.recorder.record("feedback.append", call, command)
	if f.appendFunc != nil {
		return f.appendFunc(ctx, call, command)
	}
	return feedback.AppendResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID},
		Response: domain.FeedbackAppendResponse{
			Feedback: domain.FeedbackEvent{FeedbackID: "fb-1", MemoryID: command.MemoryID, Purpose: command.Purpose},
			Proposal: domain.ProposalOutcome{Status: "not_requested"},
		},
	}, nil
}

// fakeEventService implements EventService.
type fakeEventService struct {
	recorder   *callRecorder
	ingestFunc func(context.Context, gateway.CallContext, extraction.IngestCommand) (extraction.IngestResult, error)
	eventFunc  func(context.Context, gateway.CallContext, extraction.GetEventQuery) (domain.RawEvent, error)
}

func (f *fakeEventService) Ingest(ctx context.Context, call gateway.CallContext, command extraction.IngestCommand) (extraction.IngestResult, error) {
	f.recorder.record("extraction.ingest", call, command)
	if f.ingestFunc != nil {
		return f.ingestFunc(ctx, call, command)
	}
	return extraction.IngestResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID},
		EventID:     "evt-1",
		Status:      "accepted",
		JobIDs:      []string{"job-1"},
		AcceptedAt:  "2024-01-01T00:00:00Z",
	}, nil
}

func (f *fakeEventService) GetEvent(ctx context.Context, call gateway.CallContext, query extraction.GetEventQuery) (domain.RawEvent, error) {
	f.recorder.record("extraction.event.get", call, query)
	if f.eventFunc != nil {
		return f.eventFunc(ctx, call, query)
	}
	return domain.RawEvent{
		EventID:          query.EventID,
		SpaceID:          "scope-1",
		SourceType:       "message",
		SourceName:       "agent",
		Content:          domain.Content{Kind: "text", MediaType: "text/plain", Text: "hello"},
		Metadata:         map[string]any{},
		ProcessingStatus: "pending",
	}, nil
}

// fakeScopeService implements ScopeService.
type fakeScopeService struct {
	recorder   *callRecorder
	createFunc func(context.Context, gateway.CallContext, scope.CreateCommand) (scope.CreateResult, error)
	getFunc    func(context.Context, gateway.CallContext, scope.GetQuery) (domain.Space, error)
}

func (f *fakeScopeService) Create(ctx context.Context, call gateway.CallContext, command scope.CreateCommand) (scope.CreateResult, error) {
	f.recorder.record("scope.create", call, command)
	if f.createFunc != nil {
		return f.createFunc(ctx, call, command)
	}
	return scope.CreateResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID},
		Scope: domain.Space{
			SpaceID:       "scope-1",
			ParentSpaceID: command.ParentSpaceID,
			SpaceType:     command.SpaceType,
			ExternalID:    command.ExternalID,
			DisplayName:   command.DisplayName,
			CreatedAt:     "2024-01-01T00:00:00Z",
		},
	}, nil
}

func (f *fakeScopeService) Get(ctx context.Context, call gateway.CallContext, query scope.GetQuery) (domain.Space, error) {
	f.recorder.record("scope.get", call, query)
	if f.getFunc != nil {
		return f.getFunc(ctx, call, query)
	}
	return domain.Space{SpaceID: query.SpaceID, SpaceType: "project", ExternalID: "ext", DisplayName: "Scope"}, nil
}

// testGateway bundles one fake per consumer-owned interface with a shared
// invocation log. A single type cannot implement both MemoryService and
// ScopeService because both declare a Get method with different signatures.
type testGateway struct {
	recorder *callRecorder
	memory   *fakeMemoryService
	recall   *fakeRecallService
	feedback *fakeFeedbackService
	events   *fakeEventService
	scopes   *fakeScopeService
}

func newTestGateway() *testGateway {
	recorder := &callRecorder{}
	return &testGateway{
		recorder: recorder,
		memory:   &fakeMemoryService{recorder: recorder},
		recall:   &fakeRecallService{recorder: recorder},
		feedback: &fakeFeedbackService{recorder: recorder},
		events:   &fakeEventService{recorder: recorder},
		scopes:   &fakeScopeService{recorder: recorder},
	}
}

// Dependencies returns the interface set the handler requires.
func (g *testGateway) Dependencies() Dependencies {
	return Dependencies{Memory: g.memory, Recall: g.recall, Feedback: g.feedback, Events: g.events, Scopes: g.scopes}
}

// Calls returns a copy of the recorded invocations.
func (g *testGateway) Calls() []recordedCall { return g.recorder.Calls() }

// CallCount returns the number of recorded invocations.
func (g *testGateway) CallCount() int { return g.recorder.CallCount() }

// newTestHandler builds a handler over one test gateway.
func newTestHandler(t *testing.T, fake *testGateway) *Handler {
	t.Helper()
	handler, err := New(fake.Dependencies())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

// callerHeaders returns a valid caller identity.
func callerHeaders() map[string]string {
	return map[string]string{callerSubjectHeader: "agent-1", callerClientHeader: "client-1"}
}

// writeHeaders returns a valid caller identity plus an idempotency key.
func writeHeaders() map[string]string {
	headers := callerHeaders()
	headers[idempotencyHeader] = "key-1"
	return headers
}

// newRequest builds one in-process request.
func newRequest(t *testing.T, method, target, body string, headers map[string]string) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return request
}

// doRequest performs one in-process request against the handler.
func doRequest(t *testing.T, handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return serve(handler, newRequest(t, method, target, body, headers))
}

// serve performs one prepared request against the handler.
func serve(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// assertErrorCode asserts the status and the stable wire error code.
func assertErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", recorder.Code, status, recorder.Body.String())
	}
	if body := decodeErrorResponse(t, recorder); body.Code != code {
		t.Errorf("code = %q, want %q", body.Code, code)
	}
}

// decodeErrorResponse decodes the unified error body.
func decodeErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var response errorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error body %q: %v", recorder.Body.String(), err)
	}
	return response.Error
}

// lastCommand returns the recorded command of the last invocation of operation.
func lastCommand(t *testing.T, fake *testGateway, operation string) recordedCall {
	t.Helper()
	calls := fake.Calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].operation == operation {
			return calls[i]
		}
	}
	t.Fatalf("no recorded call for operation %q", operation)
	return recordedCall{}
}
