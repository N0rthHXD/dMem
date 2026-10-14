package storage

import (
	"context"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/conflict"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
)

// TestMemoryCapabilityAdministersAuthority proves the memory administration
// adapter serves bounded reads and the two fenced writes, and that replayed
// writes return the persisted operation identifier.
func TestMemoryCapabilityAdministersAuthority(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	created := createTestMemory(t, repository, "gateway-admin-memory")
	capability := NewMemoryCapability(repository)

	page, err := capability.ListMemories(ctx, memory.ListFilter{SpaceID: "space-1", Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].MemoryID != created.MemoryID {
		t.Fatalf("ListMemories() = %+v error=%v", page, err)
	}
	if _, err := capability.ListMemories(ctx, memory.ListFilter{SpaceID: "space-1", State: "archived", Limit: 10}); err != nil {
		t.Fatalf("ListMemories(archived) error = %v", err)
	}

	current, err := capability.GetMemory(ctx, created.MemoryID, false)
	if err != nil || current.MemoryID != created.MemoryID {
		t.Fatalf("GetMemory() = %+v error=%v", current, err)
	}

	revise := memory.ReviseParams{
		MemoryID:           created.MemoryID,
		ExpectedRevisionID: created.CurrentRevision.RevisionID,
		Content:            "revised administratively",
		Evidence:           []memory.EvidenceParams{{EventID: "event-1", Role: domain.EvidenceRoleDirect}},
		Reason:             "fix typo",
	}
	reviseCall := gatewayCall("gateway-caller", "memory-admin-revise", "request-memory-revise")
	reviseIdentity := gatewayIdentity(t, reviseCall, memory.OperationRevise, revise)
	revised, operationID, replay, err := capability.ReviseMemory(ctx, reviseIdentity, revise)
	if err != nil || replay || revised.CurrentRevision.DisplayContent != "revised administratively" || operationID == "" {
		t.Fatalf("ReviseMemory() = %+v op=%q replay=%t error=%v", revised, operationID, replay, err)
	}
	replayed, replayedOperationID, replay, err := capability.ReviseMemory(ctx, reviseIdentity, revise)
	if err != nil || !replay || replayed.CurrentRevision.RevisionID != revised.CurrentRevision.RevisionID || replayedOperationID != operationID {
		t.Fatalf("replayed ReviseMemory() = %+v op=%q replay=%t error=%v", replayed, replayedOperationID, replay, err)
	}

	archive := memory.StateChangeParams{MemoryID: created.MemoryID, ExpectedRevisionID: revised.CurrentRevision.RevisionID, Action: "archive", Reason: "cleanup"}
	archiveCall := gatewayCall("gateway-caller", "memory-admin-archive", "request-memory-archive")
	archiveIdentity := gatewayIdentity(t, archiveCall, memory.OperationStateChange, archive)
	archived, archiveOperation, replay, err := capability.ChangeMemoryState(ctx, archiveIdentity, archive)
	if err != nil || replay || archived.CurrentState != "archived" || archiveOperation == "" {
		t.Fatalf("ChangeMemoryState() = %+v op=%q replay=%t error=%v", archived, archiveOperation, replay, err)
	}
	replayedState, replayedArchiveOperation, replay, err := capability.ChangeMemoryState(ctx, archiveIdentity, archive)
	if err != nil || !replay || replayedState.CurrentState != "archived" || replayedArchiveOperation != archiveOperation {
		t.Fatalf("replayed ChangeMemoryState() = %+v op=%q replay=%t error=%v", replayedState, replayedArchiveOperation, replay, err)
	}

	revisions, err := capability.ListMemoryRevisions(ctx, created.MemoryID)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("ListMemoryRevisions() = %+v error=%v", revisions, err)
	}
	events, err := capability.ListMemoryStateEvents(ctx, created.MemoryID)
	if err != nil || len(events) != 2 || events[0].ToState != "archived" {
		t.Fatalf("ListMemoryStateEvents() = %+v error=%v", events, err)
	}
}

