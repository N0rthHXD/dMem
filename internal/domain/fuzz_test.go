package domain

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

// FuzzPredicateRegistryIsClosed proves the graph predicate registry is a closed
// typed vocabulary: lookup never panics, only registered names resolve, and any
// resolved definition is internally consistent with the declared subject and
// object types. Raw provider predicates must never resolve.
func FuzzPredicateRegistryIsClosed(f *testing.F) {
	for _, name := range PredicateNames() {
		f.Add(name)
	}
	f.Add("works_for")
	f.Add("free_form_predicate")
	f.Add("")
	f.Add("  participates_in  ")
	f.Add("PARTICIPATES_IN")
	f.Add("participates_in\x00")
	f.Add(string([]byte{0xff, 0xfe}))

	registered := make(map[string]struct{}, len(PredicateNames()))
	for _, name := range PredicateNames() {
		registered[name] = struct{}{}
	}

	f.Fuzz(func(t *testing.T, name string) {
		definition, ok := LookupPredicate(name)
		if !ok {
			return
		}
		if definition.Name == "" {
			t.Fatalf("resolved predicate %q has no canonical name", name)
		}
		if _, exists := registered[definition.Name]; !exists {
			t.Fatalf("resolved predicate %q is not in the published registry", definition.Name)
		}
		if !definition.Direction.Valid() {
			t.Fatalf("predicate %q has invalid direction %q", definition.Name, definition.Direction)
		}
		if !definition.Cardinality.Valid() {
			t.Fatalf("predicate %q has invalid cardinality %q", definition.Name, definition.Cardinality)
		}
		if !definition.Transitivity.Valid() {
			t.Fatalf("predicate %q has invalid transitivity %q", definition.Name, definition.Transitivity)
		}
		if !definition.TemporalPolicy.Valid() {
			t.Fatalf("predicate %q has invalid temporal policy %q", definition.Name, definition.TemporalPolicy)
		}
		switch definition.AutoConfirm {
		case PredicateAutoConfirmNever, PredicateAutoConfirmExactMatch, PredicateAutoConfirmThreshold:
		default:
			t.Fatalf("predicate %q has invalid auto-confirm policy %q", definition.Name, definition.AutoConfirm)
		}
		if len(definition.SubjectTypes) == 0 {
			t.Fatalf("predicate %q declares no subject types", definition.Name)
		}
		if definition.Literal() {
			switch definition.LiteralType {
			case "string", "number", "boolean":
			default:
				t.Fatalf("literal predicate %q has invalid literal type %q", definition.Name, definition.LiteralType)
			}
			if len(definition.ObjectTypes) != 0 {
				t.Fatalf("literal predicate %q must not declare object entity types", definition.Name)
			}
			return
		}
		if len(definition.ObjectTypes) == 0 {
			t.Fatalf("entity predicate %q declares no object types", definition.Name)
		}
	})
}

// FuzzPredicatePathPositionsAreKnown proves the path registry only publishes
// positions the traversal engine understands.
func FuzzPredicatePathPositionsAreKnown(f *testing.F) {
	for _, predicate := range PredicatePathNames(PredicatePathFirst) {
		f.Add(string(PredicatePathFirst), predicate)
	}
	f.Add(string(PredicatePathSecond), "works_for")
	f.Add("unknown-position", "works_for")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, position, predicate string) {
		names := PredicatePathNames(PredicatePathPosition(position))
		for _, name := range names {
			definition, ok := LookupPredicate(name)
			if !ok {
				t.Fatalf("path position %q published unregistered predicate %q", position, name)
			}
			if !definition.AllowsPathPosition(PredicatePathPosition(position)) {
				t.Fatalf("path position %q published predicate %q that does not allow it", position, name)
			}
		}
	})
}

