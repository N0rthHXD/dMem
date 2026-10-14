package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
)

func memoryHandler(t *testing.T, memories *fakeMemories) *Handler {
	t.Helper()
	handler, err := New(Dependencies{AgentTools: &fakeTools{}, Scopes: &fakeScopes{}, Events: &fakeEvents{}, Reviews: &fakeReviews{}, Memories: memories, Conflicts: &fakeConflicts{}, Proposals: &fakeProposals{}})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestMemoryAdministrationReadsUseManagementSurface(t *testing.T) {
	memories := &fakeMemories{}
	handler := memoryHandler(t, memories)

	list := request(t, handler, http.MethodGet, "/v1/memories?scope_id=scope&state=archived&include_ancestors=true&limit=5", "", caller())
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", list.Code, list.Body.String())
	}
	var page memoryListOutput
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].MemoryID != "memory-1" || page.Items[0].ScopeID != "scope" {
		t.Fatalf("list output = %+v", page)
	}
	query, ok := memories.command.(memory.ListQuery)
	if !ok || query.SpaceID != "scope" || query.State != "archived" || !query.IncludeAncestors || query.Page.Limit != 5 {
		t.Fatalf("list query = %+v", memories.command)
	}

	get := request(t, handler, http.MethodGet, "/v1/memories/memory-1", "", caller())
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", get.Code, get.Body.String())
	}
	var single memoryOutput
	if err := json.Unmarshal(get.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if single.MemoryID != "memory-1" || single.ScopeID != "scope" || single.CurrentRevision.RevisionID != "revision-1" {
		t.Fatalf("get output = %+v", single)
	}

	revisions := request(t, handler, http.MethodGet, "/v1/memories/memory-1/revisions", "", caller())
	if revisions.Code != http.StatusOK {
		t.Fatalf("revisions status = %d: %s", revisions.Code, revisions.Body.String())
	}
	var revisionList memoryRevisionListOutput
	if err := json.Unmarshal(revisions.Body.Bytes(), &revisionList); err != nil {
		t.Fatal(err)
	}
	if len(revisionList.Items) != 1 || revisionList.Items[0].RevisionID != "revision-1" {
		t.Fatalf("revisions output = %+v", revisionList)
	}

	events := request(t, handler, http.MethodGet, "/v1/memories/memory-1/state-events", "", caller())
	if events.Code != http.StatusOK {
		t.Fatalf("state events status = %d: %s", events.Code, events.Body.String())
	}
	var eventList memoryStateEventListOutput
	if err := json.Unmarshal(events.Body.Bytes(), &eventList); err != nil {
		t.Fatal(err)
	}
	if len(eventList.Items) != 1 || eventList.Items[0].StateEventID != "state-event-1" {
		t.Fatalf("state events output = %+v", eventList)
	}
}

func TestMemoryAdministrationReviseMapsCommand(t *testing.T) {
	memories := &fakeMemories{}
	handler := memoryHandler(t, memories)

	response := request(t, handler, http.MethodPost, "/v1/memories/memory-1/revisions",
		`{"expected_revision_id":"revision-1","content":"new content","valid_from":"2026-01-01T00:00:00Z","evidence":[{"event_id":"event-1","role":"derived","start_offset":0,"end_offset":4}],"reason":"fix typo"}`,
		writes())
	if response.Code != http.StatusOK {
		t.Fatalf("revise status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get(replayHeader) != "true" {
		t.Fatal("revise did not publish the replay header")
	}
	var output memoryWriteOutput
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.OperationID != "op" || !output.Replayed || output.Memory.MemoryID != "memory-1" {
		t.Fatalf("revise output = %+v", output)
	}
	command, ok := memories.command.(memory.ReviseCommand)
	if !ok || command.MemoryID != "memory-1" || command.ExpectedRevisionID != "revision-1" || command.Content != "new content" || command.Reason != "fix typo" {
		t.Fatalf("revise command = %+v", memories.command)
	}
	if command.ValidFrom == nil || *command.ValidFrom != "2026-01-01T00:00:00Z" {
		t.Fatalf("revise valid_from = %+v", command.ValidFrom)
	}
	if len(command.Evidence) != 1 || command.Evidence[0].EventID != "event-1" || string(command.Evidence[0].Role) != "derived" || *command.Evidence[0].StartOffset != 0 || *command.Evidence[0].EndOffset != 4 {
		t.Fatalf("revise evidence = %+v", command.Evidence)
	}
}

func TestMemoryAdministrationRevisePreservesEmptyEvidence(t *testing.T) {
	memories := &fakeMemories{}
	handler := memoryHandler(t, memories)

	response := request(t, handler, http.MethodPost, "/v1/memories/memory-1/revisions",
		`{"expected_revision_id":"revision-1","content":"new content","evidence":[],"reason":"fix typo"}`, writes())
	if response.Code != http.StatusOK {
		t.Fatalf("revise status = %d: %s", response.Code, response.Body.String())
	}
	command := memories.command.(memory.ReviseCommand)
	if command.Evidence == nil || len(command.Evidence) != 0 {
		t.Fatalf("evidence = %#v; want an explicitly empty list", command.Evidence)
	}

	memories.command = nil
	response = request(t, handler, http.MethodPost, "/v1/memories/memory-1/revisions",
		`{"expected_revision_id":"revision-1","content":"new content","reason":"fix typo"}`, writes())
	if response.Code != http.StatusOK {
		t.Fatalf("revise status = %d: %s", response.Code, response.Body.String())
	}
	if command := memories.command.(memory.ReviseCommand); command.Evidence != nil {
		t.Fatalf("evidence = %#v; want nil to inherit the current revision", command.Evidence)
	}
}

func TestMemoryAdministrationStateChangeMapsCommand(t *testing.T) {
	memories := &fakeMemories{}
	handler := memoryHandler(t, memories)

	response := request(t, handler, http.MethodPost, "/v1/memories/memory-1/state",
		`{"expected_revision_id":"revision-1","action":"restore","restore_revision_id":"revision-0","reason":"recover"}`, writes())
	if response.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", response.Code, response.Body.String())
	}
	command, ok := memories.command.(memory.StateChangeCommand)
	if !ok || command.MemoryID != "memory-1" || command.Action != memory.StateRestore || command.RestoreRevisionID == nil || *command.RestoreRevisionID != "revision-0" {
		t.Fatalf("state command = %+v", memories.command)
	}
	var output memoryWriteOutput
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Memory.CurrentState != "archived" {
		t.Fatalf("state output = %+v", output)
	}
}

