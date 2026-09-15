package retrieval

import (
	"context"
	"math"
	"testing"
)

func pointer(value float64) *float64 { return &value }

func TestNormalizeMinMaxIsDeterministicAndStableForTies(t *testing.T) {
	got := NormalizeMinMax([]float64{1, 3, 2})
	want := []float64{0, 1, 0.5}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("NormalizeMinMax = %v, want %v", got, want)
		}
	}
	tied := NormalizeMinMax([]float64{5, 5, 5})
	for _, value := range tied {
		if value != 1 {
			t.Fatalf("equal values should normalize to 1, got %v", tied)
		}
	}
}

func TestPercentilesRankTopRelative(t *testing.T) {
	got := Percentiles([]float64{0.2, 0.9, 0.5})
	if got[1] != 1 || got[2] != 0.5 || got[0] != 0 {
		t.Fatalf("Percentiles = %v", got)
	}
}

func TestClamp01TreatsNaNAsZero(t *testing.T) {
	if Clamp01(math.NaN()) != 0 || Clamp01(-1) != 0 || Clamp01(2) != 1 || Clamp01(0.4) != 0.4 {
		t.Fatalf("Clamp01 bounds failed")
	}
}

func TestMissingFeaturesRemainMissing(t *testing.T) {
	weights := DefaultWeights()
	output := Score(weights, RankInput{Features: FeatureVector{Keyword: pointer(1)}})
	if output.Primary != weights.Keyword {
		t.Fatalf("primary = %v, want keyword weight %v", output.Primary, weights.Keyword)
	}
	if _, ok := (FeatureVector{}).Value(FeatureSemantic); ok {
		t.Fatal("missing semantic feature reported present")
	}
}

func TestPrimaryCombinesWeightedFeatures(t *testing.T) {
	weights := DefaultWeights()
	features := FeatureVector{
		Semantic: pointer(1), Keyword: pointer(1), Exact: pointer(1),
		Temporal: pointer(1), Quality: pointer(1), Agreement: pointer(1),
	}
	output := Score(weights, RankInput{Features: features})
	want := weights.Semantic + weights.Keyword + weights.Exact + weights.Temporal + weights.Quality + weights.Agreement
	if math.Abs(output.Primary-want) > 1e-12 {
		t.Fatalf("primary = %v, want %v", output.Primary, want)
	}
}

func TestPenaltyFeaturesAreNormalizedBoundedAndSubtracted(t *testing.T) {
	weights := DefaultWeights()
	features := FeatureVector{
		Semantic:      pointer(1),
		DuplicateRisk: pointer(1),
		Overbreadth:   pointer(1),
		StalePenalty:  pointer(1),
	}
	output := Score(weights, RankInput{Features: features})
	wantPenalty := 0.15 + 0.10 + 0.10
	if math.Abs(output.Penalty-wantPenalty) > 1e-12 {
		t.Fatalf("penalty = %v, want %v", output.Penalty, wantPenalty)
	}
	if math.Abs(output.Final-(output.Primary-wantPenalty)) > 1e-12 {
		t.Fatalf("final = %v, want primary %v minus penalty %v", output.Final, output.Primary, wantPenalty)
	}
	clamped := Score(weights, RankInput{Features: FeatureVector{Semantic: pointer(1), DuplicateRisk: pointer(5)}})
	if math.Abs(clamped.Penalty-0.15) > 1e-12 {
		t.Fatalf("out-of-range duplicate penalty not clamped: %v", clamped.Penalty)
	}
	negative := Score(weights, RankInput{Features: FeatureVector{Semantic: pointer(1), StalePenalty: pointer(-1)}})
	if negative.Penalty != 0 {
		t.Fatalf("negative penalty not clamped to zero: %v", negative.Penalty)
	}
}

func TestFeedbackFeatureIsBoundedAndAdditive(t *testing.T) {
	weights := DefaultWeights()
	base := Score(weights, RankInput{Features: FeatureVector{Semantic: pointer(0.5)}})
	boosted := Score(weights, RankInput{Features: FeatureVector{Semantic: pointer(0.5), Feedback: pointer(1)}})
	if math.Abs(boosted.Feedback-1) > 1e-12 || boosted.Final <= base.Final {
		t.Fatalf("feedback = %v final = %v base = %v", boosted.Feedback, boosted.Final, base.Final)
	}
	over := Score(weights, RankInput{Features: FeatureVector{Semantic: pointer(0.5), Feedback: pointer(10)}})
	if over.Feedback != 1 {
		t.Fatalf("out-of-range feedback not clamped: %v", over.Feedback)
	}
	negative := Score(weights, RankInput{Features: FeatureVector{Semantic: pointer(0.5), Feedback: pointer(-1)}})
	if negative.Feedback != -1 || negative.Final >= base.Final {
		t.Fatalf("negative feedback = %v final = %v base = %v", negative.Feedback, negative.Final, base.Final)
	}
	under := Score(weights, RankInput{Features: FeatureVector{Semantic: pointer(0.5), Feedback: pointer(-10)}})
	if under.Feedback != -1 {
		t.Fatalf("out-of-range negative feedback not clamped: %v", under.Feedback)
	}
}

