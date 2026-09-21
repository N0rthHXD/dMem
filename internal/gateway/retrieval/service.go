package retrieval

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	coreretrieval "github.com/N0rthHXD/dMem/internal/retrieval"
)

const (
	maxResults       = 1000
	maxContextTokens = 1000000
	maxContextChars  = 4000000
	maxExplainItems  = 64
)

// Capability names used for retrieval policy decisions. They are stable
// identifiers, not wire values.
const (
	PolicyCapabilityRecall        = "recall"
	PolicyCapabilitySemanticQuery = "semantic_query"
)

// Defaults contains deployment-selected recall budgets. Retrieval applies them
// when a protocol omits the corresponding fields.
type Defaults struct {
	MaxResults       int
	MaxContextTokens int
	MaxContextChars  int
	GraphPolicy      coreretrieval.GraphPolicy
}

// ServiceImpl owns recall defaults, query planning, capability selection, and
// degradation orchestration.
type ServiceImpl struct {
	ports             Ports
	mu                sync.RWMutex
	defaults          Defaults
	enricher          coreretrieval.QueryEnricher
	enrichmentAllowed bool
}

func (s *ServiceImpl) SetQueryEnricher(enricher coreretrieval.QueryEnricher, allowed bool) {
	s.mu.Lock()
	s.enricher, s.enrichmentAllowed = enricher, allowed
	s.mu.Unlock()
}

var _ Service = (*ServiceImpl)(nil)

// NewService wires Retrieval over the coarse recall capability retained for the
// first search migration slice.
func NewService(ports Ports, defaults Defaults) *ServiceImpl {
	return &ServiceImpl{ports: ports, defaults: defaults}
}

// SetDefaults atomically replaces deployment-selected defaults after a
// configuration activation. In-flight calls continue with one complete set.
func (s *ServiceImpl) SetDefaults(defaults Defaults) {
	s.mu.Lock()
	s.defaults = defaults
	s.mu.Unlock()
}

