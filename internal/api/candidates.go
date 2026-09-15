package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/storage"
)

type reviewCandidateRequest struct {
	ReasonCode string  `json:"reason_code"`
	Note       *string `json:"note"`
}

func (h *Handler) candidates(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
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
	filter := storage.CandidateFilter{
		SpaceID: spaceID, ReviewState: r.URL.Query().Get("review_state"), MemoryType: r.URL.Query().Get("memory_type"),
		ProcessorIdentity: r.URL.Query().Get("processor_identity"), ProcessingVersion: r.URL.Query().Get("processing_version"),
		Limit: limit, Cursor: cursor,
	}
	if filter.ReviewState != "" && filter.ReviewState != "pending" && filter.ReviewState != "confirmed" && filter.ReviewState != "rejected" {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "invalid review_state", requestID, "", false, nil)
		return
	}
	if filter.MemoryType != "" && !validMemoryKind(filter.MemoryType) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "invalid memory_type", requestID, "", false, nil)
		return
	}
	for name, target := range map[string]*string{"created_from": &filter.CreatedFrom, "created_until": &filter.CreatedUntil} {
		if value := r.URL.Query().Get(name); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", name+" must be RFC3339", requestID, "", false, nil)
				return
			}
			*target = formatAPITime(parsed)
		}
	}
	result, err := h.repository.ListCandidates(r.Context(), filter)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) candidate(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	callerID, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	candidateID := r.PathValue("candidate_id")
	if !validUUID(candidateID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "candidate_id must be a UUID", requestID, "", false, nil)
		return
	}
	action := r.PathValue("action")
	if action == "" {
		result, err := h.repository.GetCandidate(r.Context(), candidateID)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		h.writeJSON(w, http.StatusOK, result)
		return
	}
	if action != "confirm" && action != "reject" {
		h.notFound(w, r)
		return
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	if !ok {
		return
	}
	var request reviewCandidateRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	if err := validateReviewCandidate(request); err != nil {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
		return
	}
	params := storage.ReviewCandidateParams{ReasonCode: request.ReasonCode, Note: request.Note}
	hashValue := struct {
		CandidateID string `json:"candidate_id"`
		storage.ReviewCandidateParams
	}{candidateID, params}
	identity := writeIdentity(callerID, key, requestID, r.Method, "/v1/candidates/{candidate_id}/"+action, hashValue)
	if action == "confirm" {
		result, replay, err := h.repository.ConfirmCandidate(r.Context(), identity, candidateID, params)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
		h.writeJSON(w, http.StatusCreated, result)
		return
	}
	result, replay, err := h.repository.RejectCandidate(r.Context(), identity, candidateID, params)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
	h.writeJSON(w, http.StatusOK, result)
}

func validateReviewCandidate(request reviewCandidateRequest) error {
	if utf8.RuneCountInString(request.ReasonCode) < 1 || utf8.RuneCountInString(request.ReasonCode) > 128 {
		return errors.New("reason_code length must be between 1 and 128")
	}
	if request.Note != nil && utf8.RuneCountInString(*request.Note) > 1024 {
		return errors.New("note length must not exceed 1024")
	}
	return nil
}
