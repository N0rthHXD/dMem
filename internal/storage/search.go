package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/retrieval"
	"github.com/mattn/go-sqlite3"
)

// These names identify internal recall capabilities and debug overrides. Graph
// evidence is not an independent production recall channel.
const (
	ChannelKeyword  = "keyword"
	ChannelSemantic = "semantic"
	ChannelGraph    = "graph"
)

var retrievalChannelOrder = []string{ChannelKeyword, ChannelSemantic, ChannelGraph}

// minSelectionWindow is the smallest ranked window retained before context
// selection. Selection rejects candidates for duplicates, sensitivity, source
// and budget reasons, so recall keeps a window well beyond the requested limit
// and lets the selector backfill from later-ranked candidates.
const minSelectionWindow = 100

// selectionWindow returns the ranked window retained before context selection.
// MaxResults only constrains the final selected context, not the window.
func selectionWindow(limit int) int {
	if limit < 1 {
		return minSelectionWindow
	}
	if w := limit * 10; w > minSelectionWindow {
		return w
	}
	return minSelectionWindow
}

// selectionRecallLimit fetches one candidate past the retained window so the
// continuation cursor can reach the first candidate after the window.
func selectionRecallLimit(limit int) int {
	return selectionWindow(limit) + 1
}

func validChannel(value string) bool {
	switch value {
	case ChannelKeyword, ChannelSemantic, ChannelGraph:
		return true
	}
	return false
}

func canonicalChannels(channels []string) []string {
	seen := make(map[string]struct{}, len(channels))
	normalized := make([]string, 0, len(retrievalChannelOrder))
	for _, channel := range retrievalChannelOrder {
		for _, value := range channels {
			if value == channel {
				if _, duplicate := seen[channel]; !duplicate {
					seen[channel] = struct{}{}
					normalized = append(normalized, channel)
				}
				break
			}
		}
	}
	return normalized
}

func hasChannel(channels []string, channel string) bool {
	for _, value := range channels {
		if value == channel {
			return true
		}
	}
	return false
}

func channelsWithout(channels []string, channel string) []string {
	next := make([]string, 0, len(channels))
	for _, value := range channels {
		if value != channel {
			next = append(next, value)
		}
	}
	return next
}

type SearchParams struct {
	OperationID            string
	Query                  string
	SpaceIDs               []string
	IncludeAncestors       bool
	MemoryTypes            []string
	MemoryTypesSet         bool
	States                 []string
	ReferenceTime          string
	ReferenceTimeSet       bool
	Channels               []string
	EffectiveChannels      []string
	DegradationReasons     []string
	SemanticVersion        int
	Limit                  int
	MaxChars               int
	MaxContextTokens       int
	MaxSources             int
	MaxSensitive           int
	IncludeEvidence        bool
	ExcludeStaleAggregates bool
	Explain                bool
	Cursor                 string
	CursorState            *gwretrieval.CursorState
	Intent                 string
	GraphPolicy            retrieval.GraphPolicy
	Plan                   retrieval.QueryPlan
	QueryTime              retrieval.TimeInterval
	QueryTimeSet           bool
	SkipDiversification    bool
	telemetry              *gwretrieval.Telemetry
	processor              gwretrieval.RecallProcessor
}

// searchIdentities binds a cursor to every algorithm identity that can change
// candidate membership, rank, or rendered context. The decoder accepts only the
// current version and no legacy channel payload.
type searchIdentities struct {
	Schema     string
	QueryPlan  string
	Membership string
	Ranking    string
	Graph      string
	Context    string
	Feedback   string
}

const (
	searchQueryPlanIdentity = retrieval.QueryPlanVersion
)

// rankingVersion is the versioned deterministic feature-rank identity.
const rankingVersion = retrieval.RankingWeightsVersion

func currentSearchIdentities(ftsVersion, semanticVersion int, metadata searchMetadata, rankingIdentity, contextIdentity string) searchIdentities {
	return searchIdentities{
		Schema:     "schema-v" + strconv.Itoa(SchemaVersion),
		QueryPlan:  searchQueryPlanIdentity,
		Membership: membershipIdentity(ftsVersion, semanticVersion, metadata),
		Ranking:    rankingIdentity,
		Graph:      graphIdentity(metadata),
		Context:    contextIdentity,
		Feedback:   "feedback-v" + strconv.FormatInt(metadata.feedbackAggregateVersion, 10) + ":" + FeedbackAggregationIdentity(),
	}
}