// FuzzRevisionWindowsRejectAmbiguousIntervals proves temporal validation is
// total and never accepts an empty or inverted window, a non-finite score, or
// context-only evidence as authority.
func FuzzRevisionWindowsRejectAmbiguousIntervals(f *testing.F) {
	f.Add(int64(0), int64(1), "exact", float64(0.5), true)
	f.Add(int64(10), int64(10), "exact", float64(0.5), true)
	f.Add(int64(10), int64(5), "day", float64(0.5), true)
	f.Add(int64(0), int64(0), "unknown", math.NaN(), true)
	f.Add(int64(0), int64(1), "century", float64(1.5), false)

	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

	f.Fuzz(func(t *testing.T, startOffset, endOffset int64, precision string, confidence float64, direct bool) {
		start := base.Add(time.Duration(startOffset%100000) * time.Second)
		end := base.Add(time.Duration(endOffset%100000) * time.Second)
		role := EvidenceRoleContext
		if direct {
			role = EvidenceRoleDirect
		}
		revision := MemoryRevision{
			Kind:             MemoryKindFact,
			CanonicalContent: "canonical content",
			Confidence:       confidence,
			Specificity:      0.5,
			TimePrecision:    TimePrecision(precision),
			EventTimeStart:   &start,
			EventTimeEnd:     &end,
			ValidFrom:        &start,
			ValidUntil:       &end,
			Evidence:         []RevisionEvidence{{EvidenceID: "evidence-1", EventID: "event-1", Role: role}},
		}
		err := ValidateMemoryRevision(revision)
		if err == nil {
			if !end.After(start) {
				t.Fatalf("accepted an inverted or empty window: %v..%v", start, end)
			}
			if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
				t.Fatalf("accepted out-of-range confidence %v", confidence)
			}
			// TimePrecision is optional; only a non-empty value must be valid.
			if precision != "" && !TimePrecision(precision).Valid() {
				t.Fatalf("accepted invalid time precision %q", precision)
			}
			if !direct {
				t.Fatal("accepted context-only evidence as authority")
			}
			return
		}
		if math.IsNaN(confidence) && err == nil {
			t.Fatal("NaN confidence must be rejected")
		}
	})
}

// FuzzEvidenceOrderingIsTotal proves the documented stable evidence order is a
// deterministic total order over arbitrary evidence sets, so revision rendering
// and export never depend on map or slice accident.
func FuzzEvidenceOrderingIsTotal(f *testing.F) {
	f.Add(int64(1), int64(2), int64(0), int64(1))
	f.Add(int64(0), int64(0), int64(0), int64(0))
	f.Add(int64(-1), int64(-1), int64(-1), int64(-1))

	f.Fuzz(func(t *testing.T, aSeq, bSeq, aStart, bStart int64) {
		eventTime := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
		aStartValue, bStartValue := aStart%100, bStart%100
		aEnd, bEnd := aStartValue+1, bStartValue+1
		a := RevisionEvidence{EvidenceID: "a", EventID: "event-a", EventTime: &eventTime, EventSequence: &aSeq, Role: EvidenceRoleDirect, StartOffset: &aStartValue, EndOffset: &aEnd}
		b := RevisionEvidence{EvidenceID: "b", EventID: "event-b", EventTime: &eventTime, EventSequence: &bSeq, Role: EvidenceRoleDirect, StartOffset: &bStartValue, EndOffset: &bEnd}

		ordered := []RevisionEvidence{a, b}
		OrderRevisionEvidence(ordered)
		if len(ordered) != 2 {
			t.Fatalf("ordering lost evidence: %+v", ordered)
		}
		reversed := []RevisionEvidence{b, a}
		OrderRevisionEvidence(reversed)
		if ordered[0].EvidenceID != reversed[0].EvidenceID || ordered[1].EvidenceID != reversed[1].EvidenceID {
			t.Fatalf("ordering is not stable across input order: %+v vs %+v", ordered, reversed)
		}
		// Reordering an already ordered set is a fixed point.
		again := []RevisionEvidence{ordered[0], ordered[1]}
		OrderRevisionEvidence(again)
		if again[0].EvidenceID != ordered[0].EvidenceID || again[1].EvidenceID != ordered[1].EvidenceID {
			t.Fatalf("ordering is not idempotent: %+v vs %+v", ordered, again)
		}
		_ = rand.Int()
	})
}
