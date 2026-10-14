package storage

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	agenttool "github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

func selectionSearchParams(query string, explain bool) SearchParams {
	return SearchParams{
		Query: query, SpaceIDs: []string{"space-1"}, States: []string{"active"},
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 10000, MaxContextTokens: 10000, Explain: explain,
	}
}

// TestSelectionRelevanceKeywordFloor locks the v3 keyword relevance floor: any
// keyword-channel hit must clear the selection utility floor even when its
// literal coverage does not. The floor scales with normalized keyword rank
// inside a band narrower than the smallest selection discontinuity, so rank
// orders candidates without tripping the MMR score-discontinuity stop.
// Semantic and graph maxima, literal coverage, and exact-primary protection
// keep their existing precedence.
func TestSelectionRelevanceKeywordFloor(t *testing.T) {
	terms := []retrieval.KeywordTerm{{Text: "Melanie"}}
	revision := domain.MemoryRevision{DisplayContent: "database pool"}
	score := func(value float64) *float64 { return &value }
	floor := func(normalized float64) float64 {
		return hintSelectionRelevance + keywordSelectionFloorSpan*normalized
	}

	for _, keywordRank := range []float64{1, 0.9, 0.4, 0} {
		if got := selectionRelevance(revision, domain.SearchResult{KeywordRelevance: score(keywordRank)}, terms, true); got != floor(keywordRank) {
			t.Fatalf("normalized keyword %.1f floor = %v, want %v", keywordRank, got, floor(keywordRank))
		}
	}
	// The floor also applies to a hint-only match without a compiled score.
	if got := selectionRelevance(revision, domain.SearchResult{KeywordHintMatched: true}, terms, true); got != hintSelectionRelevance {
		t.Fatalf("hint-only floor = %v, want %v", got, hintSelectionRelevance)
	}
	// A hint-only match with a rank still receives the rank-scaled floor; the
	// hint bound must not pin the relevance and skip rank scaling.
	if got := selectionRelevance(revision, domain.SearchResult{KeywordHintMatched: true, KeywordRelevance: score(1)}, terms, true); got != floor(1) {
		t.Fatalf("hint-matched rank floor = %v, want %v", got, floor(1))
	}
	// Better literal coverage is never lowered by a weak keyword score.
	if got := selectionRelevance(revision, domain.SearchResult{KeywordRelevance: score(0)}, []retrieval.KeywordTerm{{Text: "database"}}, true); got != 1 {
		t.Fatalf("full literal coverage = %v, want 1", got)
	}
	// Coverage above the floor keeps its value directly.
	covered := domain.MemoryRevision{DisplayContent: "database pool Melanie"}
	partial := []retrieval.KeywordTerm{{Text: "database"}, {Text: "pool"}, {Text: "Melanie"}, {Text: "Berlin"}}
	if got := selectionRelevance(covered, domain.SearchResult{KeywordRelevance: score(1)}, partial, true); got != 0.75 {
		t.Fatalf("partial literal coverage = %v, want 0.75", got)
	}
	// With the floor disabled (semantic executed), keyword matches keep only
	// their coverage so calibrated semantic ordering stays authoritative.
	if got := selectionRelevance(revision, domain.SearchResult{KeywordRelevance: score(1)}, terms, false); got != 0 {
		t.Fatalf("disabled keyword floor = %v, want coverage 0", got)
	}
	if got := selectionRelevance(revision, domain.SearchResult{KeywordHintMatched: true}, terms, false); got != hintSelectionRelevance {
		t.Fatalf("disabled-floor hint match = %v, want %v", got, hintSelectionRelevance)
	}
	// Calibrated semantic and graph scores still take the maximum.
	if got := selectionRelevance(revision, domain.SearchResult{KeywordRelevance: score(0), SemanticScore: score(0.9)}, terms, true); got != 0.9 {
		t.Fatalf("semantic maximum = %v, want 0.9", got)
	}
	if got := selectionRelevance(revision, domain.SearchResult{KeywordRelevance: score(0), GraphScore: score(0.95)}, terms, true); got != 0.95 {
		t.Fatalf("graph maximum = %v, want 0.95", got)
	}
	// Protected exact primary matches remain canonical.
	if got := selectionRelevance(revision, domain.SearchResult{ProtectedPrimary: true}, terms, true); got != 1 {
		t.Fatalf("protected primary = %v, want 1", got)
	}
}

// TestContextEvidenceKeyRequiresSourceSpan locks the duplicate-evidence key:
// only an event plus its exact span identifies duplicate provenance. A bare
// event backs a whole committed batch of distinct memories, so it must not
// produce a cluster key.
func TestContextEvidenceKeyRequiresSourceSpan(t *testing.T) {
	if got := contextEvidenceKey(domain.RevisionEvidence{EventID: "event-1"}); got != "" {
		t.Fatalf("span-less evidence key = %q, want empty", got)
	}
	start, end := int64(1), int64(5)
	if got := contextEvidenceKey(domain.RevisionEvidence{EventID: "event-1", StartOffset: &start, EndOffset: &end}); got != "event-1:1:5" {
		t.Fatalf("span evidence key = %q, want event-1:1:5", got)
	}
	if got := contextEvidenceKey(domain.RevisionEvidence{EventID: "event-1", StartOffset: &start}); got != "" {
		t.Fatalf("half-span evidence key = %q, want empty", got)
	}
}

