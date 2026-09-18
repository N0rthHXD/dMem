package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
)

// EventCapability adapts the SQLite repository to the Extraction module's event
// ingress port.
type EventCapability struct {
	repo *Repository
}

// NewEventCapability wires the event ingress port over one repository.
func NewEventCapability(repo *Repository) *EventCapability {
	return &EventCapability{repo: repo}
}

// IngestEvent atomically persists the raw event, its extraction job, audit
// record, and idempotency result. Storage failures are translated into stable
// gateway error codes while the original cause is retained.
func (c *EventCapability) IngestEvent(ctx context.Context, identity gateway.WriteIdentity, params extraction.IngestParams) (extraction.IngestRecord, bool, error) {
	result, replay, err := c.repo.IngestEvent(ctx, gatewayWriteIdentity(identity), IngestParams{
		SpaceID:         params.SpaceID,
		SourceType:      params.SourceType,
		SourceName:      params.SourceName,
		SourceEventID:   params.SourceEventID,
		SourceStreamKey: params.SourceStreamKey,
		Sequence:        params.Sequence,
		OccurredAt:      params.OccurredAt,
		Content:         params.Content,
		Metadata:        params.Metadata,
	})
	if err != nil {
		return extraction.IngestRecord{}, replay, gatewayError(err)
	}
	return extraction.IngestRecord{
		OperationID: result.OperationID,
		EventID:     result.EventID,
		Status:      result.Status,
		JobIDs:      result.JobIDs,
		AcceptedAt:  result.AcceptedAt,
	}, replay, nil
}

// GetEvent returns one raw event by identifier.
func (c *EventCapability) GetEvent(ctx context.Context, eventID string) (domain.RawEvent, error) {
	event, err := c.repo.GetEvent(ctx, eventID)
	if err != nil {
		return domain.RawEvent{}, gatewayError(err)
	}
	return event, nil
}

// ExtractionWorkCapability adapts the SQLite job/processing routines to the
// worker-facing extraction port. Provider calls happen outside these calls and
// every commit is fenced by the claim.
type ExtractionWorkCapability struct {
	repo *Repository
}

// NewExtractionWorkCapability wires the worker-facing port over one repository.
func NewExtractionWorkCapability(repo *Repository) *ExtractionWorkCapability {
	return &ExtractionWorkCapability{repo: repo}
}

// Prepare loads the bounded processing input for a claimed extraction job.
func (c *ExtractionWorkCapability) Prepare(ctx context.Context, claim extraction.WorkClaim, params extraction.PrepareRequest) (domain.ProcessingInput, error) {
	return c.repo.PrepareProcessing(ctx, claimedJob(claim), ProcessingPreparationParams{
		ProcessorIdentity:      params.ProcessorIdentity,
		AllowedMemoryTypes:     params.AllowedMemoryTypes,
		MaxCandidates:          params.MaxCandidates,
		MaxCandidateChars:      params.MaxCandidateChars,
		MaxTotalCandidateChars: params.MaxTotalCandidateChars,
		MaxContentChars:        params.MaxContentChars,
		MaxContextEvents:       params.MaxContextEvents,
		MaxContextMemories:     params.MaxContextMemories,
	})
}

// Commit applies a fenced processing result in one transaction.
func (c *ExtractionWorkCapability) Commit(ctx context.Context, claim extraction.WorkClaim, input domain.ProcessingInput, request extraction.CommitRequest) error {
	return c.repo.CommitProcessing(ctx, claimedJob(claim), input, ProcessingCommit{Result: request.Result})
}

// Block marks a claimed job as blocked by policy.
func (c *ExtractionWorkCapability) Block(ctx context.Context, claim extraction.WorkClaim, reason string) error {
	return c.repo.BlockJob(ctx, claimedJob(claim), reason)
}

// Fail records a transient or permanent processing failure.
func (c *ExtractionWorkCapability) Fail(ctx context.Context, claim extraction.WorkClaim, input domain.ProcessingInput, failure extraction.Failure) error {
	return c.repo.FailProcessing(ctx, claimedJob(claim), input, JobFailure{
		Code:      failure.Code,
		Message:   failure.Message,
		Transient: failure.Transient,
		Backoff:   failure.Backoff,
	})
}

func claimedJob(claim extraction.WorkClaim) ClaimedJob {
	return ClaimedJob{
		Job: domain.Job{
			JobID:        claim.JobID,
			OperationID:  claim.OperationID,
			JobType:      claim.JobType,
			TargetType:   claim.TargetType,
			TargetID:     claim.TargetID,
			AttemptCount: claim.Attempt,
			MaxAttempts:  claim.MaxAttempts,
		},
		ProcessingVersion: claim.ProcessingVersion,
		LeaseOwner:        claim.LeaseOwner,
		LeaseGeneration:   claim.LeaseGeneration,
	}
}
