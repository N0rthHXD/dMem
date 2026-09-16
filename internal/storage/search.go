package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
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
	Intent                 string
	GraphPolicy            retrieval.GraphPolicy
	Plan                   retrieval.QueryPlan
	QueryTime              retrieval.TimeInterval
	QueryTimeSet           bool
	SkipDiversification    bool
	telemetry              *recallTelemetry
}

// searchIdentities binds a cursor to every algorithm identity that can change
// candidate membership, rank, or rendered context. The decoder accepts only the
// current version and no legacy channel payload (ADR 0035 section 11).
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
	searchContextIdentity   = domain.ContextSelectionVersion + ":tokens=" + domain.ContextTokenFormatVersion
)

// rankingVersion is the versioned deterministic feature-rank identity.
const rankingVersion = retrieval.RankingWeightsVersion

func currentSearchIdentities(ftsVersion, semanticVersion int, metadata searchMetadata, rankingIdentity string) searchIdentities {
	return searchIdentities{
		Schema:     "schema-v" + strconv.Itoa(SchemaVersion),
		QueryPlan:  searchQueryPlanIdentity,
		Membership: membershipIdentity(ftsVersion, semanticVersion, metadata),
		Ranking:    rankingIdentity,
		Graph:      graphIdentity(metadata),
		Context:    searchContextIdentity,
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

type searchCursor struct {
	Version            int    `json:"v"`
	SchemaIdentity     string `json:"si"`
	QueryHash          string `json:"q"`
	QueryPlanIdentity  string `json:"qp"`
	MembershipIdentity string `json:"mi"`
	RankingIdentity    string `json:"ri"`
	GraphIdentity      string `json:"gi"`
	ContextIdentity    string `json:"ci"`
	FeedbackIdentity   string `json:"fi"`
	Limit              int    `json:"l"`
	MaxChars           int    `json:"b"`
	ScoreBits          string `json:"s"`
	MemoryID           string `json:"m"`
	RevisionID         string `json:"r"`
	ReferenceTime      string `json:"rt"`
}

type searchCandidate struct {
	memoryID, revisionID, updatedAt string
	contentHash                     string
	ftsContent, projectionContent   string
	keywordRaw, keywordScore        float64
	semanticRaw, semanticScore      float64
	semanticPercentile              float64
	semanticRejected                bool
	semanticRejectReason            string
	graphScore                      float64
	score                           float64
	keywordRank, semanticRank       int
	graphRank                       int
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

func (r *Repository) SearchMemories(ctx context.Context, params SearchParams) (domain.SearchResponse, error) {
	params.Channels = canonicalChannels(params.Channels)
	if len(params.EffectiveChannels) > 0 {
		params.EffectiveChannels = canonicalChannels(params.EffectiveChannels)
	}
	if params.Cursor != "" && !params.ReferenceTimeSet {
		cursor, err := decodeSearchCursor(params.Cursor)
		if err != nil {
			return domain.SearchResponse{}, ErrInvalidCursor
		}
		params.ReferenceTime = cursor.ReferenceTime
	}
	if params.ReferenceTime == "" {
		params.ReferenceTime = formatTime(r.runtime.Now())
	}
	params.telemetry = newRecallTelemetry()
	totalStart := time.Now()
	planStart := time.Now()
	params = r.planQuery(ctx, params)
	params.telemetry.markPhase("query_planning", planStart)
	if len(params.Channels) == 0 {
		params.Channels = []string{ChannelKeyword, ChannelSemantic}
		if params.Plan.GraphPolicy != retrieval.GraphOff {
			params.Channels = append(params.Channels, ChannelGraph)
		}
	}
	if len(params.EffectiveChannels) == 0 {
		params.EffectiveChannels = params.Channels
	}
	var response domain.SearchResponse
	var err error
	recallStart := time.Now()
	if hasChannel(params.EffectiveChannels, ChannelSemantic) {
		response, err = r.searchSemanticHybrid(ctx, params)
	} else {
		response, err = r.searchKeywordGraph(ctx, params)
	}
	params.telemetry.markPhase("recall", recallStart)
	response.ReferenceTime = params.ReferenceTime
	if err == nil {
		selectStart := time.Now()
		err = r.applyContextSelection(ctx, params, &response)
		params.telemetry.markPhase("context_selection", selectStart)
	}
	params.telemetry.markPhase("total", totalStart)
	if err == nil && params.OperationID != "" {
		err = r.recordQueryTelemetry(ctx, params, response)
	}
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
	var after *searchCursor
	if params.Cursor != "" {
		cursor, err := decodeSearchCursor(params.Cursor)
		if err != nil {
			return domain.SearchResponse{}, ErrInvalidCursor
		}
		if !params.ReferenceTimeSet {
			params.ReferenceTime = cursor.ReferenceTime
		}
		after = &cursor
	}
	hash := searchQueryHash(params)
	identities := currentSearchIdentities(ftsVersion, params.SemanticVersion, metadata, r.rankingIdentity())
	if after != nil && !searchCursorMatches(*after, hash, params, identities) {
		return domain.SearchResponse{}, ErrInvalidCursor
	}
	response := domain.SearchResponse{Results: []domain.SearchResult{}, RequestedChannels: params.Channels, ExecutedChannels: executed, DegradationReasons: []string{}, FTSIndexVersion: ftsVersion, SemanticIndexVersion: params.SemanticVersion, RankingVersion: rankingVersion, QueryPlanIdentity: params.Plan.Identity(), RankingIdentity: identities.Ranking, ContextIdentity: identities.Context, CorpusGeneration: metadata.corpusGeneration, FeedbackAggregateVersion: metadata.feedbackAggregateVersion, FeedbackIdentity: identities.Feedback, GraphIndexVersion: metadata.graphIndexVersion, GraphIndexState: metadata.graphIndexState}
	if hasChannel(executed, ChannelKeyword) && indexState != "ready" {
		executed = channelsWithout(executed, ChannelKeyword)
		response.ExecutedChannels = executed
		response.Degraded = true
		response.DegradationReasons = append(response.DegradationReasons, "keyword_unavailable")
	}
	if len(executed) == 0 {
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	if len(params.States) == 0 || params.MemoryTypesSet && len(params.MemoryTypes) == 0 {
		executed, response = r.applyGraphReadiness(params, metadata, response, executed)
		if err := tx.Commit(); err != nil {
			return domain.SearchResponse{}, err
		}
		return response, nil
	}

	all := map[string]*searchCandidate{}
	keywordStart := time.Now()
	if hasChannel(executed, ChannelKeyword) {
		candidates, keywordErr := r.keywordRecall(ctx, tx, params, spaces, max(100, params.Limit*10))
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
	calibrateKeywordScores(candidatePointers(all))
	params.telemetry.markPhase("keyword", keywordStart)
	graphStart := time.Now()
	executed, response, err = r.applyGraphChannel(ctx, tx, params, spaces, metadata, response, executed, all, false, max(100, params.Limit*10))
	if err != nil {
		return domain.SearchResponse{}, err
	}
	params.telemetry.markPhase("graph", graphStart)
	if len(executed) == 0 {
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	usageScores, err := r.FeedbackUsageScores(ctx, tx, revisionKeys(all), metadata.feedbackAggregateVersion, searchQueryHash(params))
	if err != nil {
		return domain.SearchResponse{}, err
	}
	list := candidatePointers(all)
	if err := hydrateRankingProjections(ctx, tx, list); err != nil {
		return domain.SearchResponse{}, err
	}
	recordCandidateTelemetry(params, all)
	temporalBefore := len(list)
	list = applyTemporalFilter(list, params.QueryTime, params.Plan.QuestionType)
	if removed := temporalBefore - len(list); removed > 0 {
		for i := 0; i < removed; i++ {
			params.telemetry.addGate("temporal_filter")
		}
	}
	rankStart := time.Now()
	r.rankCandidates(list, params, usageScores)
	list = admittedRankedCandidates(list)
	params.telemetry.markPhase("ranking", rankStart)
	recordGraphAdmissionTelemetry(params, list)
	start := 0
	if after != nil {
		found := false
		for i := range list {
			if list[i].memoryID == after.MemoryID && list[i].revisionID == after.RevisionID && strconv.FormatUint(math.Float64bits(list[i].score), 16) == after.ScoreBits {
				start, found = i+1, true
				break
			}
		}
		if !found {
			return domain.SearchResponse{}, ErrInvalidCursor
		}
	}

	var lastInspected *searchCandidate
	hasMore := false
	returned := make(map[string]struct{})
	for i := start; i < len(list); i++ {
		candidate := list[i]
		if _, duplicate := returned[candidate.memoryID]; duplicate {
			continue
		}
		if len(response.Results) == params.Limit {
			memory, err := getMemory(ctx, tx, candidate.memoryID, false)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return domain.SearchResponse{}, err
			}
			if err == nil && searchCandidateCurrent(memory, *candidate, spaces, params) {
				hasMore = true
				response.Truncated = true
				break
			}
			continue
		}
		memory, err := getMemory(ctx, tx, candidate.memoryID, false)
		if errors.Is(err, ErrNotFound) {
			lastInspected = candidate
			continue
		}
		if err != nil {
			return domain.SearchResponse{}, err
		}
		if !searchCandidateCurrent(memory, *candidate, spaces, params) {
			lastInspected = candidate
			continue
		}
		chars := utf8.RuneCountInString(memory.CurrentRevision.DisplayContent)
		if response.CharsUsed+chars > params.MaxChars {
			response.Truncated = true
			if len(response.Results) > 0 && chars <= params.MaxChars {
				hasMore = true
				break
			}
			lastInspected = candidate
			continue
		}
		lastInspected = candidate
		channels := make([]string, 0, 2)
		var relevance *float64
		reason := ""
		if candidate.keywordRank > 0 {
			channels = append(channels, ChannelKeyword)
			value := candidate.keywordScore
			relevance = &value
			reason = "keyword_match"
		}
		if candidate.graphRank > 0 {
			channels = append(channels, ChannelGraph)
			if reason == "" {
				reason = "graph_match"
			} else {
				reason = "channel_fusion"
			}
		}
		refs, err := conflictRefs(ctx, tx, memory.MemoryID, memory.CurrentRevision.RevisionID)
		if err != nil {
			return domain.SearchResponse{}, err
		}
		searchResult := domain.SearchResult{Memory: memory, Rank: len(response.Results) + 1, KeywordRelevance: relevance, MatchedChannels: channels, GraphSignals: candidate.graphSignals, GraphFacts: candidate.graphFacts, GraphPaths: candidate.graphPaths, SelectionReason: reason, ConflictRefs: refs, RankContribution: rankContribution(candidate), ProtectedPrimary: candidate.protected}
		if params.Explain {
			r.applyExplain(candidate, &searchResult, params)
		}
		response.Results = append(response.Results, searchResult)
		returned[memory.MemoryID] = struct{}{}
		response.CharsUsed += chars
	}
	if hasMore && lastInspected != nil {
		next := encodeSearchCursor(hash, params, identities, *lastInspected)
		response.NextCursor = &next
	}
	if err := tx.Commit(); err != nil {
		return domain.SearchResponse{}, err
	}
	return response, nil
}

func searchCandidateCurrent(memory domain.Memory, candidate searchCandidate, spaces []string, params SearchParams) bool {
	if memory.CurrentRevision.RevisionID != candidate.revisionID {
		return false
	}
	if candidate.ftsContent != "" {
		if memory.CurrentRevision.DisplayContent != candidate.projectionContent {
			return false
		}
		if canonicalStructuredDocument(memory.CurrentRevision) != candidate.ftsContent {
			return false
		}
	}
	return searchMemoryCurrent(memory, spaces, params)
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
		Evidence               bool     `json:"include_evidence"`
		ExcludeStaleAggregates bool     `json:"exclude_stale_aggregates"`
		Intent                 string   `json:"intent"`
		GraphPolicy            string   `json:"graph_policy"`
	}{3, params.Query, planIdentity, spaces, params.IncludeAncestors, types, params.MemoryTypesSet, states, params.ReferenceTime, params.Channels, params.EffectiveChannels, params.SemanticVersion, params.Limit, params.MaxChars, params.IncludeEvidence, params.ExcludeStaleAggregates, params.Intent, string(params.GraphPolicy)}
	encoded, _ := json.Marshal(canonical)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func searchCursorMatches(cursor searchCursor, hash string, params SearchParams, identities searchIdentities) bool {
	return cursor.QueryHash == hash && cursor.ReferenceTime == params.ReferenceTime &&
		cursor.SchemaIdentity == identities.Schema && cursor.QueryPlanIdentity == identities.QueryPlan &&
		cursor.MembershipIdentity == identities.Membership && cursor.RankingIdentity == identities.Ranking &&
		cursor.GraphIdentity == identities.Graph && cursor.ContextIdentity == identities.Context &&
		cursor.FeedbackIdentity == identities.Feedback &&
		cursor.Limit == params.Limit && cursor.MaxChars == params.MaxChars
}

func encodeSearchCursor(hash string, params SearchParams, identities searchIdentities, candidate searchCandidate) string {
	value := searchCursor{
		Version: 5, SchemaIdentity: identities.Schema, QueryHash: hash, QueryPlanIdentity: identities.QueryPlan,
		MembershipIdentity: identities.Membership, RankingIdentity: identities.Ranking, GraphIdentity: identities.Graph,
		ContextIdentity: identities.Context, FeedbackIdentity: identities.Feedback, Limit: params.Limit,
		MaxChars: params.MaxChars, ScoreBits: strconv.FormatUint(math.Float64bits(candidate.score), 16),
		MemoryID: candidate.memoryID, RevisionID: candidate.revisionID, ReferenceTime: params.ReferenceTime,
	}
	encoded, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeSearchCursor(value string) (searchCursor, error) {
	var cursor searchCursor
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return searchCursor{}, ErrInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || cursor.Version != 5 ||
		cursor.SchemaIdentity == "" || cursor.QueryHash == "" || cursor.QueryPlanIdentity == "" ||
		cursor.MembershipIdentity == "" || cursor.RankingIdentity == "" || cursor.GraphIdentity == "" ||
		cursor.ContextIdentity == "" || cursor.FeedbackIdentity == "" ||
		cursor.MemoryID == "" || cursor.RevisionID == "" || cursor.ReferenceTime == "" {
		return searchCursor{}, ErrInvalidCursor
	}
	bits, err := strconv.ParseUint(cursor.ScoreBits, 16, 64)
	if err != nil || math.IsNaN(math.Float64frombits(bits)) || math.IsInf(math.Float64frombits(bits), 0) {
		return searchCursor{}, ErrInvalidCursor
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.ReferenceTime); err != nil {
		return searchCursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
