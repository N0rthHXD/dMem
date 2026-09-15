package jobs

import (
	"errors"
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
		if len(result.Proposals) != 0 {
			return errInvalidProcessingResult
		}
	case domain.ProcessingCandidatesExtracted:
		if len(result.Proposals) == 0 || len(result.Proposals) > input.Policy.MaxCandidates {
			return errInvalidProcessingResult
		}
	case domain.ProcessingTransientFailure, domain.ProcessingPermanentFailure, domain.ProcessingInvalidOutput:
		if len(result.Proposals) != 0 {
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
	allowedKinds := make(map[domain.MemoryKind]struct{}, len(input.Policy.AllowedMemoryTypes))
	for _, memoryType := range input.Policy.AllowedMemoryTypes {
		allowedKinds[domain.MemoryKind(memoryType)] = struct{}{}
	}
	events := map[string]domain.ProcessingEvent{input.Target.EventID: input.Target}
	for _, event := range input.ContextEvents {
		if event.SpaceID != input.Target.SpaceID {
			return errInvalidProcessingResult
		}
		events[event.EventID] = event
	}
	totalChars := 0
	for _, proposal := range result.Proposals {
		if !proposal.Action.Valid() {
			return errInvalidProcessingResult
		}
		revision := proposal.Revision
		if _, ok := allowedKinds[revision.Kind]; !ok || revision.Sensitivity != "normal" && revision.Sensitivity != "sensitive" {
			return errInvalidProcessingResult
		}
		if err := domain.ValidateMemoryRevision(revision); err != nil {
			return errInvalidProcessingResult
		}
		chars := utf8.RuneCountInString(revision.DisplayContent)
		totalChars += chars
		if revision.DisplayContent == "" || chars > input.Policy.MaxCandidateChars || totalChars > input.Policy.MaxTotalCandidateChars {
			return errInvalidProcessingResult
		}
		seenEvidence := make(map[string]struct{}, len(revision.Evidence))
		for _, evidence := range revision.Evidence {
			event, ok := events[evidence.EventID]
			if !ok {
				return invalidProcessingResult("invalid_evidence_reference")
			}
			if evidence.StartOffset != nil {
				length := int64(utf8.RuneCountInString(event.Content.Text))
				if event.Content.Kind == "reference" || *evidence.StartOffset < 0 || *evidence.EndOffset <= *evidence.StartOffset || *evidence.EndOffset > length {
					return invalidProcessingResult("invalid_evidence_reference")
				}
			}
			if _, duplicate := seenEvidence[evidence.EvidenceID]; duplicate {
				return invalidProcessingResult("invalid_evidence_reference")
			}
			seenEvidence[evidence.EvidenceID] = struct{}{}
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

func boundedProcessingString(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}
