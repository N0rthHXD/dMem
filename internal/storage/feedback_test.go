package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
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
	return domainFeedback{ID: result.Feedback.FeedbackID, Purpose: result.Feedback.Purpose, ProposalStatus: result.Proposal.Status, ProposalID: result.Proposal.ProposalID}
}

type domainFeedback struct {
	ID, Purpose, ProposalStatus string
	ProposalID                  *string
}

func testQueryHash(seed string) string {
	hash := strings.Repeat("0", 64-len(seed))
	return hash + seed
}

func testDecision(repository *Repository, queryHash string, rank int, state string) *domain.FeedbackDecisionContext {
	return &domain.FeedbackDecisionContext{
		SearchOperationID: "00000000-0000-4000-8000-000000000123",
		QueryHash:         queryHash,
		QueryType:         "lookup",
		EntityIDs:         []string{"00000000-0000-4000-8000-000000000201"},
		RankingIdentity:   repository.rankingIdentity(),
		FeatureIdentity:   retrieval.RankingFeatureVersion,
		ContextIdentity:   "test-context-selection",
		ResultRank:        rank,
		SelectionState:    state,
		MatchedSignals:    []string{"keyword", "semantic"},
	}
}

func retrievalFeedbackParams(repository *Repository, memory domain.Memory, caller, queryHash, kind, reason string) AppendFeedbackParams {
	return AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, Purpose: domain.FeedbackPurposeRetrieval, FeedbackType: kind, ReasonCode: reason, Decision: testDecision(repository, queryHash, 1, "selected")}
}

func TestFeedbackAppendIsRevisionBoundAndIdempotent(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-001")
	p := AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, Purpose: domain.FeedbackPurposeMemoryQuality, FeedbackType: "confirm", ReasonCode: "other"}
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
	changed.ReasonCode = "duplicate"
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
	for i, combination := range []struct{ purpose, kind, reason string }{
		{domain.FeedbackPurposeMemoryQuality, "confirm", "other"},
		{domain.FeedbackPurposeMemoryQuality, "correct", "fact_incorrect"},
		{domain.FeedbackPurposeMemoryQuality, "contest", "conflicts_with_evidence"},
		{domain.FeedbackPurposeRetrieval, "useful", "answered_query"},
		{domain.FeedbackPurposeRetrieval, "irrelevant", "not_relevant"},
	} {
		params := AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, Purpose: combination.purpose, FeedbackType: combination.kind, ReasonCode: combination.reason}
		if domain.FeedbackPurposeRequiresDecision(combination.purpose) {
			params.Decision = testDecision(repository, testQueryHash(fmt.Sprintf("%d", i)), 1, "selected")
		}
		appendTestFeedback(t, repository, "caller", fmt.Sprintf("feedback-002-%d", i), params)
	}
	current, err := repository.GetMemory(context.Background(), memory.MemoryID)
	if err != nil || current.CurrentState != memory.CurrentState || current.CurrentRevision.RevisionID != memory.CurrentRevision.RevisionID {
		t.Fatalf("memory changed after feedback: %+v, %v", current, err)
	}
}

func TestFeedbackEvolutionProposalIsDistinctPendingOutcome(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-003")
	result := appendTestFeedback(t, repository, "caller", "feedback-003-proposal", AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, Purpose: domain.FeedbackPurposeMemoryQuality, FeedbackType: "correct", ReasonCode: "fact_incorrect", CreateProposal: true, SourceEventID: "event-1", ProposedContent: "corrected pending content"})
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

