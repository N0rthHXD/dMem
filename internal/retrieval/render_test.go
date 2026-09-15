package retrieval

import (
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func renderTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return &parsed
}

func TestRenderContextItemIncludesStructuredFields(t *testing.T) {
	item := domain.ContextItem{
		Rank: 1, MemoryID: "m1", RevisionID: "r1", ClusterID: "cluster-1",
		Kind: domain.MemoryKindEvent, DisplayContent: "Alice visited Berlin",
		EventTimeStart: renderTime("2026-03-01T00:00:00Z"), EventTimeEnd: renderTime("2026-03-02T00:00:00Z"),
		ValidFrom: renderTime("2026-03-01T00:00:00Z"), ValidUntil: renderTime("2026-04-01T00:00:00Z"),
		TimePrecision: domain.TimePrecisionDay, SourceSpeaker: "Alice",
		Confidence: 0.9, LifecycleState: "active", EstimatedTokens: 6,
		Evidence: []domain.RevisionEvidence{
			{EventID: "e1", Role: domain.EvidenceRoleDirect},
			{EventID: "e2", Role: domain.EvidenceRoleTemporalAnchor},
		},
		GraphExplanation:       "entity:Alice --visited--> place:Berlin",
		CorroboratingMemoryIDs: []string{"m2"},
		SupplementaryMemoryIDs: []string{"agg1"},
	}
	text := RenderContextItem(item)
	for _, want := range []string{
		"kind=event", "confidence=0.900", "lifecycle=active", "tokens=6",
		"cluster-1", "Alice visited Berlin", "event_time: 2026-03-01T00:00:00Z..2026-03-02T00:00:00Z (day)",
		"valid_time: 2026-03-01T00:00:00Z..2026-04-01T00:00:00Z",
		"speaker: Alice", "evidence: e1 (direct), e2 (temporal_anchor)",
		"graph: entity:Alice --visited--> place:Berlin",
		"corroborating: m2", "supplementary: agg1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered context missing %q:\n%s", want, text)
		}
	}
}

func TestRenderContextItemOmitsUnavailableDateAndSpeaker(t *testing.T) {
	item := domain.ContextItem{
		Rank: 2, MemoryID: "m3", RevisionID: "r3", Kind: domain.MemoryKindFact,
		DisplayContent: "Zephyr launch checklist", Confidence: 0.5,
		LifecycleState: "active", EstimatedTokens: 4,
	}
	text := RenderContextItem(item)
	if strings.Contains(text, "event_time") || strings.Contains(text, "valid_time") || strings.Contains(text, "speaker:") {
		t.Fatalf("renderer claimed unavailable time or speaker:\n%s", text)
	}
	if !strings.Contains(text, "Zephyr launch checklist") || !strings.Contains(text, "kind=fact") {
		t.Fatalf("renderer lost required content:\n%s", text)
	}
}

func TestRenderContextPackageDeterministic(t *testing.T) {
	pkg := domain.ContextPackage{Items: []domain.ContextItem{
		{Rank: 1, MemoryID: "m1", RevisionID: "r1", Kind: domain.MemoryKindFact, DisplayContent: "one", LifecycleState: "active"},
		{Rank: 2, MemoryID: "m2", RevisionID: "r2", Kind: domain.MemoryKindFact, DisplayContent: "two", LifecycleState: "active"},
	}}
	first, second := RenderContextPackage(pkg), RenderContextPackage(pkg)
	if first != second {
		t.Fatal("render is not deterministic")
	}
	if !strings.Contains(first, "one") || !strings.Contains(first, "two") || !strings.Contains(first, "\n\n") {
		t.Fatalf("package render = %q", first)
	}
}

func TestRenderContextItemDoesNotClaimReconstructedConversation(t *testing.T) {
	text := RenderContextItem(domain.ContextItem{
		Rank: 1, MemoryID: "m1", RevisionID: "r1", Kind: domain.MemoryKindScene,
		DisplayContent: "Session summary", LifecycleState: "active",
	})
	lower := strings.ToLower(text)
	for _, forbidden := range []string{"reconstructed", "original conversation", "conversation date"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("renderer claimed reconstruction %q:\n%s", forbidden, text)
		}
	}
}
