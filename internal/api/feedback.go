package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

const feedbackNoteLimit = 2000

type appendFeedbackRequest struct {
	SpaceID         string                          `json:"space_id"`
	MemoryID        string                          `json:"memory_id"`
	RevisionID      string                          `json:"revision_id"`
	Purpose         string                          `json:"feedback_purpose"`
	FeedbackType    string                          `json:"feedback_type"`
	ReasonCode      string                          `json:"reason_code"`
	Note            json.RawMessage                 `json:"note"`
	Decision        *domain.FeedbackDecisionContext `json:"decision,omitempty"`
	CreateProposal  bool                            `json:"create_proposal,omitempty"`
	SourceEventID   string                          `json:"source_event_id,omitempty"`
	ProposedContent string                          `json:"proposed_content,omitempty"`
}

func (h *Handler) feedback(w http.ResponseWriter, r *http.Request) {
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
		var request appendFeedbackRequest
		if !h.decode(w, r, requestID, &request) {
			return
		}
		params, err := validateFeedback(request)
		if err != nil {
			h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
			return
		}
		identity := writeIdentity(caller, key, requestID, r.Method, "/v1/feedback", params)
		result, replay, err := h.repository.AppendFeedback(r.Context(), identity, params)
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
	limit, cursor, ok := pageParams(w, r, requestID)
	if !ok {
		return
	}
	filter := storage.FeedbackFilter{CallerID: caller, SpaceID: spaceID, MemoryID: r.URL.Query().Get("memory_id"), RevisionID: r.URL.Query().Get("revision_id"), Purpose: r.URL.Query().Get("feedback_purpose"), FeedbackType: r.URL.Query().Get("feedback_type"), Limit: limit, Cursor: cursor}
	if filter.MemoryID != "" && !validUUID(filter.MemoryID) || filter.RevisionID != "" && !validUUID(filter.RevisionID) || filter.Purpose != "" && !isFeedbackPurpose(filter.Purpose) || filter.FeedbackType != "" && !feedbackTypes[filter.FeedbackType] {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "feedback filters are invalid", requestID, "", false, nil)
		return
	}
	result, err := h.repository.ListFeedback(r.Context(), filter)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// feedbackTypes is the union of types across all purposes, used only for the
// list filter. Submit validation uses the closed purpose/type/reason contract.
var feedbackTypes = map[string]bool{"confirm": true, "correct": true, "contest": true, "supplement": true, "useful": true, "irrelevant": true, "supported": true, "unsupported": true}

var feedbackSignals = map[string]bool{"keyword": true, "semantic": true, "graph": true}

func isFeedbackPurpose(purpose string) bool {
	for _, candidate := range domain.FeedbackPurposes() {
		if candidate == purpose {
			return true
		}
	}
	return false
}

func validateFeedback(request appendFeedbackRequest) (storage.AppendFeedbackParams, error) {
	if !validUUID(request.SpaceID) || !validUUID(request.MemoryID) || !validUUID(request.RevisionID) {
		return storage.AppendFeedbackParams{}, errors.New("space_id, memory_id, and revision_id must be UUIDs")
	}
	if !domain.ValidFeedbackCombination(request.Purpose, request.FeedbackType, request.ReasonCode) {
		return storage.AppendFeedbackParams{}, errors.New("feedback_purpose, feedback_type, or reason_code is invalid")
	}
	var note *string
	if request.Note != nil {
		var value string
		if isNull(request.Note) || json.Unmarshal(request.Note, &value) != nil || utf8.RuneCountInString(value) > feedbackNoteLimit {
			return storage.AppendFeedbackParams{}, errors.New("note must be a string of at most 2000 characters")
		}
		note = &value
	}
	decision, err := validateFeedbackDecision(request.Purpose, request.Decision)
	if err != nil {
		return storage.AppendFeedbackParams{}, err
	}
	if request.CreateProposal && !domain.CanProposeEvolution(request.Purpose, request.FeedbackType) {
		return storage.AppendFeedbackParams{}, errors.New("this feedback_purpose and feedback_type cannot create a proposal")
	}
	if request.SourceEventID != "" && !validUUID(request.SourceEventID) {
		return storage.AppendFeedbackParams{}, errors.New("source_event_id must be a UUID")
	}
	if utf8.RuneCountInString(request.ProposedContent) > 100000 {
		return storage.AppendFeedbackParams{}, errors.New("proposed_content is too long")
	}
	return storage.AppendFeedbackParams{SpaceID: request.SpaceID, MemoryID: request.MemoryID, RevisionID: request.RevisionID, Purpose: request.Purpose, FeedbackType: request.FeedbackType, ReasonCode: request.ReasonCode, Note: note, Decision: decision, CreateProposal: request.CreateProposal, SourceEventID: request.SourceEventID, ProposedContent: request.ProposedContent}, nil
}

func validateFeedbackDecision(purpose string, decision *domain.FeedbackDecisionContext) (*domain.FeedbackDecisionContext, error) {
	if !domain.FeedbackPurposeRequiresDecision(purpose) {
		if decision != nil {
			return nil, errors.New("memory_quality_feedback must not carry retrieval decision context")
		}
		return nil, nil
	}
	if decision == nil {
		return nil, errors.New("retrieval and answer feedback require a decision context")
	}
	if !validUUID(decision.SearchOperationID) || len(decision.QueryHash) != 64 || decision.QueryType == "" || decision.RankingIdentity == "" || decision.FeatureIdentity == "" || decision.ContextIdentity == "" || decision.ResultRank < 1 || (decision.SelectionState != "selected" && decision.SelectionState != "rejected") || len(decision.MatchedSignals) == 0 {
		return nil, errors.New("decision context is invalid")
	}
	for _, entityID := range decision.EntityIDs {
		if !validUUID(entityID) {
			return nil, errors.New("decision context entity_ids must be UUIDs")
		}
	}
	signals := append([]string(nil), decision.MatchedSignals...)
	sort.Strings(signals)
	for i, signal := range signals {
		if !feedbackSignals[signal] || (i > 0 && signals[i-1] == signal) {
			return nil, errors.New("decision context matched_signals is invalid")
		}
	}
	if purpose == domain.FeedbackPurposeAnswer && decision.AnswerIdentity == "" {
		return nil, errors.New("answer feedback requires an answer_identity")
	}
	if purpose == domain.FeedbackPurposeRetrieval && decision.AnswerIdentity != "" {
		return nil, errors.New("retrieval feedback must not carry an answer_identity")
	}
	result := *decision
	result.EntityIDs = append([]string(nil), decision.EntityIDs...)
	sort.Strings(result.EntityIDs)
	result.MatchedSignals = signals
	return &result, nil
}

func (h *Handler) feedbackAggregateHealth(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	result, err := h.repository.FeedbackAggregateHealth(r.Context())
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) rebuildFeedbackAggregates(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	if !ok {
		return
	}
	identity := writeIdentity(caller, key, requestID, r.Method, "/v1/feedback/aggregates/rebuild", struct{}{})
	result, replay, err := h.repository.CreateFeedbackRebuildJob(r.Context(), identity)
	if errors.Is(err, storage.ErrJobAlreadyActive) {
		h.writeError(w, http.StatusConflict, "job_already_active", "an active feedback aggregate rebuild job already exists", requestID, "", false, map[string]any{"job_id": result.JobID})
		return
	}
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(replay))
	h.writeJSON(w, http.StatusAccepted, result)
}
