package jobs

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

const outcomeIndexRebuilt = "index_rebuilt"

// Processor is the dMem Provider Route processing adapter. It is present only
// when the route is explicitly enabled and a real provider is configured; there
// is no no-op implementation that could report success for a model job.
type Processor interface {
	Enabled() bool
	Identity() string
	Process(context.Context, domain.ProcessingInput) domain.ProcessingResult
}

type GraphProcessor interface {
	ProcessGraph(context.Context, domain.GraphExtractionInput) domain.GraphExtractionResult
}

type IndexRepository interface {
	RebuildFTSClaimed(context.Context, storage.ClaimedJob) error
	EmbedMemoryClaimed(context.Context, storage.ClaimedJob, string) error
	RebuildSemanticClaimed(context.Context, storage.ClaimedJob) error
}

type EntityIndexRepository interface {
	RebuildEntityIndexClaimed(context.Context, storage.ClaimedJob) error
}

type RelationIndexRepository interface {
	RebuildRelationIndexClaimed(context.Context, storage.ClaimedJob) error
}

type GraphIndexRepository interface {
	RebuildGraphIndexClaimed(context.Context, storage.ClaimedJob) error
}

type FeedbackAggregateRepository interface {
	RebuildFeedbackAggregatesClaimed(context.Context, storage.ClaimedJob) error
}

type AggregateRebuilder interface {
	RebuildClaimed(context.Context, storage.ClaimedJob) error
}

type DiskPressureSource interface {
	DiskPressure() (string, error)
}
