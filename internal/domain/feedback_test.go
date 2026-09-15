package domain

import "testing"

func TestFeedbackPurposesSeparateTypesAndReasons(t *testing.T) {
	cases := []struct {
		name    string
		purpose string
		kind    string
		reason  string
		valid   bool
	}{
		{"memory-quality correct", FeedbackPurposeMemoryQuality, "correct", "fact_incorrect", true},
		{"memory-quality contest", FeedbackPurposeMemoryQuality, "contest", "conflicts_with_evidence", true},
		{"memory-quality supplement", FeedbackPurposeMemoryQuality, "supplement", "fact_incomplete", true},
		{"memory-quality rejects retrieval type", FeedbackPurposeMemoryQuality, "useful", "answered_query", false},
		{"memory-quality rejects retrieval reason", FeedbackPurposeMemoryQuality, "correct", "answered_query", false},
		{"retrieval useful", FeedbackPurposeRetrieval, "useful", "answered_query", true},
		{"retrieval irrelevant", FeedbackPurposeRetrieval, "irrelevant", "not_relevant", true},
		{"retrieval rejects quality type", FeedbackPurposeRetrieval, "correct", "fact_incorrect", false},
		{"retrieval rejects answer reason", FeedbackPurposeRetrieval, "useful", "evidence_supported", false},
		{"answer supported", FeedbackPurposeAnswer, "supported", "evidence_supported", true},
		{"answer unsupported", FeedbackPurposeAnswer, "unsupported", "evidence_insufficient", true},
		{"answer rejects retrieval type", FeedbackPurposeAnswer, "useful", "answered_query", false},
		{"unknown purpose", "click_feedback", "useful", "answered_query", false},
		{"empty", "", "", "", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidFeedbackCombination(test.purpose, test.kind, test.reason); got != test.valid {
				t.Fatalf("ValidFeedbackCombination(%q,%q,%q) = %t, want %t", test.purpose, test.kind, test.reason, got, test.valid)
			}
		})
	}
}

func TestFeedbackPurposesAreClosedAndVersioned(t *testing.T) {
	versioned := FeedbackPurposes()
	if len(versioned) != 3 {
		t.Fatalf("purposes = %v", versioned)
	}
	for _, purpose := range []string{FeedbackPurposeMemoryQuality, FeedbackPurposeRetrieval, FeedbackPurposeAnswer} {
		if !containsString(versioned, purpose) {
			t.Fatalf("missing purpose %q in %v", purpose, versioned)
		}
	}
}

func TestFeedbackPurposeRequiresDecisionContext(t *testing.T) {
	if FeedbackPurposeRequiresDecision(FeedbackPurposeMemoryQuality) {
		t.Fatal("memory quality feedback must not require retrieval decision context")
	}
	for _, purpose := range []string{FeedbackPurposeRetrieval, FeedbackPurposeAnswer} {
		if !FeedbackPurposeRequiresDecision(purpose) {
			t.Fatalf("purpose %q must require retrieval decision context", purpose)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
