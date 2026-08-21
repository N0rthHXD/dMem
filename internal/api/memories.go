package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/storage"
)

type evidenceRequest struct {
	EventID      string          `json:"event_id"`
	EvidenceType string          `json:"evidence_type"`
	StartOffset  json.RawMessage `json:"start_offset"`
	EndOffset    json.RawMessage `json:"end_offset"`
}

type createMemoryRequest struct {
	SpaceID     string            `json:"space_id"`
	MemoryType  string            `json:"memory_type"`
	Content     string            `json:"content"`
	ValidFrom   *string           `json:"valid_from"`
	ValidUntil  *string           `json:"valid_until"`
	Sensitivity json.RawMessage   `json:"sensitivity"`
	Evidence    []evidenceRequest `json:"evidence"`
	Reason      string            `json:"reason"`
}

type reviseMemoryRequest struct {
	ExpectedRevisionID string            `json:"expected_revision_id"`
	Content            string            `json:"content"`
	ValidFrom          *string           `json:"valid_from"`
	ValidUntil         *string           `json:"valid_until"`
	Evidence           []evidenceRequest `json:"evidence"`
	Reason             string            `json:"reason"`
}

type changeMemoryStateRequest struct {
	ExpectedRevisionID string  `json:"expected_revision_id"`
	Action             string  `json:"action"`
	RestoreRevisionID  *string `json:"restore_revision_id"`
	Reason             string  `json:"reason"`
}

func (h *Handler) memories(w http.ResponseWriter, r *http.Request) {
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
		var request createMemoryRequest
		if !h.decode(w, r, requestID, &request) {
			return
		}
		params, err := validateCreateMemory(request)
		if err != nil {
			h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
			return
		}
		identity := writeIdentity(callerID, key, requestID, r.Method, "/v1/memories", params)
		result, replay, err := h.repository.CreateMemory(r.Context(), identity, params)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
		h.writeJSON(w, http.StatusCreated, result)
		return
	}
	spaceID := r.URL.Query().Get("space_id")
	if !validUUID(spaceID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "space_id is required and must be a UUID", requestID, "", false, nil)
		return
	}
	includeAncestors := true
	if value := r.URL.Query().Get("include_ancestors"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "include_ancestors must be a boolean", requestID, "", false, nil)
			return
		}
		includeAncestors = parsed
	}
	state := r.URL.Query().Get("state")
	if state != "" && !validMemoryState(state) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "invalid state", requestID, "", false, nil)
		return
	}
	limit, cursor, ok := pageParams(w, r, requestID)
	if !ok {
		return
	}
	result, err := h.repository.ListMemories(r.Context(), storage.MemoryFilter{SpaceID: spaceID, IncludeAncestors: includeAncestors, State: state, Limit: limit, Cursor: cursor})
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) memory(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	callerID, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	memoryID := r.PathValue("memory_id")
	if !validUUID(memoryID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "memory_id must be a UUID", requestID, "", false, nil)
		return
	}
	if r.Method == http.MethodPatch {
		key, ok := h.requireIdempotency(w, r, requestID)
		if !ok {
			return
		}
		var request reviseMemoryRequest
		if !h.decode(w, r, requestID, &request) {
			return
		}
		params, err := validateReviseMemory(request)
		if err != nil {
			h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
			return
		}
		hashValue := struct {
			MemoryID string `json:"memory_id"`
			storage.ReviseMemoryParams
		}{memoryID, params}
		identity := writeIdentity(callerID, key, requestID, r.Method, "/v1/memories/{memory_id}", hashValue)
		result, replay, err := h.repository.ReviseMemory(r.Context(), identity, memoryID, params)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	result, err := h.repository.GetMemory(r.Context(), memoryID)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) memoryRevisions(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	memoryID := r.PathValue("memory_id")
	if !validUUID(memoryID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "memory_id must be a UUID", requestID, "", false, nil)
		return
	}
	result, err := h.repository.ListMemoryRevisions(r.Context(), memoryID)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) memoryState(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	callerID, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	if !ok {
		return
	}
	memoryID := r.PathValue("memory_id")
	if !validUUID(memoryID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "memory_id must be a UUID", requestID, "", false, nil)
		return
	}
	var request changeMemoryStateRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	params, err := validateChangeMemoryState(request)
	if err != nil {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
		return
	}
	hashValue := struct {
		MemoryID string `json:"memory_id"`
		storage.ChangeMemoryStateParams
	}{memoryID, params}
	identity := writeIdentity(callerID, key, requestID, r.Method, "/v1/memories/{memory_id}/state", hashValue)
	result, replay, err := h.repository.ChangeMemoryState(r.Context(), identity, memoryID, params)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) memoryStateEvents(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	memoryID := r.PathValue("memory_id")
	if !validUUID(memoryID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "memory_id must be a UUID", requestID, "", false, nil)
		return
	}
	result, err := h.repository.ListMemoryStateEvents(r.Context(), memoryID)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func validateCreateMemory(request createMemoryRequest) (storage.CreateMemoryParams, error) {
	if !validUUID(request.SpaceID) {
		return storage.CreateMemoryParams{}, errors.New("space_id must be a UUID")
	}
	if !validMemoryType(request.MemoryType) {
		return storage.CreateMemoryParams{}, errors.New("invalid memory_type")
	}
	sensitivity := "normal"
	if request.Sensitivity != nil {
		if string(request.Sensitivity) == "null" || json.Unmarshal(request.Sensitivity, &sensitivity) != nil {
			return storage.CreateMemoryParams{}, errors.New("sensitivity must be normal or sensitive")
		}
	}
	if sensitivity != "normal" && sensitivity != "sensitive" {
		return storage.CreateMemoryParams{}, errors.New("invalid sensitivity")
	}
	evidence, err := validateEvidence(request.Evidence)
	if err != nil {
		return storage.CreateMemoryParams{}, err
	}
	validFrom, validUntil, err := validateMemoryContent(request.Content, request.ValidFrom, request.ValidUntil, request.Reason)
	if err != nil {
		return storage.CreateMemoryParams{}, err
	}
	return storage.CreateMemoryParams{SpaceID: request.SpaceID, MemoryType: request.MemoryType, Content: request.Content, ValidFrom: validFrom, ValidUntil: validUntil, Sensitivity: sensitivity, Evidence: evidence, Reason: request.Reason}, nil
}