// Recall plans one transport-free query and delegates ranked recall to the store.
func (s *ServiceImpl) Recall(ctx context.Context, call gateway.CallContext, query Query) (Result, error) {
	totalStart := time.Now()
	s.mu.RLock()
	defaults, enricher, allowed := s.defaults, s.enricher, s.enrichmentAllowed
	s.mu.RUnlock()
	prepared := applyDefaults(query, call, defaults)
	if err := validateQuery(prepared); err != nil {
		return Result{}, gateway.NewError(gateway.ErrorInvalidInput, err.Error())
	}
	if s.ports.Policy != nil {
		decision, err := s.ports.Policy.Authorize(ctx, call, PolicyRequest{Capability: PolicyCapabilityRecall, SpaceIDs: prepared.SpaceIDs})
		if err != nil {
			return Result{}, classifyRecallError(err)
		}
		if !decision.Allowed {
			return Result{}, gateway.NewError(gateway.ErrorPermissionDenied, "recall denied by policy")
		}
	}

	params := recallParams(prepared)
	params.Telemetry = NewTelemetry()
	var err error
	params, err = s.PrepareCursor(params)
	if err != nil {
		return Result{}, err
	}
	reference, err := s.ports.ReferenceTime.RecallReferenceTime(ctx, params.ReferenceTime, params.ReferenceTimeSet)
	if err != nil {
		return Result{}, classifyRecallError(err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, reference)
	if err != nil {
		return Result{}, gateway.WrapError(gateway.ErrorInternal, "invalid recall reference time", err)
	}
	params.ReferenceTime = reference
	budget := params.Limit * 10
	if budget < 100 {
		budget = 100
	}
	params.Plan = coreretrieval.BuildQueryPlan(params.Query, coreretrieval.PlanOptions{
		ReferenceTime: parsed, Intent: params.Intent, Limit: params.Limit, CandidateBudget: budget,
		MaxChars: params.MaxChars, RequestedKinds: params.MemoryTypes, RequestedScope: params.SpaceIDs,
		EntityHints: params.EntityHints,
	})
	params.Plan = coreretrieval.EnrichPlan(ctx, params.Plan, enricher, allowed, 8, 8)
	params.QueryTime, _, params.QueryTimeSet = coreretrieval.ParseQueryTime(params.Query, parsed)
	requestedGraphPolicy := params.Plan.GraphPolicy
	if params.GraphPolicy.Valid() {
		requestedGraphPolicy = params.GraphPolicy
	}
	params.GraphPolicy = clampGraphPolicy(defaults.GraphPolicy, requestedGraphPolicy)
	params.Channels = requestedCapabilities(params)
	status, err := s.ports.Capabilities.RecallCapabilities(ctx, CapabilityRequest{
		Requested: params.Channels, SpaceIDs: params.SpaceIDs, IncludeAncestors: params.IncludeAncestors,
		EmptyEligibility: len(params.States) == 0 || params.MemoryTypesSet && len(params.MemoryTypes) == 0,
		GraphPolicy:      params.GraphPolicy,
	})
	if err != nil {
		return Result{}, classifyRecallError(err)
	}
	var reasons []string
	params.EffectiveChannels, params.SemanticVersion, reasons = selectCapabilities(params.Channels, status)
	if hasCapability(params.EffectiveChannels, "semantic") && s.ports.Policy != nil {
		decision, err := s.ports.Policy.Authorize(ctx, call, PolicyRequest{Capability: PolicyCapabilitySemanticQuery, SpaceIDs: params.SpaceIDs})
		if err != nil {
			return Result{}, classifyRecallError(err)
		}
		if !decision.Allowed {
			params.EffectiveChannels = withoutCapability(params.EffectiveChannels, "semantic")
			reasons = appendReason(reasons, "semantic_policy_denied")
		}
	}
	if status.SemanticStale && hasCapability(params.EffectiveChannels, "semantic") {
		reasons = appendReason(reasons, "semantic_stale")
	}
	if len(params.EffectiveChannels) == 0 {
		return Result{}, unavailableCapabilities(reasons)
	}

	var response domain.SearchResponse
	recallStart := time.Now()
	for {
		params.DegradationReasons = copyStrings(reasons)
		response, err = s.ports.Candidates.Recall(ctx, params, s)
		var unavailable *CapabilityUnavailable
		if !errors.As(err, &unavailable) {
			break
		}
		reasons = appendReason(reasons, unavailable.Reason)
		params.EffectiveChannels = withoutCapability(params.EffectiveChannels, unavailable.Capability)
		if len(params.EffectiveChannels) == 0 {
			if unavailable.Denied {
				return Result{}, gateway.NewError(gateway.ErrorPermissionDenied, "recall capability denied by policy")
			}
			return Result{}, unavailableCapabilities(reasons)
		}
	}
	if err != nil {
		return Result{}, classifyRecallError(err)
	}
	params.Telemetry.MarkPhase("recall", recallStart)
	response.RequestedChannels = copyStrings(params.Channels)
	response.ExecutedChannels = copyStrings(params.EffectiveChannels)
	for _, reason := range response.DegradationReasons {
		reasons = appendReason(reasons, reason)
	}
	response.DegradationReasons = reasons
	response.Degraded = len(reasons) > 0
	params.Telemetry.MarkPhase("total", totalStart)
	if params.OperationID != "" {
		if err := s.ports.Telemetry.RecordRecallTelemetry(ctx, params.Telemetry.Record(params, response)); err != nil {
			return Result{}, classifyRecallError(err)
		}
	}
	return buildResult(prepared, response), nil
}

func (s *ServiceImpl) PreparePrimary(input PrimaryInput) PrimaryOutput {
	candidates := append([]Candidate(nil), input.Candidates...)
	present := make([]int, 0, len(candidates))
	values := make([]float64, 0, len(candidates))
	for i := range candidates {
		if candidates[i].KeywordRank > 0 || candidates[i].KeywordRaw != 0 {
			present = append(present, i)
			values = append(values, -candidates[i].KeywordRaw)
		}
	}
	for i, score := range coreretrieval.NormalizeMinMax(values) {
		candidates[present[i]].KeywordScore = score
	}
	top := 0.0
	values = values[:0]
	for i := range candidates {
		if candidates[i].SemanticRank > 0 {
			values = append(values, candidates[i].SemanticRaw)
			top = math.Max(top, candidates[i].SemanticRaw)
		}
	}
	percentiles := coreretrieval.Percentiles(values)
	semantic := 0
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.SemanticRank == 0 {
			continue
		}
		candidate.SemanticPercentile = percentiles[semantic]
		semantic++
		candidate.SemanticScore, _, candidate.SemanticRejectReason = input.Calibration.CalibrateContext(
			candidate.Kind, candidate.Language, input.QuestionType, candidate.SemanticRaw, top, candidate.SemanticPercentile,
		)
		candidate.SemanticRejected = candidate.SemanticRejectReason != ""
	}
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if candidate.SemanticRejected && candidate.KeywordRank == 0 && candidate.GraphScore == 0 {
			continue
		}
		filtered = append(filtered, candidate)
	}
	seeds, primary := primarySeeds(filtered, input.Weights, input.SemanticTop, input.KeywordTop, input.MaxSeeds)
	return PrimaryOutput{Candidates: filtered, Seeds: seeds, PrimaryKeys: primary}
}

