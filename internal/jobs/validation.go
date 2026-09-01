package jobs

import (
	"errors"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
)

var errInvalidProcessingResult = errors.New("invalid processing result")

type processingValidationError struct {
	code string
}

func (e *processingValidationError) Error() string { return e.code }

func invalidProcessingResult(code string) error {
	return &processingValidationError{code: code}
}

func processingValidationCode(err error) string {
	var validationErr *processingValidationError
	if errors.As(err, &validationErr) {
		return validationErr.code
	}
	return string(domain.ProcessingInvalidOutput)
}

func validateProcessingResult(input domain.ProcessingInput, result domain.ProcessingResult) error {
	if input.Policy.MaxCandidateChars == 0 {
		input.Policy.MaxCandidateChars = input.Policy.MaxContentChars
	}
	if input.Policy.MaxTotalCandidateChars == 0 {
		input.Policy.MaxTotalCandidateChars = input.Policy.MaxCandidateChars
	}
	switch result.Code {
	case domain.ProcessingNoProcessor, domain.ProcessingNoMemory, domain.ProcessingPolicyRejected:
		if len(result.Candidates) != 0 {
			return errInvalidProcessingResult
		}
	case domain.ProcessingCandidatesExtracted:
		if len(result.Candidates) == 0 || len(result.Candidates) > input.Policy.MaxCandidates {
			return errInvalidProcessingResult
		}
	case domain.ProcessingTransientFailure, domain.ProcessingPermanentFailure, domain.ProcessingInvalidOutput:
		if len(result.Candidates) != 0 {
			return errInvalidProcessingResult
		}
		return nil
	default:
		return errInvalidProcessingResult
	}
	if result.Usage.InputTokens < 0 || result.Usage.OutputTokens < 0 || !boundedProcessingString(result.ProviderRequestID, 128) || !boundedProcessingString(result.ReasonCode, 512) || !boundedProcessingString(result.ProviderIdentity, 512) || !boundedProcessingString(result.ModelIdentity, 512) || !boundedProcessingString(result.PolicyVersion, 512) {
		return errInvalidProcessingResult
	}
	omitted := 0
	for reason, count := range result.OmissionSummary {
		if count < 1 || !validOmissionReason(reason) {
			return errInvalidProcessingResult
		}
		omitted += count
	}
	if result.OmittedContextCount < 0 || omitted != result.OmittedContextCount {
		return errInvalidProcessingResult
	}
	allowedTypes := stringSet(input.Policy.AllowedMemoryTypes)
	events := map[string]domain.ProcessingEvent{input.Target.EventID: input.Target}
	for _, event := range input.ContextEvents {
		if event.SpaceID != input.Target.SpaceID {
			return errInvalidProcessingResult
		}
		events[event.EventID] = event
	}
	memories := make(map[string]domain.ProcessingMemory, len(input.ContextMemories))
	for _, memory := range input.ContextMemories {
		memories[memory.MemoryID] = memory
	}
	totalChars := 0
	for _, candidate := range result.Candidates {
		chars := utf8.RuneCountInString(candidate.Content)
		totalChars += chars
		if candidate.Content == "" || chars > input.Policy.MaxCandidateChars || totalChars > input.Policy.MaxTotalCandidateChars || candidate.SpaceID != input.Target.SpaceID {
			return errInvalidProcessingResult
		}
		if _, ok := allowedTypes[candidate.MemoryType]; !ok || math.IsNaN(candidate.Confidence) || math.IsInf(candidate.Confidence, 0) || candidate.Confidence < 0 || candidate.Confidence > 1 {
			return errInvalidProcessingResult
		}
		if candidate.Sensitivity != "normal" && candidate.Sensitivity != "sensitive" || !validSuggestion(candidate.SuggestionKind) || len(candidate.Evidence) == 0 || len(candidate.Evidence) > 100 || len(candidate.RelatedMemoryIDs) > 100 {
			return errInvalidProcessingResult
		}
		if candidate.ValidFrom != nil && candidate.ValidUntil != nil && !candidate.ValidFrom.Before(*candidate.ValidUntil) {
			return errInvalidProcessingResult
		}
		seenEvidence := make(map[string]struct{}, len(candidate.Evidence))
		for _, evidence := range candidate.Evidence {
			event, ok := events[evidence.EventID]
			if !ok || !validEvidenceType(evidence.EvidenceType) || (evidence.StartOffset == nil) != (evidence.EndOffset == nil) {
				return errInvalidProcessingResult
			}
			if evidence.StartOffset != nil {
				length := int64(utf8.RuneCountInString(event.Content.Text))
				if event.Content.Kind == "reference" || *evidence.StartOffset < 0 || *evidence.EndOffset <= *evidence.StartOffset || *evidence.EndOffset > length {
					return errInvalidProcessingResult
				}
			}
			if candidate.SuggestionKind != "new" && evidence.StartOffset == nil {
				return invalidProcessingResult("invalid_evidence_reference")
			}
			key := evidenceKey(evidence)
			if _, duplicate := seenEvidence[key]; duplicate {
				return invalidProcessingResult("invalid_evidence_reference")
			}
			seenEvidence[key] = struct{}{}
		}
		seenMemories := make(map[string]struct{}, len(candidate.RelatedMemoryIDs))
		for _, memoryID := range candidate.RelatedMemoryIDs {
			memory, ok := memories[memoryID]
			if !ok || memory.SpaceID == "" || candidate.SuggestionKind != "new" && !memory.ProposalCapable {
				return errInvalidProcessingResult
			}
			if _, duplicate := seenMemories[memoryID]; duplicate {
				return invalidProcessingResult("invalid_related_memory_reference")
			}
			seenMemories[memoryID] = struct{}{}
		}
		switch candidate.SuggestionKind {
		case "supplement", "replacement":
			if len(candidate.RelatedMemoryIDs) != 1 {
				return invalidProcessingResult("invalid_related_memory_reference")
			}
		case "conflict":
			if len(candidate.RelatedMemoryIDs) != 2 {
				return invalidProcessingResult("invalid_related_memory_reference")
			}
		case "duplicate":
			if len(candidate.RelatedMemoryIDs) == 0 {
				return invalidProcessingResult("invalid_related_memory_reference")
			}
		}
	}
	return nil
}

func validOmissionReason(value string) bool {
	switch value {
	case "context_limit", "context_space_not_allowed", "context_content_type_not_allowed", "context_source_type_not_allowed", "context_sensitivity_not_allowed", "invalid_context_event", "context_input_limit", "context_memory_sensitivity_not_allowed", "invalid_context_memory", "context_memory_policy_denied":
		return true
	}
	return false
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func boundedProcessingString(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func validSuggestion(value string) bool {
	switch value {
	case "new", "duplicate", "supplement", "replacement", "conflict":
		return true
	}
	return false
}

func validEvidenceType(value string) bool {
	return value == "direct_statement" || value == "tool_result" || value == "manual_reference"
}

func evidenceKey(value domain.ProcessingEvidence) string {
	if value.StartOffset == nil {
		return value.EventID + "|-"
	}
	return value.EventID + "|" + strconv.FormatInt(*value.StartOffset, 10) + "|" + strconv.FormatInt(*value.EndOffset, 10)
}
