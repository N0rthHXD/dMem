package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// recallTelemetry accumulates bounded, content-free decision telemetry for one
// recall operation. It records counts, scores, identities, degradation, and
// phase latency only; it never records query text, memory prose, evidence
// spans, or provider payloads.
type recallTelemetry struct {
	phaseLatencyMS  map[string]int64
	candidateCounts map[string]int
	gateRejections  map[string]int
	graphSeeds      int
	graphEdges      int
	graphPaths      int
	graphPromotions int
	rejected        []domain.RejectedContextCandidate
}

func newRecallTelemetry() *recallTelemetry {
	return &recallTelemetry{
		phaseLatencyMS:  map[string]int64{},
		candidateCounts: map[string]int{},
		gateRejections:  map[string]int{},
	}
}

func (t *recallTelemetry) markPhase(name string, start time.Time) {
	if t == nil || name == "" {
		return
	}
	if elapsed := time.Since(start).Milliseconds(); elapsed >= 0 {
		t.phaseLatencyMS[name] = elapsed
	}
}

func (t *recallTelemetry) addGate(reason string) {
	if t == nil || reason == "" {
		return
	}
	t.gateRejections[reason]++
}

// recordCandidateTelemetry snapshots the bounded candidate union by source and
// records calibration rejections and graph edge/path counts. It is called after
// primary recall and graph merge and before ranking.
func recordCandidateTelemetry(params SearchParams, all map[string]*searchCandidate) {
	t := params.telemetry
	if t == nil {
		return
	}
	for _, candidate := range all {
		t.candidateCounts["total"]++
		if candidate.keywordRank > 0 {
			t.candidateCounts["keyword"]++
		}
		if candidate.semanticRank > 0 && !candidate.semanticRejected {
			t.candidateCounts["semantic"]++
		}
		if candidate.graphRank > 0 {
			t.candidateCounts["graph"]++
		}
		if candidate.semanticRejected {
			t.addGate("semantic_" + candidate.semanticRejectReason)
		}
		t.graphEdges += len(candidate.graphFacts)
		for _, path := range candidate.graphPaths {
			t.graphEdges += len(path.Edges)
		}
		t.graphPaths += len(candidate.graphPaths)
	}
}

// recordGraphAdmissionTelemetry records graph-only candidates that passed the
// admission gate after ranking.
func recordGraphAdmissionTelemetry(params SearchParams, admitted []*searchCandidate) {
	t := params.telemetry
	if t == nil {
		return
	}
	for _, candidate := range admitted {
		if candidate.graphOnly && candidate.rankOutput.GraphOnlyAdmitted {
			t.graphPromotions++
		}
	}
}

