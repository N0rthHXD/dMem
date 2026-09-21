package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/protocol/mcp"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/pkg/client"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

// newIntegrationRuntime assembles the production runtime over a real SQLite
// database and returns its protocol handler. Providers stay cleared so the test
// exercises offline defaults through the stable runtime boundary.
func newIntegrationRuntime(t *testing.T) http.Handler {
	t.Helper()
	cfg := runtimeConfig(t)
	d, err := StartStaging(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartStaging() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close(context.Background()) })
	return d.Handler()
}

// callHandler issues one in-process request through the assembled protocol
// handler. It mirrors the wire contract without creating a listener.
func callHandler(t *testing.T, handler http.Handler, method, path, idempotencyKey string, body any, caller bool) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if caller {
		request.Header.Set("X-Caller-Subject-ID", "integration-test")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeInto(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %d %s: %v", recorder.Code, recorder.Body.String(), err)
	}
}

// ensureScope creates one scope through the application API and returns its ID.
func ensureScope(t *testing.T, handler http.Handler, externalID string) string {
	t.Helper()
	recorder := callHandler(t, handler, http.MethodPost, "/v1/scopes/ensure", "scope-"+externalID, map[string]any{
		"scope_type": "user", "external_id": externalID, "display_name": externalID,
	}, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ensure scope status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response toolcontract.ScopeEnsureOutput
	decodeInto(t, recorder, &response)
	if response.Scope.ScopeID == "" {
		t.Fatal("scope_id missing from ensure response")
	}
	return response.Scope.ScopeID
}

func memoryCommitBody(scopeID, content string) map[string]any {
	return map[string]any{
		"scope_id": scopeID,
		"source": map[string]any{
			"type": "agent_tool", "name": "assistant", "conversation_id": "conversation-1",
			"turn_id": "turn-1", "occurred_at": "2026-08-21T11:00:00Z",
		},
		"memories": []map[string]any{
			{"local_id": "memory-1", "kind": "fact", "content": content, "sensitivity": "normal", "confidence": 0.9},
		},
	}
}

// TestGatewayIntegrationLifecycle drives scope ensure, memory commit, recall,
// feedback, and forget through the assembled runtime handler.
func TestGatewayIntegrationLifecycle(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)
	scopeID := ensureScope(t, handler, "integration")

	commit := callHandler(t, handler, http.MethodPost, "/v1/memory-commits", "commit-key", memoryCommitBody(scopeID, "integration keyword memory"), true)
	if commit.Code != http.StatusCreated {
		t.Fatalf("commit status = %d: %s", commit.Code, commit.Body.String())
	}
	var commitResponse toolcontract.MemoryCommitOutput
	decodeInto(t, commit, &commitResponse)
	if len(commitResponse.Memories) != 1 || commitResponse.Memories[0].Status != "authority" {
		t.Fatalf("commit response = %+v", commitResponse)
	}
	memoryID := commitResponse.Memories[0].MemoryID
	revisionID := commitResponse.Memories[0].RevisionID
	if memoryID == "" || revisionID == "" || commitResponse.SourceEventID == "" {
		t.Fatalf("commit response missing authority identity: %+v", commitResponse)
	}

	recall := callHandler(t, handler, http.MethodPost, "/v1/recall", "", map[string]any{
		"query": "integration keyword", "scope_ids": []string{scopeID},
	}, true)
	if recall.Code != http.StatusOK {
		t.Fatalf("recall status = %d: %s", recall.Code, recall.Body.String())
	}
	var recallResponse toolcontract.RecallOutput
	decodeInto(t, recall, &recallResponse)
	if len(recallResponse.Memories) != 1 || recallResponse.Memories[0].MemoryID != memoryID {
		t.Fatalf("recall memories = %+v", recallResponse.Memories)
	}
	if recallResponse.OperationID == "" {
		t.Fatal("recall operation_id missing")
	}
	degraded := false
	for _, capability := range recallResponse.DegradedCapabilities {
		if capability == "semantic" {
			degraded = true
		}
	}
	if !degraded {
		t.Fatalf("offline runtime did not report semantic degradation: %v", recallResponse.DegradedCapabilities)
	}

	feedback := callHandler(t, handler, http.MethodPost, "/v1/feedback", "feedback-key", map[string]any{
		"scope_id": scopeID, "memory_id": memoryID, "revision_id": revisionID,
		"kind": "used", "recall_operation_id": recallResponse.OperationID,
	}, true)
	if feedback.Code != http.StatusCreated {
		t.Fatalf("feedback status = %d: %s", feedback.Code, feedback.Body.String())
	}

	forget := callHandler(t, handler, http.MethodPost, "/v1/forget", "forget-key", map[string]any{
		"scope_id": scopeID, "memory_id": memoryID, "expected_revision_id": revisionID, "reason": "integration cleanup",
	}, true)
	if forget.Code != http.StatusOK {
		t.Fatalf("forget status = %d: %s", forget.Code, forget.Body.String())
	}
	var forgetResponse toolcontract.ForgetOutput
	decodeInto(t, forget, &forgetResponse)
	if forgetResponse.State != "deleted" || forgetResponse.RevisionID != revisionID {
		t.Fatalf("forget response = %+v", forgetResponse)
	}

	afterForget := callHandler(t, handler, http.MethodPost, "/v1/recall", "", map[string]any{
		"query": "integration keyword", "scope_ids": []string{scopeID},
	}, true)
	var afterForgetResponse toolcontract.RecallOutput
	decodeInto(t, afterForget, &afterForgetResponse)
	if len(afterForgetResponse.Memories) != 0 {
		t.Fatalf("forgotten memory still recalled: %+v", afterForgetResponse.Memories)
	}
}

