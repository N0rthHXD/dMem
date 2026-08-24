package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

func unmarshalStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

type Error struct {
	ErrorCode   string         `json:"error_code"`
	Message     string         `json:"message"`
	RequestID   string         `json:"request_id"`
	OperationID string         `json:"operation_id"`
	Retryable   bool           `json:"retryable"`
	RetryAfter  *int           `json:"retry_after"`
	Details     map[string]any `json:"details,omitempty"`
}

type createSpaceRequest struct {
	ParentSpaceID *string `json:"parent_space_id"`
	SpaceType     string  `json:"space_type"`
	ExternalID    string  `json:"external_id"`
	DisplayName   string  `json:"display_name"`
}

type ingestEventRequest struct {
	SpaceID       string          `json:"space_id"`
	SourceType    string          `json:"source_type"`
	SourceName    string          `json:"source_name"`
	SourceEventID json.RawMessage `json:"source_event_id"`
	OccurredAt    string          `json:"occurred_at"`
	Content       contentRequest  `json:"content"`
	Metadata      json.RawMessage `json:"metadata"`
}

type contentRequest struct {
	Kind         string          `json:"kind"`
	MediaType    string          `json:"media_type"`
	Text         string          `json:"text,omitempty"`
	Value        json.RawMessage `json:"value,omitempty"`
	ReferenceURI string          `json:"reference_uri,omitempty"`
	DeclaredSize *int64          `json:"declared_size,omitempty"`
	DeclaredHash string          `json:"declared_hash,omitempty"`
	TextPresent  bool            `json:"-"`
	ValuePresent bool            `json:"-"`
}

func (c *contentRequest) UnmarshalJSON(data []byte) error {
	type fields struct {
		Kind         string          `json:"kind"`
		MediaType    string          `json:"media_type"`
		Text         json.RawMessage `json:"text"`
		Value        json.RawMessage `json:"value"`
		ReferenceURI string          `json:"reference_uri"`
		DeclaredSize *int64          `json:"declared_size"`
		DeclaredHash string          `json:"declared_hash"`
	}
	var value fields
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	c.Kind, c.MediaType = value.Kind, value.MediaType
	c.ReferenceURI, c.DeclaredSize, c.DeclaredHash = value.ReferenceURI, value.DeclaredSize, value.DeclaredHash
	c.TextPresent, c.ValuePresent = value.Text != nil, value.Value != nil
	if c.TextPresent {
		if err := json.Unmarshal(value.Text, &c.Text); err != nil {
			return err
		}
	}
	c.Value = value.Value
	return nil
}

