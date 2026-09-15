package api

import (
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

func (h *Handler) conflictCases(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	space := r.URL.Query().Get("space_id")
	if !validUUID(space) {
		h.writeError(w, 422, "invalid_request", "space_id is required and must be a UUID", requestID, "", false, nil)
		return
	}
	limit, cursor, ok := pageParams(w, r, requestID)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	if state != "" && !containsAPI([]string{"open", "resolved", "dismissed"}, state) {
		h.writeError(w, 422, "invalid_request", "invalid conflict state", requestID, "", false, nil)
		return
	}
	v, err := h.repository.ListConflictCases(r.Context(), storage.ConflictCaseFilter{SpaceID: space, State: state, Limit: limit, Cursor: cursor})
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, v)
}

func (h *Handler) conflictCase(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	id := r.PathValue("case_id")
	if !validUUID(id) {
		h.writeError(w, 422, "invalid_request", "case_id must be a UUID", requestID, "", false, nil)
		return
	}
	action := r.PathValue("action")
	if action == "" {
		v, err := h.repository.GetConflictCase(r.Context(), id)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		h.writeJSON(w, 200, v)
		return
	}
	if action != "resolve" {
		h.notFound(w, r)
		return
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	if !ok {
		return
	}
	var req domain.ConflictResolutionCommand
	if !h.decode(w, r, requestID, &req) {
		return
	}
	if !containsAPI([]string{"keep_both", "dismiss", "revise_one", "supersede", "reconcile"}, req.ResolutionType) {
		h.writeError(w, 422, "unsupported", "unsupported conflict resolution", requestID, "", false, nil)
		return
	}
	if err := validateConflictResolution(&req); err != nil {
		h.writeError(w, 422, "invalid_request", err.Error(), requestID, "", false, nil)
		return
	}
	p := storage.ResolveConflictParams(req)
	identity := writeIdentity(caller, key, requestID, r.Method, "/v1/conflict-cases/{case_id}/resolve", p)
	v, replay, err := h.repository.ResolveConflict(r.Context(), identity, id, p)
	if err != nil {
		if errors.Is(err, storage.ErrUnsupportedAction) {
			h.writeError(w, 422, "unsupported", "resolution requires an explicit reviewed evolution proposal", requestID, "", false, nil)
			return
		}
		if errors.Is(err, storage.ErrFrozenRevision) {
			h.writeError(w, 409, "revision_conflict", "memory is frozen", requestID, "", false, map[string]any{"reason": "frozen"})
			return
		}
		if errors.Is(err, storage.ErrRevisionConflict) {
			h.writeError(w, 409, "revision_conflict", "expected revision is not current", requestID, "", false, map[string]any{"reason": "revision_changed"})
			return
		}
		h.repositoryError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
	h.writeJSON(w, 200, v)
}

func validateConflictResolution(req *domain.ConflictResolutionCommand) error {
	if utf8.RuneCountInString(req.Reason) < 1 || utf8.RuneCountInString(req.Reason) > 1024 {
		return errors.New("reason length must be between 1 and 1024")
	}
	destructive := req.ResolutionType == "revise_one" || req.ResolutionType == "supersede" || req.ResolutionType == "reconcile"
	if !destructive {
		if req.SelectedTarget != nil || req.ProposedContent != nil || req.ValidFrom != nil || req.ValidUntil != nil || len(req.Evidence) != 0 || req.ReplacementMode != nil {
			return errors.New("resolution content is not valid for this resolution type")
		}
		return nil
	}
	if req.ProposedContent == nil {
		return errors.New("proposed_content is required")
	}
	from, until, err := validateMemoryContent(*req.ProposedContent, req.ValidFrom, req.ValidUntil, req.Reason)
	if err != nil {
		return err
	}
	req.ValidFrom, req.ValidUntil = from, until
	if len(req.Evidence) == 0 {
		return errors.New("evidence must contain at least one item")
	}
	for _, evidence := range req.Evidence {
		if !validUUID(evidence.EventID) || !evidence.Role.Valid() || (evidence.StartOffset == nil) != (evidence.EndOffset == nil) || evidence.StartOffset != nil && (*evidence.StartOffset < 0 || *evidence.EndOffset <= *evidence.StartOffset) {
			return errors.New("invalid evidence")
		}
	}
	if req.ResolutionType == "reconcile" {
		if req.SelectedTarget != nil || req.ReplacementMode != nil {
			return errors.New("reconcile does not accept selected_target or replacement_mode")
		}
		return nil
	}
	if req.SelectedTarget == nil || !validUUID(req.SelectedTarget.MemoryID) || !validUUID(req.SelectedTarget.ExpectedRevisionID) {
		return errors.New("selected_target is required and must identify a participant revision")
	}
	if req.ResolutionType == "supersede" {
		if req.ReplacementMode == nil || !containsAPI([]string{"same_memory_revision", "new_memory"}, *req.ReplacementMode) {
			return errors.New("replacement_mode must be same_memory_revision or new_memory")
		}
	} else if req.ReplacementMode != nil {
		return errors.New("replacement_mode is only valid for supersede")
	}
	return nil
}