// TestGatewayIntegrationIdempotentReplay proves a repeated write identity
// returns the original authority result and publishes the replay signal.
func TestGatewayIntegrationIdempotentReplay(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)
	scopeID := ensureScope(t, handler, "replay")

	command := memoryCommitBody(scopeID, "replay keyword memory")
	first := callHandler(t, handler, http.MethodPost, "/v1/memory-commits", "same-key", command, true)
	second := callHandler(t, handler, http.MethodPost, "/v1/memory-commits", "same-key", command, true)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("commit statuses = %d/%d: %s / %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if first.Header().Get("Idempotency-Replayed") != "false" || second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay headers = %q/%q", first.Header().Get("Idempotency-Replayed"), second.Header().Get("Idempotency-Replayed"))
	}
	var firstResponse, secondResponse toolcontract.MemoryCommitOutput
	decodeInto(t, first, &firstResponse)
	decodeInto(t, second, &secondResponse)
	if firstResponse.SourceEventID != secondResponse.SourceEventID || firstResponse.Memories[0].MemoryID != secondResponse.Memories[0].MemoryID {
		t.Fatalf("replay returned a different result: %+v vs %+v", firstResponse, secondResponse)
	}
}

// TestGatewayIntegrationEnforcesRoutePolicy proves caller identity and
// idempotency requirements are enforced at the protocol boundary and that
// strict decoding rejects unknown fields.
func TestGatewayIntegrationEnforcesRoutePolicy(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)

	noCaller := callHandler(t, handler, http.MethodPost, "/v1/scopes/ensure", "scope-key", map[string]any{
		"scope_type": "user", "external_id": "no-caller", "display_name": "No Caller",
	}, false)
	if noCaller.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing caller status = %d: %s", noCaller.Code, noCaller.Body.String())
	}

	noIdempotency := callHandler(t, handler, http.MethodPost, "/v1/scopes/ensure", "", map[string]any{
		"scope_type": "user", "external_id": "no-idempotency", "display_name": "No Idempotency",
	}, true)
	if noIdempotency.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing idempotency status = %d: %s", noIdempotency.Code, noIdempotency.Body.String())
	}

	unknownField := callHandler(t, handler, http.MethodPost, "/v1/scopes/ensure", "scope-key", map[string]any{
		"scope_type": "user", "external_id": "unknown", "display_name": "Unknown", "unexpected": true,
	}, true)
	if unknownField.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d: %s", unknownField.Code, unknownField.Body.String())
	}

	recallWithoutIdempotency := callHandler(t, handler, http.MethodPost, "/v1/recall", "", map[string]any{
		"query": "keyword", "scope_ids": []string{"missing-scope"},
	}, true)
	if recallWithoutIdempotency.Code == http.StatusUnprocessableEntity {
		t.Fatalf("recall must not require an idempotency key: %s", recallWithoutIdempotency.Body.String())
	}
}

