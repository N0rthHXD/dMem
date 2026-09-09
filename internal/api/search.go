package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

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
