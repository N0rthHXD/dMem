// Package extraction defines the protocol-independent contracts for raw event
// ingress and extraction work semantics. Inbound ingestion is separate from the
// worker-facing prepare, commit, block, and fail ports so a protocol adapter
// cannot drive provider processing directly.
package extraction

import (
	"context"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// OperationIngest is the stable operation name for raw event ingestion.
const OperationIngest gateway.OperationName = "extraction.ingest"

// Service is the inbound, protocol-facing Extraction module surface.
type Service interface {
	Ingest(context.Context, gateway.CallContext, IngestCommand) (IngestResult, error)
	GetEvent(context.Context, gateway.CallContext, GetEventQuery) (domain.RawEvent, error)
}

// IngestCommand accepts one raw event and atomically schedules its extraction
// job.
type IngestCommand struct {
	SpaceID         string
	SourceType      string
	SourceName      string
	SourceEventID   *string
	SourceStreamKey string
	Sequence        *int64
	OccurredAt      string
	Content         domain.Content
	Metadata        map[string]any
}

// IngestResult is the accepted event and its durable job references.
type IngestResult struct {
	gateway.WriteResult
	EventID    string
	Status     string
	JobIDs     []string
	AcceptedAt string
}

// GetEventQuery reads one raw event by identifier.
type GetEventQuery struct {
	EventID string
}

// WorkClaim is the transport-free identity of one claimed extraction job. A
// runtime adapter maps its lease record to this value.
type WorkClaim struct {
	JobID             string
	JobType           string
	TargetType        string
	TargetID          string
	Attempt           int
	MaxAttempts       int
	ProcessingVersion string
	LeaseOwner        string
	LeaseGeneration   int
}

// PrepareRequest bounds the context a worker may load for extraction.
type PrepareRequest struct {
	ProcessorIdentity      string
	AllowedMemoryTypes     []string
	MaxCandidates          int
	MaxCandidateChars      int
	MaxTotalCandidateChars int
	MaxContentChars        int
	MaxContextEvents       int
	MaxContextMemories     int
}

// CommitRequest carries a fenced processing result.
type CommitRequest struct {
	Result domain.ProcessingResult
}

// Failure reports a transient or permanent processing failure.
type Failure struct {
	Code      string
	Message   string
	Transient bool
	Backoff   time.Duration
}

// WorkStore is the worker-facing persistence port. Provider calls happen outside
// the commit transaction and are fenced by the claim.
type WorkStore interface {
	Prepare(context.Context, WorkClaim, PrepareRequest) (domain.ProcessingInput, error)
	Commit(context.Context, WorkClaim, domain.ProcessingInput, CommitRequest) error
	Block(context.Context, WorkClaim, string) error
	Fail(context.Context, WorkClaim, domain.ProcessingInput, Failure) error
}
