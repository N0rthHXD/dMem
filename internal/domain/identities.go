package domain

// IdentityKind is one independently versioned algorithm contract (ADR 0035).
type IdentityKind string

const (
	IdentityProcessing          IdentityKind = "processing"
	IdentityConsolidation       IdentityKind = "consolidation"
	IdentityProjection          IdentityKind = "projection"
	IdentityEmbeddingDocument   IdentityKind = "embedding_document"
	IdentityEmbeddingQuery      IdentityKind = "embedding_query"
	IdentitySemanticCalibration IdentityKind = "semantic_calibration"
	IdentityKeywordCompiler     IdentityKind = "keyword_compiler"
	IdentityEntityNormalization IdentityKind = "entity_normalization"
	IdentityPredicateRegistry   IdentityKind = "predicate_registry"
	IdentityGraphExtraction     IdentityKind = "graph_extraction"
	IdentityGraphScoring        IdentityKind = "graph_scoring"
	IdentityQueryPlanning       IdentityKind = "query_planning"
	IdentityRankingFeatures     IdentityKind = "ranking_features"
	IdentityRankingWeights      IdentityKind = "ranking_weights"
	IdentityContextSelection    IdentityKind = "context_selection"
	IdentityFeedbackAggregation IdentityKind = "feedback_aggregation"
	IdentityMetricSchema        IdentityKind = "metric_schema"
)

func (k IdentityKind) Valid() bool {
	switch k {
	case IdentityProcessing, IdentityConsolidation, IdentityProjection, IdentityEmbeddingDocument,
		IdentityEmbeddingQuery, IdentitySemanticCalibration, IdentityKeywordCompiler,
		IdentityEntityNormalization, IdentityPredicateRegistry, IdentityGraphExtraction,
		IdentityGraphScoring, IdentityQueryPlanning, IdentityRankingFeatures, IdentityRankingWeights,
		IdentityContextSelection, IdentityFeedbackAggregation, IdentityMetricSchema:
		return true
	default:
		return false
	}
}

// GenerationScope binds an active-generation record to a pipeline stage.
type GenerationScope string

const (
	GenerationScopeMembership GenerationScope = "membership"
	GenerationScopeRanking    GenerationScope = "ranking"
	GenerationScopeGraph      GenerationScope = "graph"
	GenerationScopeContext    GenerationScope = "context"
	GenerationScopeEvaluation GenerationScope = "evaluation"
)

func (s GenerationScope) Valid() bool {
	switch s {
	case GenerationScopeMembership, GenerationScopeRanking, GenerationScopeGraph,
		GenerationScopeContext, GenerationScopeEvaluation:
		return true
	default:
		return false
	}
}

// IdentityVersion is one stored algorithm identity and its build state.
type IdentityVersion struct {
	Kind        IdentityKind `json:"identity_kind"`
	Identity    string       `json:"identity"`
	Version     string       `json:"version"`
	Generation  int64        `json:"generation"`
	State       string       `json:"state"`
	CreatedAt   string       `json:"created_at"`
	ActivatedAt *string      `json:"activated_at,omitempty"`
}

// ActiveGeneration is the identity currently bound to a pipeline stage. Cursors
// and checkpoints compare this generation before accepting a stored payload.
type ActiveGeneration struct {
	Scope        GenerationScope `json:"scope"`
	Kind         IdentityKind    `json:"identity_kind"`
	Identity     string          `json:"identity"`
	Generation   int64           `json:"generation"`
	UpdatedAt    string          `json:"updated_at"`
}
