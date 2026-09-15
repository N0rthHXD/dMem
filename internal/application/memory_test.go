package application

import (
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestValidateRecallUsesReplacementMemoryKinds(t *testing.T) {
	base := RecallQuery{
		Query: "query", SpaceIDs: []string{"space"}, Intent: domain.RecallIntentAuto,
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 1000, MaxContextTokens: 1000,
	}
	for _, kind := range []string{"fact", "event", "preference", "plan", "procedure", "scene", "topic", "timeline"} {
		query := base
		query.MemoryTypes = []string{kind}
		query.MemoryTypesSet = true
		if err := validateRecall(query); err != nil {
			t.Fatalf("validateRecall rejected replacement kind %q: %v", kind, err)
		}
	}
	for _, obsolete := range []string{"constraint", "decision", "task", "project_context", "agent_experience", "aggregate"} {
		query := base
		query.MemoryTypes = []string{obsolete}
		query.MemoryTypesSet = true
		if err := validateRecall(query); err == nil {
			t.Fatalf("validateRecall accepted obsolete memory_type %q", obsolete)
		}
	}
}
