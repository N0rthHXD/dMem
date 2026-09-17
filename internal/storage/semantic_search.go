package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/retrieval"
	vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// hybridCandidate is the shared recall candidate used by both the semantic and
// keyword-only paths.
type hybridCandidate = searchCandidate

// searchSemanticHybrid executes the semantic channel (plus keyword and graph
// when requested) and unions candidates for the versioned deterministic
// feature ranker. Requested channels that are unavailable are dropped with an
// explicit degradation reason; a request whose executed set becomes empty
// fails.
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
	executed := append([]string{}, params.EffectiveChannels...)
	reasons := []string{}
	if hasChannel(executed, ChannelSemantic) && !r.semanticQueryable(status, 0) {
		executed = channelsWithout(executed, ChannelSemantic)
		reasons = append(reasons, "semantic_unavailable")
	}
	if hasChannel(executed, ChannelKeyword) && ftsState != "ready" {
		executed = channelsWithout(executed, ChannelKeyword)
		reasons = append(reasons, "keyword_unavailable")
	}
	if len(executed) == 0 {
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	if !hasChannel(executed, ChannelSemantic) {
		return r.semanticKeywordFallback(ctx, params, status.ActiveVersion, reasons)
	}
	if len(params.States) == 0 || params.MemoryTypesSet && len(params.MemoryTypes) == 0 {
		return r.semanticSearchResponse(params, executed, reasons, status, ftsState, ftsVersion, preflightMetadata), nil
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
		if hasChannel(params.Channels, ChannelKeyword) && ftsState == "ready" {
			return r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(reasons, "semantic_policy_denied"))
		}
		return domain.SearchResponse{}, err
	}
	if len(scope) == 0 {
		if hasChannel(params.Channels, ChannelKeyword) && ftsState == "ready" {
			return r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(reasons, "semantic_policy_denied"))
		}
		return domain.SearchResponse{}, ErrPolicyDenied
	}
	if policyGeneration > 0 && runtime.PolicyGeneration != policyGeneration {
		if hasChannel(params.Channels, ChannelKeyword) && ftsState == "ready" {
			return r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(reasons, "semantic_policy_changed"))
		}
		return domain.SearchResponse{}, ErrPolicyDenied
	}

	queryResult, err := runtime.Provider.Embed(ctx, provider.NewEmbeddingQueryRequest(scope[0], "text/plain", "normal", []string{"semantic_query"}, []string{embeddingQueryDocument(params.Query)}))
	if err != nil || len(queryResult.Vectors()) != 1 {
		if hasChannel(params.Channels, ChannelKeyword) {
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
		if hasChannel(params.Channels, ChannelKeyword) {
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
	semanticAvailable := r.semanticQueryable(status, len(queryVector))
	keywordAvailable := ftsState == "ready"
	executed, reasons = append([]string{}, params.EffectiveChannels...), []string{}
	if hasChannel(executed, ChannelSemantic) && !semanticAvailable {
		_ = tx.Rollback()
		if !hasChannel(params.Channels, ChannelKeyword) || !keywordAvailable {
			return domain.SearchResponse{}, ErrIndexDegraded
		}
		return r.semanticKeywordFallback(ctx, params, status.ActiveVersion, []string{"semantic_unavailable"})
	}
	if hasChannel(executed, ChannelKeyword) && !keywordAvailable {
		executed = channelsWithout(executed, ChannelKeyword)
		reasons = append(reasons, "keyword_unavailable")
	}
	spaces, err := searchScope(ctx, tx, params.SpaceIDs, params.IncludeAncestors)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	response := r.semanticSearchResponse(params, executed, reasons, status, ftsState, ftsVersion, metadata)
	recallLimit := selectionRecallLimit(params.Limit)
	all := map[string]*hybridCandidate{}
	keywordStart := time.Now()
	if hasChannel(executed, ChannelKeyword) {
		keyword, err := r.keywordRecall(ctx, tx, params, spaces, recallLimit)
		if err != nil {
			if errors.Is(err, ErrInvalidSearchQuery) {
				return domain.SearchResponse{}, err
			}
			executed = channelsWithout(executed, ChannelKeyword)
			response.ExecutedChannels, response.Degraded = executed, true
			response.DegradationReasons = append(response.DegradationReasons, "keyword_unavailable")
		} else {
			for i, item := range keyword {
				item.keywordRank = i + 1
				copy := item
				all[item.memoryID+"\x00"+item.revisionID] = &copy
			}
		}
	}
	if current := r.emb(); current.Generation != runtime.Generation || current.PolicyGeneration != runtime.PolicyGeneration || current.CompatibilityIdentity != runtime.CompatibilityIdentity {
		if hasChannel(params.Channels, ChannelKeyword) {
			_ = tx.Rollback()
			result, keywordErr := r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(response.DegradationReasons, "semantic_unavailable"))
			if keywordErr != nil {
				return domain.SearchResponse{}, ErrIndexDegraded
			}
			return result, nil
		}
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	queryBlob, err := vec.SerializeFloat32(queryVector)
	if err != nil {
		if hasChannel(params.Channels, ChannelKeyword) {
			_ = tx.Rollback()
			result, keywordErr := r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(response.DegradationReasons, "semantic_unavailable"))
			if keywordErr != nil {
				return domain.SearchResponse{}, ErrIndexDegraded
			}
			return result, nil
		}
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	if r.semanticSearchErr != nil {
		if hasChannel(params.Channels, ChannelKeyword) {
			_ = tx.Rollback()
			result, keywordErr := r.semanticKeywordFallback(ctx, params, status.ActiveVersion, append(response.DegradationReasons, "semantic_unavailable"))
			if keywordErr != nil {
				return domain.SearchResponse{}, ErrIndexDegraded
			}
			return result, nil
		}
		return domain.SearchResponse{}, ErrIndexDegraded
	}
	params.telemetry.markPhase("keyword", keywordStart)
	semanticStart := time.Now()
	semantic, err := r.semanticVectorRecallWindow(ctx, tx, params, spaces, runtime.PolicyGeneration, runtime.CompatibilityIdentity, len(queryVector), queryBlob, recallLimit)
	if err != nil {
		if hasChannel(params.Channels, ChannelKeyword) {
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
		key := item.MemoryID + "\x00" + item.RevisionID
		candidate := all[key]
		if candidate == nil {
			candidate = &hybridCandidate{memoryID: item.MemoryID, revisionID: item.RevisionID, contentHash: item.ContentHash}
			all[key] = candidate
		}
		candidate.semanticRank = i + 1
		candidate.semanticRaw = item.Score
		candidate.confidence = item.Confidence
		candidate.eventStart, candidate.eventEnd = item.EventStart, item.EventEnd
		candidate.validFrom, candidate.validUntil = item.ValidFrom, item.ValidUntil
		candidate.timePrecision, candidate.sourceSpeaker, candidate.language = item.TimePrecision, item.SourceSpeaker, item.Language
		candidate.kind = item.Kind
	}
	calibrateKeywordScores(candidatePointers(all))
	calibrateSemanticScores(r.semanticCalibrationValue(), candidatePointers(all), params.Plan.QuestionType)
	params.telemetry.markPhase("semantic", semanticStart)
	pruneRejectedCandidates(all)
	graphStart := time.Now()
	executed, response, err = r.applyGraphChannel(ctx, tx, params, spaces, metadata, response, executed, all, true, recallLimit)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	params.telemetry.markPhase("graph", graphStart)
	usageScores, err := r.FeedbackUsageScores(ctx, tx, hybridRevisionKeys(all), metadata.feedbackAggregateVersion, searchQueryHash(params))
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
	identities := currentSearchIdentities(ftsVersion, status.ActiveVersion, metadata, r.rankingIdentity(), contextSelectionIdentity(params))
	response.RankingIdentity = identities.Ranking
	response.ContextIdentity = identities.Context
	response.FeedbackIdentity = identities.Feedback
	hash := semanticQueryHash(params)
	start := 0
	if params.Cursor != "" {
		cursor, err := decodeSearchCursor(params.Cursor)
		if err != nil {
			return domain.SearchResponse{}, ErrInvalidCursor
		}
		if !searchCursorMatches(cursor, hash, params, identities) {
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
	var last *searchCandidate
	windowFull := false
	window := selectionWindow(params.Limit)
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
		channels := make([]string, 0, 3)
		reason := ""
		var keyword, semanticScore *float64
		if candidate.keywordRank > 0 {
			channels = append(channels, ChannelKeyword)
			value := candidate.keywordScore
			keyword = &value
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
			return domain.SearchResponse{}, err
		}
		searchResult := domain.SearchResult{Memory: memory, Rank: len(response.Results) + 1, KeywordRelevance: keyword, SemanticScore: semanticScore, MatchedChannels: channels, GraphSignals: candidate.graphSignals, GraphFacts: candidate.graphFacts, GraphPaths: candidate.graphPaths, SelectionReason: reason, ConflictRefs: refs, RankContribution: rankContribution(candidate), ProtectedPrimary: candidate.protected}
		if params.Explain {
			r.applyExplain(candidate, &searchResult, params)
		}
		response.Results = append(response.Results, searchResult)
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
		value := encodeSearchCursor(hash, params, identities, searchCandidate{memoryID: last.memoryID, revisionID: last.revisionID, score: last.score})
		response.NextCursor = &value
	}
	if err := tx.Commit(); err != nil {
		return domain.SearchResponse{}, err
	}
	return response, nil
}

type semanticRecallItem struct {
	MemoryID, RevisionID, ContentHash string
	Kind                              string
	Score                             float64
	Confidence                        float64
	EventStart, EventEnd              *time.Time
	ValidFrom, ValidUntil             *time.Time
	TimePrecision, SourceSpeaker      string
	Language                          string
}

func semanticVectorRecall(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, policyGeneration int, compatibilityIdentity string, dimension int, queryBlob []byte, limit int) ([]semanticRecallItem, error) {
	query := `SELECT p.memory_id,p.revision_id,p.content_hash,rv.kind,vec_distance_cosine(e.vector_blob, ?) AS distance,rv.confidence,rv.event_time_start,rv.event_time_end,rv.time_precision,rv.source_speaker,rv.language,rv.valid_from,rv.valid_until FROM memory_projections p JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state JOIN memory_revisions rv ON rv.memory_id=p.memory_id AND rv.revision_id=p.revision_id JOIN memory_embeddings e ON e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.content_hash=p.content_hash AND e.compatibility_identity=? AND e.dimension=? WHERE p.searchable=1 AND p.current_state IN ('active','contested') AND EXISTS (SELECT 1 FROM embedding_eligibility el WHERE el.memory_id=p.memory_id AND el.revision_id=p.revision_id AND el.policy_generation=? AND el.eligible=1) AND p.space_id IN (` + placeholders(len(spaces)) + `)`
	args := make([]any, 0, len(spaces)+len(params.States)+len(params.MemoryTypes)+4)
	args = append(args, queryBlob, compatibilityIdentity, dimension, policyGeneration)
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
	temporalSQL, temporalArgs := temporalEligibilitySQL(params)
	query += temporalSQL
	args = append(args, temporalArgs...)
	query += ` ORDER BY distance ASC,p.memory_id ASC,p.revision_id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []semanticRecallItem
	for rows.Next() {
		var item semanticRecallItem
		var distance sql.NullFloat64
		var confidence sql.NullFloat64
		var eventStart, eventEnd, timePrecision, sourceSpeaker, language, validFrom, validUntil sql.NullString
		if err := rows.Scan(&item.MemoryID, &item.RevisionID, &item.ContentHash, &item.Kind, &distance, &confidence, &eventStart, &eventEnd, &timePrecision, &sourceSpeaker, &language, &validFrom, &validUntil); err != nil {
			return nil, err
		}
		if !distance.Valid {
			item.Score = 0
		} else {
			item.Score = 1 - distance.Float64
		}
		if confidence.Valid {
			item.Confidence = confidence.Float64
		}
		item.EventStart = nullableTime(eventStart)
		item.EventEnd = nullableTime(eventEnd)
		item.ValidFrom = nullableTime(validFrom)
		item.ValidUntil = nullableTime(validUntil)
		item.TimePrecision = timePrecision.String
		item.SourceSpeaker = sourceSpeaker.String
		item.Language = language.String
		result = append(result, item)
	}
	return result, rows.Err()
}

func semanticSearchStatus(ctx context.Context, tx *sql.Tx) (SemanticStatus, string, int, error) {
	var status SemanticStatus
	var active, dimension sql.NullInt64
	var providerName, model, compatibilityIdentity sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT state,active_version,provider_identity,model_identity,compatibility_identity,dimension,source_revision_count,coalesce(policy_generation,0) FROM semantic_index_metadata WHERE singleton=1`).Scan(&status.State, &active, &providerName, &model, &compatibilityIdentity, &dimension, &status.SourceRevisionCount, &status.PolicyGeneration); err != nil {
		return status, "", 0, err
	}
	status.ActiveVersion, status.Dimension = int(active.Int64), int(dimension.Int64)
	status.Provider, status.Model, status.CompatibilityIdentity = providerName.String, model.String, compatibilityIdentity.String
	var ftsState string
	var ftsVersion int
	if err := tx.QueryRowContext(ctx, `SELECT state,index_version FROM index_metadata WHERE index_name='memory_fts'`).Scan(&ftsState, &ftsVersion); err != nil {
		return status, "", 0, err
	}
	return status, ftsState, ftsVersion, nil
}

func (r *Repository) semanticSearchResponse(params SearchParams, executed []string, reasons []string, status SemanticStatus, ftsState string, ftsVersion int, metadata searchMetadata) domain.SearchResponse {
	identities := currentSearchIdentities(ftsVersion, status.ActiveVersion, metadata, r.rankingIdentity(), contextSelectionIdentity(params))
	response := domain.SearchResponse{Results: []domain.SearchResult{}, RequestedChannels: params.Channels, ExecutedChannels: executed, Degraded: len(reasons) > 0 || status.State == "stale", DegradationReasons: reasons, FTSIndexVersion: ftsVersion, SemanticIndexVersion: status.ActiveVersion, RankingVersion: rankingVersion, QueryPlanIdentity: params.Plan.Identity(), RankingIdentity: identities.Ranking, ContextIdentity: identities.Context, CorpusGeneration: metadata.corpusGeneration, FeedbackAggregateVersion: metadata.feedbackAggregateVersion, FeedbackIdentity: identities.Feedback, GraphIndexVersion: metadata.graphIndexVersion, GraphIndexState: metadata.graphIndexState}
	if status.State == "stale" {
		response.DegradationReasons = append(response.DegradationReasons, "semantic_stale")
	}
	return response
}

// semanticKeywordFallback executes available local recall without recursively
// finalizing context or telemetry.
func (r *Repository) semanticKeywordFallback(ctx context.Context, params SearchParams, semanticVersion int, reasons []string) (domain.SearchResponse, error) {
	keywordParams := params
	keywordParams.EffectiveChannels = channelsWithout(params.EffectiveChannels, ChannelSemantic)
	keywordParams.SemanticVersion = semanticVersion
	result, err := r.searchKeywordGraph(ctx, keywordParams)
	if err != nil {
		return result, err
	}
	result.RequestedChannels = params.Channels
	result.SemanticIndexVersion = semanticVersion
	for _, reason := range result.DegradationReasons {
		if !contains(reasons, reason) {
			reasons = append(reasons, reason)
		}
	}
	result.DegradationReasons = reasons
	result.Degraded = len(reasons) > 0
	return result, nil
}

func hybridRevisionKeys(all map[string]*hybridCandidate) []string {
	keys := make([]string, 0, len(all))
	for key := range all {
		_, revisionID, _ := strings.Cut(key, "\x00")
		keys = append(keys, revisionID)
	}
	return keys
}

// semanticVectorRecallWindow uses the exact cosine path by default. When an
// ANN window hook is configured, its bounded window is reranked by exact cosine
// so approximate recall can never change the final score ordering.
func (r *Repository) semanticVectorRecallWindow(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, policyGeneration int, compatibilityIdentity string, dimension int, queryBlob []byte, limit int) ([]semanticRecallItem, error) {
	if r.semanticWindowRecall == nil {
		return semanticVectorRecall(ctx, tx, params, spaces, policyGeneration, compatibilityIdentity, dimension, queryBlob, limit)
	}
	window, err := r.semanticWindowRecall(ctx, tx, params, spaces, policyGeneration, compatibilityIdentity, dimension, queryBlob, limit)
	if err != nil {
		return nil, err
	}
	if len(window) > limit {
		window = window[:limit]
	}
	return exactRerankWindow(ctx, tx, window, params, spaces, policyGeneration, compatibilityIdentity, dimension, queryBlob)
}

// exactRerankWindow recomputes exact cosine similarity for a bounded ANN window
// and re-applies the revision projections needed by ranking features.
func exactRerankWindow(ctx context.Context, tx *sql.Tx, window []semanticRecallItem, params SearchParams, spaces []string, policyGeneration int, compatibilityIdentity string, dimension int, queryBlob []byte) ([]semanticRecallItem, error) {
	result := make([]semanticRecallItem, 0, len(window))
	for _, item := range window {
		var distance sql.NullFloat64
		var confidence sql.NullFloat64
		var eventStart, eventEnd, timePrecision, sourceSpeaker, language, validFrom, validUntil sql.NullString
		query := `SELECT vec_distance_cosine(e.vector_blob, ?),rv.kind,rv.confidence,rv.event_time_start,rv.event_time_end,rv.time_precision,rv.source_speaker,rv.language,rv.valid_from,rv.valid_until FROM memory_projections p JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state JOIN memory_revisions rv ON rv.memory_id=p.memory_id AND rv.revision_id=p.revision_id JOIN memory_embeddings e ON e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.content_hash=p.content_hash AND e.compatibility_identity=? AND e.dimension=? WHERE p.memory_id=? AND p.revision_id=? AND p.searchable=1 AND EXISTS (SELECT 1 FROM embedding_eligibility el WHERE el.memory_id=p.memory_id AND el.revision_id=p.revision_id AND el.policy_generation=? AND el.eligible=1) AND p.space_id IN (` + placeholders(len(spaces)) + `) AND p.current_state IN (` + placeholders(len(params.States)) + `)`
		args := []any{queryBlob, compatibilityIdentity, dimension, item.MemoryID, item.RevisionID, policyGeneration}
		args = append(args, appendStrings2(nil, spaces)...)
		args = append(args, appendStrings2(nil, params.States)...)
		if params.MemoryTypesSet {
			query += ` AND p.memory_type IN (` + placeholders(len(params.MemoryTypes)) + `)`
			args = append(args, appendStrings2(nil, params.MemoryTypes)...)
		}
		query += ` AND (p.valid_from IS NULL OR p.valid_from<=?) AND (p.valid_until IS NULL OR p.valid_until>?)`
		args = append(args, params.ReferenceTime, params.ReferenceTime)
		temporalSQL, temporalArgs := temporalEligibilitySQL(params)
		query += temporalSQL
		args = append(args, temporalArgs...)
		err := tx.QueryRowContext(ctx, query, args...).Scan(&distance, &item.Kind, &confidence, &eventStart, &eventEnd, &timePrecision, &sourceSpeaker, &language, &validFrom, &validUntil)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !distance.Valid {
			item.Score = 0
		} else {
			item.Score = 1 - distance.Float64
		}
		if confidence.Valid {
			item.Confidence = confidence.Float64
		}
		item.EventStart = nullableTime(eventStart)
		item.EventEnd = nullableTime(eventEnd)
		item.ValidFrom = nullableTime(validFrom)
		item.ValidUntil = nullableTime(validUntil)
		item.TimePrecision = timePrecision.String
		item.SourceSpeaker = sourceSpeaker.String
		item.Language = language.String
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		if result[i].MemoryID != result[j].MemoryID {
			return result[i].MemoryID < result[j].MemoryID
		}
		return result[i].RevisionID < result[j].RevisionID
	})
	return result, nil
}

// semanticCalibrationValue returns the configured semantic calibration.
func (r *Repository) semanticCalibrationValue() retrieval.Calibration {
	_, calibration, _ := r.retrievalCalibration()
	return calibration
}

func semanticQueryHash(params SearchParams) string {
	value := searchQueryHash(params)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
