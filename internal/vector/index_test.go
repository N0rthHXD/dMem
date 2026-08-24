package vector

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildSearchScopesStableTiesAndMetadata(t *testing.T) {
	index, err := Open(filepath.Join(t.TempDir(), "vectors"))
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		record("memory-c", "revision-1", "space-b", "hash-c", []float64{1, 0}, 2),
		record("memory-b", "revision-2", "space-a", "hash-b", []float64{1, 0}, 2),
		record("memory-a", "revision-3", "space-a", "hash-a", []float64{0, 1}, 2),
	}
	if err := index.Build(2, Snapshot{Provider: "local", Model: "test-v1", Dimension: 2, Records: records}); err != nil {
		t.Fatal(err)
	}
	if got := index.Status(2); got.State != StateReady || got.Stale || got.RecordCount != 3 || got.Provider != "local" || got.Model != "test-v1" || got.Dimension != 2 {
		t.Fatalf("shadow status = %+v", got)
	}
	if err := index.Activate(2); err != nil {
		t.Fatal(err)
	}
	eligible := identities(records...)
	results, err := index.Search([]float64{1, 0}, []string{"space-a"}, eligible, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Metadata.MemoryID != "memory-b" || results[1].Metadata.MemoryID != "memory-a" {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Score != 1 || results[1].Score != 0 || results[0].Metadata.ContentHash != "hash-b" || results[0].Metadata.IndexVersion != 2 {
		t.Fatalf("result metadata = %+v", results)
	}
	if empty, err := index.Search([]float64{1, 0}, nil, eligible, 10, 2); err != nil || len(empty) != 0 {
		t.Fatalf("empty scope results = %+v, %v", empty, err)
	}
}

func TestStableTieUsesMemoryAndRevisionIDs(t *testing.T) {
	index, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		record("memory-b", "revision-a", "space", "hash-1", []float64{2, 0}, 1),
		record("memory-a", "revision-z", "space", "hash-2", []float64{3, 0}, 1),
		record("memory-a", "revision-a", "space", "hash-3", []float64{4, 0}, 1),
	}
	if err := index.Build(1, Snapshot{Provider: "local", Model: "test-v1", Dimension: 2, Records: records}); err != nil {
		t.Fatal(err)
	}
	results, err := index.Search([]float64{1, 0}, []string{"space"}, identities(records...), 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"memory-a/revision-a", "memory-a/revision-z", "memory-b/revision-a"}
	for n, result := range results {
		if got := result.Metadata.MemoryID + "/" + result.Metadata.RevisionID; got != want[n] {
			t.Fatalf("result %d = %s, want %s", n, got, want[n])
		}
	}
}

func TestSearchFiltersEligibleIdentitiesBeforeTopK(t *testing.T) {
	index, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		record("ineligible-a", "revision", "space", "hash-a", []float64{1, 0}, 1),
		record("ineligible-b", "revision", "space", "hash-b", []float64{0.9, 0.1}, 1),
		record("eligible", "revision", "space", "current-hash", []float64{0.3, 0.7}, 1),
	}
	if err := index.Build(1, Snapshot{Provider: "local", Model: "test-v1", Dimension: 2, Records: records}); err != nil {
		t.Fatal(err)
	}
	eligible := []Identity{{MemoryID: "eligible", RevisionID: "revision", ContentHash: "current-hash"}}
	results, err := index.Search([]float64{1, 0}, []string{"space"}, eligible, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Metadata.MemoryID != "eligible" {
		t.Fatalf("eligible top-k results = %+v", results)
	}

	wrongHash := []Identity{{MemoryID: "eligible", RevisionID: "revision", ContentHash: "stale-hash"}}
	if results, err := index.Search([]float64{1, 0}, []string{"space"}, wrongHash, 1, 1); err != nil || len(results) != 0 {
		t.Fatalf("content hash filtering results = %+v, %v", results, err)
	}
}

func TestUpsertDeleteDropAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vectors")
	index, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	metadata := record("memory-a", "revision-a", "space-a", "hash-a", []float64{1, 0}, 4).Metadata
	if err := index.Upsert(metadata.MemoryID, metadata.RevisionID, []float64{1, 0}, metadata); err != nil {
		t.Fatal(err)
	}
	if err := index.Activate(4); err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(metadata.MemoryID, metadata.RevisionID, []float64{0, 1}, metadata); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.Status(4); got.State != StateReady || got.ActiveVersion != 4 || got.RecordCount != 1 {
		t.Fatalf("restart status = %+v", got)
	}
	eligible := identities(Record{Metadata: metadata})
	results, err := restarted.Search([]float64{0, 1}, []string{"space-a"}, eligible, 1, 4)
	if err != nil || len(results) != 1 || results[0].Score != 1 {
		t.Fatalf("restart search = %+v, %v", results, err)
	}
	if err := restarted.Delete("memory-a", "revision-a"); err != nil {
		t.Fatal(err)
	}
	if results, err = restarted.Search([]float64{0, 1}, []string{"space-a"}, eligible, 1, 4); err != nil || len(results) != 0 {
		t.Fatalf("search after delete = %+v, %v", results, err)
	}
	if err := restarted.Drop(4); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("drop active error = %v", err)
	}
	if err := restarted.Build(5, Snapshot{Provider: "local", Model: "test-v1", Dimension: 2, Records: []Record{}}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Drop(5); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Status(5); got.State != StateAbsent {
		t.Fatalf("dropped status = %+v", got)
	}
}

