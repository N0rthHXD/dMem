package storage

import (
	"context"
	"testing"

	"github.com/N0rthHXD/dMem/internal/retrieval"
)

// TestKeywordRecallAdmitsOnlyWordBoundaryMatches is the P1 precision guard.
// Trigrams are not word bounded, so an OR-of-trigrams query recalls memories
// that share only a three-rune window with one part of the query. When the query
// compiles word-boundary spans, only candidates covering every span are
// admitted, which removes the cross-word false positives without changing the
// recall path.
func TestKeywordRecallAdmitsOnlyWordBoundaryMatches(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	precise := createMemoryWithContent(t, repository, "p1-precise", "数据库连接池：负责人交给张伟。")
	topicOnly := createMemoryWithContent(t, repository, "p1-topic-only", "数据库连接池：暂停两周。")
	facetOnly := createMemoryWithContent(t, repository, "p1-facet-only", "支付网关：负责人交给李娜。")

	query := "数据库连接池的负责人是谁"
	if segments := retrieval.CompileQuerySegments(query); len(segments) != 2 {
		t.Fatalf("query did not compile word-boundary spans: %v", segments)
	}
	result, err := repository.SearchMemories(context.Background(), SearchParams{
		Query: query, SpaceIDs: []string{"space-1"}, States: []string{"active"},
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 100000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Memory.MemoryID != precise.MemoryID {
		t.Fatalf("precise tier results = %+v, want only the memory covering both spans", result.Results)
	}
	for _, item := range result.Results {
		if item.Memory.MemoryID == topicOnly.MemoryID || item.Memory.MemoryID == facetOnly.MemoryID {
			t.Fatalf("candidate sharing only one query span survived admission: %s", item.Memory.MemoryID)
		}
	}

	// A boundary-free query has no word boundary to enforce, so the broad
	// trigram recall is returned unchanged rather than emptied.
	broadMatch := createMemoryWithContent(t, repository, "p1-broad", "上海产品评审会议改到下周一举行。")
	broad, err := repository.SearchMemories(context.Background(), SearchParams{
		Query: "上海产品评审会议", SpaceIDs: []string{"space-1"}, States: []string{"active"},
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 100000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(broad.Results) != 1 || broad.Results[0].Memory.MemoryID != broadMatch.MemoryID {
		t.Fatalf("boundary-free query results = %+v, want the single trigram match", broad.Results)
	}
}

// TestKeywordRecallFallsBackWhenNoCandidateCoversEverySpan proves the precise
// tier cannot collapse recall. A memory that answers the question while wording
// the facet differently does not literally repeat the query spans, so the broad
// recall is kept whole instead of returning nothing.
func TestKeywordRecallFallsBackWhenNoCandidateCoversEverySpan(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	// The answer shares the topic word but phrases the facet as 维护.
	answer := createMemoryWithContent(t, repository, "p1-paraphrase", "张伟负责数据库连接池的日常维护。")
	createMemoryWithContent(t, repository, "p1-unrelated", "支付方式改成月结。")

	query := "数据库连接池的负责人是谁"
	result, err := repository.SearchMemories(context.Background(), SearchParams{
		Query: query, SpaceIDs: []string{"space-1"}, States: []string{"active"},
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 100000,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range result.Results {
		if item.Memory.MemoryID == answer.MemoryID {
			found = true
		}
	}
	if !found {
		t.Fatalf("precise tier emptied recall instead of falling back: %+v", result.Results)
	}
}

// TestKeywordRecallPreciseTierKeepsShortCJKCandidates proves the two P0 literal
// paths still feed the precise tier: a short CJK term is served by the literal
// path and must survive admission.
func TestKeywordRecallPreciseTierKeepsShortCJKCandidates(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	short := createMemoryWithContent(t, repository, "p1-short", "支付网关的灰度发布已经完成。")

	result, err := repository.SearchMemories(context.Background(), SearchParams{
		Query: "支付", SpaceIDs: []string{"space-1"}, States: []string{"active"},
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 100000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Memory.MemoryID != short.MemoryID {
		t.Fatalf("short CJK literal candidate did not survive the precise tier: %+v", result.Results)
	}
}
