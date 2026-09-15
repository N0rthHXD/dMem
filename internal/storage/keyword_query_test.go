package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

func keywordTermByText(query retrieval.KeywordQuery, text string) (retrieval.KeywordTerm, bool) {
	for _, term := range query.Terms {
		if term.Text == text {
			return term, true
		}
	}
	return retrieval.KeywordTerm{}, false
}

func TestCompileKeywordPreservesNamesAcronymsIdentifiersDatesAndNumbers(t *testing.T) {
	query := compileKeyword("Did Ann and NASA meet about ZXQ-1847 on 2026-08-21 for project 42?")
	for _, want := range []string{"Ann", "NASA", "ZXQ-1847", "2026-08-21", "42"} {
		term, ok := keywordTermByText(query, want)
		if !ok {
			t.Fatalf("compiled terms %+v missing %q", query.Terms, want)
		}
		if (want == "NASA" || want == "ZXQ-1847") && !term.Required {
			t.Fatalf("term %q required=%v, want required", want, term.Required)
		}
		if (want == "2026-08-21" || want == "42") && !term.Exact {
			t.Fatalf("term %q exact=%v, want exact", want, term.Exact)
		}
	}
	if !query.Searchable || query.Match == "" {
		t.Fatalf("compiled query = %+v", query)
	}
	if !strings.Contains(query.Match, "Ann") {
		t.Fatalf("short proper name dropped from match: %q", query.Match)
	}
}

func TestCompileKeywordQuotedPhrasesAreRequiredExact(t *testing.T) {
	query := compileKeyword(`find the "quarterly revenue report" now`)
	term, ok := keywordTermByText(query, "quarterly revenue report")
	if !ok {
		t.Fatalf("quoted phrase missing: %+v", query.Terms)
	}
	if !term.Required || !term.Exact || term.Kind != retrieval.TermPhrase {
		t.Fatalf("quoted phrase term = %+v", term)
	}
}

func TestCompileKeywordCJKFallbackForShortSegments(t *testing.T) {
	// Two runes cannot form a trigram: emit a bigram fallback term.
	short := compileKeyword("上海")
	if len(short.ShortTerms) == 0 || short.Match != "" {
		t.Fatalf("two-rune CJK fallback = %+v", short)
	}
	// A single rune similarly falls back to a unigram.
	single := compileKeyword("沪")
	if len(single.ShortTerms) != 1 || single.ShortTerms[0] != "沪" {
		t.Fatalf("single-rune CJK fallback = %+v", single)
	}
	// Three or more runes form trigrams served by the FTS trigram tokenizer.
	long := compileKeyword("上海旅游住")
	if long.Match == "" || len(long.ShortTerms) != 0 {
		t.Fatalf("trigram CJK query = %+v", long)
	}
}

func TestCompileKeywordRequiredAndOptionalClauses(t *testing.T) {
	query := compileKeyword("what did Alice report about TOKEN42")
	if !strings.Contains(query.Match, "AND") {
		t.Fatalf("match lacks required term conjunction: %q", query.Match)
	}
	if !strings.Contains(query.Match, "OR") {
		t.Fatalf("match lacks optional term disjunction: %q", query.Match)
	}
}

func TestTermWeightRewardsRareTerms(t *testing.T) {
	common := retrieval.TermWeight(0.6, 900, 1000)
	rare := retrieval.TermWeight(0.6, 2, 1000)
	if rare <= common {
		t.Fatalf("rare term weight %v <= common term weight %v", rare, common)
	}
}

func TestKeywordSearchFindsShortProperName(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	short := createMemoryWithContent(t, repository, "short-name", "Meeting notes for Ann and Bo")
	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "Ann", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 10, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range result.Results {
		if item.Memory.MemoryID == short.MemoryID {
			found = true
		}
	}
	if !found {
		t.Fatalf("short proper name search results = %+v", result.Results)
	}
}

// TestKeywordCandidatesIdenticalAcrossPaths proves hybrid recall uses the same
// keyword candidate order and tie-break as keyword-only recall.
func TestKeywordCandidatesIdenticalAcrossPaths(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMemoryWithContent(t, repository, "path-keyword-a", "ZOOMTOP alpha report")
	createMemoryWithContent(t, repository, "path-keyword-b", "ZOOMTOP beta report")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	params := SearchParams{Query: "ZOOMTOP report", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 10, MaxChars: 10000}
	keywordOnly, err := repository.SearchMemories(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	params.Channels = []string{"keyword", "semantic"}
	hybrid, err := repository.SearchMemories(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	order := func(items []domain.SearchResult) []string {
		result := make([]string, 0, len(items))
		for _, item := range items {
			if hasChannel(item.MatchedChannels, ChannelKeyword) {
				result = append(result, item.Memory.MemoryID+"\x00"+item.Memory.CurrentRevision.RevisionID)
			}
		}
		return result
	}
	if strings.Join(order(keywordOnly.Results), ",") != strings.Join(order(hybrid.Results), ",") {
		t.Fatalf("keyword candidate order differs:\nkeyword-only=%v\nhybrid=%v", order(keywordOnly.Results), order(hybrid.Results))
	}
}

func TestHybridKeywordRecallIncludesShortTerms(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	short := createMemoryWithContent(t, repository, "hybrid-short-name", "Meeting notes for Ann and Bo")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "Ann", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "semantic"}, Limit: 10, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	item, ok := searchResultsByID(result)[short.MemoryID]
	if !ok || !hasChannel(item.MatchedChannels, ChannelKeyword) {
		t.Fatalf("hybrid short-term keyword result = %+v", result.Results)
	}
}
