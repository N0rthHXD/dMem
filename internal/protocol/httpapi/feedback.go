package httpapi

import (
	"net/http"

	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

func (h *Handler) handleFeedback(ex *exchange, _ routeParams) {
	var input toolcontract.FeedbackInput
	if err := ex.decodeContractBody(toolcontract.SchemaFeedbackInput, &input); err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.agentTools.Feedback(ex.context(), ex.call, agenttool.FeedbackCommand{
		ScopeID: input.ScopeID, MemoryID: input.MemoryID, RevisionID: input.RevisionID,
		Kind: agenttool.FeedbackKind(input.Kind), Reason: input.Reason, RecallOperationID: input.RecallOperationID,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusCreated, toolcontract.FeedbackOutput{WriteMetadata: toolcontract.WriteMetadata{OperationID: result.OperationID, Replayed: result.Replay},
		FeedbackID: result.FeedbackID, MemoryID: result.MemoryID, RevisionID: result.RevisionID, Kind: string(result.Kind)})
}
