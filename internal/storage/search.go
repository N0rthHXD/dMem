package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/mattn/go-sqlite3"
)

// Retrieval channels are the only top-level recall sources. Entity resolution
// is query understanding, and entity facts / memory relations are internal
// graph signals; neither contributes an independent rank to the fusion.
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
	IncludeEvidence        bool
	ExcludeStaleAggregates bool
	Cursor                 string
}

type searchCursor struct {
	Version                  int      `json:"v"`
	QueryHash                string   `json:"q"`
	RequestedChannels        []string `json:"rc"`
	ExecutedChannels         []string `json:"ec"`
	RankingVersion           string   `json:"rv"`
	FTSVersion               int      `json:"fv"`
	SemanticVersion          int      `json:"sv"`
	CorpusGeneration         int64    `json:"cg"`
	FeedbackAggregateVersion int64    `json:"av"`
	EntityIndexVersion       int64    `json:"ev"`
	RelationIndexVersion     int64    `json:"lv"`
	GraphIndexVersion        int64    `json:"gv"`
	EntityIndexGeneration    int64    `json:"eg"`
	RelationIndexGeneration  int64    `json:"lg"`
	GraphIndexGeneration     int64    `json:"gg"`
	Limit                    int      `json:"l"`
	MaxChars                 int      `json:"b"`
	ScoreBits                string   `json:"s"`
	MemoryID                 string   `json:"m"`
	RevisionID               string   `json:"r"`
	ReferenceTime            string   `json:"rt"`
}

type searchCandidate struct {
	memoryID, revisionID, updatedAt string
	ftsContent, projectionContent   string
	keywordScore, rrfScore, score   float64
	keywordRank, graphRank          int
	graphSignals                    []domain.GraphSignal
	graphFacts                      []domain.EntityFact
	graphPaths                      []domain.GraphPath
}

