package storage

import (
	"context"
	"testing"
)

// TestKeywordRecallServesShortCJKTermsByLiteralSubstring is the regression guard
// for the literal fallback path. A one or two rune CJK term cannot form a
// trigram, so it is served by the literal substring path; that path must return
// a candidate that survives materialization against the live revision.
func TestKeywordRecallServesShortCJKTermsByLiteralSubstring(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	short := createMemoryWithContent(t, repository, "cjk-short", "支付网关的灰度发布已经完成。")
	other := createMemoryWithContent(t, repository, "cjk-other", "数据库连接池扩容到 200。")

	compiled := compileKeyword("支付")
	if compiled.Match != "" || len(compiled.ShortTerms) != 1 || compiled.ShortTerms[0] != "支付" {
		t.Fatalf("two-rune CJK query did not compile to the literal path: %+v", compiled)
	}

	result, err := repository.SearchMemories(context.Background(), SearchParams{
		Query: "支付", SpaceIDs: []string{"space-1"}, States: []string{"active"},
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range result.Results {
		if item.Memory.MemoryID == short.MemoryID {
			found = true
		}
		if item.Memory.MemoryID == other.MemoryID {
			t.Fatalf("unrelated memory recalled for a short CJK term: %s", other.MemoryID)
		}
	}
	if !found {
		t.Fatalf("short CJK term returned no candidate: %+v", result.Results)
	}
	if len(result.Context.Items) != 1 || result.Context.Items[0].MemoryID != short.MemoryID {
		t.Fatalf("short CJK candidate was not selected into the returned context: %+v", result.Context.Items)
	}
}

// TestKeywordRecallServesShortCJKTermsInsideMixedQueries proves the literal path
// runs in parallel with the FTS path. Deriving it from "the compiled expression
// is empty" would drop the CJK terms of every query that also carries a latin
// term, so the CJK term must still contribute recall.
func TestKeywordRecallServesShortCJKTermsInsideMixedQueries(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	deploy := createMemoryWithContent(t, repository, "mixed-deploy", "部署 deploy 脚本放在 ops 目录。")
	unrelated := createMemoryWithContent(t, repository, "mixed-unrelated", "用户偏好使用 PostgreSQL 数据库。")

	compiled := compileKeyword("部署 deploy")
	if compiled.Match == "" || len(compiled.ShortTerms) != 1 || compiled.ShortTerms[0] != "部署" {
		t.Fatalf("mixed query did not compile to both recall paths: %+v", compiled)
	}

	result, err := repository.SearchMemories(context.Background(), SearchParams{
		Query: "部署 deploy", SpaceIDs: []string{"space-1"}, States: []string{"active"},
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range result.Results {
		if item.Memory.MemoryID == deploy.MemoryID {
			found = true
		}
		if item.Memory.MemoryID == unrelated.MemoryID {
			t.Fatalf("unrelated memory recalled for a mixed query: %s", unrelated.MemoryID)
		}
	}
	if !found {
		t.Fatalf("mixed query dropped its CJK term: %+v", result.Results)
	}
}

// TestKeywordRecallSplitsUnspacedMixedCJKQueries is the end-to-end regression
// guard for the v3 compiler defect: a latin name glued to a Chinese question
// compiled into one literal phrase, FTS MATCH returned zero rows, and the
// keyword channel produced no candidate. v4 splits at the script boundary, so
// the name and the CJK trigrams both participate in recall without a provider.
func TestKeywordRecallSplitsUnspacedMixedCJKQueries(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	matched := createMemoryWithContent(t, repository, "mixed-nospace-match", "Melanie最近庆祝了她的生日，和家人一起。")
	unrelated := createMemoryWithContent(t, repository, "mixed-nospace-other", "数据库连接池扩容到 200 个连接。")

	compiled := compileKeyword("Melanie最近庆祝了谁的生日？")
	if compiled.Match == "" {
		t.Fatalf("unspaced mixed query did not compile: %+v", compiled)
	}
	for _, term := range compiled.Terms {
		if term.Required {
			t.Fatalf("natural-language mixed query produced required term %+v", term)
		}
	}

	result, err := repository.SearchMemories(context.Background(), SearchParams{
		Query: "Melanie最近庆祝了谁的生日？", SpaceIDs: []string{"space-1"}, States: []string{"active"},
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range result.Results {
		if item.Memory.MemoryID == matched.MemoryID {
			found = true
		}
		if item.Memory.MemoryID == unrelated.MemoryID {
			t.Fatalf("unrelated memory recalled for an unspaced mixed query: %s", unrelated.MemoryID)
		}
	}
	if !found {
		t.Fatalf("unspaced mixed query returned no candidate: %+v", result.Results)
	}
	if len(result.Context.Items) != 1 || result.Context.Items[0].MemoryID != matched.MemoryID {
		t.Fatalf("unspaced mixed candidate was not selected into the returned context: %+v", result.Context.Items)
	}
}

// TestKeywordSubstringFallbackRanksByLiteralCoverage proves the literal path has
// a relevance order of its own. It has no BM25 score, so ranking only by
// recency would let an arbitrary recent document win over a better literal
// match.
func TestKeywordSubstringFallbackRanksByLiteralCoverage(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	// Two disjoint two-rune segments compile to two literal terms, so coverage
	// ranking is observable. The oldest memory covers both terms; the newest
	// covers only one.
	best := createMemoryWithContent(t, repository, "coverage-both", "支付上网方案已经确认。")
	createMemoryWithContent(t, repository, "coverage-one-a", "支付方式改成月结。")
	createMemoryWithContent(t, repository, "coverage-one-b", "上网带宽需要扩容。")

	tx, err := repository.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	params := SearchParams{Query: "支付 上网", States: []string{"active"}, ReferenceTime: formatTime(repository.runtime.Now())}
	candidates, err := repository.keywordSubstringRecall(context.Background(), tx, params, []string{"space-1"}, compileKeyword("支付 上网"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("literal fallback candidates = %d, want 3", len(candidates))
	}
	if candidates[0].memoryID != best.MemoryID {
		t.Fatalf("literal fallback did not rank by coverage: got %s first, want %s", candidates[0].memoryID, best.MemoryID)
	}
	if candidates[0].shortTermMatches != 2 || candidates[1].shortTermMatches != 1 || candidates[2].shortTermMatches != 1 {
		t.Fatalf("literal match counts = %d/%d/%d, want 2/1/1",
			candidates[0].shortTermMatches, candidates[1].shortTermMatches, candidates[2].shortTermMatches)
	}
}
