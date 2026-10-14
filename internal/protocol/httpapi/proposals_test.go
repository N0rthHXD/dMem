package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
)

func proposalHandler(t *testing.T, proposals *fakeProposals) *Handler {
	t.Helper()
	handler, err := New(Dependencies{AgentTools: &fakeTools{}, Scopes: &fakeScopes{}, Events: &fakeEvents{}, Reviews: &fakeReviews{}, Memories: &fakeMemories{}, Conflicts: &fakeConflicts{}, Proposals: proposals})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestProposalReviewListAndGet(t *testing.T) {
	proposals := &fakeProposals{}
	handler := proposalHandler(t, proposals)

	list := request(t, handler, http.MethodGet, "/v1/proposals?scope_id=scope&proposal_type=fact&review_state=pending&limit=5", "", caller())
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", list.Code, list.Body.String())
	}
	var page proposalListOutput
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || string(page.Items[0]["proposal_type"]) != `"fact"` || string(page.Items[0]["scope_id"]) != `"scope"` {
		t.Fatalf("list output = %+v", page)
	}
	if strings.Contains(string(page.Items[0]["proposal_type"]), "space_id") {
		t.Fatalf("list item leaked space_id: %+v", page.Items[0])
	}
	query, ok := proposals.command.(review.ListProposalsQuery)
	if !ok || query.SpaceID != "scope" || query.Kind != review.ProposalKindFact || query.ReviewState != "pending" || query.Page.Limit != 5 {
		t.Fatalf("list query = %+v", proposals.command)
	}

	get := request(t, handler, http.MethodGet, "/v1/proposals/entity/proposal-1", "", caller())
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", get.Code, get.Body.String())
	}
	var single map[string]json.RawMessage
	if err := json.Unmarshal(get.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if string(single["proposal_type"]) != `"entity"` || string(single["scope_id"]) != `"scope"` {
		t.Fatalf("get output = %+v", single)
	}
	if _, ok := single["space_id"]; ok {
		t.Fatalf("get output leaked space_id: %+v", single)
	}
	getQuery := proposals.command.(review.GetProposalQuery)
	if getQuery.Kind != review.ProposalKindEntity || getQuery.ProposalID != "proposal-1" {
		t.Fatalf("get query = %+v", getQuery)
	}
}

func TestProposalReviewConfirmMapsEvolutionInputs(t *testing.T) {
	proposals := &fakeProposals{}
	handler := proposalHandler(t, proposals)

	body := `{"reason_code":"reviewed","note":"lgtm","expected_targets":[{"memory_id":"memory-1","expected_revision_id":"revision-1"}],"replacement_mode":"new_memory","supersede_targets":true}`
	response := request(t, handler, http.MethodPost, "/v1/proposals/evolution/proposal-1/confirm", body, writes())
	if response.Code != http.StatusOK {
		t.Fatalf("confirm status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get(replayHeader) != "true" {
		t.Fatal("confirm did not publish the replay header")
	}
	var output proposalReviewOutput
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.OperationID != "op" || !output.Replayed || string(output.Proposal["proposal_type"]) != `"evolution"` {
		t.Fatalf("confirm output = %+v", output)
	}
	command, ok := proposals.command.(review.ReviewProposalCommand)
	if !ok || command.Kind != review.ProposalKindEvolution || command.ProposalID != "proposal-1" || command.ReasonCode != "reviewed" || command.Note == nil || *command.Note != "lgtm" {
		t.Fatalf("confirm command = %+v", proposals.command)
	}
	if len(command.ExpectedTargets) != 1 || command.ExpectedTargets[0].MemoryID != "memory-1" || command.ReplacementMode == nil || *command.ReplacementMode != "new_memory" || !command.SupersedeTargets {
		t.Fatalf("confirm evolution inputs = %+v", command)
	}

	reject := request(t, handler, http.MethodPost, "/v1/proposals/evolution/proposal-1/reject", `{"reason_code":"duplicate"}`, writes())
	if reject.Code != http.StatusOK {
		t.Fatalf("reject status = %d: %s", reject.Code, reject.Body.String())
	}
	if rejected := proposals.command.(review.ReviewProposalCommand); rejected.ExpectedTargets != nil || rejected.SupersedeTargets {
		t.Fatalf("reject inputs = %+v", rejected)
	}
}

func TestProposalReviewRequiresIdentityAndIdempotency(t *testing.T) {
	handler := proposalHandler(t, &fakeProposals{})

	if response := request(t, handler, http.MethodGet, "/v1/proposals?scope_id=scope&proposal_type=entity", "", nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("list without caller status = %d: %s", response.Code, response.Body.String())
	}
	body := `{"reason_code":"reviewed"}`
	if response := request(t, handler, http.MethodPost, "/v1/proposals/entity/proposal-1/confirm", body, caller()); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("confirm without idempotency status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/proposals/entity/proposal-1/confirm", body, nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("confirm without caller status = %d: %s", response.Code, response.Body.String())
	}
}

func TestProposalReviewStrictBodyAndMethods(t *testing.T) {
	handler := proposalHandler(t, &fakeProposals{})

	if response := request(t, handler, http.MethodPost, "/v1/proposals/entity/proposal-1/confirm", `{"reason_code":"reviewed","extra":1}`, writes()); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodGet, "/v1/proposals/entity/proposal-1", "{}", caller()); response.Code != http.StatusBadRequest {
		t.Fatalf("read body status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/proposals", `{}`, writes()); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("collection POST status = %d: %s", response.Code, response.Body.String())
	}
}

func TestProposalReviewClassifiesGatewayErrors(t *testing.T) {
	proposals := &fakeProposals{}
	handler := proposalHandler(t, proposals)

	proposals.err = gateway.NewError(gateway.ErrorNotFound, "proposal not found")
	if response := request(t, handler, http.MethodGet, "/v1/proposals/fact/missing", "", caller()); response.Code != http.StatusNotFound {
		t.Fatalf("missing proposal status = %d: %s", response.Code, response.Body.String())
	}
	proposals.err = gateway.NewError(gateway.ErrorConflict, "proposal already reviewed")
	if response := request(t, handler, http.MethodPost, "/v1/proposals/fact/proposal-1/confirm", `{"reason_code":"reviewed"}`, writes()); response.Code != http.StatusConflict {
		t.Fatalf("reviewed proposal status = %d: %s", response.Code, response.Body.String())
	}
}

func TestProposalDTOKeepsUnionMemberFields(t *testing.T) {
	t.Parallel()
	value := testProposal(review.ProposalKindRelation)
	object, err := proposalDTO(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(object["proposal_type"]) != `"relation"` || string(object["scope_id"]) != `"scope"` {
		t.Fatalf("proposal object = %+v", object)
	}
	if _, ok := object["relation_type"]; !ok {
		t.Fatalf("proposal object lost the union member fields: %+v", object)
	}
	if _, ok := object["space_id"]; ok {
		t.Fatalf("proposal object leaked space_id: %+v", object)
	}
}