// TestContextSelectionKeepsDistinctSpanlessEvidenceMemories is the regression
// guard for committed batches: every memory of one Agent Tool commit cites the
// same event without offsets. They are distinct memories and must each consume
// a selection slot instead of collapsing into one duplicate cluster that hides
// the actual match.
func TestContextSelectionKeepsDistinctSpanlessEvidenceMemories(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	capability := NewAgentToolCapability(repository)
	params := agenttool.CommitParams{
		ScopeID: "space-1",
		Source: agenttool.Source{
			Type: "agent_tool", Name: "assistant", ConversationID: "spanless-conversation",
			TurnID: "turn-1", OccurredAt: time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC),
		},
		Memories: []agenttool.MemorySuggestion{
			{LocalID: "alpha", Kind: domain.MemoryKindFact, Content: "zephyr batch alpha", Sensitivity: agenttool.SensitivityNormal, Confidence: 0.9},
			{LocalID: "beta", Kind: domain.MemoryKindFact, Content: "zephyr batch beta", Sensitivity: agenttool.SensitivityNormal, Confidence: 0.9},
		},
	}
	record, replay, err := capability.CommitMemory(context.Background(), agentToolIdentity(t, "spanless-batch", params, agenttool.OperationMemoryCommit), params)
	if err != nil || replay || len(record.Memories) != 2 {
		t.Fatalf("commit span-less batch: record=%+v replay=%t err=%v", record, replay, err)
	}
	response, err := repository.SearchMemories(context.Background(), selectionSearchParams("zephyr batch", false))
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]bool{}
	for _, item := range response.Context.Items {
		selected[item.MemoryID] = true
	}
	for _, outcome := range record.Memories {
		if !selected[outcome.MemoryID] {
			t.Fatalf("span-less batch collapsed: selected=%v missing %s", selected, outcome.MemoryID)
		}
	}
}

// TestKeywordSelectionFloorKeepsLowCoverageMatches is the v3 regression guard
// for selection relevance. The memory is recalled by the keyword channel
// because it shares the subject name with a natural-language Chinese question,
// but its literal compiled-term coverage is far below the selection utility
// floor. The keyword relevance floor must keep it selected instead of letting
// selection clear the whole context.
func TestKeywordSelectionFloorKeepsLowCoverageMatches(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createMemoryWithContent(t, repository, "selection-floor", "Melanie moved to Berlin last year.")

	query := "Melanie最近庆祝了谁的生日？"
	compiled := compileKeyword(query)
	if coverage := retrieval.Coverage(memory.CurrentRevision.DisplayContent, compiled.Terms); coverage >= 0.20 {
		t.Fatalf("fixture literal coverage = %.3f, want below the lookup selection floor", coverage)
	}
	response, err := repository.SearchMemories(context.Background(), selectionSearchParams(query, true))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Memory.MemoryID != memory.MemoryID {
		t.Fatalf("keyword results = %+v", response.Results)
	}
	if response.Results[0].KeywordRelevance == nil {
		t.Fatalf("keyword candidate missing normalized relevance: %+v", response.Results[0])
	}
	if len(response.Context.Items) != 1 || response.Context.Items[0].MemoryID != memory.MemoryID {
		t.Fatalf("low-coverage keyword candidate was not selected: %+v", response.Context.Items)
	}
}

func TestContextSelectionCollapsesDuplicateCandidates(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	first := createMemoryWithContent(t, repository, "sel-first", "zephyr duplicate concept")
	second := createMemoryWithContent(t, repository, "sel-second", "zephyr duplicate concept")
	response, err := repository.SearchMemories(context.Background(), selectionSearchParams("zephyr duplicate concept", false))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 {
		t.Fatalf("raw ranked candidates = %d, want 2", len(response.Results))
	}
	if len(response.Context.Items) != 1 {
		t.Fatalf("selected context = %+v, want one representative", response.Context.Items)
	}
	item := response.Context.Items[0]
	representative, corroborated := first, second
	if item.MemoryID == second.MemoryID {
		representative, corroborated = second, first
	} else if item.MemoryID != first.MemoryID {
		t.Fatalf("unexpected representative %s", item.MemoryID)
	}
	_ = representative
	if len(item.CorroboratingMemoryIDs) != 1 || item.CorroboratingMemoryIDs[0] != corroborated.MemoryID {
		t.Fatalf("corroborating = %v, want [%s]", item.CorroboratingMemoryIDs, corroborated.MemoryID)
	}
	if len(response.Context.Rejected) != 0 {
		t.Fatalf("default context exposed rejections: %+v", response.Context.Rejected)
	}
	if response.ContextSelectionIdentity == "" || response.Context.Items[0].ClusterID == "" {
		t.Fatalf("selection identity/cluster missing: %q %+v", response.ContextSelectionIdentity, response.Context.Items[0])
	}
}