func (s *ServiceImpl) Orchestrate(ctx context.Context, input PipelineInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	primary := s.PreparePrimary(input.Primary)
	candidates, err := input.ExpandGraph(primary)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rankingInput, err := input.RankingInput(candidates)
	if err != nil {
		return err
	}
	ranked := s.Rank(rankingInput)
	if err := validateCursor(input.Params.CursorState, input.Params, input.CursorBinding); err != nil {
		return err
	}
	ranked.Start, err = cursorStart(ranked.Candidates, input.Params.CursorState)
	if err != nil {
		return err
	}
	selectionInput, err := input.Materialize(ranked)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return input.Render(s.Select(selectionInput))
}

func (s *ServiceImpl) Rank(input RankInput) RankOutput {
	candidates := append([]Candidate(nil), input.Candidates...)
	filtered := candidates[:0]
	removed := 0
	for _, candidate := range candidates {
		if coreretrieval.ImpossibleInterval(candidate.EventStart, candidate.EventEnd) ||
			coreretrieval.ImpossibleInterval(candidate.ValidFrom, candidate.ValidUntil) {
			removed++
			continue
		}
		if input.Params.QueryTimeSet {
			interval, ok := (coreretrieval.CandidateTime{
				EventStart: candidate.EventStart, EventEnd: candidate.EventEnd,
				ValidFrom: candidate.ValidFrom, ValidUntil: candidate.ValidUntil,
				EventPrecision: candidate.TimePrecision,
			}).ForQuestion(input.Params.Plan.QuestionType)
			if ok {
				if _, compatible := coreretrieval.TemporalMatch(input.Params.QueryTime, interval); !compatible {
					removed++
					continue
				}
			}
		}
		candidate.Features = candidateFeatures(candidate, input)
		if score, ok := input.UsageScores[candidate.RevisionID]; ok {
			value := 0.001 * score
			candidate.Features.Feedback = &value
		}
		candidate.GraphOnly = !candidate.Features.HasPrimary() && candidate.GraphScore > 0
		candidate.RankOutput = coreretrieval.Score(input.Weights, coreretrieval.RankInput{
			Features: candidate.Features, GraphAdmitted: candidate.GraphScore >= input.Weights.GraphCandidateFloor,
			GraphOnlyCandidate: candidate.GraphOnly,
		})
		if candidate.GraphOnly && !candidate.RankOutput.GraphOnlyAdmitted {
			continue
		}
		candidate.Score = candidate.RankOutput.Final
		candidate.Protected = !candidate.GraphOnly && input.Weights.ProtectedPrimary(candidate.Features)
		filtered = append(filtered, candidate)
	}
	sort.Slice(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		if a.Protected != b.Protected && (a.GraphOnly || b.GraphOnly) {
			return a.Protected
		}
		// A canonical content match always outranks a candidate that matched only
		// the Agent Tool search-hint projection, even when the hint-only candidate
		// normalized to a higher keyword score.
		if a.HintOnly != b.HintOnly && !a.GraphOnly && !b.GraphOnly {
			return !a.HintOnly
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.RankOutput.Primary != b.RankOutput.Primary {
			return a.RankOutput.Primary > b.RankOutput.Primary
		}
		if a.RankOutput.GraphOnly != b.RankOutput.GraphOnly {
			return a.RankOutput.GraphOnly > b.RankOutput.GraphOnly
		}
		if a.MemoryID != b.MemoryID {
			return a.MemoryID < b.MemoryID
		}
		return a.RevisionID < b.RevisionID
	})
	return RankOutput{Candidates: filtered, TemporalRemoved: removed}
}

