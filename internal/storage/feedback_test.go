package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func feedbackIdentity(caller, key string, params AppendFeedbackParams) WriteIdentity {
	return WriteIdentity{CallerID: caller, IdempotencyKey: key, RequestID: "request-" + key, Method: "POST", Route: "/v1/feedback", RequestHash: HashRequest(params)}
}

func appendTestFeedback(t *testing.T, repository *Repository, caller, key string, params AppendFeedbackParams) domainFeedback {
	t.Helper()
	result, replay, err := repository.AppendFeedback(context.Background(), feedbackIdentity(caller, key, params), params)
	if err != nil || replay {
		t.Fatalf("AppendFeedback() replay=%t error=%v", replay, err)
	}
	return domainFeedback{ID: result.Feedback.FeedbackID, ProposalStatus: result.Proposal.Status, ProposalID: result.Proposal.ProposalID}
}

type domainFeedback struct {
	ID, ProposalStatus string
	ProposalID         *string
}

func TestFeedbackAppendIsRevisionBoundAndIdempotent(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-001")
	p := AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, FeedbackType: "useful", ReasonCode: "answered_query"}
	identity := feedbackIdentity("caller-a", "feedback-001", p)
	first, replay, err := repository.AppendFeedback(context.Background(), identity, p)
	if err != nil || replay {
		t.Fatalf("first append replay=%t error=%v", replay, err)
	}
	second, replay, err := repository.AppendFeedback(context.Background(), identity, p)
	if err != nil || !replay || second.Feedback.FeedbackID != first.Feedback.FeedbackID {
		t.Fatalf("replay = %+v, %t, %v", second, replay, err)
	}
	changed := p
	changed.FeedbackType = "irrelevant"
	identity.RequestHash = HashRequest(changed)
	if _, _, err := repository.AppendFeedback(context.Background(), identity, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
	var jobs int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='aggregate_feedback' AND target_id=?`, p.RevisionID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("aggregate jobs = %d, %v", jobs, err)
	}
}

func TestFeedbackNeverChangesLifecycle(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-002")
	for i, kind := range []string{"useful", "irrelevant", "correct", "supplement", "contest"} {
		appendTestFeedback(t, repository, "caller", "feedback-002-"+kind, AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, FeedbackType: kind, ReasonCode: "other"})
		_ = i
	}
	current, err := repository.GetMemory(context.Background(), memory.MemoryID)
	if err != nil || current.CurrentState != memory.CurrentState || current.CurrentRevision.RevisionID != memory.CurrentRevision.RevisionID {
		t.Fatalf("memory changed after feedback: %+v, %v", current, err)
	}
}

func TestFeedbackEvolutionProposalIsDistinctPendingOutcome(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-003")
	result := appendTestFeedback(t, repository, "caller", "feedback-003-proposal", AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, FeedbackType: "correct", ReasonCode: "fact_incorrect", CreateProposal: true, SourceEventID: "event-1", ProposedContent: "corrected pending content"})
	if result.ProposalStatus != "created" || result.ProposalID == nil || *result.ProposalID == result.ID {
		t.Fatalf("proposal outcome = %+v", result)
	}
	var state string
	if err := repository.store.reader.QueryRow(`SELECT review_state FROM evolution_proposals WHERE proposal_id=?`, *result.ProposalID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("proposal state = %q, %v", state, err)
	}
	current, _ := repository.GetMemory(context.Background(), memory.MemoryID)
	if current.CurrentRevision.RevisionID != memory.CurrentRevision.RevisionID {
		t.Fatal("feedback proposal directly revised memory")
	}
}

func TestFeedbackAggregateDeleteAndRebuildIsDeterministic(t *testing.T) {
	repository, runtime := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-004")
	for i, vote := range []struct{ caller, kind string }{{"a", "useful"}, {"a", "irrelevant"}, {"b", "correct"}, {"c", "supplement"}, {"d", "contest"}} {
		runtime.now = runtime.now.Add(time.Second)
		appendTestFeedback(t, repository, vote.caller, fmt.Sprintf("feedback-004-%d", i), AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, FeedbackType: vote.kind, ReasonCode: "other"})
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := readAggregate(t, repository, memory.CurrentRevision.RevisionID)
	if first.positive != 0 || first.negative != 1 || first.score != -0.125 {
		t.Fatalf("aggregate = %+v", first)
	}
	if _, err := repository.store.writer.Exec(`DELETE FROM feedback_aggregates`); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := readAggregate(t, repository, memory.CurrentRevision.RevisionID)
	if first.positive != second.positive || first.negative != second.negative || first.score != second.score || first.watermark != second.watermark || first.algorithm != second.algorithm {
		t.Fatalf("rebuilt aggregate changed: first=%+v second=%+v", first, second)
	}
}

type aggregateRow struct {
	algorithm, watermark string
	generation           int64
	positive, negative   int
	score                float64
}

func readAggregate(t *testing.T, r *Repository, revision string) aggregateRow {
	t.Helper()
	var a aggregateRow
	if err := r.store.reader.QueryRow(`SELECT algorithm_version,generation,positive_count,negative_count,bounded_usage_score,coalesce(source_watermark,'') FROM feedback_aggregates WHERE revision_id=?`, revision).Scan(&a.algorithm, &a.generation, &a.positive, &a.negative, &a.score, &a.watermark); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFeedbackStaleClaimCannotPublishAggregate(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	claim := ClaimedJob{Job: domain.Job{JobID: "missing", JobType: "rebuild_feedback_aggregates"}, LeaseOwner: "old-worker", LeaseGeneration: 1}
	if err := repository.RebuildFeedbackAggregatesClaimed(context.Background(), claim); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale claim error = %v", err)
	}
	health, err := repository.FeedbackAggregateHealth(context.Background())
	if err != nil || health.Generation != 1 {
		t.Fatalf("stale worker changed generation: %+v, %v", health, err)
	}
}

func TestFeedbackNewRevisionDoesNotInheritScore(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-006")
	appendTestFeedback(t, repository, "caller", "feedback-006-vote", AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, FeedbackType: "useful", ReasonCode: "other"})
	revised, _, err := repository.ReviseMemory(context.Background(), memoryIdentity("feedback-006-revise", "/v1/memories/{memory_id}/revisions", ReviseMemoryParams{}), memory.MemoryID, ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "new revision", Evidence: directEvidence("event-1"), Reason: "new revision"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM feedback_aggregates WHERE revision_id=?`, revised.CurrentRevision.RevisionID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("new revision inherited aggregate: %d, %v", count, err)
	}
}