// TestProviderDisabledOperationPreservesCoreAndDegradesSafely proves dMem with
// every provider disabled still commits direct memories, recalls them with the
// lexical index, and reports semantic degradation explicitly instead of
// resolving a provider secret or making a network call.
func TestProviderDisabledOperationPreservesCoreAndDegradesSafely(t *testing.T) {
	t.Setenv("DMEM_OFFLINE_PROBE_SECRET", "must-not-be-read")
	cfg := runtimeConfig(t)
	cfg.ProcessingProvider = config.ProviderConfig{Enabled: false, Secret: provider.SecretReference{Source: "environment", Name: "DMEM_OFFLINE_PROBE_SECRET"}}
	cfg.EmbeddingProvider = config.ProviderConfig{Enabled: false, Secret: provider.SecretReference{Source: "environment", Name: "DMEM_OFFLINE_PROBE_SECRET"}}
	d, err := StartStaging(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	handler := d.Handler()

	scopeID := ensureScope(t, handler, "offline")

	commit := callHandler(t, handler, http.MethodPost, "/v1/memory-commits", "remember-key", memoryCommitBody(scopeID, "offline keyword memory"), true)
	if commit.Code != http.StatusCreated {
		t.Fatalf("commit status = %d: %s", commit.Code, commit.Body.String())
	}
	var commitResponse toolcontract.MemoryCommitOutput
	decodeInto(t, commit, &commitResponse)
	memoryID := commitResponse.Memories[0].MemoryID

	recall := callHandler(t, handler, http.MethodPost, "/v1/recall", "", map[string]any{
		"query": "offline keyword", "scope_ids": []string{scopeID}, "reference_time": "2026-08-21T12:00:00Z",
	}, true)
	if recall.Code != http.StatusOK {
		t.Fatalf("recall status = %d: %s", recall.Code, recall.Body.String())
	}
	var recallResponse toolcontract.RecallOutput
	decodeInto(t, recall, &recallResponse)
	if len(recallResponse.Memories) != 1 || recallResponse.Memories[0].MemoryID != memoryID {
		t.Fatalf("recall memories = %+v", recallResponse.Memories)
	}
	degradedSemantic := false
	for _, capability := range recallResponse.DegradedCapabilities {
		if capability == "semantic" {
			degradedSemantic = true
		}
	}
	if !degradedSemantic {
		t.Fatalf("provider-disabled recall did not report semantic degradation: %v", recallResponse.DegradedCapabilities)
	}
	if state, identity := d.jobs.ProcessorHealth(); state != "not_configured" || identity != "none" {
		t.Fatalf("provider-disabled processor health = %q/%q", state, identity)
	}
}

// TestProviderDisabledAgentToolHintRecallClosesLoop proves the offline Agent
// Tool Route recalls a memory through Agent-generated search hints and reports
// semantic degradation explicitly instead of claiming a semantic score.
func TestProviderDisabledAgentToolHintRecallClosesLoop(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)
	scopeID := ensureScope(t, handler, "hint-loop")

	command := map[string]any{
		"scope_id": scopeID,
		"source": map[string]any{
			"type": "agent_tool", "name": "assistant", "conversation_id": "conversation-1",
			"turn_id": "turn-1", "occurred_at": "2026-08-21T11:00:00Z",
		},
		"memories": []map[string]any{
			{"local_id": "memory-1", "kind": "preference", "content": "PostgreSQL is preferred", "sensitivity": "normal", "confidence": 0.9, "search_hints": []string{"database choice"}},
		},
	}
	commit := callHandler(t, handler, http.MethodPost, "/v1/memory-commits", "hint-commit-key", command, true)
	if commit.Code != http.StatusCreated {
		t.Fatalf("commit status = %d: %s", commit.Code, commit.Body.String())
	}
	var commitResponse toolcontract.MemoryCommitOutput
	decodeInto(t, commit, &commitResponse)
	if len(commitResponse.Memories) != 1 || commitResponse.Memories[0].Status != "authority" {
		t.Fatalf("commit response = %+v", commitResponse)
	}
	memoryID := commitResponse.Memories[0].MemoryID

	recall := callHandler(t, handler, http.MethodPost, "/v1/recall", "", map[string]any{
		"query": "database choice", "scope_ids": []string{scopeID},
	}, true)
	if recall.Code != http.StatusOK {
		t.Fatalf("recall status = %d: %s", recall.Code, recall.Body.String())
	}
	var recallResponse toolcontract.RecallOutput
	decodeInto(t, recall, &recallResponse)
	if len(recallResponse.Memories) != 1 || recallResponse.Memories[0].MemoryID != memoryID {
		t.Fatalf("hint-only recall memories = %+v body=%s", recallResponse.Memories, recall.Body.String())
	}
	degradedSemantic := false
	for _, capability := range recallResponse.DegradedCapabilities {
		if capability == "keyword" {
			t.Fatalf("keyword recall must execute offline: %v", recallResponse.DegradedCapabilities)
		}
		if capability == "semantic" {
			degradedSemantic = true
		}
	}
	if !degradedSemantic {
		t.Fatalf("hint-only recall did not report semantic degradation: %v", recallResponse.DegradedCapabilities)
	}
}

