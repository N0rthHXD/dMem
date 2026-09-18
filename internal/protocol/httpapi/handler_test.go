package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/feedback"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	"github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
)

func TestRememberRoute(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	headers := writeHeaders()
	headers[requestIDHeader] = "req-fixed"
	headers[correlationHeader] = "corr-1"

	recorder := doRequest(t, handler, http.MethodPost, "/v1/memories",
		`{"scope_id":"scope-1","content":"hello","kind":"fact","sensitivity":"normal","source_name":"agent","metadata":{"n":9007199254740993},"reason":"test"}`,
		headers)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get(replayHeader); got != "false" {
		t.Errorf("replay header = %q, want false", got)
	}
	if got := recorder.Header().Get(requestIDHeader); got != "req-fixed" {
		t.Errorf("request id header = %q, want req-fixed", got)
	}

	recorded := lastCommand(t, fake, "memory.remember")
	if recorded.call.RequestID != "req-fixed" || recorded.call.CorrelationID != "corr-1" {
		t.Errorf("call metadata = %+v", recorded.call)
	}
	if recorded.call.Caller.SubjectID != "agent-1" || recorded.call.Caller.ClientID != "client-1" {
		t.Errorf("caller = %+v", recorded.call.Caller)
	}
	if recorded.call.IdempotencyKey() != "key-1" {
		t.Errorf("idempotency key = %q", recorded.call.IdempotencyKey())
	}
	command, ok := recorded.command.(memory.RememberCommand)
	if !ok {
		t.Fatalf("command type = %T", recorded.command)
	}
	if command.SpaceID != "scope-1" || command.Content != "hello" || command.Kind != "fact" {
		t.Errorf("command = %+v", command)
	}
	if got, want := command.Metadata["n"], json.Number("9007199254740993"); got != want {
		t.Errorf("metadata number = %#v (%T), want json.Number %q", got, got, want)
	}

	response := mustJSONMap(t, recorder)
	memoryBody := response["memory"].(map[string]any)
	if memoryBody["scope_id"] != "scope-1" {
		t.Errorf("memory.scope_id = %v", memoryBody["scope_id"])
	}
	if _, present := memoryBody["space_id"]; present {
		t.Errorf("memory response leaked storage-era space_id")
	}
}

func TestRememberReplayHeader(t *testing.T) {
	fake := newTestGateway()
	fake.memory.rememberFunc = func(_ context.Context, call gateway.CallContext, command memory.RememberCommand) (memory.RememberResult, error) {
		return memory.RememberResult{
			WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: true},
			Memory:      domain.Memory{MemoryID: "mem-1", SpaceID: command.SpaceID},
		}, nil
	}
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodPost, "/v1/memories", `{"scope_id":"s","content":"c"}`, writeHeaders())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", recorder.Code)
	}
	if got := recorder.Header().Get(replayHeader); got != "true" {
		t.Errorf("replay header = %q, want true", got)
	}
	if response := mustJSONMap(t, recorder); response["replayed"] != true {
		t.Errorf("replayed = %v, want true", response["replayed"])
	}
}

func TestGetMemoryRoute(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/mem-42", "", callerHeaders())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	recorded := lastCommand(t, fake, "memory.get")
	query := recorded.command.(memory.GetQuery)
	if query.MemoryID != "mem-42" {
		t.Errorf("memory id = %q", query.MemoryID)
	}
	response := mustJSONMap(t, recorder)
	if response["memory"].(map[string]any)["memory_id"] != "mem-42" {
		t.Errorf("response = %v", response)
	}
}

func TestForgetRoute(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodPost, "/v1/memories/mem-42/forget", `{"reason":"cleanup"}`, writeHeaders())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	recorded := lastCommand(t, fake, "memory.forget")
	command := recorded.command.(memory.ForgetCommand)
	if command.MemoryID != "mem-42" || command.Reason != "cleanup" {
		t.Errorf("command = %+v", command)
	}
}

func TestFeedbackRoute(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodPost, "/v1/memories/mem-42/feedback",
		`{"scope_id":"scope-1","purpose":"memory_quality_feedback","feedback_type":"confirm","reason_code":"other","note":"ok"}`,
		writeHeaders())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
	}
	recorded := lastCommand(t, fake, "feedback.append")
	command := recorded.command.(feedback.AppendCommand)
	if command.MemoryID != "mem-42" || command.SpaceID != "scope-1" || command.Purpose != "memory_quality_feedback" {
		t.Errorf("command = %+v", command)
	}
	if command.Note == nil || *command.Note != "ok" {
		t.Errorf("note = %v", command.Note)
	}
	response := mustJSONMap(t, recorder)
	if response["feedback"].(map[string]any)["feedback_id"] != "fb-1" {
		t.Errorf("response = %v", response)
	}
}

func TestRecallRoute(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	headers := writeHeaders()
	headers[requestIDHeader] = "req-recall"
	recorder := doRequest(t, handler, http.MethodPost, "/v1/recall",
		`{"query":"what happened","scope_ids":["scope-1"],"include_ancestors":false,"memory_types":[],"max_results":5,"explain":true}`,
		headers)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	recorded := lastCommand(t, fake, "retrieval.recall")
	query := recorded.command.(retrieval.Query)
	if query.OperationID != "req-recall" || query.Query != "what happened" {
		t.Errorf("query = %+v", query)
	}
	if len(query.SpaceIDs) != 1 || query.SpaceIDs[0] != "scope-1" {
		t.Errorf("scope ids = %v", query.SpaceIDs)
	}
	if query.IncludeAncestors || !query.IncludeAncestorsSet {
		t.Errorf("include_ancestors mapping = %v set=%v", query.IncludeAncestors, query.IncludeAncestorsSet)
	}
	if !query.MemoryTypesSet || len(query.MemoryTypes) != 0 {
		t.Errorf("memory_types mapping = %v set=%v", query.MemoryTypes, query.MemoryTypesSet)
	}
	if query.Limit != 5 || !query.LimitSet {
		t.Errorf("max_results mapping = %d set=%v", query.Limit, query.LimitSet)
	}
	if !query.Explain {
		t.Errorf("explain mapping = false")
	}
	response := mustJSONMap(t, recorder)
	if _, present := response["context"]; !present {
		t.Errorf("response missing context: %v", response)
	}
	if _, present := response["execution"]; !present {
		t.Errorf("response missing execution: %v", response)
	}
}

