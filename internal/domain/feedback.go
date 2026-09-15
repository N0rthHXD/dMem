package domain

// Feedback purposes separate three distinct learning signals. A feedback event
// carries exactly one purpose; its type and reason are valid only for that
// purpose.
const (
	FeedbackPurposeMemoryQuality = "memory_quality_feedback"
	FeedbackPurposeRetrieval     = "retrieval_feedback"
	FeedbackPurposeAnswer        = "answer_feedback"
)

type feedbackPurposeContract struct {
	purpose string
	types   []string
	reasons []string
}

// feedbackContracts is the canonical, ordered purpose registry. Order is stable
// so version identities and error messages do not depend on map iteration.
var feedbackContracts = []feedbackPurposeContract{
	{
		purpose: FeedbackPurposeMemoryQuality,
		types:   []string{"confirm", "correct", "contest", "supplement"},
		reasons: []string{"fact_incorrect", "fact_incomplete", "fact_outdated", "conflicts_with_evidence", "duplicate", "other"},
	},
	{
		purpose: FeedbackPurposeRetrieval,
		types:   []string{"useful", "irrelevant"},
		reasons: []string{"answered_query", "relevant_context", "not_relevant", "other"},
	},
	{
		purpose: FeedbackPurposeAnswer,
		types:   []string{"supported", "unsupported"},
		reasons: []string{"evidence_supported", "evidence_insufficient", "evidence_contradicted", "other"},
	},
}

// FeedbackPurposes returns the ordered list of supported feedback purposes.
func FeedbackPurposes() []string {
	result := make([]string, 0, len(feedbackContracts))
	for _, contract := range feedbackContracts {
		result = append(result, contract.purpose)
	}
	return result
}

// FeedbackPurposeRequiresDecision reports whether a purpose must bind a
// retrieval decision context. Only retrieval and answer feedback can reference
// a query, its ranking, and the selected or rejected candidate rank.
func FeedbackPurposeRequiresDecision(purpose string) bool {
	return purpose == FeedbackPurposeRetrieval || purpose == FeedbackPurposeAnswer
}

// ValidFeedbackCombination reports whether the purpose/type/reason triple is one
// of the closed, explicit combinations.
func ValidFeedbackCombination(purpose, feedbackType, reason string) bool {
	contract, ok := feedbackContractFor(purpose)
	if !ok {
		return false
	}
	return contains(contract.types, feedbackType) && contains(contract.reasons, reason)
}

// FeedbackTypesForPurpose returns the allowed types for a purpose, or nil.
func FeedbackTypesForPurpose(purpose string) []string {
	if contract, ok := feedbackContractFor(purpose); ok {
		return append([]string(nil), contract.types...)
	}
	return nil
}

// FeedbackReasonsForPurpose returns the allowed reasons for a purpose, or nil.
func FeedbackReasonsForPurpose(purpose string) []string {
	if contract, ok := feedbackContractFor(purpose); ok {
		return append([]string(nil), contract.reasons...)
	}
	return nil
}

// CanProposeEvolution reports whether a purpose/type combination may create an
// explicit evolution proposal. Only durable memory quality feedback can request
// evolution, and its result is always a proposal, never an authority change.
func CanProposeEvolution(purpose, feedbackType string) bool {
	return purpose == FeedbackPurposeMemoryQuality &&
		(feedbackType == "correct" || feedbackType == "supplement" || feedbackType == "contest")
}

func feedbackContractFor(purpose string) (feedbackPurposeContract, bool) {
	for _, contract := range feedbackContracts {
		if contract.purpose == purpose {
			return contract, true
		}
	}
	return feedbackPurposeContract{}, false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