func membershipIdentity(ftsVersion, semanticVersion int, metadata searchMetadata) string {
	return "membership-v1" +
		":fts=" + strconv.Itoa(ftsVersion) +
		":semantic=" + strconv.Itoa(semanticVersion) +
		":corpus=" + strconv.FormatInt(metadata.corpusGeneration, 10) +
		":entity=" + strconv.FormatInt(metadata.entityIndexVersion, 10) +
		":entity_generation=" + strconv.FormatInt(metadata.entityIndexGeneration, 10) +
		":entity_state=" + metadata.entityIndexState +
		":entity_normalization=" + metadata.entityNormalization +
		":relation=" + strconv.FormatInt(metadata.relationIndexVersion, 10) +
		":relation_generation=" + strconv.FormatInt(metadata.relationIndexGeneration, 10) +
		":relation_state=" + metadata.relationIndexState +
		":keyword=" + retrieval.KeywordCompilerVersion +
		":hints=" + canonicalSearchHintFormatVersion +
		":temporal=" + retrieval.TemporalFeatureVersion
}

func graphIdentity(metadata searchMetadata) string {
	return retrieval.GraphScoringVersion +
		":version=" + strconv.FormatInt(metadata.graphIndexVersion, 10) +
		":generation=" + strconv.FormatInt(metadata.graphIndexGeneration, 10) +
		":state=" + metadata.graphIndexState +
		":facts=" + strconv.FormatInt(metadata.graphSourceFactCount, 10) +
		":entities=" + strconv.FormatInt(metadata.graphSourceEntityCount, 10) +
		":relations=" + strconv.FormatInt(metadata.graphSourceRelationCount, 10)
}

type searchCandidate struct {
	memoryID, revisionID, updatedAt string
	contentHash                     string
	ftsContent, hintsContent        string
	projectionContent               string
	keywordRaw, keywordScore        float64
	semanticRaw, semanticScore      float64
	semanticPercentile              float64
	semanticRejected                bool
	semanticRejectReason            string
	graphScore                      float64
	score                           float64
	keywordRank, semanticRank       int
	graphRank                       int
	contentMatched, hintMatched     bool
	hintOnly                        bool
	graphSignals                    []domain.GraphSignal
	graphFacts                      []domain.EntityFact
	graphPaths                      []domain.GraphPath
	confidence                      float64
	specificity                     float64
	eventStart, eventEnd            *time.Time
	validFrom, validUntil           *time.Time
	timePrecision, timeSource       string
	sourceSpeaker                   string
	language, kind                  string
	features                        retrieval.FeatureVector
	rankOutput                      retrieval.RankOutput
	graphOnly                       bool
	protected                       bool
	quality, duplicateRisk          *float64
	overbreadth, lifecyclePenalty   *float64
}