func TestRankingKeepsRecallAndUsesBoundedPostFusionSignal(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	a := createMemoryWithContent(t, repository, "rank-a", "rankword alpha")
	b := createMemoryWithContent(t, repository, "rank-b", "rankword beta")
	for i := 0; i < 8; i++ {
		appendTestFeedback(t, repository, fmt.Sprintf("negative-%d", i), fmt.Sprintf("rank-a-negative-%d", i), AppendFeedbackParams{SpaceID: "space-1", MemoryID: a.MemoryID, RevisionID: a.CurrentRevision.RevisionID, FeedbackType: "irrelevant", ReasonCode: "not_relevant"})
		appendTestFeedback(t, repository, fmt.Sprintf("positive-%d", i), fmt.Sprintf("rank-b-positive-%d", i), AppendFeedbackParams{SpaceID: "space-1", MemoryID: b.MemoryID, RevisionID: b.CurrentRevision.RevisionID, FeedbackType: "useful", ReasonCode: "answered_query"})
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "rankword", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 10, MaxChars: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if result.RankingVersion != "rrf-v3" || len(result.Results) != 2 || result.Results[0].Memory.MemoryID != b.MemoryID {
		t.Fatalf("ranking v2 result = %+v", result)
	}
}

func TestFeedbackPoisoningSingleCallerIsBounded(t *testing.T) {
	var gate struct {
		Version  string  `json:"version"`
		Repeated int     `json:"repeated_events"`
		Maximum  float64 `json:"single_caller_max_absolute_score"`
		Weight   float64 `json:"ranking_signal_weight"`
	}
	raw, err := os.ReadFile("testdata/feedback-poisoning-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &gate); err != nil {
		t.Fatal(err)
	}
	repository, runtime := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "rank-002")
	for i := 0; i < gate.Repeated; i++ {
		runtime.now = runtime.now.Add(time.Second)
		kind := "useful"
		if i%2 == 1 {
			kind = "irrelevant"
		}
		appendTestFeedback(t, repository, "poisoner", fmt.Sprintf("poison-%d", i), AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, FeedbackType: kind, ReasonCode: "other"})
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := readAggregate(t, repository, memory.CurrentRevision.RevisionID)
	if math.Abs(a.score) > gate.Maximum || math.Abs(gate.Weight*a.score) > 0.001*gate.Maximum {
		t.Fatalf("poisoning exceeded gate %+v aggregate=%+v", gate, a)
	}
}

func TestAggregateVersionSwitchRejectsCursor(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "rank-cursor-a", "cursorfeedback one")
	createMemoryWithContent(t, repository, "rank-cursor-b", "cursorfeedback two")
	p := SearchParams{Query: "cursorfeedback", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 1, MaxChars: 1000}
	first, err := repository.SearchMemories(context.Background(), p)
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.Cursor = *first.NextCursor
	if _, err := repository.SearchMemories(context.Background(), p); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("old aggregate cursor error = %v", err)
	}
}
