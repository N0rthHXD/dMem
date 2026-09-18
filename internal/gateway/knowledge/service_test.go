package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeReadStore struct {
	entity         domain.Entity
	entityErr      error
	entityQuery    GetEntityQuery
	entities       domain.EntityPage
	entitiesErr    error
	entitiesQuery  ListEntitiesQuery
	relation       domain.MemoryRelation
	relationErr    error
	relationQuery  GetRelationQuery
	relations      domain.RelationPage
	relationsErr   error
	relationsQuery ListRelationsQuery
	fact           domain.EntityFact
	factErr        error
	factQuery      GetEntityFactQuery
	facts          domain.EntityFactPage
	factsErr       error
	factsQuery     ListEntityFactsQuery
	conflict       domain.ConflictCase
	conflictErr    error
	conflictQuery  GetConflictCaseQuery
	conflicts      domain.ConflictCasePage
	conflictsErr   error
	conflictsQuery ListConflictCasesQuery
	respectCancel  bool
}

func (f *fakeReadStore) GetEntity(ctx context.Context, query GetEntityQuery) (domain.Entity, error) {
	f.entityQuery = query
	if f.respectCancel {
		if err := ctx.Err(); err != nil {
			return domain.Entity{}, err
		}
	}
	return f.entity, f.entityErr
}

func (f *fakeReadStore) ListEntities(_ context.Context, query ListEntitiesQuery) (domain.EntityPage, error) {
	f.entitiesQuery = query
	return f.entities, f.entitiesErr
}

func (f *fakeReadStore) GetRelation(_ context.Context, query GetRelationQuery) (domain.MemoryRelation, error) {
	f.relationQuery = query
	return f.relation, f.relationErr
}

func (f *fakeReadStore) ListRelations(_ context.Context, query ListRelationsQuery) (domain.RelationPage, error) {
	f.relationsQuery = query
	return f.relations, f.relationsErr
}

func (f *fakeReadStore) GetEntityFact(_ context.Context, query GetEntityFactQuery) (domain.EntityFact, error) {
	f.factQuery = query
	return f.fact, f.factErr
}

func (f *fakeReadStore) ListEntityFacts(_ context.Context, query ListEntityFactsQuery) (domain.EntityFactPage, error) {
	f.factsQuery = query
	return f.facts, f.factsErr
}

func (f *fakeReadStore) GetConflictCase(_ context.Context, query GetConflictCaseQuery) (domain.ConflictCase, error) {
	f.conflictQuery = query
	return f.conflict, f.conflictErr
}

func (f *fakeReadStore) ListConflictCases(_ context.Context, query ListConflictCasesQuery) (domain.ConflictCasePage, error) {
	f.conflictsQuery = query
	return f.conflicts, f.conflictsErr
}

type fakeReviewStore struct {
	entityProposal    domain.EntityProposal
	relationProposal  domain.RelationProposal
	factProposal      domain.FactProposal
	evolutionProposal domain.EvolutionProposal
	aggregateProposal domain.AggregateProposal
	conflictCase      domain.ConflictCase
	replay            bool
	err               error
	calls             int
	lastID            gateway.WriteIdentity
	lastProposalID    string
	lastParams        ReviewParams
	lastCaseID        string
	lastCommand       domain.ConflictResolutionCommand
	respectCancel     bool
}

func (f *fakeReviewStore) record(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params ReviewParams) error {
	f.calls++
	f.lastID = identity
	f.lastProposalID = proposalID
	f.lastParams = params
	if f.respectCancel {
		return ctx.Err()
	}
	return nil
}

func (f *fakeReviewStore) ReviewEntityProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params ReviewParams) (domain.EntityProposal, bool, error) {
	if err := f.record(ctx, identity, proposalID, params); err != nil {
		return domain.EntityProposal{}, false, err
	}
	return f.entityProposal, f.replay, f.err
}

func (f *fakeReviewStore) ReviewRelationProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params ReviewParams) (domain.RelationProposal, bool, error) {
	if err := f.record(ctx, identity, proposalID, params); err != nil {
		return domain.RelationProposal{}, false, err
	}
	return f.relationProposal, f.replay, f.err
}

func (f *fakeReviewStore) ReviewFactProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params ReviewParams) (domain.FactProposal, bool, error) {
	if err := f.record(ctx, identity, proposalID, params); err != nil {
		return domain.FactProposal{}, false, err
	}
	return f.factProposal, f.replay, f.err
}

