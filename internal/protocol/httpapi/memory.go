package httpapi

import (
	"net/http"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

func (h *Handler) handleMemoryCommit(ex *exchange, _ routeParams) {
	var input toolcontract.MemoryCommitInput
	if err := ex.decodeContractBody(toolcontract.SchemaMemoryCommitInput, &input); err != nil {
		ex.writeError(err)
		return
	}
	command, err := memoryCommitCommand(input)
	if err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.agentTools.MemoryCommit(ex.context(), ex.call, command)
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusCreated, memoryCommitOutput(result))
}

func (h *Handler) handleForget(ex *exchange, _ routeParams) {
	var input toolcontract.ForgetInput
	if err := ex.decodeContractBody(toolcontract.SchemaForgetInput, &input); err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.agentTools.Forget(ex.context(), ex.call, agenttool.ForgetCommand{
		ScopeID: input.ScopeID, MemoryID: input.MemoryID,
		ExpectedRevisionID: input.ExpectedRevisionID, Reason: input.Reason,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusOK, toolcontract.ForgetOutput{
		WriteMetadata: toolcontract.WriteMetadata{OperationID: result.OperationID, Replayed: result.Replay},
		MemoryID:      result.MemoryID, RevisionID: result.RevisionID, State: result.State,
	})
}

func memoryCommitCommand(input toolcontract.MemoryCommitInput) (agenttool.MemoryCommitCommand, error) {
	occurredAt, err := time.Parse(time.RFC3339, input.Source.OccurredAt)
	if err != nil {
		return agenttool.MemoryCommitCommand{}, invalidContractBody()
	}
	command := agenttool.MemoryCommitCommand{
		ScopeID: input.ScopeID,
		Source: agenttool.Source{Type: input.Source.Type, Name: input.Source.Name, ConversationID: input.Source.ConversationID,
			TurnID: input.Source.TurnID, ToolCallID: input.Source.ToolCallID, OccurredAt: occurredAt, Metadata: input.Source.Metadata},
		Memories: make([]agenttool.MemorySuggestion, len(input.Memories)), Entities: make([]agenttool.EntitySuggestion, len(input.Entities)),
		Facts: make([]agenttool.FactSuggestion, len(input.Facts)), Relations: make([]agenttool.RelationSuggestion, len(input.Relations)),
	}
	for i, value := range input.Memories {
		validFrom, err := optionalTime(value.ValidFrom)
		if err != nil {
			return agenttool.MemoryCommitCommand{}, invalidContractBody()
		}
		validUntil, err := optionalTime(value.ValidUntil)
		if err != nil {
			return agenttool.MemoryCommitCommand{}, invalidContractBody()
		}
		command.Memories[i] = agenttool.MemorySuggestion{LocalID: value.LocalID, Kind: domain.MemoryKind(value.Kind), Content: value.Content,
			Sensitivity: agenttool.Sensitivity(value.Sensitivity), Confidence: value.Confidence, SearchHints: value.SearchHints, ValidFrom: validFrom, ValidUntil: validUntil}
	}
	for i, value := range input.Entities {
		command.Entities[i] = agenttool.EntitySuggestion{LocalID: value.LocalID, Type: agenttool.EntityType(value.Type), Name: value.Name, Aliases: value.Aliases, Confidence: value.Confidence}
	}
	for i, value := range input.Facts {
		validFrom, err := optionalTime(value.ValidFrom)
		if err != nil {
			return agenttool.MemoryCommitCommand{}, invalidContractBody()
		}
		validUntil, err := optionalTime(value.ValidUntil)
		if err != nil {
			return agenttool.MemoryCommitCommand{}, invalidContractBody()
		}
		object := agenttool.FactObject{Entity: entityReference(value.Object.Entity), StringValue: value.Object.StringValue, BooleanValue: value.Object.BooleanValue}
		if value.Object.NumberValue != nil {
			number, err := value.Object.NumberValue.Float64()
			if err != nil {
				return agenttool.MemoryCommitCommand{}, invalidContractBody()
			}
			object.NumberValue = &number
		}
		command.Facts[i] = agenttool.FactSuggestion{LocalID: value.LocalID, Subject: toEntityReference(value.Subject), Predicate: value.Predicate,
			Object: object, EvidenceMemory: value.EvidenceMemory, Confidence: value.Confidence, ValidFrom: validFrom, ValidUntil: validUntil}
	}
	for i, value := range input.Relations {
		command.Relations[i] = agenttool.RelationSuggestion{LocalID: value.LocalID, Type: agenttool.RelationType(value.Type),
			Source: toMemoryReference(value.Source), Target: toMemoryReference(value.Target), EvidenceMemory: value.EvidenceMemory, Confidence: value.Confidence}
	}
	return command, nil
}

func optionalTime(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func entityReference(value *toolcontract.EntityReference) *agenttool.EntityReference {
	if value == nil {
		return nil
	}
	converted := toEntityReference(*value)
	return &converted
}

func toEntityReference(value toolcontract.EntityReference) agenttool.EntityReference {
	return agenttool.EntityReference{LocalID: value.LocalID, EntityID: value.EntityID}
}

func toMemoryReference(value toolcontract.MemoryReference) agenttool.MemoryReference {
	return agenttool.MemoryReference{LocalID: value.LocalID, MemoryID: value.MemoryID, RevisionID: value.RevisionID}
}

func memoryCommitOutput(result agenttool.MemoryCommitResult) toolcontract.MemoryCommitOutput {
	return toolcontract.MemoryCommitOutput{WriteMetadata: toolcontract.WriteMetadata{OperationID: result.OperationID, Replayed: result.Replay},
		SourceEventID: result.SourceEventID, Memories: outcomes(result.Memories), Entities: outcomes(result.Entities), Facts: outcomes(result.Facts), Relations: outcomes(result.Relations)}
}

func outcomes(values []agenttool.CommitOutcome) []toolcontract.CommitOutcome {
	result := make([]toolcontract.CommitOutcome, len(values))
	for i, value := range values {
		result[i] = toolcontract.CommitOutcome{LocalID: value.LocalID, Status: string(value.Status), MemoryID: value.MemoryID,
			RevisionID: value.RevisionID, ProposalID: value.ProposalID, ReasonCode: value.ReasonCode}
	}
	return result
}
