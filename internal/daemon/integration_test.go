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
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/provider"
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

// TestGatewayIntegrationLifecycle drives scope, memory, recall, event, feedback,
// and forget through the assembled runtime handler.
func TestGatewayIntegrationLifecycle(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)

	createScope := callHandler(t, handler, http.MethodPost, "/v1/scopes", "scope-key", map[string]any{
		"scope_type": "user", "external_id": "integration", "display_name": "Integration",
	}, true)
	if createScope.Code != http.StatusCreated {
		t.Fatalf("create scope status = %d: %s", createScope.Code, createScope.Body.String())
	}
	var scopeResponse struct {
		Scope struct {
			ScopeID string `json:"scope_id"`
		} `json:"scope"`
	}
	decodeInto(t, createScope, &scopeResponse)
	scopeID := scopeResponse.Scope.ScopeID
	if scopeID == "" {
		t.Fatal("scope_id missing from create response")
	}

	remember := callHandler(t, handler, http.MethodPost, "/v1/memories", "remember-key", map[string]any{
		"scope_id": scopeID, "content": "integration keyword memory",
	}, true)
	if remember.Code != http.StatusCreated {
		t.Fatalf("remember status = %d: %s", remember.Code, remember.Body.String())
	}
	var rememberResponse struct {
		Memory struct {
			MemoryID string `json:"memory_id"`
			ScopeID  string `json:"scope_id"`
		} `json:"memory"`
	}
	decodeInto(t, remember, &rememberResponse)
	memoryID := rememberResponse.Memory.MemoryID
	if memoryID == "" || rememberResponse.Memory.ScopeID != scopeID {
		t.Fatalf("remember response = %+v", rememberResponse)
	}

	getMemory := callHandler(t, handler, http.MethodGet, "/v1/memories/"+memoryID, "", nil, true)
	if getMemory.Code != http.StatusOK {
		t.Fatalf("get memory status = %d: %s", getMemory.Code, getMemory.Body.String())
	}

	recall := callHandler(t, handler, http.MethodPost, "/v1/recall", "recall-key", map[string]any{
		"query": "integration keyword", "scope_ids": []string{scopeID},
	}, true)
	if recall.Code != http.StatusOK {
		t.Fatalf("recall status = %d: %s", recall.Code, recall.Body.String())
	}
	var recallResponse domain.RecallResponse
	decodeInto(t, recall, &recallResponse)
	if len(recallResponse.Context.Items) != 1 || recallResponse.Context.Items[0].MemoryID != memoryID {
		t.Fatalf("recall items = %+v", recallResponse.Context.Items)
	}
	keyword := false
	for _, capability := range recallResponse.Execution.ExecutedCapabilities {
		if capability == "keyword" {
			keyword = true
		}
		if capability == "semantic" {
			t.Fatalf("offline runtime executed provider-backed capability: %v", recallResponse.Execution.ExecutedCapabilities)
		}
	}
	if !keyword {
		t.Fatalf("keyword capability not executed: %v", recallResponse.Execution.ExecutedCapabilities)
	}

	ingest := callHandler(t, handler, http.MethodPost, "/v1/events", "event-key", map[string]any{
		"scope_id": scopeID, "source_type": "manual", "source_name": "integration-source",
		"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "integration raw event"},
	}, true)
	if ingest.Code != http.StatusAccepted {
		t.Fatalf("ingest status = %d: %s", ingest.Code, ingest.Body.String())
	}
	var eventResponse struct {
		EventID string   `json:"event_id"`
		JobIDs  []string `json:"job_ids"`
	}
	decodeInto(t, ingest, &eventResponse)
	if eventResponse.EventID == "" || len(eventResponse.JobIDs) == 0 {
		t.Fatalf("ingest response = %+v", eventResponse)
	}

	getEvent := callHandler(t, handler, http.MethodGet, "/v1/events/"+eventResponse.EventID, "", nil, true)
	if getEvent.Code != http.StatusOK {
		t.Fatalf("get event status = %d: %s", getEvent.Code, getEvent.Body.String())
	}

	feedback := callHandler(t, handler, http.MethodPost, "/v1/memories/"+memoryID+"/feedback", "feedback-key", map[string]any{
		"scope_id": scopeID, "purpose": "memory_quality_feedback", "feedback_type": "confirm", "reason_code": "other",
	}, true)
	if feedback.Code != http.StatusCreated {
		t.Fatalf("feedback status = %d: %s", feedback.Code, feedback.Body.String())
	}

	forget := callHandler(t, handler, http.MethodPost, "/v1/memories/"+memoryID+"/forget", "forget-key", map[string]any{
		"reason": "integration cleanup",
	}, true)
	if forget.Code != http.StatusOK {
		t.Fatalf("forget status = %d: %s", forget.Code, forget.Body.String())
	}
	var forgetResponse struct {
		Memory struct {
			CurrentState string `json:"current_state"`
		} `json:"memory"`
	}
	decodeInto(t, forget, &forgetResponse)
	if forgetResponse.Memory.CurrentState == "active" {
		t.Fatalf("memory still active after forget: %+v", forgetResponse)
	}
}

