package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

func (h *Handler) job(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	callerID, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	jobID := r.PathValue("job_id")
	if !validUUID(jobID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "job_id must be a UUID", requestID, "", false, nil)
		return
	}
	if r.Method == http.MethodGet {
		job, err := h.repository.GetJob(r.Context(), jobID)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		h.writeJSON(w, http.StatusOK, job)
		return
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	if !ok {
		return
	}
	action := r.PathValue("action")
	if action != "cancel" && action != "retry" {
		h.notFound(w, r)
		return
	}
	route := "/v1/jobs/{job_id}/" + action
	identity := writeIdentity(callerID, key, requestID, r.Method, route, struct {
		JobID string `json:"job_id"`
	}{jobID})
	var replay bool
	job, replay, err := h.transitionJob(r, identity, jobID, action)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidJobState) {
			h.writeError(w, http.StatusConflict, "conflict", "job state does not allow this operation", requestID, "", false, nil)
			return
		}
		h.repositoryError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
	status := http.StatusOK
	if action == "retry" {
		status = http.StatusAccepted
	}
	h.writeJSON(w, status, job)
}

func (h *Handler) transitionJob(r *http.Request, identity storage.WriteIdentity, jobID, action string) (domain.Job, bool, error) {
	if action == "cancel" {
		return h.repository.CancelJob(r.Context(), identity, jobID)
	}
	return h.repository.RetryJob(r.Context(), identity, jobID)
}
