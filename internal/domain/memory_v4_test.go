package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func baseRevision() MemoryRevision {
	return MemoryRevision{
		RevisionID:       "rev-1",
		MemoryID:         "mem-1",
		Kind:             MemoryKindFact,
		DisplayContent:   "Alice prefers tea",
		CanonicalContent: "alice prefers tea",
		Confidence:       0.9,
		Specificity:      0.7,
		Language:         "en",
		SourceSpeaker:    "alice",
		Evidence: []RevisionEvidence{
			{EvidenceID: "ev-1", EventID: "event-1", Role: EvidenceRoleDirect},
		},
	}
}

func TestMemoryUsesReplacementKindAndRevisionModels(t *testing.T) {
	t.Parallel()

	memory := Memory{Kind: MemoryKindProcedure, CurrentRevision: baseRevision()}
	if memory.Kind != MemoryKindProcedure || memory.CurrentRevision.Kind != MemoryKindFact {
		t.Fatalf("memory kinds = %q, %q", memory.Kind, memory.CurrentRevision.Kind)
	}
}

func TestMemoryKindValidation(t *testing.T) {
	t.Parallel()
	for _, kind := range []MemoryKind{
		MemoryKindFact, MemoryKindEvent, MemoryKindPreference, MemoryKindPlan,
		MemoryKindProcedure, MemoryKindScene, MemoryKindTopic, MemoryKindTimeline,
	} {
		if !kind.Valid() {
			t.Fatalf("kind %q should be valid", kind)
		}
	}
	for _, kind := range []MemoryKind{"", "constraint", "decision", "project_context", "agent_experience", "aggregate"} {
		if kind.Valid() {
			t.Fatalf("kind %q should be rejected", kind)
		}
	}
}

func TestValidAggregateKind(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"scene", "topic", "timeline"} {
		if !ValidAggregateKind(kind) {
			t.Fatalf("aggregate kind %q should be valid", kind)
		}
	}
	for _, kind := range []string{"", "fact", "event", "summary", "aggregate"} {
		if ValidAggregateKind(kind) {
			t.Fatalf("aggregate kind %q should be rejected", kind)
		}
	}
}

func TestMemoryRevisionSeparationOfEventAndValidTime(t *testing.T) {
	t.Parallel()
	eventStart := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	eventEnd := eventStart.Add(2 * time.Hour)
	validFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	validUntil := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	rev := baseRevision()
	rev.Kind = MemoryKindEvent
	rev.EventTimeStart = &eventStart
	rev.EventTimeEnd = &eventEnd
	rev.TimePrecision = TimePrecisionExact
	rev.ValidFrom = &validFrom
	rev.ValidUntil = &validUntil
	if err := ValidateMemoryRevision(rev); err != nil {
		t.Fatalf("valid separated event/valid time rejected: %v", err)
	}

	bad := rev
	bad.EventTimeStart = &eventEnd
	bad.EventTimeEnd = &eventStart
	if !errors.Is(ValidateMemoryRevision(bad), ErrEventWindowInvalid) {
		t.Fatalf("inverted event window should be rejected, got %v", ValidateMemoryRevision(bad))
	}

	bad = rev
	bad.ValidFrom = &validUntil
	bad.ValidUntil = &validFrom
	if !errors.Is(ValidateMemoryRevision(bad), ErrValidWindowInvalid) {
		t.Fatalf("inverted valid window should be rejected, got %v", ValidateMemoryRevision(bad))
	}
}

func TestMemoryRevisionFieldValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*MemoryRevision)
		want   error
	}{
		{"missing kind", func(r *MemoryRevision) { r.Kind = "" }, ErrKindRequired},
		{"unsupported kind", func(r *MemoryRevision) { r.Kind = "aggregate" }, ErrKindRequired},
		{"empty canonical content", func(r *MemoryRevision) { r.CanonicalContent = "  " }, ErrCanonicalContentEmpty},
		{"confidence high", func(r *MemoryRevision) { r.Confidence = 1.5 }, ErrConfidenceOutOfRange},
		{"confidence low", func(r *MemoryRevision) { r.Confidence = -0.1 }, ErrConfidenceOutOfRange},
		{"confidence NaN", func(r *MemoryRevision) { r.Confidence = math.NaN() }, ErrConfidenceOutOfRange},
		{"confidence Inf", func(r *MemoryRevision) { r.Confidence = math.Inf(1) }, ErrConfidenceOutOfRange},
		{"specificity high", func(r *MemoryRevision) { r.Specificity = 2 }, ErrSpecificityOutOfRange},
		{"specificity NaN", func(r *MemoryRevision) { r.Specificity = math.NaN() }, ErrSpecificityOutOfRange},
		{"bad precision", func(r *MemoryRevision) { r.TimePrecision = "fortnight" }, ErrTimePrecisionInvalid},
		{"bad evidence span", func(r *MemoryRevision) {
			start, end := int64(5), int64(2)
			r.Evidence = []RevisionEvidence{{EvidenceID: "ev", EventID: "e", Role: EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}}
		}, ErrEvidenceInvalid},
		{"half evidence span", func(r *MemoryRevision) {
			start := int64(0)
			r.Evidence = []RevisionEvidence{{EvidenceID: "ev", EventID: "e", Role: EvidenceRoleDirect, StartOffset: &start}}
		}, ErrEvidenceInvalid},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rev := baseRevision()
			tc.mutate(&rev)
			if err := ValidateMemoryRevision(rev); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v; want %v", err, tc.want)
			}
		})
	}
}

func TestMemoryRevisionRequiresAuthoritativeEvidence(t *testing.T) {
	t.Parallel()
	contextOnly := baseRevision()
	contextOnly.Evidence = []RevisionEvidence{
		{EvidenceID: "ctx", EventID: "event-1", Role: EvidenceRoleContext},
		{EvidenceID: "anchor", EventID: "event-2", Role: EvidenceRoleTemporalAnchor},
	}
	if !errors.Is(ValidateMemoryRevision(contextOnly), ErrNoAuthoritativeEvidence) {
		t.Fatalf("context-only evidence should not establish authority")
	}

	derived := contextOnly
	derived.Evidence = []RevisionEvidence{
		{EvidenceID: "ctx", EventID: "event-1", Role: EvidenceRoleContext},
		{EvidenceID: "der", EventID: "event-2", Role: EvidenceRoleDerived},
	}
	if err := ValidateMemoryRevision(derived); err != nil {
		t.Fatalf("derived evidence should establish authority: %v", err)
	}
	if HasAuthoritativeEvidence(contextOnly.Evidence) {
		t.Fatal("context-only evidence reported as authoritative")
	}
}

func TestOrderRevisionEvidenceStableChronologicalOrder(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	seq1, seq2 := int64(5), int64(1)
	offA, offB := int64(10), int64(2)
	evidence := []RevisionEvidence{
		{EvidenceID: "later", EventID: "e1", EventTime: &t1, Role: EvidenceRoleDirect},
		{EvidenceID: "earlier", EventID: "e2", EventTime: &t2, Role: EvidenceRoleDirect},
		{EvidenceID: "tie-b", EventID: "e3", EventTime: &t2, EventSequence: &seq1, StartOffset: &offA, Role: EvidenceRoleDirect},
		{EvidenceID: "tie-a", EventID: "e4", EventTime: &t2, EventSequence: &seq2, StartOffset: &offB, Role: EvidenceRoleDirect},
	}
	OrderRevisionEvidence(evidence)
	got := []string{evidence[0].EvidenceID, evidence[1].EvidenceID, evidence[2].EvidenceID, evidence[3].EvidenceID}
	want := []string{"tie-a", "tie-b", "earlier", "later"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v; want %v", got, want)
		}
	}

	// Missing event time sorts last but stays deterministic by evidence ID.
	missing := []RevisionEvidence{
		{EvidenceID: "z", EventID: "e1", Role: EvidenceRoleDirect},
		{EvidenceID: "a", EventID: "e2", Role: EvidenceRoleDirect},
		{EvidenceID: "m", EventID: "e3", EventTime: &t2, Role: EvidenceRoleDirect},
	}
	OrderRevisionEvidence(missing)
	if missing[0].EvidenceID != "m" || missing[1].EvidenceID != "a" || missing[2].EvidenceID != "z" {
		t.Fatalf("missing-time order = %s,%s,%s", missing[0].EvidenceID, missing[1].EvidenceID, missing[2].EvidenceID)
	}
}