func TestContextSelectionExplainExposesRejectionsOnly(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "sel-explain-first", "zephyr explain concept")
	createMemoryWithContent(t, repository, "sel-explain-second", "zephyr explain concept")
	response, err := repository.SearchMemories(context.Background(), selectionSearchParams("zephyr explain concept", true))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Context.Items) != 1 || len(response.Context.Rejected) != 1 {
		t.Fatalf("explain selection items=%d rejected=%d", len(response.Context.Items), len(response.Context.Rejected))
	}
	if response.Context.Rejected[0].Reason != "duplicate_cluster" {
		t.Fatalf("rejection reason = %q", response.Context.Rejected[0].Reason)
	}
	if response.Context.Items[0].Explanation == nil || response.Context.Items[0].Explanation.SelectionReason == "" {
		t.Fatalf("explain context missing explanation: %+v", response.Context.Items[0])
	}
}

func TestContextSelectionBudgetIsHardCap(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "sel-budget-a", "budget concept alpha")
	createMemoryWithContent(t, repository, "sel-budget-b", "budget concept beta")
	params := selectionSearchParams("budget concept", false)
	params.MaxContextTokens = 12
	params.MaxChars = 12
	response, err := repository.SearchMemories(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if response.Context.Accounting.TokensUsed > 12 {
		t.Fatalf("token budget exceeded: %+v", response.Context.Accounting)
	}
	if len(response.Context.Items) > 1 {
		t.Fatalf("result budget not enforced: %+v", response.Context.Items)
	}
}

func TestContextSelectionProtectsExactPrimaryOverAggregate(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	source := createMemoryWithContent(t, repository, "sel-source", "zephyr protected source")
	params := aggregateProposalParams(aggregateInput(source))
	params.Content = "zephyr protected source summary"
	proposal := createAggregateProposalTest(t, repository, "sel-aggregate-proposal", params)
	confirmed := confirmAggregateTest(t, repository, "sel-aggregate-confirm", proposal)
	aggregate, err := repository.GetAggregate(context.Background(), *confirmed.ResultMemoryID)
	if err != nil || aggregate.AggregateKind != "scene" {
		t.Fatalf("aggregate kind = %+v err=%v", aggregate, err)
	}
	response, err := repository.SearchMemories(context.Background(), selectionSearchParams("zephyr protected source", true))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Context.Items) != 1 {
		t.Fatalf("selected = %+v", response.Context.Items)
	}
	if response.Context.Items[0].MemoryID != source.MemoryID {
		t.Fatalf("broad aggregate consumed the primary slot: %+v", response.Context.Items[0])
	}
	if len(response.Context.Items[0].SupplementaryMemoryIDs) == 0 || response.Context.Items[0].SupplementaryMemoryIDs[0] != *confirmed.ResultMemoryID {
		t.Fatalf("aggregate synthesis not attached as supplementary: %+v", response.Context.Items[0])
	}
}

func TestContextSelectionExcludesStaleAggregateByDefault(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	input := createTestMemory(t, repository, "stale-input")
	proposal := createAggregateProposalTest(t, repository, "stale-proposal", aggregateProposalParams(aggregateInput(input)))
	confirmed := confirmAggregateTest(t, repository, "stale-confirm", proposal)
	revise := ReviseMemoryParams{ExpectedRevisionID: input.CurrentRevision.RevisionID, Content: "changed input", Evidence: directEvidence("event-2"), Reason: "changed"}
	if _, _, _, err := repository.ReviseMemory(context.Background(), memoryIdentity("stale-revise", "/v1/memories/{memory_id}", revise), input.MemoryID, revise); err != nil {
		t.Fatal(err)
	}
	if aggregate, err := repository.GetAggregate(context.Background(), *confirmed.ResultMemoryID); err != nil || !aggregate.AggregateStale {
		t.Fatalf("aggregate not stale: %+v err=%v", aggregate, err)
	}

	excludeParams := selectionSearchParams("aggregate output", false)
	excludeParams.ExcludeStaleAggregates = true
	excluded, err := repository.SearchMemories(context.Background(), excludeParams)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range excluded.Context.Items {
		if item.MemoryID == *confirmed.ResultMemoryID {
			t.Fatalf("stale aggregate appeared in default context: %+v", item)
		}
	}
	params := selectionSearchParams("aggregate output", true)
	params.ExcludeStaleAggregates = false
	provenance, err := repository.SearchMemories(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range provenance.Context.Items {
		if item.MemoryID != *confirmed.ResultMemoryID {
			continue
		}
		found = true
		if !item.Stale || len(item.StaleReasons) == 0 || len(item.ProvenanceMemoryIDs) == 0 {
			t.Fatalf("debug stale aggregate missing reason/provenance: %+v", item)
		}
	}
	if !found {
		t.Fatalf("debug inclusion did not return stale aggregate: %+v", provenance.Context.Items)
	}
}
