package application

import (
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestValidateRecallUsesDomainMemoryKinds(t *testing.T) {
	base := RecallQuery{
		Query: "query", SpaceIDs: []string{"space"}, Intent: domain.RecallIntentAuto,
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 1000, MaxContextTokens: 1000,
	}
	for _, kind := range []string{"fact", "event", "preference", "plan", "procedure", "scene", "topic", "timeline"} {
		query := base
		query.MemoryTypes = []string{kind}
		query.MemoryTypesSet = true
		if err := validateRecall(query); err != nil {
			t.Fatalf("validateRecall rejected kind %q: %v", kind, err)
		}
	}
	query := base
	query.MemoryTypes = []string{"not-a-kind"}
	query.MemoryTypesSet = true
	if err := validateRecall(query); err == nil {
		t.Fatal("validateRecall accepted an invalid memory kind")
	}
}

func TestValidateRecallDoesNotRequireDebugCapabilities(t *testing.T) {
	query := RecallQuery{
		Query: "query", SpaceIDs: []string{"space"}, Intent: domain.RecallIntentAuto,
		Limit: 10, MaxChars: 1000, MaxContextTokens: 1000,
	}
	if err := validateRecall(query); err != nil {
		t.Fatalf("production recall without debug capabilities rejected: %v", err)
	}
}
