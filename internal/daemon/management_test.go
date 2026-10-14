package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

// managementMemory reads one management memory object and returns its
// current_state and current revision id.
func managementMemory(t *testing.T, handler http.Handler, memoryID string) (string, string) {
	t.Helper()
	recorder := callHandler(t, handler, http.MethodGet, "/v1/memories/"+memoryID, "", nil, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("get memory status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		CurrentState    string `json:"current_state"`
		CurrentRevision struct {
			RevisionID string `json:"revision_id"`
		} `json:"current_revision"`
	}
	decodeInto(t, recorder, &body)
	return body.CurrentState, body.CurrentRevision.RevisionID
}

// TestManagementSurfaceClosesMemoryAdministrationLoop drives the management
// memory surface through the assembled runtime: list, detail, revision,
// lifecycle, state events, and the deleted-memory audit read.
func TestManagementSurfaceClosesMemoryAdministrationLoop(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)
	scopeID := ensureScope(t, handler, "management")

	commit := callHandler(t, handler, http.MethodPost, "/v1/memory-commits", "management-commit", memoryCommitBody(scopeID, "administrative keyword memory"), true)
	if commit.Code != http.StatusCreated {
		t.Fatalf("commit status = %d: %s", commit.Code, commit.Body.String())
	}
	var commitResponse toolcontract.MemoryCommitOutput
	decodeInto(t, commit, &commitResponse)
	memoryID, revisionID, eventID := commitResponse.Memories[0].MemoryID, commitResponse.Memories[0].RevisionID, commitResponse.SourceEventID
	if memoryID == "" || revisionID == "" || eventID == "" {
		t.Fatalf("commit response = %+v", commitResponse)
	}

	list := callHandler(t, handler, http.MethodGet, "/v1/memories?scope_id="+scopeID, "", nil, true)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", list.Code, list.Body.String())
	}
	var page struct {
		Items []struct {
			MemoryID string `json:"memory_id"`
			ScopeID  string `json:"scope_id"`
		} `json:"items"`
	}
	decodeInto(t, list, &page)
	if len(page.Items) != 1 || page.Items[0].MemoryID != memoryID || page.Items[0].ScopeID != scopeID {
		t.Fatalf("list page = %+v", page)
	}

	revise := callHandler(t, handler, http.MethodPost, "/v1/memories/"+memoryID+"/revisions", "management-revise", map[string]any{
		"expected_revision_id": revisionID,
		"content":              "revised administrative keyword memory",
		"reason":               "management edit",
		"evidence":             []map[string]any{{"event_id": eventID, "role": "direct"}},
	}, true)
	if revise.Code != http.StatusOK {
		t.Fatalf("revise status = %d: %s", revise.Code, revise.Body.String())
	}
	var reviseResponse struct {
		OperationID string `json:"operation_id"`
		Replayed    bool   `json:"replayed"`
		Memory      struct {
			CurrentRevision struct {
				RevisionID string `json:"revision_id"`
			} `json:"current_revision"`
		} `json:"memory"`
	}
	decodeInto(t, revise, &reviseResponse)
	if reviseResponse.OperationID == "" || reviseResponse.Replayed || reviseResponse.Memory.CurrentRevision.RevisionID == revisionID {
		t.Fatalf("revise response = %+v", reviseResponse)
	}
	revisedID := reviseResponse.Memory.CurrentRevision.RevisionID

	archiveBody := map[string]any{"expected_revision_id": revisedID, "action": "archive", "reason": "management cleanup"}
	archive := callHandler(t, handler, http.MethodPost, "/v1/memories/"+memoryID+"/state", "management-archive", archiveBody, true)
	if archive.Code != http.StatusOK || archive.Header().Get("Idempotency-Replayed") != "false" {
		t.Fatalf("archive status = %d headers=%v: %s", archive.Code, archive.Header(), archive.Body.String())
	}
	replayed := callHandler(t, handler, http.MethodPost, "/v1/memories/"+memoryID+"/state", "management-archive", archiveBody, true)
	if replayed.Code != http.StatusOK || replayed.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("archive replay status = %d headers=%v: %s", replayed.Code, replayed.Header(), replayed.Body.String())
	}
	state, _ := managementMemory(t, handler, memoryID)
	if state != "archived" {
		t.Fatalf("archived state = %q", state)
	}

	restored := callHandler(t, handler, http.MethodPost, "/v1/memories/"+memoryID+"/state", "management-restore", map[string]any{
		"expected_revision_id": revisedID, "action": "restore", "restore_revision_id": revisionID, "reason": "management restore",
	}, true)
	if restored.Code != http.StatusOK {
		t.Fatalf("restore status = %d: %s", restored.Code, restored.Body.String())
	}
	state, currentID := managementMemory(t, handler, memoryID)
	if state != "active" || currentID == revisedID {
		t.Fatalf("restored state = %s revision=%s", state, currentID)
	}

	stale := callHandler(t, handler, http.MethodPost, "/v1/memories/"+memoryID+"/state", "management-stale", map[string]any{
		"expected_revision_id": revisedID, "action": "archive", "reason": "stale fence",
	}, true)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale fence status = %d: %s", stale.Code, stale.Body.String())
	}

	revisions := callHandler(t, handler, http.MethodGet, "/v1/memories/"+memoryID+"/revisions", "", nil, true)
	var revisionPage struct {
		Items []json.RawMessage `json:"items"`
	}
	decodeInto(t, revisions, &revisionPage)
	if revisions.Code != http.StatusOK || len(revisionPage.Items) != 3 {
		t.Fatalf("revision history = %+v status=%d", revisionPage, revisions.Code)
	}
	events := callHandler(t, handler, http.MethodGet, "/v1/memories/"+memoryID+"/state-events", "", nil, true)
	var eventPage struct {
		Items []struct {
			ToState string `json:"to_state"`
		} `json:"items"`
	}
	decodeInto(t, events, &eventPage)
	if events.Code != http.StatusOK || len(eventPage.Items) != 3 || eventPage.Items[0].ToState != "active" {
		t.Fatalf("state events = %+v status=%d", eventPage, events.Code)
	}

	forget := callHandler(t, handler, http.MethodPost, "/v1/forget", "management-forget", map[string]any{
		"scope_id": scopeID, "memory_id": memoryID, "expected_revision_id": currentID, "reason": "management delete",
	}, true)
	if forget.Code != http.StatusOK {
		t.Fatalf("forget status = %d: %s", forget.Code, forget.Body.String())
	}
	state, _ = managementMemory(t, handler, memoryID)
	if state != "deleted" {
		t.Fatalf("deleted state = %q", state)
	}
	deleted := callHandler(t, handler, http.MethodGet, "/v1/memories?scope_id="+scopeID+"&state=deleted", "", nil, true)
	var deletedPage struct {
		Items []json.RawMessage `json:"items"`
	}
	decodeInto(t, deleted, &deletedPage)
	if deleted.Code != http.StatusOK || len(deletedPage.Items) != 1 {
		t.Fatalf("deleted list = %+v status=%d", deletedPage, deleted.Code)
	}
}

