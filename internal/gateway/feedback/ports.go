package feedback

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Store is the narrow persistence port the Feedback module needs.
type Store interface {
	// AppendFeedback atomically appends the feedback event and schedules any
	// required aggregate work against the current revision. The bool reports an
	// idempotent replay.
	AppendFeedback(context.Context, gateway.WriteIdentity, AppendParams) (domain.FeedbackAppendResponse, bool, error)
	// FeedbackAggregateHealth returns the aggregate projection health.
	FeedbackAggregateHealth(context.Context) (domain.FeedbackAggregateHealth, error)
}

// AppendParams is the transport-free persistence input for a feedback append.
type AppendParams struct {
	SpaceID         string
	MemoryID        string
	Purpose         string
	FeedbackType    string
	ReasonCode      string
	Note            *string
	Decision        *domain.FeedbackDecisionContext
	CreateProposal  bool
	SourceEventID   string
	ProposedContent string
}