// TestGatewayIntegrationIdempotentReplay proves a repeated write identity
// returns the original authority result and publishes the replay signal.
func TestGatewayIntegrationIdempotentReplay(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)
	createScope := callHandler(t, handler, http.MethodPost, "/v1/scopes", "scope-key", map[string]any{
		"scope_type": "user", "external_id": "replay", "display_name": "Replay",
	}, true)
	var scopeResponse struct {
		Scope struct {
			ScopeID string `json:"scope_id"`
		} `json:"scope"`
	}
	decodeInto(t, createScope, &scopeResponse)

	command := map[string]any{"scope_id": scopeResponse.Scope.ScopeID, "content": "replay keyword memory"}
	first := callHandler(t, handler, http.MethodPost, "/v1/memories", "same-key", command, true)
	second := callHandler(t, handler, http.MethodPost, "/v1/memories", "same-key", command, true)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("remember statuses = %d/%d", first.Code, second.Code)
	}
	if first.Header().Get("Idempotency-Replayed") != "false" || second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay headers = %q/%q", first.Header().Get("Idempotency-Replayed"), second.Header().Get("Idempotency-Replayed"))
	}
	var firstResponse, secondResponse struct {
		Memory struct {
			MemoryID string `json:"memory_id"`
		} `json:"memory"`
	}
	decodeInto(t, first, &firstResponse)
	decodeInto(t, second, &secondResponse)
	if firstResponse.Memory.MemoryID != secondResponse.Memory.MemoryID {
		t.Fatalf("replay returned a different memory: %q != %q", firstResponse.Memory.MemoryID, secondResponse.Memory.MemoryID)
	}
}

// TestGatewayIntegrationEnforcesRoutePolicy proves caller identity and
// idempotency requirements are enforced at the protocol boundary and that
// strict decoding rejects unknown fields.
func TestGatewayIntegrationEnforcesRoutePolicy(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)

	noCaller := callHandler(t, handler, http.MethodPost, "/v1/scopes", "scope-key", map[string]any{
		"scope_type": "user", "external_id": "no-caller", "display_name": "No Caller",
	}, false)
	if noCaller.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing caller status = %d: %s", noCaller.Code, noCaller.Body.String())
	}

	noIdempotency := callHandler(t, handler, http.MethodPost, "/v1/scopes", "", map[string]any{
		"scope_type": "user", "external_id": "no-idempotency", "display_name": "No Idempotency",
	}, true)
	if noIdempotency.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing idempotency status = %d: %s", noIdempotency.Code, noIdempotency.Body.String())
	}

	unknownField := callHandler(t, handler, http.MethodPost, "/v1/scopes", "scope-key", map[string]any{
		"scope_type": "user", "external_id": "unknown", "display_name": "Unknown", "unexpected": true,
	}, true)
	if unknownField.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d: %s", unknownField.Code, unknownField.Body.String())
	}
}

// TestProviderDisabledOperationPreservesCoreAndDegradesSafely proves dMem with
// every provider disabled still creates direct memories, recalls them with the
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

	createScope := callHandler(t, handler, http.MethodPost, "/v1/scopes", "scope-key", map[string]any{
		"scope_type": "user", "external_id": "offline", "display_name": "Offline",
	}, true)
	if createScope.Code != http.StatusCreated {
		t.Fatalf("create scope status = %d: %s", createScope.Code, createScope.Body.String())
	}
	var scopeResponse struct {
		Scope struct {
			ScopeID string `json:"scope_id"`
		} `json:"scope"`
	}
	decodeInto(t, createScope, &scopeResponse)

	remember := callHandler(t, handler, http.MethodPost, "/v1/memories", "remember-key", map[string]any{
		"scope_id": scopeResponse.Scope.ScopeID, "content": "offline keyword memory",
	}, true)
	if remember.Code != http.StatusCreated {
		t.Fatalf("remember status = %d: %s", remember.Code, remember.Body.String())
	}
	var rememberResponse struct {
		Memory struct {
			MemoryID string `json:"memory_id"`
		} `json:"memory"`
	}
	decodeInto(t, remember, &rememberResponse)

	first := callHandler(t, handler, http.MethodPost, "/v1/recall", "recall-key", map[string]any{
		"query": "offline keyword", "scope_ids": []string{scopeResponse.Scope.ScopeID}, "reference_time": "2026-08-21T12:00:00Z",
	}, true)
	if first.Code != http.StatusOK {
		t.Fatalf("recall status = %d: %s", first.Code, first.Body.String())
	}
	var firstResponse domain.RecallResponse
	decodeInto(t, first, &firstResponse)
	if len(firstResponse.Context.Items) != 1 || firstResponse.Context.Items[0].MemoryID != rememberResponse.Memory.MemoryID {
		t.Fatalf("recall items = %+v", firstResponse.Context.Items)
	}
	if firstResponse.Execution.QueryPlanIdentity == "" {
		t.Fatal("deterministic query plan identity missing")
	}
	for _, capability := range firstResponse.Execution.ExecutedCapabilities {
		if capability == "semantic" {
			t.Fatalf("provider-disabled recall executed %q", capability)
		}
	}

	second := callHandler(t, handler, http.MethodPost, "/v1/recall", "recall-key-2", map[string]any{
		"query": "offline keyword", "scope_ids": []string{scopeResponse.Scope.ScopeID}, "reference_time": "2026-08-21T12:00:00Z",
	}, true)
	var secondResponse domain.RecallResponse
	decodeInto(t, second, &secondResponse)
	if secondResponse.Execution.QueryPlanIdentity != firstResponse.Execution.QueryPlanIdentity {
		t.Fatalf("query plan is not deterministic: %q vs %q", secondResponse.Execution.QueryPlanIdentity, firstResponse.Execution.QueryPlanIdentity)
	}
	if !secondResponse.Execution.Degraded {
		t.Fatal("provider-disabled recall did not report degradation")
	}
	found := false
	for _, reason := range secondResponse.Execution.DegradationReasons {
		if reason == "semantic_unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("degradation reasons = %v", secondResponse.Execution.DegradationReasons)
	}
	if state, identity := d.jobs.ProcessorHealth(); state != "not_configured" || identity != "none" {
		t.Fatalf("provider-disabled processor health = %q/%q", state, identity)
	}
}
