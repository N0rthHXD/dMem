package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/N0rthHXD/dMem/internal/api"
)

// TestProviderDisabledOperationPreservesCoreAndDegradesSafely proves dMem with
// every provider disabled still ingests raw events, creates direct memories,
// recalls them with keyword, produces a deterministic query plan, and degrades
// semantic requests explicitly instead of resolving a secret or making a
// network call.
func TestProviderDisabledOperationPreservesCoreAndDegradesSafely(t *testing.T) {
	t.Setenv("DMEM_OFFLINE_PROBE_SECRET", "must-not-be-read")
	cfg := testConfig(t)
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)

	space := createSpace(t, client, "offline-space", map[string]any{"space_type": "user", "external_id": "offline", "display_name": "Offline"})
	event := ingestManualEvent(t, client, "offline-event", space.SpaceID, "offline source text")

	fetched := requestJSON(t, client, http.MethodGet, "/v1/events/"+event.EventID, "", nil)
	if fetched.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(fetched.Body)
		fetched.Body.Close()
		t.Fatalf("raw event fetch status = %d: %s", fetched.StatusCode, data)
	}
	fetched.Body.Close()

	memory, _ := createMemory(t, client, "offline-memory", memoryCreateBody(space.SpaceID, event.EventID, "offline keyword memory"))
	first := search(t, client, map[string]any{"query": "offline keyword", "space_ids": []string{space.SpaceID}, "reference_time": "2026-08-21T12:00:00Z"})
	assertSearchSet(t, first, []string{memory.MemoryID})
	if first.Execution.QueryPlanIdentity == "" {
		t.Fatal("deterministic query plan identity missing")
	}
	for _, capability := range first.Execution.ExecutedCapabilities {
		if capability == "semantic" {
			t.Fatalf("provider-disabled recall executed provider-backed capability %q", capability)
		}
	}
	if !containsString(first.Execution.ExecutedCapabilities, "keyword") {
		t.Fatalf("executed capabilities = %v", first.Execution.ExecutedCapabilities)
	}

	second := search(t, client, map[string]any{"query": "offline keyword", "space_ids": []string{space.SpaceID}, "reference_time": "2026-08-21T12:00:00Z"})
	if second.Execution.QueryPlanIdentity != first.Execution.QueryPlanIdentity {
		t.Fatalf("query plan is not deterministic: %q vs %q", first.Execution.QueryPlanIdentity, second.Execution.QueryPlanIdentity)
	}
	if !second.Execution.Degraded {
		t.Fatal("provider-disabled semantic recall did not report degradation")
	}
	if len(second.Execution.DegradationReasons) == 0 {
		t.Fatal("provider-disabled semantic recall reported no degradation reason")
	}
	if !containsString(second.Execution.DegradationReasons, "semantic_unavailable") {
		t.Fatalf("degradation reasons = %v", second.Execution.DegradationReasons)
	}

	healthResponse := requestJSON(t, client, http.MethodGet, "/v1/health", "", nil)
	if healthResponse.StatusCode != http.StatusOK {
		healthResponse.Body.Close()
		t.Fatalf("health status = %d", healthResponse.StatusCode)
	}
	var health api.Health
	if err := json.NewDecoder(healthResponse.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	healthResponse.Body.Close()
	if health.ProcessorState != "not_configured" || health.ProcessorIdentity != "none" {
		t.Fatalf("provider-disabled health = %+v", health)
	}
	switch health.SemanticIndexState {
	case "not_configured", "rebuild_required", "absent":
	default:
		t.Fatalf("provider-disabled semantic state = %q", health.SemanticIndexState)
	}
}
