package retrieval

import (
	"reflect"
	"testing"
)

// TestCompileQuerySegmentsRecoversWordBoundaries locks the deterministic
// function-word split that gives CJK literal recall a word-boundary tier. It
// carries no lexicon and no segmentation model, so the cases below document the
// exact behavior, including the conservative cases that must not tighten recall.
func TestCompileQuerySegmentsRecoversWordBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		// A possessive or verb marker separates two content spans.
		{"possessive", "数据库连接池的负责人是谁", []string{"数据库连接池", "负责人"}},
		{"two_spans", "支付网关的上线时间", []string{"支付网关", "上线时间"}},
		{"facet", "产品评审的预算", []string{"产品评审", "预算"}},
		{"three_spans", "灰度发布的回滚方案", []string{"灰度发布", "回滚方案"}},
		// A query with no function word at all yields a single span, so there is
		// no word boundary to enforce.
		{"no_boundary", "上海产品评审会议", nil},
		{"single_word", "数据库连接池", nil},
		// Leading question markers leave one content span; requiring it as a
		// phrase would tighten recall without a boundary, so it stays broad.
		{"question_prefix", "什么是数据库连接池", nil},
		{"question_suffix", "数据库连接池怎么样", nil},
		{"single_marker", "灰度发布", nil},
		// A short fragment is not a usable span.
		{"short_fragments", "数据库的库", nil},
		// Latin text is a boundary, but a single CJK span remains, so the query
		// stays broad.
		{"mixed", "灰度 deploy", nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := CompileQuerySegments(testCase.query); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("CompileQuerySegments(%q) = %v, want %v", testCase.query, got, testCase.want)
			}
		})
	}
}

// TestCompileKeywordPublishesSegments proves the compiler carries the spans on
// the compiled query so recall can use the same deterministic partition.
func TestCompileKeywordPublishesSegments(t *testing.T) {
	query := CompileKeyword("数据库连接池的负责人是谁")
	if !reflect.DeepEqual(query.Segments, []string{"数据库连接池", "负责人"}) {
		t.Fatalf("compiled segments = %v", query.Segments)
	}
	if query.Version != KeywordCompilerVersion {
		t.Fatalf("compiled version = %q, want %q", query.Version, KeywordCompilerVersion)
	}
	broad := CompileKeyword("上海产品评审会议")
	if len(broad.Segments) != 0 {
		t.Fatalf("boundary-free query published segments: %v", broad.Segments)
	}
}
