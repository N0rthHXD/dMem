package storage

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

func TestParseQueryTimeAbsoluteAndRelative(t *testing.T) {
	reference := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		query     string
		precision string
	}{
		{"what happened yesterday", "day"},
		{"what happened today", "day"},
		{"what happened last week", "week"},
		{"what happened last month", "month"},
		{"what happened last year", "year"},
		{"what happened 3 days ago", "approximate"},
		{"what happened on 2025-03-04", "day"},
		{"what happened in 2024", "year"},
	}
	for _, test := range cases {
		interval, wording, ok := retrieval.ParseQueryTime(test.query, reference)
		if !ok || !interval.Set || interval.Precision != test.precision {
			t.Fatalf("ParseQueryTime(%q) = %+v/%q/%v", test.query, interval, wording, ok)
		}
		if wording == "" {
			t.Fatalf("ParseQueryTime(%q) lost source wording", test.query)
		}
	}
}

func TestTemporalMatchOverlapDistanceAndImpossibility(t *testing.T) {
	query := retrieval.TimeInterval{Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Set: true}
	overlap := retrieval.TimeInterval{Start: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC), Set: true}
	if score, compatible := retrieval.TemporalMatch(query, overlap); !compatible || score != 1 {
		t.Fatalf("overlap score = %v compatible=%v", score, compatible)
	}
	near := retrieval.TimeInterval{Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), Set: true}
	if score, compatible := retrieval.TemporalMatch(query, near); !compatible || score <= 0 || score >= 1 {
		t.Fatalf("near score = %v compatible=%v", score, compatible)
	}
	far := retrieval.TimeInterval{Start: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC), Set: true}
	if _, compatible := retrieval.TemporalMatch(query, far); compatible {
		t.Fatal("far interval reported compatible")
	}
	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !retrieval.ImpossibleInterval(&start, &end) {
		t.Fatal("reversed interval not flagged impossible")
	}
}

func TestParseQueryTimeRejectsImpossibleCalendarDate(t *testing.T) {
	if _, _, ok := retrieval.ParseQueryTime("what happened on 2026-02-31", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)); ok {
		t.Fatal("impossible calendar date was normalized instead of rejected")
	}
}

func TestTemporalPrecisionAffectsScore(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	query := retrieval.TimeInterval{Start: start, End: end, Set: true}
	exact, _ := retrieval.TemporalMatch(query, retrieval.TimeInterval{Start: start, End: end, Precision: "day", Set: true})
	approximate, _ := retrieval.TemporalMatch(query, retrieval.TimeInterval{Start: start, End: end, Precision: "approximate", Set: true})
	if exact <= approximate {
		t.Fatalf("exact precision score %v <= approximate %v", exact, approximate)
	}
}

func TestTemporalFilterAlwaysRejectsImpossibleIntervals(t *testing.T) {
	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	filtered := applyTemporalFilter([]*searchCandidate{{eventStart: &start, eventEnd: &end}}, retrieval.TimeInterval{}, retrieval.QuestionLookup)
	if len(filtered) != 0 {
		t.Fatalf("impossible interval survived without query time: %+v", filtered)
	}
}

func TestCandidateTimeSeparatesEventAndValidTime(t *testing.T) {
	event := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	valid := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	candidate := retrieval.CandidateTime{EventStart: &event, ValidFrom: &valid}
	temporal, ok := candidate.ForQuestion(retrieval.QuestionTemporal)
	if !ok || !temporal.Start.Equal(event) {
		t.Fatalf("temporal question used %+v, want event time", temporal)
	}
	lookup, ok := candidate.ForQuestion(retrieval.QuestionLookup)
	if !ok || !lookup.Start.Equal(valid) {
		t.Fatalf("lookup question used %+v, want valid time", lookup)
	}
}