func (s *ServiceImpl) Select(input SelectionInput) SelectionOutput {
	selectionInput := coreretrieval.SelectInput{QuestionType: input.QuestionType, Budget: input.Budget, Candidates: input.Candidates}
	if input.SkipDiversification {
		return SelectionOutput{Selection: coreretrieval.SelectContextFixedCutoff(selectionInput)}
	}
	return SelectionOutput{Selection: coreretrieval.SelectContext(selectionInput)}
}

func candidateFeatures(candidate Candidate, input RankInput) coreretrieval.FeatureVector {
	var features coreretrieval.FeatureVector
	if candidate.KeywordRank > 0 || candidate.KeywordScore > 0 {
		features.Keyword = floatPointer(candidate.KeywordScore)
	}
	if candidate.SemanticRank > 0 && !candidate.SemanticRejected {
		features.Semantic = floatPointer(candidate.SemanticScore)
	}
	exact := 0.5*coreretrieval.Coverage(candidate.FTSContent, input.Terms) + 0.5*boolScore(coreretrieval.ExactMatched(candidate.FTSContent, input.Terms))
	features.Exact = &exact
	agreement := 0.0
	if candidate.KeywordRank > 0 {
		agreement += 0.5
	}
	if candidate.SemanticRank > 0 && !candidate.SemanticRejected {
		agreement += 0.5
	}
	features.Agreement = &agreement
	if candidate.Quality != nil {
		features.Quality = floatPointer(coreretrieval.Clamp01(*candidate.Quality))
	} else if candidate.Confidence > 0 {
		features.Quality = floatPointer(coreretrieval.Clamp01(candidate.Confidence))
	}
	features.DuplicateRisk, features.Overbreadth, features.StalePenalty = candidate.DuplicateRisk, candidate.Overbreadth, candidate.LifecyclePenalty
	if candidate.GraphScore > 0 {
		features.Graph = floatPointer(candidate.GraphScore)
	}
	if input.Params.QueryTimeSet {
		interval, ok := (coreretrieval.CandidateTime{
			EventStart: candidate.EventStart, EventEnd: candidate.EventEnd,
			ValidFrom: candidate.ValidFrom, ValidUntil: candidate.ValidUntil,
			EventPrecision: candidate.TimePrecision,
		}).ForQuestion(input.Params.Plan.QuestionType)
		if ok {
			if score, compatible := coreretrieval.TemporalMatch(input.Params.QueryTime, interval); compatible {
				features.Temporal = &score
			}
		}
	}
	return features
}

