package domain

// ConsolidationAction is the deterministic decision applied to a structured
// candidate proposal relative to existing memory revisions (ADR 0035).
type ConsolidationAction string

const (
	ConsolidationAttachEvidence ConsolidationAction = "attach_evidence"
	ConsolidationRefine         ConsolidationAction = "refine"
	ConsolidationSupersede      ConsolidationAction = "supersede"
	ConsolidationContest        ConsolidationAction = "contest"
	ConsolidationCreate         ConsolidationAction = "create"
	ConsolidationReject         ConsolidationAction = "reject"
)

func (a ConsolidationAction) Valid() bool {
	switch a {
	case ConsolidationAttachEvidence, ConsolidationRefine, ConsolidationSupersede,
		ConsolidationContest, ConsolidationCreate, ConsolidationReject:
		return true
	default:
		return false
	}
}

// MemoryCandidateProposal is a structured extraction proposal plus the
// consolidation decision and the current-revision targets it applies to. The
// provider only proposes the revision; deterministic validation and review
// policy decide whether the action is published.
type MemoryCandidateProposal struct {
	Revision MemoryRevision
	Action   ConsolidationAction
	Targets  []EvolutionTarget
}
