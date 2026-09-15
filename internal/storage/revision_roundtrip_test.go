package storage

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestStructuredRevisionEvidenceAndProjectionRoundTrip(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	ctx := context.Background()
	tx, err := repository.store.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	now := "2026-08-21T12:00:00.123Z"
	eventStart := time.Date(2026, 8, 20, 9, 30, 0, 0, time.UTC)
	eventEnd := eventStart.Add(45 * time.Minute)
	validFrom := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	validUntil := validFrom.AddDate(0, 1, 0)
	start, end := int64(1), int64(5)
	revision := domain.MemoryRevision{
		RevisionID:        "structured-revision",
		MemoryID:          "structured-memory",
		Kind:              domain.MemoryKindEvent,
		DisplayContent:    "Alice visited Kyoto",
		CanonicalContent:  "alice visited kyoto",
		CanonicalHash:     "structured-canonical-hash",
		SubjectEntityIDs:  []string{"entity-alice", "entity-team"},
		ObjectEntityIDs:   []string{"entity-kyoto"},
		EventType:         "visit",
		Attributes:        map[string]string{"purpose": "conference", "transport": "train"},
		EventTimeStart:    &eventStart,
		EventTimeEnd:      &eventEnd,
		TimePrecision:     domain.TimePrecisionExact,
		TimeSource:        "speaker_statement",
		ValidFrom:         &validFrom,
		ValidUntil:        &validUntil,
		SourceSpeaker:     "alice",
		Language:          "en",
		Sensitivity:       "sensitive",
		Specificity:       0.8,
		Confidence:        0.9,
		CreatedBy:         "storage-test",
		ProcessingVersion: "test-v1",
		ProcessorIdentity: "processor/test",
		CreatedAt:         now,
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,frozen,sensitivity,created_at,updated_at) VALUES (?,?,?,'active',0,?,?,?)`, revision.MemoryID, "space-1", revision.RevisionID, revision.Sensitivity, now, now); err != nil {
		t.Fatal(err)
	}
	if err = insertRevision(ctx, tx, revision); err != nil {
		t.Fatal(err)
	}
	evidence := []EvidenceParams{
		{EventID: "event-2", Role: domain.EvidenceRoleContext},
		{EventID: "event-1", Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end},
	}
	if err = insertEvidence(ctx, tx, revision.RevisionID, evidence, []string{"evidence-context", "evidence-direct"}, now); err != nil {
		t.Fatal(err)
	}
	if err = repository.upsertMemoryProjection(ctx, tx, revision.MemoryID, revision.RevisionID, "space-1", "active", now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, err := repository.GetMemory(ctx, revision.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	evidenceTime := time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC)
	revision.Evidence = []domain.RevisionEvidence{
		{EvidenceID: "evidence-direct", EventID: "event-1", EventTime: &evidenceTime, Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end},
		{EvidenceID: "evidence-context", EventID: "event-2", EventTime: &evidenceTime, Role: domain.EvidenceRoleContext},
	}
	if !reflect.DeepEqual(got.CurrentRevision, revision) {
		t.Fatalf("revision round trip mismatch:\n got: %#v\nwant: %#v", got.CurrentRevision, revision)
	}
	if got.Kind != revision.Kind {
		t.Fatalf("memory kind = %q; want %q", got.Kind, revision.Kind)
	}
	var kind, display, canonicalHash string
	if err = repository.store.reader.QueryRow(`SELECT memory_type,display_content,content_hash FROM memory_projections WHERE memory_id=?`, revision.MemoryID).Scan(&kind, &display, &canonicalHash); err != nil {
		t.Fatal(err)
	}
	if kind != string(revision.Kind) || display != revision.DisplayContent || canonicalHash != revision.CanonicalHash {
		t.Fatalf("projection = kind %q display %q hash %q", kind, display, canonicalHash)
	}
}
