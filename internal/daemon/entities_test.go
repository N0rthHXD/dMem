package daemon

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestEntityManualCapabilityWithoutProvider(t *testing.T) {
	cfg := testConfig(t)
	d, err := StartStaging(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "entity-space", map[string]any{"space_type": "user", "external_id": "entity", "display_name": "Entity"})
	response := requestJSON(t, client, http.MethodPost, "/v1/events", "entity-event", map[string]any{"space_id": space.SpaceID, "source_type": "manual", "source_name": "test", "occurred_at": "2026-08-25T00:00:00Z", "content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "Alice works here"}})
	var event domain.IngestResponse
	decodeResponse(t, response, &event)
	body := map[string]any{"event_id": event.EventID, "space_id": space.SpaceID, "proposal_kind": "entity", "entity_type": "person", "canonical_name": "Alice", "sensitivity": "normal", "processor_identity": "processor/api", "processing_version": "entity-v1"}
	response = requestJSON(t, client, http.MethodPost, "/v1/entity-proposals", "entity-proposal", body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create proposal status=%d", response.StatusCode)
	}
	var proposal domain.EntityProposal
	decodeResponse(t, response, &proposal)
	response = requestJSON(t, client, http.MethodGet, "/v1/entity-proposals?space_id="+url.QueryEscape(space.SpaceID), "", nil)
	var proposals domain.EntityProposalPage
	decodeResponse(t, response, &proposals)
	if len(proposals.Items) != 1 {
		t.Fatalf("proposal list=%+v", proposals)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/entity-proposals/"+proposal.ProposalID, "", nil)
	decodeResponse(t, response, &proposal)
	review := map[string]any{"reason_code": "confirmed"}
	response = requestJSON(t, client, http.MethodPost, "/v1/entity-proposals/"+proposal.ProposalID+"/confirm", "entity-confirm", review)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("confirm status=%d", response.StatusCode)
	}
	decodeResponse(t, response, &proposal)
	if proposal.ResultEntityID == nil {
		t.Fatalf("confirmed proposal=%+v", proposal)
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/entity-proposals/"+proposal.ProposalID+"/confirm", "entity-confirm", review)
	if response.StatusCode != http.StatusCreated || response.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("confirm replay=%d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	response.Body.Close()
	response = requestJSON(t, client, http.MethodGet, "/v1/entities?space_id="+url.QueryEscape(space.SpaceID)+"&include_mentions=true", "", nil)
	var entities domain.EntityPage
	decodeResponse(t, response, &entities)
	if len(entities.Items) != 1 || len(entities.Items[0].Aliases) != 1 {
		t.Fatalf("entities=%+v", entities)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/entities/"+*proposal.ResultEntityID+"?space_id="+url.QueryEscape(space.SpaceID)+"&include_mentions=true", "", nil)
	var entity domain.Entity
	decodeResponse(t, response, &entity)
	if entity.CanonicalName != "Alice" || len(entity.Mentions) != 0 {
		t.Fatalf("entity=%+v", entity)
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/entities/"+*proposal.ResultEntityID+"/merge", "merge", map[string]any{})
	if response.StatusCode != http.StatusNotImplemented {
		t.Fatalf("merge status=%d", response.StatusCode)
	}
	var apiErr api.Error
	decodeResponse(t, response, &apiErr)
	if apiErr.ErrorCode != "unsupported_operation" {
		t.Fatalf("merge error=%+v", apiErr)
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/indexes/entities/rebuild", "entity-rebuild-active", map[string]any{})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate rebuild status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = requestJSON(t, client, http.MethodGet, "/v1/health", "", nil)
	var health api.Health
	decodeResponse(t, response, &health)
	if health.EntityIndexState != "stale" || health.EntityIndexVersion < 1 || health.Mode != "degraded" {
		t.Fatalf("entity health=%+v", health)
	}
}
