package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Weights is the versioned deterministic feature-ranker contract. The initial
// values mirror the configured defaults; they are starting points, not fixed
// truths, and must be calibrated before promotion.
type Weights struct {
	Version                 string  `json:"version"`
	FeatureIdentity         string  `json:"feature_identity"`
	Semantic                float64 `json:"semantic"`
	Keyword                 float64 `json:"keyword"`
	Exact                   float64 `json:"exact"`
	Temporal                float64 `json:"temporal"`
	Quality                 float64 `json:"quality"`
	Agreement               float64 `json:"agreement"`
	GraphBoostPrimaryCap    float64 `json:"graph_boost_primary_cap"`
	GraphBoostGraphCap      float64 `json:"graph_boost_graph_cap"`
	GraphOnlyGraph          float64 `json:"graph_only_graph"`
	GraphOnlyExact          float64 `json:"graph_only_exact"`
	GraphOnlyTemporal       float64 `json:"graph_only_temporal"`
	GraphOnlyQuality        float64 `json:"graph_only_quality"`
	GraphCandidateFloor     float64 `json:"graph_candidate_floor"`
	GraphOnlyAdmissionFloor float64 `json:"graph_only_admission_floor"`
	ProtectedExactThreshold float64 `json:"protected_exact_threshold"`
	SemanticSeedFloor       float64 `json:"semantic_seed_floor"`
	KeywordSeedFloor        float64 `json:"keyword_seed_floor"`
}

// DefaultWeights returns the deterministic feature-ranker weights.
func DefaultWeights() Weights {
	return Weights{
		Version:                 RankingWeightsVersion,
		FeatureIdentity:         RankingFeatureVersion,
		Semantic:                1.00,
		Keyword:                 0.35,
		Exact:                   0.15,
		Temporal:                0.10,
		Quality:                 0.10,
		Agreement:               0.05,
		GraphBoostPrimaryCap:    0.10,
		GraphBoostGraphCap:      0.08,
		GraphOnlyGraph:          0.45,
		GraphOnlyExact:          0.15,
		GraphOnlyTemporal:       0.10,
		GraphOnlyQuality:        0.10,
		GraphCandidateFloor:     0.20,
		GraphOnlyAdmissionFloor: 0.30,
		ProtectedExactThreshold: 0.999,
		SemanticSeedFloor:       0.35,
		KeywordSeedFloor:        0.10,
	}
}

// Identity returns a deterministic hash of the weight contract.
func (w Weights) Identity() string {
	encoded, _ := json.Marshal(w)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// RankInput is the query-local input to the deterministic ranker.
type RankInput struct {
	Features           FeatureVector
	GraphAdmitted      bool
	GraphOnlyCandidate bool
}

// RankOutput preserves every score contribution so explain output can report
// why a candidate ranked where it did.
type RankOutput struct {
	Primary           float64 `json:"primary"`
	GraphBoost        float64 `json:"graph_boost"`
	GraphOnly         float64 `json:"graph_only"`
	Penalty           float64 `json:"penalty"`
	Feedback          float64 `json:"feedback"`
	Final             float64 `json:"final"`
	GraphOnlyAdmitted bool    `json:"graph_only_admitted"`
}

// Score applies the versioned deterministic formula. A candidate never receives
// both graph_only and graph_boost.
func Score(weights Weights, input RankInput) RankOutput {
	features := input.Features
	var output RankOutput
	output.Primary = weights.weighted(features, false)
	output.Penalty = 0.15*value(features.DuplicateRisk) +
		0.10*value(features.Overbreadth) +
		0.10*value(features.StalePenalty)
	output.Feedback = value(features.Feedback)
	if input.GraphOnlyCandidate {
		graph, ok := features.Value(FeatureGraph)
		if !ok || Clamp01(graph) < weights.GraphOnlyAdmissionFloor {
			output.Final = 0
			return output
		}
		output.GraphOnlyAdmitted = true
		output.GraphOnly = weights.GraphOnlyGraph*Clamp01(graph) +
			weights.GraphOnlyExact*value(features.Exact) +
			weights.GraphOnlyTemporal*value(features.Temporal) +
			weights.GraphOnlyQuality*value(features.Quality)
		output.Final = output.GraphOnly + output.Feedback - output.Penalty
		return output
	}
	if input.GraphAdmitted {
		if graph, ok := features.Value(FeatureGraph); ok {
			output.GraphBoost = minFloat(
				weights.GraphBoostPrimaryCap*output.Primary,
				weights.GraphBoostGraphCap*Clamp01(graph),
			)
		}
	}
	output.Final = output.Primary + output.GraphBoost + output.Feedback - output.Penalty
	if output.Final < 0 {
		output.Final = 0
	}
	return output
}

func (w Weights) weighted(features FeatureVector, graphOnly bool) float64 {
	total := 0.0
	total += w.Semantic * value(features.Semantic)
	total += w.Keyword * value(features.Keyword)
	total += w.Exact * value(features.Exact)
	total += w.Temporal * value(features.Temporal)
	total += w.Quality * value(features.Quality)
	total += w.Agreement * value(features.Agreement)
	_ = graphOnly
	return total
}

// ProtectedPrimary reports whether a candidate is a strong exact primary match
// that graph-only admission must not displace.
func (w Weights) ProtectedPrimary(features FeatureVector) bool {
	if !features.HasPrimary() {
		return false
	}
	exact, ok := features.Value(FeatureExact)
	if !ok {
		return false
	}
	return Clamp01(exact) >= w.ProtectedExactThreshold
}

func value(pointer *float64) float64 {
	if pointer == nil {
		return 0
	}
	return Clamp01(*pointer)
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
