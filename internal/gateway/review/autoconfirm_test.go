package review

import (
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func autoConfirmRevision() domain.MemoryRevision {
	start, end := int64(1), int64(5)
	return domain.MemoryRevision{
		Kind: domain.MemoryKindFact, DisplayContent: "Alice prefers tea", CanonicalContent: "alice prefers tea",
		Sensitivity: "normal", Confidence: 0.97, Specificity: 0.5,
		Evidence: []domain.RevisionEvidence{{EvidenceID: "ev-1", EventID: "event-1", Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}},
	}
}

func autoConfirmTestPolicy() AutoConfirmPolicy {
	return NewAutoConfirmPolicy(true, "", 0.95, []string{"fact", "preference", "plan"}, nil, 20)
}

func TestAutoConfirmPolicyDefaultsToDisabled(t *testing.T) {
	t.Parallel()
	if policy := NewAutoConfirmPolicy(false, "auto-review-v1", 0.95, []string{"fact"}, nil, 20); policy.Valid() {
		t.Fatalf("disabled request produced a valid policy: %+v", policy)
	}
	if (AutoConfirmPolicy{}).Valid() {
		t.Fatal("the zero policy must be invalid so storage treats it as disabled")
	}
	candidate := domain.Candidate{CandidateID: "candidate-1", SpaceID: "space-1", ProcessingVersion: "v1"}
	if decision := (AutoConfirmPolicy{}).Screen(candidate, autoConfirmRevision()); decision.Eligible || decision.ReasonCode != AutoConfirmDisabled {
		t.Fatalf("zero policy decision = %+v; want disabled", decision)
	}
}

func TestAutoConfirmPolicyCanonicalizesTheClosedKindSet(t *testing.T) {
	t.Parallel()
	policy := NewAutoConfirmPolicy(true, "", 0.95, []string{"plan", "topic", "opinion", "fact", "fact", "scene"}, nil, 20)
	if !policy.Valid() {
		t.Fatalf("policy is invalid: %+v", policy)
	}
	want := []domain.MemoryKind{domain.MemoryKindFact, domain.MemoryKindPlan}
	if len(policy.Kinds) != len(want) {
		t.Fatalf("kinds = %+v; want %+v", policy.Kinds, want)
	}
	for i := range want {
		if policy.Kinds[i] != want[i] {
			t.Fatalf("kinds = %+v; want %+v (sorted, deduplicated, aggregates dropped)", policy.Kinds, want)
		}
	}
	if policy.Version != DefaultAutoConfirmVersion {
		t.Fatalf("version = %q; want the published default", policy.Version)
	}
}

func TestAutoConfirmPolicyValidityBounds(t *testing.T) {
	t.Parallel()
	valid := autoConfirmTestPolicy()
	if !valid.Valid() {
		t.Fatalf("baseline policy is invalid: %+v", valid)
	}
	for name, mutate := range map[string]func(*AutoConfirmPolicy){
		"empty version":            func(p *AutoConfirmPolicy) { p.Version = "" },
		"confidence above one":     func(p *AutoConfirmPolicy) { p.MinConfidence = 1.5 },
		"confidence zero":          func(p *AutoConfirmPolicy) { p.MinConfidence = 0 },
		"rate limit zero":          func(p *AutoConfirmPolicy) { p.MaxPerSpacePerHour = 0 },
		"rate limit above bound":   func(p *AutoConfirmPolicy) { p.MaxPerSpacePerHour = MaxAutoConfirmsPerSpacePerHour + 1 },
		"no kinds":                 func(p *AutoConfirmPolicy) { p.Kinds = nil },
		"aggregate kind injected":  func(p *AutoConfirmPolicy) { p.Kinds = []domain.MemoryKind{domain.MemoryKindTopic} },
		"unknown kind injected":    func(p *AutoConfirmPolicy) { p.Kinds = []domain.MemoryKind{"opinion"} },
		"too many version filters": func(p *AutoConfirmPolicy) { p.ProcessingVersions = make([]string, MaxAutoConfirmVersions+1) },
	} {
		candidate := valid
		mutate(&candidate)
		if candidate.Valid() {
			t.Fatalf("%s: policy is still valid: %+v", name, candidate)
		}
	}
}

func TestAutoConfirmScreenEnforcesEveryBound(t *testing.T) {
	t.Parallel()
	policy := autoConfirmTestPolicy()
	revision := autoConfirmRevision()
	base := domain.Candidate{CandidateID: "candidate-1", SpaceID: "space-1", ProcessingVersion: "v1", Evidence: revision.Evidence}

	for name, tc := range map[string]struct {
		candidate domain.Candidate
		revision  domain.MemoryRevision
		want      string
	}{
		"eligible":            {base, revision, AutoConfirmEligible},
		"kind excluded":       {base, func() domain.MemoryRevision { r := revision; r.Kind = domain.MemoryKindEvent; return r }(), AutoConfirmKindExcluded},
		"sensitive":           {base, func() domain.MemoryRevision { r := revision; r.Sensitivity = "sensitive"; return r }(), AutoConfirmSensitivity},
		"missing sensitivity": {base, func() domain.MemoryRevision { r := revision; r.Sensitivity = ""; return r }(), AutoConfirmSensitivity},
		"below confidence":    {base, func() domain.MemoryRevision { r := revision; r.Confidence = 0.9499; return r }(), AutoConfirmBelowConfidence},
	} {
		decision := policy.Screen(tc.candidate, tc.revision)
		if tc.want == AutoConfirmEligible {
			if !decision.Eligible || decision.ReasonCode != AutoConfirmEligible {
				t.Fatalf("%s: decision = %+v", name, decision)
			}
			continue
		}
		if decision.Eligible || decision.ReasonCode != tc.want {
			t.Fatalf("%s: decision = %+v; want %s", name, decision, tc.want)
		}
	}

	derived := base
	derived.Evidence = []domain.RevisionEvidence{{EvidenceID: "ev-2", EventID: "event-1", Role: domain.EvidenceRoleDerived}}
	if decision := policy.Screen(derived, revision); decision.Eligible || decision.ReasonCode != AutoConfirmEvidenceRequired {
		t.Fatalf("derived-only decision = %+v; want %s", decision, AutoConfirmEvidenceRequired)
	}
	versioned := autoConfirmTestPolicy()
	versioned.ProcessingVersions = []string{"other-v1"}
	if decision := versioned.Screen(base, revision); decision.Eligible || decision.ReasonCode != AutoConfirmVersionExcluded {
		t.Fatalf("version decision = %+v; want %s", decision, AutoConfirmVersionExcluded)
	}
}

func TestAutoConfirmPublishesOnlyNonDestructiveDeterministicOutcomes(t *testing.T) {
	t.Parallel()
	policy := autoConfirmTestPolicy()
	for _, tc := range []struct {
		action         domain.ConsolidationAction
		classification domain.ConsolidationClassification
		want           bool
	}{
		{domain.ConsolidationCreate, domain.ConsolidationNone, true},
		{domain.ConsolidationCreate, domain.ConsolidationDeterministic, true},
		{domain.ConsolidationAttachEvidence, domain.ConsolidationDeterministic, true},
		{domain.ConsolidationRefine, domain.ConsolidationDeterministic, false},
		{domain.ConsolidationSupersede, domain.ConsolidationDeterministic, false},
		{domain.ConsolidationContest, domain.ConsolidationDeterministic, false},
		{domain.ConsolidationReject, domain.ConsolidationDeterministic, false},
		{domain.ConsolidationCreate, domain.ConsolidationProvider, false},
		{domain.ConsolidationAttachEvidence, domain.ConsolidationProvider, false},
	} {
		publishes, _ := policy.PublishesOutcome(tc.action, tc.classification)
		if publishes != tc.want {
			t.Fatalf("PublishesOutcome(%s, %s) = %t; want %t", tc.action, tc.classification, publishes, tc.want)
		}
	}
	if _, reason := policy.PublishesOutcome(domain.ConsolidationCreate, domain.ConsolidationProvider); reason != AutoConfirmProviderOutcome {
		t.Fatalf("provider reason = %q", reason)
	}
	if _, reason := policy.PublishesOutcome(domain.ConsolidationSupersede, domain.ConsolidationDeterministic); reason != AutoConfirmActionExcluded {
		t.Fatalf("destructive reason = %q", reason)
	}
}

func TestAutoConfirmReviewNoteRecordsPolicyAndThresholds(t *testing.T) {
	t.Parallel()
	policy := autoConfirmTestPolicy()
	similarity := 0.93
	note := policy.ReviewNote(autoConfirmRevision(), domain.ConsolidationAttachEvidence, &similarity)
	for _, want := range []string{
		DefaultAutoConfirmVersion, "min_confidence=0.9500", "confidence=0.9700", "kind=fact", "action=attach_evidence", "similarity=0.9300",
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q; missing %q", note, want)
		}
	}
	if again := policy.ReviewNote(autoConfirmRevision(), domain.ConsolidationAttachEvidence, &similarity); again != note {
		t.Fatalf("note is not deterministic: %q != %q", note, again)
	}
	if withSimilarity := policy.ReviewNote(autoConfirmRevision(), domain.ConsolidationCreate, nil); strings.Contains(withSimilarity, "similarity") {
		t.Fatalf("note = %q; want no similarity when none was computed", withSimilarity)
	}
}
