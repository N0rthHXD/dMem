package storage

import (
	"errors"

	"github.com/N0rthHXD/dMem/internal/gateway"
)

// gatewayError translates a storage failure into a stable gateway error while
// retaining the original cause for errors.Is and internal logging. Adapters use
// it so gateway modules and protocol adapters never inspect storage.Err*.
func gatewayError(err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) {
		return err
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return gateway.WrapError(gateway.ErrorNotFound, "object not found", err)
	case errors.Is(err, ErrIdempotencyConflict):
		return gateway.WrapError(gateway.ErrorIdempotencyConflict, "idempotency key was used for a different command", err)
	case errors.Is(err, ErrConflict):
		return gateway.WrapError(gateway.ErrorConflict, "operation conflicts with existing state", err)
	case errors.Is(err, ErrInvalidParent):
		return gateway.WrapError(gateway.ErrorInvalidInput, "invalid parent scope", err)
	case errors.Is(err, ErrInvalidCursor):
		return gateway.WrapError(gateway.ErrorInvalidInput, "cursor is invalid", err)
	case errors.Is(err, ErrInvalidSearchQuery):
		return gateway.WrapError(gateway.ErrorInvalidInput, "recall query is invalid", err)
	case errors.Is(err, ErrPolicyDenied):
		return gateway.WrapError(gateway.ErrorPermissionDenied, "recall denied by policy", err)
	case errors.Is(err, ErrInvalidStateTransition):
		return gateway.WrapError(gateway.ErrorConflict, "state transition is not allowed", err)
	case errors.Is(err, ErrCandidateReviewed), errors.Is(err, ErrEvolutionReviewed), errors.Is(err, ErrConflictResolved):
		return gateway.WrapError(gateway.ErrorConflict, "object has already reached a terminal review state", err)
	case errors.Is(err, ErrRevisionConflict):
		return gateway.WrapError(gateway.ErrorConflict, "expected revision is not current", err)
	case errors.Is(err, ErrFrozenRevision):
		return gateway.WrapError(gateway.ErrorConflict, "memory is frozen", err)
	case errors.Is(err, ErrUnsupportedAction):
		return gateway.WrapError(gateway.ErrorInvalidInput, "operation is not supported for this object", err)
	case errors.Is(err, ErrInvalidEvolutionProposal), errors.Is(err, ErrInvalidConsolidation):
		return gateway.WrapError(gateway.ErrorInvalidInput, "proposal is invalid", err)
	case errors.Is(err, ErrFeedbackIdentityMismatch):
		return gateway.WrapError(gateway.ErrorInvalidInput, "feedback decision identity is incompatible", err)
	case errors.Is(err, ErrQueueFull):
		return gateway.WrapError(gateway.ErrorResourceExhausted, "job queue is full", err).WithRetryable(true)
	case errors.Is(err, ErrResourceExhausted):
		return gateway.WrapError(gateway.ErrorResourceExhausted, "resource exhausted", err)
	case errors.Is(err, ErrIndexDegraded):
		return gateway.WrapError(gateway.ErrorCapabilityUnavailable, "requested index is unavailable", err)
	default:
		return err
	}
}
