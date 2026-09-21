package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

type fakeTools struct {
	call    gateway.CallContext
	command any
}

func (f *fakeTools) Recall(_ context.Context, call gateway.CallContext, command agenttool.RecallCommand) (agenttool.RecallResult, error) {
	f.call, f.command = call, command
	return agenttool.RecallResult{OperationID: "recall-op", Memories: []agenttool.RecalledMemory{{MemoryID: "m", RevisionID: "r", Kind: domain.MemoryKindFact,
		Content: "answer", Confidence: 1, Evidence: []agenttool.EvidenceReference{}}}, DegradedCapabilities: []agenttool.Capability{}, NextCursor: nil}, nil
}
func (f *fakeTools) MemoryCommit(_ context.Context, call gateway.CallContext, command agenttool.MemoryCommitCommand) (agenttool.MemoryCommitResult, error) {
	f.call, f.command = call, command
	return agenttool.MemoryCommitResult{WriteResult: gateway.WriteResult{OperationID: "op", Replay: true}, SourceEventID: "event",
		Memories: []agenttool.CommitOutcome{{LocalID: "local", Status: agenttool.CommitAuthority, MemoryID: "m", RevisionID: "r"}},
		Entities: []agenttool.CommitOutcome{}, Facts: []agenttool.CommitOutcome{}, Relations: []agenttool.CommitOutcome{}}, nil
}
func (f *fakeTools) Feedback(_ context.Context, call gateway.CallContext, command agenttool.FeedbackCommand) (agenttool.FeedbackResult, error) {
	f.call, f.command = call, command
	return agenttool.FeedbackResult{WriteResult: gateway.WriteResult{OperationID: "op"}, FeedbackID: "fb", MemoryID: command.MemoryID, RevisionID: command.RevisionID, Kind: command.Kind}, nil
}
func (f *fakeTools) Forget(_ context.Context, call gateway.CallContext, command agenttool.ForgetCommand) (agenttool.ForgetResult, error) {
	f.call, f.command = call, command
	return agenttool.ForgetResult{WriteResult: gateway.WriteResult{OperationID: "op"}, MemoryID: command.MemoryID, RevisionID: command.ExpectedRevisionID, State: "deleted"}, nil
}

type fakeScopes struct{ calls []string }

func (f *fakeScopes) Ensure(_ context.Context, _ gateway.CallContext, command scope.EnsureCommand) (scope.EnsureResult, error) {
	f.calls = append(f.calls, "ensure")
	return scope.EnsureResult{WriteResult: gateway.WriteResult{OperationID: "op"}, Scope: testScope(command)}, nil
}
func (f *fakeScopes) Get(_ context.Context, _ gateway.CallContext, query scope.GetQuery) (domain.Space, error) {
	f.calls = append(f.calls, "get")
	return domain.Space{SpaceID: query.SpaceID, SpaceType: "project", ExternalID: "external", DisplayName: "Project", CreatedAt: "2026-09-20T10:00:00Z"}, nil
}
func testScope(command scope.EnsureCommand) domain.Space {
	return domain.Space{SpaceID: "scope", ParentSpaceID: command.ParentSpaceID, SpaceType: command.SpaceType, ExternalID: command.ExternalID,
		DisplayName: command.DisplayName, CreatedAt: "2026-09-20T10:00:00Z"}
}

