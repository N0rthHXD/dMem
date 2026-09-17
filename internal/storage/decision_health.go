package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

// DecisionIdentity is one versioned decision-stage identity exposed by health.
// It reports the effective algorithm identity, its generation, and a bounded
// state so an operator can distinguish "ready but empty" from "stale".
type DecisionIdentity struct {
	Name       string `json:"name"`
	Identity   string `json:"identity"`
	Generation int64  `json:"generation"`
	State      string `json:"state"`
}

// DecisionHealth is the bounded, non-secret health view of the consolidation,
// projection, calibration, graph, ranking, context, and feedback decision
// stages. It never contains authority content or configuration secrets.
type DecisionHealth struct {
	Consolidation       DecisionIdentity `json:"consolidation"`
	Quality             DecisionIdentity `json:"quality"`
	Duplicate           DecisionIdentity `json:"duplicate"`
	SemanticCalibration DecisionIdentity `json:"semantic_calibration"`
	Graph               DecisionIdentity `json:"graph"`
	Ranker              DecisionIdentity `json:"ranker"`
	Context             DecisionIdentity `json:"context"`
	Feedback            DecisionIdentity `json:"feedback"`
}

// DecisionHealth reports the effective decision-stage identities and the
// rebuildable projection generations.
func (r *Repository) DecisionHealth(ctx context.Context) DecisionHealth {
	weights, calibration, _ := r.retrievalCalibration()
	health := DecisionHealth{
		Consolidation:       DecisionIdentity{Name: "consolidation", Identity: r.consolidationRules().Version, State: "active"},
		SemanticCalibration: DecisionIdentity{Name: "semantic_calibration", Identity: calibration.Identity(), State: "active"},
		Graph:               DecisionIdentity{Name: "graph_scoring", Identity: retrieval.GraphScoringVersion, State: "unavailable"},
		Ranker:              DecisionIdentity{Name: "ranking", Identity: weights.Identity(), State: "active"},
		Context:             DecisionIdentity{Name: "context_selection", Identity: retrieval.ContextSelectionVersion + ":tokens=" + domain.ContextTokenFormatVersion, State: "active"},
	}
	if generation, err := r.projectionGeneration(ctx, "quality"); err == nil {
		health.Quality.Generation = generation
	}
	health.Quality.Name = "quality"
	health.Quality.Identity = qualityAlgorithmVersion
	health.Quality.State = "unavailable"
	health.Duplicate.Name = "duplicate"
	health.Duplicate.Identity = duplicateAlgorithmVersion
	health.Duplicate.State = "unavailable"
	if projection, err := r.ProjectionHealth(ctx); err == nil {
		health.Quality.State = projection.QualityState
		health.Quality.Generation = projection.QualityGeneration
		health.Quality.Identity = projection.QualityAlgorithmVersion
		health.Duplicate.State = projection.DuplicateState
		health.Duplicate.Generation = projection.DuplicateGeneration
		health.Duplicate.Identity = projection.DuplicateAlgorithmVersion
	}
	if state, version := r.GraphIndexHealth(ctx); state != "" {
		health.Graph.State = state
		health.Graph.Generation = version
	}
	if feedback, err := r.FeedbackAggregateHealth(ctx); err == nil {
		health.Feedback = DecisionIdentity{Name: "feedback_aggregation", Identity: FeedbackAggregationIdentity(), Generation: feedback.Generation, State: feedback.State}
	} else {
		health.Feedback = DecisionIdentity{Name: "feedback_aggregation", Identity: FeedbackAggregationIdentity(), State: "unavailable"}
	}
	return health
}
