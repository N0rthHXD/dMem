package review

import (
	"context"
	"errors"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeProposalStore struct {
	filter      ProposalFilter
	page        ProposalPage
	listErr     error
	getKind     ProposalKind
	getID       string
	getValue    Proposal
	getErr      error
	reviewValue Proposal
	reviewOp    string
	reviewRep   bool
	reviewErr   error
	reviewID    gateway.WriteIdentity
	reviewP     ReviewProposalParams
}

func (f *fakeProposalStore) ListProposals(_ context.Context, filter ProposalFilter) (ProposalPage, error) {
	f.filter = filter
	return f.page, f.listErr
}

func (f *fakeProposalStore) GetProposal(_ context.Context, kind ProposalKind, proposalID string) (Proposal, error) {
	f.getKind, f.getID = kind, proposalID
	return f.getValue, f.getErr
}

func (f *fakeProposalStore) ReviewProposal(_ context.Context, identity gateway.WriteIdentity, params ReviewProposalParams) (Proposal, string, bool, error) {
	f.reviewID, f.reviewP = identity, params
	return f.reviewValue, f.reviewOp, f.reviewRep, f.reviewErr
}

func TestListProposalsValidatesQuery(t *testing.T) {
	service := NewProposalService(&fakeProposalStore{page: ProposalPage{Items: []Proposal{}}})
	cases := []ListProposalsQuery{
		{Kind: ProposalKindEntity},
		{SpaceID: "s"},
		{SpaceID: "s", Kind: "bogus"},
		{SpaceID: "s", Kind: ProposalKindEntity, ReviewState: "bogus"},
		{SpaceID: "s", Kind: ProposalKindEntity, Page: gateway.PageRequest{Limit: 500}},
	}
	for _, query := range cases {
		if _, err := service.ListProposals(context.Background(), proposalCall(""), query); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
			t.Errorf("query %+v error = %v", query, err)
		}
	}
}

func TestListProposalsAppliesFilter(t *testing.T) {
	store := &fakeProposalStore{page: ProposalPage{Items: []Proposal{NewEntityProposal(domain.EntityProposal{ProposalID: "p-1"})}}}
	service := NewProposalService(store)
	page, err := service.ListProposals(context.Background(), proposalCall(""), ListProposalsQuery{SpaceID: "scope-1", Kind: ProposalKindEntity, ReviewState: "pending", Page: gateway.PageRequest{Limit: 10, Cursor: "cursor-1"}})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListProposals() = %+v error = %v", page, err)
	}
	if store.filter.Kind != ProposalKindEntity || store.filter.SpaceID != "scope-1" || store.filter.ReviewState != "pending" || store.filter.Limit != 10 || store.filter.Cursor != "cursor-1" {
		t.Fatalf("filter = %+v", store.filter)
	}
}

func TestGetProposalValidatesKindAndIdentifier(t *testing.T) {
	service := NewProposalService(&fakeProposalStore{getValue: NewEntityProposal(domain.EntityProposal{ProposalID: "p-1"})})
	if _, err := service.GetProposal(context.Background(), proposalCall(""), GetProposalQuery{Kind: "bogus", ProposalID: "p-1"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("bad kind error = %v", err)
	}
	if _, err := service.GetProposal(context.Background(), proposalCall(""), GetProposalQuery{Kind: ProposalKindEntity}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing id error = %v", err)
	}
	if _, err := service.GetProposal(context.Background(), proposalCall(""), GetProposalQuery{Kind: ProposalKindEntity, ProposalID: "p-1"}); err != nil {
		t.Fatalf("GetProposal() error = %v", err)
	}
}

func TestConfirmProposalValidatesCommand(t *testing.T) {
	service := NewProposalService(&fakeProposalStore{})
	if err := validateProposalReview(ReviewProposalCommand{Kind: ProposalKindEntity, ProposalID: "p-1", ReasonCode: "reviewed"}, true); err != nil {
		t.Fatalf("valid command error = %v", err)
	}
	cases := map[string]ReviewProposalCommand{
		"bad kind":       {Kind: "bogus", ProposalID: "p-1", ReasonCode: "reviewed"},
		"missing id":     {Kind: ProposalKindEntity, ReasonCode: "reviewed"},
		"missing reason": {Kind: ProposalKindEntity, ProposalID: "p-1"},
		"evolution inputs on entity": {Kind: ProposalKindEntity, ProposalID: "p-1", ReasonCode: "reviewed",
			ExpectedTargets: []domain.EvolutionTarget{{MemoryID: "m", ExpectedRevisionID: "r"}}},
		"evolution without targets": {Kind: ProposalKindEvolution, ProposalID: "p-1", ReasonCode: "reviewed"},
		"evolution duplicate targets": {Kind: ProposalKindEvolution, ProposalID: "p-1", ReasonCode: "reviewed",
			ExpectedTargets: []domain.EvolutionTarget{{MemoryID: "m", ExpectedRevisionID: "r"}, {MemoryID: "m", ExpectedRevisionID: "r2"}}},
		"evolution bad mode": {Kind: ProposalKindEvolution, ProposalID: "p-1", ReasonCode: "reviewed",
			ExpectedTargets: []domain.EvolutionTarget{{MemoryID: "m", ExpectedRevisionID: "r"}}, ReplacementMode: strptrValue("bogus")},
		"reject with inputs": {Kind: ProposalKindEvolution, ProposalID: "p-1", ReasonCode: "reviewed",
			ExpectedTargets: []domain.EvolutionTarget{{MemoryID: "m", ExpectedRevisionID: "r"}}},
	}
	for name, command := range cases {
		confirm := name != "reject with inputs"
		if _, err := service.review(context.Background(), proposalCall("key-1"), command, confirm); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
			t.Errorf("%s error = %v", name, err)
		}
	}
}

