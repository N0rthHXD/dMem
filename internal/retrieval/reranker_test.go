package retrieval

import (
	"context"
	"errors"
	"testing"
)

func rerankWindow() RerankWindow {
	return RerankWindow{
		QueryHash:           "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		QueryType:           "lookup",
		RankingIdentity:     "ranking-weights-v1",
		FeatureIdentity:     "ranking-features-v1",
		CalibrationIdentity: "semantic-calibration-v1",
		ScopeIDs:            []string{"scope-a"},
		MaxTokens:           100,
		Candidates: []RerankCandidate{
			{MemoryID: "m1", RevisionID: "r1", ScopeID: "scope-a", Score: 0.9, EstimatedTokens: 20},
			{MemoryID: "m2", RevisionID: "r2", ScopeID: "scope-a", Score: 0.8, EstimatedTokens: 20},
			{MemoryID: "m3", RevisionID: "r3", ScopeID: "scope-a", Score: 0.7, EstimatedTokens: 20},
		},
	}
}

func TestDeterministicRerankerPreservesOrder(t *testing.T) {
	window := rerankWindow()
	result, err := (DeterministicReranker{}).Rerank(context.Background(), window, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRerankResult(window, (DeterministicReranker{}).Identity(), result); err != nil {
		t.Fatal(err)
	}
	if len(result.Order) != 3 || result.Order[0] != "m1\x00r1" || result.Order[2] != "m3\x00r3" {
		t.Fatalf("deterministic order = %v", result.Order)
	}
}

func TestRerankerCannotAddOrDropCandidates(t *testing.T) {
	window := rerankWindow()
	reranker := EvaluationReranker{Version: "test", Score: func(RerankWindow, RerankCandidate) float64 { return 0 }}
	result, err := reranker.Rerank(context.Background(), window, 10)
	if err != nil {
		t.Fatal(err)
	}
	added := result
	added.Order = append(append([]string{}, result.Order...), "m4\x00r4")
	if err := ValidateRerankResult(window, reranker.Identity(), added); !errors.Is(err, ErrRerankCandidateSetChanged) {
		t.Fatalf("added candidate error = %v", err)
	}
	dropped := result
	dropped.Order = dropped.Order[:2]
	if err := ValidateRerankResult(window, reranker.Identity(), dropped); !errors.Is(err, ErrRerankCandidateSetChanged) {
		t.Fatalf("dropped candidate error = %v", err)
	}
	swapped := result
	swapped.Order = []string{"m1\x00r1", "m1\x00r1", "m3\x00r3"}
	if err := ValidateRerankResult(window, reranker.Identity(), swapped); !errors.Is(err, ErrRerankCandidateSetChanged) {
		t.Fatalf("duplicate candidate error = %v", err)
	}
}

func TestRerankerRejectsStaleIdentity(t *testing.T) {
	window := rerankWindow()
	result, err := (DeterministicReranker{}).Rerank(context.Background(), window, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRerankResult(window, "reranker-interface-v1:other", result); !errors.Is(err, ErrRerankStaleIdentity) {
		t.Fatalf("stale identity error = %v", err)
	}
}

func TestRerankerWindowEnforcesScopeAndBudget(t *testing.T) {
	window := rerankWindow()
	if err := ValidateRerankWindow(window, 2); !errors.Is(err, ErrRerankWindowExceeded) {
		t.Fatalf("window cap error = %v", err)
	}
	crossScope := window
	crossScope.Candidates = append([]RerankCandidate{}, window.Candidates...)
	crossScope.Candidates[1].ScopeID = "scope-b"
	if err := ValidateRerankWindow(crossScope, 10); !errors.Is(err, ErrRerankScopeViolation) {
		t.Fatalf("scope error = %v", err)
	}
	overBudget := window
	overBudget.MaxTokens = 30
	if err := ValidateRerankWindow(overBudget, 10); !errors.Is(err, ErrRerankBudgetExceeded) {
		t.Fatalf("budget error = %v", err)
	}
}

func TestRerankerDefaultsToDeterministicUnlessEvaluationOptIn(t *testing.T) {
	window := rerankWindow()
	evaluation := EvaluationReranker{Version: "test-v1", Score: func(_ RerankWindow, candidate RerankCandidate) float64 {
		switch candidate.MemoryID {
		case "m3":
			return 1
		case "m2":
			return 0.5
		default:
			return 0
		}
	}}
	result, err := SelectReranker(RerankerModeOff, evaluation).Rerank(context.Background(), window, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Order[0] != "m1\x00r1" {
		t.Fatalf("off mode reordered: %v", result.Order)
	}
	evaluated, err := SelectReranker(RerankerModeEvaluation, evaluation).Rerank(context.Background(), window, 10)
	if err != nil {
		t.Fatal(err)
	}
	if evaluated.Order[0] != "m3\x00r3" {
		t.Fatalf("evaluation mode order = %v", evaluated.Order)
	}
}

func TestPromotionRequiresGainsWithoutDegradation(t *testing.T) {
	thresholds := RerankPromotionThresholds{MinimumMeanRankImprovement: 0.05, MaximumLatencyRatio: 1.5, MaximumVarianceIncrease: 0.1, MinimumWindowCount: 100}
	good := RerankPromotionEvidence{WindowCount: 120, MeanRankImprovement: 0.09, LatencyRatio: 1.2, VarianceIncrease: 0.05}
	if !PromotionAllowed(good, thresholds) {
		t.Fatal("good evidence was rejected")
	}
	degraded := good
	degraded.DegradationCount = 1
	if PromotionAllowed(degraded, thresholds) {
		t.Fatal("degradation allowed promotion")
	}
	small := good
	small.WindowCount = 10
	if PromotionAllowed(small, thresholds) {
		t.Fatal("small window count allowed promotion")
	}
	noisy := good
	noisy.VarianceIncrease = 0.5
	if PromotionAllowed(noisy, thresholds) {
		t.Fatal("excess variance allowed promotion")
	}
}