func (r *Repository) executeRecall(ctx context.Context, params SearchParams) (domain.SearchResponse, error) {
	params.Channels = canonicalChannels(params.Channels)
	params.EffectiveChannels = canonicalChannels(params.EffectiveChannels)
	if params.ReferenceTime == "" {
		params.ReferenceTime = formatTime(r.runtime.Now())
	}
	if params.telemetry == nil {
		params.telemetry = gwretrieval.NewTelemetry()
	}
	if len(params.EffectiveChannels) == 0 {
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	var response domain.SearchResponse
	var err error
	if hasChannel(params.EffectiveChannels, ChannelSemantic) {
		response, err = r.searchSemanticHybrid(ctx, params)
	} else {
		response, err = r.searchKeywordGraph(ctx, params)
	}
	response.ReferenceTime = params.ReferenceTime
	for _, reason := range params.DegradationReasons {
		if !contains(response.DegradationReasons, reason) {
			response.DegradationReasons = append(response.DegradationReasons, reason)
		}
	}
	response.Degraded = len(response.DegradationReasons) > 0
	return response, err
}

// searchKeywordGraph executes the keyword and/or graph channels without the
// semantic pipeline. It also serves the degraded hybrid fallback path.
func (r *Repository) searchKeywordGraph(ctx context.Context, params SearchParams) (domain.SearchResponse, error) {
	if params.ReferenceTime == "" {
		params.ReferenceTime = formatTime(r.runtime.Now())
	}
	executed := params.EffectiveChannels
	tx, err := r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.SearchResponse{}, err
	}
	defer tx.Rollback()

	var indexState string
	var ftsVersion int
	if err := tx.QueryRowContext(ctx, `SELECT state, index_version FROM index_metadata WHERE index_name='memory_fts'`).Scan(&indexState, &ftsVersion); err != nil {
		return domain.SearchResponse{}, err
	}
	metadata, err := readSearchMetadata(ctx, tx)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	spaces, err := searchScope(ctx, tx, params.SpaceIDs, params.IncludeAncestors)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	hash := searchQueryHash(params)
	identities := currentSearchIdentities(ftsVersion, params.SemanticVersion, metadata, r.rankingIdentity(), contextSelectionIdentity(params))
	response := domain.SearchResponse{Results: []domain.SearchResult{}, RequestedChannels: params.Channels, ExecutedChannels: executed, DegradationReasons: []string{}, FTSIndexVersion: ftsVersion, SemanticIndexVersion: params.SemanticVersion, RankingVersion: rankingVersion, QueryPlanIdentity: params.Plan.Identity(), RankingIdentity: identities.Ranking, ContextIdentity: identities.Context, CorpusGeneration: metadata.corpusGeneration, FeedbackAggregateVersion: metadata.feedbackAggregateVersion, FeedbackIdentity: identities.Feedback, GraphIndexVersion: metadata.graphIndexVersion, GraphIndexState: metadata.graphIndexState}
	if hasChannel(executed, ChannelKeyword) && indexState != "ready" {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelKeyword, Reason: "keyword_unavailable"}
	}
	if len(params.States) == 0 || params.MemoryTypesSet && len(params.MemoryTypes) == 0 {
		if hasChannel(executed, ChannelGraph) {
			if _, executable, reason := graphExecutable(metadata, params.GraphPolicy); !executable {
				return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelGraph, Reason: reason}
			}
		}
		response.ContextSelectionIdentity = contextSelectionIdentity(params)
		response.Context = domain.ContextPackage{Items: []domain.ContextItem{}}
		if err := tx.Commit(); err != nil {
			return domain.SearchResponse{}, err
		}
		return response, nil
	}

	all := map[string]*searchCandidate{}
	keywordStart := time.Now()
	if hasChannel(executed, ChannelKeyword) {
		candidates, keywordErr := r.keywordRecall(ctx, tx, params, spaces, selectionRecallLimit(params.Limit))
		if errors.Is(keywordErr, ErrInvalidSearchQuery) {
			return domain.SearchResponse{}, keywordErr
		}
		if keywordErr != nil {
			var sqliteErr sqlite3.Error
			if errors.As(keywordErr, &sqliteErr) && sqliteErr.Code == sqlite3.ErrError {
				r.markFTSDegraded()
				return domain.SearchResponse{}, ErrIndexDegraded
			}
			return domain.SearchResponse{}, keywordErr
		}
		for i := range candidates {
			item := candidates[i]
			item.keywordRank = i + 1
			copy := item
			all[item.memoryID+"\x00"+item.revisionID] = &copy
		}
	}
	weights, calibration, _ := r.retrievalCalibration()
	params.telemetry.MarkPhase("keyword", keywordStart)
	var responseSelectionState contextSelectionState
	var contextSelectionStart time.Time
	err = params.processor.Orchestrate(ctx, gwretrieval.PipelineInput{
		Params: gatewayRecallParams(params), CursorBinding: gatewayCursorBinding(identities),
		Primary: gwretrieval.PrimaryInput{
			Candidates: toGatewayCandidates(candidatePointers(all)), Calibration: calibration, Weights: weights,
			QuestionType: params.Plan.QuestionType, KeywordTop: 10, MaxSeeds: 15,
		},
		ExpandGraph: func(prepared gwretrieval.PrimaryOutput) ([]gwretrieval.Candidate, error) {
			graphStart := time.Now()
			all = candidateMap(fromGatewayCandidates(prepared.Candidates))
			executed, response, err = r.applyGraphChannel(ctx, tx, params, spaces, metadata, response, executed, all, prepared.Seeds, prepared.PrimaryKeys, selectionRecallLimit(params.Limit))
			params.telemetry.MarkPhase("graph", graphStart)
			if err == nil && len(executed) == 0 {
				err = ErrIndexDegraded
			}
			return toGatewayCandidates(candidatePointers(all)), err
		},
		RankingInput: func(candidates []gwretrieval.Candidate) (gwretrieval.RankInput, error) {
			list := fromGatewayCandidates(candidates)
			if err := hydrateRankingProjections(ctx, tx, list); err != nil {
				return gwretrieval.RankInput{}, err
			}
			all = candidateMap(list)
			params.telemetry.ObserveCandidates(toGatewayCandidates(candidatePointers(all)))
			usageScores, err := r.FeedbackUsageScores(ctx, tx, revisionKeys(all), metadata.feedbackAggregateVersion, searchQueryHash(params))
			return gwretrieval.RankInput{Candidates: toGatewayCandidates(list), Params: gatewayRecallParams(params), Weights: weights, UsageScores: usageScores, Terms: params.planTerms()}, err
		},
		Materialize: func(ranked gwretrieval.RankOutput) (gwretrieval.SelectionInput, error) {
			rankStart := time.Now()
			for i := 0; i < ranked.TemporalRemoved; i++ {
				params.telemetry.AddGate("temporal_filter", 1)
			}
			list := fromGatewayCandidates(ranked.Candidates)
			params.telemetry.MarkPhase("ranking", rankStart)
			params.telemetry.ObserveGraphAdmissions(toGatewayCandidates(list))
			if err := r.finalizeRankedCandidates(ctx, tx, params, spaces, &response, list, ranked.Start, identities, hash); err != nil {
				return gwretrieval.SelectionInput{}, err
			}
			contextSelectionStart = time.Now()
			input, state := r.prepareContextSelection(ctx, tx, params, &response)
			responseSelectionState = state
			return input, nil
		},
		Render: func(output gwretrieval.SelectionOutput) error {
			r.renderContextSelection(params, &response, output, responseSelectionState)
			params.telemetry.MarkPhase("context_selection", contextSelectionStart)
			return nil
		},
	})
	if err != nil {
		return domain.SearchResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SearchResponse{}, err
	}
	return response, nil
}

