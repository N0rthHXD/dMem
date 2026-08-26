package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func aggregateProposalParams(inputs ...domain.AggregateInput) CommitAggregateProposalParams {
	return CommitAggregateProposalParams{SpaceID: "space-1", AggregateKind: "scene", Content: "aggregate output", Sensitivity: "normal", PolicyVersion: "aggregate-policy-v1", ProcessorIdentity: "aggregate/fake", ProviderIdentity: "fake/local", ModelIdentity: "fake-v1", ProcessingVersion: "aggregate-v1", Inputs: inputs}
}

func aggregateInput(memory domain.Memory) domain.AggregateInput {
	return domain.AggregateInput{MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, SpaceID: memory.SpaceID, Content: memory.CurrentRevision.Content, CurrentState: memory.CurrentState, Sensitivity: memory.Sensitivity}
}

func createAggregateProposalTest(t *testing.T, r *Repository, key string, params CommitAggregateProposalParams) domain.AggregateProposal {
	t.Helper()
	proposal, _, err := r.CommitAggregateProposal(context.Background(), memoryIdentity(key, "/v1/aggregate-proposals", params), params, nil)
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func confirmAggregateTest(t *testing.T, r *Repository, key string, proposal domain.AggregateProposal) domain.AggregateProposal {
	t.Helper()
	review := ReviewAggregateProposalParams{ReasonCode: "confirmed"}
	result, _, err := r.ConfirmAggregateProposal(context.Background(), memoryIdentity(key, "/v1/aggregate-proposals/{proposal_id}/confirm", review), proposal.ProposalID, review)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestConfirmAggregateAtomicProvenance(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	a, b := createTestMemory(t, r, "agg-a"), createTestMemory(t, r, "agg-b")
	confirmed := confirmAggregateTest(t, r, "agg-confirm", createAggregateProposalTest(t, r, "agg-proposal", aggregateProposalParams(aggregateInput(a), aggregateInput(b))))
	if confirmed.ResultMemoryID == nil {
		t.Fatalf("confirmed=%+v", confirmed)
	}
	aggregate, err := r.GetAggregate(context.Background(), *confirmed.ResultMemoryID)
	if err != nil || len(aggregate.Provenance) != 2 || aggregate.AggregateStale {
		t.Fatalf("aggregate=%+v err=%v", aggregate, err)
	}
	var memories, revisions, descriptors, relations, evidence, reviews, audits, jobs int
	err = r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memories WHERE memory_type='aggregate'),(SELECT count(*) FROM memory_revisions WHERE memory_id=?),(SELECT count(*) FROM aggregate_descriptors),(SELECT count(*) FROM memory_relations WHERE relation_type='derived_from'),(SELECT count(*) FROM relation_evidence),(SELECT count(*) FROM aggregate_proposal_reviews),(SELECT count(*) FROM audit_events WHERE action='confirm_aggregate_proposal'),(SELECT count(*) FROM jobs WHERE job_type='rebuild_relation_index')`, *confirmed.ResultMemoryID).Scan(&memories, &revisions, &descriptors, &relations, &evidence, &reviews, &audits, &jobs)
	if err != nil || memories != 1 || revisions != 1 || descriptors != 1 || relations != 2 || evidence != 2 || reviews != 1 || audits != 1 || jobs != 1 {
		t.Fatalf("rows=%d/%d/%d/%d/%d/%d/%d/%d err=%v", memories, revisions, descriptors, relations, evidence, reviews, audits, jobs, err)
	}
}

func TestAggregateMissingInputEvidenceRejected(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, r, "agg-evidence")
	if _, err := r.store.writer.Exec(`DELETE FROM memory_evidence WHERE revision_id=?`, memory.CurrentRevision.RevisionID); err == nil {
		t.Fatal("immutable evidence deletion unexpectedly succeeded")
	}
	input := aggregateInput(memory)
	input.RevisionID = "missing-revision"
	_, _, err := r.CommitAggregateProposal(context.Background(), memoryIdentity("agg-bad-evidence", "/v1/aggregate-proposals", input), aggregateProposalParams(input), nil)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("error=%v", err)
	}
	var count int
	_ = r.store.reader.QueryRow(`SELECT count(*) FROM aggregate_proposals`).Scan(&count)
	if count != 0 {
		t.Fatalf("partial proposals=%d", count)
	}
}

func TestAggregateInputRevisionStateChangedStaleDedupRebuild(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	input := createTestMemory(t, r, "agg-stale")
	confirmed := confirmAggregateTest(t, r, "agg-stale-confirm", createAggregateProposalTest(t, r, "agg-stale-proposal", aggregateProposalParams(aggregateInput(input))))
	revise := ReviseMemoryParams{ExpectedRevisionID: input.CurrentRevision.RevisionID, Content: "changed input", Evidence: directEvidence("event-2"), Reason: "changed"}
	if _, _, err := r.ReviseMemory(context.Background(), memoryIdentity("agg-revise", "/v1/memories/{memory_id}", revise), input.MemoryID, revise); err != nil {
		t.Fatal(err)
	}
	aggregate, err := r.GetAggregate(context.Background(), *confirmed.ResultMemoryID)
	if err != nil || !aggregate.AggregateStale || !contains(aggregate.StaleReasons, "input_revision_changed") {
		t.Fatalf("aggregate=%+v err=%v", aggregate, err)
	}
	if err := r.markStaleForTest(input.MemoryID); err != nil {
		t.Fatal(err)
	}
	var jobs int
	_ = r.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='rebuild_aggregate'`).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("rebuild jobs=%d", jobs)
	}
}

