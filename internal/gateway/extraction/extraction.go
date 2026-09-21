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

// Raw event input bounds. Transport adapters additionally cap the request body;
// these are the domain-level limits that make one accepted event bounded
// regardless of which adapter submitted it. They are deliberately larger than
// the Agent Tool memory bounds because a raw event is unprocessed material that
// a processing provider later summarizes into bounded candidates.
const (
	// MaxContentRunes bounds raw event text content. It is below the protocol
	// body cap so one event can never consume an unbounded share of the
	// authority store or of a provider request.
	MaxContentRunes = 64 << 10
	// MaxReferenceURIRunes bounds the recorded metadata of an external
	// reference. dMem records the reference and never copies the target.
	MaxReferenceURIRunes = 4096
	// MaxMetadataKeys bounds the number of keys at one metadata level and the
	// number of items in one metadata array.
	MaxMetadataKeys = 32
	// MaxMetadataDepth bounds metadata nesting so a deeply nested payload cannot
	// exhaust the decoder or the store.
	MaxMetadataDepth = 4
	// MaxMetadataBytes bounds the encoded size of one metadata value.
	MaxMetadataBytes = 16 << 10
	// MaxJSONValueDepth bounds embedded JSON content nesting.
	MaxJSONValueDepth = 8
	// MaxJSONValueNodes bounds the total number of embedded JSON nodes.
	MaxJSONValueNodes = 4096
)

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
	OperationID       string
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
