package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestPredicateRegistryFixtureMatchesDomain(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "predicate-registry-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture []domain.PredicateDefinition
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("fixture is not a registry: %v", err)
	}
	live := domain.PredicateDefinitions()
	if len(fixture) != len(live) {
		t.Fatalf("fixture predicates=%d live=%d", len(fixture), len(live))
	}
	for i := range live {
		if fixture[i].Name != live[i].Name {
			t.Fatalf("fixture predicate %d = %q, live %q", i, fixture[i].Name, live[i].Name)
		}
	}
}

func TestPredicateRegistryFixtureCoversGeneralDomains(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "predicate-registry-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture []domain.PredicateDefinition
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{
		"same_as": false, "member_of": false, "works_for": false, "leads": false,
		"located_at": false, "owned_by": false, "prefers": false, "works_on": false,
		"participates_in": false, "created_by": false, "plan_targets": false,
		"occurred_at": false, "occurred_during": false, "depends_on": false, "derived_from": false,
	}
	for _, definition := range fixture {
		if _, ok := required[definition.Name]; ok {
			required[definition.Name] = true
		}
		if definition.Name == "related_to" {
			t.Fatal("related_to must not appear in the registry fixture")
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("registry fixture missing %q", name)
		}
	}
}

func TestStoragePersistsClosedPredicateRegistry(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	ctx := context.Background()
	for _, name := range domain.PredicateNames() {
		var count int
		if err := r.store.reader.QueryRow(`SELECT count(*) FROM predicate_definitions WHERE predicate=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("predicate_definitions %s count=%d err=%v", name, count, err)
		}
	}
	definition, err := r.predicateDefinition(ctx, r.store.reader, "member_of")
	if err != nil || definition.Inverse != "has_member" || !definition.AllowsPathPosition(domain.PredicatePathFirst) {
		t.Fatalf("loaded member_of = %+v err=%v", definition, err)
	}
	if _, err := r.predicateDefinition(ctx, r.store.reader, "related_to"); err == nil {
		t.Fatal("free-form predicate accepted by storage")
	}
}
