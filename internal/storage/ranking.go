package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
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

// planQuery builds the immutable QueryPlan, compiled keyword query, and query
// time interval for a recall request. It is deterministic and provider-free.
func (r *Repository) planQuery(ctx context.Context, params SearchParams) SearchParams {
	reference, err := time.Parse(time.RFC3339Nano, params.ReferenceTime)
	if err != nil {
		reference = r.runtime.Now()
	}
	params.Plan = retrieval.BuildQueryPlan(params.Query, retrieval.PlanOptions{
		ReferenceTime:   reference,
		Intent:          params.Intent,
		Limit:           params.Limit,
		CandidateBudget: selectionWindow(params.Limit),
		MaxChars:        params.MaxChars,
		RequestedKinds:  params.MemoryTypes,
		RequestedScope:  params.SpaceIDs,
	})
	params.Plan = retrieval.EnrichPlan(ctx, params.Plan, r.queryEnricher, r.queryEnrichmentAllowed, 8, 8)
	params.QueryTime, _, params.QueryTimeSet = retrieval.ParseQueryTime(params.Query, reference)
	return params
}

// clampGraphPolicy lowers a requested policy to the configured maximum trust.
func clampGraphPolicy(configured, requested retrieval.GraphPolicy) retrieval.GraphPolicy {
	if !configured.Valid() {
		configured = retrieval.GraphFeaturesOnly
	}
	if !requested.Valid() {
		return configured
	}
	if graphTrustRank(requested) > graphTrustRank(configured) {
		return configured
	}
	return requested
}

