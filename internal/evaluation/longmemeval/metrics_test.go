package longmemeval

import (
	"math"
	"testing"
)

func TestRetrievalMetricsMatchesOfficialFormula(t *testing.T) {
	metric := RetrievalMetrics([]bool{false, true, true}, 2, 3)
	want := (1/math.Log2(2) + 1/math.Log2(3)) / (1 + 1/math.Log2(2))
	if metric.RecallAny != 1 || metric.RecallAll != 1 || math.Abs(metric.NDCG-want) > 1e-12 {
		t.Fatalf("metric = %+v, want nDCG %.12f", metric, want)
	}
}

func TestSessionRetrievalMetricsDeduplicatesSessions(t *testing.T) {
	metric := SessionRetrievalMetrics([]string{"filler", "filler", "answer-a", "answer-b"}, []string{"answer-a", "answer-b"}, 3)
	if metric.RecallAny != 1 || metric.RecallAll != 1 {
		t.Fatalf("metric = %+v", metric)
	}
}

func TestRetrievalMetricsWithNoLabelsIsZero(t *testing.T) {
	metric := RetrievalMetrics([]bool{true}, 0, 1)
	if metric.RecallAll != 0 || metric.NDCG != 0 {
		t.Fatalf("metric = %+v", metric)
	}
}