// TestConflictCapabilityListsAndResolves proves the conflict adapter lists,
// reads, and resolves one open case idempotently.
func TestConflictCapabilityListsAndResolves(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	a, b := createTestMemory(t, repository, "gateway-conflict-a"), createTestMemory(t, repository, "gateway-conflict-b")
	proposal := createEvolutionTest(t, repository, "gateway-conflict", evolutionParams("conflict", a, b))
	confirmEvolutionTest(t, repository, "gateway-conflict-confirm", proposal, nil)
	var caseID string
	if err := repository.store.reader.QueryRow(`SELECT case_id FROM conflict_cases`).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	capability := NewConflictCapability(repository)

	page, err := capability.ListConflictCases(ctx, conflict.ConflictFilter{SpaceID: "space-1", State: "open", Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].CaseID != caseID {
		t.Fatalf("ListConflictCases() = %+v error=%v", page, err)
	}
	if _, err := capability.GetConflictCase(ctx, caseID); err != nil {
		t.Fatalf("GetConflictCase() error = %v", err)
	}

	params := conflict.ResolveParams{
		CaseID: caseID, ResolutionType: "keep_both", ExpectedTargets: proposal.Targets,
		ResultStates: map[string]string{a.MemoryID: "active", b.MemoryID: "active"}, Reason: "both valid",
	}
	resolveCall := gatewayCall("gateway-caller", "conflict-admin-resolve", "request-conflict-resolve")
	identity := gatewayIdentity(t, resolveCall, conflict.OperationResolve, params)
	resolved, operationID, replay, err := capability.ResolveConflict(ctx, identity, params)
	if err != nil || replay || resolved.State != "resolved" || operationID == "" {
		t.Fatalf("ResolveConflict() = %+v op=%q replay=%t error=%v", resolved, operationID, replay, err)
	}
	replayed, replayedOperationID, replay, err := capability.ResolveConflict(ctx, identity, params)
	if err != nil || !replay || replayed.State != "resolved" || replayedOperationID != operationID {
		t.Fatalf("replayed ResolveConflict() = %+v op=%q replay=%t error=%v", replayed, replayedOperationID, replay, err)
	}
}

// TestProposalCapabilityCoversEveryFamily proves the proposal review adapter
// lists, reads, and reviews each reviewable family.
func TestProposalCapabilityCoversEveryFamily(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	capability := NewProposalCapability(repository)

	entityProposal := createEntityProposalTest(t, repository, CreateEntityProposalParams{
		EventID: "event-1", SpaceID: "space-1", ProposalKind: "entity", EntityType: "person",
		CanonicalName: "Gateway Entity", Sensitivity: "normal", ProcessorIdentity: "processor/test", ProcessingVersion: "entity-v1",
	}).ID
	seedEntity(t, repository, "entity-gateway", "Gateway Fact Subject", "person")
	factMemory := createTestMemory(t, repository, "gateway-fact-source")
	lit, typ := "active", "string"
	factProposal, _, err := repository.CreateFactProposal(ctx, memoryIdentity("gateway-fact-proposal", "/v1/fact-proposals", struct{}{}), CreateFactProposalParams{
		EventID: "event-1", SpaceID: "space-1", SubjectEntityID: "entity-gateway", Predicate: "has_status",
		ObjectLiteral: &lit, ObjectLiteralType: &typ, SourceMemoryID: factMemory.MemoryID, SourceRevisionID: factMemory.CurrentRevision.RevisionID,
		Evidence: []domain.EntityFactEvidence{{EvidenceType: "human_confirmation"}}, ProcessorIdentity: "processor/test", ProcessingVersion: "fact-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	relationProposal := createRelationProposalTest(t, repository, "gateway-relation-proposal",
		relationParams(createTestMemory(t, repository, "gateway-relation-source"), createTestMemory(t, repository, "gateway-relation-target")))
	evolutionProposal := createEvolutionTest(t, repository, "gateway-evolution-proposal",
		evolutionParams("supplement", createTestMemory(t, repository, "gateway-evolution-memory")))
	aggregateProposal := createAggregateProposalTest(t, repository, "gateway-aggregate-proposal",
		aggregateProposalParams(aggregateInput(createTestMemory(t, repository, "gateway-aggregate-input"))))

	families := []struct {
		kind review.ProposalKind
		id   string
	}{
		{review.ProposalKindEntity, entityProposal},
		{review.ProposalKindFact, factProposal.ProposalID},
		{review.ProposalKindRelation, relationProposal.ProposalID},
		{review.ProposalKindEvolution, evolutionProposal.ProposalID},
		{review.ProposalKindAggregate, aggregateProposal.ProposalID},
	}
	for _, family := range families {
		page, err := capability.ListProposals(ctx, review.ProposalFilter{Kind: family.kind, SpaceID: "space-1", ReviewState: "pending", Limit: 10})
		if err != nil || len(page.Items) != 1 || page.Items[0].Kind != family.kind {
			t.Fatalf("ListProposals(%s) = %+v error=%v", family.kind, page, err)
		}
		value, err := capability.GetProposal(ctx, family.kind, family.id)
		if err != nil || value.Kind != family.kind {
			t.Fatalf("GetProposal(%s) = %+v error=%v", family.kind, value, err)
		}
	}

	confirmCall := gatewayCall("gateway-caller", "proposal-admin-confirm", "request-proposal-confirm")
	confirmParams := review.ReviewProposalParams{Kind: review.ProposalKindEntity, ProposalID: entityProposal, Confirm: true, ReasonCode: "reviewed"}
	confirmed, operationID, replay, err := capability.ReviewProposal(ctx, gatewayIdentity(t, confirmCall, review.OperationEntityConfirm, confirmParams), confirmParams)
	if err != nil || replay || operationID == "" || confirmed.Entity == nil || confirmed.Entity.ReviewState != "confirmed" {
		t.Fatalf("confirm entity proposal = %+v op=%q replay=%t error=%v", confirmed, operationID, replay, err)
	}

	rejectCall := gatewayCall("gateway-caller", "proposal-admin-reject", "request-proposal-reject")
	rejectParams := review.ReviewProposalParams{Kind: review.ProposalKindFact, ProposalID: factProposal.ProposalID, ReasonCode: "irrelevant"}
	rejected, rejectOperation, replay, err := capability.ReviewProposal(ctx, gatewayIdentity(t, rejectCall, review.OperationFactReject, rejectParams), rejectParams)
	if err != nil || replay || rejectOperation == "" || rejected.Fact == nil || rejected.Fact.ReviewState != "rejected" {
		t.Fatalf("reject fact proposal = %+v op=%q replay=%t error=%v", rejected, rejectOperation, replay, err)
	}
	replayed, replayedOperation, replay, err := capability.ReviewProposal(ctx, gatewayIdentity(t, rejectCall, review.OperationFactReject, rejectParams), rejectParams)
	if err != nil || !replay || replayedOperation != rejectOperation || replayed.Fact == nil || replayed.Fact.ReviewState != "rejected" {
		t.Fatalf("replayed reject fact proposal = %+v op=%q replay=%t error=%v", replayed, replayedOperation, replay, err)
	}

	evolutionTargets := evolutionProposal.Targets
	evolutionParamsReview := review.ReviewProposalParams{Kind: review.ProposalKindEvolution, ProposalID: evolutionProposal.ProposalID, Confirm: true, ReasonCode: "reviewed", ExpectedTargets: evolutionTargets}
	evolutionValue, evolutionOperation, _, err := capability.ReviewProposal(ctx, gatewayIdentity(t, gatewayCall("gateway-caller", "proposal-admin-evolution", "request-proposal-evolution"), review.OperationEvolutionConfirm, evolutionParamsReview), evolutionParamsReview)
	if err != nil || evolutionOperation == "" || evolutionValue.Evolution == nil || evolutionValue.Evolution.ReviewState != "confirmed" {
		t.Fatalf("confirm evolution proposal = %+v op=%q error=%v", evolutionValue, evolutionOperation, err)
	}
}
