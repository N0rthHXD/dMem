package storage

import (
	"context"
	"encoding/json"

	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
)

func (r *Repository) recordQueryTelemetry(ctx context.Context, record gwretrieval.TelemetryRecord) error {
	ids, err := r.newIDs(1 + len(record.Selected) + len(record.Rejected))
	if err != nil {
		return err
	}
	requested, _ := json.Marshal(record.RequestedCapabilities)
	executed, _ := json.Marshal(record.ExecutedCapabilities)
	scope, _ := json.Marshal(record.RequestedScope)
	degraded, _ := json.Marshal(record.DegradationReasons)
	candidates, _ := json.Marshal(record.CandidateCounts)
	rejections, _ := json.Marshal(record.RejectionCounts)
	scoreJSON, _ := json.Marshal(record.ScoreSummary)
	graph, _ := json.Marshal(map[string]any{
		"signals": record.Graph.Signals, "paths": record.Graph.Paths, "candidates": record.Graph.Candidates,
		"families": record.Graph.Families, "graph_only_admitted": record.Graph.GraphOnlyAdmitted,
		"seeds": record.Graph.Seeds, "edges": record.Graph.Edges, "promotions": record.Graph.Promotions,
		"score": record.Graph.Score,
	})
	latency, _ := json.Marshal(record.PhaseLatencyMS)

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
		ids[0], record.OperationID, record.QueryPlanVersion, record.QueryPlanIdentity, record.QueryHash, record.QueryType,
		string(scope), record.ReferenceTime, record.CandidateBudget, record.ContextBudget, string(requested), string(executed),
		string(candidates), string(rejections), string(scoreJSON), string(graph), record.DuplicateClusters, record.ContextTokens,
		record.ContextChars, record.RankingIdentity, record.ContextIdentity, record.FeedbackIdentity, string(degraded), string(latency), now); err != nil {
		return err
	}
	for i, item := range record.Selected {
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_decisions(
			decision_id,operation_id,memory_id,revision_id,result_rank,selection_state,selection_reason,context_identity,estimated_tokens,created_at
		) VALUES (?,?,?,?,?,'selected',?,?,?,?)`, ids[i+1], record.OperationID, item.MemoryID,
			item.RevisionID, item.Rank, item.Reason, record.ContextIdentity,
			item.EstimatedTokens, now); err != nil {
			return err
		}
	}
	rejectedBase := 1 + len(record.Selected)
	for i, rejected := range record.Rejected {
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
		) VALUES (?,?,?,?,?,'rejected',?,?,?,?,?)`, ids[rejectedBase+i], record.OperationID, rejected.MemoryID,
			rejected.RevisionID, rank, rejected.Reason, record.ContextIdentity, 0, cluster, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
