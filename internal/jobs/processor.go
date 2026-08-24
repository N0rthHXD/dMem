package jobs

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

const outcomeIndexRebuilt = "index_rebuilt"

type Processor interface {
	Enabled() bool
	Identity() string
	Process(context.Context, domain.ProcessingInput) domain.ProcessingResult
}

type NoopProcessor struct{}

func (NoopProcessor) Enabled() bool    { return false }
func (NoopProcessor) Identity() string { return "none" }

func (NoopProcessor) Process(context.Context, domain.ProcessingInput) domain.ProcessingResult {
	return domain.ProcessingResult{Code: domain.ProcessingNoProcessor}
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

type FeedbackAggregateRepository interface {
	RebuildFeedbackAggregatesClaimed(context.Context, storage.ClaimedJob) error
}

type AggregateRebuilder interface {
	RebuildClaimed(context.Context, storage.ClaimedJob) error
}

type DiskPressureSource interface {
	DiskPressure() (string, error)
}
