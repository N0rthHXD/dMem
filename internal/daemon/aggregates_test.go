package daemon

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/jobs"
	"github.com/N0rthHXD/dMem/internal/provider"
)

func TestAggregateDaemonBuildConfirmProvenanceStaleAndHealth(t *testing.T) {
	cfg := testConfig(t)
	cfg.ProcessingVersion = "aggregate-v1"
	cfg.ProcessingProvider = config.ProviderConfig{
		Enabled: true,
		Policy: provider.Policy{
			Default:       "deny",
			ContentTypes:  []string{"text/plain"},
			SourceTypes:   []string{"manual"},
			Sensitivities: []string{"normal"},
			Assignments: []provider.PolicyAssignment{
				{Selector: "space", SpaceID: "11111111-1111-4111-8111-111111111111", Effect: "allow"},
			},
		},
	}
	aggregateProvider := &fakeAggregateProvider{result: domain.AggregateProviderResult{Content: "scene output", Sensitivity: "normal", ProviderIdentity: "fake/local", ModelIdentity: "fake-v1"}}
	d, err := startService(context.Background(), cfg, jobs.NoopProcessor{}, aggregateProvider, false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "aggregate-space", map[string]any{"space_type": "user", "external_id": "aggregate", "display_name": "Aggregate"})
	response := requestJSON(t, client, http.MethodPost, "/v1/events", "aggregate-event", map[string]any{"space_id": space.SpaceID, "source_type": "manual", "source_name": "test", "occurred_at": "2026-08-25T00:00:00Z", "content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "Alpha and Beta"}})
	var event domain.IngestResponse
	decodeResponse(t, response, &event)
	a, _ := createMemory(t, client, "aggregate-a", memoryCreateBody(space.SpaceID, event.EventID, "Alpha"))
	b, _ := createMemory(t, client, "aggregate-b", memoryCreateBody(space.SpaceID, event.EventID, "Beta"))
	build := map[string]any{"space_id": space.SpaceID, "aggregate_kind": "scene", "input_revision_ids": []string{b.CurrentRevision.RevisionID, a.CurrentRevision.RevisionID}}
	response = requestJSON(t, client, http.MethodPost, "/v1/aggregate-proposals", "aggregate-build", build)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("build status=%d", response.StatusCode)
	}
	var proposal domain.AggregateProposal
	decodeResponse(t, response, &proposal)
	if len(aggregateProvider.calls) != 1 || proposal.ProviderIdentity == nil || proposal.ModelIdentity == nil || proposal.InputFingerprint == "" {
		t.Fatalf("proposal=%+v calls=%+v", proposal, aggregateProvider.calls)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/aggregate-proposals?space_id="+url.QueryEscape(space.SpaceID), "", nil)
	var proposals domain.AggregateProposalPage
	decodeResponse(t, response, &proposals)
	if len(proposals.Items) != 1 {
		t.Fatalf("proposals=%+v", proposals)
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/aggregate-proposals/"+proposal.ProposalID+"/confirm", "aggregate-confirm", map[string]any{"reason_code": "confirmed"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("confirm status=%d", response.StatusCode)
	}
	decodeResponse(t, response, &proposal)
	if proposal.ResultMemoryID == nil {
		t.Fatalf("confirmed=%+v", proposal)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/aggregates?space_id="+url.QueryEscape(space.SpaceID), "", nil)
	var aggregates domain.AggregatePage
	decodeResponse(t, response, &aggregates)
	if len(aggregates.Items) != 1 || len(aggregates.Items[0].Provenance) != 2 || aggregates.Items[0].AggregateStale {
		t.Fatalf("aggregates=%+v", aggregates)
	}
	aggregateID := aggregates.Items[0].AggregateID
	response = requestJSON(t, client, http.MethodGet, "/v1/aggregates/"+aggregateID+"/provenance", "", nil)
	var provenance []domain.AggregateProvenance
	decodeResponse(t, response, &provenance)
	if len(provenance) != 2 {
		t.Fatalf("provenance=%+v", provenance)
	}
	revise := memoryReviseBody(a.CurrentRevision.RevisionID, event.EventID, "Alpha changed")
	response = requestJSON(t, client, http.MethodPatch, "/v1/memories/"+a.MemoryID, "aggregate-input-revise", revise)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revise status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = requestJSON(t, client, http.MethodGet, "/v1/aggregates/"+aggregateID, "", nil)
	var result domain.Aggregate
	decodeResponse(t, response, &result)
	if !result.AggregateStale || len(result.StaleReasons) == 0 {
		t.Fatalf("stale aggregate=%+v", result)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/aggregates?space_id="+url.QueryEscape(space.SpaceID)+"&exclude_stale=true", "", nil)
	decodeResponse(t, response, &aggregates)
	if len(aggregates.Items) != 0 {
		t.Fatalf("excluded stale=%+v", aggregates)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/health", "", nil)
	var health api.Health
	decodeResponse(t, response, &health)
	if health.AggregateState != "stale" || health.AggregateStaleCount != 1 || health.AggregatePendingRebuilds != 1 {
		t.Fatalf("health=%+v", health)
	}
}

func TestAggregateOffline(t *testing.T) {
	cfg := testConfig(t)
	d, err := StartStaging(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "aggregate-offline-space", map[string]any{"space_type": "user", "external_id": "aggregate-offline", "display_name": "Aggregate Offline"})
	response := requestJSON(t, client, http.MethodPost, "/v1/aggregate-proposals", "aggregate-offline", map[string]any{"space_id": space.SpaceID, "aggregate_kind": "scene", "input_revision_ids": []string{"00000000-0000-4000-8000-000000000001"}})
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("offline aggregate endpoint status=%d, want %d", response.StatusCode, http.StatusNotFound)
	}
	var apiError api.Error
	decodeResponse(t, response, &apiError)
	if apiError.ErrorCode != "unsupported" {
		t.Fatalf("offline aggregate endpoint error=%+v, want code not_found", apiError)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/health", "", nil)
	var health api.Health
	decodeResponse(t, response, &health)
	if health.AggregateState != "ready" || health.AggregateFreshCount != 0 || health.AggregateStaleCount != 0 || health.AggregatePendingRebuilds != 0 {
		t.Fatalf("aggregate health=%+v", health)
	}
}