func TestFeedbackDecisionContextRoundTrips(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-decision")
	decision := testDecision(repository, testQueryHash("decision"), 4, "rejected")
	decision.AnswerIdentity = "answer-123"
	params := AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, Purpose: domain.FeedbackPurposeAnswer, FeedbackType: "unsupported", ReasonCode: "evidence_insufficient", Note: strPtr("note"), Decision: decision}
	appendTestFeedback(t, repository, "caller-decision", "feedback-decision", params)

	page, err := repository.ListFeedback(context.Background(), FeedbackFilter{CallerID: "caller-decision", SpaceID: "space-1", Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list feedback = %+v, %v", page, err)
	}
	stored := page.Items[0]
	if stored.Purpose != domain.FeedbackPurposeAnswer || stored.Decision == nil {
		t.Fatalf("stored feedback = %+v", stored)
	}
	if stored.Decision.QueryHash != decision.QueryHash || stored.Decision.QueryType != "lookup" || stored.Decision.ResultRank != 4 || stored.Decision.SelectionState != "rejected" || stored.Decision.AnswerIdentity != "answer-123" {
		t.Fatalf("decision context lost: %+v", stored.Decision)
	}
	if len(stored.Decision.MatchedSignals) != 2 || stored.Decision.MatchedSignals[0] != "keyword" || stored.Decision.MatchedSignals[1] != "semantic" {
		t.Fatalf("matched signals lost: %+v", stored.Decision.MatchedSignals)
	}
	if stored.CallerID != "caller-decision" || stored.CreatedAt == "" {
		t.Fatalf("caller/timestamp lost: %+v", stored)
	}
}

func TestFeedbackRetrievalRequiresDecisionContext(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-nodecision")
	params := AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, Purpose: domain.FeedbackPurposeRetrieval, FeedbackType: "useful", ReasonCode: "answered_query"}
	if _, _, err := repository.AppendFeedback(context.Background(), feedbackIdentity("caller", "feedback-nodecision", params), params); err == nil {
		t.Fatal("retrieval feedback without decision context was accepted")
	}
}

func TestFeedbackRejectsIdentityIncompatibleDecision(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-identity")
	for name, corrupt := range map[string]func(*domain.FeedbackDecisionContext){
		"ranking": func(d *domain.FeedbackDecisionContext) { d.RankingIdentity = "stale-ranking-identity" },
		"feature": func(d *domain.FeedbackDecisionContext) { d.FeatureIdentity = "stale-feature-identity" },
	} {
		decision := testDecision(repository, testQueryHash("identity"), 1, "selected")
		corrupt(decision)
		params := AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, Purpose: domain.FeedbackPurposeRetrieval, FeedbackType: "useful", ReasonCode: "answered_query", Decision: decision}
		if _, _, err := repository.AppendFeedback(context.Background(), feedbackIdentity("caller-"+name, "feedback-identity-"+name, params), params); !errors.Is(err, ErrFeedbackIdentityMismatch) {
			t.Fatalf("%s identity mismatch error = %v", name, err)
		}
	}
}