// mcpLoop drives the dmem-mcp stdio adapter against a real runtime over HTTP.
// It proves the Agent Tool Route is a complete product path with every provider
// disabled: MCP tool calls alone produce authority, recall, feedback, and
// forget, and no model job is involved.
type mcpLoop struct {
	t         *testing.T
	input     *io.PipeWriter
	responses chan map[string]any
	done      chan error
}

func startMCPLoop(t *testing.T, handler http.Handler, externalScopeID string) *mcpLoop {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	applicationClient, err := client.New(client.Config{
		Endpoint: server.URL,
		Caller:   client.Caller{SubjectID: "mcp-agent", ClientID: "dmem-mcp"},
	})
	if err != nil {
		t.Fatalf("application client: %v", err)
	}
	serverImpl, err := mcp.New(mcp.Config{
		Name: "dmem-mcp", Title: "dMem memory", Version: "test",
		Client: applicationClient,
		Scope:  &mcp.ConfiguredScope{Client: applicationClient, ScopeType: "user", ExternalID: externalScopeID},
	})
	if err != nil {
		t.Fatalf("mcp server: %v", err)
	}
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- serverImpl.Serve(ctx, inReader, outWriter) }()
	responses := make(chan map[string]any, 32)
	go func() {
		reader := bufio.NewReader(outReader)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				var value map[string]any
				if json.Unmarshal(line, &value) == nil {
					responses <- value
				}
			}
			if err != nil {
				close(responses)
				return
			}
		}
	}()
	loop := &mcpLoop{t: t, input: inWriter, responses: responses, done: done}
	t.Cleanup(func() {
		_ = inWriter.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cancel()
			<-done
		}
		_ = outWriter.Close()
	})
	return loop
}

func (l *mcpLoop) send(id int, method string, params map[string]any) {
	l.t.Helper()
	message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		l.t.Fatalf("encode MCP message: %v", err)
	}
	if _, err := l.input.Write(append(encoded, '\n')); err != nil {
		l.t.Fatalf("write MCP message: %v", err)
	}
}

func (l *mcpLoop) receive(id int) map[string]any {
	l.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case response, ok := <-l.responses:
			if !ok {
				l.t.Fatalf("MCP server closed while waiting for id %d", id)
			}
			if fmt.Sprint(response["id"]) == fmt.Sprint(id) {
				return response
			}
		case <-deadline:
			l.t.Fatalf("timed out waiting for MCP response %d", id)
		}
	}
}

func (l *mcpLoop) callTool(id int, name string, arguments map[string]any) map[string]any {
	l.t.Helper()
	l.send(id, "tools/call", map[string]any{"name": name, "arguments": arguments})
	response := l.receive(id)
	result, ok := response["result"].(map[string]any)
	if !ok {
		l.t.Fatalf("tool call %s returned %v", name, response)
	}
	if isError, _ := result["isError"].(bool); isError {
		l.t.Fatalf("tool call %s failed: %v", name, result)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		l.t.Fatalf("tool call %s returned no structured content: %v", name, result)
	}
	return structured
}

