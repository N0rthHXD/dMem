package httpapi

import (
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
)

// candidateOutput is the protocol-local candidate representation. Candidate
// review is a Provider Route management surface, not part of the versioned Agent
// Tool contract, so it owns its wire shape.
type candidateOutput struct {
	CandidateID         string                    `json:"candidate_id"`
	EventID             string                    `json:"event_id"`
	ScopeID             string                    `json:"scope_id"`
	ReviewState         string                    `json:"review_state"`
	Revision            domain.MemoryRevision     `json:"revision"`
	Evidence            []domain.RevisionEvidence `json:"evidence"`
	ProcessorIdentity   string                    `json:"processor_identity"`
	ProviderIdentity    *string                   `json:"provider_identity,omitempty"`
	ModelIdentity       *string                   `json:"model_identity,omitempty"`
	ProcessingVersion   string                    `json:"processing_version"`
	SpecificationHash   string                    `json:"specification_hash"`
	ConfirmedMemoryID   *string                   `json:"confirmed_memory_id,omitempty"`
	ConfirmedRevisionID *string                   `json:"confirmed_revision_id,omitempty"`
	Reviews             []domain.CandidateReview  `json:"reviews"`
	CreatedAt           string                    `json:"created_at"`
	ReviewedAt          *string                   `json:"reviewed_at,omitempty"`
}

type candidateListOutput struct {
	Items      []candidateOutput `json:"items"`
	NextCursor *string           `json:"next_cursor,omitempty"`
}

type candidateReviewInput struct {
	ReasonCode string  `json:"reason_code"`
	Note       *string `json:"note,omitempty"`
}

type candidateReviewOutput struct {
	OperationID string          `json:"operation_id"`
	Replayed    bool            `json:"replayed"`
	Candidate   candidateOutput `json:"candidate"`
	Memory      *domain.Memory  `json:"memory,omitempty"`
}

func (h *Handler) handleListCandidates(ex *exchange, _ routeParams) {
	values := ex.request.URL.Query()
	page, err := ParsePageRequest(values)
	if err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.reviews.ListCandidates(ex.context(), ex.call, review.ListCandidatesQuery{
		SpaceID:           values.Get("scope_id"),
		ReviewState:       values.Get("review_state"),
		MemoryType:        values.Get("memory_type"),
		ProcessorIdentity: values.Get("processor_identity"),
		ProcessingVersion: values.Get("processing_version"),
		CreatedFrom:       values.Get("created_from"),
		CreatedUntil:      values.Get("created_until"),
		Page:              page,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	items := make([]candidateOutput, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, candidateDTO(item))
	}
	ex.writeJSON(http.StatusOK, candidateListOutput{Items: items, NextCursor: result.NextCursor})
}

func (h *Handler) handleGetCandidate(ex *exchange, params routeParams) {
	candidateID, err := requirePathID(params, "candidate_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	candidate, err := h.reviews.GetCandidate(ex.context(), ex.call, review.GetCandidateQuery{CandidateID: candidateID})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.writeJSON(http.StatusOK, candidateDTO(candidate))
}

func (h *Handler) handleConfirmCandidate(ex *exchange, params routeParams) {
	h.handleCandidateReview(ex, params, true)
}

func (h *Handler) handleRejectCandidate(ex *exchange, params routeParams) {
	h.handleCandidateReview(ex, params, false)
}

// handleCandidateReview serves the confirm and reject sub-resources. Both are
// idempotent writes over an existing candidate, so they share one decoding and
// response path.
func (h *Handler) handleCandidateReview(ex *exchange, params routeParams, confirm bool) {
	candidateID, err := requirePathID(params, "candidate_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	var input candidateReviewInput
	if err := ex.decodeBody(&input); err != nil {
		ex.writeError(err)
		return
	}
	command := review.ReviewCandidateCommand{CandidateID: candidateID, ReasonCode: input.ReasonCode, Note: input.Note}
	var result review.CandidateReviewResult
	if confirm {
		result, err = h.reviews.ConfirmCandidate(ex.context(), ex.call, command)
	} else {
		result, err = h.reviews.RejectCandidate(ex.context(), ex.call, command)
	}
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusOK, candidateReviewOutput{
		OperationID: result.OperationID,
		Replayed:    result.Replay,
		Candidate:   candidateDTO(result.Candidate),
		Memory:      result.Memory,
	})
}

func candidateDTO(value domain.Candidate) candidateOutput {
	evidence := value.Evidence
	if evidence == nil {
		evidence = []domain.RevisionEvidence{}
	}
	reviews := value.Reviews
	if reviews == nil {
		reviews = []domain.CandidateReview{}
	}
	return candidateOutput{
		CandidateID:         value.CandidateID,
		EventID:             value.EventID,
		ScopeID:             value.SpaceID,
		ReviewState:         value.ReviewState,
		Revision:            value.Revision,
		Evidence:            evidence,
		ProcessorIdentity:   value.ProcessorIdentity,
		ProviderIdentity:    value.ProviderIdentity,
		ModelIdentity:       value.ModelIdentity,
		ProcessingVersion:   value.ProcessingVersion,
		SpecificationHash:   value.SpecificationHash,
		ConfirmedMemoryID:   value.ConfirmedMemoryID,
		ConfirmedRevisionID: value.ConfirmedRevisionID,
		Reviews:             reviews,
		CreatedAt:           value.CreatedAt,
		ReviewedAt:          value.ReviewedAt,
	}
}