func TestTemporalFilterRemovesIncompatibleCandidatesBeforeTopK(t *testing.T) {
	query := retrieval.TimeInterval{Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Set: true}
	farStart := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	farEnd := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
	candidates := []*searchCandidate{
		{memoryID: "compatible", revisionID: "r1", eventStart: &query.Start, eventEnd: &query.End, keywordRank: 1},
		{memoryID: "incompatible", revisionID: "r2", eventStart: &farStart, eventEnd: &farEnd, keywordRank: 2},
	}
	filtered := applyTemporalFilter(candidates, query, retrieval.QuestionTemporal)
	if len(filtered) != 1 || filtered[0].memoryID != "compatible" {
		t.Fatalf("temporal filter survivors = %+v", filtered)
	}
}

func TestTemporalExplainPreservesWordingIntervalAnchorAndPrecision(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createMemoryWithContent(t, repository, "temporal-explain", "temporalexplain report")
	setRevisionEventTime(t, repository, memory, "temporal-explain-event", "2026-08-20T00:00:00Z", "2026-08-21T00:00:00Z")
	result, err := repository.SearchMemories(context.Background(), SearchParams{
		Query: "temporalexplain report yesterday", SpaceIDs: []string{"space-1"}, States: []string{"active"},
		ReferenceTime: "2026-08-21T12:00:00Z", ReferenceTimeSet: true,
		Channels: []string{"keyword"}, Explain: true, Limit: 5, MaxChars: 1000,
	})
	if err != nil || len(result.Results) == 0 {
		t.Fatalf("temporal explain search = %+v, %v", result, err)
	}
	item := result.Results[0]
	if item.Memory.MemoryID != memory.MemoryID {
		t.Fatalf("temporal explain returned %v, want %s", resultIDs(result), memory.MemoryID)
	}
	if item.TemporalSourceWording != "yesterday" {
		t.Fatalf("temporal source wording = %q", item.TemporalSourceWording)
	}
	if item.QueryTimeStart == nil || item.QueryTimeEnd == nil || item.ReferenceTimeAnchor == nil {
		t.Fatalf("temporal interval/anchor not preserved: %+v", item)
	}
	if _, ok := item.RankingFeatures[retrieval.FeatureTemporal]; !ok {
		t.Fatalf("temporal feature missing from explain: %+v", item.RankingFeatures)
	}
}

func setRevisionEventTime(t *testing.T, repository *Repository, memory domain.Memory, revisionID, start, end string) {
	t.Helper()
	now := formatTime(repository.runtime.Now())
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_revisions(revision_id,memory_id,parent_revision_id,kind,display_content,canonical_content,canonical_hash,event_time_start,event_time_end,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES (?,?,?,'fact',?,?,?,?,?,'normal',0,0,'test','v1',?)`, revisionID, memory.MemoryID, memory.CurrentRevision.RevisionID, memory.CurrentRevision.DisplayContent, memory.CurrentRevision.CanonicalContent, memory.CurrentRevision.CanonicalHash, start, end, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`UPDATE memories SET current_revision_id=? WHERE memory_id=?; UPDATE memory_projections SET revision_id=? WHERE memory_id=?`, revisionID, memory.MemoryID, revisionID, memory.MemoryID); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildFTS(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTemporalQueryFiltersDistantCandidateBeforeTopK(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	near := createMemoryWithContent(t, repository, "temporal-near", "temporalword near report")
	far := createMemoryWithContent(t, repository, "temporal-far", "temporalword far report")
	setRevisionEventTime(t, repository, near, "temporal-near-event", "2026-01-15T00:00:00Z", "2026-01-16T00:00:00Z")
	setRevisionEventTime(t, repository, far, "temporal-far-event", "2030-01-01T00:00:00Z", "2030-01-02T00:00:00Z")
	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "temporalword report on 2026-01-15", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 10, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	byID := searchResultsByID(result)
	if _, present := byID[far.MemoryID]; present {
		t.Fatalf("temporally distant candidate returned: %v", resultIDs(result))
	}
	if _, present := byID[near.MemoryID]; !present {
		t.Fatalf("temporally compatible candidate missing: %v", resultIDs(result))
	}
}
