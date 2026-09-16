package domain

import (
	"sort"
	"testing"
)

func TestPredicateRegistryIsClosedAndTyped(t *testing.T) {
	if err := ValidatePredicateRegistry(); err != nil {
		t.Fatalf("registry invalid: %v", err)
	}
	if len(PredicateNames()) < 20 {
		t.Fatalf("registry has %d predicates, want a general registry", len(PredicateNames()))
	}
	for _, definition := range PredicateDefinitions() {
		if !definition.Direction.Valid() {
			t.Fatalf("%s: invalid direction %q", definition.Name, definition.Direction)
		}
		if !definition.Cardinality.Valid() {
			t.Fatalf("%s: invalid cardinality %q", definition.Name, definition.Cardinality)
		}
		if !definition.TemporalPolicy.Valid() {
			t.Fatalf("%s: invalid temporal policy %q", definition.Name, definition.TemporalPolicy)
		}
		if !definition.Transitivity.Valid() {
			t.Fatalf("%s: invalid transitivity %q", definition.Name, definition.Transitivity)
		}
		if !definition.AutoConfirm.Valid() {
			t.Fatalf("%s: invalid auto-confirm %q", definition.Name, definition.AutoConfirm)
		}
		if !definition.Literal() && len(definition.ObjectTypes) == 0 {
			t.Fatalf("%s: entity predicate without object types", definition.Name)
		}
		if definition.Literal() && (definition.LiteralType != "string" && definition.LiteralType != "number" && definition.LiteralType != "boolean") {
			t.Fatalf("%s: literal predicate with invalid literal type %q", definition.Name, definition.LiteralType)
		}
		if !definition.AllowsPathPosition(PredicatePathFirst) {
			t.Fatalf("%s: not traversable as first hop", definition.Name)
		}
		if definition.AutoConfirm == PredicateAutoConfirmThreshold && definition.MinConfidence <= 0 {
			t.Fatalf("%s: threshold auto-confirm without a confidence", definition.Name)
		}
	}
}

func TestPredicateRegistryInverseIsDirectionalAndSymmetric(t *testing.T) {
	for _, name := range []string{"member_of", "works_for", "reports_to", "owned_by", "participates_in", "created_by", "depends_on"} {
		definition, ok := LookupPredicate(name)
		if !ok {
			t.Fatalf("%s: not registered", name)
		}
		if definition.Inverse == "" {
			t.Fatalf("%s: missing inverse", name)
		}
		inverse, ok := LookupPredicate(definition.Inverse)
		if !ok {
			t.Fatalf("%s: inverse %q not registered", name, definition.Inverse)
		}
		if inverse.Inverse != name {
			t.Fatalf("%s: inverse %q does not point back", name, definition.Inverse)
		}
	}
	if definition, ok := LookupPredicate("same_as"); !ok || definition.Direction != PredicateSymmetric || definition.Inverse != "" {
		t.Fatalf("same_as = %+v", definition)
	}
	if _, ok := LookupPredicate("hates"); ok {
		t.Fatal("free-form predicate was accepted by the registry")
	}
}

func TestPredicateRegistryCoversGeneralDomains(t *testing.T) {
	required := map[string]bool{
		"same_as": false, "member_of": false, "works_for": false, "leads": false,
		"located_at": false, "owned_by": false, "prefers": false, "works_on": false,
		"participates_in": false, "created_by": false, "plan_targets": false,
		"occurred_at": false, "occurred_during": false, "depends_on": false, "derived_from": false,
	}
	for _, name := range PredicateNames() {
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("registry missing general predicate %q", name)
		}
	}
}

func TestPredicateRegistryLiteralPredicates(t *testing.T) {
	for _, name := range []string{"has_status", "is_active", "has_participant_count", "occurred_during"} {
		definition, ok := LookupPredicate(name)
		if !ok || !definition.Literal() {
			t.Fatalf("%s should be a literal predicate: %+v", name, definition)
		}
	}
	for _, name := range []string{"member_of", "participates_in", "located_at"} {
		definition, _ := LookupPredicate(name)
		if definition.Literal() {
			t.Fatalf("%s should be an entity predicate", name)
		}
	}
}

func TestPredicateRegistryIdentityIsDeterministic(t *testing.T) {
	first := PredicateRegistryIdentity()
	second := PredicateRegistryIdentity()
	if first == "" || first != second {
		t.Fatalf("identity not deterministic: %q vs %q", first, second)
	}
	names := PredicateNames()
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	for i := range names {
		if names[i] != sorted[i] {
			t.Fatalf("predicate names are not sorted: %v", names)
		}
	}
	if len(names) != len(PredicateDefinitions()) {
		t.Fatalf("names/definitions mismatch: %d vs %d", len(names), len(PredicateDefinitions()))
	}
}

func TestPredicateTypeCompatibility(t *testing.T) {
	definition, _ := LookupPredicate("member_of")
	if !containsType(definition.SubjectTypes, "person") || !containsType(definition.ObjectTypes, "organization") {
		t.Fatalf("member_of types = %v -> %v", definition.SubjectTypes, definition.ObjectTypes)
	}
	status, _ := LookupPredicate("has_status")
	if status.LiteralType != "string" {
		t.Fatalf("has_status literal type = %q", status.LiteralType)
	}
}

func containsType(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
