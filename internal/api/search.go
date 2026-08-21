package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/storage"
)

type searchRequest struct {
	Query            string          `json:"query"`
	SpaceIDs         []string        `json:"space_ids"`
	IncludeAncestors json.RawMessage `json:"include_ancestors"`
	MemoryTypes      json.RawMessage `json:"memory_types"`
	States           json.RawMessage `json:"states"`
	ReferenceTime    json.RawMessage `json:"reference_time"`
	Mode             json.RawMessage `json:"mode"`
	Limit            json.RawMessage `json:"limit"`
	MaxChars         json.RawMessage `json:"max_chars"`
	IncludeEvidence  json.RawMessage `json:"include_evidence"`
	Cursor           json.RawMessage `json:"cursor"`
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	var request searchRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	params, err := h.validateSearch(request)
	if err != nil {
		code := "invalid_request"
		if errors.Is(err, errSemanticUnsupported) {
			code = "unsupported"
		}
		h.writeError(w, http.StatusUnprocessableEntity, code, err.Error(), requestID, "", false, nil)
		return
	}
	result, err := h.repository.SearchMemories(r.Context(), params)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalidSearchQuery) {
			h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "search request is invalid", requestID, "", false, nil)
			return
		}
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

var errSemanticUnsupported = errors.New("semantic search is unsupported")

func (h *Handler) validateSearch(request searchRequest) (storage.SearchParams, error) {
	if strings.TrimSpace(request.Query) == "" || utf8.RuneCountInString(request.Query) < 1 {
		return storage.SearchParams{}, errors.New("query must not be empty")
	}
	if len(request.SpaceIDs) == 0 {
		return storage.SearchParams{}, errors.New("space_ids must contain at least one UUID")
	}
	seen := map[string]bool{}
	for _, id := range request.SpaceIDs {
		if !validUUID(id) {
			return storage.SearchParams{}, errors.New("space_ids must contain UUIDs")
		}
		if seen[id] {
			return storage.SearchParams{}, errors.New("space_ids must be unique")
		}
		seen[id] = true
	}
	params := storage.SearchParams{Query: request.Query, SpaceIDs: request.SpaceIDs, IncludeAncestors: true, States: []string{"active"}, Mode: "keyword", Limit: 10, MaxChars: 12000}
	if request.IncludeAncestors != nil {
		if isNull(request.IncludeAncestors) || json.Unmarshal(request.IncludeAncestors, &params.IncludeAncestors) != nil {
			return storage.SearchParams{}, errors.New("include_ancestors must be a boolean")
		}
	}
	validTypes := map[string]bool{"fact": true, "preference": true, "constraint": true, "decision": true, "task": true, "event": true, "project_context": true, "agent_experience": true}
	if request.MemoryTypes != nil {
		params.MemoryTypesSet = true
		if isNull(request.MemoryTypes) || json.Unmarshal(request.MemoryTypes, &params.MemoryTypes) != nil || !validValues(params.MemoryTypes, validTypes) {
			return storage.SearchParams{}, errors.New("memory_types is invalid")
		}
	}
	validStates := map[string]bool{"candidate": true, "active": true, "contested": true, "archived": true, "expired": true, "retracted": true, "deleted": true}
	if request.States != nil {
		if isNull(request.States) || json.Unmarshal(request.States, &params.States) != nil || !validValues(params.States, validStates) {
			return storage.SearchParams{}, errors.New("states is invalid")
		}
	}
	if request.ReferenceTime != nil {
		var value string
		if isNull(request.ReferenceTime) || json.Unmarshal(request.ReferenceTime, &value) != nil {
			return storage.SearchParams{}, errors.New("reference_time must be RFC3339")
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return storage.SearchParams{}, errors.New("reference_time must be RFC3339")
		}
		params.ReferenceTime, params.ReferenceTimeSet = formatAPITime(parsed), true
	}
	if request.Mode != nil {
		if isNull(request.Mode) || json.Unmarshal(request.Mode, &params.Mode) != nil || params.Mode != "keyword" && params.Mode != "semantic" {
			return storage.SearchParams{}, errors.New("mode is invalid")
		}
	}
	if params.Mode == "semantic" {
		return storage.SearchParams{}, errSemanticUnsupported
	}
	if err := decodeRange(request.Limit, &params.Limit, 1, 100, "limit"); err != nil {
		return storage.SearchParams{}, err
	}
	if err := decodeRange(request.MaxChars, &params.MaxChars, 1, 100000, "max_chars"); err != nil {
		return storage.SearchParams{}, err
	}
	if request.IncludeEvidence != nil && (isNull(request.IncludeEvidence) || json.Unmarshal(request.IncludeEvidence, &params.IncludeEvidence) != nil) {
		return storage.SearchParams{}, errors.New("include_evidence must be a boolean")
	}
	if request.Cursor != nil && (isNull(request.Cursor) || json.Unmarshal(request.Cursor, &params.Cursor) != nil) {
		return storage.SearchParams{}, errors.New("cursor must be a string")
	}
	params.States = sortedUnique(params.States)
	params.MemoryTypes = sortedUnique(params.MemoryTypes)
	return params, nil
}

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
