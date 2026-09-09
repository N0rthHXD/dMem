package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/storage"
)

type entityProposalRequest struct {
	EventID              string  `json:"event_id"`
	SpaceID              string  `json:"space_id"`
	ProposalKind         string  `json:"proposal_kind"`
	EntityID             *string `json:"entity_id"`
	EntityType           string  `json:"entity_type"`
	CanonicalName        string  `json:"canonical_name"`
	Alias                string  `json:"alias"`
	NormalizationVersion string  `json:"normalization_version"`
	MentionMemoryID      *string `json:"mention_memory_id"`
	MentionRevisionID    *string `json:"mention_revision_id"`
	MentionEventID       *string `json:"mention_event_id"`
	StartOffset          *int    `json:"start_offset"`
	EndOffset            *int    `json:"end_offset"`
	MentionType          string  `json:"mention_type"`
	Sensitivity          string  `json:"sensitivity"`
	ProcessorIdentity    string  `json:"processor_identity"`
	ProcessingVersion    string  `json:"processing_version"`
	ProviderIdentity     *string `json:"provider_identity"`
	ModelIdentity        *string `json:"model_identity"`
}

type entityReviewRequest struct {
	ReasonCode string  `json:"reason_code"`
	Note       *string `json:"note"`
}

func (h *Handler) entityProposals(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		key, ok := h.requireIdempotency(w, r, requestID)
		if !ok {
			return
		}
		var req entityProposalRequest
		if !h.decode(w, r, requestID, &req) {
			return
		}
		params, err := validateEntityProposalRequest(req)
		if err != nil {
			h.writeError(w, 422, "invalid_request", err.Error(), requestID, "", false, nil)
			return
		}
		identity := writeIdentity(caller, key, requestID, r.Method, "/v1/entity-proposals", params)
		result, replay, err := h.repository.CreateEntityProposal(r.Context(), identity, params)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
		h.writeJSON(w, 201, result)
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
	state, kind := r.URL.Query().Get("review_state"), r.URL.Query().Get("proposal_kind")
	if state != "" && !containsAPI([]string{"pending", "confirmed", "rejected"}, state) || kind != "" && !containsAPI([]string{"entity", "alias", "mention"}, kind) {
		h.writeError(w, 422, "invalid_request", "invalid entity proposal filter", requestID, "", false, nil)
		return
	}
	result, err := h.repository.ListEntityProposals(r.Context(), storage.EntityProposalFilter{SpaceID: space, ReviewState: state, ProposalKind: kind, Limit: limit, Cursor: cursor})
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, result)
}

func (h *Handler) entityProposal(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	id := r.PathValue("proposal_id")
	if !validUUID(id) {
		h.writeError(w, 422, "invalid_request", "proposal_id must be a UUID", requestID, "", false, nil)
		return
	}
	action := r.PathValue("action")
	if action == "" {
		v, err := h.repository.GetEntityProposal(r.Context(), id)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		h.writeJSON(w, 200, v)
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
	var req entityReviewRequest
	if !h.decode(w, r, requestID, &req) {
		return
	}
	if utf8.RuneCountInString(req.ReasonCode) < 1 || utf8.RuneCountInString(req.ReasonCode) > 128 || req.Note != nil && utf8.RuneCountInString(*req.Note) > 1024 {
		h.writeError(w, 422, "invalid_request", "invalid entity review", requestID, "", false, nil)
		return
	}
	params := storage.ReviewEntityProposalParams{ReasonCode: req.ReasonCode, Note: req.Note}
	identity := writeIdentity(caller, key, requestID, r.Method, "/v1/entity-proposals/{proposal_id}/"+action, params)
	var v any
	var replay bool
	var err error
	if action == "confirm" {
		v, replay, err = h.repository.ConfirmEntityProposal(r.Context(), identity, id, params)
	} else {
		v, replay, err = h.repository.RejectEntityProposal(r.Context(), identity, id, params)
	}
	if err != nil {
		if errors.Is(err, storage.ErrFrozenRevision) {
			h.writeError(w, 409, "revision_conflict", "memory is frozen", requestID, "", false, map[string]any{"reason": "frozen"})
			return
		}
		h.repositoryError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
	status := 200
	if action == "confirm" {
		status = 201
	}
	h.writeJSON(w, status, v)
}

func validateEntityProposalRequest(v entityProposalRequest) (storage.CreateEntityProposalParams, error) {
	p := storage.CreateEntityProposalParams{EventID: v.EventID, SpaceID: v.SpaceID, ProposalKind: v.ProposalKind, EntityID: v.EntityID, EntityType: v.EntityType, CanonicalName: strings.TrimSpace(v.CanonicalName), Alias: strings.TrimSpace(v.Alias), NormalizationVersion: v.NormalizationVersion, MentionMemoryID: v.MentionMemoryID, MentionRevisionID: v.MentionRevisionID, MentionEventID: v.MentionEventID, StartOffset: v.StartOffset, EndOffset: v.EndOffset, MentionType: v.MentionType, Sensitivity: v.Sensitivity, ProcessorIdentity: v.ProcessorIdentity, ProviderIdentity: v.ProviderIdentity, ModelIdentity: v.ModelIdentity, ProcessingVersion: v.ProcessingVersion}
	if !validUUID(p.EventID) || !validUUID(p.SpaceID) || !containsAPI([]string{"entity", "alias", "mention"}, p.ProposalKind) || p.ProcessorIdentity == "" || p.ProcessingVersion == "" {
		return p, errors.New("invalid entity proposal")
	}
	if p.Sensitivity == "" {
		p.Sensitivity = "normal"
	}
	if p.Sensitivity != "normal" && p.Sensitivity != "sensitive" {
		return p, errors.New("invalid sensitivity")
	}
	for _, id := range []*string{p.EntityID, p.MentionMemoryID, p.MentionRevisionID, p.MentionEventID} {
		if id != nil && !validUUID(*id) {
			return p, errors.New("entity proposal IDs must be UUIDs")
		}
	}
	return p, nil
}

func (h *Handler) entities(w http.ResponseWriter, r *http.Request) {
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
	state, kind := r.URL.Query().Get("state"), r.URL.Query().Get("entity_type")
	mentions := r.URL.Query().Get("include_mentions") == "true"
	result, err := h.repository.ListEntities(r.Context(), storage.EntityFilter{SpaceID: space, State: state, EntityType: kind, Limit: limit, Cursor: cursor, IncludeMentions: mentions})
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, result)
}
func (h *Handler) entity(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	id := r.PathValue("entity_id")
	if !validUUID(id) {
		h.writeError(w, 422, "invalid_request", "entity_id must be a UUID", requestID, "", false, nil)
		return
	}
	if r.PathValue("action") == "merge" {
		h.writeError(w, http.StatusNotImplemented, "unsupported_operation", "entity merge is not supported", requestID, "", false, nil)
		return
	}
	spaceID := r.URL.Query().Get("space_id")
	if !validUUID(spaceID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "space_id is required and must be a UUID", requestID, "", false, nil)
		return
	}
	result, err := h.repository.GetEntityInSpace(r.Context(), id, spaceID, r.URL.Query().Get("include_mentions") != "false")
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, result)
}
func containsAPI(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