func (f *fakeReviewStore) ReviewEvolutionProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params ReviewParams) (domain.EvolutionProposal, bool, error) {
	if err := f.record(ctx, identity, proposalID, params); err != nil {
		return domain.EvolutionProposal{}, false, err
	}
	return f.evolutionProposal, f.replay, f.err
}

func (f *fakeReviewStore) ReviewAggregateProposal(ctx context.Context, identity gateway.WriteIdentity, proposalID string, params ReviewParams) (domain.AggregateProposal, bool, error) {
	if err := f.record(ctx, identity, proposalID, params); err != nil {
		return domain.AggregateProposal{}, false, err
	}
	return f.aggregateProposal, f.replay, f.err
}

func (f *fakeReviewStore) ResolveConflict(ctx context.Context, identity gateway.WriteIdentity, caseID string, command domain.ConflictResolutionCommand) (domain.ConflictCase, bool, error) {
	f.calls++
	f.lastID = identity
	f.lastCaseID = caseID
	f.lastCommand = command
	if f.respectCancel {
		return domain.ConflictCase{}, false, ctx.Err()
	}
	return f.conflictCase, f.replay, f.err
}

func testCall(caller, key, requestID string) gateway.CallContext {
	call := gateway.CallContext{RequestID: requestID, Caller: gateway.CallerIdentity{SubjectID: caller}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func TestGetEntityValidatesAndEnforcesScope(t *testing.T) {
	store := &fakeReadStore{entity: domain.Entity{EntityID: "entity-1", SpaceID: "space-1"}}
	service := NewReadService(store)

	if _, err := service.GetEntity(context.Background(), testCall("caller-1", "", "request-1"), GetEntityQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing id error = %v", err)
	}
	if _, err := service.GetEntity(context.Background(), testCall("caller-1", "", "request-1"), GetEntityQuery{EntityID: "entity-1", SpaceID: "space-2"}); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("scope mismatch error = %v", err)
	}
	entity, err := service.GetEntity(context.Background(), testCall("caller-1", "", "request-1"), GetEntityQuery{EntityID: "entity-1", SpaceID: "space-1"})
	if err != nil || entity.EntityID != "entity-1" {
		t.Fatalf("GetEntity() = %+v error = %v", entity, err)
	}
	if store.entityQuery.IncludeMentions {
		t.Fatalf("query = %+v", store.entityQuery)
	}
}

func TestListEntitiesRequiresScopeAndAppliesDefaultLimit(t *testing.T) {
	store := &fakeReadStore{entities: domain.EntityPage{Items: []domain.Entity{{EntityID: "entity-1"}}}}
	service := NewReadService(store)

	if _, err := service.ListEntities(context.Background(), testCall("caller-1", "", "request-1"), ListEntitiesQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing scope error = %v", err)
	}
	if _, err := service.ListEntities(context.Background(), testCall("caller-1", "", "request-1"), ListEntitiesQuery{SpaceID: "space-1", Page: gateway.PageRequest{Limit: -1}}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("negative limit error = %v", err)
	}
	if _, err := service.ListEntities(context.Background(), testCall("caller-1", "", "request-1"), ListEntitiesQuery{SpaceID: "space-1", Page: gateway.PageRequest{Limit: gateway.MaxPageLimit + 1}}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("oversized limit error = %v", err)
	}
	page, err := service.ListEntities(context.Background(), testCall("caller-1", "", "request-1"), ListEntitiesQuery{SpaceID: "space-1"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListEntities() = %+v error = %v", page, err)
	}
	if store.entitiesQuery.Page.Limit != gateway.DefaultPageLimit {
		t.Fatalf("default limit = %d, want %d", store.entitiesQuery.Page.Limit, gateway.DefaultPageLimit)
	}
}

func TestGetRelationEnforcesScopeAndValidatesID(t *testing.T) {
	store := &fakeReadStore{relation: domain.MemoryRelation{RelationID: "relation-1", SpaceID: "space-1"}}
	service := NewReadService(store)

	if _, err := service.GetRelation(context.Background(), testCall("caller-1", "", "request-1"), GetRelationQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing id error = %v", err)
	}
	if _, err := service.GetRelation(context.Background(), testCall("caller-1", "", "request-1"), GetRelationQuery{RelationID: "relation-1", SpaceID: "space-2"}); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("scope mismatch error = %v", err)
	}
	if _, err := service.GetRelation(context.Background(), testCall("caller-1", "", "request-1"), GetRelationQuery{RelationID: "relation-1", SpaceID: "space-1"}); err != nil {
		t.Fatalf("GetRelation() error = %v", err)
	}
}

func TestListRelationsValidatesDirectionAndScope(t *testing.T) {
	store := &fakeReadStore{}
	service := NewReadService(store)
	if _, err := service.ListRelations(context.Background(), testCall("caller-1", "", "request-1"), ListRelationsQuery{Direction: "sideways"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("invalid direction error = %v", err)
	}
	if _, err := service.ListRelations(context.Background(), testCall("caller-1", "", "request-1"), ListRelationsQuery{SpaceID: "space-1", Direction: "outgoing"}); err != nil {
		t.Fatalf("ListRelations() error = %v", err)
	}
	if store.relationsQuery.Direction != "outgoing" || store.relationsQuery.Page.Limit != gateway.DefaultPageLimit {
		t.Fatalf("query = %+v", store.relationsQuery)
	}
}

func TestGetEntityFactEnforcesScope(t *testing.T) {
	store := &fakeReadStore{fact: domain.EntityFact{FactID: "fact-1", SpaceID: "space-1"}}
	service := NewReadService(store)
	if _, err := service.GetEntityFact(context.Background(), testCall("caller-1", "", "request-1"), GetEntityFactQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing id error = %v", err)
	}
	if _, err := service.GetEntityFact(context.Background(), testCall("caller-1", "", "request-1"), GetEntityFactQuery{FactID: "fact-1", SpaceID: "space-2"}); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("scope mismatch error = %v", err)
	}
	if _, err := service.GetEntityFact(context.Background(), testCall("caller-1", "", "request-1"), GetEntityFactQuery{FactID: "fact-1", SpaceID: "space-1"}); err != nil {
		t.Fatalf("GetEntityFact() error = %v", err)
	}
}

func TestListEntityFactsValidatesAndNormalizesReferenceTime(t *testing.T) {
	store := &fakeReadStore{}
	service := NewReadService(store)
	if _, err := service.ListEntityFacts(context.Background(), testCall("caller-1", "", "request-1"), ListEntityFactsQuery{State: "bogus", SpaceID: "space-1"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("invalid state error = %v", err)
	}
	if _, err := service.ListEntityFacts(context.Background(), testCall("caller-1", "", "request-1"), ListEntityFactsQuery{SpaceID: "space-1", ReferenceTime: "not-a-time"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("invalid reference time error = %v", err)
	}
	if _, err := service.ListEntityFacts(context.Background(), testCall("caller-1", "", "request-1"), ListEntityFactsQuery{SpaceID: "space-1", ReferenceTime: "2026-08-21T12:00:00+02:00"}); err != nil {
		t.Fatalf("ListEntityFacts() error = %v", err)
	}
	want := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if store.factsQuery.ReferenceTime != want {
		t.Fatalf("reference time = %q, want %q", store.factsQuery.ReferenceTime, want)
	}
}

func TestConflictReadsValidateIDsAndState(t *testing.T) {
	store := &fakeReadStore{conflict: domain.ConflictCase{CaseID: "case-1"}}
	service := NewReadService(store)
	if _, err := service.GetConflictCase(context.Background(), testCall("caller-1", "", "request-1"), GetConflictCaseQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing case error = %v", err)
	}
	if _, err := service.GetConflictCase(context.Background(), testCall("caller-1", "", "request-1"), GetConflictCaseQuery{CaseID: "case-1"}); err != nil {
		t.Fatalf("GetConflictCase() error = %v", err)
	}
	if _, err := service.ListConflictCases(context.Background(), testCall("caller-1", "", "request-1"), ListConflictCasesQuery{SpaceID: "space-1", State: "closed"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("invalid state error = %v", err)
	}
	if _, err := service.ListConflictCases(context.Background(), testCall("caller-1", "", "request-1"), ListConflictCasesQuery{SpaceID: "space-1", State: "open"}); err != nil {
		t.Fatalf("ListConflictCases() error = %v", err)
	}
}

func TestReadClassifiesPortErrors(t *testing.T) {
	store := &fakeReadStore{entityErr: gateway.NewError(gateway.ErrorNotFound, "missing")}
	service := NewReadService(store)
	if _, err := service.GetEntity(context.Background(), testCall("caller-1", "", "request-1"), GetEntityQuery{EntityID: "entity-1"}); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("gateway error passthrough = %v", err)
	}
	store.entityErr = errors.New("disk on fire")
	if _, err := service.GetEntity(context.Background(), testCall("caller-1", "", "request-1"), GetEntityQuery{EntityID: "entity-1"}); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("unknown error classification = %v", err)
	}
}

func TestReadPropagatesCancellation(t *testing.T) {
	store := &fakeReadStore{respectCancel: true}
	service := NewReadService(store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.GetEntity(ctx, testCall("caller-1", "", "request-1"), GetEntityQuery{EntityID: "entity-1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestReviewEntityProposalValidatesAndPropagatesIdentity(t *testing.T) {
	store := &fakeReviewStore{entityProposal: domain.EntityProposal{ProposalID: "proposal-1"}}
	service := NewReviewService(store)
	note := "approved"
	command := ReviewProposalCommand{ProposalID: "proposal-1", Decision: ReviewConfirm, ReasonCode: "approved", Note: &note}

	result, err := service.ReviewEntityProposal(context.Background(), testCall("caller-1", "key-1", "request-1"), command)
	if err != nil || result.Proposal.ProposalID != "proposal-1" || result.Replay || result.OperationID != "request-1" {
		t.Fatalf("result = %+v error = %v", result, err)
	}
	if store.lastID.Operation != OperationReviewEntityProposal || store.lastID.IdempotencyKey != "key-1" {
		t.Fatalf("write identity = %+v", store.lastID)
	}
	if store.lastProposalID != "proposal-1" || store.lastParams.Decision != ReviewConfirm || store.lastParams.Note != &note {
		t.Fatalf("params = %+v proposal=%q", store.lastParams, store.lastProposalID)
	}
	wantHash, _ := gateway.CanonicalHash(command)
	if store.lastID.CommandHash != wantHash {
		t.Fatalf("command hash = %q, want %q", store.lastID.CommandHash, wantHash)
	}
}

func TestReviewProposalRejectsInvalidInputBeforeStore(t *testing.T) {
	store := &fakeReviewStore{}
	service := NewReviewService(store)
	longReason := strings.Repeat("r", 129)
	longNote := strings.Repeat("n", 1025)
	cases := map[string]ReviewProposalCommand{
		"missing proposal": {Decision: ReviewConfirm, ReasonCode: "approved"},
		"bad decision":     {ProposalID: "proposal-1", Decision: "maybe", ReasonCode: "approved"},
		"missing reason":   {ProposalID: "proposal-1", Decision: ReviewConfirm},
		"long reason":      {ProposalID: "proposal-1", Decision: ReviewConfirm, ReasonCode: longReason},
		"long note":        {ProposalID: "proposal-1", Decision: ReviewConfirm, ReasonCode: "approved", Note: &longNote},
	}
	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := service.ReviewEntityProposal(context.Background(), testCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("error = %v, want invalid_input", err)
			}
		})
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times for invalid input", store.calls)
	}
}

func TestReviewRequiresCallerAndIdempotency(t *testing.T) {
	service := NewReviewService(&fakeReviewStore{})
	valid := ReviewProposalCommand{ProposalID: "proposal-1", Decision: ReviewReject, ReasonCode: "duplicate"}
	if _, err := service.ReviewRelationProposal(context.Background(), testCall("", "key-1", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing caller error = %v", err)
	}
	if _, err := service.ReviewRelationProposal(context.Background(), testCall("caller-1", "", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing idempotency error = %v", err)
	}
}

func TestReviewClassifiesPortErrors(t *testing.T) {
	store := &fakeReviewStore{err: gateway.NewError(gateway.ErrorConflict, "already reviewed")}
	service := NewReviewService(store)
	command := ReviewProposalCommand{ProposalID: "proposal-1", Decision: ReviewConfirm, ReasonCode: "approved"}
	if _, err := service.ReviewFactProposal(context.Background(), testCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorConflict) {
		t.Fatalf("gateway error passthrough = %v", err)
	}
	store.err = errors.New("disk on fire")
	if _, err := service.ReviewFactProposal(context.Background(), testCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("unknown error classification = %v", err)
	}
}

func TestReviewPropagatesCancellation(t *testing.T) {
	store := &fakeReviewStore{respectCancel: true}
	service := NewReviewService(store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := ReviewProposalCommand{ProposalID: "proposal-1", Decision: ReviewConfirm, ReasonCode: "approved"}
	if _, err := service.ReviewEvolutionProposal(ctx, testCall("caller-1", "key-1", "request-1"), command); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestResolveConflictValidatesCommand(t *testing.T) {
	store := &fakeReviewStore{conflictCase: domain.ConflictCase{CaseID: "case-1"}}
	service := NewReviewService(store)
	content := "resolved content"
	start, end := int64(1), int64(5)
	evidence := []domain.RevisionEvidence{{EventID: "event-1", Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}}
	target := &domain.EvolutionTarget{MemoryID: "memory-1", ExpectedRevisionID: "revision-1"}
	mode := "same_memory_revision"

	invalid := map[string]ResolveConflictCommand{
		"missing case":       {ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "dismiss", Reason: "resolved"}},
		"unsupported type":   {CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "delete", Reason: "resolved"}},
		"missing reason":     {CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "dismiss"}},
		"non destructive":    {CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "dismiss", Reason: "resolved", ProposedContent: &content}},
		"no content":         {CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "revise_one", Reason: "resolved", Evidence: evidence}},
		"no evidence":        {CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "revise_one", Reason: "resolved", ProposedContent: &content}},
		"bad evidence":       {CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "revise_one", Reason: "resolved", ProposedContent: &content, Evidence: []domain.RevisionEvidence{{Role: domain.EvidenceRoleDirect}}}},
		"no selected target": {CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "revise_one", Reason: "resolved", ProposedContent: &content, Evidence: evidence}},
		"bad replacement":    {CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "supersede", Reason: "resolved", ProposedContent: &content, Evidence: evidence, SelectedTarget: target}},
		"reconcile target":   {CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "reconcile", Reason: "resolved", ProposedContent: &content, Evidence: evidence, SelectedTarget: target}},
		"bad valid time":     {CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{ResolutionType: "revise_one", Reason: "resolved", ProposedContent: &content, Evidence: evidence, SelectedTarget: target, ValidFrom: stringPointer("not-a-time")}},
	}
	for name, command := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := service.ResolveConflict(context.Background(), testCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("error = %v, want invalid_input", err)
			}
		})
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times for invalid input", store.calls)
	}

	valid := ResolveConflictCommand{CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{
		ResolutionType: "supersede", Reason: "resolved", ProposedContent: &content,
		Evidence: evidence, SelectedTarget: target, ReplacementMode: &mode,
	}}
	result, err := service.ResolveConflict(context.Background(), testCall("caller-1", "key-1", "request-1"), valid)
	if err != nil || result.Case.CaseID != "case-1" {
		t.Fatalf("ResolveConflict() = %+v error = %v", result, err)
	}
	if store.lastID.Operation != OperationResolveConflict || store.lastCaseID != "case-1" || store.lastCommand.ResolutionType != "supersede" {
		t.Fatalf("store call = identity %+v case %q command %+v", store.lastID, store.lastCaseID, store.lastCommand)
	}
}