func (h *Handler) spaces(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	callerID, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		key, ok := h.requireIdempotency(w, r, requestID)
		if !ok {
			return
		}
		var request createSpaceRequest
		if !h.decode(w, r, requestID, &request) {
			return
		}
		if err := validateCreateSpace(request); err != nil {
			h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
			return
		}
		identity := writeIdentity(callerID, key, requestID, r.Method, "/v1/spaces", request)
		result, replay, err := h.repository.CreateSpace(r.Context(), identity, storage.CreateSpaceParams{ParentSpaceID: request.ParentSpaceID, SpaceType: request.SpaceType, ExternalID: request.ExternalID, DisplayName: request.DisplayName})
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
		h.writeJSON(w, http.StatusCreated, result)
		return
	}
	limit, cursor, ok := pageParams(w, r, requestID)
	if !ok {
		return
	}
	var parentID *string
	if value := r.URL.Query().Get("parent_space_id"); value != "" {
		if !validUUID(value) {
			h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "parent_space_id must be a UUID", requestID, "", false, nil)
			return
		}
		parentID = &value
	}
	result, err := h.repository.ListSpaces(r.Context(), parentID, limit, cursor)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) space(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	id := r.PathValue("space_id")
	if !validUUID(id) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "space_id must be a UUID", requestID, "", false, nil)
		return
	}
	result, err := h.repository.GetSpace(r.Context(), id)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	callerID, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		key, ok := h.requireIdempotency(w, r, requestID)
		if !ok {
			return
		}
		var request ingestEventRequest
		if !h.decode(w, r, requestID, &request) {
			return
		}
		if request.Content.Kind == "binary" {
			h.writeError(w, http.StatusUnprocessableEntity, "unsupported_content_type", "binary content is not supported", requestID, "", false, nil)
			return
		}
		params, err := validateIngest(request)
		if err != nil {
			h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
			return
		}
		identity := writeIdentity(callerID, key, requestID, r.Method, "/v1/events", params)
		result, replay, err := h.repository.IngestEvent(r.Context(), identity, params)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
		h.writeJSON(w, http.StatusAccepted, result)
		return
	}
	spaceID := r.URL.Query().Get("space_id")
	if !validUUID(spaceID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "space_id is required and must be a UUID", requestID, "", false, nil)
		return
	}
	limit, cursor, ok := pageParams(w, r, requestID)
	if !ok {
		return
	}
	filter := storage.EventFilter{SpaceID: spaceID, SourceName: r.URL.Query().Get("source_name"), Limit: limit, Cursor: cursor}
	for query, target := range map[string]*string{"occurred_from": &filter.OccurredFrom, "occurred_until": &filter.OccurredUntil} {
		if value := r.URL.Query().Get(query); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", query+" must be RFC3339", requestID, "", false, nil)
				return
			}
			*target = formatAPITime(parsed)
		}
	}
	result, err := h.repository.ListEvents(r.Context(), filter)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) event(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	id := r.PathValue("event_id")
	if !validUUID(id) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "event_id must be a UUID", requestID, "", false, nil)
		return
	}
	result, err := h.repository.GetEvent(r.Context(), id)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) replayEvent(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	callerID, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	if !ok {
		return
	}
	id := r.PathValue("event_id")
	if !validUUID(id) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "event_id must be a UUID", requestID, "", false, nil)
		return
	}
	identity := writeIdentity(callerID, key, requestID, r.Method, "/v1/events/{event_id}/replay", struct {
		EventID string `json:"event_id"`
	}{id})
	result, replay, err := h.repository.ReplayEvent(r.Context(), identity, id)
	if err != nil {
		if errors.Is(err, storage.ErrJobAlreadyActive) {
			h.writeError(w, http.StatusConflict, "job_already_active", "an active job already exists", requestID, "", false, map[string]any{"job_id": result.JobID})
			return
		}
		h.repositoryError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
	h.writeJSON(w, http.StatusAccepted, result)
}

func validateCreateSpace(request createSpaceRequest) error {
	validTypes := map[string]bool{"user": true, "agent": true, "application": true, "project": true, "task": true, "session": true}
	if !validTypes[request.SpaceType] {
		return errors.New("invalid space_type")
	}
	if utf8.RuneCountInString(request.ExternalID) < 1 || utf8.RuneCountInString(request.ExternalID) > 128 {
		return errors.New("external_id length must be between 1 and 128")
	}
	if utf8.RuneCountInString(request.DisplayName) < 1 || utf8.RuneCountInString(request.DisplayName) > 256 {
		return errors.New("display_name length must be between 1 and 256")
	}
	if request.ParentSpaceID != nil && !validUUID(*request.ParentSpaceID) {
		return errors.New("parent_space_id must be a UUID or null")
	}
	return nil
}

