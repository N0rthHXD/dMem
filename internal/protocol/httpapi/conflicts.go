package httpapi

import (
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/conflict"
)

// conflictOutput is the protocol-local conflict case representation. Conflict
// administration is not part of the versioned Agent Tool contract, so it owns
// its wire shape and renames the storage term space_id to scope_id.
type conflictOutput struct {
	CaseID            string                          `json:"case_id"`
	ScopeID           string                          `json:"scope_id"`
	State             string                          `json:"state"`
	OpenedBy          string                          `json:"opened_by"`
	ProcessingVersion string                          `json:"processing_version"`
	CreatedAt         string                          `json:"created_at"`
	ClosedAt          *string                         `json:"closed_at,omitempty"`
	Participants      []domain.ConflictParticipant    `json:"participants"`
	Relations         []domain.MemoryRelation         `json:"relations"`
	Resolution        *domain.ConflictResolutionEvent `json:"resolution,omitempty"`
}

type conflictListOutput struct {
	Items      []conflictOutput `json:"items"`
	NextCursor *string          `json:"next_cursor,omitempty"`
}

type conflictEvidenceInput struct {
	EventID     string `json:"event_id"`
	Role        string `json:"role,omitempty"`
	StartOffset *int64 `json:"start_offset,omitempty"`
	EndOffset   *int64 `json:"end_offset,omitempty"`
}

type conflictResolveInput struct {
	ResolutionType  string                   `json:"resolution_type"`
	ExpectedTargets []domain.EvolutionTarget `json:"expected_targets"`
	SelectedTarget  *domain.EvolutionTarget  `json:"selected_target,omitempty"`
	ProposedContent *string                  `json:"proposed_content,omitempty"`
	ValidFrom       *string                  `json:"valid_from,omitempty"`
	ValidUntil      *string                  `json:"valid_until,omitempty"`
	Evidence        []conflictEvidenceInput  `json:"evidence,omitempty"`
	ReplacementMode *string                  `json:"replacement_mode,omitempty"`
	ResultStates    map[string]string        `json:"result_states"`
	Reason          string                   `json:"reason"`
}

type conflictResolveOutput struct {
	OperationID string         `json:"operation_id"`
	Replayed    bool           `json:"replayed"`
	Case        conflictOutput `json:"case"`
}

func (h *Handler) handleListConflicts(ex *exchange, _ routeParams) {
	values := ex.request.URL.Query()
	page, err := ParsePageRequest(values)
	if err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.conflicts.List(ex.context(), ex.call, conflict.ListQuery{
		SpaceID: values.Get("scope_id"),
		State:   values.Get("state"),
		Page:    page,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	items := make([]conflictOutput, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, conflictDTO(item))
	}
	ex.writeJSON(http.StatusOK, conflictListOutput{Items: items, NextCursor: result.NextCursor})
}

func (h *Handler) handleGetConflict(ex *exchange, params routeParams) {
	caseID, err := requirePathID(params, "case_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	value, err := h.conflicts.Get(ex.context(), ex.call, conflict.GetQuery{CaseID: caseID})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.writeJSON(http.StatusOK, conflictDTO(value))
}

func (h *Handler) handleResolveConflict(ex *exchange, params routeParams) {
	caseID, err := requirePathID(params, "case_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	var input conflictResolveInput
	if err := ex.decodeBody(&input); err != nil {
		ex.writeError(err)
		return
	}
	command := conflict.ResolveCommand{
		CaseID:          caseID,
		ResolutionType:  conflict.ResolutionType(input.ResolutionType),
		ExpectedTargets: input.ExpectedTargets,
		SelectedTarget:  input.SelectedTarget,
		ProposedContent: input.ProposedContent,
		ValidFrom:       input.ValidFrom,
		ValidUntil:      input.ValidUntil,
		ReplacementMode: input.ReplacementMode,
		ResultStates:    input.ResultStates,
		Reason:          input.Reason,
	}
	if input.Evidence != nil {
		command.Evidence = make([]conflict.Evidence, len(input.Evidence))
		for i, item := range input.Evidence {
			command.Evidence[i] = conflict.Evidence{EventID: item.EventID, Role: domain.EvidenceRole(item.Role), StartOffset: item.StartOffset, EndOffset: item.EndOffset}
		}
	}
	result, err := h.conflicts.Resolve(ex.context(), ex.call, command)
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusOK, conflictResolveOutput{OperationID: result.OperationID, Replayed: result.Replay, Case: conflictDTO(result.Case)})
}

func conflictDTO(value domain.ConflictCase) conflictOutput {
	participants := value.Participants
	if participants == nil {
		participants = []domain.ConflictParticipant{}
	}
	relations := value.Relations
	if relations == nil {
		relations = []domain.MemoryRelation{}
	}
	return conflictOutput{
		CaseID:            value.CaseID,
		ScopeID:           value.SpaceID,
		State:             value.State,
		OpenedBy:          value.OpenedBy,
		ProcessingVersion: value.ProcessingVersion,
		CreatedAt:         value.CreatedAt,
		ClosedAt:          value.ClosedAt,
		Participants:      participants,
		Relations:         relations,
		Resolution:        value.Resolution,
	}
}
