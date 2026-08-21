package jobs

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
)

type OutcomeCode string

const (
	OutcomeNoProcessor         OutcomeCode = "no_processor"
	OutcomeNoMemory            OutcomeCode = "no_memory"
	OutcomeCandidatesExtracted OutcomeCode = "candidates_extracted"
	OutcomeTransientFailure    OutcomeCode = "transient_failure"
	OutcomePermanentFailure    OutcomeCode = "permanent_failure"
	OutcomeInvalidOutput       OutcomeCode = "invalid_output"
	OutcomePolicyRejected      OutcomeCode = "policy_rejected"
	OutcomeIndexRebuilt        OutcomeCode = "index_rebuilt"
)

type Outcome struct {
	Code    OutcomeCode
	Value   any
	Message string
}

type Processor interface {
	Process(context.Context, domain.Job) Outcome
}

type NoopProcessor struct{}

func (NoopProcessor) Process(context.Context, domain.Job) Outcome {
	return Outcome{Code: OutcomeNoProcessor}
}

type IndexRepository interface {
	RebuildFTS(context.Context) error
}

type DispatchProcessor struct {
	Extract Processor
	Indexes IndexRepository
}

func (p DispatchProcessor) Process(ctx context.Context, job domain.Job) Outcome {
	switch job.JobType {
	case "extract_memory":
		if p.Extract == nil {
			return Outcome{Code: OutcomeNoProcessor}
		}
		return p.Extract.Process(ctx, job)
	case "rebuild_fts":
		if p.Indexes == nil || job.TargetType != "index" || job.TargetID != "memory_fts" {
			return Outcome{Code: OutcomeInvalidOutput, Message: "invalid index rebuild target"}
		}
		if err := p.Indexes.RebuildFTS(ctx); err != nil {
			return Outcome{Code: OutcomeTransientFailure, Message: "FTS rebuild failed"}
		}
		return Outcome{Code: OutcomeIndexRebuilt}
	default:
		return Outcome{Code: OutcomeInvalidOutput, Message: "unsupported job type"}
	}
}
