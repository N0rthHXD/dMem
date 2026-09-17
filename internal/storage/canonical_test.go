package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestCanonicalStructuredDocumentCoversEveryMemoryKind(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	kinds := []domain.MemoryKind{
		domain.MemoryKindFact, domain.MemoryKindEvent, domain.MemoryKindPreference,
		domain.MemoryKindPlan, domain.MemoryKindProcedure, domain.MemoryKindScene,
		domain.MemoryKindTopic, domain.MemoryKindTimeline,
	}
	for _, kind := range kinds {
		content := "canonical body for " + string(kind)
		if domain.ValidAggregateKind(string(kind)) {
			revision := domain.MemoryRevision{Kind: kind, DisplayContent: content, CanonicalContent: content, Sensitivity: "normal"}
			want := canonicalStructuredDocument(revision)
			if !strings.HasPrefix(want, "\nkind:"+string(kind)) {
				t.Fatalf("kind %s canonical document missing kind prefix: %q", kind, want)
			}
			if embedding := embeddingDocument(revision); !strings.Contains(embedding, "kind:"+string(kind)) || !strings.Contains(embedding, content) {
				t.Fatalf("kind %s embedding document = %q", kind, embedding)
			}
			continue
		}
		params := CreateMemoryParams{SpaceID: "space-1", Kind: string(kind), Content: content, Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "canonical"}
		memory, replay, err := repository.CreateMemory(context.Background(), memoryIdentity("canonical-"+string(kind), "/v1/memories", params), params)
		if err != nil || replay {
			t.Fatalf("CreateMemory(%s) = %+v replay=%t err=%v", kind, memory, replay, err)
		}
		var fts string
		if err := repository.store.reader.QueryRow(`SELECT content FROM memory_fts WHERE memory_id=?`, memory.MemoryID).Scan(&fts); err != nil {
			t.Fatal(err)
		}
		want := canonicalStructuredDocument(memory.CurrentRevision)
		if fts != want {
			t.Fatalf("kind %s FTS document mismatch:\n got %q\nwant %q", kind, fts, want)
		}
		if !strings.HasPrefix(want, "\nkind:"+string(kind)) {
			t.Fatalf("kind %s canonical document missing kind prefix: %q", kind, want)
		}
		embedding := embeddingDocument(memory.CurrentRevision)
		if !strings.Contains(embedding, "kind:"+string(kind)) || !strings.Contains(embedding, content) {
			t.Fatalf("kind %s embedding document = %q", kind, embedding)
		}
	}
}

func TestCanonicalStructuredDocumentIsFieldAware(t *testing.T) {
	t.Parallel()
	revision := domain.MemoryRevision{
		Kind: domain.MemoryKindFact, CanonicalContent: "alice role",
		SubjectEntityIDs: []string{"alice"}, ObjectEntityIDs: []string{"engineer"},
		Attributes: map[string]string{"team": "core", "level": "senior"},
		EventType:  "assignment", Language: "en", SourceSpeaker: "alice",
		TimePrecision: domain.TimePrecisionDay,
	}
	document := canonicalStructuredDocument(revision)
	for _, want := range []string{
		"kind:fact", "alice role", "subject:alice", "object:engineer",
		"attribute:level:senior", "attribute:team:core", "event_type:assignment",
		"language:en", "speaker:alice", "time_precision:day",
	} {
		if !strings.Contains(document, want) {
			t.Fatalf("canonical document missing %q: %q", want, document)
		}
	}
	if canonicalStructuredDocument(revision) != canonicalStructuredDocument(revision) {
		t.Fatal("canonical document is not deterministic")
	}
}
