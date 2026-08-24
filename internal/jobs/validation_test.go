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
	return domain.ProcessingResult{Code: domain.ProcessingCandidatesExtracted, Candidates: []domain.ProcessingCandidate{{
		Content: "remember", MemoryType: "fact", SpaceID: "space-1", Confidence: 0.8, Sensitivity: "normal", SuggestionKind: "new",
		Evidence: []domain.ProcessingEvidence{{EventID: "event-1", EvidenceType: "direct_statement", StartOffset: &start, EndOffset: &end}}, RelatedMemoryIDs: []string{"memory-1"},
	}}}
}

func TestValidateProcessingResultRejectsEveryUntrustedBoundary(t *testing.T) {
	input := validProcessingInput()
	if err := validateProcessingResult(input, validProcessingResult()); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	tests := map[string]func(*domain.ProcessingResult){
		"unknown outcome": func(r *domain.ProcessingResult) { r.Code = "unknown" },
		"wrong type":      func(r *domain.ProcessingResult) { r.Candidates[0].MemoryType = "unknown" },
		"wrong space":     func(r *domain.ProcessingResult) { r.Candidates[0].SpaceID = "space-2" },
		"non-finite":      func(r *domain.ProcessingResult) { r.Candidates[0].Confidence = math.NaN() },
		"bad validity": func(r *domain.ProcessingResult) {
			now := time.Now()
			r.Candidates[0].ValidFrom, r.Candidates[0].ValidUntil = &now, &now
		},
		"cross-input evidence": func(r *domain.ProcessingResult) { r.Candidates[0].Evidence[0].EventID = "event-3" },
		"offset overflow":      func(r *domain.ProcessingResult) { end := int64(99); r.Candidates[0].Evidence[0].EndOffset = &end },
		"half offset":          func(r *domain.ProcessingResult) { r.Candidates[0].Evidence[0].EndOffset = nil },
		"unrelated memory":     func(r *domain.ProcessingResult) { r.Candidates[0].RelatedMemoryIDs = []string{"memory-2"} },
		"character budget":     func(r *domain.ProcessingResult) { r.Candidates[0].Content = "this candidate is too long" },
		"candidate budget": func(r *domain.ProcessingResult) {
			r.Candidates = append(r.Candidates, r.Candidates[0], r.Candidates[0])
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
		t.Fatal("no_memory with candidates accepted")
	}
}
