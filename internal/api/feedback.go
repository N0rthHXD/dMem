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
	SpaceID         string                        `json:"space_id"`
	MemoryID        string                        `json:"memory_id"`
	RevisionID      string                        `json:"revision_id"`
	FeedbackType    string                        `json:"feedback_type"`
	ReasonCode      string                        `json:"reason_code"`
	Note            json.RawMessage               `json:"note"`
	Search          *domain.FeedbackSearchContext `json:"search,omitempty"`
	CreateProposal  bool                          `json:"create_proposal,omitempty"`
	SourceEventID   string                        `json:"source_event_id,omitempty"`
	ProposedContent string                        `json:"proposed_content,omitempty"`
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
	filter := storage.FeedbackFilter{CallerID: caller, SpaceID: spaceID, MemoryID: r.URL.Query().Get("memory_id"), RevisionID: r.URL.Query().Get("revision_id"), FeedbackType: r.URL.Query().Get("feedback_type"), Limit: limit, Cursor: cursor}
	if filter.MemoryID != "" && !validUUID(filter.MemoryID) || filter.RevisionID != "" && !validUUID(filter.RevisionID) || filter.FeedbackType != "" && !feedbackTypes[filter.FeedbackType] {
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

var feedbackTypes = map[string]bool{"useful": true, "irrelevant": true, "correct": true, "supplement": true, "contest": true}
var feedbackReasons = map[string]bool{"answered_query": true, "relevant_context": true, "not_relevant": true, "fact_incorrect": true, "fact_incomplete": true, "fact_outdated": true, "conflicts_with_evidence": true, "duplicate": true, "other": true}

func validateFeedback(request appendFeedbackRequest) (storage.AppendFeedbackParams, error) {
	if !validUUID(request.SpaceID) || !validUUID(request.MemoryID) || !validUUID(request.RevisionID) {
		return storage.AppendFeedbackParams{}, errors.New("space_id, memory_id, and revision_id must be UUIDs")
	}
	if !feedbackTypes[request.FeedbackType] || !feedbackReasons[request.ReasonCode] {
		return storage.AppendFeedbackParams{}, errors.New("feedback_type or reason_code is invalid")
	}
	var note *string
	if request.Note != nil {
		var value string
		if isNull(request.Note) || json.Unmarshal(request.Note, &value) != nil || utf8.RuneCountInString(value) > feedbackNoteLimit {
			return storage.AppendFeedbackParams{}, errors.New("note must be a string of at most 2000 characters")
		}
		note = &value
	}
	if request.Search != nil {
		s := request.Search
		if !validUUID(s.SearchOperationID) || len(s.QueryHash) != 64 || s.ResultRank < 1 || s.RankingVersion == "" || len(s.MatchedChannels) == 0 {
			return storage.AppendFeedbackParams{}, errors.New("search context is invalid")
		}
		allowed, seen := map[string]bool{"keyword": true, "semantic": true, "graph": true}, map[string]bool{}
		for _, channel := range s.MatchedChannels {
			if !allowed[channel] || seen[channel] {
				return storage.AppendFeedbackParams{}, errors.New("matched_channels is invalid")
			}
			seen[channel] = true
		}
		sort.Strings(s.MatchedChannels)
	}
	canPropose := request.FeedbackType == "correct" || request.FeedbackType == "supplement" || request.FeedbackType == "contest"
	if request.CreateProposal && !canPropose {
		return storage.AppendFeedbackParams{}, errors.New("this feedback_type cannot create a proposal")
	}
	if request.SourceEventID != "" && !validUUID(request.SourceEventID) {
		return storage.AppendFeedbackParams{}, errors.New("source_event_id must be a UUID")
	}
	if utf8.RuneCountInString(request.ProposedContent) > 100000 {
		return storage.AppendFeedbackParams{}, errors.New("proposed_content is too long")
	}
	return storage.AppendFeedbackParams{SpaceID: request.SpaceID, MemoryID: request.MemoryID, RevisionID: request.RevisionID, FeedbackType: request.FeedbackType, ReasonCode: request.ReasonCode, Note: note, Search: request.Search, CreateProposal: request.CreateProposal, SourceEventID: request.SourceEventID, ProposedContent: request.ProposedContent}, nil
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
