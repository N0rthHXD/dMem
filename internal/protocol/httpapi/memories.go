package httpapi

import (
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
)

// memoryOutput is the protocol-local memory representation of the management
// surface. Memory administration is not part of the versioned Agent Tool
// contract, so it owns its wire shape and renames the storage term space_id to
// the public term scope_id.
type memoryOutput struct {
	MemoryID                string                `json:"memory_id"`
	ScopeID                 string                `json:"scope_id"`
	Kind                    string                `json:"kind"`
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

type memoryListOutput struct {
	Items      []memoryOutput `json:"items"`
	NextCursor *string        `json:"next_cursor,omitempty"`
}

type memoryRevisionListOutput struct {
	Items []domain.MemoryRevision `json:"items"`
}

type memoryStateEventListOutput struct {
	Items []domain.MemoryStateEvent `json:"items"`
}

type memoryEvidenceInput struct {
	EventID     string `json:"event_id"`
	Role        string `json:"role,omitempty"`
	StartOffset *int64 `json:"start_offset,omitempty"`
	EndOffset   *int64 `json:"end_offset,omitempty"`
}

type memoryReviseInput struct {
	ExpectedRevisionID string                `json:"expected_revision_id"`
	Content            string                `json:"content"`
	ValidFrom          *string               `json:"valid_from,omitempty"`
	ValidUntil         *string               `json:"valid_until,omitempty"`
	Evidence           []memoryEvidenceInput `json:"evidence,omitempty"`
	Reason             string                `json:"reason"`
}

type memoryStateInput struct {
	ExpectedRevisionID string  `json:"expected_revision_id"`
	Action             string  `json:"action"`
	RestoreRevisionID  *string `json:"restore_revision_id,omitempty"`
	Reason             string  `json:"reason"`
}

type memoryWriteOutput struct {
	OperationID string       `json:"operation_id"`
	Replayed    bool         `json:"replayed"`
	Memory      memoryOutput `json:"memory"`
}

func (h *Handler) handleListMemories(ex *exchange, _ routeParams) {
	values := ex.request.URL.Query()
	page, err := ParsePageRequest(values)
	if err != nil {
		ex.writeError(err)
		return
	}
	includeAncestors, err := queryBool("include_ancestors", values.Get(includeAncestorsQueryParam))
	if err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.memories.List(ex.context(), ex.call, memory.ListQuery{
		SpaceID:          values.Get("scope_id"),
		State:            values.Get("state"),
		IncludeAncestors: includeAncestors,
		Page:             page,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	items := make([]memoryOutput, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, memoryDTO(item))
	}
	ex.writeJSON(http.StatusOK, memoryListOutput{Items: items, NextCursor: result.NextCursor})
}

func (h *Handler) handleGetMemory(ex *exchange, params routeParams) {
	memoryID, err := requirePathID(params, "memory_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	value, err := h.memories.Get(ex.context(), ex.call, memory.GetQuery{MemoryID: memoryID})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.writeJSON(http.StatusOK, memoryDTO(value))
}

func (h *Handler) handleListMemoryRevisions(ex *exchange, params routeParams) {
	memoryID, err := requirePathID(params, "memory_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	revisions, err := h.memories.ListRevisions(ex.context(), ex.call, memory.MemoryQuery{MemoryID: memoryID})
	if err != nil {
		ex.writeError(err)
		return
	}
	if revisions == nil {
		revisions = []domain.MemoryRevision{}
	}
	ex.writeJSON(http.StatusOK, memoryRevisionListOutput{Items: revisions})
}

func (h *Handler) handleListMemoryStateEvents(ex *exchange, params routeParams) {
	memoryID, err := requirePathID(params, "memory_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	events, err := h.memories.ListStateEvents(ex.context(), ex.call, memory.MemoryQuery{MemoryID: memoryID})
	if err != nil {
		ex.writeError(err)
		return
	}
	if events == nil {
		events = []domain.MemoryStateEvent{}
	}
	ex.writeJSON(http.StatusOK, memoryStateEventListOutput{Items: events})
}

func (h *Handler) handleReviseMemory(ex *exchange, params routeParams) {
	memoryID, err := requirePathID(params, "memory_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	var input memoryReviseInput
	if err := ex.decodeBody(&input); err != nil {
		ex.writeError(err)
		return
	}
	command := memory.ReviseCommand{
		MemoryID:           memoryID,
		ExpectedRevisionID: input.ExpectedRevisionID,
		Content:            input.Content,
		ValidFrom:          input.ValidFrom,
		ValidUntil:         input.ValidUntil,
		Reason:             input.Reason,
	}
	if input.Evidence != nil {
		command.Evidence = make([]memory.Evidence, len(input.Evidence))
		for i, item := range input.Evidence {
			command.Evidence[i] = memory.Evidence{EventID: item.EventID, Role: domain.EvidenceRole(item.Role), StartOffset: item.StartOffset, EndOffset: item.EndOffset}
		}
	}
	result, err := h.memories.Revise(ex.context(), ex.call, command)
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusOK, memoryWriteOutput{OperationID: result.OperationID, Replayed: result.Replay, Memory: memoryDTO(result.Memory)})
}

func (h *Handler) handleChangeMemoryState(ex *exchange, params routeParams) {
	memoryID, err := requirePathID(params, "memory_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	var input memoryStateInput
	if err := ex.decodeBody(&input); err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.memories.ChangeState(ex.context(), ex.call, memory.StateChangeCommand{
		MemoryID:           memoryID,
		ExpectedRevisionID: input.ExpectedRevisionID,
		Action:             memory.StateAction(input.Action),
		RestoreRevisionID:  input.RestoreRevisionID,
		Reason:             input.Reason,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusOK, memoryWriteOutput{OperationID: result.OperationID, Replayed: result.Replay, Memory: memoryDTO(result.Memory)})
}

func memoryDTO(value domain.Memory) memoryOutput {
	return memoryOutput{
		MemoryID:                value.MemoryID,
		ScopeID:                 value.SpaceID,
		Kind:                    string(value.Kind),
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