// TestManagementSurfaceExposesProposalAndConflictQueues proves the proposal and
// conflict management routes are wired through the assembled runtime and map
// empty and missing objects correctly.
func TestManagementSurfaceExposesProposalAndConflictQueues(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)
	scopeID := ensureScope(t, handler, "management-queues")

	proposals := callHandler(t, handler, http.MethodGet, "/v1/proposals?scope_id="+scopeID+"&proposal_type=fact&review_state=pending", "", nil, true)
	if proposals.Code != http.StatusOK {
		t.Fatalf("proposal list status = %d: %s", proposals.Code, proposals.Body.String())
	}
	var proposalPage struct {
		Items      []json.RawMessage `json:"items"`
		NextCursor *string           `json:"next_cursor"`
	}
	decodeInto(t, proposals, &proposalPage)
	if len(proposalPage.Items) != 0 || proposalPage.NextCursor != nil {
		t.Fatalf("proposal page = %+v", proposalPage)
	}
	badType := callHandler(t, handler, http.MethodGet, "/v1/proposals?scope_id="+scopeID+"&proposal_type=bogus", "", nil, true)
	if badType.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad proposal type status = %d: %s", badType.Code, badType.Body.String())
	}
	missingProposal := callHandler(t, handler, http.MethodGet, "/v1/proposals/fact/missing", "", nil, true)
	if missingProposal.Code != http.StatusNotFound {
		t.Fatalf("missing proposal status = %d: %s", missingProposal.Code, missingProposal.Body.String())
	}

	conflicts := callHandler(t, handler, http.MethodGet, "/v1/conflicts?scope_id="+scopeID, "", nil, true)
	if conflicts.Code != http.StatusOK {
		t.Fatalf("conflict list status = %d: %s", conflicts.Code, conflicts.Body.String())
	}
	missingConflict := callHandler(t, handler, http.MethodGet, "/v1/conflicts/missing", "", nil, true)
	if missingConflict.Code != http.StatusNotFound {
		t.Fatalf("missing conflict status = %d: %s", missingConflict.Code, missingConflict.Body.String())
	}
	resolveMissing := callHandler(t, handler, http.MethodPost, "/v1/conflicts/missing/resolve", "conflict-resolve", map[string]any{
		"resolution_type":  "keep_both",
		"expected_targets": []map[string]any{},
		"result_states":    map[string]string{},
		"reason":           "none",
	}, true)
	if resolveMissing.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid resolve status = %d: %s", resolveMissing.Code, resolveMissing.Body.String())
	}
}
