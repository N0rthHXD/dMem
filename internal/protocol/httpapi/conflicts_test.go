package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/conflict"
)

func conflictHandler(t *testing.T, conflicts *fakeConflicts) *Handler {
	t.Helper()
	handler, err := New(Dependencies{AgentTools: &fakeTools{}, Scopes: &fakeScopes{}, Events: &fakeEvents{}, Reviews: &fakeReviews{}, Memories: &fakeMemories{}, Conflicts: conflicts, Proposals: &fakeProposals{}})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestConflictAdministrationRoutes(t *testing.T) {
	conflicts := &fakeConflicts{}
	handler := conflictHandler(t, conflicts)

	list := request(t, handler, http.MethodGet, "/v1/conflicts?scope_id=scope&state=open&limit=5", "", caller())
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", list.Code, list.Body.String())
	}
	var page conflictListOutput
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].CaseID != "case-1" || page.Items[0].ScopeID != "scope" || len(page.Items[0].Participants) != 2 {
		t.Fatalf("list output = %+v", page)
	}
	query, ok := conflicts.command.(conflict.ListQuery)
	if !ok || query.SpaceID != "scope" || query.State != "open" || query.Page.Limit != 5 {
		t.Fatalf("list query = %+v", conflicts.command)
	}

	get := request(t, handler, http.MethodGet, "/v1/conflicts/case-1", "", caller())
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", get.Code, get.Body.String())
	}
	var single conflictOutput
	if err := json.Unmarshal(get.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if single.CaseID != "case-1" || single.ScopeID != "scope" || single.State != "open" {
		t.Fatalf("get output = %+v", single)
	}
}

func TestConflictResolveMapsCommand(t *testing.T) {
	conflicts := &fakeConflicts{}
	handler := conflictHandler(t, conflicts)

	body := `{"resolution_type":"reconcile","expected_targets":[{"memory_id":"memory-1","expected_revision_id":"revision-1"},{"memory_id":"memory-2","expected_revision_id":"revision-2"}],"proposed_content":"reconciled","valid_from":"2026-01-01T00:00:00Z","evidence":[{"event_id":"event-1","role":"direct"}],"result_states":{"memory-1":"superseded","memory-2":"superseded"},"reason":"merge the two"}`
	response := request(t, handler, http.MethodPost, "/v1/conflicts/case-1/resolve", body, writes())
	if response.Code != http.StatusOK {
		t.Fatalf("resolve status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get(replayHeader) != "true" {
		t.Fatal("resolve did not publish the replay header")
	}
	var output conflictResolveOutput
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.OperationID != "op" || !output.Replayed || output.Case.State != "resolved" {
		t.Fatalf("resolve output = %+v", output)
	}
	command, ok := conflicts.command.(conflict.ResolveCommand)
	if !ok || command.CaseID != "case-1" || command.ResolutionType != conflict.ResolutionReconcile || command.Reason != "merge the two" {
		t.Fatalf("resolve command = %+v", conflicts.command)
	}
	if len(command.ExpectedTargets) != 2 || command.ExpectedTargets[0].MemoryID != "memory-1" || command.ExpectedTargets[1].ExpectedRevisionID != "revision-2" {
		t.Fatalf("resolve targets = %+v", command.ExpectedTargets)
	}
	if command.ProposedContent == nil || *command.ProposedContent != "reconciled" || command.ValidFrom == nil {
		t.Fatalf("resolve content = %+v", command)
	}
	if len(command.Evidence) != 1 || command.Evidence[0].EventID != "event-1" {
		t.Fatalf("resolve evidence = %+v", command.Evidence)
	}
	if command.ResultStates["memory-1"] != "superseded" || len(command.ResultStates) != 2 {
		t.Fatalf("resolve result states = %+v", command.ResultStates)
	}
}

func TestConflictResolveRequiresIdentityAndIdempotency(t *testing.T) {
	handler := conflictHandler(t, &fakeConflicts{})

	body := `{"resolution_type":"keep_both","expected_targets":[{"memory_id":"memory-1","expected_revision_id":"revision-1"}],"result_states":{"memory-1":"active"},"reason":"r"}`
	if response := request(t, handler, http.MethodPost, "/v1/conflicts/case-1/resolve", body, caller()); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("resolve without idempotency status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/conflicts/case-1/resolve", body, nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("resolve without caller status = %d: %s", response.Code, response.Body.String())
	}
}

func TestConflictRoutesStrictBodyAndMethods(t *testing.T) {
	handler := conflictHandler(t, &fakeConflicts{})

	if response := request(t, handler, http.MethodPost, "/v1/conflicts/case-1/resolve", `{"resolution_type":"keep_both","extra":1}`, writes()); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodGet, "/v1/conflicts/case-1", "{}", caller()); response.Code != http.StatusBadRequest {
		t.Fatalf("read body status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/conflicts", `{}`, writes()); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("collection POST status = %d: %s", response.Code, response.Body.String())
	}
}

func TestConflictAdministrationClassifiesGatewayErrors(t *testing.T) {
	conflicts := &fakeConflicts{}
	handler := conflictHandler(t, conflicts)

	conflicts.err = gateway.NewError(gateway.ErrorNotFound, "conflict case not found")
	if response := request(t, handler, http.MethodGet, "/v1/conflicts/missing", "", caller()); response.Code != http.StatusNotFound {
		t.Fatalf("missing case status = %d: %s", response.Code, response.Body.String())
	}
	conflicts.err = gateway.NewError(gateway.ErrorConflict, "conflict case is already resolved")
	if response := request(t, handler, http.MethodPost, "/v1/conflicts/case-1/resolve", `{"resolution_type":"keep_both","expected_targets":[{"memory_id":"memory-1","expected_revision_id":"revision-1"}],"result_states":{"memory-1":"active"},"reason":"r"}`, writes()); response.Code != http.StatusConflict {
		t.Fatalf("resolved case status = %d: %s", response.Code, response.Body.String())
	}
}

func TestConflictDTOReportsScopeAndResolution(t *testing.T) {
	t.Parallel()
	marshaled, err := json.Marshal(conflictDTO(domain.ConflictCase{
		CaseID: "case-1", SpaceID: "scope-1", State: "resolved",
		Participants: []domain.ConflictParticipant{{MemoryID: "memory-1", RevisionID: "revision-1"}},
		Resolution:   &domain.ConflictResolutionEvent{ResolutionEventID: "resolution-1", ResolutionType: "keep_both", FromState: "open", ToState: "resolved", Reason: "ok", ResolvedBy: "operator", CreatedAt: "2026-09-20T10:00:00Z"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	body := string(marshaled)
	if !strings.Contains(body, `"scope_id":"scope-1"`) || strings.Contains(body, `"space_id"`) {
		t.Fatalf("conflict DTO JSON = %s; want scope_id and no space_id", body)
	}
	if !strings.Contains(body, `"resolution_type":"keep_both"`) {
		t.Fatalf("conflict DTO JSON = %s; want the resolution event", body)
	}
}
