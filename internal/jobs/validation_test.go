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