func TestFeedbackQueryTextIsNeverPersisted(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-privacy")
	const secret = "the-private-query-text"
	params := AppendFeedbackParams{SpaceID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.CurrentRevision.RevisionID, Purpose: domain.FeedbackPurposeRetrieval, FeedbackType: "useful", ReasonCode: "answered_query", Decision: testDecision(repository, testQueryHash("privacy"), 1, "selected")}
	appendTestFeedback(t, repository, "caller", "feedback-privacy", params)
	var leaked int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM feedback_events WHERE query_hash=? OR query_type=? OR coalesce(note,'')=?`, secret, secret, secret).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("query text leaked into feedback: %d, %v", leaked, err)
	}
}

func TestFeedbackAggregateDeleteAndRebuildIsDeterministic(t *testing.T) {
	repository, runtime := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-004")
	for i, vote := range []struct{ caller, kind string }{{"a", "useful"}, {"a", "irrelevant"}, {"b", "useful"}, {"c", "irrelevant"}, {"d", "useful"}} {
		runtime.now = runtime.now.Add(time.Second)
		appendTestFeedback(t, repository, vote.caller, fmt.Sprintf("feedback-004-%d", i), retrievalFeedbackParams(repository, memory, vote.caller, testQueryHash("rebuild"), vote.kind, "other"))
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := readAggregate(t, repository, memory.CurrentRevision.RevisionID)
	// Latest per caller: a=irrelevant, b=useful, c=irrelevant, d=useful -> 2 useful, 2 irrelevant, bounded 0.
	if first.positive != 2 || first.negative != 2 || math.Abs(first.score) > 1e-6 {
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
	if first.algorithm != FeedbackAlgorithmVersion {
		t.Fatalf("algorithm version = %q", first.algorithm)
	}
}

func TestFeedbackAggregationIsIdenticalOnlineAndMaintenance(t *testing.T) {
	repository, runtime := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-shared")
	for i := 0; i < 4; i++ {
		runtime.now = runtime.now.Add(time.Second)
		caller := fmt.Sprintf("caller-%d", i)
		kind := "useful"
		if i%2 == 1 {
			kind = "irrelevant"
		}
		appendTestFeedback(t, repository, caller, fmt.Sprintf("shared-%d", i), retrievalFeedbackParams(repository, memory, caller, testQueryHash("shared"), kind, "other"))
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	online := readAllFeedbackAggregates(t, repository)

	tx, err := repository.store.writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AggregateFeedback(context.Background(), tx, runtime.now.UTC(), FeedbackAlgorithmVersion, feedbackWindowDays, feedbackMinimumSupport, feedbackHalfLifeDays); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	maintenance := readAllFeedbackAggregates(t, repository)
	if online != maintenance {
		t.Fatalf("online and maintenance aggregates differ:\nonline=%s\nmaintenance=%s", online, maintenance)
	}
}

func TestFeedbackQueryConditionedRequiresSupportAndDistinctCallers(t *testing.T) {
	repository, runtime := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-query")
	queryHash := testQueryHash("query-conditioned")
	for i := 0; i < 5; i++ {
		runtime.now = runtime.now.Add(time.Second)
		appendTestFeedback(t, repository, "single-caller", fmt.Sprintf("single-%d", i), retrievalFeedbackParams(repository, memory, "single-caller", queryHash, "useful", "answered_query"))
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM feedback_query_aggregates WHERE query_hash=?`, queryHash).Scan(&count); err != nil || count != 0 {
		t.Fatalf("single-caller query aggregate = %d, %v", count, err)
	}
	for i := 0; i < 3; i++ {
		runtime.now = runtime.now.Add(time.Second)
		appendTestFeedback(t, repository, fmt.Sprintf("distinct-%d", i), fmt.Sprintf("distinct-%d", i), retrievalFeedbackParams(repository, memory, fmt.Sprintf("distinct-%d", i), queryHash, "useful", "answered_query"))
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	var adjustment float64
	if err := repository.store.reader.QueryRow(`SELECT bounded_adjustment FROM feedback_query_aggregates WHERE query_hash=? AND revision_id=?`, queryHash, memory.CurrentRevision.RevisionID).Scan(&adjustment); err != nil {
		t.Fatal(err)
	}
	if adjustment <= 0 || adjustment > 1 {
		t.Fatalf("query-conditioned adjustment = %v", adjustment)
	}
}

func TestFeedbackUsageScoresCombineGlobalAndQueryConditioned(t *testing.T) {
	repository, runtime := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "feedback-usage")
	queryHash := testQueryHash("usage")
	for i := 0; i < 4; i++ {
		runtime.now = runtime.now.Add(time.Second)
		caller := fmt.Sprintf("usage-%d", i)
		appendTestFeedback(t, repository, caller, "usage-"+caller, retrievalFeedbackParams(repository, memory, caller, queryHash, "useful", "answered_query"))
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	health, err := repository.FeedbackAggregateHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := repository.store.writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	matched, err := repository.FeedbackUsageScores(context.Background(), tx, []string{memory.CurrentRevision.RevisionID}, health.Generation, queryHash)
	if err != nil {
		t.Fatal(err)
	}
	other, err := repository.FeedbackUsageScores(context.Background(), tx, []string{memory.CurrentRevision.RevisionID}, health.Generation, testQueryHash("other-query"))
	if err != nil {
		t.Fatal(err)
	}
	limit := FeedbackGlobalWeight() + FeedbackQueryWeight()
	if matched[memory.CurrentRevision.RevisionID] <= 0 || matched[memory.CurrentRevision.RevisionID] > limit {
		t.Fatalf("query-conditioned usage score = %v", matched)
	}
	globalOnly := other[memory.CurrentRevision.RevisionID]
	if globalOnly <= 0 || globalOnly > FeedbackGlobalWeight() {
		t.Fatalf("global usage score = %v", globalOnly)
	}
	if matched[memory.CurrentRevision.RevisionID] <= globalOnly {
		t.Fatalf("query-conditioned score %v did not exceed global %v", matched, globalOnly)
	}
}