// TestMCPToolRouteClosesMemoryLoopWithoutProvider proves a provider-free runtime
// serves the full Agent Tool Route over MCP stdio: scope bootstrap, atomic
// remember, recall with explicit semantic degradation, feedback, and forget.
func TestMCPToolRouteClosesMemoryLoopWithoutProvider(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)
	loop := startMCPLoop(t, handler, "mcp-loop")
	loop.send(1, "initialize", map[string]any{"protocolVersion": mcp.ProtocolVersion20250618,
		"capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test-agent", "version": "1"}})
	if result, ok := loop.receive(1)["result"].(map[string]any); !ok || result["protocolVersion"] != mcp.ProtocolVersion20250618 {
		t.Fatalf("initialize = %v", loop.receive(1))
	}

	commit := loop.callTool(2, toolcontract.ToolRemember, map[string]any{
		"memories": []map[string]any{{"local_id": "m", "kind": "fact", "content": "mcp loop keyword memory",
			"sensitivity": "normal", "confidence": 0.9}},
	})
	memories, ok := commit["memories"].([]any)
	if !ok || len(memories) != 1 {
		t.Fatalf("commit outcomes = %v", commit["memories"])
	}
	outcome, _ := memories[0].(map[string]any)
	if outcome["status"] != "authority" {
		t.Fatalf("commit status = %v", outcome["status"])
	}
	memoryID, _ := outcome["memory_id"].(string)
	revisionID, _ := outcome["revision_id"].(string)
	if memoryID == "" || revisionID == "" {
		t.Fatalf("commit identity = %v", outcome)
	}

	recall := loop.callTool(3, toolcontract.ToolRecall, map[string]any{"query": "mcp loop keyword"})
	operationID, _ := recall["operation_id"].(string)
	if operationID == "" {
		t.Fatalf("recall = %v", recall)
	}
	recalled, ok := recall["memories"].([]any)
	if !ok || len(recalled) != 1 {
		t.Fatalf("recalled = %v", recall["memories"])
	}
	first, _ := recalled[0].(map[string]any)
	if first["memory_id"] != memoryID {
		t.Fatalf("recalled memory = %v", first)
	}
	degraded, _ := recall["degraded_capabilities"].([]any)
	foundSemantic := false
	for _, capability := range degraded {
		if capability == "semantic" {
			foundSemantic = true
		}
	}
	if !foundSemantic {
		t.Fatalf("provider-free recall did not report semantic degradation: %v", degraded)
	}

	feedback := loop.callTool(4, toolcontract.ToolFeedback, map[string]any{
		"memory_id": memoryID, "revision_id": revisionID, "kind": "used", "recall_operation_id": operationID})
	if feedback["feedback_id"] == "" || feedback["kind"] != "used" {
		t.Fatalf("feedback = %v", feedback)
	}

	forget := loop.callTool(5, toolcontract.ToolForget, map[string]any{
		"memory_id": memoryID, "expected_revision_id": revisionID, "reason": "mcp loop cleanup"})
	if forget["state"] != "deleted" {
		t.Fatalf("forget = %v", forget)
	}

	after := loop.callTool(6, toolcontract.ToolRecall, map[string]any{"query": "mcp loop keyword"})
	if remaining, _ := after["memories"].([]any); len(remaining) != 0 {
		t.Fatalf("forgotten memory still recalled: %v", remaining)
	}
}

// TestProviderDisabledEventIngestStoresWithoutModelJob proves that with the
// Provider Route disabled a raw event is stored without creating an extraction
// job that only a no-op worker could consume.
func TestProviderDisabledEventIngestStoresWithoutModelJob(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)
	scopeID := ensureScope(t, handler, "provider-disabled-event")

	response := callHandler(t, handler, http.MethodPost, "/v1/events", "event-disabled", map[string]any{
		"scope_id": scopeID,
		"source":   map[string]any{"type": "manual", "name": "collector", "occurred_at": "2026-08-21T11:00:00Z"},
		"content":  map[string]any{"kind": "text", "media_type": "text/plain", "text": "stored without processing"},
	}, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("ingest status = %d: %s", response.Code, response.Body.String())
	}
	var output struct {
		EventID string   `json:"event_id"`
		Status  string   `json:"status"`
		JobIDs  []string `json:"job_ids"`
	}
	decodeInto(t, response, &output)
	if output.EventID == "" || output.Status != "stored" || len(output.JobIDs) != 0 {
		t.Fatalf("provider-disabled ingest = %+v, want stored with no job", output)
	}

	status := callHandler(t, handler, http.MethodGet, "/v1/events/"+output.EventID, "", nil, true)
	if status.Code != http.StatusOK {
		t.Fatalf("event status = %d: %s", status.Code, status.Body.String())
	}
	var event struct {
		ProcessingStatus string `json:"processing_status"`
	}
	decodeInto(t, status, &event)
	if event.ProcessingStatus != "stored" {
		t.Fatalf("processing_status = %q, want stored", event.ProcessingStatus)
	}
}
