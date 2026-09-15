package jobs

import (
	"math"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func validProcessingInput() domain.ProcessingInput {
	return domain.ProcessingInput{
		ProcessorIdentity: "processor/test", ProcessingVersion: "v1", JobID: "job-1", Attempt: 1,
		Target:          domain.ProcessingEvent{EventID: "event-1", SpaceID: "space-1", Content: domain.ProcessingContent{Kind: "text", MediaType: "text/plain", Text: "source text"}},
		ContextEvents:   []domain.ProcessingEvent{{EventID: "event-2", SpaceID: "space-1", Content: domain.ProcessingContent{Kind: "text", MediaType: "text/plain", Text: "context"}}},
		ContextMemories: []domain.ProcessingMemory{{MemoryID: "memory-1", SpaceID: "space-1"}},
		Policy:          domain.ProcessingPolicy{AllowedMemoryTypes: []string{"fact"}, MaxCandidates: 2, MaxContentChars: 20},
	}
}

func validProcessingResult() domain.ProcessingResult {
	start, end := int64(0), int64(6)
	return domain.ProcessingResult{Code: domain.ProcessingCandidatesExtracted, Proposals: []domain.MemoryCandidateProposal{{
		Action: domain.ConsolidationCreate,
		Revision: domain.MemoryRevision{
			Kind: domain.MemoryKindFact, DisplayContent: "remember", CanonicalContent: "remember",
			Confidence: 0.8, Specificity: 0.5, Sensitivity: "normal",
			Evidence: []domain.RevisionEvidence{{EvidenceID: "ev-1", EventID: "event-1", Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}},
		},
	}}}
}

func TestValidateProcessingResultRejectsEveryUntrustedBoundary(t *testing.T) {
	input := validProcessingInput()
	if err := validateProcessingResult(input, validProcessingResult()); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	tests := map[string]func(*domain.ProcessingResult){
		"unknown outcome": func(r *domain.ProcessingResult) { r.Code = "unknown" },
		"bad action":      func(r *domain.ProcessingResult) { r.Proposals[0].Action = "explode" },
		"wrong kind":      func(r *domain.ProcessingResult) { r.Proposals[0].Revision.Kind = "constraint" },
		"non-finite":      func(r *domain.ProcessingResult) { r.Proposals[0].Revision.Confidence = math.NaN() },
		"bad validity": func(r *domain.ProcessingResult) {
			now := time.Now()
			r.Proposals[0].Revision.ValidFrom, r.Proposals[0].Revision.ValidUntil = &now, &now
		},
		"cross-input evidence": func(r *domain.ProcessingResult) { r.Proposals[0].Revision.Evidence[0].EventID = "event-3" },
		"offset overflow": func(r *domain.ProcessingResult) {
			end := int64(99)
			r.Proposals[0].Revision.Evidence[0].EndOffset = &end
		},
		"half offset": func(r *domain.ProcessingResult) { r.Proposals[0].Revision.Evidence[0].EndOffset = nil },
		"character budget": func(r *domain.ProcessingResult) {
			r.Proposals[0].Revision.DisplayContent = "this candidate is too long"
		},
		"proposal budget": func(r *domain.ProcessingResult) {
			r.Proposals = append(r.Proposals, r.Proposals[0], r.Proposals[0])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			result := validProcessingResult()
			mutate(&result)
			if err := validateProcessingResult(input, result); err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
}

func TestValidateProcessingOutcomeCombinations(t *testing.T) {
	input := validProcessingInput()
	for _, code := range []domain.ProcessingOutcome{domain.ProcessingNoProcessor, domain.ProcessingNoMemory, domain.ProcessingPolicyRejected, domain.ProcessingTransientFailure, domain.ProcessingPermanentFailure, domain.ProcessingInvalidOutput} {
		if err := validateProcessingResult(input, domain.ProcessingResult{Code: code, ReasonCode: "safe_code"}); err != nil {
			t.Errorf("%s rejected: %v", code, err)
		}
	}
	result := validProcessingResult()
	result.Code = domain.ProcessingNoMemory
	if err := validateProcessingResult(input, result); err == nil {
		t.Fatal("no_memory with proposals accepted")
	}
}

func TestValidateProcessingResultReturnsSpecificContractErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*domain.ProcessingResult)
		want   string
	}{
		{
			name: "unknown evidence event",
			mutate: func(result *domain.ProcessingResult) {
				result.Proposals[0].Revision.Evidence[0].EventID = "event-3"
			},
			want: "invalid_evidence_reference",
		},
		{
			name: "duplicate evidence id",
			mutate: func(result *domain.ProcessingResult) {
				evidence := result.Proposals[0].Revision.Evidence[0]
				result.Proposals[0].Revision.Evidence = append(result.Proposals[0].Revision.Evidence, evidence)
			},
			want: "invalid_evidence_reference",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := validProcessingResult()
			test.mutate(&result)
			if got := processingValidationCode(validateProcessingResult(validProcessingInput(), result)); got != test.want {
				t.Fatalf("validation code = %q, want %q", got, test.want)
			}
		})
	}
}

