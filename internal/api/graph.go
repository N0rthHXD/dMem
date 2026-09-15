package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type factProposalRequest struct {
	EventID           string                      `json:"event_id"`
	SpaceID           string                      `json:"space_id"`
	SubjectEntityID   string                      `json:"subject_entity_id"`
	Predicate         string                      `json:"predicate"`
	ObjectEntityID    *string                     `json:"object_entity_id"`
	ObjectLiteral     *string                     `json:"object_literal"`
	ObjectLiteralType *string                     `json:"object_literal_type"`
	SourceMemoryID    string                      `json:"source_memory_id"`
	SourceRevisionID  string                      `json:"source_revision_id"`
	Confidence        *float64                    `json:"confidence"`
	ValidFrom         *string                     `json:"valid_from"`
	ValidUntil        *string                     `json:"valid_until"`
	Evidence          []domain.EntityFactEvidence `json:"evidence"`
	ProcessorIdentity string                      `json:"processor_identity"`
	ProcessingVersion string                      `json:"processing_version"`
	ProviderIdentity  *string                     `json:"provider_identity"`
	ModelIdentity     *string                     `json:"model_identity"`
}

type factReviewRequest struct {
	ReasonCode string  `json:"reason_code"`
	Note       *string `json:"note"`
}

func (h *Handler) factProposals(w http.ResponseWriter, r *http.Request) {
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
		var req factProposalRequest
		if !h.decode(w, r, requestID, &req) {
			return
		}
		params, err := validateFactProposalRequest(req)
		if err != nil {
			h.writeError(w, 422, "invalid_request", err.Error(), requestID, "", false, nil)
			return
		}
		identity := writeIdentity(caller, key, requestID, r.Method, "/v1/fact-proposals", params)
		result, replay, err := h.repository.CreateFactProposal(r.Context(), identity, params)
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
	state, predicate := r.URL.Query().Get("review_state"), r.URL.Query().Get("predicate")
	if state != "" && !containsAPI([]string{"pending", "confirmed", "rejected"}, state) || predicate != "" && !validPredicateAPI(predicate) {
		h.writeError(w, 422, "invalid_request", "invalid fact proposal filter", requestID, "", false, nil)
		return
	}
	result, err := h.repository.ListFactProposals(r.Context(), storage.FactProposalFilter{SpaceID: space, ReviewState: state, Predicate: predicate, Limit: limit, Cursor: cursor})
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, result)
}

func (h *Handler) factProposal(w http.ResponseWriter, r *http.Request) {
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
		v, err := h.repository.GetFactProposal(r.Context(), id)
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
	var req factReviewRequest
	if !h.decode(w, r, requestID, &req) {
		return
	}
	if utf8.RuneCountInString(req.ReasonCode) < 1 || utf8.RuneCountInString(req.ReasonCode) > 128 || req.Note != nil && utf8.RuneCountInString(*req.Note) > 1024 {
		h.writeError(w, 422, "invalid_request", "invalid fact review", requestID, "", false, nil)
		return
	}
	params := storage.ReviewFactProposalParams{ReasonCode: req.ReasonCode, Note: req.Note}
	identity := writeIdentity(caller, key, requestID, r.Method, "/v1/fact-proposals/{proposal_id}/"+action, params)
	var v any
	var replay bool
	var err error
	if action == "confirm" {
		v, replay, err = h.repository.ConfirmFactProposal(r.Context(), identity, id, params)
	} else {
		v, replay, err = h.repository.RejectFactProposal(r.Context(), identity, id, params)
	}
	if err != nil {
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

func (h *Handler) entityFacts(w http.ResponseWriter, r *http.Request) {
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
	f := storage.FactFilter{SpaceID: space, State: r.URL.Query().Get("state"), Predicate: r.URL.Query().Get("predicate"), SubjectEntityID: r.URL.Query().Get("subject_entity_id"), ObjectEntityID: r.URL.Query().Get("object_entity_id"), Direction: r.URL.Query().Get("direction"), Cursor: cursor, Limit: limit}
	if f.State != "" && !containsAPI([]string{"active", "dismissed", "superseded"}, f.State) || f.Predicate != "" && !validPredicateAPI(f.Predicate) || f.Direction != "" && !containsAPI([]string{"incoming", "outgoing", "both"}, f.Direction) || f.SubjectEntityID != "" && !validUUID(f.SubjectEntityID) || f.ObjectEntityID != "" && !validUUID(f.ObjectEntityID) {
		h.writeError(w, 422, "invalid_request", "invalid entity fact filter", requestID, "", false, nil)
		return
	}
	if value := r.URL.Query().Get("reference_time"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			h.writeError(w, 422, "invalid_request", "reference_time must be RFC3339", requestID, "", false, nil)
			return
		}
		f.ReferenceTime = formatAPITime(parsed)
	}
	result, err := h.repository.ListEntityFacts(r.Context(), f)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, result)
}