// searchCandidateCurrent re-verifies a recalled candidate against the
// authoritative current revision. Keyword candidates carry the canonical
// document retained at recall time; semantic candidates carry the projection
// content hash and must still match the live searchable projection.
func searchCandidateCurrent(ctx context.Context, db rowQuerier, memory domain.Memory, candidate searchCandidate, spaces []string, params SearchParams) (bool, error) {
	if memory.CurrentRevision.RevisionID != candidate.revisionID {
		return false, nil
	}
	if candidate.ftsContent != "" {
		if memory.CurrentRevision.DisplayContent != candidate.projectionContent {
			return false, nil
		}
		if canonicalStructuredDocument(memory.CurrentRevision) != candidate.ftsContent {
			return false, nil
		}
	}
	if candidate.semanticRank > 0 {
		var projectionHash string
		if err := db.QueryRowContext(ctx, `SELECT content_hash FROM memory_projections WHERE memory_id=? AND revision_id=? AND searchable=1`, candidate.memoryID, candidate.revisionID).Scan(&projectionHash); err != nil {
			return false, nil
		}
		if projectionHash != candidate.contentHash {
			return false, nil
		}
	}
	return searchMemoryCurrent(memory, spaces, params), nil
}

// finalizeRankedCandidates materializes the ranked window from start into
// response.Results. It re-reads authoritative current state for every candidate,
// deduplicates by memory, derives matched channels and selection reason from the
// candidate's channel fields, attaches conflict references and explain payloads,
// and emits the continuation cursor when the window filled.
func (r *Repository) finalizeRankedCandidates(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, response *domain.SearchResponse, list []*searchCandidate, start int, identities searchIdentities, hash string) error {
	response.RankingIdentity = identities.Ranking
	response.ContextIdentity = identities.Context
	response.FeedbackIdentity = identities.Feedback
	response.QueryPlanIdentity = params.Plan.Identity()
	returned := make(map[string]struct{})
	var last *searchCandidate
	windowFull := false
	window := selectionWindow(params.Limit)
	for i := start; i < len(list); i++ {
		candidate := list[i]
		if _, duplicate := returned[candidate.memoryID]; duplicate {
			continue
		}
		memory, err := getMemory(ctx, tx, candidate.memoryID, false)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		current, err := searchCandidateCurrent(ctx, tx, memory, *candidate, spaces, params)
		if err != nil {
			return err
		}
		if !current {
			continue
		}
		channels := make([]string, 0, 3)
		reason := ""
		var keywordRelevance, semanticScore *float64
		if candidate.keywordRank > 0 {
			channels = append(channels, ChannelKeyword)
			value := candidate.keywordScore
			keywordRelevance = &value
			reason = "keyword_match"
		}
		if candidate.semanticRank > 0 && !candidate.semanticRejected {
			channels = append(channels, ChannelSemantic)
			value := candidate.semanticScore
			semanticScore = &value
			if reason == "" {
				reason = "semantic_match"
			}
		}
		if candidate.graphRank > 0 {
			channels = append(channels, ChannelGraph)
			if reason == "" {
				reason = "graph_match"
			}
		}
		if len(channels) >= 2 {
			reason = "channel_fusion"
		}
		refs, err := conflictRefs(ctx, tx, memory.MemoryID, memory.CurrentRevision.RevisionID)
		if err != nil {
			return err
		}
		searchResult := domain.SearchResult{Memory: memory, Rank: len(response.Results) + 1, KeywordRelevance: keywordRelevance, SemanticScore: semanticScore, MatchedChannels: channels, GraphSignals: candidate.graphSignals, GraphFacts: candidate.graphFacts, GraphPaths: candidate.graphPaths, SelectionReason: reason, ConflictRefs: refs, RankContribution: rankContribution(candidate), ProtectedPrimary: candidate.protected, KeywordHintMatched: candidate.hintMatched}
		if params.Explain {
			r.applyExplain(candidate, &searchResult, params)
		}
		response.Results = append(response.Results, searchResult)
		returned[memory.MemoryID] = struct{}{}
		last = candidate
		if len(response.Results) >= window {
			windowFull = true
			break
		}
	}
	// A full ranked window may have more candidates behind the bounded recall,
	// so it always yields a continuation cursor anchored at the window tail.
	if windowFull && last != nil {
		response.Truncated = true
		next, err := params.processor.EncodeCursor(gatewayRecallParams(params), gatewayCursorBinding(identities), gwretrieval.CursorPosition{MemoryID: last.memoryID, RevisionID: last.revisionID, Score: last.score})
		if err != nil {
			return err
		}
		response.NextCursor = &next
	}
	return nil
}