func TestMemoryAdministrationRequiresIdentityAndIdempotency(t *testing.T) {
	handler := memoryHandler(t, &fakeMemories{})

	if response := request(t, handler, http.MethodGet, "/v1/memories?scope_id=scope", "", nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("list without caller status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/memories/memory-1/state", `{"expected_revision_id":"r","action":"archive","reason":"r"}`, caller()); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("state without idempotency status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/memories/memory-1/state", `{"expected_revision_id":"r","action":"archive","reason":"r"}`, nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("state without caller status = %d: %s", response.Code, response.Body.String())
	}
}

func TestMemoryAdministrationStrictBodyAndMethods(t *testing.T) {
	handler := memoryHandler(t, &fakeMemories{})

	if response := request(t, handler, http.MethodPost, "/v1/memories/memory-1/revisions", `{"content":"c","extra":1}`, writes()); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodGet, "/v1/memories/memory-1", "{}", caller()); response.Code != http.StatusBadRequest {
		t.Fatalf("read body status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/memories", `{}`, writes()); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("collection POST status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodGet, "/v1/memories?scope_id=scope&include_ancestors=maybe", "", caller()); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid filter status = %d: %s", response.Code, response.Body.String())
	}
}

func TestMemoryAdministrationClassifiesGatewayErrors(t *testing.T) {
	memories := &fakeMemories{}
	handler := memoryHandler(t, memories)

	memories.err = gateway.NewError(gateway.ErrorNotFound, "memory not found")
	if response := request(t, handler, http.MethodGet, "/v1/memories/missing", "", caller()); response.Code != http.StatusNotFound {
		t.Fatalf("missing memory status = %d: %s", response.Code, response.Body.String())
	}
	memories.err = gateway.NewError(gateway.ErrorConflict, "expected revision is not current")
	if response := request(t, handler, http.MethodPost, "/v1/memories/memory-1/state", `{"expected_revision_id":"revision-1","action":"archive","reason":"r"}`, writes()); response.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d: %s", response.Code, response.Body.String())
	}
}

func TestMemoryDTOReportsScopeAndAggregateState(t *testing.T) {
	t.Parallel()
	marshaled, err := json.Marshal(memoryDTO(memoryTestValue()))
	if err != nil {
		t.Fatal(err)
	}
	body := string(marshaled)
	if !strings.Contains(body, `"scope_id":"scope-1"`) || strings.Contains(body, `"space_id"`) {
		t.Fatalf("memory DTO JSON = %s; want scope_id and no space_id", body)
	}
	if !strings.Contains(body, `"aggregate_stale":true`) || !strings.Contains(body, `"aggregate_stale_reasons":["input-revised"]`) {
		t.Fatalf("memory DTO JSON = %s; want aggregate freshness", body)
	}
}

func memoryTestValue() domain.Memory {
	return domain.Memory{
		MemoryID: "memory-1", SpaceID: "scope-1", CurrentState: "active", AggregateStale: true,
		AggregateStaleReasons: []string{"input-revised"}, CurrentRevision: domain.MemoryRevision{RevisionID: "revision-1"},
	}
}