func TestRecallOmittedOptionalFieldsAreUnset(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodPost, "/v1/recall", `{"query":"q","scope_ids":["s"]}`, writeHeaders())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	query := lastCommand(t, fake, "retrieval.recall").command.(retrieval.Query)
	if query.IncludeAncestorsSet || query.MemoryTypesSet || query.LimitSet || query.MaxCharsSet ||
		query.MaxContextTokensSet || query.ReferenceTimeSet || query.ExcludeStaleAggregatesSet {
		t.Errorf("omitted optional fields were marked set: %+v", query)
	}
}

func TestIngestEventRoute(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodPost, "/v1/events",
		`{"scope_id":"scope-1","source_type":"message","source_name":"agent","sequence":3,"occurred_at":"2024-01-01T00:00:00Z","content":{"kind":"text","media_type":"text/plain","text":"hello"},"metadata":{"k":"v"}}`,
		writeHeaders())
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body.String())
	}
	recorded := lastCommand(t, fake, "extraction.ingest")
	command := recorded.command.(extraction.IngestCommand)
	if command.SpaceID != "scope-1" || command.SourceType != "message" || command.SourceName != "agent" {
		t.Errorf("command = %+v", command)
	}
	if command.Sequence == nil || *command.Sequence != 3 {
		t.Errorf("sequence = %v", command.Sequence)
	}
	if command.Content.Kind != "text" || command.Content.Text != "hello" || command.Content.MediaType != "text/plain" {
		t.Errorf("content = %+v", command.Content)
	}
	response := mustJSONMap(t, recorder)
	if response["event_id"] != "evt-1" || response["status"] != "accepted" {
		t.Errorf("response = %v", response)
	}
}

func TestIngestEventJSONContentPreservesNumbers(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodPost, "/v1/events",
		`{"scope_id":"s","source_type":"message","source_name":"agent","content":{"kind":"json","media_type":"application/json","value":{"n":9007199254740993}}}`,
		writeHeaders())
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body.String())
	}
	command := lastCommand(t, fake, "extraction.ingest").command.(extraction.IngestCommand)
	value, ok := command.Content.Value.(map[string]any)
	if !ok {
		t.Fatalf("content value type = %T", command.Content.Value)
	}
	if got, want := value["n"], json.Number("9007199254740993"); got != want {
		t.Errorf("content value number = %#v (%T), want json.Number %q", got, got, want)
	}
}

func TestGetEventRoute(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodGet, "/v1/events/evt-9", "", callerHeaders())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	query := lastCommand(t, fake, "extraction.event.get").command.(extraction.GetEventQuery)
	if query.EventID != "evt-9" {
		t.Errorf("event id = %q", query.EventID)
	}
	event := mustJSONMap(t, recorder)["event"].(map[string]any)
	if event["event_id"] != "evt-9" || event["scope_id"] != "scope-1" {
		t.Errorf("event = %v", event)
	}
	if _, present := event["space_id"]; present {
		t.Errorf("event response leaked storage-era space_id")
	}
}

func TestCreateScopeRoute(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodPost, "/v1/scopes",
		`{"parent_scope_id":null,"scope_type":"project","external_id":"ext-1","display_name":"Project"}`,
		writeHeaders())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
	}
	command := lastCommand(t, fake, "scope.create").command.(scope.CreateCommand)
	if command.ParentSpaceID != nil || command.SpaceType != "project" || command.ExternalID != "ext-1" || command.DisplayName != "Project" {
		t.Errorf("command = %+v", command)
	}
	response := mustJSONMap(t, recorder)
	scopeBody := response["scope"].(map[string]any)
	if scopeBody["scope_id"] != "scope-1" || scopeBody["scope_type"] != "project" {
		t.Errorf("scope = %v", scopeBody)
	}
	if _, present := scopeBody["space_id"]; present {
		t.Errorf("scope response leaked storage-era space_id")
	}
}

func TestCreateScopeWithParent(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodPost, "/v1/scopes",
		`{"parent_scope_id":"scope-parent","scope_type":"task","external_id":"ext-2","display_name":"Task"}`,
		writeHeaders())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
	}
	command := lastCommand(t, fake, "scope.create").command.(scope.CreateCommand)
	if command.ParentSpaceID == nil || *command.ParentSpaceID != "scope-parent" {
		t.Errorf("parent = %v", command.ParentSpaceID)
	}
}

func TestGetScopeRoute(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodGet, "/v1/scopes/scope-7", "", callerHeaders())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	query := lastCommand(t, fake, "scope.get").command.(scope.GetQuery)
	if query.SpaceID != "scope-7" {
		t.Errorf("scope id = %q", query.SpaceID)
	}
	scopeBody := mustJSONMap(t, recorder)["scope"].(map[string]any)
	if scopeBody["scope_id"] != "scope-7" {
		t.Errorf("scope = %v", scopeBody)
	}
}

// mustJSONMap decodes a JSON object response.
func mustJSONMap(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}
