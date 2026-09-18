package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

// revisionKeys returns the distinct revision IDs in the candidate union.
func revisionKeys(all map[string]*searchCandidate) []string {
	keys := make([]string, 0, len(all))
	for key := range all {
		_, revisionID, _ := strings.Cut(key, "\x00")
		keys = append(keys, revisionID)
	}
	return keys
}

// graphExecutable applies fail-closed graph readiness. A stale, rebuilding,
// incompatible, or missing graph is removed from execution and reported as
// degraded rather than silently altering ranking.
func graphExecutable(metadata searchMetadata, policy retrieval.GraphPolicy) (retrieval.GraphPolicy, bool, string) {
	if policy == retrieval.GraphOff {
		return retrieval.GraphOff, false, ""
	}
	switch metadata.graphIndexState {
	case "ready":
	case "stale":
		return retrieval.GraphOff, false, "graph_stale"
	case "rebuilding":
		return retrieval.GraphOff, false, "graph_rebuilding"
	default:
		return retrieval.GraphOff, false, "graph_" + metadata.graphIndexState
	}
	if metadata.graphIndexVersion < 1 {
		return retrieval.GraphOff, false, "graph_incompatible"
	}
	if metadata.entityNormalization != "v1" {
		return retrieval.GraphOff, false, "graph_incompatible"
	}
	if metadata.entityIndexState != "ready" {
		return retrieval.GraphOff, false, "graph_entity_" + metadata.entityIndexState
	}
	if metadata.relationIndexState != "ready" {
		return retrieval.GraphOff, false, "graph_relation_" + metadata.relationIndexState
	}
	if metadata.graphSourceFactCount != metadata.activeFactCount ||
		metadata.graphSourceEntityCount != metadata.activeEntityCount ||
		metadata.graphSourceRelationCount != metadata.activeRelationCount {
		return retrieval.GraphOff, false, "graph_partial"
	}
	return policy, true, ""
}