func validateIngest(request ingestEventRequest) (storage.IngestParams, error) {
	if !validUUID(request.SpaceID) {
		return storage.IngestParams{}, errors.New("space_id must be a UUID")
	}
	validSources := map[string]bool{"message": true, "conversation": true, "tool_call": true, "tool_result": true, "file_reference": true, "collector_event": true, "manual": true}
	if !validSources[request.SourceType] {
		return storage.IngestParams{}, errors.New("invalid source_type")
	}
	if utf8.RuneCountInString(request.SourceName) < 1 || utf8.RuneCountInString(request.SourceName) > 128 {
		return storage.IngestParams{}, errors.New("source_name length must be between 1 and 128")
	}
	var sourceEventID *string
	if request.SourceEventID != nil {
		var value string
		if string(request.SourceEventID) == "null" || json.Unmarshal(request.SourceEventID, &value) != nil {
			return storage.IngestParams{}, errors.New("source_event_id must be a string")
		}
		if utf8.RuneCountInString(value) > 256 {
			return storage.IngestParams{}, errors.New("source_event_id is too long")
		}
		sourceEventID = &value
	}
	occurred, err := time.Parse(time.RFC3339, request.OccurredAt)
	if err != nil {
		return storage.IngestParams{}, errors.New("occurred_at must be RFC3339")
	}
	input := request.Content
	content := domain.Content{Kind: input.Kind, MediaType: input.MediaType, Text: input.Text, ReferenceURI: input.ReferenceURI, DeclaredSize: input.DeclaredSize, DeclaredHash: input.DeclaredHash}
	if input.ValuePresent {
		decoder := json.NewDecoder(bytes.NewReader(input.Value))
		decoder.UseNumber()
		if err := decoder.Decode(&content.Value); err != nil {
			return storage.IngestParams{}, errors.New("content.value must be valid JSON")
		}
	}
	if content.MediaType == "" {
		return storage.IngestParams{}, errors.New("content.media_type is required")
	}
	switch content.Kind {
	case "text":
		if !input.TextPresent || input.ValuePresent || content.ReferenceURI != "" || content.DeclaredSize != nil || content.DeclaredHash != "" {
			return storage.IngestParams{}, errors.New("text content contains incompatible fields")
		}
	case "json":
		if !input.ValuePresent || input.TextPresent || content.ReferenceURI != "" || content.DeclaredSize != nil || content.DeclaredHash != "" {
			return storage.IngestParams{}, errors.New("json content requires value only")
		}
	case "reference":
		if content.ReferenceURI == "" || input.TextPresent || input.ValuePresent {
			return storage.IngestParams{}, errors.New("reference content requires reference_uri")
		}
	default:
		return storage.IngestParams{}, errors.New("unsupported content kind")
	}
	if content.DeclaredSize != nil && *content.DeclaredSize < 0 {
		return storage.IngestParams{}, errors.New("declared_size must not be negative")
	}
	metadata := map[string]any{}
	if request.Metadata != nil {
		if string(request.Metadata) == "null" {
			return storage.IngestParams{}, errors.New("metadata must be an object")
		}
		decoder := json.NewDecoder(bytes.NewReader(request.Metadata))
		decoder.UseNumber()
		if err := decoder.Decode(&metadata); err != nil {
			return storage.IngestParams{}, errors.New("metadata must be an object")
		}
	}
	return storage.IngestParams{SpaceID: request.SpaceID, SourceType: request.SourceType, SourceName: request.SourceName, SourceEventID: sourceEventID, OccurredAt: formatAPITime(occurred), Content: content, Metadata: metadata}, nil
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, requestID string, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestSize)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		status, code := http.StatusBadRequest, "invalid_json"
		if strings.Contains(err.Error(), "unknown field") {
			status, code = http.StatusUnprocessableEntity, "invalid_request"
		}
		h.writeError(w, status, code, "request body is invalid", requestID, "", false, nil)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		h.writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain one JSON value", requestID, "", false, nil)
		return false
	}
	return true
}

func (h *Handler) requireCaller(w http.ResponseWriter, r *http.Request, requestID string) (string, bool) {
	value := r.Header.Get("X-Caller-ID")
	if len(value) < 1 || len(value) > 128 {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "X-Caller-ID is required", requestID, "", false, nil)
		return "", false
	}
	return value, true
}

func (h *Handler) requireIdempotency(w http.ResponseWriter, r *http.Request, requestID string) (string, bool) {
	value := r.Header.Get("Idempotency-Key")
	if len(value) < 1 || len(value) > 128 {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "Idempotency-Key is required", requestID, "", false, nil)
		return "", false
	}
	return value, true
}

