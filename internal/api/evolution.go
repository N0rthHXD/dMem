package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type evolutionProposalRequest struct {
	EventID, SpaceID, Action               string
	ProposedContent, ValidFrom, ValidUntil *string
	MemoryType                             *string
	Sensitivity                            string
	RelationSpecs, MentionSpecs            json.RawMessage
	ProcessorIdentity, ProcessingVersion   string
	ProviderIdentity, ModelIdentity        *string
	Targets                                []domain.EvolutionTarget
	Evidence                               []domain.RelationEvidence
}

func (v *evolutionProposalRequest) UnmarshalJSON(data []byte) error {
	type request struct {
		EventID           string                    `json:"event_id"`
		SpaceID           string                    `json:"space_id"`
		Action            string                    `json:"action"`
		ProposedContent   *string                   `json:"proposed_content"`
		ValidFrom         *string                   `json:"valid_from"`
		ValidUntil        *string                   `json:"valid_until"`
		MemoryType        *string                   `json:"memory_type"`
		Sensitivity       string                    `json:"sensitivity"`
		RelationSpecs     json.RawMessage           `json:"relation_specs"`
		MentionSpecs      json.RawMessage           `json:"mention_specs"`
		ProcessorIdentity string                    `json:"processor_identity"`
		ProcessingVersion string                    `json:"processing_version"`
		ProviderIdentity  *string                   `json:"provider_identity"`
		ModelIdentity     *string                   `json:"model_identity"`
		Targets           []domain.EvolutionTarget  `json:"targets"`
		Evidence          []domain.RelationEvidence `json:"evidence"`
	}
	var r request
	if err := unmarshalStrict(data, &r); err != nil {
		return err
	}
	v.EventID, v.SpaceID, v.Action = r.EventID, r.SpaceID, r.Action
	v.ProposedContent, v.ValidFrom, v.ValidUntil, v.MemoryType = r.ProposedContent, r.ValidFrom, r.ValidUntil, r.MemoryType
	v.Sensitivity, v.RelationSpecs, v.MentionSpecs = r.Sensitivity, r.RelationSpecs, r.MentionSpecs
	v.ProcessorIdentity, v.ProcessingVersion, v.ProviderIdentity, v.ModelIdentity = r.ProcessorIdentity, r.ProcessingVersion, r.ProviderIdentity, r.ModelIdentity
	v.Targets, v.Evidence = r.Targets, r.Evidence
	return nil
}

type evolutionReviewRequest struct {
	ExpectedTargets  []domain.EvolutionTarget `json:"expected_targets"`
	ReplacementMode  *string                  `json:"replacement_mode"`
	SupersedeTargets bool                     `json:"supersede_targets"`
	ReasonCode       string                   `json:"reason_code"`
	Note             *string                  `json:"note"`
}

func (h *Handler) evolutionProposals(w http.ResponseWriter, r *http.Request) {
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
		var req evolutionProposalRequest
		if !h.decode(w, r, requestID, &req) {
			return
		}
		if !validUUID(req.EventID) || !validUUID(req.SpaceID) || !containsAPI([]string{"duplicate", "supplement", "replacement", "conflict"}, req.Action) {
			h.writeError(w, 422, "invalid_request", "invalid evolution proposal", requestID, "", false, nil)
			return
		}
		p := storage.CreateEvolutionProposalParams{EventID: req.EventID, SpaceID: req.SpaceID, Action: req.Action, ProposedContent: req.ProposedContent, ValidFrom: req.ValidFrom, ValidUntil: req.ValidUntil, MemoryType: req.MemoryType, Sensitivity: req.Sensitivity, RelationSpecs: req.RelationSpecs, MentionSpecs: req.MentionSpecs, ProcessorIdentity: req.ProcessorIdentity, ProcessingVersion: req.ProcessingVersion, ProviderIdentity: req.ProviderIdentity, ModelIdentity: req.ModelIdentity, Targets: req.Targets, Evidence: req.Evidence}
		identity := writeIdentity(caller, key, requestID, r.Method, "/v1/evolution-proposals", p)
		v, replay, err := h.repository.CreateEvolutionProposal(r.Context(), identity, p)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
		h.writeJSON(w, 201, v)
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
	state, action := r.URL.Query().Get("review_state"), r.URL.Query().Get("action")
	if state != "" && !containsAPI([]string{"pending", "confirmed", "rejected"}, state) || action != "" && !containsAPI([]string{"duplicate", "supplement", "replacement", "conflict"}, action) {
		h.writeError(w, 422, "invalid_request", "invalid evolution proposal filter", requestID, "", false, nil)
		return
	}
	v, err := h.repository.ListEvolutionProposals(r.Context(), storage.EvolutionProposalFilter{SpaceID: space, ReviewState: state, Action: action, Limit: limit, Cursor: cursor})
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, v)
}

func (h *Handler) evolutionProposal(w http.ResponseWriter, r *http.Request) {
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
		v, err := h.repository.GetEvolutionProposal(r.Context(), id)
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
	var req evolutionReviewRequest
	if !h.decode(w, r, requestID, &req) {
		return
	}
	if !validReviewTextAPI(req.ReasonCode, req.Note) {
		h.writeError(w, 422, "invalid_request", "invalid evolution review", requestID, "", false, nil)
		return
	}
	p := storage.ReviewEvolutionProposalParams{ExpectedTargets: req.ExpectedTargets, ReplacementMode: req.ReplacementMode, SupersedeTargets: req.SupersedeTargets, ReasonCode: req.ReasonCode, Note: req.Note}
	identity := writeIdentity(caller, key, requestID, r.Method, "/v1/evolution-proposals/{proposal_id}/"+action, p)
	var v domain.EvolutionProposal
	var replay bool
	var err error
	if action == "confirm" {
		v, replay, err = h.repository.ConfirmEvolutionProposal(r.Context(), identity, id, p)
	} else {
		v, replay, err = h.repository.RejectEvolutionProposal(r.Context(), identity, id, p)
	}
	if err != nil {
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
	status := 200
	if action == "confirm" {
		status = 201
	}
	h.writeJSON(w, status, v)
}

func validReviewTextAPI(reason string, note *string) bool {
	return len(reason) >= 1 && len(reason) <= 128 && (note == nil || len(*note) <= 1024)
}
