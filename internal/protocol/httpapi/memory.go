package httpapi

import (
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
)

// rememberRequest is the wire representation of a remember command.
type rememberRequest struct {
	ScopeID         string         `json:"scope_id"`
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

// forgetRequest is the wire representation of a forget command.
type forgetRequest struct {
	Reason string `json:"reason"`
}

// memoryResource is the public representation of a memory. It renames the
// storage-era space field to the public scope terminology; nested revision and
// conflict values are protocol-neutral domain projections.
type memoryResource struct {
	MemoryID                string                `json:"memory_id"`
	ScopeID                 string                `json:"scope_id"`
	Kind                    domain.MemoryKind     `json:"kind"`
	CurrentState            string                `json:"current_state"`
	Frozen                  bool                  `json:"frozen"`
	Sensitivity             string                `json:"sensitivity,omitempty"`
	CurrentRevision         domain.MemoryRevision `json:"current_revision"`
	CreatedAt               string                `json:"created_at"`
	UpdatedAt               string                `json:"updated_at"`
	ConflictRefs            []domain.ConflictRef  `json:"conflict_refs,omitempty"`
	AggregateStale          bool                  `json:"aggregate_stale,omitempty"`
	AggregateStaleReasons   []string              `json:"aggregate_stale_reasons,omitempty"`
	AggregateProvenanceRefs []string              `json:"aggregate_provenance_refs,omitempty"`
}

// newMemoryResource converts a domain memory into its public representation.
func newMemoryResource(value domain.Memory) memoryResource {
	return memoryResource{
		MemoryID:                value.MemoryID,
		ScopeID:                 value.SpaceID,
		Kind:                    value.Kind,
		CurrentState:            value.CurrentState,
		Frozen:                  value.Frozen,
		Sensitivity:             value.Sensitivity,
		CurrentRevision:         value.CurrentRevision,
		CreatedAt:               value.CreatedAt,
		UpdatedAt:               value.UpdatedAt,
		ConflictRefs:            value.ConflictRefs,
		AggregateStale:          value.AggregateStale,
		AggregateStaleReasons:   value.AggregateStaleReasons,
		AggregateProvenanceRefs: value.AggregateProvenanceRefs,
	}
}

// memoryWriteResponse is the result envelope shared by memory writes.
type memoryWriteResponse struct {
	OperationID string         `json:"operation_id"`
	Replayed    bool           `json:"replayed"`
	Memory      memoryResource `json:"memory"`
	EventID     string         `json:"event_id,omitempty"`
}

// memoryGetResponse is the single-memory read envelope.
type memoryGetResponse struct {
	Memory memoryResource `json:"memory"`
}

// handleRemember serves POST /v1/memories.
func (h *Handler) handleRemember(ex *exchange, _ routeParams) {
	var request rememberRequest
	if err := ex.decodeBody(&request); err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.memory.Remember(ex.context(), ex.call, memory.RememberCommand{
		SpaceID:         request.ScopeID,
		Content:         request.Content,
		Kind:            request.Kind,
		Sensitivity:     request.Sensitivity,
		ValidFrom:       request.ValidFrom,
		ValidUntil:      request.ValidUntil,
		SourceName:      request.SourceName,
		SourceEventID:   request.SourceEventID,
		SourceStreamKey: request.SourceStreamKey,
		Sequence:        request.Sequence,
		OccurredAt:      request.OccurredAt,
		Metadata:        request.Metadata,
		Reason:          request.Reason,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusCreated, memoryWriteResponse{
		OperationID: result.OperationID,
		Replayed:    result.Replay,
		Memory:      newMemoryResource(result.Memory),
		EventID:     result.EventID,
	})
}

// handleGetMemory serves GET /v1/memories/{memory_id}.
func (h *Handler) handleGetMemory(ex *exchange, params routeParams) {
	memoryID, err := requirePathID(params, "memory_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	found, err := h.memory.Get(ex.context(), ex.call, memory.GetQuery{MemoryID: memoryID})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.writeJSON(http.StatusOK, memoryGetResponse{Memory: newMemoryResource(found)})
}

// handleForget serves POST /v1/memories/{memory_id}/forget.
func (h *Handler) handleForget(ex *exchange, params routeParams) {
	memoryID, err := requirePathID(params, "memory_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	var request forgetRequest
	if err := ex.decodeBody(&request); err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.memory.Forget(ex.context(), ex.call, memory.ForgetCommand{
		MemoryID: memoryID,
		Reason:   request.Reason,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusOK, memoryWriteResponse{
		OperationID: result.OperationID,
		Replayed:    result.Replay,
		Memory:      newMemoryResource(result.Memory),
	})
}
