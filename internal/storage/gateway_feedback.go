package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/feedback"
)

// FeedbackCapability adapts the SQLite repository to the Feedback module's
// store port. Feedback targets the memory's current revision, matching the
// protocol-neutral append command which carries no revision identifier.
type FeedbackCapability struct {
	repo *Repository
}

// NewFeedbackCapability wires the Feedback store port over one repository.
func NewFeedbackCapability(repo *Repository) *FeedbackCapability {
	return &FeedbackCapability{repo: repo}
}

// AppendFeedback atomically appends the feedback event, schedules aggregate
// work, and records audit history and the idempotency result.
func (c *FeedbackCapability) AppendFeedback(ctx context.Context, identity gateway.WriteIdentity, params feedback.AppendParams) (domain.FeedbackAppendResponse, bool, error) {
	return c.repo.AppendCurrentFeedback(ctx, gatewayWriteIdentity(identity), AppendFeedbackParams{
		SpaceID:         params.SpaceID,
		MemoryID:        params.MemoryID,
		Purpose:         params.Purpose,
		FeedbackType:    params.FeedbackType,
		ReasonCode:      params.ReasonCode,
		Note:            params.Note,
		Decision:        params.Decision,
		CreateProposal:  params.CreateProposal,
		SourceEventID:   params.SourceEventID,
		ProposedContent: params.ProposedContent,
	})
}

// FeedbackAggregateHealth returns the aggregate projection health.
func (c *FeedbackCapability) FeedbackAggregateHealth(ctx context.Context) (domain.FeedbackAggregateHealth, error) {
	return c.repo.FeedbackAggregateHealth(ctx)
}
