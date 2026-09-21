package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/provider"
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