func primarySeeds(candidates []Candidate, weights coreretrieval.Weights, semanticTop, keywordTop, maxSeeds int) ([]string, map[string]struct{}) {
	type ranked struct {
		key   string
		score float64
	}
	semantic, keyword := []ranked{}, []ranked{}
	for _, candidate := range candidates {
		key := candidate.MemoryID + "\x00" + candidate.RevisionID
		if candidate.SemanticRank > 0 && !candidate.SemanticRejected && candidate.SemanticScore >= weights.SemanticSeedFloor {
			semantic = append(semantic, ranked{key, candidate.SemanticScore})
		}
		if candidate.KeywordRank > 0 && candidate.KeywordScore >= weights.KeywordSeedFloor {
			keyword = append(keyword, ranked{key, candidate.KeywordScore})
		}
	}
	less := func(values []ranked) {
		sort.SliceStable(values, func(i, j int) bool {
			if values[i].score != values[j].score {
				return values[i].score > values[j].score
			}
			return values[i].key < values[j].key
		})
	}
	less(semantic)
	less(keyword)
	seeds, seen := make([]string, 0, maxSeeds), map[string]struct{}{}
	for _, window := range []struct {
		values []ranked
		limit  int
	}{{semantic, semanticTop}, {keyword, keywordTop}} {
		for i := 0; i < len(window.values) && i < window.limit && len(seeds) < maxSeeds; i++ {
			if _, ok := seen[window.values[i].key]; ok {
				continue
			}
			seen[window.values[i].key] = struct{}{}
			parts := strings.SplitN(window.values[i].key, "\x00", 2)
			seeds = append(seeds, parts[1])
		}
	}
	return seeds, seen
}

func floatPointer(value float64) *float64 { return &value }

func boolScore(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func classifyRecallError(err error) error {
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return gateway.WrapError(gateway.ErrorInternal, "recall failed", err)
}

func requestedCapabilities(params RecallParams) []string {
	if len(params.Channels) > 0 {
		return copyStrings(params.Channels)
	}
	result := []string{"keyword", "semantic"}
	if params.Plan.GraphPolicy != coreretrieval.GraphOff {
		result = append(result, "graph")
	}
	return result
}

func selectCapabilities(requested []string, status CapabilityStatus) ([]string, int, []string) {
	executed := make([]string, 0, len(requested))
	reasons := []string{}
	for _, capability := range requested {
		switch capability {
		case "keyword":
			if status.Keyword {
				executed = append(executed, capability)
			} else {
				reasons = appendReason(reasons, "keyword_unavailable")
			}
		case "semantic":
			if status.Semantic {
				executed = append(executed, capability)
			} else {
				reason := status.SemanticReason
				if reason == "" {
					reason = "semantic_unavailable"
				}
				reasons = appendReason(reasons, reason)
			}
		case "graph":
			if status.Graph {
				executed = append(executed, capability)
			} else if status.GraphReason != "" {
				reasons = appendReason(reasons, status.GraphReason)
			}
		}
	}
	return executed, status.SemanticVersion, reasons
}

func unavailableCapabilities(reasons []string) error {
	for _, reason := range reasons {
		if reason == "semantic_policy_denied" || reason == "semantic_policy_changed" {
			return gateway.NewError(gateway.ErrorPermissionDenied, "recall capability denied by policy")
		}
	}
	return gateway.NewError(gateway.ErrorCapabilityUnavailable, "requested recall capabilities are unavailable")
}

func hasCapability(capabilities []string, target string) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

func withoutCapability(capabilities []string, target string) []string {
	result := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability != target {
			result = append(result, capability)
		}
	}
	return result
}

