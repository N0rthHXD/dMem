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
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/vector"
)

const rankingVersion = "rrf-v2"

// Exact cosine search has no native cutoff; zero and negative similarity are not relevant recalls.
const minimumSemanticScore = 0.2

type hybridCandidate struct {
	memoryID, revisionID, contentHash                   string
	keywordScore, semanticScore                         float64
	keywordRank, semanticRank, entityRank, relationRank int
	rrfScore, score                                     float64
}

type hybridCursor struct {
	Version                                                int `json:"v"`
	QueryHash, RequestedMode, ExecutedMode, RankingVersion string
	FTSVersion, SemanticVersion, Limit, MaxChars           int
	CorpusGeneration, FeedbackAggregateVersion             int64
	EntityIndexVersion, RelationIndexVersion               int64
	ReferenceTime, ScoreBits, MemoryID, RevisionID         string
}

func (r *Repository) searchSemanticHybrid(ctx context.Context, params SearchParams) (domain.SearchResponse, error) {
	if params.ReferenceTime == "" {
		params.ReferenceTime = formatTime(r.runtime.Now())
	}
	preflight, err := r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.SearchResponse{}, err
	}
	status, ftsState, ftsVersion, err := semanticSearchStatus(ctx, preflight)
	if err != nil {
		_ = preflight.Rollback()
		return domain.SearchResponse{}, err
	}
	preflightMetadata, err := readSearchMetadata(ctx, preflight)
	if err != nil {
		_ = preflight.Rollback()
		return domain.SearchResponse{}, err
	}
	if err := preflight.Commit(); err != nil {
		return domain.SearchResponse{}, err
	}
	runtime := r.emb()
	semanticAvailable := r.semanticQueryable(status)
	keywordAvailable := ftsState == "ready"
	executed := params.Mode
	reasons := []string{}
	if params.Mode == "semantic" && !semanticAvailable {
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	if params.Mode == "hybrid" {
		if !semanticAvailable {
			executed, reasons = "keyword", append(reasons, "semantic_unavailable")
		}
		if !keywordAvailable {
			executed, reasons = "semantic", append(reasons, "keyword_unavailable")
		}
		if !semanticAvailable && !keywordAvailable {
			return domain.SearchResponse{}, ErrIndexDegraded
		}
	}
	if executed == "keyword" {
		return r.semanticKeywordFallback(ctx, params, status.ActiveVersion, reasons)
	}
	if len(params.States) == 0 || params.MemoryTypesSet && len(params.MemoryTypes) == 0 {
		return semanticSearchResponse(params, executed, reasons, status, ftsState, ftsVersion, preflightMetadata), nil
	}
	authorization, err := r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.SearchResponse{}, err
	}
	scope, err := searchScope(ctx, authorization, params.SpaceIDs, params.IncludeAncestors)
	policyGeneration := 0
	if err == nil {
		policyGeneration, err = r.authorizeSemanticQueryScope(ctx, authorization, scope)
	}
	if closeErr := authorization.Rollback(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		if executed == "hybrid" && keywordAvailable {
			return r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(reasons, "semantic_policy_denied"))
		}
		return domain.SearchResponse{}, err
	}
	if len(scope) == 0 {
		if executed == "hybrid" && keywordAvailable {
			return r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(reasons, "semantic_policy_denied"))
		}
		return domain.SearchResponse{}, ErrPolicyDenied
	}
	if policyGeneration > 0 && runtime.PolicyGeneration != policyGeneration {
		if executed == "hybrid" && keywordAvailable {
			return r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(reasons, "semantic_policy_changed"))
		}
		return domain.SearchResponse{}, ErrPolicyDenied
	}

	queryResult, err := runtime.Provider.Embed(ctx, provider.NewEmbeddingQueryRequest(scope[0], "text/plain", "normal", []string{"semantic_query"}, []string{params.Query}))
	if err != nil || len(queryResult.Vectors()) != 1 {
		if executed == "hybrid" {
			result, keywordErr := r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(reasons, "semantic_unavailable"))
			if keywordErr != nil {
				return domain.SearchResponse{}, ErrIndexDegraded
			}
			return result, nil
		}
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	queryVector, err := validEmbedding(queryResult.Vectors()[0])
	if err != nil {
		if executed == "hybrid" {
			result, keywordErr := r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(reasons, "semantic_unavailable"))
			if keywordErr != nil {
				return domain.SearchResponse{}, ErrIndexDegraded
			}
			return result, nil
		}
		return domain.SearchResponse{}, ErrIndexDegraded
	}

	tx, err := r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.SearchResponse{}, err
	}
	defer tx.Rollback()
	status, ftsState, ftsVersion, err = semanticSearchStatus(ctx, tx)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	metadata, err := readSearchMetadata(ctx, tx)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	semanticAvailable = r.semanticQueryable(status) && len(queryVector) == status.Dimension
	keywordAvailable = ftsState == "ready"
	executed, reasons = params.Mode, []string{}
	if params.Mode == "semantic" && !semanticAvailable {
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	if params.Mode == "hybrid" {
		if !semanticAvailable {
			_ = tx.Rollback()
			if !keywordAvailable {
				return domain.SearchResponse{}, ErrIndexDegraded
			}
			return r.semanticKeywordFallback(ctx, params, status.ActiveVersion, []string{"semantic_unavailable"})
		}
		if !keywordAvailable {
			executed, reasons = "semantic", append(reasons, "keyword_unavailable")
		}
	}
	spaces, err := searchScope(ctx, tx, params.SpaceIDs, params.IncludeAncestors)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	response := semanticSearchResponse(params, executed, reasons, status, ftsState, ftsVersion, metadata)
	recallLimit := max(100, params.Limit*10)
	all := map[string]*hybridCandidate{}
	if executed == "hybrid" {
		keyword, err := semanticKeywordRecall(ctx, tx, params, spaces, recallLimit)
		if err != nil {
			executed, response.ExecutedMode, response.Degraded = "semantic", "semantic", true
			response.DegradationReasons = append(response.DegradationReasons, "keyword_unavailable")
		} else {
			for i, item := range keyword {
				item.keywordRank = i + 1
				copy := item
				all[item.memoryID+"\x00"+item.revisionID] = &copy
			}
		}
	}
	eligible, err := semanticVectorEligibility(ctx, tx, params, spaces, runtime.PolicyGeneration)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	if current := r.emb(); current.Generation != runtime.Generation || current.PolicyGeneration != runtime.PolicyGeneration || current.CompatibilityIdentity != runtime.CompatibilityIdentity {
		if executed == "hybrid" {
			_ = tx.Rollback()
			result, keywordErr := r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(response.DegradationReasons, "semantic_unavailable"))
			if keywordErr != nil {
				return domain.SearchResponse{}, ErrIndexDegraded
			}
			return result, nil
		}
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	semantic, err := runtime.Index.Search(queryVector, spaces, eligible, recallLimit, status.ActiveVersion)
	if err != nil {
		if executed == "hybrid" {
			_ = tx.Rollback()
			result, keywordErr := r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(response.DegradationReasons, "semantic_unavailable"))
			if keywordErr != nil {
				return domain.SearchResponse{}, ErrIndexDegraded
			}
			return result, nil
		}
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	for i, item := range semantic {
		if item.Score < minimumSemanticScore {
			continue
		}
		key := item.Metadata.MemoryID + "\x00" + item.Metadata.RevisionID
		candidate := all[key]
		if candidate == nil {
			candidate = &hybridCandidate{memoryID: item.Metadata.MemoryID, revisionID: item.Metadata.RevisionID, contentHash: item.Metadata.ContentHash}
			all[key] = candidate
		}
		candidate.semanticRank, candidate.semanticScore = i+1, item.Score
	}
	entity, err := entityRecall(ctx, tx, params.Query, spaces, recallLimit)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	for i, item := range entity {
		key := item.memoryID + "\x00" + item.revisionID
		candidate := all[key]
		if candidate == nil {
			copy := item
			candidate = &copy
			all[key] = candidate
		}
		candidate.entityRank = i + 1
	}
	relation, relationErr := relationRecall(ctx, tx, params.Query, spaces, recallLimit)
	if errors.Is(relationErr, ErrIndexDegraded) {
		response.Degraded = true
		response.DegradationReasons = append(response.DegradationReasons, "relation_unavailable")
	} else if relationErr != nil {
		return domain.SearchResponse{}, relationErr
	}
	if relationErr == nil {
		for i, item := range relation {
			key := item.memoryID + "\x00" + item.revisionID
			candidate := all[key]
			if candidate == nil {
				copy := item
				candidate = &copy
				all[key] = candidate
			}
			candidate.relationRank = i + 1
		}
	}
	list := make([]hybridCandidate, 0, len(all))
	for _, item := range all {
		if item.keywordRank > 0 {
			item.rrfScore += 1 / float64(60+item.keywordRank)
		}
		if item.semanticRank > 0 {
			item.rrfScore += 1 / float64(60+item.semanticRank)
		}
		if item.entityRank > 0 {
			item.rrfScore += 1 / float64(60+item.entityRank)
		}
		if item.relationRank > 0 {
			item.rrfScore += 1 / float64(60+item.relationRank)
		}
		usage, err := feedbackUsageScore(ctx, tx, item.revisionID, metadata.feedbackAggregateVersion)
		if err != nil {
			return domain.SearchResponse{}, err
		}
		item.score = item.rrfScore + 0.001*usage
		list = append(list, *item)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		if list[i].rrfScore != list[j].rrfScore {
			return list[i].rrfScore > list[j].rrfScore
		}
		bi, bj := bestRank(list[i]), bestRank(list[j])
		if bi != bj {
			return bi < bj
		}
		if list[i].memoryID != list[j].memoryID {
			return list[i].memoryID < list[j].memoryID
		}
		return list[i].revisionID < list[j].revisionID
	})
	start := 0
	if params.Cursor != "" {
		cursor, err := decodeHybridCursor(params.Cursor)
		if err != nil {
			return domain.SearchResponse{}, ErrInvalidCursor
		}
		if cursor.ReferenceTime != params.ReferenceTime || cursor.QueryHash != semanticQueryHash(params) || cursor.RequestedMode != params.Mode || cursor.ExecutedMode != executed || cursor.RankingVersion != rankingVersion || cursor.FTSVersion != ftsVersion || cursor.SemanticVersion != status.ActiveVersion || cursor.CorpusGeneration != metadata.corpusGeneration || cursor.FeedbackAggregateVersion != metadata.feedbackAggregateVersion || cursor.EntityIndexVersion != metadata.entityIndexVersion || cursor.RelationIndexVersion != metadata.relationIndexVersion || cursor.Limit != params.Limit || cursor.MaxChars != params.MaxChars {
			return domain.SearchResponse{}, ErrInvalidCursor
		}
		found := false
		for i := range list {
			if list[i].memoryID == cursor.MemoryID && list[i].revisionID == cursor.RevisionID && strconv.FormatUint(math.Float64bits(list[i].score), 16) == cursor.ScoreBits {
				start, found = i+1, true
				break
			}
		}
		if !found {
			return domain.SearchResponse{}, ErrInvalidCursor
		}
	}
	hash := semanticQueryHash(params)
	var last *hybridCandidate
	for i := start; i < len(list); i++ {
		candidate := list[i]
		memory, err := getMemory(ctx, tx, candidate.memoryID, false)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return domain.SearchResponse{}, err
		}
		if memory.CurrentRevision.RevisionID != candidate.revisionID || !searchMemoryCurrent(memory, spaces, params) {
			continue
		}
		var projectionHash string
		if err := tx.QueryRowContext(ctx, `SELECT content_hash FROM memory_projections WHERE memory_id=? AND revision_id=? AND searchable=1`, candidate.memoryID, candidate.revisionID).Scan(&projectionHash); err != nil || candidate.semanticRank > 0 && projectionHash != candidate.contentHash {
			continue
		}
		chars := utf8.RuneCountInString(memory.CurrentRevision.Content)
		if response.CharsUsed+chars > params.MaxChars {
			response.Truncated = true
			if len(response.Results) > 0 && chars <= params.MaxChars {
				break
			}
			continue
		}
		if !params.IncludeEvidence {
			memory.CurrentRevision.Evidence = nil
		}
		channels := []string{}
		reason := "semantic_match"
		var keyword, semanticScore *float64
		if candidate.keywordRank > 0 {
			channels = append(channels, "keyword")
			value := candidate.keywordScore
			keyword = &value
			reason = "keyword_match"
		}
		if candidate.semanticRank > 0 {
			channels = append(channels, "semantic")
			value := candidate.semanticScore
			semanticScore = &value
		}
		if candidate.entityRank > 0 {
			channels = append(channels, "entity")
			if len(channels) == 1 {
				reason = "entity_match"
			} else {
				reason = "hybrid_fusion"
			}
		}
		if candidate.relationRank > 0 {
			channels = append(channels, "relation")
			if len(channels) == 1 {
				reason = "relation_match"
			} else {
				reason = "hybrid_fusion"
			}
		}
		if len(channels) >= 2 {
			reason = "hybrid_fusion"
		}
		refs, err := conflictRefs(ctx, tx, memory.MemoryID, memory.CurrentRevision.RevisionID)
		if err != nil {
			return domain.SearchResponse{}, err
		}
		response.Results = append(response.Results, domain.SearchResult{Memory: memory, Rank: len(response.Results) + 1, KeywordRelevance: keyword, SemanticScore: semanticScore, MatchedChannels: channels, SelectionReason: reason, ConflictRefs: refs})
		response.CharsUsed += chars
		copy := candidate
		last = &copy
		if len(response.Results) == params.Limit {
			if i+1 < len(list) {
				response.Truncated = true
			}
			break
		}
	}
	if response.Truncated && last != nil {
		value := encodeHybridCursor(hash, params, executed, ftsVersion, status.ActiveVersion, metadata, *last)
		response.NextCursor = &value
	}
	if err := tx.Commit(); err != nil {
		return domain.SearchResponse{}, err
	}
	return response, nil
}

