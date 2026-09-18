package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/feedback"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type rememberResponse struct {
	Memory  domain.Memory `json:"memory"`
	EventID string        `json:"event_id"`
}

type forgetResponse struct {
	Memory domain.Memory `json:"memory"`
}

type rememberRequest struct {
	SpaceID         string         `json:"space_id"`
	Content         string         `json:"content"`
	Kind            string         `json:"kind,omitempty"`
	Sensitivity     string         `json:"sensitivity,omitempty"`
	ValidFrom       *string        `json:"valid_from,omitempty"`
	ValidUntil      *string        `json:"valid_until,omitempty"`
	SourceName      string         `json:"source_name,omitempty"`
	SourceEventID   *string        `json:"source_event_id,omitempty"`
	SourceStreamKey string         `json:"source_stream_key,omitempty"`
	Sequence        *int64         `json:"sequence,omitempty"`
	OccurredAt      string         `json:"occurred_at,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Reason          string         `json:"reason,omitempty"`
}

type recallRequest struct {
	Query            string              `json:"query"`
	SpaceIDs         []string            `json:"space_ids"`
	IncludeAncestors json.RawMessage     `json:"include_ancestors"`
	MemoryTypes      json.RawMessage     `json:"memory_types"`
	States           json.RawMessage     `json:"states"`
	ReferenceTime    json.RawMessage     `json:"reference_time"`
	Intent           json.RawMessage     `json:"intent"`
	Debug            *recallDebugRequest `json:"debug"`
	MaxResults       json.RawMessage     `json:"max_results"`
	MaxContextTokens json.RawMessage     `json:"max_context_tokens"`
	IncludeEvidence  json.RawMessage     `json:"include_evidence"`
	Explain          json.RawMessage     `json:"explain"`
	Cursor           json.RawMessage     `json:"cursor"`
}

// recallDebugRequest is the evaluation/debug-only contract. Production
// callers express intent and budget; explicit capability overrides exist only
// for evaluation and debugging.
type recallDebugRequest struct {
	Capabilities           json.RawMessage `json:"capabilities"`
	GraphPolicy            json.RawMessage `json:"graph_policy"`
	DisableDiversification json.RawMessage `json:"disable_diversification"`
	IncludeStaleAggregates json.RawMessage `json:"include_stale_aggregates"`
}

type memoryFeedbackRequest struct {
	SpaceID         string                          `json:"space_id"`
	Purpose         string                          `json:"feedback_purpose"`
	Type            string                          `json:"type"`
	ReasonCode      string                          `json:"reason_code"`
	Note            *string                         `json:"note,omitempty"`
	Decision        *domain.FeedbackDecisionContext `json:"decision,omitempty"`
	CreateProposal  bool                            `json:"create_proposal,omitempty"`
	SourceEventID   string                          `json:"source_event_id,omitempty"`
	ProposedContent string                          `json:"proposed_content,omitempty"`
}

type forgetRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) remember(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, key, ok := h.writeHeaders(w, r, requestID)
	if !ok {
		return
	}
	var request rememberRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	if !validUUID(request.SpaceID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "space_id must be a UUID", requestID, "", false, nil)
		return
	}
	command := memory.RememberCommand{
		SpaceID: request.SpaceID, Content: request.Content, Kind: request.Kind, Sensitivity: request.Sensitivity,
		ValidFrom: request.ValidFrom, ValidUntil: request.ValidUntil, SourceName: request.SourceName,
		SourceEventID: request.SourceEventID, SourceStreamKey: request.SourceStreamKey, Sequence: request.Sequence,
		OccurredAt: request.OccurredAt, Metadata: request.Metadata, Reason: request.Reason,
	}
	result, err := h.memoryGateway.Remember(r.Context(), gatewayCall(caller, key, requestID), command)
	if err != nil {
		h.gatewayError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(result.Replay))
	h.writeJSON(w, http.StatusCreated, rememberResponse{Memory: result.Memory, EventID: result.EventID})
}

func (h *Handler) recall(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	var request recallRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	query, err := h.recallQuery(request)
	if err != nil {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
		return
	}
	result, err := h.retrievalGateway.Recall(r.Context(), gatewayCall(caller, "", requestID), query)
	if err != nil {
		// An unknown scope space is a request error, not a missing object:
		// scope resolution fails before any ranking happens.
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalidSearchQuery) {
			h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "recall request is invalid", requestID, "", false, nil)
			return
		}
		h.gatewayError(w, err, requestID)
		return
	}
	h.writeJSON(w, http.StatusOK, domain.RecallResponse{Context: result.Context, Execution: result.Execution})
}

func (h *Handler) memoryFeedback(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, key, ok := h.writeHeaders(w, r, requestID)
	if !ok {
		return
	}
	memoryID := r.PathValue("memory_id")
	if !validUUID(memoryID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "memory_id must be a UUID", requestID, "", false, nil)
		return
	}
	var request memoryFeedbackRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	if !validUUID(request.SpaceID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "space_id must be a UUID", requestID, "", false, nil)
		return
	}
	command := feedback.AppendCommand{
		SpaceID: request.SpaceID, MemoryID: memoryID, Purpose: request.Purpose, FeedbackType: request.Type, ReasonCode: request.ReasonCode,
		Note: request.Note, Decision: request.Decision, CreateProposal: request.CreateProposal,
		SourceEventID: request.SourceEventID, ProposedContent: request.ProposedContent,
	}
	result, err := h.feedbackGateway.Append(r.Context(), gatewayCall(caller, key, requestID), command)
	if err != nil {
		h.gatewayError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(result.Replay))
	h.writeJSON(w, http.StatusCreated, result.Response)
}

func (h *Handler) forget(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, key, ok := h.writeHeaders(w, r, requestID)
	if !ok {
		return
	}
	memoryID := r.PathValue("memory_id")
	if !validUUID(memoryID) {
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", "memory_id must be a UUID", requestID, "", false, nil)
		return
	}
	var request forgetRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	command := memory.ForgetCommand{MemoryID: memoryID, Reason: request.Reason}
	result, err := h.memoryGateway.Forget(r.Context(), gatewayCall(caller, key, requestID), command)
	if err != nil {
		h.gatewayError(w, err, requestID)
		return
	}
	w.Header().Set("Idempotent-Replay", strconv.FormatBool(result.Replay))
	h.writeJSON(w, http.StatusOK, forgetResponse{Memory: result.Memory})
}

func (h *Handler) writeHeaders(w http.ResponseWriter, r *http.Request, requestID string) (string, string, bool) {
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return "", "", false
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	return caller, key, ok
}

func gatewayCall(caller, key, requestID string) gateway.CallContext {
	call := gateway.CallContext{RequestID: requestID, Caller: gateway.CallerIdentity{SubjectID: caller}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func (h *Handler) gatewayError(w http.ResponseWriter, err error, requestID string) {
	switch gateway.CodeOf(err) {
	case gateway.ErrorInvalidInput:
		h.writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), requestID, "", false, nil)
	case gateway.ErrorNotFound:
		h.writeError(w, http.StatusNotFound, "not_found", "object not found", requestID, "", false, nil)
	case gateway.ErrorConflict:
		h.writeError(w, http.StatusConflict, "conflict", "object conflicts with existing state", requestID, "", false, nil)
	case gateway.ErrorIdempotencyConflict:
		h.writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key was used for a different request", requestID, "", false, nil)
	case gateway.ErrorPermissionDenied:
		h.writeError(w, http.StatusForbidden, "permission_denied", "operation is not permitted", requestID, "", false, nil)
	case gateway.ErrorResourceExhausted:
		h.writeError(w, http.StatusInsufficientStorage, "resource_exhausted", "disk hard limit reached", requestID, "", false, map[string]any{"resource": "disk", "pressure": "hard"})
	case gateway.ErrorCapabilityUnavailable:
		h.writeError(w, http.StatusServiceUnavailable, "index_degraded", "requested search index is unavailable", requestID, "", true, map[string]any{"index_state": "degraded"})
	default:
		h.repositoryError(w, err, requestID)
	}
}

func (h *Handler) recallQuery(request recallRequest) (gwretrieval.Query, error) {
	query := gwretrieval.Query{Query: request.Query, SpaceIDs: request.SpaceIDs}
	seenSpaces := map[string]bool{}
	for _, id := range request.SpaceIDs {
		if !validUUID(id) {
			return gwretrieval.Query{}, errors.New("space_ids must contain UUIDs")
		}
		if seenSpaces[id] {
			return gwretrieval.Query{}, errors.New("space_ids must be unique")
		}
		seenSpaces[id] = true
	}
	if request.IncludeAncestors != nil && (isNull(request.IncludeAncestors) || json.Unmarshal(request.IncludeAncestors, &query.IncludeAncestors) != nil) {
		return gwretrieval.Query{}, errors.New("include_ancestors must be a boolean")
	} else if request.IncludeAncestors != nil {
		query.IncludeAncestorsSet = true
	}
	if request.MemoryTypes != nil {
		query.MemoryTypesSet = true
		if isNull(request.MemoryTypes) || json.Unmarshal(request.MemoryTypes, &query.MemoryTypes) != nil {
			return gwretrieval.Query{}, errors.New("memory_types must be an array")
		}
	}
	if request.States != nil {
		if isNull(request.States) || json.Unmarshal(request.States, &query.States) != nil {
			return gwretrieval.Query{}, errors.New("states must be an array")
		}
	}
	if request.ReferenceTime != nil {
		if isNull(request.ReferenceTime) || json.Unmarshal(request.ReferenceTime, &query.ReferenceTime) != nil {
			return gwretrieval.Query{}, errors.New("reference_time must be a string")
		}
		query.ReferenceTimeSet = true
	}
	if request.Intent != nil {
		if isNull(request.Intent) || json.Unmarshal(request.Intent, &query.Intent) != nil {
			return gwretrieval.Query{}, errors.New("intent must be a string")
		}
	}
	if request.Debug != nil && request.Debug.Capabilities != nil {
		if isNull(request.Debug.Capabilities) || json.Unmarshal(request.Debug.Capabilities, &query.Channels) != nil {
			return gwretrieval.Query{}, errors.New("debug.capabilities must be an array")
		}
	}
	if request.Debug != nil && request.Debug.GraphPolicy != nil {
		if isNull(request.Debug.GraphPolicy) || json.Unmarshal(request.Debug.GraphPolicy, &query.GraphPolicy) != nil {
			return gwretrieval.Query{}, errors.New("debug.graph_policy must be a string")
		}
	}
	if request.Debug != nil && request.Debug.DisableDiversification != nil {
		if isNull(request.Debug.DisableDiversification) || json.Unmarshal(request.Debug.DisableDiversification, &query.SkipDiversification) != nil {
			return gwretrieval.Query{}, errors.New("debug.disable_diversification must be a boolean")
		}
	}
	if request.Debug != nil && request.Debug.IncludeStaleAggregates != nil {
		var include bool
		if isNull(request.Debug.IncludeStaleAggregates) || json.Unmarshal(request.Debug.IncludeStaleAggregates, &include) != nil {
			return gwretrieval.Query{}, errors.New("debug.include_stale_aggregates must be a boolean")
		}
		query.ExcludeStaleAggregates = !include
		query.ExcludeStaleAggregatesSet = true
	}
	if err := decodeInt(request.MaxResults, &query.Limit, "max_results"); err != nil {
		return gwretrieval.Query{}, err
	} else if request.MaxResults != nil {
		query.LimitSet = true
	}
	if err := decodeInt(request.MaxContextTokens, &query.MaxContextTokens, "max_context_tokens"); err != nil {
		return gwretrieval.Query{}, err
	} else if request.MaxContextTokens != nil {
		query.MaxContextTokensSet = true
	}
	if request.IncludeEvidence != nil && (isNull(request.IncludeEvidence) || json.Unmarshal(request.IncludeEvidence, &query.IncludeEvidence) != nil) {
		return gwretrieval.Query{}, errors.New("include_evidence must be a boolean")
	}
	if request.Explain != nil && (isNull(request.Explain) || json.Unmarshal(request.Explain, &query.Explain) != nil) {
		return gwretrieval.Query{}, errors.New("explain must be a boolean")
	}
	if request.Cursor != nil && (isNull(request.Cursor) || json.Unmarshal(request.Cursor, &query.Cursor) != nil) {
		return gwretrieval.Query{}, errors.New("cursor must be a string")
	}
	return query, nil
}