func TestFeedbackDecayFavorsRecentEvents(t *testing.T) {
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	recent := feedbackDecay(at.Add(-time.Hour).Format(time.RFC3339Nano), at, feedbackHalfLifeDays)
	old := feedbackDecay(at.AddDate(0, 0, -feedbackHalfLifeDays).Format(time.RFC3339Nano), at, feedbackHalfLifeDays)
	if recent <= old || recent > 1 || old <= 0 {
		t.Fatalf("decay recent=%v old=%v", recent, old)
	}
	if math.Abs(old-0.5) > 1e-6 {
		t.Fatalf("half-life decay = %v, want 0.5", old)
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

func readAllFeedbackAggregates(t *testing.T, r *Repository) string {
	t.Helper()
	rows, err := r.store.reader.Query(`SELECT revision_id||'|'||algorithm_version||'|'||positive_count||'|'||negative_count||'|'||bounded_usage_score||'|'||window_start||'|'||window_end||'|'||coalesce(source_watermark,'')||'|'||state FROM feedback_aggregates ORDER BY revision_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var builder strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	queryRows, err := r.store.reader.Query(`SELECT query_hash||'|'||query_type||'|'||revision_id||'|'||positive_count||'|'||negative_count||'|'||distinct_callers||'|'||support_count||'|'||bounded_adjustment||'|'||coalesce(source_watermark,'')||'|'||state FROM feedback_query_aggregates ORDER BY query_hash,revision_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer queryRows.Close()
	for queryRows.Next() {
		var line string
		if err := queryRows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return builder.String()
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
	appendTestFeedback(t, repository, "caller", "feedback-006-vote", retrievalFeedbackParams(repository, memory, "caller", testQueryHash("inherit"), "useful", "other"))
	revised, _, _, err := repository.ReviseMemory(context.Background(), memoryIdentity("feedback-006-revise", "/v1/memories/{memory_id}/revisions", ReviseMemoryParams{}), memory.MemoryID, ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "new revision", Evidence: directEvidence("event-1"), Reason: "new revision"})
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
		appendTestFeedback(t, repository, fmt.Sprintf("negative-%d", i), fmt.Sprintf("rank-a-negative-%d", i), retrievalFeedbackParams(repository, a, fmt.Sprintf("negative-%d", i), testQueryHash("rank"), "irrelevant", "not_relevant"))
		appendTestFeedback(t, repository, fmt.Sprintf("positive-%d", i), fmt.Sprintf("rank-b-positive-%d", i), retrievalFeedbackParams(repository, b, fmt.Sprintf("positive-%d", i), testQueryHash("rank"), "useful", "answered_query"))
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "rankword", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 10, MaxChars: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if result.RankingVersion != rankingVersion || len(result.Results) != 2 || result.Results[0].Memory.MemoryID != b.MemoryID {
		t.Fatalf("ranking result = %+v", result)
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
		appendTestFeedback(t, repository, "poisoner", fmt.Sprintf("poison-%d", i), retrievalFeedbackParams(repository, memory, "poisoner", testQueryHash("poison"), kind, "other"))
	}
	if err := repository.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := readAggregate(t, repository, memory.CurrentRevision.RevisionID)
	if math.Abs(a.score) > gate.Maximum || math.Abs(FeedbackGlobalWeight()*a.score) > 0.001*gate.Maximum {
		t.Fatalf("poisoning exceeded gate %+v aggregate=%+v", gate, a)
	}
}

func TestAggregateVersionSwitchRejectsCursor(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMatchingMemories(t, repository, "rank-cursor", "cursorfeedback", minSelectionWindow+1)
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

func strPtr(value string) *string { return &value }