func feedbackUsageScore(ctx context.Context, tx *sql.Tx, revisionID string, generation int64) (float64, error) {
	var score float64
	err := tx.QueryRowContext(ctx, `SELECT bounded_usage_score FROM feedback_aggregates WHERE revision_id=? AND algorithm_version='v1' AND generation=? AND state='current'`, revisionID, generation).Scan(&score)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return score, err
}

func semanticKeywordRecall(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, limit int) ([]hybridCandidate, error) {
	ftsQuery, searchable := compileKeywordQuery(params.Query)
	if !searchable {
		return nil, nil
	}
	query := `SELECT f.memory_id,f.revision_id,p.content_hash,bm25(memory_fts) FROM memory_fts f JOIN memory_projections p ON p.memory_id=f.memory_id AND p.revision_id=f.revision_id JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state WHERE memory_fts MATCH ? AND f.content=p.display_content AND p.searchable=1 AND p.current_state IN ('active','contested') AND p.space_id IN (` + placeholders(len(spaces)) + `)`
	args := []any{ftsQuery}
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
	query += ` AND (p.valid_from IS NULL OR p.valid_from<=?) AND (p.valid_until IS NULL OR p.valid_until>?) ORDER BY bm25(memory_fts),f.memory_id LIMIT ?`
	args = append(args, params.ReferenceTime, params.ReferenceTime)
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []hybridCandidate
	for rows.Next() {
		var item hybridCandidate
		if err := rows.Scan(&item.memoryID, &item.revisionID, &item.contentHash, &item.keywordScore); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func semanticVectorEligibility(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, policyGeneration int) ([]vector.Identity, error) {
	query := `SELECT p.memory_id,p.revision_id,p.content_hash FROM memory_projections p JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state WHERE p.searchable=1 AND p.current_state IN ('active','contested') AND EXISTS (SELECT 1 FROM embedding_eligibility e WHERE e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.policy_generation=? AND e.eligible=1) AND p.space_id IN (` + placeholders(len(spaces)) + `)`
	args := make([]any, 0, len(spaces)+len(params.States)+len(params.MemoryTypes)+3)
	args = append(args, policyGeneration)
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
	query += ` AND (p.valid_from IS NULL OR p.valid_from<=?) AND (p.valid_until IS NULL OR p.valid_until>?) ORDER BY p.memory_id,p.revision_id`
	args = append(args, params.ReferenceTime, params.ReferenceTime)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []vector.Identity
	for rows.Next() {
		var identity vector.Identity
		if err := rows.Scan(&identity.MemoryID, &identity.RevisionID, &identity.ContentHash); err != nil {
			return nil, err
		}
		result = append(result, identity)
	}
	return result, rows.Err()
}

func semanticSearchStatus(ctx context.Context, tx *sql.Tx) (SemanticStatus, string, int, error) {
	var status SemanticStatus
	var active, dimension sql.NullInt64
	var providerName, model sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT state,active_version,provider_identity,model_identity,dimension,source_revision_count,coalesce(policy_generation,0) FROM semantic_index_metadata WHERE singleton=1`).Scan(&status.State, &active, &providerName, &model, &dimension, &status.SourceRevisionCount, &status.PolicyGeneration); err != nil {
		return status, "", 0, err
	}
	status.ActiveVersion, status.Dimension = int(active.Int64), int(dimension.Int64)
	status.Provider, status.Model = providerName.String, model.String
	var ftsState string
	var ftsVersion int
	if err := tx.QueryRowContext(ctx, `SELECT state,index_version FROM index_metadata WHERE index_name='memory_fts'`).Scan(&ftsState, &ftsVersion); err != nil {
		return status, "", 0, err
	}
	return status, ftsState, ftsVersion, nil
}

func semanticSearchResponse(params SearchParams, executed string, reasons []string, status SemanticStatus, ftsState string, ftsVersion int, metadata searchMetadata) domain.SearchResponse {
	response := domain.SearchResponse{Results: []domain.SearchResult{}, IndexState: ftsState, RequestedMode: params.Mode, ExecutedMode: executed, Degraded: len(reasons) > 0 || status.State == "stale", DegradationReasons: reasons, FTSIndexVersion: ftsVersion, SemanticIndexVersion: status.ActiveVersion, RankingVersion: rankingVersion, CorpusGeneration: metadata.corpusGeneration, FeedbackAggregateVersion: metadata.feedbackAggregateVersion, EntityIndexVersion: metadata.entityIndexVersion, RelationIndexVersion: metadata.relationIndexVersion}
	if status.State == "stale" {
		response.DegradationReasons = append(response.DegradationReasons, "semantic_stale")
	}
	return response
}

func (r *Repository) semanticKeywordFallback(ctx context.Context, params SearchParams, semanticVersion int, reasons []string) (domain.SearchResponse, error) {
	keywordParams := withMode(params, "keyword")
	keywordParams.RequestedMode, keywordParams.SemanticVersion = params.Mode, semanticVersion
	result, err := r.SearchMemories(ctx, keywordParams)
	if err != nil {
		return result, err
	}
	result.RequestedMode, result.ExecutedMode = params.Mode, "keyword"
	result.Degraded, result.DegradationReasons = len(reasons) > 0, reasons
	result.SemanticIndexVersion = semanticVersion
	return result, nil
}

func withMode(params SearchParams, mode string) SearchParams { params.Mode = mode; return params }
func bestRank(c hybridCandidate) int {
	best := 0
	for _, rank := range []int{c.keywordRank, c.semanticRank, c.entityRank, c.relationRank} {
		if rank > 0 && (best == 0 || rank < best) {
			best = rank
		}
	}
	return best
}
func semanticQueryHash(params SearchParams) string {
	value := searchQueryHash(params)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func encodeHybridCursor(hash string, p SearchParams, executed string, ftsVersion, semanticVersion int, metadata searchMetadata, c hybridCandidate) string {
	value := hybridCursor{Version: 3, QueryHash: hash, RequestedMode: p.Mode, ExecutedMode: executed, RankingVersion: rankingVersion, FTSVersion: ftsVersion, SemanticVersion: semanticVersion, CorpusGeneration: metadata.corpusGeneration, FeedbackAggregateVersion: metadata.feedbackAggregateVersion, EntityIndexVersion: metadata.entityIndexVersion, RelationIndexVersion: metadata.relationIndexVersion, Limit: p.Limit, MaxChars: p.MaxChars, ReferenceTime: p.ReferenceTime, ScoreBits: strconv.FormatUint(math.Float64bits(c.score), 16), MemoryID: c.memoryID, RevisionID: c.revisionID}
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeHybridCursor(value string) (hybridCursor, error) {
	var cursor hybridCursor
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Version != 3 || cursor.QueryHash == "" || cursor.MemoryID == "" || cursor.RevisionID == "" || cursor.ReferenceTime == "" {
		return cursor, ErrInvalidCursor
	}
	if _, err = time.Parse(time.RFC3339Nano, cursor.ReferenceTime); err != nil {
		return cursor, ErrInvalidCursor
	}
	bits, err := strconv.ParseUint(cursor.ScoreBits, 16, 64)
	if err != nil || math.IsNaN(math.Float64frombits(bits)) || math.IsInf(math.Float64frombits(bits), 0) {
		return cursor, ErrInvalidCursor
	}
	return cursor, nil
}
