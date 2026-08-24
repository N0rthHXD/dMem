package daemon

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestEvolutionAndConflictDaemonHandlers(t *testing.T) {
	cfg := testConfig(t)
	d, release, client := startHeldDaemon(t, cfg)
	defer func() { close(release); _ = d.Close(context.Background()) }()
	space := createSpace(t, client, "evolution-handler-space", map[string]any{"space_type": "user", "external_id": "evolution-handler", "display_name": "Evolution Handler"})
	event := ingestManualEvent(t, client, "evolution-handler-event", space.SpaceID, "source evidence for evolution")
	first, _ := createMemory(t, client, "evolution-handler-first", memoryCreateBody(space.SpaceID, event.EventID, "first conflict searchable memory"))
	second, _ := createMemory(t, client, "evolution-handler-second", memoryCreateBody(space.SpaceID, event.EventID, "second conflict searchable memory"))
	start, end := 0, 6
	evidence := []map[string]any{{"evidence_type": "raw_event_range", "event_id": event.EventID, "start_offset": start, "end_offset": end}}
	targets := []map[string]any{{"memory_id": first.MemoryID, "expected_revision_id": first.CurrentRevision.RevisionID}, {"memory_id": second.MemoryID, "expected_revision_id": second.CurrentRevision.RevisionID}}
	body := map[string]any{"event_id": event.EventID, "space_id": space.SpaceID, "action": "conflict", "proposed_content": "these memories conflict", "sensitivity": "normal", "processor_identity": "daemon/test", "processing_version": "evolution-v1", "targets": targets, "evidence": evidence}
	response := requestJSON(t, client, http.MethodPost, "/v1/evolution-proposals", "daemon-evolution-create", body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create evolution status=%d", response.StatusCode)
	}
	var proposal domain.EvolutionProposal
	decodeResponse(t, response, &proposal)
	response = requestJSON(t, client, http.MethodGet, "/v1/evolution-proposals?space_id="+url.QueryEscape(space.SpaceID), "", nil)
	var page domain.EvolutionProposalPage
	decodeResponse(t, response, &page)
	if len(page.Items) != 1 || page.Items[0].ProposalID != proposal.ProposalID {
		t.Fatalf("evolution page=%+v", page)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/evolution-proposals/"+proposal.ProposalID, "", nil)
	response.Body.Close()
	review := map[string]any{"expected_targets": targets, "reason_code": "reviewed"}
	response = requestJSON(t, client, http.MethodPost, "/v1/evolution-proposals/"+proposal.ProposalID+"/confirm", "daemon-evolution-confirm", review)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("confirm evolution status=%d", response.StatusCode)
	}
	decodeResponse(t, response, &proposal)
	caseID := ""
	for _, ref := range proposal.Reviews[0].Results {
		if ref.ResultType == "conflict_case" {
			caseID = ref.ResultID
		}
	}
	if caseID == "" {
		t.Fatalf("confirmed proposal has no conflict case: %+v", proposal)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/conflict-cases?space_id="+url.QueryEscape(space.SpaceID), "", nil)
	var cases domain.ConflictCasePage
	decodeResponse(t, response, &cases)
	if len(cases.Items) != 1 || cases.Items[0].CaseID != caseID {
		t.Fatalf("conflict cases=%+v", cases)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/conflict-cases/"+caseID, "", nil)
	var conflict domain.ConflictCase
	decodeResponse(t, response, &conflict)
	resolve := map[string]any{"resolution_type": "keep_both", "expected_targets": targets, "result_states": map[string]string{first.MemoryID: "active", second.MemoryID: "active"}, "reason": "both remain useful"}
	response = requestJSON(t, client, http.MethodPost, "/v1/conflict-cases/"+caseID+"/resolve", "daemon-conflict-resolve", resolve)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("resolve conflict status=%d", response.StatusCode)
	}
	decodeResponse(t, response, &conflict)
	if conflict.State != "resolved" || conflict.Resolution == nil {
		t.Fatalf("resolved conflict=%+v", conflict)
	}
}