func structuredProcessingInput() domain.ProcessingInput {
	input := validProcessingInput()
	input.Target.Content.Text = "alice prefers tea"
	input.Policy.MaxContentChars = 200
	input.Policy.MaxCandidateChars = 200
	input.Policy.MaxTotalCandidateChars = 200
	return input
}

func structuredProcessingResult() domain.ProcessingResult {
	eventStart := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	eventEnd := time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)
	validFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	validUntil := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	start, end := int64(0), int64(5)
	return domain.ProcessingResult{Code: domain.ProcessingCandidatesExtracted, Proposals: []domain.MemoryCandidateProposal{{
		Action: domain.ConsolidationCreate,
		Revision: domain.MemoryRevision{
			Kind: domain.MemoryKindFact, DisplayContent: "Alice prefers tea", CanonicalContent: "alice prefers tea",
			SubjectEntityIDs: []string{"alice"}, ObjectEntityIDs: []string{"tea"}, EventType: "preference",
			Attributes:     map[string]string{"preference": "tea"},
			EventTimeStart: &eventStart, EventTimeEnd: &eventEnd, TimePrecision: domain.TimePrecisionExact, TimeSource: "relative_anchor",
			ValidFrom: &validFrom, ValidUntil: &validUntil, SourceSpeaker: "alice", Language: "en",
			Sensitivity: "normal", Specificity: 0.7, Confidence: 0.9,
			Evidence: []domain.RevisionEvidence{{EvidenceID: "ev-1", EventID: "event-1", Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}},
		},
	}}}
}

func TestValidateProcessingResultAcceptsStructuredCandidateFields(t *testing.T) {
	if err := validateProcessingResult(structuredProcessingInput(), structuredProcessingResult()); err != nil {
		t.Fatalf("structured candidate rejected: %v", err)
	}
}

func TestValidateProcessingResultRejectsStructuredViolations(t *testing.T) {
	overlapStart, overlapEnd := int64(3), int64(8)
	tests := map[string]struct {
		input  func() domain.ProcessingInput
		mutate func(*domain.ProcessingResult)
		want   string
	}{
		"impossible event interval": {
			mutate: func(result *domain.ProcessingResult) {
				revision := &result.Proposals[0].Revision
				start := time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)
				end := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
				revision.EventTimeStart, revision.EventTimeEnd = &start, &end
			},
			want: "invalid_output",
		},
		"impossible valid interval": {
			mutate: func(result *domain.ProcessingResult) {
				revision := &result.Proposals[0].Revision
				start := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
				end := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
				revision.ValidFrom, revision.ValidUntil = &start, &end
			},
			want: "invalid_output",
		},
		"context-only evidence": {
			mutate: func(result *domain.ProcessingResult) {
				result.Proposals[0].Revision.Evidence[0].Role = domain.EvidenceRoleContext
			},
			want: "invalid_output",
		},
		"invalid entity reference": {
			mutate: func(result *domain.ProcessingResult) {
				result.Proposals[0].Revision.SubjectEntityIDs = []string{"alice", "alice"}
			},
			want: "invalid_entity_reference",
		},
		"empty entity reference": {
			mutate: func(result *domain.ProcessingResult) {
				result.Proposals[0].Revision.SubjectEntityIDs = []string{"  "}
			},
			want: "invalid_entity_reference",
		},
		"overlapping evidence ranges": {
			mutate: func(result *domain.ProcessingResult) {
				evidence := result.Proposals[0].Revision.Evidence[0]
				evidence.EvidenceID = "ev-2"
				evidence.StartOffset, evidence.EndOffset = &overlapStart, &overlapEnd
				result.Proposals[0].Revision.Evidence = append(result.Proposals[0].Revision.Evidence, evidence)
			},
			want: "overlapping_evidence_range",
		},
		"output bounds": {
			mutate: func(result *domain.ProcessingResult) {
				result.Proposals[0].Revision.DisplayContent = "this candidate exceeds the configured character budget"
			},
			want: "invalid_output",
		},
		"source-scope violation": {
			input: func() domain.ProcessingInput {
				input := structuredProcessingInput()
				input.ContextEvents = append(input.ContextEvents, domain.ProcessingEvent{EventID: "event-x", SpaceID: "space-2", Content: domain.ProcessingContent{Kind: "text", Text: "other scope"}})
				return input
			},
			mutate: func(result *domain.ProcessingResult) {
				evidence := result.Proposals[0].Revision.Evidence[0]
				evidence.EvidenceID = "ev-2"
				result.Proposals[0].Revision.Evidence = append(result.Proposals[0].Revision.Evidence, evidence)
			},
			want: "invalid_output",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			input := structuredProcessingInput()
			if test.input != nil {
				input = test.input()
			}
			result := structuredProcessingResult()
			test.mutate(&result)
			if got := processingValidationCode(validateProcessingResult(input, result)); got != test.want {
				t.Fatalf("validation code = %q, want %q", got, test.want)
			}
		})
	}
}
