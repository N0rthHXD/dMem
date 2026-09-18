package extraction

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// EventStore is the narrow ingress persistence port the Extraction module needs.
type EventStore interface {
	// IngestEvent atomically persists the raw event, its extraction job, and its
	// audit record. The bool reports an idempotent replay.
	IngestEvent(context.Context, gateway.WriteIdentity, IngestParams) (IngestRecord, bool, error)
	// GetEvent returns one raw event by identifier.
	GetEvent(context.Context, string) (domain.RawEvent, error)
}

// IngestParams is the transport-free persistence input for event ingestion.
type IngestParams struct {
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

// IngestRecord is the persisted result of an event ingestion.
type IngestRecord struct {
	OperationID string
	EventID     string
	Status      string
	JobIDs      []string
	AcceptedAt  string
}