func TestValidationRejectsInvalidVectorsIdentityAndCorruptFiles(t *testing.T) {
	index, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	valid := record("memory", "revision", "space", "hash", []float64{1, 0}, 1)
	tests := []Record{
		record("memory", "revision", "space", "hash", []float64{1}, 1),
		record("memory", "revision", "space", "hash", []float64{0, 0}, 1),
		record("memory", "revision", "space", "hash", []float64{math.NaN(), 0}, 1),
		record("memory", "revision", "space", "hash", []float64{math.Inf(1), 0}, 1),
	}
	for _, invalid := range tests {
		if err := index.Build(1, Snapshot{Provider: "local", Model: "test-v1", Dimension: 2, Records: []Record{invalid}}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("invalid vector %v error = %v", invalid.Vector, err)
		}
	}
	wrongIdentity := valid
	wrongIdentity.Metadata.Model = "other"
	if err := index.Build(1, Snapshot{Provider: "local", Model: "test-v1", Dimension: 2, Records: []Record{wrongIdentity}}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("identity error = %v", err)
	}
	if err := index.Build(1, Snapshot{Provider: "local", Model: "test-v1", Dimension: 2, Records: []Record{valid}}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Search([]float64{1}, []string{"space"}, identities(valid), 1, 1); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("query dimension error = %v", err)
	}
	if _, err := index.Search([]float64{math.NaN(), 0}, []string{"space"}, identities(valid), 1, 1); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("query finite error = %v", err)
	}
	large := record("large", "revision", "space", "hash", []float64{math.MaxFloat64, math.MaxFloat64}, 2)
	if err := index.Build(2, Snapshot{Provider: "local", Model: "test-v1", Dimension: 2, Records: []Record{large}}); err != nil {
		t.Fatal(err)
	}
	if results, err := index.Search([]float64{math.MaxFloat64, math.MaxFloat64}, []string{"space"}, identities(large), 1, 2); err != nil || len(results) != 1 || math.Abs(results[0].Score-1) > 1e-15 {
		t.Fatalf("large finite vector search = %+v, %v", results, err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index-9.json"), []byte(`{"format_version":1,"index_version":9,"provider":"p","model":"m","dimension":2,"records":[{"memory_id":"m","revision_id":"r","space_id":"s","content_hash":"h","vector":[1]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := corrupt.Status(9); got.State != StateDegraded || got.Err == nil {
		t.Fatalf("corrupt status = %+v", got)
	}
}

func TestShadowActivationIsAtomicAndPermissionsArePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vectors")
	index, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 2; version++ {
		item := record("memory", "revision", "space", "hash", []float64{float64(version), 1}, version)
		if err := index.Build(version, Snapshot{Provider: "local", Model: "test-v1", Dimension: 2, Records: []Record{item}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := index.Activate(1); err != nil {
		t.Fatal(err)
	}
	if got := index.Status(1); got.State != StateReady || got.ActiveVersion != 1 {
		t.Fatalf("version 1 status = %+v", got)
	}
	if got := index.Status(2); got.State != StateReady || got.Stale {
		t.Fatalf("shadow status = %+v", got)
	}
	if err := index.Activate(2); err != nil {
		t.Fatal(err)
	}
	if got := index.Status(2); got.State != StateReady || got.ActiveVersion != 2 {
		t.Fatalf("version 2 status = %+v", got)
	}
	if got := index.Status(1); got.State != StateStale || !got.Stale || got.ActiveVersion != 2 {
		t.Fatalf("retained version status = %+v", got)
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory permissions = %v", info.Mode().Perm())
	}
	for _, name := range []string{"index-1.json", "index-2.json", "active.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions = %v", name, info.Mode().Perm())
		}
	}
}

func record(memoryID, revisionID, spaceID, hash string, vector []float64, version int) Record {
	return Record{
		Metadata: Metadata{MemoryID: memoryID, RevisionID: revisionID, SpaceID: spaceID, ContentHash: hash, Provider: "local", Model: "test-v1", Dimension: 2, IndexVersion: version},
		Vector:   vector,
	}
}

func identities(records ...Record) []Identity {
	result := make([]Identity, len(records))
	for i, record := range records {
		result[i] = Identity{MemoryID: record.Metadata.MemoryID, RevisionID: record.Metadata.RevisionID, ContentHash: record.Metadata.ContentHash}
	}
	return result
}