func hydrateRankingProjections(ctx context.Context, tx *sql.Tx, candidates []*searchCandidate) error {
	for _, candidate := range candidates {
		var quality, duplicateDensity sql.NullFloat64
		var memberCount sql.NullInt64
		var aggregateKind, state sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT q.quality,q.duplicate_density,
			(SELECT dc.member_count FROM duplicate_cluster_members dcm JOIN duplicate_clusters dc ON dc.cluster_id=dcm.cluster_id WHERE dcm.memory_id=? AND dcm.revision_id=? ORDER BY dc.generation DESC,dc.cluster_id LIMIT 1),
			(SELECT aggregate_kind FROM aggregate_descriptors WHERE memory_id=?),
			(SELECT current_state FROM memories WHERE memory_id=?)
			FROM (SELECT 1) base LEFT JOIN memory_quality q ON q.memory_id=? AND q.revision_id=?`,
			candidate.memoryID, candidate.revisionID, candidate.memoryID, candidate.memoryID, candidate.memoryID, candidate.revisionID).
			Scan(&quality, &duplicateDensity, &memberCount, &aggregateKind, &state)
		if err != nil {
			return err
		}
		if quality.Valid {
			candidate.quality = floatPointer(retrieval.Clamp01(quality.Float64))
		}
		if duplicateDensity.Valid {
			candidate.duplicateRisk = floatPointer(retrieval.Clamp01(duplicateDensity.Float64))
		} else if memberCount.Valid && memberCount.Int64 > 1 {
			candidate.duplicateRisk = floatPointer(retrieval.Clamp01(float64(memberCount.Int64-1) / float64(memberCount.Int64)))
		}
		if aggregateKind.Valid {
			value := 0.5
			if aggregateKind.String == "topic" || aggregateKind.String == "timeline" {
				value = 1
			}
			candidate.overbreadth = &value
		}
		if state.String == "contested" {
			candidate.lifecyclePenalty = floatPointer(1)
		}
	}
	return nil
}

func floatPointer(value float64) *float64 { return &value }

// applyExplain attaches per-result feature contributions, graph and semantic
// calibration provenance, and temporal provenance in explain mode.
func (r *Repository) applyExplain(candidate *searchCandidate, result *domain.SearchResult, params SearchParams) {
	features := make(map[string]float64)
	for _, name := range retrieval.FeatureNames {
		if value, ok := candidate.features.Value(name); ok {
			features[name] = value
		}
	}
	result.RankingFeatures = features
	result.FeatureContributions = candidate.rankOutput.FeatureContributions
	if candidate.graphScore > 0 {
		value := candidate.graphScore
		result.GraphScore = &value
	}
	if candidate.semanticRank > 0 {
		raw := candidate.semanticRaw
		percentile := candidate.semanticPercentile
		result.SemanticRawScore = &raw
		result.SemanticPercentile = &percentile
	}
	result.SemanticRejectionReason = candidate.semanticRejectReason
	_, calibration, _ := r.retrievalCalibration()
	result.SemanticCalibrationIdentity = calibration.Identity()
	result.TemporalPrecision = candidate.timePrecision
	result.TemporalSourceWording = params.Plan.TimeExpression
	result.TemporalTimeSource = result.Memory.CurrentRevision.TimeSource
	if params.QueryTimeSet {
		start, end := params.QueryTime.Start, params.QueryTime.End
		result.QueryTimeStart, result.QueryTimeEnd = &start, &end
	}
	if reference, err := time.Parse(time.RFC3339Nano, params.ReferenceTime); err == nil {
		result.ReferenceTimeAnchor = &reference
	}
	result.RankContribution = rankContribution(candidate)
	if candidate.keywordRank > 0 {
		raw := candidate.keywordRaw
		coverage := retrieval.Coverage(candidate.ftsContent, params.planTerms())
		result.KeywordRawBM25 = &raw
		result.KeywordMatchedFields = []string{"content"}
		result.KeywordExactMatch = retrieval.ExactMatched(candidate.ftsContent, params.planTerms())
		result.KeywordTermCoverage = &coverage
	}
}

func rankContribution(candidate *searchCandidate) domain.RankContribution {
	return domain.RankContribution{
		Primary: candidate.rankOutput.Primary, GraphBoost: candidate.rankOutput.GraphBoost,
		GraphOnly: candidate.rankOutput.GraphOnly, Penalty: candidate.rankOutput.Penalty,
		Feedback: candidate.rankOutput.Feedback, Final: candidate.rankOutput.Final,
		GraphOnlyAdmitted: candidate.rankOutput.GraphOnlyAdmitted,
	}
}

// rankingIdentity is the versioned ranker identity bound into cursors.
func (r *Repository) rankingIdentity() string {
	weights, calibration, policy := r.retrievalCalibration()
	return rankingVersion + ":weights=" + weights.Identity() + ":semantic=" + calibration.Identity() +
		":graph_policy=" + string(policy) + fmt.Sprintf(":entity_floor=%g:fact_floor=%g", r.minEntityConfidence, r.minFactConfidence)
}

// planTerms returns the compiled keyword terms for the current plan.
func (p SearchParams) planTerms() []retrieval.KeywordTerm {
	if p.Query == "" {
		return nil
	}
	return retrieval.CompileKeyword(p.Query).Terms
}

// candidatePointers returns the union in deterministic key order.
func candidatePointers(all map[string]*searchCandidate) []*searchCandidate {
	keys := make([]string, 0, len(all))
	for key := range all {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]*searchCandidate, 0, len(keys))
	for _, key := range keys {
		result = append(result, all[key])
	}
	return result
}

// applyGraphChannel runs the gated graph channel, merging graph scores and
// signal families into the candidate union. Unready, incompatible, or
// stale/rebuilding graph state removes the channel and reports degradation.
func (r *Repository) applyGraphChannel(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, metadata searchMetadata, response domain.SearchResponse, executed []string, all map[string]*searchCandidate, seeds []string, primary map[string]struct{}, recallLimit int) ([]string, domain.SearchResponse, error) {
	if !hasChannel(executed, ChannelGraph) {
		return executed, response, nil
	}
	policy := params.GraphPolicy
	effective, executable, reason := graphExecutable(metadata, policy)
	if !executable {
		return executed, response, &gwretrieval.CapabilityUnavailable{Capability: ChannelGraph, Reason: reason}
	}
	resolved, err := resolveQueryEntities(ctx, tx, params.Query, spaces)
	if err != nil {
		return executed, response, err
	}
	response.ResolvedEntities = resolved
	if params.telemetry != nil {
		params.telemetry.SetGraphSeeds(len(seeds))
	}
	graphPolicy := graphRecallPolicy{
		Policy:              effective,
		MinEntityConfidence: r.minEntityConfidence,
		MinFactConfidence:   r.minFactConfidence,
		AllowNewCandidates:  effective == retrieval.GraphDirect || effective == retrieval.GraphBoundedPath,
		Predicates:          params.Plan.TargetPredicates,
		Primary:             primary,
	}
	graph, err := r.graphChannelRecall(ctx, tx, params, spaces, graphPolicy, resolved, seeds, recallLimit)
	if err != nil {
		return executed, response, err
	}
	for i := range graph.items {
		item := graph.items[i]
		key := item.memoryID + "\x00" + item.revisionID
		candidate := all[key]
		if candidate == nil {
			candidate = &searchCandidate{memoryID: item.memoryID, revisionID: item.revisionID}
			all[key] = candidate
		}
		candidate.graphScore = item.score
		candidate.graphRank = item.graphRank
		candidate.graphSignals = item.signals
		candidate.graphFacts = item.facts
		candidate.graphPaths = item.paths
	}
	response.ExecutedChannels = executed
	return executed, response, nil
}
