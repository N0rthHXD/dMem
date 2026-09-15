package api

import (
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type relationProposalRequest struct {
	EventID, SpaceID, RelationType       string
	SourceMemoryID, SourceRevisionID     string
	TargetMemoryID, TargetRevisionID     string
	Confidence                           *float64
	Evidence                             []domain.RelationEvidence
	ProcessorIdentity, ProcessingVersion string
	ProviderIdentity, ModelIdentity      *string
}

func (v *relationProposalRequest) UnmarshalJSON(data []byte) error {
	type request struct {
		EventID           string                    `json:"event_id"`
		SpaceID           string                    `json:"space_id"`
		RelationType      string                    `json:"relation_type"`
		SourceMemoryID    string                    `json:"source_memory_id"`
		SourceRevisionID  string                    `json:"source_revision_id"`
		TargetMemoryID    string                    `json:"target_memory_id"`
		TargetRevisionID  string                    `json:"target_revision_id"`
		Confidence        *float64                  `json:"confidence"`
		Evidence          []domain.RelationEvidence `json:"evidence"`
		ProcessorIdentity string                    `json:"processor_identity"`
		ProcessingVersion string                    `json:"processing_version"`
		ProviderIdentity  *string                   `json:"provider_identity"`
		ModelIdentity     *string                   `json:"model_identity"`
	}
	var value request
	if err := unmarshalStrict(data, &value); err != nil {
		return err
	}
	v.EventID, v.SpaceID, v.RelationType = value.EventID, value.SpaceID, value.RelationType
	v.SourceMemoryID, v.SourceRevisionID, v.TargetMemoryID, v.TargetRevisionID = value.SourceMemoryID, value.SourceRevisionID, value.TargetMemoryID, value.TargetRevisionID
	v.Confidence, v.Evidence, v.ProcessorIdentity, v.ProcessingVersion = value.Confidence, value.Evidence, value.ProcessorIdentity, value.ProcessingVersion
	v.ProviderIdentity, v.ModelIdentity = value.ProviderIdentity, value.ModelIdentity
	return nil
}

type relationReviewRequest struct {
	ReasonCode string  `json:"reason_code"`
	Note       *string `json:"note"`
}

func (h *Handler) relationProposals(w http.ResponseWriter, r *http.Request) {
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
		var req relationProposalRequest
		if !h.decode(w, r, requestID, &req) {
			return
		}
		p := storage.CreateRelationProposalParams{EventID: req.EventID, SpaceID: req.SpaceID, RelationType: req.RelationType, SourceMemoryID: req.SourceMemoryID, SourceRevisionID: req.SourceRevisionID, TargetMemoryID: req.TargetMemoryID, TargetRevisionID: req.TargetRevisionID, Confidence: req.Confidence, Evidence: req.Evidence, ProcessorIdentity: req.ProcessorIdentity, ProcessingVersion: req.ProcessingVersion, ProviderIdentity: req.ProviderIdentity, ModelIdentity: req.ModelIdentity}
		if err := validateRelationProposalRequest(p); err != nil {
			h.writeError(w, 422, "invalid_request", err.Error(), requestID, "", false, nil)
			return
		}
		identity := writeIdentity(caller, key, requestID, r.Method, "/v1/relation-proposals", p)
		result, replay, err := h.repository.CreateRelationProposal(r.Context(), identity, p)
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
	state, kind := r.URL.Query().Get("review_state"), r.URL.Query().Get("relation_type")
	if state != "" && !containsAPI([]string{"pending", "confirmed", "rejected"}, state) || kind != "" && !validRelationTypeAPI(kind) {
		h.writeError(w, 422, "invalid_request", "invalid relation proposal filter", requestID, "", false, nil)
		return
	}
	result, err := h.repository.ListRelationProposals(r.Context(), storage.RelationProposalFilter{SpaceID: space, ReviewState: state, RelationType: kind, Limit: limit, Cursor: cursor})
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, result)
}

func (h *Handler) relationProposal(w http.ResponseWriter, r *http.Request) {
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
		v, err := h.repository.GetRelationProposal(r.Context(), id)
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
	var req relationReviewRequest
	if !h.decode(w, r, requestID, &req) {
		return
	}
	if utf8.RuneCountInString(req.ReasonCode) < 1 || utf8.RuneCountInString(req.ReasonCode) > 128 || req.Note != nil && utf8.RuneCountInString(*req.Note) > 1024 {
		h.writeError(w, 422, "invalid_request", "invalid relation review", requestID, "", false, nil)
		return
	}
	params := storage.ReviewRelationProposalParams{ReasonCode: req.ReasonCode, Note: req.Note}
	identity := writeIdentity(caller, key, requestID, r.Method, "/v1/relation-proposals/{proposal_id}/"+action, params)
	var v domain.RelationProposal
	var replay bool
	var err error
	if action == "confirm" {
		v, replay, err = h.repository.ConfirmRelationProposal(r.Context(), identity, id, params)
	} else {
		v, replay, err = h.repository.RejectRelationProposal(r.Context(), identity, id, params)
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

func (h *Handler) relations(w http.ResponseWriter, r *http.Request) {
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
	f := storage.RelationFilter{SpaceID: space, State: r.URL.Query().Get("state"), RelationType: r.URL.Query().Get("relation_type"), MemoryID: r.URL.Query().Get("memory_id"), RevisionID: r.URL.Query().Get("revision_id"), Direction: r.URL.Query().Get("direction"), Limit: limit, Cursor: cursor}
	if f.State != "" && !containsAPI([]string{"active", "dismissed"}, f.State) || f.RelationType != "" && !validRelationTypeAPI(f.RelationType) || f.Direction != "" && !containsAPI([]string{"incoming", "outgoing", "both"}, f.Direction) || f.MemoryID != "" && !validUUID(f.MemoryID) || f.RevisionID != "" && !validUUID(f.RevisionID) {
		h.writeError(w, 422, "invalid_request", "invalid relation filter", requestID, "", false, nil)
		return
	}
	v, err := h.repository.ListRelations(r.Context(), f)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, v)
}

func (h *Handler) relation(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	id, space := r.PathValue("relation_id"), r.URL.Query().Get("space_id")
	if !validUUID(id) || !validUUID(space) {
		h.writeError(w, 422, "invalid_request", "relation_id and space_id must be UUIDs", requestID, "", false, nil)
		return
	}
	v, err := h.repository.GetRelationInSpace(r.Context(), id, space)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, v)
}

func validateRelationProposalRequest(p storage.CreateRelationProposalParams) error {
	if !validUUID(p.EventID) || !validUUID(p.SpaceID) || !validUUID(p.SourceMemoryID) || !validUUID(p.SourceRevisionID) || !validUUID(p.TargetMemoryID) || !validUUID(p.TargetRevisionID) || !validRelationTypeAPI(p.RelationType) || p.ProcessorIdentity == "" || p.ProcessingVersion == "" {
		return errors.New("invalid relation proposal")
	}
	return nil
}
func validRelationTypeAPI(v string) bool {
	return containsAPI([]string{"supports", "contradicts", "supersedes", "derived_from"}, v)
}
