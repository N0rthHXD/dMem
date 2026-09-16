package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// TestRecallTelemetryCapturesDecisionPhases pins the bounded decision
// telemetry contract: query-plan type, executed capabilities, per-source
// candidate counts, score summaries, graph seed/edge/path/promotion counts,
// identities, degradation, and phase latency. The payload must stay
// content-free.
func TestRecallTelemetryCapturesDecisionPhases(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "telemetry-phases-a", "phaseword alpha content")
	createMemoryWithContent(t, repository, "telemetry-phases-b", "phaseword beta content")

	result, err := repository.SearchMemories(context.Background(), SearchParams{
		OperationID: "telemetry-phases", Query: "phaseword", SpaceIDs: []string{"space-1"},
		States: []string{"active"}, Channels: []string{"keyword"}, Explain: true,
		Limit: 5, MaxChars: 1000, MaxContextTokens: 1000,
	})
	if err != nil || len(result.Results) == 0 {
		t.Fatalf("search = %+v, %v", result, err)
	}
	row := readTelemetryRow(t, repository, "telemetry-phases")
	if row.planVersion == "" || row.planIdentity == "" || row.queryType == "" {
		t.Fatalf("query-plan telemetry missing: %+v", row)
	}
	if row.requestedCapabilities == "" || row.executedCapabilities == "" {
		t.Fatalf("capability telemetry missing: %+v", row)
	}
	counts := decodeObject(t, row.candidateCounts)
	if counts["total"].(float64) < 2 || counts["keyword"].(float64) < 2 || counts["selected"].(float64) < 1 {
		t.Fatalf("candidate counts = %v", counts)
	}
	scores := decodeObject(t, row.scoreSummary)
	final, ok := scores["final"].(map[string]any)
	if !ok || final["count"].(float64) < 1 {
		t.Fatalf("score summary = %v", scores)
	}
	if _, ok := scores["primary"]; !ok {
		t.Fatalf("score summary missing primary contributions: %v", scores)
	}
	graph := decodeObject(t, row.graphCounts)
	for _, key := range []string{"seeds", "edges", "paths", "promotions", "families", "score"} {
		if _, ok := graph[key]; !ok {
			t.Fatalf("graph counts missing %q: %v", key, graph)
		}
	}
	latency := decodeObject(t, row.phaseLatency)
	for _, phase := range []string{"query_planning", "recall", "context_selection", "total"} {
		if _, ok := latency[phase]; !ok {
			t.Fatalf("phase latency missing %q: %v", phase, latency)
		}
	}
	degradation := decodeArray(t, row.degradation)
	if degradation == nil {
		t.Fatalf("degradation telemetry not an array: %s", row.degradation)
	}
	if row.rankingIdentity == "" || row.contextIdentity == "" || row.feedbackIdentity == "" {
		t.Fatalf("identity telemetry missing: %+v", row)
	}
	for _, forbidden := range []string{"phaseword", "alpha content", "beta content"} {
		if containsString([]string{row.candidateCounts, row.scoreSummary, row.graphCounts, row.phaseLatency}, forbidden) {
			t.Fatalf("telemetry leaked content %q", forbidden)
		}
	}
}

// TestRecallTelemetryPersistsRejectedDecisions proves budget-rejected context
// candidates are recorded with a selection state and reason instead of being
// silently dropped.
func TestRecallTelemetryPersistsRejectedDecisions(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "telemetry-reject-a", "budgetword alpha")
	createMemoryWithContent(t, repository, "telemetry-reject-b", "budgetword beta")

	if _, err := repository.SearchMemories(context.Background(), SearchParams{
		OperationID: "telemetry-reject", Query: "budgetword", SpaceIDs: []string{"space-1"},
		States: []string{"active"}, Channels: []string{"keyword"}, Limit: 5,
		MaxChars: 1000, MaxContextTokens: 20,
	}); err != nil {
		t.Fatal(err)
	}
	var selected, rejected int
	if err := repository.store.reader.QueryRow(`SELECT
		coalesce(sum(selection_state='selected'),0), coalesce(sum(selection_state='rejected'),0)
		FROM context_decisions WHERE operation_id='telemetry-reject'`).Scan(&selected, &rejected); err != nil {
		t.Fatal(err)
	}
	if selected < 1 || rejected < 1 {
		t.Fatalf("context decisions selected=%d rejected=%d", selected, rejected)
	}
	row := readTelemetryRow(t, repository, "telemetry-reject")
	rejections := decodeObject(t, row.rejections)
	if len(rejections) == 0 {
		t.Fatalf("rejection counts empty: %s", row.rejections)
	}
}

