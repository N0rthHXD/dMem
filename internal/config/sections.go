package config

import "sort"

// ConsolidationConfig bounds deterministic candidate consolidation (ADR 0035).
type ConsolidationConfig struct {
	Version               string
	MaxCandidates         int
	ExactHashEnabled      bool
	SameSourceEventCosine float64
	SameSubjectKindCosine float64
}

// SemanticConfig bounds semantic calibration of primary recall.
type SemanticConfig struct {
	CalibrationVersion string
	AbsoluteFloor      float64
	TopMargin          float64
}

// GraphConfig bounds gated graph retrieval.
type GraphConfig struct {
	Policy              string
	MinEntityConfidence float64
	MinFactConfidence   float64
	BoostCap            float64
}

// RankingConfig carries deterministic feature-ranker weights.
type RankingConfig struct {
	Version         string
	SemanticWeight  float64
	KeywordWeight   float64
	ExactWeight     float64
	TemporalWeight  float64
	QualityWeight   float64
	AgreementWeight float64
}

// ContextConfig bounds budgeted context selection.
type ContextConfig struct {
	Version          string
	MaxContextTokens int
	MaxContextChars  int
	MaxResults       int
}

var graphPolicies = map[string]bool{
	"off": true, "features_only": true, "direct": true, "bounded_path": true,
}

func defaultConsolidationConfig() ConsolidationConfig {
	return ConsolidationConfig{
		Version:               "consolidation-v1",
		MaxCandidates:         32,
		ExactHashEnabled:      true,
		SameSourceEventCosine: 0.90,
		SameSubjectKindCosine: 0.94,
	}
}

func defaultSemanticConfig() SemanticConfig {
	return SemanticConfig{CalibrationVersion: "semantic-calibration-v1", AbsoluteFloor: 0.35, TopMargin: 0.08}
}

func defaultGraphConfig() GraphConfig {
	return GraphConfig{Policy: "features_only", MinEntityConfidence: 0.60, MinFactConfidence: 0.70, BoostCap: 0.08}
}

func defaultRankingConfig() RankingConfig {
	return RankingConfig{
		Version:        "ranking-weights-v1",
		SemanticWeight: 1.0, KeywordWeight: 0.35, ExactWeight: 0.15,
		TemporalWeight: 0.10, QualityWeight: 0.10, AgreementWeight: 0.05,
	}
}

func defaultContextConfig() ContextConfig {
	return ContextConfig{Version: "context-selection-v1", MaxContextTokens: 4000, MaxContextChars: 16000, MaxResults: 20}
}

func buildConsolidationConfig(t consolidationTable) (ConsolidationConfig, error) {
	c := defaultConsolidationConfig()
	if t.Version != nil {
		c.Version = *t.Version
	}
	if t.MaxCandidates != nil {
		c.MaxCandidates = *t.MaxCandidates
	}
	if t.ExactHashEnabled != nil {
		c.ExactHashEnabled = *t.ExactHashEnabled
	}
	if t.SameSourceEventCosine != nil {
		c.SameSourceEventCosine = *t.SameSourceEventCosine
	}
	if t.SameSubjectKindCosine != nil {
		c.SameSubjectKindCosine = *t.SameSubjectKindCosine
	}
	if c.Version == "" {
		return c, newValidationError("config_missing_field", Diagnostic{Code: "config_missing_field", Path: "consolidation.version"})
	}
	if c.MaxCandidates < 1 || c.MaxCandidates > 256 {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "consolidation.max_candidates"})
	}
	if c.SameSourceEventCosine < 0 || c.SameSourceEventCosine > 1 || c.SameSubjectKindCosine < 0 || c.SameSubjectKindCosine > 1 {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "consolidation_threshold_out_of_bounds", Path: "consolidation"})
	}
	if c.SameSourceEventCosine > c.SameSubjectKindCosine {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "consolidation_threshold_order", Path: "consolidation"})
	}
	return c, nil
}

