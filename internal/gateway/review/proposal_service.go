package review

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/gateway"
)

// proposalReviewStates is the closed set of review states a list query may
// filter on. The empty string means "any state".
var proposalReviewStates = map[string]bool{"pending": true, "confirmed": true, "rejected": true}

// proposalReplacementModes is the closed set of evolution replacement modes.
var proposalReplacementModes = map[string]bool{"same_memory_revision": true, "new_memory": true}

// ProposalServiceImpl is the default protocol-independent proposal review
// service. It owns input validation, protocol-neutral idempotent write
// identity, and stable error mapping; the review state machine and revision
// fences stay in storage.
type ProposalServiceImpl struct {
	proposals ProposalStore
}

// NewProposalService wires the proposal review service over a proposal store
// port.
func NewProposalService(proposals ProposalStore) *ProposalServiceImpl {
	return &ProposalServiceImpl{proposals: proposals}
}

// ListProposals returns one bounded page of proposals of one family.
func (s *ProposalServiceImpl) ListProposals(ctx context.Context, _ gateway.CallContext, query ListProposalsQuery) (ProposalPage, error) {
	if strings.TrimSpace(query.SpaceID) == "" {
		return ProposalPage{}, gateway.NewError(gateway.ErrorInvalidInput, "space_id is required")
	}
	if !query.Kind.Valid() {
		return ProposalPage{}, gateway.NewError(gateway.ErrorInvalidInput, "proposal_type is unsupported")
	}
	if query.ReviewState != "" && !proposalReviewStates[query.ReviewState] {
		return ProposalPage{}, gateway.NewError(gateway.ErrorInvalidInput, "review_state is invalid")
	}
	if err := query.Page.Validate(); err != nil {
		return ProposalPage{}, gateway.WrapError(gateway.ErrorInvalidInput, "page request is invalid", err)
	}
	page, err := s.proposals.ListProposals(ctx, ProposalFilter{
		Kind:        query.Kind,
		SpaceID:     query.SpaceID,
		ReviewState: query.ReviewState,
		Limit:       query.Page.EffectiveLimit(),
		Cursor:      query.Page.Cursor,
	})
	if err != nil {
		return ProposalPage{}, normalizeError(err)
	}
	if page.Items == nil {
		page.Items = []Proposal{}
	}
	return page, nil
}

// GetProposal reads one proposal by family and identifier.
func (s *ProposalServiceImpl) GetProposal(ctx context.Context, _ gateway.CallContext, query GetProposalQuery) (Proposal, error) {
	if err := validateProposalRef(query.Kind, query.ProposalID); err != nil {
		return Proposal{}, invalidProposalInput(err)
	}
	value, err := s.proposals.GetProposal(ctx, query.Kind, query.ProposalID)
	if err != nil {
		return Proposal{}, normalizeError(err)
	}
	return value, nil
}

// ConfirmProposal confirms one pending proposal.
func (s *ProposalServiceImpl) ConfirmProposal(ctx context.Context, call gateway.CallContext, command ReviewProposalCommand) (ProposalReviewResult, error) {
	return s.review(ctx, call, command, true)
}

// RejectProposal rejects one pending proposal.
func (s *ProposalServiceImpl) RejectProposal(ctx context.Context, call gateway.CallContext, command ReviewProposalCommand) (ProposalReviewResult, error) {
	return s.review(ctx, call, command, false)
}

func (s *ProposalServiceImpl) review(ctx context.Context, call gateway.CallContext, command ReviewProposalCommand, confirm bool) (ProposalReviewResult, error) {
	if err := validateProposalReview(command, confirm); err != nil {
		return ProposalReviewResult{}, invalidProposalInput(err)
	}
	identity, err := gateway.NewWriteIdentity(call, proposalOperation(command.Kind, confirm), command)
	if err != nil {
		return ProposalReviewResult{}, gateway.WriteIdentityError(err)
	}
	reviewed, operationID, replay, err := s.proposals.ReviewProposal(ctx, identity, ReviewProposalParams{
		Kind:             command.Kind,
		ProposalID:       command.ProposalID,
		Confirm:          confirm,
		ReasonCode:       command.ReasonCode,
		Note:             command.Note,
		ExpectedTargets:  command.ExpectedTargets,
		ReplacementMode:  command.ReplacementMode,
		SupersedeTargets: command.SupersedeTargets,
	})
	if err != nil {
		return ProposalReviewResult{}, normalizeError(err)
	}
	return ProposalReviewResult{WriteResult: gateway.WriteResult{OperationID: operationID, Replay: replay}, Proposal: reviewed}, nil
}

func validateProposalRef(kind ProposalKind, proposalID string) error {
	if !kind.Valid() {
		return errors.New("proposal_type is unsupported")
	}
	if strings.TrimSpace(proposalID) != proposalID || utf8.RuneCountInString(proposalID) < 1 || utf8.RuneCountInString(proposalID) > MaxReasonCodeRunes {
		return errors.New("proposal_id length must be between 1 and 128")
	}
	return nil
}

func validateProposalReview(command ReviewProposalCommand, confirm bool) error {
	if err := validateProposalRef(command.Kind, command.ProposalID); err != nil {
		return err
	}
	if strings.TrimSpace(command.ReasonCode) != command.ReasonCode || utf8.RuneCountInString(command.ReasonCode) < 1 || utf8.RuneCountInString(command.ReasonCode) > MaxReasonCodeRunes {
		return errors.New("reason_code length must be between 1 and 128")
	}
	if command.Note != nil && utf8.RuneCountInString(*command.Note) > MaxNoteRunes {
		return errors.New("note exceeds the maximum length")
	}
	evolutionInput := len(command.ExpectedTargets) != 0 || command.ReplacementMode != nil || command.SupersedeTargets
	if command.Kind != ProposalKindEvolution {
		if evolutionInput {
			return errors.New("evolution review inputs are only valid for evolution proposals")
		}
		return nil
	}
	if !confirm {
		if evolutionInput {
			return errors.New("reject must not carry evolution review inputs")
		}
		return nil
	}
	if len(command.ExpectedTargets) < 1 || len(command.ExpectedTargets) > MaxProposalTargets {
		return errors.New("expected_targets must contain between 1 and 16 items")
	}
	seen := map[string]bool{}
	for _, target := range command.ExpectedTargets {
		if strings.TrimSpace(target.MemoryID) != target.MemoryID || strings.TrimSpace(target.ExpectedRevisionID) != target.ExpectedRevisionID ||
			utf8.RuneCountInString(target.MemoryID) < 1 || utf8.RuneCountInString(target.ExpectedRevisionID) < 1 ||
			utf8.RuneCountInString(target.MemoryID) > MaxReasonCodeRunes || utf8.RuneCountInString(target.ExpectedRevisionID) > MaxReasonCodeRunes {
			return errors.New("expected_targets contains an invalid identifier")
		}
		if seen[target.MemoryID] {
			return errors.New("expected_targets contains duplicate memory ids")
		}
		seen[target.MemoryID] = true
	}
	if command.ReplacementMode != nil && !proposalReplacementModes[*command.ReplacementMode] {
		return errors.New("replacement_mode is unsupported")
	}
	return nil
}

func invalidProposalInput(err error) error {
	return gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
}
