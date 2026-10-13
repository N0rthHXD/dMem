// Package review defines the protocol-independent candidate review surface of
// the dMem Provider Route. Candidates produced by the processing pipeline are
// independent review objects: only an explicit confirm changes memory authority
// and only an explicit reject discards a candidate. This module never reaches
// the worker, provider, or job runtime.
package review

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Operation names published by the candidate review module. Only state-changing
// operations publish stable names; read operations are adapter-declared.
const (
	OperationCandidateConfirm gateway.OperationName = "review.candidate_confirm"
	OperationCandidateReject  gateway.OperationName = "review.candidate_reject"
)

// Candidate review input bounds.
const (
	// MaxReasonCodeRunes bounds one review reason code.
	MaxReasonCodeRunes = 128
	// MaxNoteRunes bounds the optional reviewer note.
	MaxNoteRunes = 1024
)

// reviewStates is the closed set of candidate review states a list query may
// filter on. The empty string means "any state".
var reviewStates = map[string]bool{"pending": true, "confirmed": true, "rejected": true}

// Service is the protocol-facing candidate review surface.
type Service interface {
	ListCandidates(context.Context, gateway.CallContext, ListCandidatesQuery) (domain.CandidatePage, error)
	GetCandidate(context.Context, gateway.CallContext, GetCandidateQuery) (domain.Candidate, error)
	ConfirmCandidate(context.Context, gateway.CallContext, ReviewCandidateCommand) (CandidateReviewResult, error)
	RejectCandidate(context.Context, gateway.CallContext, ReviewCandidateCommand) (CandidateReviewResult, error)
}

// ListCandidatesQuery bounds one candidate list read. SpaceID is required
// because every candidate belongs to exactly one space.
type ListCandidatesQuery struct {
	SpaceID           string
	ReviewState       string
	MemoryType        string
	ProcessorIdentity string
	ProcessingVersion string
	CreatedFrom       string
	CreatedUntil      string
	Page              gateway.PageRequest
}

// GetCandidateQuery reads one candidate by identifier.
type GetCandidateQuery struct {
	CandidateID string
}

// ReviewCandidateCommand is one caller-supplied candidate review.
type ReviewCandidateCommand struct {
	CandidateID string
	ReasonCode  string
	Note        *string
}

// CandidateReviewResult is the terminal state of one reviewed candidate. Memory
// is present only when the review published or attached memory authority; a
// consolidation that reduces a confirmed candidate to a rejection leaves it nil.
type CandidateReviewResult struct {
	gateway.WriteResult
	Candidate domain.Candidate
	Memory    *domain.Memory
}
