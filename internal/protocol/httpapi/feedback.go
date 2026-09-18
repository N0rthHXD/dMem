package httpapi

import (
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/feedback"
)

// feedbackRequest is the wire representation of a feedback append command. The
// target memory is carried by the path, not the body.
type feedbackRequest struct {
	ScopeID         string                          `json:"scope_id"`
	Purpose         string                          `json:"purpose"`
	FeedbackType    string                          `json:"feedback_type"`
	ReasonCode      string                          `json:"reason_code"`
	Note            *string                         `json:"note,omitempty"`
	Decision        *domain.FeedbackDecisionContext `json:"decision,omitempty"`
	CreateProposal  bool                            `json:"create_proposal,omitempty"`
	SourceEventID   string                          `json:"source_event_id,omitempty"`
	ProposedContent string                          `json:"proposed_content,omitempty"`
}

// feedbackAppendResponse is the feedback write envelope.
type feedbackAppendResponse struct {
	OperationID string                 `json:"operation_id"`
	Replayed    bool                   `json:"replayed"`
	Feedback    domain.FeedbackEvent   `json:"feedback"`
	Proposal    domain.ProposalOutcome `json:"proposal"`
}

// handleFeedback serves POST /v1/memories/{memory_id}/feedback.
func (h *Handler) handleFeedback(ex *exchange, params routeParams) {
	memoryID, err := requirePathID(params, "memory_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	var request feedbackRequest
	if err := ex.decodeBody(&request); err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.feedback.Append(ex.context(), ex.call, feedback.AppendCommand{
		SpaceID:         request.ScopeID,
		MemoryID:        memoryID,
		Purpose:         request.Purpose,
		FeedbackType:    request.FeedbackType,
		ReasonCode:      request.ReasonCode,
		Note:            request.Note,
		Decision:        request.Decision,
		CreateProposal:  request.CreateProposal,
		SourceEventID:   request.SourceEventID,
		ProposedContent: request.ProposedContent,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusCreated, feedbackAppendResponse{
		OperationID: result.OperationID,
		Replayed:    result.Replay,
		Feedback:    result.Response.Feedback,
		Proposal:    result.Response.Proposal,
	})
}