func TestResolveConflictNormalizesValidTime(t *testing.T) {
	store := &fakeReviewStore{}
	service := NewReviewService(store)
	content := "resolved content"
	start, end := int64(1), int64(5)
	from := "2026-08-21T12:00:00+02:00"
	until := "2026-08-22T12:00:00+02:00"
	command := ResolveConflictCommand{CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{
		ResolutionType: "reconcile", Reason: "resolved", ProposedContent: &content,
		Evidence:  []domain.RevisionEvidence{{EventID: "event-1", Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}},
		ValidFrom: &from, ValidUntil: &until,
	}}
	if _, err := service.ResolveConflict(context.Background(), testCall("caller-1", "key-1", "request-1"), command); err != nil {
		t.Fatalf("ResolveConflict() error = %v", err)
	}
	wantFrom := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if store.lastCommand.ValidFrom == nil || *store.lastCommand.ValidFrom != wantFrom {
		t.Fatalf("valid_from = %v, want %q", store.lastCommand.ValidFrom, wantFrom)
	}
}

func TestResolveConflictRejectsInvertedValidTime(t *testing.T) {
	store := &fakeReviewStore{}
	service := NewReviewService(store)
	content := "resolved content"
	start, end := int64(1), int64(5)
	from := "2026-08-22T00:00:00Z"
	until := "2026-08-21T00:00:00Z"
	command := ResolveConflictCommand{CaseID: "case-1", ConflictResolutionCommand: domain.ConflictResolutionCommand{
		ResolutionType: "reconcile", Reason: "resolved", ProposedContent: &content,
		Evidence:  []domain.RevisionEvidence{{EventID: "event-1", Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}},
		ValidFrom: &from, ValidUntil: &until,
	}}
	if _, err := service.ResolveConflict(context.Background(), testCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("error = %v, want invalid_input", err)
	}
}

func stringPointer(value string) *string { return &value }
