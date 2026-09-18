// Package feedback defines the protocol-independent contracts for feedback
// append and aggregate inspection. Feedback is separate from memory because it
// has its own purpose/type rules, decision context, aggregate projection, and
// optional corrective proposal.
package feedback

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// OperationAppend is the stable operation name for feedback append.
const OperationAppend gateway.OperationName = "feedback.append"

// Service is the inbound, protocol-facing Feedback module surface.
type Service interface {
	Append(context.Context, gateway.CallContext, AppendCommand) (AppendResult, error)
	Aggregate(context.Context, gateway.CallContext, AggregateQuery) (AggregateResult, error)
}

// AppendCommand appends one feedback event, optionally requesting a corrective
// evolution proposal.
type AppendCommand struct {
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

// AppendResult is the outcome of a feedback append.
type AppendResult struct {
	gateway.WriteResult
	Response domain.FeedbackAppendResponse
}

// AggregateQuery selects the aggregate projection to inspect. The current
// implementation exposes the singleton aggregate health projection.
type AggregateQuery struct{}

// AggregateResult is the inspected aggregate projection state.
type AggregateResult struct {
	Health domain.FeedbackAggregateHealth
}