func (r *Repository) SearchMemories(ctx context.Context, params SearchParams) (domain.SearchResponse, error) {
	params.Channels = canonicalChannels(params.Channels)
	if len(params.Channels) == 0 {
		params.Channels = append([]string{}, retrievalChannelOrder...)
	}
	if len(params.EffectiveChannels) == 0 {
		params.EffectiveChannels = params.Channels
	} else {
		params.EffectiveChannels = canonicalChannels(params.EffectiveChannels)
	}
	if params.Cursor != "" && !params.ReferenceTimeSet {
		cursor, err := decodeSearchCursor(params.Cursor)
		if err != nil {
			return domain.SearchResponse{}, ErrInvalidCursor
		}
		params.ReferenceTime = cursor.ReferenceTime
	}
	if hasChannel(params.EffectiveChannels, ChannelSemantic) {
		return r.searchSemanticHybrid(ctx, params)
	}
	return r.searchKeywordGraph(ctx, params)
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
	if after != nil && !searchCursorMatches(*after, hash, params, ftsVersion, params.SemanticVersion, metadata) {
		return domain.SearchResponse{}, ErrInvalidCursor
	}
	response := domain.SearchResponse{Results: []domain.SearchResult{}, RequestedChannels: params.Channels, ExecutedChannels: executed, DegradationReasons: []string{}, FTSIndexVersion: ftsVersion, SemanticIndexVersion: params.SemanticVersion, RankingVersion: rankingVersion, CorpusGeneration: metadata.corpusGeneration, FeedbackAggregateVersion: metadata.feedbackAggregateVersion, GraphIndexVersion: metadata.graphIndexVersion, GraphIndexState: metadata.graphIndexState}
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
		if err := tx.Commit(); err != nil {
			return domain.SearchResponse{}, err
		}
		return response, nil
	}

	all := map[string]*searchCandidate{}
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
			all[item.memoryID+"\x00"+item.revisionID] = &item
		}
	}
	if hasChannel(executed, ChannelGraph) {
		resolved, graphErr := r.appendGraphCandidates(ctx, tx, params, spaces, all, max(100, params.Limit*10))
		if graphErr != nil {
			return domain.SearchResponse{}, graphErr
		}
		response.ResolvedEntities = resolved
	}
	usageScores, err := feedbackUsageScores(ctx, tx, revisionKeys(all), metadata.feedbackAggregateVersion)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	candidates := make([]searchCandidate, 0, len(all))
	for _, item := range all {
		item.rrfScore = rrfScore(item.keywordRank, 0, item.graphRank)
		item.score = item.rrfScore + 0.001*usageScores[item.revisionID]
		candidates = append(candidates, *item)
	}
	sortCandidates(candidates)
	start := 0
	if after != nil {
		found := false
		for i := range candidates {
			if candidates[i].memoryID == after.MemoryID && candidates[i].revisionID == after.RevisionID && strconv.FormatUint(math.Float64bits(candidates[i].score), 16) == after.ScoreBits {
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
	for i := start; i < len(candidates); i++ {
		candidate := candidates[i]
		if _, duplicate := returned[candidate.memoryID]; duplicate {
			continue
		}
		if len(response.Results) == params.Limit {
			memory, err := getMemory(ctx, tx, candidate.memoryID, false)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return domain.SearchResponse{}, err
			}
			if err == nil && searchCandidateCurrent(memory, candidate, spaces, params) {
				hasMore = true
				response.Truncated = true
				break
			}
			continue
		}
		memory, err := getMemory(ctx, tx, candidate.memoryID, false)
		if errors.Is(err, ErrNotFound) {
			lastInspected = &candidate
			continue
		}
		if err != nil {
			return domain.SearchResponse{}, err
		}
		if !searchCandidateCurrent(memory, candidate, spaces, params) {
			lastInspected = &candidate
			continue
		}
		chars := utf8.RuneCountInString(memory.CurrentRevision.Content)
		if response.CharsUsed+chars > params.MaxChars {
			response.Truncated = true
			if len(response.Results) > 0 && chars <= params.MaxChars {
				hasMore = true
				break
			}
			lastInspected = &candidate
			continue
		}
		lastInspected = &candidate
		if !params.IncludeEvidence {
			memory.CurrentRevision.Evidence = nil
		}
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
		response.Results = append(response.Results, domain.SearchResult{Memory: memory, Rank: len(response.Results) + 1, KeywordRelevance: relevance, MatchedChannels: channels, GraphSignals: candidate.graphSignals, GraphFacts: candidate.graphFacts, GraphPaths: candidate.graphPaths, SelectionReason: reason, ConflictRefs: refs})
		returned[memory.MemoryID] = struct{}{}
		response.CharsUsed += chars
	}
	if hasMore && lastInspected != nil {
		next := encodeSearchCursor(hash, params, ftsVersion, params.SemanticVersion, metadata, *lastInspected)
		response.NextCursor = &next
	}
	if err := tx.Commit(); err != nil {
		return domain.SearchResponse{}, err
	}
	return response, nil
}

// keywordRecall performs bounded FTS recall with all eligibility filters
// applied before the top-k cut.
func (r *Repository) keywordRecall(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, limit int) ([]searchCandidate, error) {
	ftsQuery, searchable := compileKeywordQuery(params.Query)
	if !searchable {
		return nil, nil
	}
	query := `WITH ranked AS (
		SELECT f.memory_id, f.revision_id, f.content, p.display_content, p.updated_at, bm25(memory_fts) AS score
		FROM memory_fts f
		JOIN memory_projections p ON p.memory_id=f.memory_id AND p.revision_id=f.revision_id
		JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state
		WHERE memory_fts MATCH ? AND p.searchable=1`
	args := []any{ftsQuery}
	query += ` AND p.space_id IN (` + placeholders(len(spaces)) + `)`
	for _, id := range spaces {
		args = append(args, id)
	}
	query += ` AND p.current_state IN (` + placeholders(len(params.States)) + `)`
	for _, state := range params.States {
		args = append(args, state)
	}
	if params.MemoryTypesSet {
		query += ` AND p.memory_type IN (` + placeholders(len(params.MemoryTypes)) + `)`
		for _, memoryType := range params.MemoryTypes {
			args = append(args, memoryType)
		}
	}
	query += ` AND (p.valid_from IS NULL OR p.valid_from<=?) AND (p.valid_until IS NULL OR p.valid_until>?)`
	args = append(args, params.ReferenceTime, params.ReferenceTime)
	query += `) SELECT memory_id, revision_id, content, display_content, updated_at, score FROM ranked`
	query += ` ORDER BY score ASC, updated_at DESC, memory_id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrError {
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "fts5: syntax error") || strings.Contains(message, "unterminated string") || strings.Contains(message, "malformed match") {
				return nil, ErrInvalidSearchQuery
			}
		}
		return nil, err
	}
	defer rows.Close()
	var candidates []searchCandidate
	for rows.Next() {
		var item searchCandidate
		if err := rows.Scan(&item.memoryID, &item.revisionID, &item.ftsContent, &item.projectionContent, &item.updatedAt, &item.keywordScore); err != nil {
			return nil, err
		}
		if math.IsNaN(item.keywordScore) || math.IsInf(item.keywordScore, 0) {
			return nil, ErrInvalidSearchQuery
		}
		candidates = append(candidates, item)
	}
	return candidates, rows.Err()
}

// appendGraphCandidates resolves query entities once, runs the graph channel
// against strict seeds (keyword candidates plus resolved-entity direct
// mentions), and merges the resulting single graph rank into all.
func (r *Repository) appendGraphCandidates(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, all map[string]*searchCandidate, limit int) ([]domain.ResolvedEntity, error) {
	resolved, err := resolveQueryEntities(ctx, tx, params.Query, spaces)
	if err != nil {
		return nil, err
	}
	keywordSeeds := make([]string, 0, len(all))
	for key := range all {
		_, revisionID, _ := strings.Cut(key, "\x00")
		keywordSeeds = append(keywordSeeds, revisionID)
	}
	graph, err := r.graphChannelRecall(ctx, tx, params, spaces, resolved, keywordSeeds, limit)
	if err != nil {
		return nil, err
	}
	for i := range graph.items {
		item := graph.items[i]
		key := item.memoryID + "\x00" + item.revisionID
		if existing := all[key]; existing != nil {
			existing.graphRank = item.graphRank
			existing.graphSignals = item.signals
			existing.graphFacts = item.facts
			existing.graphPaths = item.paths
		} else {
			all[key] = &searchCandidate{memoryID: item.memoryID, revisionID: item.revisionID, graphRank: item.graphRank, graphSignals: item.signals, graphFacts: item.facts, graphPaths: item.paths}
		}
	}
	return graph.resolvedEntities, nil
}

func revisionKeys(all map[string]*searchCandidate) []string {
	keys := make([]string, 0, len(all))
	for key := range all {
		_, revisionID, _ := strings.Cut(key, "\x00")
		keys = append(keys, revisionID)
	}
	return keys
}

func rrfScore(keywordRank, semanticRank, graphRank int) float64 {
	score := 0.0
	if keywordRank > 0 {
		score += 1 / float64(60+keywordRank)
	}
	if semanticRank > 0 {
		score += 1 / float64(60+semanticRank)
	}
	if graphRank > 0 {
		score += 1 / float64(60+graphRank)
	}
	return score
}

func sortCandidates(candidates []searchCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].rrfScore != candidates[j].rrfScore {
			return candidates[i].rrfScore > candidates[j].rrfScore
		}
		bi, bj := candidateBestRank(candidates[i]), candidateBestRank(candidates[j])
		if bi != bj {
			return bi < bj
		}
		if candidates[i].memoryID != candidates[j].memoryID {
			return candidates[i].memoryID < candidates[j].memoryID
		}
		return candidates[i].revisionID < candidates[j].revisionID
	})
}

func candidateBestRank(candidate searchCandidate) int {
	best := 0
	for _, rank := range []int{candidate.keywordRank, candidate.graphRank} {
		if rank > 0 && (best == 0 || rank < best) {
			best = rank
		}
	}
	return best
}

func searchCandidateCurrent(memory domain.Memory, candidate searchCandidate, spaces []string, params SearchParams) bool {
	if memory.CurrentRevision.RevisionID != candidate.revisionID {
		return false
	}
	if candidate.ftsContent != "" && (memory.CurrentRevision.Content != candidate.ftsContent || memory.CurrentRevision.Content != candidate.projectionContent) {
		return false
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
	if params.ExcludeStaleAggregates && memory.MemoryType == "aggregate" && memory.AggregateStale {
		return false
	}
	if !contains(spaces, memory.SpaceID) || !contains(params.States, memory.CurrentState) {
		return false
	}
	if memory.CurrentState != "active" && memory.CurrentState != "contested" {
		return false
	}
	if params.MemoryTypesSet && !contains(params.MemoryTypes, memory.MemoryType) {
		return false
	}
	reference, err := time.Parse(time.RFC3339Nano, params.ReferenceTime)
	if err != nil {
		return false
	}
	if memory.CurrentRevision.ValidFrom != nil {
		from, err := time.Parse(time.RFC3339Nano, *memory.CurrentRevision.ValidFrom)
		if err != nil || reference.Before(from) {
			return false
		}
	}
	if memory.CurrentRevision.ValidUntil != nil {
		until, err := time.Parse(time.RFC3339Nano, *memory.CurrentRevision.ValidUntil)
		if err != nil || !reference.Before(until) {
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
	canonical := struct {
		Version                int      `json:"v"`
		Query                  string   `json:"query"`
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
	}{2, params.Query, spaces, params.IncludeAncestors, types, params.MemoryTypesSet, states, params.ReferenceTime, params.Channels, params.EffectiveChannels, params.SemanticVersion, params.Limit, params.MaxChars, params.IncludeEvidence, params.ExcludeStaleAggregates}
	encoded, _ := json.Marshal(canonical)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func searchCursorMatches(cursor searchCursor, hash string, params SearchParams, ftsVersion, semanticVersion int, metadata searchMetadata) bool {
	return cursor.QueryHash == hash && cursor.ReferenceTime == params.ReferenceTime && equalStringSlices(cursor.RequestedChannels, params.Channels) && equalStringSlices(cursor.ExecutedChannels, params.EffectiveChannels) && cursor.RankingVersion == rankingVersion && cursor.FTSVersion == ftsVersion && cursor.SemanticVersion == semanticVersion && cursor.CorpusGeneration == metadata.corpusGeneration && cursor.FeedbackAggregateVersion == metadata.feedbackAggregateVersion && cursor.EntityIndexVersion == metadata.entityIndexVersion && cursor.RelationIndexVersion == metadata.relationIndexVersion && cursor.GraphIndexVersion == metadata.graphIndexVersion && cursor.EntityIndexGeneration == metadata.entityIndexGeneration && cursor.RelationIndexGeneration == metadata.relationIndexGeneration && cursor.GraphIndexGeneration == metadata.graphIndexGeneration && cursor.Limit == params.Limit && cursor.MaxChars == params.MaxChars
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func encodeSearchCursor(hash string, params SearchParams, ftsVersion, semanticVersion int, metadata searchMetadata, candidate searchCandidate) string {
	value := searchCursor{Version: 4, QueryHash: hash, RequestedChannels: params.Channels, ExecutedChannels: params.EffectiveChannels, RankingVersion: rankingVersion, FTSVersion: ftsVersion, SemanticVersion: semanticVersion, CorpusGeneration: metadata.corpusGeneration, FeedbackAggregateVersion: metadata.feedbackAggregateVersion, EntityIndexVersion: metadata.entityIndexVersion, RelationIndexVersion: metadata.relationIndexVersion, GraphIndexVersion: metadata.graphIndexVersion, EntityIndexGeneration: metadata.entityIndexGeneration, RelationIndexGeneration: metadata.relationIndexGeneration, GraphIndexGeneration: metadata.graphIndexGeneration, Limit: params.Limit, MaxChars: params.MaxChars, ScoreBits: strconv.FormatUint(math.Float64bits(candidate.score), 16), MemoryID: candidate.memoryID, RevisionID: candidate.revisionID, ReferenceTime: params.ReferenceTime}
	encoded, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeSearchCursor(value string) (searchCursor, error) {
	var cursor searchCursor
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 4 || cursor.QueryHash == "" || len(cursor.RequestedChannels) == 0 || len(cursor.ExecutedChannels) == 0 || cursor.RankingVersion != rankingVersion || cursor.FTSVersion < 1 || cursor.MemoryID == "" || cursor.RevisionID == "" || cursor.ReferenceTime == "" {
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

func parseScoreBits(value string) uint64 {
	bits, _ := strconv.ParseUint(value, 16, 64)
	return bits
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
