package httpapi

import (
	"net/http"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

func (h *Handler) handleCapabilities(ex *exchange, _ routeParams) {
	ex.writeJSON(http.StatusOK, toolcontract.V1Capabilities())
}

func (h *Handler) handleRecall(ex *exchange, _ routeParams) {
	var input toolcontract.RecallInput
	if err := ex.decodeContractBody(toolcontract.SchemaRecallInput, &input); err != nil {
		ex.writeError(err)
		return
	}
	referenceTime, err := optionalTime(input.ReferenceTime)
	if err != nil {
		ex.writeError(invalidContractBody())
		return
	}
	limit := 0
	if input.Limit != nil {
		limit = *input.Limit
	}
	result, err := h.agentTools.Recall(ex.context(), ex.call, agenttool.RecallCommand{
		Query: input.Query, ScopeIDs: input.ScopeIDs, Intent: domain.RecallIntent(input.Intent), EntityHints: input.Entities,
		ReferenceTime: referenceTime, Limit: limit, Cursor: input.Cursor,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	operationID := result.OperationID
	if operationID == "" {
		operationID = ex.requestID
	}
	ex.writeJSON(http.StatusOK, recallOutput(operationID, result))
}

func recallOutput(operationID string, result agenttool.RecallResult) toolcontract.RecallOutput {
	output := toolcontract.RecallOutput{OperationID: operationID, Memories: make([]toolcontract.RecalledMemory, len(result.Memories)),
		DegradedCapabilities: make([]string, len(result.DegradedCapabilities)), NextCursor: result.NextCursor}
	for i, capability := range result.DegradedCapabilities {
		output.DegradedCapabilities[i] = string(capability)
	}
	for i, memory := range result.Memories {
		output.Memories[i] = toolcontract.RecalledMemory{MemoryID: memory.MemoryID, RevisionID: memory.RevisionID, Kind: string(memory.Kind),
			Content: memory.Content, ValidFrom: formatTime(memory.ValidFrom), ValidUntil: formatTime(memory.ValidUntil), Confidence: memory.Confidence,
			Evidence: make([]toolcontract.EvidenceReference, len(memory.Evidence))}
		for j, evidence := range memory.Evidence {
			output.Memories[i].Evidence[j] = toolcontract.EvidenceReference{EvidenceID: evidence.EvidenceID, EventID: evidence.EventID,
				Role: string(evidence.Role), EventTime: formatTime(evidence.EventTime), EventSequence: evidence.EventSequence,
				StartOffset: evidence.StartOffset, EndOffset: evidence.EndOffset}
		}
	}
	return output
}

func formatTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}