func testHandler(t *testing.T) (*Handler, *fakeTools, *fakeScopes) {
	t.Helper()
	tools, scopes := &fakeTools{}, &fakeScopes{}
	handler, err := New(Dependencies{AgentTools: tools, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	return handler, tools, scopes
}

var targetRoutes = []RouteInfo{
	{Operation: OperationCapabilities, Method: http.MethodGet, Path: "/v1/capabilities", Public: true, Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: agenttool.OperationMemoryCommit, Method: http.MethodPost, Path: "/v1/memory-commits", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxMemoryCommitBodyBytes},
	{Operation: agenttool.OperationRecall, Method: http.MethodPost, Path: "/v1/recall", Access: AccessRead, Idempotency: IdempotencyNotRequired, MaxBodyBytes: MaxRecallBodyBytes},
	{Operation: agenttool.OperationFeedback, Method: http.MethodPost, Path: "/v1/feedback", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxFeedbackBodyBytes},
	{Operation: agenttool.OperationForget, Method: http.MethodPost, Path: "/v1/forget", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxForgetBodyBytes},
	{Operation: scope.OperationEnsure, Method: http.MethodPost, Path: "/v1/scopes/ensure", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxScopeBodyBytes},
	{Operation: OperationScopeGet, Method: http.MethodGet, Path: "/v1/scopes/{scope_id}", Access: AccessRead, Idempotency: IdempotencyNotRequired},
}

func TestRoutesAreExactPhaseTwoSurface(t *testing.T) {
	handler, _, _ := testHandler(t)
	if got := handler.Routes(); !reflect.DeepEqual(got, targetRoutes) {
		t.Fatalf("routes = %+v", got)
	}
	for _, path := range []string{"/v1/memories/m", "/v1/events/e", "/v1/scopes"} {
		if response := request(t, handler, http.MethodGet, path, "", nil); response.Code != http.StatusNotFound {
			t.Errorf("%s status = %d", path, response.Code)
		}
	}
}

func TestCapabilitiesIsPublic(t *testing.T) {
	handler, _, _ := testHandler(t)
	response := request(t, handler, http.MethodGet, "/v1/capabilities", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var got toolcontract.Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, toolcontract.V1Capabilities()) {
		t.Fatalf("capabilities = %+v", got)
	}
}

func TestRecallIsReadSemanticsAndReturnsOperationID(t *testing.T) {
	handler, tools, _ := testHandler(t)
	response := request(t, handler, http.MethodPost, "/v1/recall", `{"query":"what","scope_ids":["s"],"limit":5}`, caller())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if tools.call.Idempotency != nil {
		t.Fatal("recall received idempotency metadata")
	}
	command := tools.command.(agenttool.RecallCommand)
	if command.Query != "what" || command.Limit != 5 {
		t.Fatalf("command = %+v", command)
	}
	var output toolcontract.RecallOutput
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.OperationID != "recall-op" {
		t.Fatalf("operation_id = %q", output.OperationID)
	}
	if err := toolcontract.Validate(toolcontract.SchemaRecallOutput, response.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryCommitMapsContractAndPreservesMetadataNumbers(t *testing.T) {
	handler, tools, _ := testHandler(t)
	body := `{"scope_id":"s","source":{"type":"agent_tool","name":"assistant","conversation_id":"c","turn_id":"t","occurred_at":"2026-09-20T10:00:00Z","metadata":{"n":9007199254740993}},"memories":[{"local_id":"local","kind":"fact","content":"answer","sensitivity":"normal","confidence":1}]}`
	response := request(t, handler, http.MethodPost, "/v1/memory-commits", body, writes())
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	command := tools.command.(agenttool.MemoryCommitCommand)
	if command.Source.Metadata["n"] != json.Number("9007199254740993") {
		t.Fatalf("metadata = %#v", command.Source.Metadata)
	}
	if response.Header().Get(replayHeader) != "true" {
		t.Fatal("missing replay header")
	}
	if err := toolcontract.Validate(toolcontract.SchemaMemoryCommitOutput, response.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackAndForgetUseBodyIdentities(t *testing.T) {
	handler, tools, _ := testHandler(t)
	feedback := request(t, handler, http.MethodPost, "/v1/feedback", `{"scope_id":"s","memory_id":"m","revision_id":"r","kind":"used","recall_operation_id":"recall"}`, writes())
	if feedback.Code != http.StatusCreated {
		t.Fatalf("feedback status = %d: %s", feedback.Code, feedback.Body.String())
	}
	if command := tools.command.(agenttool.FeedbackCommand); command.MemoryID != "m" || command.RecallOperationID != "recall" {
		t.Fatalf("feedback = %+v", command)
	}
	forget := request(t, handler, http.MethodPost, "/v1/forget", `{"scope_id":"s","memory_id":"m","expected_revision_id":"r","reason":"delete"}`, writes())
	if forget.Code != http.StatusOK {
		t.Fatalf("forget status = %d: %s", forget.Code, forget.Body.String())
	}
	if command := tools.command.(agenttool.ForgetCommand); command.MemoryID != "m" || command.ExpectedRevisionID != "r" {
		t.Fatalf("forget = %+v", command)
	}
}

func TestScopeEnsureResolvesThenCreatesAndScopeGet(t *testing.T) {
	handler, _, scopes := testHandler(t)
	body := `{"parent_scope_id":null,"scope_type":"project","external_id":"external","display_name":"Project"}`
	response := request(t, handler, http.MethodPost, "/v1/scopes/ensure", body, writes())
	if response.Code != http.StatusOK {
		t.Fatalf("ensure status = %d: %s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(scopes.calls, []string{"ensure"}) {
		t.Fatalf("calls = %v", scopes.calls)
	}
	response = request(t, handler, http.MethodGet, "/v1/scopes/scope", "", caller())
	if response.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", response.Code, response.Body.String())
	}
	if err := toolcontract.Validate(toolcontract.SchemaScopeOutput, response.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestStrictBodiesLimitsAndWriteIdempotency(t *testing.T) {
	handler, tools, _ := testHandler(t)
	valid := `{"query":"q","scope_ids":["s"]}`
	for name, body := range map[string]string{"unknown": `{"query":"q","scope_ids":["s"],"unknown":true}`, "trailing": valid + ` {}`, "non-object": `[]`} {
		t.Run(name, func(t *testing.T) {
			response := request(t, handler, http.MethodPost, "/v1/recall", body, caller())
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
		})
	}
	response := request(t, handler, http.MethodPost, "/v1/feedback", `{}`, caller())
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing idempotency status = %d", response.Code)
	}
	requestValue := httptest.NewRequest(http.MethodPost, "/v1/recall", strings.NewReader(`{"query":"`+strings.Repeat("x", int(MaxRecallBodyBytes))+`","scope_ids":["s"]}`))
	requestValue.Header.Set("Content-Type", "application/json")
	requestValue.Header.Set(callerSubjectHeader, "agent")
	requestValue.ContentLength = -1
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, requestValue)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d", response.Code)
	}
	if tools.command != nil {
		t.Fatal("gateway called for rejected requests")
	}
}

func request(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	value := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		value.Header.Set("Content-Type", "application/json")
	}
	for name, header := range headers {
		value.Header.Set(name, header)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, value)
	return response
}
func caller() map[string]string { return map[string]string{callerSubjectHeader: "agent"} }
func writes() map[string]string {
	return map[string]string{callerSubjectHeader: "agent", idempotencyHeader: "key"}
}
