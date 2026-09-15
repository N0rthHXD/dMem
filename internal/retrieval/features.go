package retrieval

import (
	"math"
	"sort"
)

// Feature names the deterministic ranking feature schema. Missing features stay
// missing (nil) rather than being assigned a misleading zero.
const (
	FeatureSemantic      = "semantic_calibrated"
	FeatureKeyword       = "keyword_calibrated"
	FeatureExact         = "exact_match"
	FeatureTemporal      = "temporal_match"
	FeatureQuality       = "memory_quality"
	FeatureAgreement     = "agreement"
	FeatureGraph         = "graph_calibrated"
	FeatureFeedback      = "query_feedback"
	FeatureDuplicateRisk = "duplicate_risk"
	FeatureOverbreadth   = "aggregate_overbreadth"
	FeatureStalePenalty  = "stale_or_contested_penalty"
)

// FeatureNames is the ordered feature schema used for version identity.
var FeatureNames = []string{
	FeatureSemantic, FeatureKeyword, FeatureExact, FeatureTemporal, FeatureQuality,
	FeatureAgreement, FeatureGraph, FeatureFeedback, FeatureDuplicateRisk,
	FeatureOverbreadth, FeatureStalePenalty,
}

// PrimaryFeatureNames are the features that can establish primary relevance.
var PrimaryFeatureNames = []string{
	FeatureSemantic, FeatureKeyword, FeatureExact, FeatureTemporal, FeatureQuality, FeatureAgreement,
}

// FeatureVector holds query-local normalized features. A nil pointer means the
// feature is missing for this candidate and contributes nothing.
type FeatureVector struct {
	Semantic      *float64
	Keyword       *float64
	Exact         *float64
	Temporal      *float64
	Quality       *float64
	Agreement     *float64
	Graph         *float64
	Feedback      *float64
	DuplicateRisk *float64
	Overbreadth   *float64
	StalePenalty  *float64
}

// Value returns a feature by name and whether it is present.
func (f FeatureVector) Value(name string) (float64, bool) {
	var pointer *float64
	switch name {
	case FeatureSemantic:
		pointer = f.Semantic
	case FeatureKeyword:
		pointer = f.Keyword
	case FeatureExact:
		pointer = f.Exact
	case FeatureTemporal:
		pointer = f.Temporal
	case FeatureQuality:
		pointer = f.Quality
	case FeatureAgreement:
		pointer = f.Agreement
	case FeatureGraph:
		pointer = f.Graph
	case FeatureFeedback:
		pointer = f.Feedback
	case FeatureDuplicateRisk:
		pointer = f.DuplicateRisk
	case FeatureOverbreadth:
		pointer = f.Overbreadth
	case FeatureStalePenalty:
		pointer = f.StalePenalty
	}
	if pointer == nil {
		return 0, false
	}
	return *pointer, true
}

// HasPrimary reports whether the candidate is established by at least one
// independently generated primary channel.
func (f FeatureVector) HasPrimary() bool {
	if f.Semantic != nil || f.Keyword != nil {
		return true
	}
	return false
}

// Clamp01 bounds a value to [0,1] and treats NaN as zero.
func Clamp01(value float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// NormalizeMinMax maps a slice of values into [0,1] deterministically. When all
// values are equal, every value maps to 1 so a uniform channel does not reorder
// candidates.
func NormalizeMinMax(values []float64) []float64 {
	result := make([]float64, len(values))
	if len(values) == 0 {
		return result
	}
	minimum, maximum := values[0], values[0]
	for _, value := range values {
		if value < minimum {
			minimum = value
		}
		if value > maximum {
			maximum = value
		}
	}
	if maximum == minimum {
		for i := range result {
			result[i] = 1
		}
		return result
	}
	for i, value := range values {
		result[i] = (value - minimum) / (maximum - minimum)
	}
	return result
}

// Percentiles returns the top-relative percentile of each value where rank 0 is
// the best value. Ties share the earliest rank, matching the deterministic sort.
func Percentiles(values []float64) []float64 {
	order := make([]int, len(values))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return values[order[i]] > values[order[j]]
	})
	result := make([]float64, len(values))
	for rank, index := range order {
		if len(values) <= 1 {
			result[index] = 1
			continue
		}
		result[index] = 1 - float64(rank)/float64(len(values)-1)
	}
	return result
}