func TestGraphFeatureNeverLeaksIntoPrimary(t *testing.T) {
	weights := DefaultWeights()
	output := Score(weights, RankInput{Features: FeatureVector{Graph: pointer(10)}, GraphAdmitted: true})
	if output.Primary != 0 {
		t.Fatalf("graph-only feature leaked into primary: %v", output.Primary)
	}
	// The graph boost is primary-relative; without primary relevance there is
	// nothing for graph to boost.
	if output.GraphBoost != 0 {
		t.Fatalf("graph boost without primary = %v", output.GraphBoost)
	}
	withPrimary := Score(weights, RankInput{Features: FeatureVector{Semantic: pointer(1), Graph: pointer(10)}, GraphAdmitted: true})
	expected := math.Min(weights.GraphBoostPrimaryCap*withPrimary.Primary, weights.GraphBoostGraphCap)
	if math.Abs(withPrimary.GraphBoost-expected) > 1e-12 {
		t.Fatalf("graph boost = %v, want capped %v", withPrimary.GraphBoost, expected)
	}
}

func TestGraphBoostIsGatedAndCappedRelativeToPrimary(t *testing.T) {
	weights := DefaultWeights()
	features := FeatureVector{Semantic: pointer(1), Graph: pointer(1)}
	output := Score(weights, RankInput{Features: features, GraphAdmitted: true})
	wantBoost := math.Min(weights.GraphBoostPrimaryCap*output.Primary, weights.GraphBoostGraphCap)
	if math.Abs(output.GraphBoost-wantBoost) > 1e-12 {
		t.Fatalf("graph boost = %v, want %v", output.GraphBoost, wantBoost)
	}
	if output.GraphBoost > weights.GraphBoostPrimaryCap*output.Primary+1e-12 {
		t.Fatalf("graph boost %v exceeded primary-relative cap", output.GraphBoost)
	}
}

func TestGraphBoostRequiresAdmission(t *testing.T) {
	weights := DefaultWeights()
	features := FeatureVector{Semantic: pointer(1), Graph: pointer(1)}
	output := Score(weights, RankInput{Features: features, GraphAdmitted: false})
	if output.GraphBoost != 0 {
		t.Fatalf("unadmitted graph produced boost %v", output.GraphBoost)
	}
}

func TestGraphOnlyUsesSeparateAdmissionFloorAndNeverReceivesBoost(t *testing.T) {
	weights := DefaultWeights()
	below := Score(weights, RankInput{Features: FeatureVector{Graph: pointer(weights.GraphOnlyAdmissionFloor - 0.01)}, GraphOnlyCandidate: true})
	if below.GraphOnlyAdmitted || below.Final != 0 {
		t.Fatalf("below-floor graph-only admitted: %+v", below)
	}
	admitted := Score(weights, RankInput{Features: FeatureVector{Graph: pointer(0.9), Exact: pointer(1)}, GraphOnlyCandidate: true})
	if !admitted.GraphOnlyAdmitted || admitted.GraphBoost != 0 {
		t.Fatalf("graph-only candidate = %+v", admitted)
	}
	if admitted.Final <= 0 {
		t.Fatalf("admitted graph-only final = %v", admitted.Final)
	}
}

func TestProtectedPrimaryExactMatch(t *testing.T) {
	weights := DefaultWeights()
	protected := weights.ProtectedPrimary(FeatureVector{Semantic: pointer(0.8), Exact: pointer(1)})
	if !protected {
		t.Fatal("strong exact primary not protected")
	}
	if weights.ProtectedPrimary(FeatureVector{Graph: pointer(1), Exact: pointer(1)}) {
		t.Fatal("graph-only candidate must not be protected")
	}
	if weights.ProtectedPrimary(FeatureVector{Semantic: pointer(0.8), Exact: pointer(0.4)}) {
		t.Fatal("weak exact match must not be protected")
	}
}

func TestScoreIsDeterministic(t *testing.T) {
	weights := DefaultWeights()
	features := FeatureVector{Semantic: pointer(0.7), Keyword: pointer(0.4), Graph: pointer(0.5)}
	input := RankInput{Features: features, GraphAdmitted: true}
	first := Score(weights, input)
	second := Score(weights, input)
	if first != second {
		t.Fatalf("score not deterministic: %+v vs %+v", first, second)
	}
}

func TestDefaultWeightsAreVersioned(t *testing.T) {
	weights := DefaultWeights()
	if weights.Version != RankingWeightsVersion || weights.Identity() == "" {
		t.Fatalf("weights identity = %+v", weights)
	}
	changed := weights
	changed.Keyword = weights.Keyword + 0.01
	if changed.Identity() == weights.Identity() {
		t.Fatal("weight change did not change identity")
	}
}