func TestProposalReviewRequiresIdempotencyAndCaller(t *testing.T) {
	service := NewProposalService(&fakeProposalStore{})
	command := ReviewProposalCommand{Kind: ProposalKindEntity, ProposalID: "p-1", ReasonCode: "reviewed"}
	if _, err := service.ConfirmProposal(context.Background(), proposalCall(""), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing idempotency error = %v", err)
	}
	noCaller := gateway.CallContext{Idempotency: &gateway.Idempotency{Key: "key-1"}}
	if _, err := service.ConfirmProposal(context.Background(), noCaller, command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing caller error = %v", err)
	}
}

func TestProposalReviewUsesFamilyOperationName(t *testing.T) {
	store := &fakeProposalStore{reviewValue: NewEvolutionProposal(domain.EvolutionProposal{ProposalID: "p-1", ReviewState: "confirmed"}), reviewOp: "op-1", reviewRep: true}
	service := NewProposalService(store)
	command := ReviewProposalCommand{
		Kind: ProposalKindEvolution, ProposalID: "p-1", ReasonCode: "reviewed",
		ExpectedTargets: []domain.EvolutionTarget{{MemoryID: "memory-1", ExpectedRevisionID: "revision-1"}},
		ReplacementMode: strptrValue("new_memory"), SupersedeTargets: true,
	}
	result, err := service.ConfirmProposal(context.Background(), proposalCall("key-1"), command)
	if err != nil {
		t.Fatalf("ConfirmProposal() error = %v", err)
	}
	if result.OperationID != "op-1" || !result.Replay || result.Proposal.Evolution == nil || result.Proposal.Evolution.ReviewState != "confirmed" {
		t.Fatalf("result = %+v", result)
	}
	if store.reviewID.Operation != OperationEvolutionConfirm || store.reviewID.IdempotencyKey != "key-1" {
		t.Fatalf("identity = %+v", store.reviewID)
	}
	if !store.reviewP.Confirm || store.reviewP.Kind != ProposalKindEvolution || !store.reviewP.SupersedeTargets || store.reviewP.ReplacementMode == nil {
		t.Fatalf("params = %+v", store.reviewP)
	}
}

func TestProposalReviewNormalizesStoreErrors(t *testing.T) {
	service := NewProposalService(&fakeProposalStore{reviewErr: errors.New("boom")})
	command := ReviewProposalCommand{Kind: ProposalKindEntity, ProposalID: "p-1", ReasonCode: "reviewed"}
	if _, err := service.RejectProposal(context.Background(), proposalCall("key-1"), command); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("store error = %v", err)
	}
}

func proposalCall(key string) gateway.CallContext {
	call := gateway.CallContext{RequestID: "request-1", Caller: gateway.CallerIdentity{SubjectID: "reviewer"}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func strptrValue(value string) *string { return &value }
