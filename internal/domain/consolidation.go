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

// ConsolidationClassification records whether a consolidation decision came from
// the deterministic exact rules or from a policy-controlled provider proposal.
type ConsolidationClassification string

const (
	ConsolidationDeterministic ConsolidationClassification = "deterministic"
	ConsolidationProvider      ConsolidationClassification = "provider"
	ConsolidationNone          ConsolidationClassification = "none"
)

func (c ConsolidationClassification) Valid() bool {
	switch c {
	case ConsolidationDeterministic, ConsolidationProvider, ConsolidationNone:
		return true
	default:
		return false
	}
}

// ConsolidationDecision is the audited outcome of bounded consolidation recall
// and classification for one candidate before it becomes authority.
type ConsolidationDecision struct {
	DecisionID       string                      `json:"decision_id"`
	CandidateID      string                      `json:"candidate_id"`
	SpaceID          string                      `json:"space_id"`
	Action           ConsolidationAction         `json:"action"`
	ReasonCode       string                      `json:"reason_code"`
	TargetMemoryID   *string                     `json:"target_memory_id,omitempty"`
	TargetRevisionID *string                     `json:"target_revision_id,omitempty"`
	Similarity       *float64                    `json:"similarity,omitempty"`
	Classification   ConsolidationClassification `json:"classification_source"`
	AlgorithmVersion string                      `json:"algorithm_version"`
	CreatedAt        string                      `json:"created_at"`
}
