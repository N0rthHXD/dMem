package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
)

// proposalObject is the protocol-local rendering of one reviewable proposal. It
// is a flat object: the populated union member fields plus proposal_type, with
// the storage term space_id renamed to the public term scope_id. Proposal
// review is a Provider Route management surface, not part of the versioned
// Agent Tool contract, so it owns its wire shape.
type proposalObject map[string]json.RawMessage

type proposalListOutput struct {
	Items      []proposalObject `json:"items"`
	NextCursor *string          `json:"next_cursor,omitempty"`
}

type proposalReviewInput struct {
	ReasonCode       string                   `json:"reason_code"`
	Note             *string                  `json:"note,omitempty"`
	ExpectedTargets  []domain.EvolutionTarget `json:"expected_targets,omitempty"`
	ReplacementMode  *string                  `json:"replacement_mode,omitempty"`
	SupersedeTargets bool                     `json:"supersede_targets,omitempty"`
}

type proposalReviewOutput struct {
	OperationID string         `json:"operation_id"`
	Replayed    bool           `json:"replayed"`
	Proposal    proposalObject `json:"proposal"`
}

func (h *Handler) handleListProposals(ex *exchange, _ routeParams) {
	values := ex.request.URL.Query()
	page, err := ParsePageRequest(values)
	if err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.proposals.ListProposals(ex.context(), ex.call, review.ListProposalsQuery{
		SpaceID:     values.Get("scope_id"),
		Kind:        review.ProposalKind(values.Get("proposal_type")),
		ReviewState: values.Get("review_state"),
		Page:        page,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	items := make([]proposalObject, 0, len(result.Items))
	for _, item := range result.Items {
		object, err := proposalDTO(item)
		if err != nil {
			ex.writeError(err)
			return
		}
		items = append(items, object)
	}
	ex.writeJSON(http.StatusOK, proposalListOutput{Items: items, NextCursor: result.NextCursor})
}

func (h *Handler) handleGetProposal(ex *exchange, params routeParams) {
	proposalType, err := requirePathID(params, "proposal_type")
	if err != nil {
		ex.writeError(err)
		return
	}
	proposalID, err := requirePathID(params, "proposal_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	value, err := h.proposals.GetProposal(ex.context(), ex.call, review.GetProposalQuery{Kind: review.ProposalKind(proposalType), ProposalID: proposalID})
	if err != nil {
		ex.writeError(err)
		return
	}
	object, err := proposalDTO(value)
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.writeJSON(http.StatusOK, object)
}

func (h *Handler) handleConfirmProposal(ex *exchange, params routeParams) {
	h.handleProposalReview(ex, params, true)
}

func (h *Handler) handleRejectProposal(ex *exchange, params routeParams) {
	h.handleProposalReview(ex, params, false)
}

// handleProposalReview serves the confirm and reject sub-resources. Both are
// idempotent writes over an existing proposal, so they share one decoding and
// response path.
func (h *Handler) handleProposalReview(ex *exchange, params routeParams, confirm bool) {
	proposalType, err := requirePathID(params, "proposal_type")
	if err != nil {
		ex.writeError(err)
		return
	}
	proposalID, err := requirePathID(params, "proposal_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	var input proposalReviewInput
	if err := ex.decodeBody(&input); err != nil {
		ex.writeError(err)
		return
	}
	command := review.ReviewProposalCommand{
		Kind:             review.ProposalKind(proposalType),
		ProposalID:       proposalID,
		ReasonCode:       input.ReasonCode,
		Note:             input.Note,
		ExpectedTargets:  input.ExpectedTargets,
		ReplacementMode:  input.ReplacementMode,
		SupersedeTargets: input.SupersedeTargets,
	}
	var result review.ProposalReviewResult
	if confirm {
		result, err = h.proposals.ConfirmProposal(ex.context(), ex.call, command)
	} else {
		result, err = h.proposals.RejectProposal(ex.context(), ex.call, command)
	}
	if err != nil {
		ex.writeError(err)
		return
	}
	object, err := proposalDTO(result.Proposal)
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusOK, proposalReviewOutput{OperationID: result.OperationID, Replayed: result.Replay, Proposal: object})
}

// proposalDTO renders one union member with the public scope terminology and an
// explicit proposal_type discriminator.
func proposalDTO(value review.Proposal) (proposalObject, error) {
	raw, err := json.Marshal(value.Value())
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if space, ok := fields["space_id"]; ok {
		fields["scope_id"] = space
		delete(fields, "space_id")
	}
	kind, err := json.Marshal(string(value.Kind))
	if err != nil {
		return nil, err
	}
	fields["proposal_type"] = kind
	return fields, nil
}
