package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/aggregate"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type aggregateBuildRequest struct {
	SpaceID          string   `json:"space_id"`
	AggregateKind    string   `json:"aggregate_kind"`
	InputRevisionIDs []string `json:"input_revision_ids"`
}

type aggregateReviewRequest struct {
	ReasonCode string  `json:"reason_code"`
	Note       *string `json:"note"`
}

func (h *Handler) aggregateProposals(w http.ResponseWriter, r *http.Request) {
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
		var req aggregateBuildRequest
		if !h.decode(w, r, requestID, &req) {
			return
		}
		if !validUUID(req.SpaceID) || !domain.ValidAggregateKind(req.AggregateKind) || len(req.InputRevisionIDs) == 0 {
			h.writeError(w, 422, "invalid_request", "invalid aggregate build", requestID, "", false, nil)
			return
		}
		for _, id := range req.InputRevisionIDs {
			if !validUUID(id) {
				h.writeError(w, 422, "invalid_request", "input_revision_ids must be UUIDs", requestID, "", false, nil)
				return
			}
		}
		if h.aggregates == nil {
			h.writeError(w, 422, "unsupported", "aggregate kind is unsupported", requestID, "", false, nil)
			return
		}
		identity := writeIdentity(caller, key, requestID, r.Method, "/v1/aggregate-proposals", req)
		result, replay, err := h.aggregates.Build(r.Context(), identity, req.SpaceID, req.AggregateKind, req.InputRevisionIDs)
		if err != nil {
			if errors.Is(err, aggregate.ErrUnsupported) {
				h.writeError(w, 422, "unsupported", "aggregate kind is unsupported", requestID, "", false, nil)
			} else {
				h.repositoryError(w, err, requestID)
			}
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
	state, kind := r.URL.Query().Get("review_state"), r.URL.Query().Get("aggregate_kind")
	if state != "" && !containsAPI([]string{"pending", "confirmed", "rejected"}, state) || kind != "" && !domain.ValidAggregateKind(kind) {
		h.writeError(w, 422, "invalid_request", "invalid aggregate proposal filter", requestID, "", false, nil)
		return
	}
	result, err := h.repository.ListAggregateProposals(r.Context(), storage.AggregateProposalFilter{SpaceID: space, ReviewState: r.URL.Query().Get("review_state"), AggregateKind: r.URL.Query().Get("aggregate_kind"), Cursor: cursor, Limit: limit})
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, result)
}

func (h *Handler) aggregateProposal(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	id, action := r.PathValue("proposal_id"), r.PathValue("action")
	if !validUUID(id) {
		h.writeError(w, 422, "invalid_request", "proposal_id must be a UUID", requestID, "", false, nil)
		return
	}
	if action == "" {
		result, err := h.repository.GetAggregateProposal(r.Context(), id)
		if err != nil {
			h.repositoryError(w, err, requestID)
			return
		}
		h.writeJSON(w, 200, result)
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
	var req aggregateReviewRequest
	if !h.decode(w, r, requestID, &req) {
		return
	}
	if utf8.RuneCountInString(req.ReasonCode) < 1 || utf8.RuneCountInString(req.ReasonCode) > 128 || req.Note != nil && utf8.RuneCountInString(*req.Note) > 1024 {
		h.writeError(w, 422, "invalid_request", "invalid aggregate review", requestID, "", false, nil)
		return
	}
	params := storage.ReviewAggregateProposalParams{ReasonCode: req.ReasonCode, Note: req.Note}
	identity := writeIdentity(caller, key, requestID, r.Method, "/v1/aggregate-proposals/{proposal_id}/"+action, params)
	var result domain.AggregateProposal
	var replay bool
	var err error
	if action == "confirm" {
		result, replay, err = h.repository.ConfirmAggregateProposal(r.Context(), identity, id, params)
	} else {
		result, replay, err = h.repository.RejectAggregateProposal(r.Context(), identity, id, params)
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
	h.writeJSON(w, status, result)
}

func (h *Handler) aggregatesList(w http.ResponseWriter, r *http.Request) {
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
	kind, freshness := r.URL.Query().Get("aggregate_kind"), r.URL.Query().Get("freshness")
	if kind != "" && !domain.ValidAggregateKind(kind) || freshness != "" && !containsAPI([]string{"fresh", "stale"}, freshness) {
		h.writeError(w, 422, "invalid_request", "invalid aggregate filter", requestID, "", false, nil)
		return
	}
	exclude := freshness != "stale"
	if raw := r.URL.Query().Get("exclude_stale"); raw != "" {
		var err error
		exclude, err = strconv.ParseBool(raw)
		if err != nil {
			h.writeError(w, 422, "invalid_request", "exclude_stale must be a boolean", requestID, "", false, nil)
			return
		}
	}
	result, err := h.repository.ListAggregates(r.Context(), storage.AggregateFilter{SpaceID: space, AggregateKind: r.URL.Query().Get("aggregate_kind"), Freshness: r.URL.Query().Get("freshness"), ExcludeStale: exclude, Cursor: cursor, Limit: limit})
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	h.writeJSON(w, 200, result)
}

func (h *Handler) aggregateGet(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	id := r.PathValue("aggregate_id")
	if !validUUID(id) {
		h.writeError(w, 422, "invalid_request", "aggregate_id must be a UUID", requestID, "", false, nil)
		return
	}
	result, err := h.repository.GetAggregate(r.Context(), id)
	if err != nil {
		h.repositoryError(w, err, requestID)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/provenance") {
		h.writeJSON(w, 200, result.Provenance)
		return
	}
	h.writeJSON(w, 200, result)
}