func buildSemanticConfig(t semanticTable) (SemanticConfig, error) {
	c := defaultSemanticConfig()
	if t.CalibrationVersion != nil {
		c.CalibrationVersion = *t.CalibrationVersion
	}
	if t.AbsoluteFloor != nil {
		c.AbsoluteFloor = *t.AbsoluteFloor
	}
	if t.TopMargin != nil {
		c.TopMargin = *t.TopMargin
	}
	if c.CalibrationVersion == "" {
		return c, newValidationError("config_missing_field", Diagnostic{Code: "config_missing_field", Path: "semantic.calibration_version"})
	}
	if c.AbsoluteFloor < 0 || c.AbsoluteFloor > 1 {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "semantic.absolute_floor"})
	}
	if c.TopMargin < 0 || c.TopMargin > 1 {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "semantic.top_margin"})
	}
	return c, nil
}

func buildGraphConfig(t graphTable) (GraphConfig, error) {
	c := defaultGraphConfig()
	if t.Policy != nil {
		c.Policy = *t.Policy
	}
	if t.MinEntityConfidence != nil {
		c.MinEntityConfidence = *t.MinEntityConfidence
	}
	if t.MinFactConfidence != nil {
		c.MinFactConfidence = *t.MinFactConfidence
	}
	if t.BoostCap != nil {
		c.BoostCap = *t.BoostCap
	}
	if !graphPolicies[c.Policy] {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "graph_policy_invalid", Path: "graph.policy"})
	}
	if c.MinEntityConfidence < 0 || c.MinEntityConfidence > 1 || c.MinFactConfidence < 0 || c.MinFactConfidence > 1 {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "graph_gate_out_of_bounds", Path: "graph"})
	}
	if c.BoostCap <= 0 || c.BoostCap > 1 {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "graph.boost_cap"})
	}
	return c, nil
}

func buildRankingConfig(t rankingTable) (RankingConfig, error) {
	c := defaultRankingConfig()
	if t.Version != nil {
		c.Version = *t.Version
	}
	applyWeight := func(dst *float64, v *float64, name string) error {
		if v == nil {
			return nil
		}
		if *v < 0 || *v > 10 {
			return newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "ranking." + name})
		}
		*dst = *v
		return nil
	}
	for _, w := range []struct {
		dst  *float64
		val  *float64
		name string
	}{
		{&c.SemanticWeight, t.SemanticWeight, "semantic_weight"},
		{&c.KeywordWeight, t.KeywordWeight, "keyword_weight"},
		{&c.ExactWeight, t.ExactWeight, "exact_weight"},
		{&c.TemporalWeight, t.TemporalWeight, "temporal_weight"},
		{&c.QualityWeight, t.QualityWeight, "quality_weight"},
		{&c.AgreementWeight, t.AgreementWeight, "agreement_weight"},
	} {
		if err := applyWeight(w.dst, w.val, w.name); err != nil {
			return c, err
		}
	}
	if c.Version == "" {
		return c, newValidationError("config_missing_field", Diagnostic{Code: "config_missing_field", Path: "ranking.version"})
	}
	if c.SemanticWeight <= 0 {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "ranking_primary_weight_required", Path: "ranking.semantic_weight"})
	}
	return c, nil
}

func buildContextConfig(t contextTable) (ContextConfig, error) {
	c := defaultContextConfig()
	if t.Version != nil {
		c.Version = *t.Version
	}
	if t.MaxContextTokens != nil {
		c.MaxContextTokens = *t.MaxContextTokens
	}
	if t.MaxContextChars != nil {
		c.MaxContextChars = *t.MaxContextChars
	}
	if t.MaxResults != nil {
		c.MaxResults = *t.MaxResults
	}
	if c.Version == "" {
		return c, newValidationError("config_missing_field", Diagnostic{Code: "config_missing_field", Path: "context.version"})
	}
	if c.MaxContextTokens < 1 || c.MaxContextTokens > 1000000 {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "context.max_context_tokens"})
	}
	if c.MaxContextChars < c.MaxContextTokens {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "context_chars_below_tokens", Path: "context.max_context_chars"})
	}
	if c.MaxResults < 1 || c.MaxResults > 1000 {
		return c, newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "context.max_results"})
	}
	return c, nil
}

func sortedGraphPolicies() []string {
	policies := make([]string, 0, len(graphPolicies))
	for p := range graphPolicies {
		policies = append(policies, p)
	}
	sort.Strings(policies)
	return policies
}
