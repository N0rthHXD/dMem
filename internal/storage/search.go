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

type SearchParams struct {
	Query                  string
	SpaceIDs               []string
	IncludeAncestors       bool
	MemoryTypes            []string
	MemoryTypesSet         bool
	States                 []string
	ReferenceTime          string
	ReferenceTimeSet       bool
	Mode                   string
	RequestedMode          string
	SemanticVersion        int
	Limit                  int
	MaxChars               int
	IncludeEvidence        bool
	ExcludeStaleAggregates bool
	Cursor                 string
}

type searchCursor struct {
	Version                  int    `json:"v"`
	QueryHash                string `json:"q"`
	RequestedMode            string `json:"rm"`
	ExecutedMode             string `json:"em"`
	RankingVersion           string `json:"rv"`
	FTSVersion               int    `json:"fv"`
	SemanticVersion          int    `json:"sv"`
	CorpusGeneration         int64  `json:"cg"`
	FeedbackAggregateVersion int64  `json:"av"`
	EntityIndexVersion       int64  `json:"ev"`
	RelationIndexVersion     int64  `json:"lv"`
	GraphIndexVersion        int64  `json:"gv"`
	EntityIndexGeneration    int64  `json:"eg"`
	RelationIndexGeneration  int64  `json:"lg"`
	GraphIndexGeneration     int64  `json:"gg"`
	Limit                    int    `json:"l"`
	MaxChars                 int    `json:"b"`
	ScoreBits                string `json:"s"`
	UpdatedAt                string `json:"u"`
	MemoryID                 string `json:"m"`
	RevisionID               string `json:"r"`
	ReferenceTime            string `json:"rt"`
}

type searchCandidate struct {
	memoryID, revisionID, updatedAt       string
	ftsContent, projectionContent         string
	keywordScore, rrfScore, score         float64
	keywordRank, entityRank, relationRank int
	graphRank                             int
	entity, relation                      bool
	graphSignals                          []domain.GraphSignal
	graphFacts                            []domain.EntityFact
	graphPaths                            []domain.GraphPath
}

