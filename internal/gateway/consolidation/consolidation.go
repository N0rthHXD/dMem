// Package consolidation defines the protocol-independent contracts for
// candidate consolidation. The first boundary represents existing behavior:
// duplicate, reinforce, supplement, conflict, candidate confirmation, and
// evolution proposal decisions. Candidate confirmation stays one atomic use
// case; the classifier is pure and never mutates authority.
package consolidation

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Operation names published by the Consolidation module.
const (
	OperationConfirm gateway.OperationName = "consolidation.confirm"
	OperationReject  gateway.OperationName = "consolidation.reject"
)

// Service is the inbound, protocol-facing Consolidation module surface.
// Confirmation and rejection are the only authority mutations; the recorded
// decision is read-only.
type Service interface {
	Confirm(context.Context, gateway.CallContext, ConfirmCommand) (ConfirmResult, error)
	Reject(context.Context, gateway.CallContext, RejectCommand) (RejectResult, error)
	Decision(context.Context, gateway.CallContext, string) (domain.ConsolidationDecision, error)
}

// ConfirmCommand confirms one candidate, publishing its revision and any
// evolution decision atomically.
type ConfirmCommand struct {
	CandidateID string
	ReasonCode  string
	Note        *string
}

// ConfirmResult is the outcome of a candidate confirmation.
type ConfirmResult struct {
	gateway.WriteResult
	Memory domain.Memory
}

// RejectCommand rejects one candidate.
type RejectCommand struct {
	CandidateID string
	ReasonCode  string
	Note        *string
}

// RejectResult is the outcome of a candidate rejection.
type RejectResult struct {
	gateway.WriteResult
	Candidate domain.Candidate
}

// Classifier is the optional, policy-gated provider boundary for candidates the
// deterministic rules leave unresolved. Its result is a proposal, never an
// authority change.
type Classifier interface {
	Classify(context.Context, ClassifyRequest) (Classification, error)
}

// ClassifyRequest is the bounded classifier input.
type ClassifyRequest struct {
	Candidate domain.MemoryRevision
	Neighbors []Neighbor
}

// Classification is a provider-proposed consolidation decision.
type Classification struct {
	Action     domain.ConsolidationAction
	ReasonCode string
	MemoryID   string
	RevisionID string
	Similarity *float64
}
