package consolidation

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// ServiceImpl is the default protocol-independent Consolidation service. It owns
// review validation, idempotency identity, and stable error mapping, and
// delegates the atomic candidate confirmation or rejection to the Store port.
// Confirmation stays one use case: the caller cannot evaluate a candidate and
// then apply it as two separately raceable steps.
type ServiceImpl struct {
	store Store
}

// NewService wires the Consolidation service over an atomic review store port.
func NewService(store Store) *ServiceImpl {
	return &ServiceImpl{store: store}
}

// Confirm validates one review and atomically publishes the candidate revision,
// its consolidation decision, and any evolution proposal it requires.
func (s *ServiceImpl) Confirm(ctx context.Context, call gateway.CallContext, command ConfirmCommand) (ConfirmResult, error) {
	if err := validateReview(command.CandidateID, command.ReasonCode, command.Note); err != nil {
		return ConfirmResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationConfirm, command)
	if err != nil {
		return ConfirmResult{}, gateway.WriteIdentityError(err)
	}
	memory, replay, err := s.store.ConfirmCandidate(ctx, identity, command.CandidateID, ReviewParams{
		ReasonCode: command.ReasonCode,
		Note:       command.Note,
	})
	if err != nil {
		return ConfirmResult{}, normalizeError(err)
	}
	return ConfirmResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay},
		Memory:      memory,
	}, nil
}

// Reject validates one review and atomically records the candidate rejection.
func (s *ServiceImpl) Reject(ctx context.Context, call gateway.CallContext, command RejectCommand) (RejectResult, error) {
	if err := validateReview(command.CandidateID, command.ReasonCode, command.Note); err != nil {
		return RejectResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationReject, command)
	if err != nil {
		return RejectResult{}, gateway.WriteIdentityError(err)
	}
	candidate, replay, err := s.store.RejectCandidate(ctx, identity, command.CandidateID, ReviewParams{
		ReasonCode: command.ReasonCode,
		Note:       command.Note,
	})
	if err != nil {
		return RejectResult{}, normalizeError(err)
	}
	return RejectResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay},
		Candidate:   candidate,
	}, nil
}

// Decision returns the recorded consolidation decision for a candidate. It is
// read-only and never mutates authority.
func (s *ServiceImpl) Decision(ctx context.Context, call gateway.CallContext, candidateID string) (domain.ConsolidationDecision, error) {
	if strings.TrimSpace(candidateID) == "" {
		return domain.ConsolidationDecision{}, gateway.NewError(gateway.ErrorInvalidInput, "candidate_id is required")
	}
	decision, err := s.store.GetConsolidationDecision(ctx, candidateID)
	if err != nil {
		return domain.ConsolidationDecision{}, normalizeError(err)
	}
	return decision, nil
}

func validateReview(candidateID, reasonCode string, note *string) error {
	if strings.TrimSpace(candidateID) == "" {
		return errors.New("candidate_id is required")
	}
	if utf8.RuneCountInString(reasonCode) < 1 || utf8.RuneCountInString(reasonCode) > 128 {
		return errors.New("reason_code length must be between 1 and 128")
	}
	if note != nil && utf8.RuneCountInString(*note) > 1024 {
		return errors.New("note is too long")
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
	return gateway.WrapError(gateway.ErrorInternal, "consolidation operation failed", err)
}
