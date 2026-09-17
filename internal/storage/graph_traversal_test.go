package storage

import (
	"context"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

// seedGraphFact inserts one active entity fact with direct evidence. Optional
// valid-time bounds exercise the pre-expansion temporal filter.
func seedGraphFact(t *testing.T, r *Repository, factID, subjectID, predicate, objectID string, memory domain.Memory, confidence float64, validFrom, validUntil *string) {
	t.Helper()
	var from, until any
	if validFrom != nil {
		from = *validFrom
	}
	if validUntil != nil {
		until = *validUntil
	}
	if _, err := r.store.writer.Exec(`INSERT INTO entity_facts(fact_id, space_id, subject_entity_id, predicate, object_entity_id, source_memory_id, source_revision_id, confidence, state, source, specification_hash, created_by, processing_version, valid_from, valid_until, created_at, updated_at) VALUES (?, 'space-1', ?, ?, ?, ?, ?, ?, 'active', 'auto_review', ?, 'test', 'v1', ?, ?, '2026-08-21T11:00:00Z', '2026-08-21T11:00:00Z')`, factID, subjectID, predicate, objectID, memory.MemoryID, memory.CurrentRevision.RevisionID, confidence, "hash-"+factID, from, until); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.writer.Exec(`INSERT INTO entity_fact_evidence(evidence_id,fact_id,evidence_type,created_by,created_at) VALUES (?,?,'human_confirmation','test','2026-08-21T11:00:00Z')`, factID+"-evidence", factID); err != nil {
		t.Fatal(err)
	}
}

func seedGraphSpace(t *testing.T, r *Repository, spaceID string) {
	t.Helper()
	if _, err := r.store.writer.Exec(`INSERT OR IGNORE INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES (?, 'space-1', 'project', ?, ?, '2026-08-21T11:00:00Z')`, spaceID, spaceID, spaceID); err != nil {
		t.Fatal(err)
	}
}

func traversalPolicy(policy retrieval.GraphPolicy, predicates []string) graphRecallPolicy {
	return graphRecallPolicy{Policy: policy, AllowNewCandidates: policy == retrieval.GraphDirect || policy == retrieval.GraphBoundedPath, Predicates: predicates, Primary: map[string]struct{}{}}
}

func graphItemFor(items []graphRecallItem, memoryID string) *graphRecallItem {
	for i := range items {
		if items[i].memoryID == memoryID {
			return &items[i]
		}
	}
	return nil
}

func runGraphRecall(t *testing.T, r *Repository, spaces []string, policy graphRecallPolicy, resolved []domain.ResolvedEntity, params SearchParams) graphChannelResult {
	t.Helper()
	tx, err := r.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := r.graphChannelRecall(context.Background(), tx, params, spaces, policy, resolved, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestGraphTraversalIncomingAndOutgoingOneHop(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	seedEntity(t, r, "entity-acme", "Acme", "organization")
	outgoing := createTestMemory(t, r, "traversal-outgoing")
	incoming := createTestMemory(t, r, "traversal-incoming")
	seedGraphFact(t, r, "fact-outgoing", "entity-alice", "participates_in", "entity-alpha", outgoing, 0.9, nil, nil)
	seedGraphFact(t, r, "fact-incoming", "entity-acme", "employs", "entity-alice", incoming, 0.9, nil, nil)

	result := runGraphRecall(t, r, []string{"space-1"}, traversalPolicy(retrieval.GraphDirect, nil),
		[]domain.ResolvedEntity{{EntityID: "entity-alice", EntityType: "person", Alias: "Alice", Confidence: 0.9, MatchKind: "alias"}},
		SearchParams{SpaceIDs: []string{"space-1"}, States: []string{"active"}, ReferenceTime: "2026-08-21T12:00:00Z"})

	out := graphItemFor(result.items, outgoing.MemoryID)
	if out == nil || len(out.signals) == 0 || out.signals[0].EdgeDirection != "outgoing" {
		t.Fatalf("outgoing one-hop signals = %+v", out)
	}
	in := graphItemFor(result.items, incoming.MemoryID)
	if in == nil || len(in.signals) == 0 || in.signals[0].EdgeDirection != "incoming" {
		t.Fatalf("incoming one-hop signals = %+v", in)
	}
}

func TestGraphTraversalHeterogeneousTwoHopPath(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	seedEntity(t, r, "entity-acme", "Acme", "organization")
	first := createTestMemory(t, r, "traversal-two-hop-first")
	second := createTestMemory(t, r, "traversal-two-hop-second")
	seedGraphFact(t, r, "fact-works-on", "entity-alice", "works_on", "entity-alpha", first, 0.9, nil, nil)
	seedGraphFact(t, r, "fact-owned-by", "entity-alpha", "owned_by", "entity-acme", second, 0.9, nil, nil)

	result := runGraphRecall(t, r, []string{"space-1"}, traversalPolicy(retrieval.GraphBoundedPath, []string{"works_on", "owned_by"}),
		[]domain.ResolvedEntity{{EntityID: "entity-alice", EntityType: "person", Alias: "Alice", Confidence: 0.9, MatchKind: "alias"}},
		SearchParams{SpaceIDs: []string{"space-1"}, States: []string{"active"}, ReferenceTime: "2026-08-21T12:00:00Z"})

	item := graphItemFor(result.items, second.MemoryID)
	if item == nil || len(item.paths) != 1 {
		t.Fatalf("heterogeneous two-hop paths = %+v", item)
	}
	path := item.paths[0]
	if path.Hop != 2 || path.StartEntityID != "entity-alice" || path.EndEntityID != "entity-acme" || len(path.Edges) != 2 {
		t.Fatalf("path shape = %+v", path)
	}
	if path.Edges[0].Predicate != "works_on" || path.Edges[1].Predicate != "owned_by" {
		t.Fatalf("path predicates are not heterogeneous: %+v", path.Edges)
	}
	if path.Edges[0].FactID == "" || path.Edges[1].FactID == "" || path.Edges[1].SourceMemoryID != second.MemoryID || path.Edges[1].SourceRevisionID != second.CurrentRevision.RevisionID {
		t.Fatalf("path edge evidence incomplete: %+v", path.Edges)
	}
	found := false
	for _, signal := range item.signals {
		if signal.Kind == "two_hop_path" {
			found = true
		}
	}
	if !found {
		t.Fatalf("two_hop_path signal missing: %+v", item.signals)
	}
}

func TestGraphTraversalRejectsPredicateWithoutSecondPathPosition(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	seedEntity(t, r, "entity-base", "Base", "project")
	first := createTestMemory(t, r, "traversal-pos-first")
	second := createTestMemory(t, r, "traversal-pos-second")
	seedGraphFact(t, r, "fact-pos-works", "entity-alice", "works_on", "entity-alpha", first, 0.9, nil, nil)
	seedGraphFact(t, r, "fact-pos-derived", "entity-alpha", "derived_from", "entity-base", second, 0.9, nil, nil)

	result := runGraphRecall(t, r, []string{"space-1"}, traversalPolicy(retrieval.GraphBoundedPath, []string{"works_on", "derived_from"}),
		[]domain.ResolvedEntity{{EntityID: "entity-alice", EntityType: "person", Alias: "Alice", Confidence: 0.9, MatchKind: "alias"}},
		SearchParams{SpaceIDs: []string{"space-1"}, States: []string{"active"}, ReferenceTime: "2026-08-21T12:00:00Z"})

	item := graphItemFor(result.items, second.MemoryID)
	if item != nil {
		for _, signal := range item.signals {
			if signal.Kind == "two_hop_path" {
				t.Fatalf("first-only provenance predicate composed a path: %+v", signal)
			}
		}
	}
}

func TestGraphTraversalMaximumTwoHopsAndCycleProtection(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	for _, id := range []string{"entity-a", "entity-b", "entity-c", "entity-d"} {
		seedEntity(t, r, id, id, "project")
	}
	ab := createTestMemory(t, r, "traversal-chain-ab")
	bc := createTestMemory(t, r, "traversal-chain-bc")
	cd := createTestMemory(t, r, "traversal-chain-cd")
	ba := createTestMemory(t, r, "traversal-chain-ba")
	seedGraphFact(t, r, "fact-ab", "entity-a", "depends_on", "entity-b", ab, 0.9, nil, nil)
	seedGraphFact(t, r, "fact-bc", "entity-b", "depends_on", "entity-c", bc, 0.9, nil, nil)
	seedGraphFact(t, r, "fact-cd", "entity-c", "depends_on", "entity-d", cd, 0.9, nil, nil)
	seedGraphFact(t, r, "fact-ba", "entity-b", "depends_on", "entity-a", ba, 0.9, nil, nil)

	result := runGraphRecall(t, r, []string{"space-1"}, traversalPolicy(retrieval.GraphBoundedPath, []string{"depends_on"}),
		[]domain.ResolvedEntity{{EntityID: "entity-a", EntityType: "project", Alias: "A", Confidence: 0.9, MatchKind: "alias"}},
		SearchParams{SpaceIDs: []string{"space-1"}, States: []string{"active"}, ReferenceTime: "2026-08-21T12:00:00Z"})

	if item := graphItemFor(result.items, cd.MemoryID); item != nil {
		t.Fatalf("three-hop target was expanded: %+v", item)
	}
	item := graphItemFor(result.items, bc.MemoryID)
	if item == nil || len(item.paths) == 0 {
		t.Fatalf("expected at least one bounded two-hop path to C: %+v", item)
	}
	for _, path := range item.paths {
		if path.Hop != 2 || path.EndEntityID != "entity-c" {
			t.Fatalf("unexpected path at two hops: %+v", path)
		}
		if path.EndEntityID == "entity-a" {
			t.Fatalf("cycle returned to a start entity: %+v", path)
		}
	}
}

func TestGraphTraversalFanOutIsBounded(t *testing.T) {
	requirePerformanceGates(t)
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-a", "A", "project")
	seedEntity(t, r, "entity-hub", "Hub", "project")
	first := createTestMemory(t, r, "traversal-fanout-first")
	seedGraphFact(t, r, "fact-a-hub", "entity-a", "depends_on", "entity-hub", first, 0.9, nil, nil)
	targets := graphFanOut + 10
	for i := 0; i < targets; i++ {
		id := "entity-target-" + strconvItoa2(i)
		seedEntity(t, r, id, "Target"+strconvItoa2(i), "project")
		memory := createTestMemory(t, r, "traversal-fanout-"+strconvItoa2(i))
		seedGraphFact(t, r, "fact-hub-"+strconvItoa2(i), "entity-hub", "depends_on", id, memory, 0.9, nil, nil)
	}

	result := runGraphRecall(t, r, []string{"space-1"}, traversalPolicy(retrieval.GraphBoundedPath, []string{"depends_on"}),
		[]domain.ResolvedEntity{{EntityID: "entity-a", EntityType: "project", Alias: "A", Confidence: 0.9, MatchKind: "alias"}},
		SearchParams{SpaceIDs: []string{"space-1"}, States: []string{"active"}, ReferenceTime: "2026-08-21T12:00:00Z"})

	ends := map[string]struct{}{}
	for _, item := range result.items {
		for _, path := range item.paths {
			ends[path.EndEntityID] = struct{}{}
		}
	}
	if len(ends) == 0 || len(ends) > graphFanOut {
		t.Fatalf("fan-out produced %d distinct path ends, want 1..%d", len(ends), graphFanOut)
	}
}

func TestGraphTraversalAppliesScopeStateAndValidTimeBeforeExpansion(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedGraphSpace(t, r, "space-2")
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	seedEntity(t, r, "entity-beta", "Beta", "project")
	inScope := createTestMemory(t, r, "traversal-valid")
	expired := createTestMemory(t, r, "traversal-expired")
	seedGraphFact(t, r, "fact-in-scope", "entity-alice", "works_on", "entity-alpha", inScope, 0.9, nil, nil)
	from, until := "2020-01-01T00:00:00Z", "2020-02-01T00:00:00Z"
	seedGraphFact(t, r, "fact-expired", "entity-alice", "works_on", "entity-beta", expired, 0.9, &from, &until)

	// The source memory lives in space-2, so the fact is out of the requested
	// scope even though the fact row itself references space-1.
	scopeParams := CreateMemoryParams{SpaceID: "space-2", Kind: "fact", Content: "out of scope fact", Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "scope"}
	scopeMemory, _, err := r.CreateMemory(context.Background(), memoryIdentity("traversal-scope-memory", "/v1/memories", scopeParams), scopeParams)
	if err != nil {
		t.Fatal(err)
	}
	seedGraphFact(t, r, "fact-scope", "entity-alice", "works_on", "entity-alpha", scopeMemory, 0.9, nil, nil)

	result := runGraphRecall(t, r, []string{"space-1"}, traversalPolicy(retrieval.GraphDirect, nil),
		[]domain.ResolvedEntity{{EntityID: "entity-alice", EntityType: "person", Alias: "Alice", Confidence: 0.9, MatchKind: "alias"}},
		SearchParams{SpaceIDs: []string{"space-1"}, States: []string{"active"}, ReferenceTime: "2026-08-21T12:00:00Z"})

	if item := graphItemFor(result.items, inScope.MemoryID); item == nil {
		t.Fatalf("in-scope fact was not recalled: %+v", result.items)
	}
	if item := graphItemFor(result.items, expired.MemoryID); item != nil {
		t.Fatalf("expired fact crossed the valid-time filter: %+v", item)
	}
	if item := graphItemFor(result.items, scopeMemory.MemoryID); item != nil {
		t.Fatalf("out-of-scope fact crossed the scope filter: %+v", item)
	}
}

func TestGraphTraversalOrderIsStable(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	seedEntity(t, r, "entity-beta", "Beta", "project")
	high := createTestMemory(t, r, "traversal-order-high")
	low := createTestMemory(t, r, "traversal-order-low")
	seedGraphFact(t, r, "fact-order-high", "entity-alice", "works_on", "entity-alpha", high, 0.95, nil, nil)
	seedGraphFact(t, r, "fact-order-low", "entity-alice", "works_on", "entity-beta", low, 0.55, nil, nil)

	resolved := []domain.ResolvedEntity{{EntityID: "entity-alice", EntityType: "person", Alias: "Alice", Confidence: 0.9, MatchKind: "alias"}}
	params := SearchParams{SpaceIDs: []string{"space-1"}, States: []string{"active"}, ReferenceTime: "2026-08-21T12:00:00Z"}
	first := runGraphRecall(t, r, []string{"space-1"}, traversalPolicy(retrieval.GraphDirect, nil), resolved, params)
	second := runGraphRecall(t, r, []string{"space-1"}, traversalPolicy(retrieval.GraphDirect, nil), resolved, params)
	if len(first.items) != len(second.items) {
		t.Fatalf("unstable item count: %d vs %d", len(first.items), len(second.items))
	}
	for i := range first.items {
		if first.items[i].memoryID != second.items[i].memoryID || first.items[i].score != second.items[i].score {
			t.Fatalf("unstable order at %d: %+v vs %+v", i, first.items[i], second.items[i])
		}
	}
	if len(first.items) != 2 || first.items[0].memoryID != high.MemoryID {
		t.Fatalf("expected high-confidence fact first: %+v", first.items)
	}
}