func (h *Handler) entityFact(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	id, space := r.PathValue("fact_id"), r.URL.Query().Get("space_id")
	if !validUUID(id) || !validUUID(space) {
		h.writeError(w, 422, "invalid_request", "fact_id and space_id must be UUIDs", requestID, "", false, nil)
		return
	}
	v, err := h.repository.GetEntityFact(r.Context(), id)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	if v.SpaceID != space {
		h.repositoryError(w, storage.ErrNotFound, requestID)
		return
	}
	h.writeJSON(w, 200, v)
}

func (h *Handler) rebuildGraphIndex(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	if !ok {
		return
	}
	identity := writeIdentity(caller, key, requestID, r.Method, "/v1/indexes/graph/rebuild", struct{}{})
	v, replay, err := h.repository.CreateGraphIndexRebuildJob(r.Context(), identity)
	if err != nil {
		if errors.Is(err, storage.ErrJobAlreadyActive) {
			h.writeError(w, 409, "job_already_active", "an active graph index rebuild job already exists", requestID, "", false, map[string]any{"job_id": v.JobID})
			return
		}
		h.repositoryError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
	h.writeJSON(w, 202, v)
}

func validateFactProposalRequest(v factProposalRequest) (storage.CreateFactProposalParams, error) {
	p := storage.CreateFactProposalParams{EventID: v.EventID, SpaceID: v.SpaceID, SubjectEntityID: v.SubjectEntityID, Predicate: v.Predicate, ObjectEntityID: v.ObjectEntityID, ObjectLiteral: v.ObjectLiteral, ObjectLiteralType: v.ObjectLiteralType, SourceMemoryID: v.SourceMemoryID, SourceRevisionID: v.SourceRevisionID, Confidence: v.Confidence, ValidFrom: v.ValidFrom, ValidUntil: v.ValidUntil, Evidence: v.Evidence, ProcessorIdentity: v.ProcessorIdentity, ProviderIdentity: v.ProviderIdentity, ModelIdentity: v.ModelIdentity, ProcessingVersion: v.ProcessingVersion}
	if !validUUID(p.EventID) || !validUUID(p.SpaceID) || !validUUID(p.SubjectEntityID) || !validUUID(p.SourceMemoryID) || !validUUID(p.SourceRevisionID) || !validPredicateAPI(p.Predicate) || p.ProcessorIdentity == "" || p.ProcessingVersion == "" {
		return p, errors.New("invalid fact proposal")
	}
	if p.ObjectEntityID != nil && !validUUID(*p.ObjectEntityID) {
		return p, errors.New("object_entity_id must be a UUID")
	}
	if p.ObjectEntityID != nil && p.ObjectLiteral != nil || p.ObjectEntityID == nil && p.ObjectLiteral == nil {
		return p, errors.New("exactly one of object_entity_id and object_literal is required")
	}
	if p.ObjectLiteral != nil && (strings.TrimSpace(*p.ObjectLiteral) == "" || p.ObjectLiteralType == nil || !containsAPI([]string{"string", "number", "boolean"}, *p.ObjectLiteralType)) {
		return p, errors.New("object_literal requires a valid object_literal_type")
	}
	return p, nil
}

func validPredicateAPI(v string) bool {
	return containsAPI([]string{"created_by", "depends_on", "has_participant_count", "has_status", "is_active", "located_at", "member_of", "owned_by", "participates_in"}, v)
}