func searchScope(ctx context.Context, tx *sql.Tx, starts []string, ancestors bool) ([]string, error) {
	set := make(map[string]struct{})
	for _, start := range starts {
		ids, err := memoryScope(ctx, tx, start, ancestors)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			set[id] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func searchMemoryCurrent(memory domain.Memory, spaces []string, params SearchParams) bool {
	if params.ExcludeStaleAggregates && memory.AggregateStale {
		return false
	}
	if !contains(spaces, memory.SpaceID) || !contains(params.States, memory.CurrentState) {
		return false
	}
	if memory.CurrentState != "active" && memory.CurrentState != "contested" {
		return false
	}
	if params.MemoryTypesSet && !contains(params.MemoryTypes, string(memory.Kind)) {
		return false
	}
	reference, err := time.Parse(time.RFC3339Nano, params.ReferenceTime)
	if err != nil {
		return false
	}
	if memory.CurrentRevision.ValidFrom != nil {
		if reference.Before(*memory.CurrentRevision.ValidFrom) {
			return false
		}
	}
	if memory.CurrentRevision.ValidUntil != nil {
		if !reference.Before(*memory.CurrentRevision.ValidUntil) {
			return false
		}
	}
	return true
}

func searchQueryHash(params SearchParams) string {
	spaces, types, states := append([]string(nil), params.SpaceIDs...), append([]string(nil), params.MemoryTypes...), append([]string(nil), params.States...)
	sort.Strings(spaces)
	sort.Strings(types)
	sort.Strings(states)
	planIdentity := ""
	if params.Plan.Version != "" {
		planIdentity = params.Plan.Identity()
	}
	canonical := struct {
		Version                int      `json:"v"`
		Query                  string   `json:"query"`
		QueryPlan              string   `json:"query_plan"`
		Spaces                 []string `json:"space_ids"`
		Ancestors              bool     `json:"include_ancestors"`
		Types                  []string `json:"memory_types"`
		TypesSet               bool     `json:"memory_types_set"`
		States                 []string `json:"states"`
		Reference              string   `json:"reference_time"`
		Channels               []string `json:"channels"`
		ExecutedChannels       []string `json:"executed_channels"`
		Semantic               int      `json:"semantic_version"`
		Limit                  int      `json:"limit"`
		MaxChars               int      `json:"max_chars"`
		MaxContextTokens       int      `json:"max_context_tokens"`
		MaxSources             int      `json:"max_sources"`
		MaxSensitive           int      `json:"max_sensitive"`
		SkipDiversification    bool     `json:"skip_diversification"`
		QuestionType           string   `json:"question_type"`
		Evidence               bool     `json:"include_evidence"`
		ExcludeStaleAggregates bool     `json:"exclude_stale_aggregates"`
		Intent                 string   `json:"intent"`
		GraphPolicy            string   `json:"graph_policy"`
	}{4, params.Query, planIdentity, spaces, params.IncludeAncestors, types, params.MemoryTypesSet, states, params.ReferenceTime, params.Channels, params.EffectiveChannels, params.SemanticVersion, params.Limit, params.MaxChars, params.MaxContextTokens, params.MaxSources, params.MaxSensitive, params.SkipDiversification, string(params.Plan.QuestionType), params.IncludeEvidence, params.ExcludeStaleAggregates, params.Intent, string(params.GraphPolicy)}
	encoded, _ := json.Marshal(canonical)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