func graphTrustRank(policy retrieval.GraphPolicy) int {
	switch policy {
	case retrieval.GraphOff:
		return 0
	case retrieval.GraphFeaturesOnly:
		return 1
	case retrieval.GraphDirect:
		return 2
	case retrieval.GraphBoundedPath:
		return 3
	default:
		return 1
	}
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

func (r *Repository) applyGraphReadiness(params SearchParams, metadata searchMetadata, response domain.SearchResponse, executed []string) ([]string, domain.SearchResponse) {
	if !hasChannel(executed, ChannelGraph) {
		return executed, response
	}
	_, _, configured := r.retrievalCalibration()
	requested := params.Plan.GraphPolicy
	if params.GraphPolicy.Valid() {
		requested = params.GraphPolicy
	}
	_, executable, reason := graphExecutable(metadata, clampGraphPolicy(configured, requested))
	if executable {
		return executed, response
	}
	executed = channelsWithout(executed, ChannelGraph)
	response.ExecutedChannels = executed
	if reason != "" {
		response.Degraded = true
		response.DegradationReasons = append(response.DegradationReasons, reason)
	}
	return executed, response
}

// calibrateKeywordScores converts raw BM25 into query-local [0,1] values. Raw
// BM25 is lower-is-better, so it is negated before min-max normalization.
func calibrateKeywordScores(candidates []*searchCandidate) {
	present := make([]*searchCandidate, 0, len(candidates))
	values := make([]float64, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.keywordRank > 0 || candidate.keywordRaw != 0 {
			present = append(present, candidate)
			values = append(values, -candidate.keywordRaw)
		}
	}
	normalized := retrieval.NormalizeMinMax(values)
	for i, candidate := range present {
		candidate.keywordScore = normalized[i]
	}
}

// calibrateSemanticScores applies the versioned calibration and records the
// raw similarity, calibrated similarity, percentile, and rejection reason.
func calibrateSemanticScores(calibration retrieval.Calibration, candidates []*searchCandidate, question retrieval.QuestionType) {
	top := 0.0
	values := make([]float64, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.semanticRank > 0 {
			values = append(values, candidate.semanticRaw)
			if candidate.semanticRaw > top {
				top = candidate.semanticRaw
			}
		}
	}
	percentiles := retrieval.Percentiles(values)
	index := 0
	for _, candidate := range candidates {
		if candidate.semanticRank == 0 {
			continue
		}
		percentile := percentiles[index]
		index++
		candidate.semanticPercentile = percentile
		calibrated, admitted, reason := calibration.CalibrateContext(candidateKind(candidate), candidate.language, question, candidate.semanticRaw, top, percentile)
		candidate.semanticScore = calibrated
		if !admitted {
			candidate.semanticRejected = true
			candidate.semanticRejectReason = reason
		}
	}
}

func candidateKind(candidate *searchCandidate) string {
	return candidate.kind
}

// applyTemporalFilter removes candidates whose time interval is impossible or
// cannot plausibly overlap an explicit query time. It runs before the top-k cut
// and returns only the survivors.
func applyTemporalFilter(candidates []*searchCandidate, query retrieval.TimeInterval, question retrieval.QuestionType) []*searchCandidate {
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if retrieval.ImpossibleInterval(candidate.eventStart, candidate.eventEnd) ||
			retrieval.ImpossibleInterval(candidate.validFrom, candidate.validUntil) {
			candidate.semanticRejectReason = "impossible_interval"
			continue
		}
		if !query.Set {
			filtered = append(filtered, candidate)
			continue
		}
		interval, ok := (retrieval.CandidateTime{
			EventStart: candidate.eventStart, EventEnd: candidate.eventEnd,
			ValidFrom: candidate.validFrom, ValidUntil: candidate.validUntil,
			EventPrecision: candidate.timePrecision,
		}).ForQuestion(question)
		if ok {
			if _, compatible := retrieval.TemporalMatch(query, interval); !compatible {
				candidate.semanticRejectReason = "temporal_incompatible"
				continue
			}
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

// pruneRejectedCandidates removes semantic candidates rejected by calibration
// unless another independent signal (keyword or graph) establishes them.
func pruneRejectedCandidates(all map[string]*searchCandidate) {
	for key, candidate := range all {
		if candidate.semanticRejected && candidate.keywordRank == 0 && candidate.graphScore == 0 {
			delete(all, key)
		}
	}
}

// buildFeatures derives the query-local feature vector for one candidate.
func (r *Repository) buildFeatures(candidate *searchCandidate, params SearchParams, plan retrieval.QueryPlan) retrieval.FeatureVector {
	var features retrieval.FeatureVector
	if candidate.keywordRank > 0 || candidate.keywordScore > 0 {
		value := candidate.keywordScore
		features.Keyword = &value
	}
	if candidate.semanticRank > 0 && !candidate.semanticRejected {
		value := candidate.semanticScore
		features.Semantic = &value
	}
	exact := 0.5*retrieval.Coverage(candidate.ftsContent, params.planTerms()) + 0.5*boolScore(retrieval.ExactMatched(candidate.ftsContent, params.planTerms()))
	features.Exact = &exact
	agreement := 0.0
	if candidate.keywordRank > 0 {
		agreement += 0.5
	}
	if candidate.semanticRank > 0 && !candidate.semanticRejected {
		agreement += 0.5
	}
	features.Agreement = &agreement
	if candidate.quality != nil {
		quality := retrieval.Clamp01(*candidate.quality)
		features.Quality = &quality
	} else if candidate.confidence > 0 {
		quality := retrieval.Clamp01(candidate.confidence)
		features.Quality = &quality
	}
	features.DuplicateRisk = candidate.duplicateRisk
	features.Overbreadth = candidate.overbreadth
	features.StalePenalty = candidate.lifecyclePenalty
	if candidate.graphScore > 0 {
		value := candidate.graphScore
		features.Graph = &value
	}
	if params.QueryTimeSet && candidate.semanticRejectReason != "temporal_incompatible" {
		interval, ok := (retrieval.CandidateTime{
			EventStart: candidate.eventStart, EventEnd: candidate.eventEnd,
			ValidFrom: candidate.validFrom, ValidUntil: candidate.validUntil,
			EventPrecision: candidate.timePrecision,
		}).ForQuestion(plan.QuestionType)
		if ok {
			if score, compatible := retrieval.TemporalMatch(params.QueryTime, interval); compatible {
				value := score
				features.Temporal = &value
			}
		}
	}
	return features
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

func boolScore(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// rankCandidates scores the union with the versioned deterministic ranker and
// sorts it with a stable, total tie-break.
func (r *Repository) rankCandidates(candidates []*searchCandidate, params SearchParams, usageScores map[string]float64) {
	weights, _, _ := r.retrievalCalibration()
	for _, candidate := range candidates {
		features := r.buildFeatures(candidate, params, params.Plan)
		if score, ok := usageScores[candidate.revisionID]; ok {
			value := 0.001 * score
			features.Feedback = &value
		}
		candidate.features = features
		graphOnly := !features.HasPrimary() && candidate.graphScore > 0
		candidate.graphOnly = graphOnly
		admitted := candidate.graphScore >= weights.GraphCandidateFloor
		candidate.rankOutput = retrieval.Score(weights, retrieval.RankInput{
			Features: features, GraphAdmitted: admitted, GraphOnlyCandidate: graphOnly,
		})
		candidate.score = candidate.rankOutput.Final
		candidate.protected = !graphOnly && weights.ProtectedPrimary(features)
	}
	sortRecallCandidates(candidates)
}

func sortRecallCandidates(candidates []*searchCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.protected && b.graphOnly {
			return true
		}
		if b.protected && a.graphOnly {
			return false
		}
		if a.score != b.score {
			return a.score > b.score
		}
		if a.rankOutput.Primary != b.rankOutput.Primary {
			return a.rankOutput.Primary > b.rankOutput.Primary
		}
		if a.rankOutput.GraphOnly != b.rankOutput.GraphOnly {
			return a.rankOutput.GraphOnly > b.rankOutput.GraphOnly
		}
		if a.memoryID != b.memoryID {
			return a.memoryID < b.memoryID
		}
		return a.revisionID < b.revisionID
	})
}

func admittedRankedCandidates(candidates []*searchCandidate) []*searchCandidate {
	admitted := candidates[:0]
	for _, candidate := range candidates {
		if candidate.graphOnly && !candidate.rankOutput.GraphOnlyAdmitted {
			continue
		}
		admitted = append(admitted, candidate)
	}
	return admitted
}

// primarySeeds selects graph seeds from calibrated semantic top N and keyword
// top N, deduplicated to at most maxSeeds revisions. Weak candidates below the
// primary score gate never seed, and expanded candidates are never included.
func primarySeeds(candidates []*searchCandidate, weights retrieval.Weights, semanticTop, keywordTop, maxSeeds int) ([]string, map[string]struct{}) {
	type ranked struct {
		key   string
		score float64
	}
	semantic := make([]ranked, 0)
	keyword := make([]ranked, 0)
	for _, candidate := range candidates {
		key := candidate.memoryID + "\x00" + candidate.revisionID
		if candidate.semanticRank > 0 && !candidate.semanticRejected && candidate.semanticScore >= weights.SemanticSeedFloor {
			semantic = append(semantic, ranked{key, candidate.semanticScore})
		}
		if candidate.keywordRank > 0 && candidate.keywordScore >= weights.KeywordSeedFloor {
			keyword = append(keyword, ranked{key, candidate.keywordScore})
		}
	}
	sort.SliceStable(semantic, func(i, j int) bool {
		if semantic[i].score != semantic[j].score {
			return semantic[i].score > semantic[j].score
		}
		return semantic[i].key < semantic[j].key
	})
	sort.SliceStable(keyword, func(i, j int) bool {
		if keyword[i].score != keyword[j].score {
			return keyword[i].score > keyword[j].score
		}
		return keyword[i].key < keyword[j].key
	})
	seeds := make([]string, 0, maxSeeds)
	seen := map[string]struct{}{}
	appendSeed := func(list []ranked, limit int) {
		for i := 0; i < len(list) && i < limit && len(seeds) < maxSeeds; i++ {
			if _, duplicate := seen[list[i].key]; duplicate {
				continue
			}
			seen[list[i].key] = struct{}{}
			_, revisionID, _ := strings.Cut(list[i].key, "\x00")
			seeds = append(seeds, revisionID)
		}
	}
	appendSeed(semantic, semanticTop)
	appendSeed(keyword, keywordTop)
	return seeds, seen
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
func (r *Repository) applyGraphChannel(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, metadata searchMetadata, response domain.SearchResponse, executed []string, all map[string]*searchCandidate, includeSemantic bool, recallLimit int) ([]string, domain.SearchResponse, error) {
	if !hasChannel(executed, ChannelGraph) {
		return executed, response, nil
	}
	readyExecuted, readyResponse := r.applyGraphReadiness(params, metadata, response, executed)
	if !hasChannel(readyExecuted, ChannelGraph) {
		return readyExecuted, readyResponse, nil
	}
	weights, _, configured := r.retrievalCalibration()
	requested := params.Plan.GraphPolicy
	if params.GraphPolicy.Valid() {
		requested = params.GraphPolicy
	}
	policy := clampGraphPolicy(configured, requested)
	effective, executable, reason := graphExecutable(metadata, policy)
	resolved, err := resolveQueryEntities(ctx, tx, params.Query, spaces)
	if err != nil {
		return executed, response, err
	}
	response.ResolvedEntities = resolved
	if !executable {
		executed = channelsWithout(executed, ChannelGraph)
		response.ExecutedChannels = executed
		if reason != "" {
			response.Degraded = true
			response.DegradationReasons = append(response.DegradationReasons, reason)
		}
		return executed, response, nil
	}
	semanticTop := 0
	if includeSemantic {
		semanticTop = 10
	}
	seeds, primary := primarySeeds(candidatePointers(all), weights, semanticTop, 10, 15)
	if params.telemetry != nil {
		params.telemetry.graphSeeds = len(seeds)
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
	_ = weights
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
