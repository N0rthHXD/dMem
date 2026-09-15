package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/application"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

// Retrieval is channel-based: a request selects any subset of the three
// top-level channels instead of a coarse execution mode.
var validChannels = map[string]bool{"keyword": true, "semantic": true, "graph": true}

func (h *Handler) rebuildFTS(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	if !ok {
		return
	}
	identity := writeIdentity(caller, key, requestID, r.Method, "/v1/indexes/fts/rebuild", struct{}{})
	result, replay, err := h.repository.CreateFTSRebuildJob(r.Context(), identity)
	if err != nil {
		if errors.Is(err, storage.ErrJobAlreadyActive) {
			h.writeError(w, http.StatusConflict, "job_already_active", "an active FTS rebuild job already exists", requestID, "", false, map[string]any{"job_id": result.JobID})
			return
		}
		h.repositoryError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
	h.writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) rebuildSemantic(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	if !ok {
		return
	}
	identity := writeIdentity(caller, key, requestID, r.Method, "/v1/indexes/semantic/rebuild", struct{}{})
	result, replay, err := h.repository.CreateSemanticRebuildJob(r.Context(), identity)
	if err != nil {
		if errors.Is(err, storage.ErrJobAlreadyActive) {
			h.writeError(w, http.StatusConflict, "job_already_active", "an active semantic rebuild job already exists", requestID, "", false, map[string]any{"job_id": result.JobID})
			return
		}
		h.repositoryError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
	h.writeJSON(w, http.StatusAccepted, result)
}

func validValues(values []string, allowed map[string]bool) bool {
	for _, value := range values {
		if !allowed[value] {
			return false
		}
	}
	return true
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func decodeRange(raw json.RawMessage, target *int, minimum, maximum int, field string) error {
	if raw == nil {
		return nil
	}
	if isNull(raw) || json.Unmarshal(raw, target) != nil || *target < minimum || *target > maximum {
		return errors.New(field + " is out of range")
	}
	return nil
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func recallResponse(operationID string, query application.RecallQuery, result domain.SearchResponse) domain.RecallResponse {
	items := make([]domain.ContextItem, 0, len(result.Results))
	degradationReasons := append([]string{}, result.DegradationReasons...)
	tokensUsed := 0
	for _, candidate := range result.Results {
		revision := candidate.Memory.CurrentRevision
		tokens := estimatedContextTokens(revision.DisplayContent)
		item := domain.ContextItem{
			Rank: candidate.Rank, MemoryID: candidate.Memory.MemoryID, RevisionID: revision.RevisionID,
			Kind: revision.Kind, DisplayContent: revision.DisplayContent,
			EventTimeStart: revision.EventTimeStart, EventTimeEnd: revision.EventTimeEnd,
			ValidFrom: revision.ValidFrom, ValidUntil: revision.ValidUntil, SourceSpeaker: revision.SourceSpeaker,
			TimePrecision: revision.TimePrecision, TimeSource: revision.TimeSource,
			Evidence: revision.Evidence, Confidence: revision.Confidence,
			LifecycleState: candidate.Memory.CurrentState, EstimatedTokens: tokens,
		}
		if query.Explain {
			item.Explanation = &domain.RecallExplanation{
				MatchedSignals:              append([]string(nil), candidate.MatchedChannels...),
				SelectionReason:             candidate.SelectionReason,
				Features:                    candidate.RankingFeatures,
				GraphScore:                  candidate.GraphScore,
				SemanticRawScore:            candidate.SemanticRawScore,
				SemanticPercentile:          candidate.SemanticPercentile,
				SemanticRejectionReason:     candidate.SemanticRejectionReason,
				SemanticCalibrationIdentity: candidate.SemanticCalibrationIdentity,
				TemporalPrecision:           candidate.TemporalPrecision,
				TemporalSourceWording:       candidate.TemporalSourceWording,
				TemporalTimeSource:          candidate.TemporalTimeSource,
				QueryTimeStart:              candidate.QueryTimeStart,
				QueryTimeEnd:                candidate.QueryTimeEnd,
				ReferenceTimeAnchor:         candidate.ReferenceTimeAnchor,
				RankContribution:            candidate.RankContribution,
				GraphSignals:                candidate.GraphSignals,
				GraphFacts:                  candidate.GraphFacts,
				GraphPaths:                  candidate.GraphPaths,
				KeywordRawBM25:              candidate.KeywordRawBM25,
				KeywordMatchedFields:        candidate.KeywordMatchedFields,
				KeywordExactMatch:           candidate.KeywordExactMatch,
				KeywordTermCoverage:         candidate.KeywordTermCoverage,
			}
		}
		items = append(items, item)
		tokensUsed += tokens
	}
	return domain.RecallResponse{
		Context: domain.ContextPackage{Items: items, TokensUsed: tokensUsed, CharsUsed: result.CharsUsed, Truncated: result.Truncated, NextCursor: result.NextCursor},
		Execution: domain.RecallExecutionMetadata{
			OperationID: operationID, Intent: query.Intent, ReferenceTime: result.ReferenceTime,
			RequestedCapabilities: append([]string(nil), result.RequestedChannels...), ExecutedCapabilities: append([]string(nil), result.ExecutedChannels...),
			Degraded: result.Degraded, DegradationReasons: degradationReasons,
			QueryPlanIdentity: result.QueryPlanIdentity, RankingIdentity: result.RankingIdentity, ContextSelectionIdentity: result.ContextIdentity,
		},
	}
}

func estimatedContextTokens(content string) int {
	return utf8.RuneCountInString(content)
}
