package retrieval

import (
	"math"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

const maxTelemetryCount = 1_000_000

var telemetryPhases = map[string]struct{}{
	"keyword": {}, "semantic": {}, "graph": {}, "ranking": {},
	"context_selection": {}, "recall": {}, "total": {},
}

// Telemetry accumulates bounded, content-free retrieval measurements. It never
// accepts query text, memory prose, evidence spans, or provider payloads.
type Telemetry struct {
	phaseLatencyMS  map[string]int64
	candidateCounts map[string]int
	gateRejections  map[string]int
	graphSeeds      int
	graphEdges      int
	graphPaths      int
	graphPromotions int
	rejected        []domain.RejectedContextCandidate
}

// TelemetryRecord is the content-safe persistence contract owned by Retrieval.
type TelemetryRecord struct {
	OperationID, QueryPlanVersion, QueryPlanIdentity, QueryHash, QueryType         string
	RequestedScope, RequestedCapabilities, ExecutedCapabilities                    []string
	ReferenceTime, RankingIdentity, ContextIdentity, FeedbackIdentity              string
	CandidateBudget, ContextBudget, DuplicateClusters, ContextTokens, ContextChars int
	CandidateCounts, RejectionCounts                                               map[string]int
	ScoreSummary                                                                   map[string]ScoreSummary
	Graph                                                                          GraphTelemetry
	DegradationReasons                                                             []string
	PhaseLatencyMS                                                                 map[string]int64
	Selected                                                                       []TelemetryDecision
	Rejected                                                                       []TelemetryDecision
}

type ScoreSummary struct {
	Count int     `json:"count"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Mean  float64 `json:"mean"`
}

type GraphTelemetry struct {
	Signals, Paths, Candidates, GraphOnlyAdmitted int
	Seeds, Edges, Promotions                      int
	Families                                      map[string]int
	Score                                         ScoreSummary
}

type TelemetryDecision struct {
	MemoryID, RevisionID, State, Reason, ClusterID string
	Rank, EstimatedTokens                          int
}

func NewTelemetry() *Telemetry {
	return &Telemetry{phaseLatencyMS: map[string]int64{}, candidateCounts: map[string]int{}, gateRejections: map[string]int{}}
}

func (t *Telemetry) MarkPhase(name string, start time.Time) {
	if t == nil {
		return
	}
	if _, allowed := telemetryPhases[name]; !allowed {
		return
	}
	if elapsed := time.Since(start).Milliseconds(); elapsed >= 0 {
		t.phaseLatencyMS[name] = elapsed
	}
}

func (t *Telemetry) AddGate(reason string, count int) {
	if t == nil || reason == "" || count < 1 || len(t.gateRejections) >= 32 && t.gateRejections[reason] == 0 {
		return
	}
	t.gateRejections[reason] = boundedAdd(t.gateRejections[reason], count)
}

func (t *Telemetry) ObserveCandidates(candidates []Candidate) {
	if t == nil {
		return
	}
	for _, candidate := range candidates {
		t.candidateCounts["total"] = boundedAdd(t.candidateCounts["total"], 1)
		if candidate.KeywordRank > 0 {
			t.candidateCounts["keyword"] = boundedAdd(t.candidateCounts["keyword"], 1)
		}
		if candidate.SemanticRank > 0 && !candidate.SemanticRejected {
			t.candidateCounts["semantic"] = boundedAdd(t.candidateCounts["semantic"], 1)
		}
		if candidate.GraphRank > 0 {
			t.candidateCounts["graph"] = boundedAdd(t.candidateCounts["graph"], 1)
		}
		if candidate.SemanticRejected {
			t.AddGate("semantic_"+candidate.SemanticRejectReason, 1)
		}
		t.graphEdges = boundedAdd(t.graphEdges, len(candidate.GraphFacts))
		for _, path := range candidate.GraphPaths {
			t.graphEdges = boundedAdd(t.graphEdges, len(path.Edges))
		}
		t.graphPaths = boundedAdd(t.graphPaths, len(candidate.GraphPaths))
	}
}

func (t *Telemetry) SetGraphSeeds(count int) {
	if t != nil {
		t.graphSeeds = boundedCount(count)
	}
}

func (t *Telemetry) ObserveGraphAdmissions(candidates []Candidate) {
	if t == nil {
		return
	}
	for _, candidate := range candidates {
		if candidate.GraphOnly && candidate.RankOutput.GraphOnlyAdmitted {
			t.graphPromotions = boundedAdd(t.graphPromotions, 1)
		}
	}
}

func (t *Telemetry) SetRejected(rejected []domain.RejectedContextCandidate) {
	if t == nil {
		return
	}
	if len(rejected) > maxResults {
		rejected = rejected[:maxResults]
	}
	t.rejected = append([]domain.RejectedContextCandidate(nil), rejected...)
	for _, item := range rejected {
		t.AddGate(item.Reason, 1)
	}
}

func boundedAdd(value, add int) int {
	if add > maxTelemetryCount-value {
		return maxTelemetryCount
	}
	return value + add
}

func boundedCount(value int) int {
	if value < 0 {
		return 0
	}
	if value > maxTelemetryCount {
		return maxTelemetryCount
	}
	return value
}

func (t *Telemetry) Record(params RecallParams, response domain.SearchResponse) TelemetryRecord {
	if t == nil {
		t = NewTelemetry()
	}
	record := TelemetryRecord{
		OperationID: params.OperationID, QueryPlanVersion: params.Plan.Version, QueryPlanIdentity: params.Plan.Identity(),
		QueryHash: cursorQueryHash(params), QueryType: string(params.Plan.QuestionType), RequestedScope: copyStrings(params.Plan.RequestedScope),
		ReferenceTime: params.ReferenceTime, CandidateBudget: params.Plan.CandidateBudget, ContextBudget: params.Plan.ContextBudget,
		RequestedCapabilities: copyStrings(response.RequestedChannels), ExecutedCapabilities: copyStrings(response.ExecutedChannels),
		RankingIdentity: response.RankingIdentity, ContextIdentity: response.ContextIdentity, FeedbackIdentity: response.FeedbackIdentity,
		ContextTokens: response.Context.Accounting.TokensUsed, ContextChars: response.CharsUsed,
		CandidateCounts: copyCounts(t.candidateCounts), RejectionCounts: copyCounts(t.gateRejections),
		ScoreSummary: map[string]ScoreSummary{}, DegradationReasons: copyStrings(response.DegradationReasons),
		PhaseLatencyMS: copyLatencies(t.phaseLatencyMS),
	}
	record.CandidateCounts["selected"] = boundedCount(len(response.Context.Items))
	record.CandidateCounts["raw"] = boundedCount(len(response.Results))
	record.Graph.Seeds, record.Graph.Edges, record.Graph.Promotions = t.graphSeeds, t.graphEdges, t.graphPromotions
	record.Graph.Families = map[string]int{}
	clusters := map[string]struct{}{}
	for _, result := range response.Results {
		observeScore(record.ScoreSummary, "final", result.RankContribution.Final)
		observeScore(record.ScoreSummary, "primary", result.RankContribution.Primary)
		observeScore(record.ScoreSummary, "graph_boost", result.RankContribution.GraphBoost)
		observeScore(record.ScoreSummary, "graph_only", result.RankContribution.GraphOnly)
		observeScore(record.ScoreSummary, "penalty", result.RankContribution.Penalty)
		observeScore(record.ScoreSummary, "feedback", result.RankContribution.Feedback)
		record.Graph.Signals = boundedAdd(record.Graph.Signals, len(result.GraphSignals))
		record.Graph.Paths = boundedAdd(record.Graph.Paths, len(result.GraphPaths))
		if len(result.GraphSignals) > 0 || len(result.GraphPaths) > 0 {
			record.Graph.Candidates = boundedAdd(record.Graph.Candidates, 1)
		}
		for _, signal := range result.GraphSignals {
			record.Graph.Families[signal.Kind] = boundedAdd(record.Graph.Families[signal.Kind], 1)
		}
		if result.RankContribution.GraphOnlyAdmitted {
			record.Graph.GraphOnlyAdmitted = boundedAdd(record.Graph.GraphOnlyAdmitted, 1)
		}
		if result.GraphScore != nil {
			record.Graph.Score = updatedSummary(record.Graph.Score, *result.GraphScore)
		}
	}
	if len(response.Results) == 0 {
		observeScore(record.ScoreSummary, "final", 0)
	}
	for _, item := range response.Context.Items {
		record.Selected = append(record.Selected, TelemetryDecision{MemoryID: item.MemoryID, RevisionID: item.RevisionID, State: "selected", Reason: selectionReason(item), ClusterID: item.ClusterID, Rank: item.Rank, EstimatedTokens: item.EstimatedTokens})
		if item.ClusterID != "" {
			clusters[item.ClusterID] = struct{}{}
		}
	}
	for _, item := range t.rejected {
		record.Rejected = append(record.Rejected, TelemetryDecision{MemoryID: item.MemoryID, RevisionID: item.RevisionID, State: "rejected", Reason: item.Reason, ClusterID: item.ClusterID, Rank: item.Rank})
		if item.ClusterID != "" {
			clusters[item.ClusterID] = struct{}{}
		}
	}
	record.DuplicateClusters = len(clusters)
	return record
}

func observeScore(summaries map[string]ScoreSummary, name string, value float64) {
	summaries[name] = updatedSummary(summaries[name], value)
}

func updatedSummary(summary ScoreSummary, value float64) ScoreSummary {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return summary
	}
	if summary.Count == 0 {
		summary.Min, summary.Max = value, value
	} else {
		summary.Min = math.Min(summary.Min, value)
		summary.Max = math.Max(summary.Max, value)
	}
	summary.Mean = (summary.Mean*float64(summary.Count) + value) / float64(summary.Count+1)
	summary.Count = boundedAdd(summary.Count, 1)
	return summary
}

func copyCounts(values map[string]int) map[string]int {
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[key] = boundedCount(value)
	}
	return result
}

func copyLatencies(values map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func selectionReason(item domain.ContextItem) string {
	if item.Utility > 0 {
		return "context_utility"
	}
	return "primary_relevance"
}