func TestAggregateFreshnessProjectionRebuildsFromFrozenInputs(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	input := createTestMemory(t, r, "agg-recover")
	confirmed := confirmAggregateTest(t, r, "agg-recover-confirm", createAggregateProposalTest(t, r, "agg-recover-proposal", aggregateProposalParams(aggregateInput(input))))
	var aggregateID string
	if err := r.store.reader.QueryRow(`SELECT aggregate_id FROM aggregate_descriptors WHERE memory_id=?`, *confirmed.ResultMemoryID).Scan(&aggregateID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.writer.Exec(`DELETE FROM aggregate_freshness WHERE aggregate_id=?`, aggregateID); err != nil {
		t.Fatal(err)
	}
	if err := r.RebuildAggregateFreshness(context.Background()); err != nil {
		t.Fatal(err)
	}
	aggregate, err := r.GetAggregate(context.Background(), aggregateID)
	if err != nil || aggregate.AggregateStale || aggregate.InputFingerprint == "" || len(aggregate.Provenance) != 1 {
		t.Fatalf("aggregate=%+v err=%v", aggregate, err)
	}
}

func (r *Repository) markStaleForTest(memoryID string) error {
	tx, err := r.store.writer.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = r.markDependentAggregatesStale(context.Background(), tx, memoryID, formatTime(r.runtime.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

func TestAggregateSensitivityFloor(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, r, "agg-sensitive")
	if _, err := r.store.writer.Exec(`UPDATE memories SET sensitivity='sensitive' WHERE memory_id=?`, memory.MemoryID); err != nil {
		t.Fatal(err)
	}
	input := aggregateInput(memory)
	input.Sensitivity = "sensitive"
	_, _, err := r.CommitAggregateProposal(context.Background(), memoryIdentity("agg-sensitive-build", "/v1/aggregate-proposals", input), aggregateProposalParams(input), nil)
	if !errors.Is(err, ErrInvalidAggregateProposal) {
		t.Fatalf("error=%v", err)
	}
}

func TestAggregateSiblingSpaceInputRejected(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, r, "agg-sibling")
	if _, err := r.store.writer.Exec(`INSERT INTO memory_spaces(space_id,parent_space_id,space_type,external_id,display_name,created_at) VALUES ('sibling','space-1','session','sibling','Sibling','now'); UPDATE memories SET space_id='sibling' WHERE memory_id=?`, memory.MemoryID); err != nil {
		t.Fatal(err)
	}
	_, err := r.PrepareAggregateBuild(context.Background(), "space-1", "scene", []string{memory.CurrentRevision.RevisionID}, 10)
	if !errors.Is(err, ErrInvalidAggregateProposal) {
		t.Fatalf("error=%v", err)
	}
}

func TestAggregateAuthorityCannotBeBypassedByGenericMemoryWrites(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	params := CreateMemoryParams{SpaceID: "space-1", MemoryType: "aggregate", Content: "unreviewed", Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "bypass"}
	if _, _, err := r.CreateMemory(context.Background(), memoryIdentity("agg-generic-create", "/v1/memories", params), params); !errors.Is(err, ErrInvalidAggregateProposal) {
		t.Fatalf("create error=%v", err)
	}

	input := createTestMemory(t, r, "agg-authority")
	confirmed := confirmAggregateTest(t, r, "agg-authority-confirm", createAggregateProposalTest(t, r, "agg-authority-proposal", aggregateProposalParams(aggregateInput(input))))
	revise := ReviseMemoryParams{ExpectedRevisionID: *confirmed.ResultRevisionID, Content: "unprovenanced revision", Evidence: directEvidence("event-1"), Reason: "bypass"}
	if _, _, err := r.ReviseMemory(context.Background(), memoryIdentity("agg-generic-revise", "/v1/memories/{memory_id}", revise), *confirmed.ResultMemoryID, revise); !errors.Is(err, ErrInvalidAggregateProposal) {
		t.Fatalf("revise error=%v", err)
	}
}