func (r *Repository) SearchMemories(ctx context.Context, params SearchParams) (domain.SearchResponse, error) {
	if params.Cursor != "" && !params.ReferenceTimeSet {
		if params.Mode == "semantic" {
			cursor, err := decodeHybridCursor(params.Cursor)
			if err != nil {
				return domain.SearchResponse{}, ErrInvalidCursor
			}
			params.ReferenceTime = cursor.ReferenceTime
		} else if params.Mode == "hybrid" {
			cursor, hybridErr := decodeHybridCursor(params.Cursor)
			if hybridErr == nil && cursor.ReferenceTime != "" {
				params.ReferenceTime = cursor.ReferenceTime
			} else {
				keywordCursor, keywordErr := decodeSearchCursor(params.Cursor)
				if keywordErr != nil || keywordCursor.RequestedMode != "hybrid" || keywordCursor.ExecutedMode != "keyword" {
					return domain.SearchResponse{}, ErrInvalidCursor
				}
				params.ReferenceTime = keywordCursor.ReferenceTime
			}
		} else {
			cursor, err := decodeSearchCursor(params.Cursor)
			if err != nil {
				return domain.SearchResponse{}, ErrInvalidCursor
			}
			params.ReferenceTime = cursor.ReferenceTime
		}
	}
	if params.Mode == "semantic" || params.Mode == "hybrid" {
		return r.searchSemanticHybrid(ctx, params)
	}
	if params.ReferenceTime == "" {
		params.ReferenceTime = formatTime(r.runtime.Now())
	}
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
	requestedMode := params.RequestedMode
	if requestedMode == "" {
		requestedMode = params.Mode
	}
	if after != nil && (after.QueryHash != hash || after.ReferenceTime != params.ReferenceTime || after.RequestedMode != requestedMode || after.ExecutedMode != "keyword" || after.FTSVersion != ftsVersion || after.SemanticVersion != params.SemanticVersion || after.CorpusGeneration != metadata.corpusGeneration || after.FeedbackAggregateVersion != metadata.feedbackAggregateVersion || after.EntityIndexVersion != metadata.entityIndexVersion || after.RelationIndexVersion != metadata.relationIndexVersion || after.GraphIndexVersion != metadata.graphIndexVersion || after.EntityIndexGeneration != metadata.entityIndexGeneration || after.RelationIndexGeneration != metadata.relationIndexGeneration || after.GraphIndexGeneration != metadata.graphIndexGeneration || after.Limit != params.Limit || after.MaxChars != params.MaxChars) {
		return domain.SearchResponse{}, ErrInvalidCursor
	}
	response := domain.SearchResponse{Results: []domain.SearchResult{}, IndexState: indexState, RequestedMode: requestedMode, ExecutedMode: "keyword", DegradationReasons: []string{}, FTSIndexVersion: ftsVersion, SemanticIndexVersion: params.SemanticVersion, RankingVersion: rankingVersion, CorpusGeneration: metadata.corpusGeneration, FeedbackAggregateVersion: metadata.feedbackAggregateVersion, EntityIndexVersion: metadata.entityIndexVersion, RelationIndexVersion: metadata.relationIndexVersion, GraphIndexVersion: metadata.graphIndexVersion}
	if indexState != "ready" {
		response.Degraded = true
		response.DegradationReasons = append(response.DegradationReasons, "keyword_unavailable")
	}
	if len(params.States) == 0 || params.MemoryTypesSet && len(params.MemoryTypes) == 0 {
		if err := tx.Commit(); err != nil {
			return domain.SearchResponse{}, err
		}
		return response, nil
	}
	ftsQuery, searchable := compileKeywordQuery(params.Query)
	if !searchable {
		if err := tx.Commit(); err != nil {
			return domain.SearchResponse{}, err
		}
		return response, nil
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
	args = append(args, max(100, params.Limit*10))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrError {
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "fts5: syntax error") || strings.Contains(message, "unterminated string") || strings.Contains(message, "malformed match") {
				return domain.SearchResponse{}, ErrInvalidSearchQuery
			}
			r.markFTSDegraded()
			return domain.SearchResponse{}, ErrIndexDegraded
		}
		return domain.SearchResponse{}, err
	}
	var candidates []searchCandidate
	for rows.Next() {
		var item searchCandidate
		if err := rows.Scan(&item.memoryID, &item.revisionID, &item.ftsContent, &item.projectionContent, &item.updatedAt, &item.keywordScore); err != nil {
			rows.Close()
			return domain.SearchResponse{}, err
		}
		if math.IsNaN(item.keywordScore) || math.IsInf(item.keywordScore, 0) {
			rows.Close()
			return domain.SearchResponse{}, ErrInvalidSearchQuery
		}
		item.keywordRank = len(candidates) + 1
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return domain.SearchResponse{}, err
	}
	entityCandidates, err := entityKeywordRecall(ctx, tx, params.Query, spaces, params.Limit*10)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	for i := range entityCandidates {
		entityCandidates[i].entityRank = i + 1
	}
	relationCandidates, relationErr := relationKeywordRecall(ctx, tx, params.Query, spaces, params.Limit*10)
	if errors.Is(relationErr, ErrIndexDegraded) {
		response.Degraded = true
		response.DegradationReasons = append(response.DegradationReasons, "relation_unavailable")
	} else if relationErr != nil {
		return domain.SearchResponse{}, relationErr
	}
	all := make(map[string]*searchCandidate, len(candidates)+len(entityCandidates)+len(relationCandidates))
	for i := range candidates {
		item := candidates[i]
		all[item.memoryID+"\x00"+item.revisionID] = &item
	}
	for i := range entityCandidates {
		item := entityCandidates[i]
		key := item.memoryID + "\x00" + item.revisionID
		if existing := all[key]; existing != nil {
			existing.entity = true
			existing.entityRank = item.entityRank
		} else {
			all[key] = &item
		}
	}
	if relationErr == nil {
		for i := range relationCandidates {
			item := relationCandidates[i]
			item.relationRank = i + 1
			key := item.memoryID + "\x00" + item.revisionID
			if existing := all[key]; existing != nil {
				existing.relation = true
				existing.relationRank = item.relationRank
			} else {
				all[key] = &item
			}
		}
	}
	seeds := make([]string, 0, len(all))
	for key := range all {
		memoryID, revisionID, _ := strings.Cut(key, "\x00")
		_ = memoryID
		seeds = append(seeds, revisionID)
	}
	graph, graphErr := r.graphChannelRecall(ctx, tx, params, spaces, nil, seeds, max(100, params.Limit*10))
	if graphErr != nil {
		return domain.SearchResponse{}, graphErr
	}
	response.ResolvedEntities = graph.resolvedEntities
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
	candidates = candidates[:0]
	for _, item := range all {
		if item.keywordRank > 0 {
			item.rrfScore += 1 / float64(60+item.keywordRank)
		}
		if item.entityRank > 0 {
			item.rrfScore += 1 / float64(60+item.entityRank)
		}
		if item.relationRank > 0 {
			item.rrfScore += 1 / float64(60+item.relationRank)
		}
		if item.graphRank > 0 {
			item.rrfScore += 1 / float64(60+item.graphRank)
		}
		usage, err := feedbackUsageScore(ctx, tx, item.revisionID, metadata.feedbackAggregateVersion)
		if err != nil {
			return domain.SearchResponse{}, err
		}
		item.score = item.rrfScore + 0.001*usage
		candidates = append(candidates, *item)
	}
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
		score := candidate.keywordScore
		channels, reason := []string{"keyword"}, "keyword_match"
		var relevance *float64 = &score
		if candidate.entity && candidate.keywordRank > 0 {
			channels, reason = []string{"keyword", "entity"}, "hybrid_fusion"
		} else if candidate.entity {
			channels, reason, relevance = []string{"entity"}, "entity_match", nil
		}
		if candidate.relation {
			channels = append(channels, "relation")
			reason = "relation_match"
			if len(channels) > 1 {
				reason = "hybrid_fusion"
			}
			if candidate.keywordRank == 0 {
				relevance = nil
			}
		}
		if candidate.graphRank > 0 {
			channels = append(channels, "graph")
			if len(channels) == 1 {
				reason = "graph_match"
			} else {
				reason = "hybrid_fusion"
			}
			if candidate.keywordRank == 0 && candidate.entityRank == 0 && candidate.relationRank == 0 {
				relevance = nil
			}
		}
		refs, err := conflictRefs(ctx, tx, memory.MemoryID, memory.CurrentRevision.RevisionID)
		if err != nil {
			return domain.SearchResponse{}, err
		}
		response.Results = append(response.Results, domain.SearchResult{Memory: memory, Rank: len(response.Results) + 1, KeywordScore: score, KeywordRelevance: relevance, MatchedChannels: channels, GraphSignals: candidate.graphSignals, GraphFacts: candidate.graphFacts, GraphPaths: candidate.graphPaths, SelectionReason: reason, ConflictRefs: refs})
		returned[memory.MemoryID] = struct{}{}
		response.CharsUsed += chars
	}
	if hasMore && lastInspected != nil {
		next := encodeSearchCursor(hash, params, ftsVersion, metadata, *lastInspected)
		response.NextCursor = &next
	}
	if err := tx.Commit(); err != nil {
		return domain.SearchResponse{}, err
	}
	return response, nil
}