func appendReason(reasons []string, reason string) []string {
	if reason == "" || hasCapability(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}

func clampGraphPolicy(configured, requested coreretrieval.GraphPolicy) coreretrieval.GraphPolicy {
	if !configured.Valid() {
		configured = coreretrieval.GraphFeaturesOnly
	}
	if !requested.Valid() || graphTrustRank(requested) > graphTrustRank(configured) {
		return configured
	}
	return requested
}

func graphTrustRank(policy coreretrieval.GraphPolicy) int {
	switch policy {
	case coreretrieval.GraphOff:
		return 0
	case coreretrieval.GraphFeaturesOnly:
		return 1
	case coreretrieval.GraphDirect:
		return 2
	case coreretrieval.GraphBoundedPath:
		return 3
	default:
		return 1
	}
}

func applyDefaults(query Query, call gateway.CallContext, defaults Defaults) Query {
	query.Query = strings.TrimSpace(query.Query)
	if query.OperationID == "" {
		query.OperationID = call.RequestID
	}
	if !query.IncludeAncestorsSet {
		query.IncludeAncestors = true
	}
	if query.States == nil {
		query.States = []string{"active", "contested"}
	}
	if query.Intent == "" {
		query.Intent = domain.RecallIntentAuto
	}
	if !query.LimitSet && query.Limit == 0 {
		query.Limit = defaults.MaxResults
	}
	if !query.MaxContextTokensSet && query.MaxContextTokens == 0 {
		query.MaxContextTokens = defaults.MaxContextTokens
	}
	if !query.MaxCharsSet && query.MaxChars == 0 {
		query.MaxChars = defaults.MaxContextChars
	}
	if !query.ExcludeStaleAggregatesSet {
		query.ExcludeStaleAggregates = true
	}
	query.MemoryTypes = sortedUnique(query.MemoryTypes)
	query.States = sortedUnique(query.States)
	query.Channels = canonicalCapabilities(query.Channels)
	if query.ReferenceTimeSet {
		if parsed, err := time.Parse(time.RFC3339, query.ReferenceTime); err == nil {
			query.ReferenceTime = parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return query
}

func validateQuery(query Query) error {
	if query.Query == "" {
		return errors.New("query is required")
	}
	if len(query.SpaceIDs) == 0 {
		return errors.New("at least one scope is required")
	}
	seenSpaces := make(map[string]struct{}, len(query.SpaceIDs))
	for _, id := range query.SpaceIDs {
		if strings.TrimSpace(id) == "" {
			return errors.New("scope identifiers must not be empty")
		}
		if _, exists := seenSpaces[id]; exists {
			return errors.New("scope identifiers must be unique")
		}
		seenSpaces[id] = struct{}{}
	}
	if !query.Intent.Valid() {
		return errors.New("intent is invalid")
	}
	for _, kind := range query.MemoryTypes {
		if !domain.MemoryKind(kind).Valid() {
			return errors.New("memory_types is invalid")
		}
	}
	for _, state := range query.States {
		if state != "active" && state != "contested" {
			return errors.New("states is invalid")
		}
	}
	if query.ReferenceTimeSet {
		if _, err := time.Parse(time.RFC3339, query.ReferenceTime); err != nil {
			return errors.New("reference_time must be RFC3339")
		}
	}
	if query.Channels != nil && len(query.Channels) == 0 {
		return errors.New("debug capabilities must not be empty")
	}
	for _, channel := range query.Channels {
		if channel != "keyword" && channel != "semantic" && channel != "graph" {
			return errors.New("debug capabilities is invalid")
		}
	}
	if query.GraphPolicy != "" && !coreretrieval.GraphPolicy(query.GraphPolicy).Valid() {
		return errors.New("debug graph_policy is invalid")
	}
	if query.Limit < 1 || query.Limit > maxResults {
		return errors.New("max_results is out of range")
	}
	if query.MaxContextTokens < 1 || query.MaxContextTokens > maxContextTokens {
		return errors.New("max_context_tokens is out of range")
	}
	if query.MaxChars < 1 || query.MaxChars > maxContextChars {
		return errors.New("context character budget is out of range")
	}
	return nil
}

func recallParams(query Query) RecallParams {
	return RecallParams{
		OperationID: query.OperationID, Query: query.Query, SpaceIDs: copyStrings(query.SpaceIDs), IncludeAncestors: query.IncludeAncestors,
		MemoryTypes: copyStrings(query.MemoryTypes), MemoryTypesSet: query.MemoryTypesSet, States: copyStrings(query.States),
		ReferenceTime: query.ReferenceTime, ReferenceTimeSet: query.ReferenceTimeSet, EntityHints: copyStrings(query.EntityHints), Channels: copyStrings(query.Channels),
		Limit: query.Limit, MaxChars: query.MaxChars, MaxContextTokens: query.MaxContextTokens,
		IncludeEvidence: query.IncludeEvidence, ExcludeStaleAggregates: query.ExcludeStaleAggregates, Explain: query.Explain,
		Cursor: query.Cursor, Intent: string(query.Intent), GraphPolicy: coreretrieval.GraphPolicy(query.GraphPolicy), SkipDiversification: query.SkipDiversification,
	}
}

func buildResult(query Query, response domain.SearchResponse) Result {
	pkg := response.Context
	if pkg.Items == nil {
		pkg.Items = []domain.ContextItem{}
	}
	pkg.NextCursor = response.NextCursor
	pkg.Truncated = response.Truncated
	if !query.Explain {
		pkg.Rejected = nil
		for i := range pkg.Items {
			pkg.Items[i].Explanation = nil
		}
	} else {
		pkg.Rejected = boundedCopy(pkg.Rejected, maxExplainItems)
		for i := range pkg.Items {
			item := &pkg.Items[i]
			item.Evidence = boundedCopy(item.Evidence, maxExplainItems)
			if item.Explanation != nil {
				item.Explanation.GraphSignals = boundedCopy(item.Explanation.GraphSignals, maxExplainItems)
				item.Explanation.GraphFacts = boundedCopy(item.Explanation.GraphFacts, maxExplainItems)
				item.Explanation.GraphPaths = boundedCopy(item.Explanation.GraphPaths, maxExplainItems)
				item.Explanation.KeywordMatchedFields = boundedCopy(item.Explanation.KeywordMatchedFields, maxExplainItems)
			}
		}
	}
	contextIdentity := response.ContextSelectionIdentity
	if contextIdentity == "" {
		contextIdentity = response.ContextIdentity
	}
	return Result{
		Context: pkg,
		Execution: domain.RecallExecutionMetadata{
			OperationID: query.OperationID, Intent: query.Intent, ReferenceTime: response.ReferenceTime,
			RequestedCapabilities: append([]string(nil), response.RequestedChannels...), ExecutedCapabilities: append([]string(nil), response.ExecutedChannels...),
			Degraded: response.Degraded, DegradationReasons: append([]string{}, response.DegradationReasons...),
			QueryPlanIdentity: response.QueryPlanIdentity, RankingIdentity: response.RankingIdentity, ContextSelectionIdentity: contextIdentity,
			RankingVersion: response.RankingVersion, FeedbackIdentity: response.FeedbackIdentity, FeedbackAggregateVersion: response.FeedbackAggregateVersion,
			FTSIndexVersion: response.FTSIndexVersion, SemanticIndexVersion: response.SemanticIndexVersion,
			GraphIndexVersion: response.GraphIndexVersion, GraphIndexState: response.GraphIndexState,
		},
	}
}

func boundedCopy[T any](values []T, limit int) []T {
	if len(values) > limit {
		values = values[:limit]
	}
	return append([]T(nil), values...)
}

func sortedUnique(values []string) []string {
	if values == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func copyStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	copy(result, values)
	return result
}

func canonicalCapabilities(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, capability := range []string{"keyword", "semantic", "graph"} {
		if hasCapability(values, capability) {
			result = append(result, capability)
		}
	}
	return result
}