func TestDegradedSemanticRecallPersistsOneCompleteTelemetryRecord(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "telemetry-fallback", "fallbackword content")
	repository.semanticSearchErr = errors.New("semantic unavailable")

	result, err := repository.SearchMemories(context.Background(), SearchParams{
		OperationID: "telemetry-fallback", Query: "fallbackword", SpaceIDs: []string{"space-1"},
		States: []string{"active"}, Channels: []string{"keyword", "semantic"},
		Limit: 5, MaxChars: 1000, MaxContextTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Degraded || !contains(result.DegradationReasons, "semantic_unavailable") {
		t.Fatalf("degradation = %t %v", result.Degraded, result.DegradationReasons)
	}
	var rows, decisions int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM query_telemetry WHERE operation_id='telemetry-fallback'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM context_decisions WHERE operation_id='telemetry-fallback'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || decisions != len(result.Context.Items)+len(result.Context.Rejected) {
		t.Fatalf("telemetry rows=%d decisions=%d context=%d rejected=%d", rows, decisions, len(result.Context.Items), len(result.Context.Rejected))
	}
	row := readTelemetryRow(t, repository, "telemetry-fallback")
	if !containsString([]string{row.degradation}, "semantic_unavailable") {
		t.Fatalf("telemetry degradation = %s", row.degradation)
	}
	latency := decodeObject(t, row.phaseLatency)
	for _, phase := range []string{"query_planning", "keyword", "ranking", "recall", "context_selection", "total"} {
		if _, ok := latency[phase]; !ok {
			t.Fatalf("phase latency missing %q: %v", phase, latency)
		}
	}
}

// TestDecisionHealthExposesStages pins the decision-stage health contract for
// consolidation, quality/duplicate projections, calibration, graph, ranker,
// context, and feedback.
func TestDecisionHealthExposesStages(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	health := repository.DecisionHealth(context.Background())
	stages := map[string]DecisionIdentity{
		"consolidation": health.Consolidation, "quality": health.Quality, "duplicate": health.Duplicate,
		"semantic_calibration": health.SemanticCalibration, "graph": health.Graph, "ranker": health.Ranker,
		"context": health.Context, "feedback": health.Feedback,
	}
	for name, stage := range stages {
		if stage.Identity == "" || stage.State == "" {
			t.Fatalf("decision health %q incomplete: %+v", name, stage)
		}
	}
}

type telemetryRow struct {
	planVersion           string
	planIdentity          string
	queryType             string
	requestedCapabilities string
	executedCapabilities  string
	candidateCounts       string
	rejections            string
	scoreSummary          string
	graphCounts           string
	duplicateClusters     int
	contextTokens         int
	contextChars          int
	rankingIdentity       string
	contextIdentity       string
	feedbackIdentity      string
	degradation           string
	phaseLatency          string
}

func readTelemetryRow(t *testing.T, repository *Repository, operationID string) telemetryRow {
	t.Helper()
	var row telemetryRow
	if err := repository.store.reader.QueryRow(`SELECT query_plan_version,query_plan_identity,query_type,
		requested_capabilities_json,executed_capabilities_json,candidate_counts_json,rejection_counts_json,
		score_summary_json,graph_counts_json,duplicate_cluster_count,context_tokens,context_chars,
		ranking_identity,context_identity,feedback_identity,degradation_reasons_json,phase_latency_ms_json
		FROM query_telemetry WHERE operation_id=?`, operationID).Scan(
		&row.planVersion, &row.planIdentity, &row.queryType, &row.requestedCapabilities,
		&row.executedCapabilities, &row.candidateCounts, &row.rejections, &row.scoreSummary,
		&row.graphCounts, &row.duplicateClusters, &row.contextTokens, &row.contextChars,
		&row.rankingIdentity, &row.contextIdentity, &row.feedbackIdentity, &row.degradation,
		&row.phaseLatency); err != nil {
		t.Fatalf("read telemetry: %v", err)
	}
	return row
}

func decodeObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("decode object %q: %v", raw, err)
	}
	return value
}

func decodeArray(t *testing.T, raw string) []any {
	t.Helper()
	var value []any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil
	}
	return value
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if len(needle) > 0 && len(value) >= len(needle) {
			for i := 0; i+len(needle) <= len(value); i++ {
				if value[i:i+len(needle)] == needle {
					return true
				}
			}
		}
	}
	return false
}