func (h *Handler) repositoryError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		h.writeError(w, http.StatusNotFound, "not_found", "object not found", requestID, "", false, nil)
	case errors.Is(err, storage.ErrInvalidParent):
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "invalid parent for space type", requestID, "", false, nil)
	case errors.Is(err, storage.ErrIdempotencyConflict):
		h.writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key was used for a different request", requestID, "", false, nil)
	case errors.Is(err, storage.ErrConflict):
		h.writeError(w, http.StatusConflict, "conflict", "object conflicts with existing state", requestID, "", false, nil)
	case errors.Is(err, storage.ErrQueueFull):
		h.writeError(w, http.StatusTooManyRequests, "retryable_overload", "job queue is full", requestID, "", true, nil)
	case errors.Is(err, storage.ErrResourceExhausted):
		h.writeError(w, http.StatusInsufficientStorage, "resource_exhausted", "disk hard limit reached", requestID, "", false, map[string]any{"resource": "disk", "pressure": "hard"})
	case errors.Is(err, storage.ErrInvalidCursor):
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "cursor is invalid", requestID, "", false, nil)
	case errors.Is(err, storage.ErrRevisionConflict):
		h.writeError(w, http.StatusConflict, "revision_conflict", "expected revision is not current", requestID, "", false, nil)
	case errors.Is(err, storage.ErrInvalidStateTransition):
		h.writeError(w, http.StatusConflict, "invalid_state_transition", "memory state transition is not allowed", requestID, "", false, nil)
	case errors.Is(err, storage.ErrInvalidEvidence):
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "memory evidence is invalid", requestID, "", false, nil)
	case errors.Is(err, storage.ErrCandidateReviewed):
		h.writeError(w, http.StatusConflict, "candidate_already_reviewed", "candidate review state is terminal", requestID, "", false, nil)
	case errors.Is(err, storage.ErrEntityProposalReviewed):
		h.writeError(w, http.StatusConflict, "proposal_already_reviewed", "entity proposal review state is terminal", requestID, "", false, nil)
	case errors.Is(err, storage.ErrInvalidRelationProposal):
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "relation proposal is invalid", requestID, "", false, nil)
	case errors.Is(err, storage.ErrRelationProposalReviewed):
		h.writeError(w, http.StatusConflict, "proposal_already_reviewed", "relation proposal review state is terminal", requestID, "", false, nil)
	case errors.Is(err, storage.ErrInvalidAggregateProposal):
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "aggregate proposal is invalid", requestID, "", false, nil)
	case errors.Is(err, storage.ErrAggregateProposalReviewed):
		h.writeError(w, http.StatusConflict, "proposal_already_reviewed", "aggregate proposal review state is terminal", requestID, "", false, nil)
	case errors.Is(err, storage.ErrInvalidEvolutionProposal):
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "evolution proposal is invalid", requestID, "", false, nil)
	case errors.Is(err, storage.ErrEvolutionReviewed), errors.Is(err, storage.ErrConflictResolved):
		h.writeError(w, http.StatusConflict, "proposal_already_reviewed", "review state is terminal", requestID, "", false, nil)
	case errors.Is(err, storage.ErrUnsupportedAction):
		h.writeError(w, http.StatusUnprocessableEntity, "unsupported", "automatic destructive action is unsupported", requestID, "", false, nil)
	case errors.Is(err, storage.ErrIndexDegraded):
		h.writeError(w, http.StatusServiceUnavailable, "index_degraded", "requested search index is unavailable", requestID, "", true, map[string]any{"index_state": "degraded"})
	default:
		h.writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage operation failed", requestID, "", true, nil)
	}
}

func (h *Handler) writeError(w http.ResponseWriter, status int, code, message, requestID, operationID string, retryable bool, details map[string]any) {
	h.writeJSON(w, status, Error{ErrorCode: code, Message: message, RequestID: requestID, OperationID: operationID, Retryable: retryable, Details: details})
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h *Handler) requestID() string {
	id, err := h.id()
	if err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	return id
}

func writeIdentity(caller, key, requestID, method, route string, request any) storage.WriteIdentity {
	return storage.WriteIdentity{CallerID: caller, IdempotencyKey: key, RequestID: requestID, Method: method, Route: route, RequestHash: storage.HashRequest(request)}
}

func pageParams(w http.ResponseWriter, r *http.Request, requestID string) (int, string, bool) {
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			(&Handler{}).writeError(w, http.StatusUnprocessableEntity, "invalid_request", "limit must be between 1 and 200", requestID, "", false, nil)
			return 0, "", false
		}
		limit = parsed
	}
	return limit, r.URL.Query().Get("cursor"), true
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}

func formatAPITime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
