package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/N0rthHXD/dMem/internal/application"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type rememberRequest struct {
	SpaceID         string         `json:"space_id"`
	Content         string         `json:"content"`
	Kind            string         `json:"kind,omitempty"`
	Sensitivity     string         `json:"sensitivity,omitempty"`
	ValidFrom       *string        `json:"valid_from,omitempty"`
	ValidUntil      *string        `json:"valid_until,omitempty"`
	SourceName      string         `json:"source_name,omitempty"`
	SourceEventID   *string        `json:"source_event_id,omitempty"`
	SourceStreamKey string         `json:"source_stream_key,omitempty"`
	Sequence        *int64         `json:"sequence,omitempty"`
	OccurredAt      string         `json:"occurred_at,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Reason          string         `json:"reason,omitempty"`
}

type recallRequest struct {
	Query            string              `json:"query"`
	SpaceIDs         []string            `json:"space_ids"`
	IncludeAncestors json.RawMessage     `json:"include_ancestors"`
	MemoryTypes      json.RawMessage     `json:"memory_types"`
	States           json.RawMessage     `json:"states"`
	ReferenceTime    json.RawMessage     `json:"reference_time"`
	Intent           json.RawMessage     `json:"intent"`
	Debug            *recallDebugRequest `json:"debug"`
	MaxResults       json.RawMessage     `json:"max_results"`
	MaxContextTokens json.RawMessage     `json:"max_context_tokens"`
	IncludeEvidence  json.RawMessage     `json:"include_evidence"`
	Explain          json.RawMessage     `json:"explain"`
	Cursor           json.RawMessage     `json:"cursor"`
}

// recallDebugRequest is the evaluation/debug-only contract. Production
// callers express intent and budget; explicit capability overrides exist only
// for evaluation and debugging.
type recallDebugRequest struct {
	Capabilities           json.RawMessage `json:"capabilities"`
	GraphPolicy            json.RawMessage `json:"graph_policy"`
	DisableDiversification json.RawMessage `json:"disable_diversification"`
	IncludeStaleAggregates json.RawMessage `json:"include_stale_aggregates"`
}

type memoryFeedbackRequest struct {
	SpaceID         string                          `json:"space_id"`
	Purpose         string                          `json:"feedback_purpose"`
	Type            string                          `json:"type"`
	ReasonCode      string                          `json:"reason_code"`
	Note            *string                         `json:"note,omitempty"`
	Decision        *domain.FeedbackDecisionContext `json:"decision,omitempty"`
	CreateProposal  bool                            `json:"create_proposal,omitempty"`
	SourceEventID   string                          `json:"source_event_id,omitempty"`
	ProposedContent string                          `json:"proposed_content,omitempty"`
}

type forgetRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) remember(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, key, ok := h.writeHeaders(w, r, requestID)
	if !ok {
		return
	}
	var request rememberRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	originalRequest := request
	if !validUUID(request.SpaceID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "space_id must be a UUID", requestID, "", false, nil)
		return
	}
	if request.Kind == "" {
		request.Kind = "fact"
	}
	if request.Sensitivity == "" {
		request.Sensitivity = "normal"
	}
	if request.SourceName == "" {
		request.SourceName = caller
	}
	if request.OccurredAt == "" {
		request.OccurredAt = formatAPITime(time.Now())
	}
	if request.Metadata == nil {
		request.Metadata = map[string]any{}
	}
	if request.Reason == "" {
		request.Reason = "agent_remember"
	}
	command := application.RememberCommand{
		SpaceID: request.SpaceID, Content: request.Content, Kind: request.Kind, Sensitivity: request.Sensitivity,
		ValidFrom: request.ValidFrom, ValidUntil: request.ValidUntil, SourceName: request.SourceName,
		SourceEventID: request.SourceEventID, SourceStreamKey: request.SourceStreamKey, Sequence: request.Sequence,
		OccurredAt: request.OccurredAt, Metadata: request.Metadata, Reason: request.Reason,
	}
	result, err := h.memoryService.Remember(r.Context(), appIdentity(caller, key, requestID, r.Method, "/v1/remember", originalRequest), command)
	if err != nil {
		h.serviceError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(result.Replay))
	h.writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) recall(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	var request recallRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	query, err := h.recallQuery(request)
	if err != nil {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
		return
	}
	query.OperationID = requestID
	result, err := h.memoryService.Recall(r.Context(), query)
	if err != nil {
		// An unknown scope space is a request error, not a missing object:
		// scope resolution fails before any ranking happens.
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalidSearchQuery) {
			h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "recall request is invalid", requestID, "", false, nil)
			return
		}
		h.serviceError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, recallResponse(requestID, query, result))
}

func (h *Handler) memoryFeedback(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, key, ok := h.writeHeaders(w, r, requestID)
	if !ok {
		return
	}
	memoryID := r.PathValue("memory_id")
	if !validUUID(memoryID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "memory_id must be a UUID", requestID, "", false, nil)
		return
	}
	var request memoryFeedbackRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	if !validUUID(request.SpaceID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "space_id must be a UUID", requestID, "", false, nil)
		return
	}
	command := application.FeedbackCommand{
		SpaceID: request.SpaceID, MemoryID: memoryID, Purpose: request.Purpose, FeedbackType: request.Type, ReasonCode: request.ReasonCode,
		Note: request.Note, Decision: request.Decision, CreateProposal: request.CreateProposal,
		SourceEventID: request.SourceEventID, ProposedContent: request.ProposedContent,
	}
	result, err := h.memoryService.Feedback(r.Context(), appIdentity(caller, key, requestID, r.Method, "/v1/memories/{memory_id}/feedback", struct {
		MemoryID string                `json:"memory_id"`
		Request  memoryFeedbackRequest `json:"request"`
	}{memoryID, request}), command)
	if err != nil {
		h.serviceError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(result.Replay))
	h.writeJSON(w, http.StatusCreated, result.Response)
}

func (h *Handler) forget(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, key, ok := h.writeHeaders(w, r, requestID)
	if !ok {
		return
	}
	memoryID := r.PathValue("memory_id")
	if !validUUID(memoryID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "memory_id must be a UUID", requestID, "", false, nil)
		return
	}
	var request forgetRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	command := application.ForgetCommand{MemoryID: memoryID, Reason: request.Reason}
	result, err := h.memoryService.Forget(r.Context(), appIdentity(caller, key, requestID, r.Method, "/v1/memories/{memory_id}/forget", struct {
		MemoryID string `json:"memory_id"`
		Reason   string `json:"reason"`
	}{memoryID, request.Reason}), command)
	if err != nil {
		h.serviceError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(result.Replay))
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) writeHeaders(w http.ResponseWriter, r *http.Request, requestID string) (string, string, bool) {
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return "", "", false
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	return caller, key, ok
}

func (h *Handler) serviceError(w http.ResponseWriter, err error, requestID string) {
	if errors.Is(err, application.ErrInvalidInput) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
		return
	}
	if errors.Is(err, storage.ErrInvalidSearchQuery) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "recall request is invalid", requestID, "", false, nil)
		return
	}
	h.repositoryError(w, err, requestID)
}

func appIdentity(caller, key, requestID, method, route string, request any) application.RequestIdentity {
	return application.RequestIdentity{CallerID: caller, IdempotencyKey: key, RequestID: requestID, Method: method, Route: route, RequestHash: application.HashRequest(request)}
}

// RecallBudgets are the daemon defaults applied when a recall request omits a
// budget. They come from the active configuration snapshot.
type RecallBudgets struct {
	MaxResults       int
	MaxContextTokens int
	MaxContextChars  int
}

func defaultRecallBudgets() RecallBudgets {
	return RecallBudgets{MaxResults: 20, MaxContextTokens: 4000, MaxContextChars: 16000}
}

func (h *Handler) recallQuery(request recallRequest) (application.RecallQuery, error) {
	defaults := defaultRecallBudgets()
	if h.configuration != nil {
		if snapshot, _ := h.configuration.ConfigurationStatus(); snapshot != nil {
			defaults = RecallBudgets{
				MaxResults:       snapshot.Config.Context.MaxResults,
				MaxContextTokens: snapshot.Config.Context.MaxContextTokens,
				MaxContextChars:  snapshot.Config.Context.MaxContextChars,
			}
		}
	}
	query := application.RecallQuery{Query: request.Query, SpaceIDs: request.SpaceIDs, IncludeAncestors: true, States: []string{"active", "contested"}, Intent: domain.RecallIntentAuto, Limit: defaults.MaxResults, MaxContextTokens: defaults.MaxContextTokens, MaxChars: defaults.MaxContextChars, ExcludeStaleAggregates: true}
	seenSpaces := map[string]bool{}
	for _, id := range request.SpaceIDs {
		if !validUUID(id) {
			return application.RecallQuery{}, errors.New("space_ids must contain UUIDs")
		}
		if seenSpaces[id] {
			return application.RecallQuery{}, errors.New("space_ids must be unique")
		}
		seenSpaces[id] = true
	}
	if request.IncludeAncestors != nil && (isNull(request.IncludeAncestors) || json.Unmarshal(request.IncludeAncestors, &query.IncludeAncestors) != nil) {
		return application.RecallQuery{}, errors.New("include_ancestors must be a boolean")
	}
	if request.MemoryTypes != nil {
		query.MemoryTypesSet = true
		if isNull(request.MemoryTypes) || json.Unmarshal(request.MemoryTypes, &query.MemoryTypes) != nil {
			return application.RecallQuery{}, errors.New("memory_types must be an array")
		}
	}
	validStates := map[string]bool{"active": true, "contested": true}
	if request.States != nil {
		if isNull(request.States) || json.Unmarshal(request.States, &query.States) != nil || !validValues(query.States, validStates) {
			return application.RecallQuery{}, errors.New("states is invalid")
		}
	}
	if request.ReferenceTime != nil {
		var value string
		if isNull(request.ReferenceTime) || json.Unmarshal(request.ReferenceTime, &value) != nil {
			return application.RecallQuery{}, errors.New("reference_time must be RFC3339")
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return application.RecallQuery{}, errors.New("reference_time must be RFC3339")
		}
		query.ReferenceTime, query.ReferenceTimeSet = formatAPITime(parsed), true
	}
	if request.Intent != nil {
		if isNull(request.Intent) || json.Unmarshal(request.Intent, &query.Intent) != nil || !query.Intent.Valid() {
			return application.RecallQuery{}, errors.New("intent is invalid")
		}
	}
	if request.Debug != nil && request.Debug.Capabilities != nil {
		if isNull(request.Debug.Capabilities) || json.Unmarshal(request.Debug.Capabilities, &query.Channels) != nil || len(query.Channels) == 0 || !validValues(query.Channels, validChannels) {
			return application.RecallQuery{}, errors.New("debug.capabilities is invalid")
		}
	}
	if request.Debug != nil && request.Debug.GraphPolicy != nil {
		if isNull(request.Debug.GraphPolicy) || json.Unmarshal(request.Debug.GraphPolicy, &query.GraphPolicy) != nil {
			return application.RecallQuery{}, errors.New("debug.graph_policy is invalid")
		}
	}
	if request.Debug != nil && request.Debug.DisableDiversification != nil {
		if isNull(request.Debug.DisableDiversification) || json.Unmarshal(request.Debug.DisableDiversification, &query.SkipDiversification) != nil {
			return application.RecallQuery{}, errors.New("debug.disable_diversification is invalid")
		}
	}
	if request.Debug != nil && request.Debug.IncludeStaleAggregates != nil {
		var include bool
		if isNull(request.Debug.IncludeStaleAggregates) || json.Unmarshal(request.Debug.IncludeStaleAggregates, &include) != nil {
			return application.RecallQuery{}, errors.New("debug.include_stale_aggregates must be a boolean")
		}
		query.ExcludeStaleAggregates = !include
	}
	if err := decodeRange(request.MaxResults, &query.Limit, 1, 1000, "max_results"); err != nil {
		return application.RecallQuery{}, err
	}
	if err := decodeRange(request.MaxContextTokens, &query.MaxContextTokens, 1, 1000000, "max_context_tokens"); err != nil {
		return application.RecallQuery{}, err
	}
	if request.IncludeEvidence != nil && (isNull(request.IncludeEvidence) || json.Unmarshal(request.IncludeEvidence, &query.IncludeEvidence) != nil) {
		return application.RecallQuery{}, errors.New("include_evidence must be a boolean")
	}
	if request.Explain != nil && (isNull(request.Explain) || json.Unmarshal(request.Explain, &query.Explain) != nil) {
		return application.RecallQuery{}, errors.New("explain must be a boolean")
	}
	if request.Cursor != nil && (isNull(request.Cursor) || json.Unmarshal(request.Cursor, &query.Cursor) != nil) {
		return application.RecallQuery{}, errors.New("cursor must be a string")
	}
	query.States = sortedUnique(query.States)
	query.MemoryTypes = sortedUnique(query.MemoryTypes)
	query.Channels = sortedUnique(query.Channels)
	return query, nil
}
