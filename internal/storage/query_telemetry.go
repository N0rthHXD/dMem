package storage

import (
	"context"
	"encoding/json"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func (r *Repository) recordQueryTelemetry(ctx context.Context, params SearchParams, response domain.SearchResponse) error {
	ids, err := r.newIDs(1 + len(response.Results))
	if err != nil {
		return err
	}
	requested, _ := json.Marshal(response.RequestedChannels)
	executed, _ := json.Marshal(response.ExecutedChannels)
	scope, _ := json.Marshal(params.Plan.RequestedScope)
	degraded, _ := json.Marshal(response.DegradationReasons)
	candidates, _ := json.Marshal(map[string]int{"selected": len(response.Results)})
	rejections, _ := json.Marshal(map[string]int{})
	graphSignals, graphPaths, graphCandidates := 0, 0, 0
	graphFamilies := map[string]int{}
	var graphScoreMin, graphScoreMax, graphScoreSum float64
	graphScoreCount := 0
	graphOnlyAdmitted := 0
	var finalMin, finalMax, finalSum float64
	for i, result := range response.Results {
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
		final := result.RankContribution.Final
		if i == 0 || final < finalMin {
			finalMin = final
		}
		if i == 0 || final > finalMax {
			finalMax = final
		}
		finalSum += final
	}
	mean := 0.0
	if len(response.Results) > 0 {
		mean = finalSum / float64(len(response.Results))
	}
	scores, _ := json.Marshal(map[string]any{"final": map[string]any{"count": len(response.Results), "min": finalMin, "max": finalMax, "mean": mean}})
	graphScoreMean := 0.0
	if graphScoreCount > 0 {
		graphScoreMean = graphScoreSum / float64(graphScoreCount)
	}
	graph, _ := json.Marshal(map[string]any{
		"signals": graphSignals, "paths": graphPaths, "candidates": graphCandidates,
		"families":            graphFamilies,
		"graph_only_admitted": graphOnlyAdmitted,
		"score":               map[string]any{"count": graphScoreCount, "min": graphScoreMin, "max": graphScoreMax, "mean": graphScoreMean},
	})
	latency, _ := json.Marshal(map[string]int{})

	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(r.runtime.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO query_telemetry(
		telemetry_id,operation_id,query_plan_version,query_plan_identity,query_hash,query_type,requested_scope_json,reference_time,candidate_budget,context_budget,requested_capabilities_json,executed_capabilities_json,
		candidate_counts_json,rejection_counts_json,score_summary_json,graph_counts_json,duplicate_cluster_count,
		context_tokens,context_chars,ranking_identity,context_identity,degradation_reasons_json,phase_latency_ms_json,created_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?,0,?,?,?,?,?,?,?)`,
		ids[0], params.OperationID, params.Plan.Version, params.Plan.Identity(), searchQueryHash(params), params.Plan.QuestionType,
		string(scope), params.ReferenceTime, params.Plan.CandidateBudget, params.Plan.ContextBudget, string(requested), string(executed),
		string(candidates), string(rejections), string(scores), string(graph), estimatedResultTokens(response.Results),
		response.CharsUsed, response.RankingIdentity, response.ContextIdentity, string(degraded), string(latency), now); err != nil {
		return err
	}
	for i, result := range response.Results {
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_decisions(
			decision_id,operation_id,memory_id,revision_id,result_rank,selection_state,selection_reason,context_identity,estimated_tokens,created_at
		) VALUES (?,?,?,?,?,'selected',?,?,?,?)`, ids[i+1], params.OperationID, result.Memory.MemoryID,
			result.Memory.CurrentRevision.RevisionID, result.Rank, result.SelectionReason, response.ContextIdentity,
			estimatedContextTokens(result.Memory.CurrentRevision.DisplayContent), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func estimatedResultTokens(results []domain.SearchResult) int {
	total := 0
	for _, result := range results {
		total += estimatedContextTokens(result.Memory.CurrentRevision.DisplayContent)
	}
	return total
}

func estimatedContextTokens(content string) int {
	count := 0
	for range content {
		count++
	}
	return count
}
