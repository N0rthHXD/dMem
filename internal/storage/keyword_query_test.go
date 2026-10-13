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

// TestCompileKeywordSplitsUnspacedMixedCJKQueries is the v4 regression guard
// for the compiler defect that swallowed a CJK run into a preceding latin or
// digit token. The glued token became one literal FTS clause that could never
// match, so natural-language questions with an unspaced script mix returned
// zero candidates. The compiler must split at the script boundary instead.
func TestCompileKeywordSplitsUnspacedMixedCJKQueries(t *testing.T) {
	cases := []struct {
		name          string
		query         string
		glued         string
		wantTerms     []string
		wantKinds     map[string]retrieval.TermKind
		wantShort     []string
		wantMatchText []string
	}{
		{
			name:          "latin_name_and_question",
			query:         "Melanie最近庆祝了谁的生日？",
			glued:         "Melanie最近",
			wantTerms:     []string{"Melanie", "最近庆", "近庆祝", "庆祝了", "祝了谁", "了谁的", "谁的生", "的生日"},
			wantKinds:     map[string]retrieval.TermKind{"Melanie": retrieval.TermWord},
			wantMatchText: []string{`"Melanie"`, `"最近庆"`},
		},
		{
			name:          "latin_name_and_verb",
			query:         "Caroline养了什么宠物？",
			glued:         "Caroline养",
			wantTerms:     []string{"Caroline", "养了什", "了什么", "什么宠", "么宠物"},
			wantMatchText: []string{`"Caroline"`, `"养了什"`},
		},
		{
			name:          "latin_name_and_pronoun",
			query:         "Oliver曾经把他的骨头藏在哪里？",
			glued:         "Oliver曾",
			wantTerms:     []string{"Oliver", "曾经把", "经把他", "把他的", "他的骨", "的骨头", "骨头藏", "头藏在", "藏在哪", "在哪里"},
			wantMatchText: []string{`"Oliver"`, `"曾经把"`},
		},
		{
			name:          "digit_and_cjk",
			query:         "4年前从哪里搬来的？",
			glued:         "4年",
			wantTerms:     []string{"4", "年前从", "前从哪", "从哪里", "哪里搬", "里搬来", "搬来的"},
			wantKinds:     map[string]retrieval.TermKind{"4": retrieval.TermNumber},
			wantShort:     []string{"4"},
			wantMatchText: []string{`"年前从"`},
		},
		{
			name:          "year_month_and_cjk",
			query:         "2023年7月的最新项目…",
			glued:         "2023年",
			wantTerms:     []string{"2023", "7", "年", "月的最", "的最新", "最新项", "新项目"},
			wantKinds:     map[string]retrieval.TermKind{"2023": retrieval.TermNumber, "7": retrieval.TermNumber},
			wantShort:     []string{"7", "年"},
			wantMatchText: []string{`"2023"`, `"最新项"`},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			query := compileKeyword(testCase.query)
			if !query.Searchable || query.Match == "" {
				t.Fatalf("compiled query = %+v", query)
			}
			for _, term := range query.Terms {
				if term.Required {
					t.Fatalf("natural-language query produced required term %+v in %+v", term, query.Terms)
				}
			}
			for _, want := range testCase.wantTerms {
				term, ok := keywordTermByText(query, want)
				if !ok {
					t.Fatalf("compiled terms %+v missing %q", query.Terms, want)
				}
				if wantKind, ok := testCase.wantKinds[want]; ok && term.Kind != wantKind {
					t.Fatalf("term %q kind = %q, want %q", want, term.Kind, wantKind)
				}
			}
			for _, want := range testCase.wantShort {
				found := false
				for _, short := range query.ShortTerms {
					if short == want {
						found = true
					}
				}
				if !found {
					t.Fatalf("short terms %v missing %q", query.ShortTerms, want)
				}
			}
			for _, want := range testCase.wantMatchText {
				if !strings.Contains(query.Match, want) {
					t.Fatalf("match %q missing %s", query.Match, want)
				}
			}
			// The mixed token must never survive as one literal clause: a
			// match containing it would scan for a phrase no memory can hold.
			if strings.Contains(query.Match, testCase.glued) {
				t.Fatalf("match %q still contains the glued token %q", query.Match, testCase.glued)
			}
		})
	}
}

// TestCompileKeywordKeepsAcronymIdentifierAndDateClassification documents the
// boundary the v4 script split must not move: a spaced acronym stays required
// and exact, an identifier keeps its classification, a date is exact but never
// required, and a non-latin letter glued to a digit is no longer mistaken for a
// latin identifier.
func TestCompileKeywordKeepsAcronymIdentifierAndDateClassification(t *testing.T) {
	acronym := compileKeyword("LGBTQ 群体")
	term, ok := keywordTermByText(acronym, "LGBTQ")
	if !ok || !term.Required || !term.Exact || term.Kind != retrieval.TermAcronym {
		t.Fatalf("acronym term = %+v, ok=%t", term, ok)
	}
	if len(acronym.ShortTerms) != 1 || acronym.ShortTerms[0] != "群体" {
		t.Fatalf("spaced CJK bigram fallback = %+v", acronym)
	}

	identifier := compileKeyword("ZXQ-1847")
	term, ok = keywordTermByText(identifier, "ZXQ-1847")
	if !ok || !term.Required || !term.Exact || term.Kind != retrieval.TermIdentifier {
		t.Fatalf("identifier term = %+v, ok=%t", term, ok)
	}

	date := compileKeyword("2026-08-21")
	term, ok = keywordTermByText(date, "2026-08-21")
	if !ok || term.Required || !term.Exact || term.Kind != retrieval.TermDate {
		t.Fatalf("date term = %+v, ok=%t", term, ok)
	}

	// Only latin letters form identifiers; another script glued to a digit
	// must not recreate the mixed-token defect under a different alphabet.
	nonLatin := compileKeyword("Х4")
	for _, item := range nonLatin.Terms {
		if item.Required {
			t.Fatalf("non-latin token classified as required: %+v", item)
		}
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