func (r *Repository) recordQueryTelemetry(ctx context.Context, params SearchParams, response domain.SearchResponse) error {
	telemetry := params.telemetry
	if telemetry == nil {
		telemetry = newRecallTelemetry()
	}
	ids, err := r.newIDs(1 + len(response.Results) + len(telemetry.rejected))
	if err != nil {
		return err
	}
	requested, _ := json.Marshal(response.RequestedChannels)
	executed, _ := json.Marshal(response.ExecutedChannels)
	scope, _ := json.Marshal(params.Plan.RequestedScope)
	degraded, _ := json.Marshal(response.DegradationReasons)
	selected := response.Context.Items
	if selected == nil {
		selected = []domain.ContextItem{}
	}
	counts := map[string]int{}
	for name, value := range telemetry.candidateCounts {
		counts[name] = value
	}
	counts["selected"] = len(selected)
	counts["raw"] = len(response.Results)
	candidates, _ := json.Marshal(counts)
	rejectionCounts := map[string]int{}
	for reason, count := range telemetry.gateRejections {
		rejectionCounts[reason] += count
	}
	for _, rejected := range response.Context.Rejected {
		if _, counted := rejectionCounts[rejected.Reason]; !counted {
			rejectionCounts[rejected.Reason]++
		}
	}
	rejections, _ := json.Marshal(rejectionCounts)
	duplicateClusters := distinctContextClusters(selected, response.Context.Rejected)
	// context_tokens reports the selected context cost, not the raw ranked
	// window retained for selection. With no selected context it is zero.
	tokensUsed := response.Context.Accounting.TokensUsed
	graphSignals, graphPaths, graphCandidates := 0, 0, 0
	graphFamilies := map[string]int{}
	var graphScoreMin, graphScoreMax, graphScoreSum float64
	graphScoreCount := 0
	graphOnlyAdmitted := 0
	summaries := map[string]*scoreSummary{}
	observe := func(name string, value float64) {
		summary := summaries[name]
		if summary == nil {
			summary = &scoreSummary{}
			summaries[name] = summary
		}
		summary.observe(value)
	}
	for _, result := range response.Results {
		graphSignals += len(result.GraphSignals)
		graphPaths += len(result.GraphPaths)
		for _, signal := range result.GraphSignals {
			graphFamilies[signal.Kind]++
		}
		if len(result.GraphSignals) > 0 || len(result.GraphPaths) > 0 {
			graphCandidates++
		}
		if result.GraphScore != nil {
			score := *result.GraphScore
			if graphScoreCount == 0 || score < graphScoreMin {
				graphScoreMin = score
			}
			if graphScoreCount == 0 || score > graphScoreMax {
				graphScoreMax = score
			}
			graphScoreSum += score
			graphScoreCount++
		}
		if result.RankContribution.GraphOnlyAdmitted {
			graphOnlyAdmitted++
		}
		observe("final", result.RankContribution.Final)
		observe("primary", result.RankContribution.Primary)
		observe("graph_boost", result.RankContribution.GraphBoost)
		observe("graph_only", result.RankContribution.GraphOnly)
		observe("penalty", result.RankContribution.Penalty)
		observe("feedback", result.RankContribution.Feedback)
	}
	if _, ok := summaries["final"]; !ok {
		observe("final", 0)
	}
	scoreJSON, _ := json.Marshal(scoreSummaryMap(summaries))
	graphScoreMean := 0.0
	if graphScoreCount > 0 {
		graphScoreMean = graphScoreSum / float64(graphScoreCount)
	}
	graph, _ := json.Marshal(map[string]any{
		"signals": graphSignals, "paths": graphPaths, "candidates": graphCandidates,
		"families":            graphFamilies,
		"graph_only_admitted": graphOnlyAdmitted,
		"seeds":               telemetry.graphSeeds,
		"edges":               telemetry.graphEdges,
		"promotions":          telemetry.graphPromotions,
		"score":               map[string]any{"count": graphScoreCount, "min": graphScoreMin, "max": graphScoreMax, "mean": graphScoreMean},
	})
	latency, _ := json.Marshal(telemetry.phaseLatencyMS)

	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(r.runtime.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO query_telemetry(
		telemetry_id,operation_id,query_plan_version,query_plan_identity,query_hash,query_type,requested_scope_json,reference_time,candidate_budget,context_budget,requested_capabilities_json,executed_capabilities_json,
		candidate_counts_json,rejection_counts_json,score_summary_json,graph_counts_json,duplicate_cluster_count,
		context_tokens,context_chars,ranking_identity,context_identity,feedback_identity,degradation_reasons_json,phase_latency_ms_json,created_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?,?)`,
		ids[0], params.OperationID, params.Plan.Version, params.Plan.Identity(), searchQueryHash(params), params.Plan.QuestionType,
		string(scope), params.ReferenceTime, params.Plan.CandidateBudget, params.Plan.ContextBudget, string(requested), string(executed),
		string(candidates), string(rejections), string(scoreJSON), string(graph), duplicateClusters, tokensUsed,
		response.CharsUsed, response.RankingIdentity, response.ContextIdentity, response.FeedbackIdentity, string(degraded), string(latency), now); err != nil {
		return err
	}
	for i, item := range selected {
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_decisions(
			decision_id,operation_id,memory_id,revision_id,result_rank,selection_state,selection_reason,context_identity,estimated_tokens,created_at
		) VALUES (?,?,?,?,?,'selected',?,?,?,?)`, ids[i+1], params.OperationID, item.MemoryID,
			item.RevisionID, item.Rank, contextSelectionReason(item), response.ContextIdentity,
			item.EstimatedTokens, now); err != nil {
			return err
		}
	}
	rejectedBase := 1 + len(response.Results)
	for i, rejected := range telemetry.rejected {
		var rank any
		if rejected.Rank > 0 {
			rank = rejected.Rank
		}
		var cluster any
		if rejected.ClusterID != "" {
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM duplicate_clusters WHERE cluster_id=?`, rejected.ClusterID).Scan(&exists); err == nil {
				cluster = rejected.ClusterID
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_decisions(
			decision_id,operation_id,memory_id,revision_id,result_rank,selection_state,selection_reason,context_identity,estimated_tokens,duplicate_cluster_id,created_at
		) VALUES (?,?,?,?,?,'rejected',?,?,?,?,?)`, ids[rejectedBase+i], params.OperationID, rejected.MemoryID,
			rejected.RevisionID, rank, rejected.Reason, response.ContextIdentity, 0, cluster, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// scoreSummary accumulates a bounded score distribution summary.
type scoreSummary struct {
	Count       int     `json:"count"`
	Min         float64 `json:"min"`
	Max         float64 `json:"max"`
	Mean        float64 `json:"mean"`
	sum         float64
	initialized bool
}

func (s *scoreSummary) observe(value float64) {
	if !s.initialized {
		s.Min, s.Max, s.initialized = value, value, true
	}
	if value < s.Min {
		s.Min = value
	}
	if value > s.Max {
		s.Max = value
	}
	s.Count++
	s.sum += value
	s.Mean = s.sum / float64(s.Count)
}

func scoreSummaryMap(summaries map[string]*scoreSummary) map[string]scoreSummary {
	result := make(map[string]scoreSummary, len(summaries))
	for name, summary := range summaries {
		result[name] = *summary
	}
	return result
}

func distinctContextClusters(selected []domain.ContextItem, rejected []domain.RejectedContextCandidate) int {
	seen := map[string]struct{}{}
	for _, item := range selected {
		if item.ClusterID != "" {
			seen[item.ClusterID] = struct{}{}
		}
	}
	for _, item := range rejected {
		if item.ClusterID != "" {
			seen[item.ClusterID] = struct{}{}
		}
	}
	return len(seen)
}

func contextSelectionReason(item domain.ContextItem) string {
	if item.Utility > 0 {
		return "context_utility"
	}
	return "primary_relevance"
}
