package storage

import (
	"context"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func createMemoryWithEvent(t *testing.T, repository *Repository, key, content, eventID string) domain.Memory {
	t.Helper()
	params := CreateMemoryParams{SpaceID: "space-1", Kind: "fact", Content: content, Sensitivity: "normal", Evidence: directEvidence(eventID), Reason: "projection test"}
	memory, replay, err := repository.CreateMemory(context.Background(), memoryIdentity(key, "/v1/memories", params), params)
	if err != nil || replay {
		t.Fatalf("CreateMemory(%s) = %+v replay=%t err=%v", key, memory, replay, err)
	}
	return memory
}

type qualityRow struct {
	revisionID string
	quality    float64
	generation int64
	algorithm  string
}

func qualityRows(t *testing.T, repository *Repository) []qualityRow {
	t.Helper()
	rows, err := repository.store.reader.Query(`SELECT revision_id, quality, generation, algorithm_version FROM memory_quality ORDER BY revision_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []qualityRow
	for rows.Next() {
		var item qualityRow
		if err := rows.Scan(&item.revisionID, &item.quality, &item.generation, &item.algorithm); err != nil {
			t.Fatal(err)
		}
		result = append(result, item)
	}
	return result
}

func TestQualityProjectionRebuildIsDeterministicAndBounded(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "quality-a", "alice prefers tea")
	createMemoryWithEvent(t, repository, "quality-b", "bob prefers coffee", "event-2")
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_evidence(evidence_id, revision_id, event_id, role, start_offset, end_offset, created_at) SELECT 'derived-evidence', revision_id, 'event-2', 'derived', 1, 5, '2026-08-21T12:00:00Z' FROM memory_revisions WHERE memory_id=(SELECT memory_id FROM memory_projections WHERE display_content='alice prefers tea')`); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildQuality(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := qualityRows(t, repository)
	if len(first) != 2 {
		t.Fatalf("quality rows = %d; want 2", len(first))
	}
	if err := repository.RebuildQuality(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := qualityRows(t, repository)
	if len(first) != len(second) {
		t.Fatalf("quality rows changed: %d -> %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("quality projection not deterministic at %d: %+v vs %+v", i, first[i], second[i])
		}
	}
	health, err := repository.ProjectionHealth(context.Background())
	if err != nil || health.State != "ready" {
		t.Fatalf("projection health = %+v, %v", health, err)
	}
}

func TestQualityNeverChangesEligibility(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "eligibility-quality", "alice prefers tea")
	var before int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM memory_projections WHERE searchable=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildQuality(context.Background()); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM memory_projections WHERE searchable=1`).Scan(&after); err != nil || before != after {
		t.Fatalf("eligibility changed by quality rebuild: %d -> %d, %v", before, after, err)
	}
}

func TestQualityReflectsLifecycleAndDuplicateDensity(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	first := createMemoryWithContent(t, repository, "density-a", "same canonical content")
	second := createMemoryWithEvent(t, repository, "density-b", "same canonical content", "event-2")
	if err := repository.RebuildQuality(context.Background()); err != nil {
		t.Fatal(err)
	}
	var duplicateDensity float64
	if err := repository.store.reader.QueryRow(`SELECT duplicate_density FROM memory_quality WHERE memory_id=?`, first.MemoryID).Scan(&duplicateDensity); err != nil {
		t.Fatal(err)
	}
	if duplicateDensity <= 0 {
		t.Fatalf("duplicate_density = %v; want > 0", duplicateDensity)
	}
	contract := ChangeMemoryStateParams{ExpectedRevisionID: second.CurrentRevision.RevisionID, Action: "contest", Reason: "test"}
	if _, _, _, err := repository.ChangeMemoryState(context.Background(), memoryIdentity("quality-contest", "/v1/memories/{memory_id}/state", contract), second.MemoryID, contract); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildQuality(context.Background()); err != nil {
		t.Fatal(err)
	}
	var activeReview, contestedReview float64
	if err := repository.store.reader.QueryRow(`SELECT (SELECT review_status FROM memory_quality WHERE memory_id=?), (SELECT review_status FROM memory_quality WHERE memory_id=?)`, first.MemoryID, second.MemoryID).Scan(&activeReview, &contestedReview); err != nil {
		t.Fatal(err)
	}
	if contestedReview >= activeReview {
		t.Fatalf("contested review_status %v >= active %v", contestedReview, activeReview)
	}
}

func TestDuplicateClustersCanonicalAndEvidenceRebuildDeterministically(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "cluster-canon-a", "shared canonical fact")
	createMemoryWithEvent(t, repository, "cluster-canon-b", "shared canonical fact", "event-2")
	if _, err := repository.store.writer.Exec(`INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES ('event-3','cluster-op-3','space-1','content-1','manual','test','seed','2026-08-21T11:00:00Z','2026-08-21T11:00:00Z','2026-08-21T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	createMemoryWithEvent(t, repository, "cluster-evidence-a", "first distinct fact", "event-3")
	createMemoryWithEvent(t, repository, "cluster-evidence-b", "second distinct fact", "event-3")
	if err := repository.RebuildDuplicates(context.Background()); err != nil {
		t.Fatal(err)
	}
	var clusters, canonicalMembers, evidenceMembers int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM duplicate_clusters), (SELECT count(*) FROM duplicate_cluster_members WHERE reason_code='equivalent_canonical_tuple'), (SELECT count(*) FROM duplicate_cluster_members WHERE reason_code='shared_primary_evidence')`).Scan(&clusters, &canonicalMembers, &evidenceMembers); err != nil {
		t.Fatal(err)
	}
	if clusters < 2 || canonicalMembers < 2 || evidenceMembers == 0 {
		t.Fatalf("clusters=%d canonical=%d evidence=%d", clusters, canonicalMembers, evidenceMembers)
	}
	var firstClusterIDs string
	if err := repository.store.reader.QueryRow(`SELECT group_concat(cluster_id, ',') FROM (SELECT cluster_id FROM duplicate_clusters ORDER BY cluster_id)`).Scan(&firstClusterIDs); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildDuplicates(context.Background()); err != nil {
		t.Fatal(err)
	}
	var secondClusterIDs string
	if err := repository.store.reader.QueryRow(`SELECT group_concat(cluster_id, ',') FROM (SELECT cluster_id FROM duplicate_clusters ORDER BY cluster_id)`).Scan(&secondClusterIDs); err != nil {
		t.Fatal(err)
	}
	if firstClusterIDs != secondClusterIDs {
		t.Fatalf("duplicate clusters not deterministic: %q vs %q", firstClusterIDs, secondClusterIDs)
	}
}

func TestDuplicateClustersSemanticTimeCompatible(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMemoryWithContent(t, repository, "semantic-car-a", "the car needs maintenance")
	createMemoryWithEvent(t, repository, "semantic-car-b", "car repair rescheduled", "event-2")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildDuplicates(context.Background()); err != nil {
		t.Fatal(err)
	}
	var semanticMembers int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM duplicate_cluster_members WHERE reason_code='semantic_time_compatible'`).Scan(&semanticMembers); err != nil {
		t.Fatal(err)
	}
	if semanticMembers < 2 {
		t.Fatalf("semantic_time_compatible members = %d; want >= 2", semanticMembers)
	}
}