func candidateBestRank(candidate searchCandidate) int {
	best := 0
	for _, rank := range []int{candidate.keywordRank, candidate.entityRank, candidate.relationRank, candidate.graphRank} {
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
		Mode                   string   `json:"mode"`
		Requested              string   `json:"requested_mode"`
		Semantic               int      `json:"semantic_version"`
		Limit                  int      `json:"limit"`
		MaxChars               int      `json:"max_chars"`
		Evidence               bool     `json:"include_evidence"`
		ExcludeStaleAggregates bool     `json:"exclude_stale_aggregates"`
	}{1, params.Query, spaces, params.IncludeAncestors, types, params.MemoryTypesSet, states, params.ReferenceTime, params.Mode, params.RequestedMode, params.SemanticVersion, params.Limit, params.MaxChars, params.IncludeEvidence, params.ExcludeStaleAggregates}
	encoded, _ := json.Marshal(canonical)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func encodeSearchCursor(hash string, params SearchParams, ftsVersion int, metadata searchMetadata, candidate searchCandidate) string {
	requestedMode := params.RequestedMode
	if requestedMode == "" {
		requestedMode = params.Mode
	}
	value := searchCursor{Version: 3, QueryHash: hash, RequestedMode: requestedMode, ExecutedMode: "keyword", RankingVersion: rankingVersion, FTSVersion: ftsVersion, SemanticVersion: params.SemanticVersion, CorpusGeneration: metadata.corpusGeneration, FeedbackAggregateVersion: metadata.feedbackAggregateVersion, EntityIndexVersion: metadata.entityIndexVersion, RelationIndexVersion: metadata.relationIndexVersion, GraphIndexVersion: metadata.graphIndexVersion, EntityIndexGeneration: metadata.entityIndexGeneration, RelationIndexGeneration: metadata.relationIndexGeneration, GraphIndexGeneration: metadata.graphIndexGeneration, Limit: params.Limit, MaxChars: params.MaxChars, ScoreBits: strconv.FormatUint(math.Float64bits(candidate.score), 16), UpdatedAt: candidate.updatedAt, MemoryID: candidate.memoryID, RevisionID: candidate.revisionID, ReferenceTime: params.ReferenceTime}
	encoded, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeSearchCursor(value string) (searchCursor, error) {
	var cursor searchCursor
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 3 || cursor.QueryHash == "" || cursor.RequestedMode == "" || cursor.ExecutedMode != "keyword" || cursor.RankingVersion != rankingVersion || cursor.FTSVersion < 1 || cursor.UpdatedAt == "" || cursor.MemoryID == "" || cursor.RevisionID == "" || cursor.ReferenceTime == "" {
		return searchCursor{}, ErrInvalidCursor
	}
	bits, err := strconv.ParseUint(cursor.ScoreBits, 16, 64)
	if err != nil || math.IsNaN(math.Float64frombits(bits)) || math.IsInf(math.Float64frombits(bits), 0) {
		return searchCursor{}, ErrInvalidCursor
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.UpdatedAt); err != nil {
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
