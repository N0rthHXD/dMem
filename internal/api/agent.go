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
	SpaceID       string         `json:"space_id"`
	Content       string         `json:"content"`
	MemoryType    string         `json:"memory_type,omitempty"`
	Sensitivity   string         `json:"sensitivity,omitempty"`
	ValidFrom     *string        `json:"valid_from,omitempty"`
	ValidUntil    *string        `json:"valid_until,omitempty"`
	SourceName    string         `json:"source_name,omitempty"`
	SourceEventID *string        `json:"source_event_id,omitempty"`
	OccurredAt    string         `json:"occurred_at,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	Reason        string         `json:"reason,omitempty"`
}

type recallRequest struct {
	Query                  string          `json:"query"`
	SpaceIDs               []string        `json:"space_ids"`
	IncludeAncestors       json.RawMessage `json:"include_ancestors"`
	MemoryTypes            json.RawMessage `json:"memory_types"`
	ReferenceTime          json.RawMessage `json:"reference_time"`
	Mode                   json.RawMessage `json:"mode"`
	Limit                  json.RawMessage `json:"limit"`
	MaxChars               json.RawMessage `json:"max_chars"`
	IncludeEvidence        json.RawMessage `json:"include_evidence"`
	Cursor                 json.RawMessage `json:"cursor"`
	ExcludeStaleAggregates json.RawMessage `json:"exclude_stale_aggregates"`
}

type memoryFeedbackRequest struct {
	SpaceID         string                        `json:"space_id"`
	Type            string                        `json:"type"`
	ReasonCode      string                        `json:"reason_code"`
	Note            *string                       `json:"note,omitempty"`
	Search          *domain.FeedbackSearchContext `json:"search,omitempty"`
	CreateProposal  bool                          `json:"create_proposal,omitempty"`
	SourceEventID   string                        `json:"source_event_id,omitempty"`
	ProposedContent string                        `json:"proposed_content,omitempty"`
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
	if request.MemoryType == "" {
		request.MemoryType = "fact"
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
		SpaceID: request.SpaceID, Content: request.Content, MemoryType: request.MemoryType, Sensitivity: request.Sensitivity,
		ValidFrom: request.ValidFrom, ValidUntil: request.ValidUntil, SourceName: request.SourceName,
		SourceEventID: request.SourceEventID, OccurredAt: request.OccurredAt, Metadata: request.Metadata, Reason: request.Reason,
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
	query, err := recallQuery(request)
	if err != nil {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
		return
	}
	result, err := h.memoryService.Recall(r.Context(), query)
	if err != nil {
		h.serviceError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
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
		SpaceID: request.SpaceID, MemoryID: memoryID, FeedbackType: request.Type, ReasonCode: request.ReasonCode,
		Note: request.Note, Search: request.Search, CreateProposal: request.CreateProposal,
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

func recallQuery(request recallRequest) (application.RecallQuery, error) {
	query := application.RecallQuery{Query: request.Query, SpaceIDs: request.SpaceIDs, IncludeAncestors: true, States: []string{"active", "contested"}, Mode: "hybrid", Limit: 10, MaxChars: 12000}
	for _, id := range request.SpaceIDs {
		if !validUUID(id) {
			return application.RecallQuery{}, errors.New("space_ids must contain UUIDs")
		}
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
	if request.Mode != nil && (isNull(request.Mode) || json.Unmarshal(request.Mode, &query.Mode) != nil) {
		return application.RecallQuery{}, errors.New("mode must be a string")
	}
	if err := decodeRange(request.Limit, &query.Limit, 1, 100, "limit"); err != nil {
		return application.RecallQuery{}, err
	}
	if err := decodeRange(request.MaxChars, &query.MaxChars, 1, 100000, "max_chars"); err != nil {
		return application.RecallQuery{}, err
	}
	if request.IncludeEvidence != nil && (isNull(request.IncludeEvidence) || json.Unmarshal(request.IncludeEvidence, &query.IncludeEvidence) != nil) {
		return application.RecallQuery{}, errors.New("include_evidence must be a boolean")
	}
	if request.Cursor != nil && (isNull(request.Cursor) || json.Unmarshal(request.Cursor, &query.Cursor) != nil) {
		return application.RecallQuery{}, errors.New("cursor must be a string")
	}
	if request.ExcludeStaleAggregates != nil && (isNull(request.ExcludeStaleAggregates) || json.Unmarshal(request.ExcludeStaleAggregates, &query.ExcludeStaleAggregates) != nil) {
		return application.RecallQuery{}, errors.New("exclude_stale_aggregates must be a boolean")
	}
	return query, nil
}
