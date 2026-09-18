package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/provider"
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
	if hasChannel(executed, ChannelSemantic) && !r.semanticQueryable(status, 0) {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_unavailable"}
	}
	if hasChannel(executed, ChannelKeyword) && ftsState != "ready" {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelKeyword, Reason: "keyword_unavailable"}
	}
	if len(params.States) == 0 || params.MemoryTypesSet && len(params.MemoryTypes) == 0 {
		response := r.semanticSearchResponse(params, executed, status, ftsState, ftsVersion, preflightMetadata)
		response.ContextSelectionIdentity = contextSelectionIdentity(params)
		response.Context = domain.ContextPackage{Items: []domain.ContextItem{}}
		return response, nil
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
		if errors.Is(err, ErrPolicyDenied) {
			return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_policy_denied", Denied: true}
		}
		return domain.SearchResponse{}, err
	}
	if len(scope) == 0 {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_policy_denied", Denied: true}
	}
	if policyGeneration > 0 && runtime.PolicyGeneration != policyGeneration {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_policy_changed", Denied: true}
	}

	queryResult, err := runtime.Provider.Embed(ctx, provider.NewEmbeddingQueryRequest(scope[0], "text/plain", "normal", []string{"semantic_query"}, []string{embeddingQueryDocument(params.Query)}))
	if err != nil || len(queryResult.Vectors()) != 1 {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_unavailable"}
	}
	queryVector, err := validEmbedding(queryResult.Vectors()[0])
	if err != nil {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_unavailable"}
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
	executed = append([]string{}, params.EffectiveChannels...)
	if hasChannel(executed, ChannelSemantic) && !semanticAvailable {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_unavailable"}
	}
	if hasChannel(executed, ChannelKeyword) && !keywordAvailable {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelKeyword, Reason: "keyword_unavailable"}
	}
	spaces, err := searchScope(ctx, tx, params.SpaceIDs, params.IncludeAncestors)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	response := r.semanticSearchResponse(params, executed, status, ftsState, ftsVersion, metadata)
	recallLimit := selectionRecallLimit(params.Limit)
	all := map[string]*hybridCandidate{}
	keywordStart := time.Now()
	if hasChannel(executed, ChannelKeyword) {
		keyword, err := r.keywordRecall(ctx, tx, params, spaces, recallLimit)
		if err != nil {
			if errors.Is(err, ErrInvalidSearchQuery) {
				return domain.SearchResponse{}, err
			}
			return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelKeyword, Reason: "keyword_unavailable"}
		}
		for i, item := range keyword {
			item.keywordRank = i + 1
			copy := item
			all[item.memoryID+"\x00"+item.revisionID] = &copy
		}
	}
	if current := r.emb(); current.Generation != runtime.Generation || current.PolicyGeneration != runtime.PolicyGeneration || current.CompatibilityIdentity != runtime.CompatibilityIdentity {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_unavailable"}
	}
	queryBlob, err := vec.SerializeFloat32(queryVector)
	if err != nil {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_unavailable"}
	}
	if r.semanticSearchErr != nil {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_unavailable"}
	}
	params.telemetry.MarkPhase("keyword", keywordStart)
	semanticStart := time.Now()
	semantic, err := r.semanticVectorRecallWindow(ctx, tx, params, spaces, runtime.PolicyGeneration, runtime.CompatibilityIdentity, len(queryVector), queryBlob, recallLimit)
	if err != nil {
		return domain.SearchResponse{}, &gwretrieval.CapabilityUnavailable{Capability: ChannelSemantic, Reason: "semantic_unavailable"}
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
	weights, calibration, _ := r.retrievalCalibration()
	params.telemetry.MarkPhase("semantic", semanticStart)
	identities := currentSearchIdentities(ftsVersion, status.ActiveVersion, metadata, r.rankingIdentity(), contextSelectionIdentity(params))
	hash := semanticQueryHash(params)
	var responseSelectionState contextSelectionState
	var contextSelectionStart time.Time
	err = params.processor.Orchestrate(ctx, gwretrieval.PipelineInput{
		Params: gatewayRecallParams(params), CursorBinding: gatewayCursorBinding(identities),
		Primary: gwretrieval.PrimaryInput{
			Candidates: toGatewayCandidates(candidatePointers(all)), Calibration: calibration, Weights: weights,
			QuestionType: params.Plan.QuestionType, SemanticTop: 10, KeywordTop: 10, MaxSeeds: 15,
		},
		ExpandGraph: func(prepared gwretrieval.PrimaryOutput) ([]gwretrieval.Candidate, error) {
			graphStart := time.Now()
			all = candidateMap(fromGatewayCandidates(prepared.Candidates))
			executed, response, err = r.applyGraphChannel(ctx, tx, params, spaces, metadata, response, executed, all, prepared.Seeds, prepared.PrimaryKeys, recallLimit)
			params.telemetry.MarkPhase("graph", graphStart)
			return toGatewayCandidates(candidatePointers(all)), err
		},
		RankingInput: func(candidates []gwretrieval.Candidate) (gwretrieval.RankInput, error) {
			list := fromGatewayCandidates(candidates)
			if err := hydrateRankingProjections(ctx, tx, list); err != nil {
				return gwretrieval.RankInput{}, err
			}
			all = candidateMap(list)
			params.telemetry.ObserveCandidates(toGatewayCandidates(candidatePointers(all)))
			usageScores, err := r.FeedbackUsageScores(ctx, tx, hybridRevisionKeys(all), metadata.feedbackAggregateVersion, searchQueryHash(params))
			return gwretrieval.RankInput{Candidates: toGatewayCandidates(list), Params: gatewayRecallParams(params), Weights: weights, UsageScores: usageScores, Terms: params.planTerms()}, err
		},
		Materialize: func(ranked gwretrieval.RankOutput) (gwretrieval.SelectionInput, error) {
			for i := 0; i < ranked.TemporalRemoved; i++ {
				params.telemetry.AddGate("temporal_filter", 1)
			}
			list := fromGatewayCandidates(ranked.Candidates)
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

func (r *Repository) semanticSearchResponse(params SearchParams, executed []string, status SemanticStatus, ftsState string, ftsVersion int, metadata searchMetadata) domain.SearchResponse {
	identities := currentSearchIdentities(ftsVersion, status.ActiveVersion, metadata, r.rankingIdentity(), contextSelectionIdentity(params))
	return domain.SearchResponse{Results: []domain.SearchResult{}, RequestedChannels: params.Channels, ExecutedChannels: executed, DegradationReasons: []string{}, FTSIndexVersion: ftsVersion, SemanticIndexVersion: status.ActiveVersion, RankingVersion: rankingVersion, QueryPlanIdentity: params.Plan.Identity(), RankingIdentity: identities.Ranking, ContextIdentity: identities.Context, CorpusGeneration: metadata.corpusGeneration, FeedbackAggregateVersion: metadata.feedbackAggregateVersion, FeedbackIdentity: identities.Feedback, GraphIndexVersion: metadata.graphIndexVersion, GraphIndexState: metadata.graphIndexState}
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

func semanticQueryHash(params SearchParams) string {
	value := searchQueryHash(params)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
