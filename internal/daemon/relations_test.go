package daemon

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestRelationManualCapabilityWithoutProvider(t *testing.T) {
	cfg := testConfig(t)
	d, err := StartStaging(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "relation-space", map[string]any{"space_type": "user", "external_id": "relation", "display_name": "Relation"})
	response := requestJSON(t, client, http.MethodPost, "/v1/events", "relation-event", map[string]any{"space_id": space.SpaceID, "source_type": "manual", "source_name": "test", "occurred_at": "2026-08-25T00:00:00Z", "content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "Alpha supports Beta"}})
	var event domain.IngestResponse
	decodeResponse(t, response, &event)
	source, _ := createMemory(t, client, "relation-source", memoryCreateBody(space.SpaceID, event.EventID, "Alpha"))
	target, _ := createMemory(t, client, "relation-target", memoryCreateBody(space.SpaceID, event.EventID, "Beta"))
	body := map[string]any{"event_id": event.EventID, "space_id": space.SpaceID, "relation_type": "supports", "source_memory_id": source.MemoryID, "source_revision_id": source.CurrentRevision.RevisionID, "target_memory_id": target.MemoryID, "target_revision_id": target.CurrentRevision.RevisionID, "evidence": []map[string]any{{"evidence_type": "human_confirmation"}}, "processor_identity": "processor/api", "processing_version": "relation-v1"}
	response = requestJSON(t, client, http.MethodPost, "/v1/relation-proposals", "relation-proposal", body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create relation proposal status=%d", response.StatusCode)
	}
	var proposal domain.RelationProposal
	decodeResponse(t, response, &proposal)
	response = requestJSON(t, client, http.MethodGet, "/v1/relation-proposals?space_id="+url.QueryEscape(space.SpaceID), "", nil)
	var proposals domain.RelationProposalPage
	decodeResponse(t, response, &proposals)
	if len(proposals.Items) != 1 {
		t.Fatalf("proposals=%+v", proposals)
	}
	review := map[string]any{"reason_code": "confirmed"}
	response = requestJSON(t, client, http.MethodPost, "/v1/relation-proposals/"+proposal.ProposalID+"/confirm", "relation-confirm", review)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("confirm status=%d", response.StatusCode)
	}
	decodeResponse(t, response, &proposal)
	if proposal.ResultRelationID == nil {
		t.Fatalf("proposal=%+v", proposal)
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/relation-proposals/"+proposal.ProposalID+"/confirm", "relation-confirm", review)
	if response.StatusCode != http.StatusCreated || response.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay=%d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	response.Body.Close()
	response = requestJSON(t, client, http.MethodGet, "/v1/relations/"+*proposal.ResultRelationID+"?space_id="+url.QueryEscape(space.SpaceID), "", nil)
	var relation domain.MemoryRelation
	decodeResponse(t, response, &relation)
	if relation.RelationID != *proposal.ResultRelationID || len(relation.Evidence) != 1 {
		t.Fatalf("relation=%+v", relation)
	}
	baseQuery := "/v1/relations?space_id=" + url.QueryEscape(space.SpaceID) + "&memory_id=" + url.QueryEscape(source.MemoryID) + "&direction=outgoing&relation_type="
	response = requestJSON(t, client, http.MethodGet, baseQuery+"supports", "", nil)
	var page domain.RelationPage
	decodeResponse(t, response, &page)
	if len(page.Items) != 1 {
		t.Fatalf("one hop=%+v", page)
	}
	response = requestJSON(t, client, http.MethodGet, baseQuery+"contradicts", "", nil)
	decodeResponse(t, response, &page)
	if len(page.Items) != 0 {
		t.Fatalf("relation type filter returned non-matching relations: %+v", page)
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/indexes/relations/rebuild", "relation-rebuild-active", map[string]any{})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("active rebuild=%d", response.StatusCode)
	}
	response.Body.Close()
	response = requestJSON(t, client, http.MethodGet, "/v1/health", "", nil)
	var health api.Health
	decodeResponse(t, response, &health)
	if health.RelationIndexState != "stale" || health.RelationIndexVersion < 1 || health.Mode != "degraded" {
		t.Fatalf("health=%+v", health)
	}
}
