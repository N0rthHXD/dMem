package storage

import (
	"context"
	"sort"
	"strings"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

// contextSelectionIdentity binds every input that can change which candidates
// consume budgeted context slots. Cursors compare this identity so a stored
// position cannot be reused after the effective selection contract changes.
func contextSelectionIdentity(params SearchParams) string {
	mode := "mmr"
	if params.SkipDiversification {
		mode = "fixed_cutoff"
	}
	policy := retrieval.PolicyForQuestionType(retrieval.QuestionType(params.Plan.QuestionType))
	return retrieval.ContextSelectionVersion + ":policy=" + policy.Identity() + ":tokens=" + domain.ContextTokenFormatVersion + ":mode=" + mode
}

// applyContextSelection runs the versioned context-selection stage after
// ranking. Ranking decides relevance; selection decides which representatives
// consume context slots under hard budgets. Raw results stay on the response
// for policy-safe explain/debug use only.
func (r *Repository) applyContextSelection(ctx context.Context, params SearchParams, response *domain.SearchResponse) error {
	if len(response.Results) == 0 {
		response.ContextSelectionIdentity = contextSelectionIdentity(params)
		response.Context = domain.ContextPackage{Items: []domain.ContextItem{}}
		return nil
	}
	candidates, index, aggregateInputs := r.buildContextCandidates(ctx, params, response.Results)
	budget := domain.ContextBudget{
		MaxTokens: params.MaxContextTokens, MaxChars: params.MaxChars,
		MaxResults: params.Limit, MaxSources: params.MaxSources, MaxSensitive: params.MaxSensitive,
	}
	input := retrieval.SelectInput{QuestionType: retrieval.QuestionType(params.Plan.QuestionType), Budget: budget, Candidates: candidates}
	var selection retrieval.SelectOutput
	if params.SkipDiversification {
		selection = retrieval.SelectContextFixedCutoff(input)
	} else {
		selection = retrieval.SelectContext(input)
	}
	response.ContextSelectionIdentity = contextSelectionIdentity(params)
	items := make([]domain.ContextItem, 0, len(selection.Items))
	for _, selected := range selection.Items {
		result, ok := index[selected.MemoryID+"\x00"+selected.RevisionID]
		if !ok {
			continue
		}
		items = append(items, r.selectedContextItem(selected, *result, params, aggregateInputs[result.Memory.MemoryID]))
	}
	response.Context = domain.ContextPackage{
		Items: items, TokensUsed: selection.Accounting.TokensUsed,
		CharsUsed: selection.Accounting.CharsUsed, Accounting: selection.Accounting,
		Truncated: response.Truncated,
	}
	// Recall keeps the full ranked window in response.Results; only the
	// selected context consumes the character budget.
	response.CharsUsed = response.Context.CharsUsed
	if params.telemetry != nil {
		params.telemetry.rejected = selection.Rejected
		for _, rejected := range selection.Rejected {
			params.telemetry.addGate(rejected.Reason)
		}
	}
	if params.Explain {
		response.Context.Rejected = selection.Rejected
	}
	return nil
}

func (r *Repository) buildContextCandidates(ctx context.Context, params SearchParams, results []domain.SearchResult) ([]domain.ContextCandidate, map[string]*domain.SearchResult, map[string][]string) {
	index := make(map[string]*domain.SearchResult, len(results))
	memoryIDs := make([]string, 0, len(results))
	for i := range results {
		result := &results[i]
		index[result.Memory.MemoryID+"\x00"+result.Memory.CurrentRevision.RevisionID] = result
		memoryIDs = append(memoryIDs, result.Memory.MemoryID)
	}
	clusters := r.contextClusterIDs(ctx, memoryIDs)
	aggregateSources := r.aggregateSources(ctx, memoryIDs)
	terms := params.planTerms()
	candidates := make([]domain.ContextCandidate, 0, len(results))
	for i := range results {
		result := &results[i]
		revision := result.Memory.CurrentRevision
		relevance := selectionRelevance(revision, *result, terms)
		evidence := make([]string, 0, len(revision.Evidence))
		sources := map[string]struct{}{}
		for _, item := range revision.Evidence {
			evidence = append(evidence, contextEvidenceKey(item))
			if item.EventID != "" {
				sources[item.EventID] = struct{}{}
			}
		}
		entities := make([]string, 0, len(revision.SubjectEntityIDs)+len(revision.ObjectEntityIDs))
		entities = append(entities, revision.SubjectEntityIDs...)
		entities = append(entities, revision.ObjectEntityIDs...)
		aggregate := isAggregateKind(revision.Kind)
		candidate := domain.ContextCandidate{
			MemoryID: result.Memory.MemoryID, RevisionID: revision.RevisionID, SpaceID: result.Memory.SpaceID,
			ClusterID: clusters[result.Memory.MemoryID], Kind: revision.Kind,
			DisplayContent: revision.DisplayContent, CanonicalHash: revision.CanonicalHash,
			PrimaryEvidence: evidence, SourceIDs: sortedStringSet(sources), EntityIDs: sortedUniqueStrings(entities),
			EventTimeStart: revision.EventTimeStart, EventTimeEnd: revision.EventTimeEnd,
			ValidFrom: revision.ValidFrom, ValidUntil: revision.ValidUntil,
			TimePrecision: revision.TimePrecision, TimeSource: revision.TimeSource, SourceSpeaker: revision.SourceSpeaker,
			Confidence: revision.Confidence, LifecycleState: result.Memory.CurrentState,
			Sensitivity: result.Memory.Sensitivity, Rank: result.Rank, Relevance: relevance,
			ExactPrimary: result.ProtectedPrimary, EstimatedTokens: domain.EstimateContextTokens(revision.DisplayContent),
			EstimatedChars: domain.EstimateContextTokens(revision.DisplayContent), Aggregate: aggregate,
			AggregateSources: aggregateSources[result.Memory.MemoryID],
		}
		candidates = append(candidates, candidate)
	}
	return candidates, index, aggregateSources
}

func (r *Repository) selectedContextItem(selected domain.SelectedContextItem, result domain.SearchResult, params SearchParams, aggregateInputs []string) domain.ContextItem {
	revision := result.Memory.CurrentRevision
	evidence := append([]domain.RevisionEvidence(nil), revision.Evidence...)
	if !params.IncludeEvidence {
		evidence = nil
	}
	item := domain.ContextItem{
		Rank: selected.Rank, MemoryID: selected.MemoryID, RevisionID: selected.RevisionID, ClusterID: selected.ClusterID,
		Kind: selected.Kind, DisplayContent: selected.DisplayContent,
		EventTimeStart: selected.EventTimeStart, EventTimeEnd: selected.EventTimeEnd,
		ValidFrom: selected.ValidFrom, ValidUntil: selected.ValidUntil,
		SourceSpeaker: selected.SourceSpeaker, TimePrecision: selected.TimePrecision, TimeSource: selected.TimeSource,
		Evidence: evidence, Confidence: selected.Confidence, LifecycleState: selected.LifecycleState,
		EstimatedTokens: selected.EstimatedTokens, Utility: selected.Utility,
		CorroboratingMemoryIDs: selected.CorroboratingMemoryIDs,
		SupplementaryMemoryIDs: selected.SupplementaryMemoryIDs,
		Stale:                  result.Memory.AggregateStale,
		StaleReasons:           append([]string(nil), result.Memory.AggregateStaleReasons...),
		ProvenanceMemoryIDs:    append([]string(nil), aggregateInputs...),
	}
	if explanation := graphExplanation(result.GraphPaths, result.GraphSignals); explanation != "" {
		item.GraphExplanation = explanation
	}
	if params.Explain {
		item.Explanation = &domain.RecallExplanation{
			MatchedSignals: append([]string(nil), result.MatchedChannels...), SelectionReason: result.SelectionReason,
			Features: result.RankingFeatures, FeatureContributions: result.FeatureContributions,
			ContextSelection: selected.Explanation, GraphScore: result.GraphScore,
			SemanticRawScore: result.SemanticRawScore, SemanticPercentile: result.SemanticPercentile,
			SemanticRejectionReason: result.SemanticRejectionReason, SemanticCalibrationIdentity: result.SemanticCalibrationIdentity,
			TemporalPrecision: result.TemporalPrecision, TemporalSourceWording: result.TemporalSourceWording,
			TemporalTimeSource: result.TemporalTimeSource, QueryTimeStart: result.QueryTimeStart, QueryTimeEnd: result.QueryTimeEnd,
			ReferenceTimeAnchor: result.ReferenceTimeAnchor, RankContribution: result.RankContribution,
			GraphSignals: result.GraphSignals, GraphFacts: result.GraphFacts, GraphPaths: result.GraphPaths,
			KeywordRawBM25: result.KeywordRawBM25, KeywordMatchedFields: result.KeywordMatchedFields,
			KeywordExactMatch: result.KeywordExactMatch, KeywordTermCoverage: result.KeywordTermCoverage,
		}
	}
	return item
}

func graphExplanation(paths []domain.GraphPath, signals []domain.GraphSignal) string {
	parts := make([]string, 0, len(paths)+len(signals))
	for _, path := range paths {
		edges := make([]string, 0, len(path.Edges))
		for _, edge := range path.Edges {
			edges = append(edges, edge.SubjectEntityID+" -"+edge.Predicate+"-> "+edge.ObjectEntityID)
		}
		if len(edges) > 0 {
			parts = append(parts, strings.Join(edges, " -> "))
		}
	}
	for _, signal := range signals {
		if signal.Predicate != "" {
			parts = append(parts, signal.Kind+":"+signal.Predicate)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

// selectionRelevance is the bounded relevance used for budgeted selection. It
// reflects direct query coverage and calibrated semantic or graph similarity
// rather than the fused ranker total, so a legitimate direct match is never
// discarded merely because rank-local calibration spread same-query results.
func selectionRelevance(revision domain.MemoryRevision, result domain.SearchResult, terms []retrieval.KeywordTerm) float64 {
	if result.ProtectedPrimary {
		return 1
	}
	relevance := 0.0
	if len(terms) > 0 {
		relevance = retrieval.Coverage(revision.DisplayContent, terms)
	}
	if result.SemanticScore != nil && *result.SemanticScore > relevance {
		relevance = *result.SemanticScore
	}
	if result.GraphScore != nil && *result.GraphScore > relevance {
		relevance = *result.GraphScore
	}
	return retrieval.Clamp01(relevance)
}

func isAggregateKind(kind domain.MemoryKind) bool {
	switch kind {
	case domain.MemoryKindScene, domain.MemoryKindTopic, domain.MemoryKindTimeline:
		return true
	default:
		return false
	}
}

func contextEvidenceKey(evidence domain.RevisionEvidence) string {
	key := evidence.EventID
	if evidence.StartOffset != nil && evidence.EndOffset != nil {
		key += ":" + formatInt(*evidence.StartOffset) + ":" + formatInt(*evidence.EndOffset)
	}
	return key
}

func (r *Repository) contextClusterIDs(ctx context.Context, memoryIDs []string) map[string]string {
	result := map[string]string{}
	if len(memoryIDs) == 0 {
		return result
	}
	rows, err := r.store.reader.QueryContext(ctx, `SELECT dcm.memory_id,dc.cluster_id,dc.generation FROM duplicate_cluster_members dcm JOIN duplicate_clusters dc ON dc.cluster_id=dcm.cluster_id WHERE dcm.memory_id IN (`+placeholders(len(memoryIDs))+`)`, stringArgs(memoryIDs)...)
	if err != nil {
		return result
	}
	defer rows.Close()
	generation := map[string]int64{}
	for rows.Next() {
		var memoryID, clusterID string
		var itemGeneration int64
		if rows.Scan(&memoryID, &clusterID, &itemGeneration) != nil {
			return result
		}
		if itemGeneration >= generation[memoryID] {
			generation[memoryID] = itemGeneration
			result[memoryID] = clusterID
		}
	}
	return result
}

func (r *Repository) aggregateSources(ctx context.Context, memoryIDs []string) map[string][]string {
	result := map[string][]string{}
	if len(memoryIDs) == 0 {
		return result
	}
	rows, err := r.store.reader.QueryContext(ctx, `SELECT source_memory_id,target_memory_id FROM memory_relations WHERE relation_type='derived_from' AND state='active' AND source_memory_id IN (`+placeholders(len(memoryIDs))+`) ORDER BY source_memory_id,target_memory_id`, stringArgs(memoryIDs)...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var source, target string
		if rows.Scan(&source, &target) != nil {
			return result
		}
		result[source] = append(result[source], target)
	}
	return result
}

func sortedStringSet(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	return sortedStringSet(set)
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}
