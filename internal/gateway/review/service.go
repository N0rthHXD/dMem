package review

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// ServiceImpl is the default protocol-independent candidate review service. It
// owns input validation, protocol-neutral idempotent write identity, and stable
// error mapping; persistence and the review state machine stay in storage.
type ServiceImpl struct {
	candidates CandidateStore
}

// NewService wires the candidate review service over a candidate store port.
func NewService(candidates CandidateStore) *ServiceImpl {
	return &ServiceImpl{candidates: candidates}
}

// ListCandidates returns one bounded page of candidates for a space.
func (s *ServiceImpl) ListCandidates(ctx context.Context, _ gateway.CallContext, query ListCandidatesQuery) (domain.CandidatePage, error) {
	if strings.TrimSpace(query.SpaceID) == "" {
		return domain.CandidatePage{}, gateway.NewError(gateway.ErrorInvalidInput, "space_id is required")
	}
	if query.ReviewState != "" && !reviewStates[query.ReviewState] {
		return domain.CandidatePage{}, gateway.NewError(gateway.ErrorInvalidInput, "review_state is invalid")
	}
	if err := query.Page.Validate(); err != nil {
		return domain.CandidatePage{}, gateway.WrapError(gateway.ErrorInvalidInput, "page request is invalid", err)
	}
	page, err := s.candidates.ListCandidates(ctx, CandidateFilter{
		SpaceID:           query.SpaceID,
		ReviewState:       query.ReviewState,
		MemoryType:        query.MemoryType,
		ProcessorIdentity: query.ProcessorIdentity,
		ProcessingVersion: query.ProcessingVersion,
		CreatedFrom:       query.CreatedFrom,
		CreatedUntil:      query.CreatedUntil,
		Limit:             query.Page.EffectiveLimit(),
		Cursor:            query.Page.Cursor,
	})
	if err != nil {
		return domain.CandidatePage{}, normalizeError(err)
	}
	return page, nil
}

// GetCandidate reads one candidate by identifier.
func (s *ServiceImpl) GetCandidate(ctx context.Context, _ gateway.CallContext, query GetCandidateQuery) (domain.Candidate, error) {
	if strings.TrimSpace(query.CandidateID) == "" {
		return domain.Candidate{}, gateway.NewError(gateway.ErrorInvalidInput, "candidate_id is required")
	}
	candidate, err := s.candidates.GetCandidate(ctx, query.CandidateID)
	if err != nil {
		return domain.Candidate{}, normalizeError(err)
	}
	return candidate, nil
}

// ConfirmCandidate confirms one pending candidate.
func (s *ServiceImpl) ConfirmCandidate(ctx context.Context, call gateway.CallContext, command ReviewCandidateCommand) (CandidateReviewResult, error) {
	identity, err := s.reviewIdentity(call, OperationCandidateConfirm, command)
	if err != nil {
		return CandidateReviewResult{}, err
	}
	memory, operationID, replay, err := s.candidates.ConfirmCandidate(ctx, identity, reviewParams(command))
	if err != nil {
		return CandidateReviewResult{}, normalizeError(err)
	}
	candidate, err := s.candidates.GetCandidate(ctx, command.CandidateID)
	if err != nil {
		return CandidateReviewResult{}, normalizeError(err)
	}
	result := CandidateReviewResult{
		WriteResult: gateway.WriteResult{OperationID: operationID, Replay: replay},
		Candidate:   candidate,
	}
	if candidate.ReviewState == "confirmed" {
		confirmed := memory
		result.Memory = &confirmed
	}
	return result, nil
}

// RejectCandidate rejects one pending candidate.
func (s *ServiceImpl) RejectCandidate(ctx context.Context, call gateway.CallContext, command ReviewCandidateCommand) (CandidateReviewResult, error) {
	identity, err := s.reviewIdentity(call, OperationCandidateReject, command)
	if err != nil {
		return CandidateReviewResult{}, err
	}
	candidate, operationID, replay, err := s.candidates.RejectCandidate(ctx, identity, reviewParams(command))
	if err != nil {
		return CandidateReviewResult{}, normalizeError(err)
	}
	return CandidateReviewResult{
		WriteResult: gateway.WriteResult{OperationID: operationID, Replay: replay},
		Candidate:   candidate,
	}, nil
}

func (s *ServiceImpl) reviewIdentity(call gateway.CallContext, operation gateway.OperationName, command ReviewCandidateCommand) (gateway.WriteIdentity, error) {
	if err := validateReview(command); err != nil {
		return gateway.WriteIdentity{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, operation, command)
	if err != nil {
		return gateway.WriteIdentity{}, gateway.WriteIdentityError(err)
	}
	return identity, nil
}

func reviewParams(command ReviewCandidateCommand) ReviewParams {
	return ReviewParams{CandidateID: command.CandidateID, ReasonCode: command.ReasonCode, Note: command.Note}
}

func validateReview(command ReviewCandidateCommand) error {
	if strings.TrimSpace(command.CandidateID) == "" {
		return errors.New("candidate_id is required")
	}
	if strings.TrimSpace(command.ReasonCode) != command.ReasonCode || utf8.RuneCountInString(command.ReasonCode) < 1 || utf8.RuneCountInString(command.ReasonCode) > MaxReasonCodeRunes {
		return errors.New("reason_code length must be between 1 and 128")
	}
	if command.Note != nil && utf8.RuneCountInString(*command.Note) > MaxNoteRunes {
		return errors.New("note exceeds the maximum length")
	}
	return nil
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) {
		return err
	}
	return gateway.WrapError(gateway.ErrorInternal, "review operation failed", err)
}