func TestCalibrationFloorAndTopMargin(t *testing.T) {
	calibration := DefaultCalibration()
	if calibrated, admitted, reason := calibration.Calibrate("fact", 0.9, 0.9, 1); !admitted || calibrated != 0.9 || reason != "" {
		t.Fatalf("top candidate = %v/%v/%q", calibrated, admitted, reason)
	}
	if _, admitted, reason := calibration.Calibrate("fact", 0.1, 0.9, 0.2); admitted || reason != "below_absolute_floor" {
		t.Fatalf("below floor reason = %q admitted=%v", reason, admitted)
	}
	if _, admitted, reason := calibration.Calibrate("fact", 0.5, 0.9, 0.6); admitted || reason != "below_top_margin" {
		t.Fatalf("below margin reason = %q admitted=%v", reason, admitted)
	}
}

func TestCalibrationIdentityChangesWithPolicy(t *testing.T) {
	calibration := DefaultCalibration()
	changed := calibration
	changed.AbsoluteFloor += 0.01
	if calibration.Identity() == changed.Identity() {
		t.Fatal("calibration identity did not change")
	}
}

func TestSemanticCalibrationUsesKindLanguageAndQuestionType(t *testing.T) {
	calibration := DefaultCalibration()
	calibration.ByKind["event"] = 0.6
	calibration.ByLanguage["cjk"] = 0.7
	calibration.ByQuestionType[string(QuestionTemporal)] = 0.8
	if _, admitted, _ := calibration.CalibrateContext("event", "cjk", QuestionTemporal, 0.75, 0.75, 1); admitted {
		t.Fatal("context-specific semantic floor was not applied")
	}
	if _, admitted, _ := calibration.CalibrateContext("fact", "en", QuestionLookup, 0.5, 0.5, 1); !admitted {
		t.Fatal("default semantic floor was not retained")
	}
}

func TestQueryPlanIsDeterministicAndVersioned(t *testing.T) {
	first := BuildQueryPlan("When did Alice visit PROJECT-ALPHA on 2026-08-21?", PlanOptions{})
	second := BuildQueryPlan("When did Alice visit PROJECT-ALPHA on 2026-08-21?", PlanOptions{})
	if first.Identity() != second.Identity() {
		t.Fatalf("query plan identity is not deterministic")
	}
	if first.Version != QueryPlanVersion || !first.GraphPolicy.Valid() {
		t.Fatalf("query plan = %+v", first)
	}
	if len(first.Dates) == 0 || len(first.Identifiers) == 0 {
		t.Fatalf("query plan extraction = %+v", first)
	}
}

func TestGraphPolicyAmbiguityIsNotResolvedByOrdering(t *testing.T) {
	plan := BuildQueryPlan("tell me about Alice", PlanOptions{})
	if plan.QuestionType != QuestionEntity && plan.QuestionType != QuestionExploratory {
		t.Fatalf("question type = %q", plan.QuestionType)
	}
}

type staticEnricher struct {
	enrichment QueryEnrichment
	ok         bool
	err        error
	calls      int
}

func (s *staticEnricher) EnrichQuery(context.Context, string) (QueryEnrichment, bool, error) {
	s.calls++
	return s.enrichment, s.ok, s.err
}

func TestEnrichPlanIsBoundedAndFallsBackDeterministically(t *testing.T) {
	plan := BuildQueryPlan("hello world", PlanOptions{})
	enricher := &staticEnricher{ok: true, enrichment: QueryEnrichment{
		QuestionType: QuestionTemporal,
		Predicates:   []string{"p1", "p2", "p3"},
		MemoryKinds:  []string{"fact", "event", "plan"},
		Language:     "en",
	}}
	enriched := EnrichPlan(context.Background(), plan, enricher, true, 2, 1)
	if enriched.QuestionType != QuestionTemporal || len(enriched.TargetPredicates) != 2 || len(enriched.TargetMemoryKinds) != 1 {
		t.Fatalf("bounded enrichment = %+v", enriched)
	}
	// Disallowed egress must not call the enricher.
	disabled := &staticEnricher{ok: true, enrichment: QueryEnrichment{QuestionType: QuestionTemporal}}
	unchanged := EnrichPlan(context.Background(), plan, disabled, false, 2, 1)
	if disabled.calls != 0 || unchanged.Identity() != plan.Identity() {
		t.Fatalf("disallowed enrichment changed the plan or called provider")
	}
	// Provider error falls back to the local plan.
	failing := &staticEnricher{err: context.DeadlineExceeded}
	fallback := EnrichPlan(context.Background(), plan, failing, true, 2, 1)
	if fallback.Identity() != plan.Identity() {
		t.Fatalf("failed enrichment did not fall back to local plan")
	}
}
