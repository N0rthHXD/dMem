package storage

import (
	"context"
	"strings"
	"testing"
)

func TestCompileKeywordQuery(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "english question", input: "What camera did I buy for my trip?", want: `"camera" OR "trip"`, ok: true},
		{name: "chinese question", input: "我上次去上海旅游住的是哪家酒店？", want: `"我上次" OR "上次去" OR "次去上" OR "去上海" OR "上海旅" OR "海旅游" OR "旅游住" OR "游住的" OR "住的是" OR "的是哪" OR "是哪家" OR "哪家酒" OR "家酒店"`, ok: true},
		{name: "mixed language", input: "我购买的 Sony camera 是什么型号？", want: `"我购买" OR "购买的" OR "Sony" OR "camera" OR "是什么" OR "什么型" OR "么型号"`, ok: true},
		{name: "hyphenated identifier", input: "Where is order ZXQ-1847?", want: `"order" OR "ZXQ-1847"`, ok: true},
		{name: "fts operators are literals", input: `NEAR( OR AND "camera"`, want: `"NEAR" OR "camera"`, ok: true},
		{name: "deduplicates case insensitively", input: "Camera camera CAMERA", want: `"Camera"`, ok: true},
		{name: "no searchable term", input: "我 你 the an buy", want: "", ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := compileKeywordQuery(test.input)
			if got != test.want || ok != test.ok {
				t.Fatalf("compileKeywordQuery(%q) = %q, %t; want %q, %t", test.input, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestCompileKeywordQueryCapsTerms(t *testing.T) {
	query, ok := compileKeywordQuery("alpha bravo charlie delta echoo foxtrot golfy hotel india juliet kiloos limaos mikeos november oscarp papaqq quebec romeoo sierra tangoa uniform victor whiskey xrayyy yankee zuluuu extraa")
	if !ok || strings.Count(query, " OR ")+1 != maximumKeywordTerms {
		t.Fatalf("compiled query = %q; want %d terms", query, maximumKeywordTerms)
	}
}

func TestKeywordSearchCompilesNaturalLanguageQueries(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	english := createMemoryWithContent(t, repository, "natural-language-english", "The camera for the Kyoto trip was a Fujifilm X100VI.")
	chinese := createMemoryWithContent(t, repository, "natural-language-chinese", "上次上海旅游住在东方酒店。")
	literal := createMemoryWithContent(t, repository, "natural-language-literal", "NEAR diagnostics are enabled.")

	for _, test := range []struct {
		query string
		want  string
	}{
		{query: "What camera did I buy for my trip?", want: english.MemoryID},
		{query: "我上次去上海旅游住的是哪家酒店？", want: chinese.MemoryID},
		{query: "NEAR(", want: literal.MemoryID},
	} {
		result, err := repository.SearchMemories(context.Background(), SearchParams{Query: test.query, SpaceIDs: []string{"space-1"}, States: []string{"active"}, Mode: "keyword", Limit: 10, MaxChars: 10000})
		if err != nil {
			t.Fatalf("keyword search %q: %v", test.query, err)
		}
		found := false
		for _, item := range result.Results {
			if item.Memory.MemoryID == test.want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("keyword search %q results = %+v; missing %s", test.query, result.Results, test.want)
		}
	}
}