func validateReviseMemory(request reviseMemoryRequest) (storage.ReviseMemoryParams, error) {
	if !validUUID(request.ExpectedRevisionID) {
		return storage.ReviseMemoryParams{}, errors.New("expected_revision_id must be a UUID")
	}
	evidence, err := validateEvidence(request.Evidence)
	if err != nil {
		return storage.ReviseMemoryParams{}, err
	}
	validFrom, validUntil, err := validateMemoryContent(request.Content, request.ValidFrom, request.ValidUntil, request.Reason)
	if err != nil {
		return storage.ReviseMemoryParams{}, err
	}
	return storage.ReviseMemoryParams{ExpectedRevisionID: request.ExpectedRevisionID, Content: request.Content, ValidFrom: validFrom, ValidUntil: validUntil, Evidence: evidence, Reason: request.Reason}, nil
}

func validateChangeMemoryState(request changeMemoryStateRequest) (storage.ChangeMemoryStateParams, error) {
	if !validUUID(request.ExpectedRevisionID) {
		return storage.ChangeMemoryStateParams{}, errors.New("expected_revision_id must be a UUID")
	}
	validActions := map[string]bool{"confirm": true, "contest": true, "archive": true, "restore": true, "freeze": true, "unfreeze": true, "retract": true, "delete": true}
	if !validActions[request.Action] {
		return storage.ChangeMemoryStateParams{}, errors.New("invalid action")
	}
	if utf8.RuneCountInString(request.Reason) < 1 || utf8.RuneCountInString(request.Reason) > 1024 {
		return storage.ChangeMemoryStateParams{}, errors.New("reason length must be between 1 and 1024")
	}
	if request.Action == "restore" && (request.RestoreRevisionID == nil || !validUUID(*request.RestoreRevisionID)) {
		return storage.ChangeMemoryStateParams{}, errors.New("restore_revision_id is required and must be a UUID")
	}
	return storage.ChangeMemoryStateParams{ExpectedRevisionID: request.ExpectedRevisionID, Action: request.Action, RestoreRevisionID: request.RestoreRevisionID, Reason: request.Reason}, nil
}

func validateEvidence(items []evidenceRequest) ([]storage.EvidenceParams, error) {
	if len(items) == 0 {
		return nil, errors.New("evidence must contain at least one item")
	}
	result := make([]storage.EvidenceParams, len(items))
	for i, item := range items {
		if !validUUID(item.EventID) {
			return nil, errors.New("evidence event_id must be a UUID")
		}
		if item.EvidenceType != "direct_statement" && item.EvidenceType != "tool_result" && item.EvidenceType != "manual_reference" {
			return nil, errors.New("invalid evidence_type")
		}
		start, err := evidenceOffset(item.StartOffset)
		if err != nil {
			return nil, err
		}
		end, err := evidenceOffset(item.EndOffset)
		if err != nil {
			return nil, err
		}
		if (start == nil) != (end == nil) || start != nil && (*start < 0 || *end <= *start) {
			return nil, errors.New("evidence offsets must satisfy 0 <= start_offset < end_offset")
		}
		result[i] = storage.EvidenceParams{EventID: item.EventID, EvidenceType: item.EvidenceType, StartOffset: start, EndOffset: end}
	}
	return result, nil
}

func evidenceOffset(raw json.RawMessage) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	var value int64
	if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
		return nil, errors.New("evidence offsets must be integers")
	}
	return &value, nil
}

func validateMemoryContent(content string, validFrom, validUntil *string, reason string) (*string, *string, error) {
	if utf8.RuneCountInString(content) < 1 {
		return nil, nil, errors.New("content must not be empty")
	}
	if utf8.RuneCountInString(reason) < 1 || utf8.RuneCountInString(reason) > 1024 {
		return nil, nil, errors.New("reason length must be between 1 and 1024")
	}
	normalize := func(value *string) (*string, *time.Time, error) {
		if value == nil {
			return nil, nil, nil
		}
		parsed, err := time.Parse(time.RFC3339, *value)
		if err != nil {
			return nil, nil, errors.New("valid time must be RFC3339")
		}
		formatted := formatAPITime(parsed)
		return &formatted, &parsed, nil
	}
	from, fromTime, err := normalize(validFrom)
	if err != nil {
		return nil, nil, err
	}
	until, untilTime, err := normalize(validUntil)
	if err != nil {
		return nil, nil, err
	}
	if fromTime != nil && untilTime != nil && !untilTime.After(*fromTime) {
		return nil, nil, errors.New("valid_until must be after valid_from")
	}
	return from, until, nil
}

func validMemoryType(value string) bool {
	return map[string]bool{"fact": true, "preference": true, "constraint": true, "decision": true, "task": true, "event": true, "project_context": true, "agent_experience": true}[value]
}

func validMemoryState(value string) bool {
	return map[string]bool{"candidate": true, "active": true, "contested": true, "archived": true, "expired": true, "retracted": true, "deleted": true}[value]
}
