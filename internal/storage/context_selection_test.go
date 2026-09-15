package storage

import (
	"context"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func selectionSearchParams(query string, explain bool) SearchParams {
	return SearchParams{
		Query: query, SpaceIDs: []string{"space-1"}, States: []string{"active"},
		Channels: []string{"keyword"}, Limit: 10, MaxChars: 10000, MaxContextTokens: 10000, Explain: explain,
	}
}

func TestContextSelectionCollapsesDuplicateCandidates(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	first := createMemoryWithContent(t, repository, "sel-first", "zephyr duplicate concept")
	second := createMemoryWithContent(t, repository, "sel-second", "zephyr duplicate concept")
	response, err := repository.SearchMemories(context.Background(), selectionSearchParams("zephyr duplicate concept", false))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 {
		t.Fatalf("raw ranked candidates = %d, want 2", len(response.Results))
	}
	if len(response.Context.Items) != 1 {
		t.Fatalf("selected context = %+v, want one representative", response.Context.Items)
	}
	item := response.Context.Items[0]
	representative, corroborated := first, second
	if item.MemoryID == second.MemoryID {
		representative, corroborated = second, first
	} else if item.MemoryID != first.MemoryID {
		t.Fatalf("unexpected representative %s", item.MemoryID)
	}
	_ = representative
	if len(item.CorroboratingMemoryIDs) != 1 || item.CorroboratingMemoryIDs[0] != corroborated.MemoryID {
		t.Fatalf("corroborating = %v, want [%s]", item.CorroboratingMemoryIDs, corroborated.MemoryID)
	}
	if len(response.Context.Rejected) != 0 {
		t.Fatalf("default context exposed rejections: %+v", response.Context.Rejected)
	}
	if response.ContextSelectionIdentity == "" || response.Context.Items[0].ClusterID == "" {
		t.Fatalf("selection identity/cluster missing: %q %+v", response.ContextSelectionIdentity, response.Context.Items[0])
	}
}

func TestContextSelectionExplainExposesRejectionsOnly(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "sel-explain-first", "zephyr explain concept")
	createMemoryWithContent(t, repository, "sel-explain-second", "zephyr explain concept")
	response, err := repository.SearchMemories(context.Background(), selectionSearchParams("zephyr explain concept", true))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Context.Items) != 1 || len(response.Context.Rejected) != 1 {
		t.Fatalf("explain selection items=%d rejected=%d", len(response.Context.Items), len(response.Context.Rejected))
	}
	if response.Context.Rejected[0].Reason != "duplicate_cluster" {
		t.Fatalf("rejection reason = %q", response.Context.Rejected[0].Reason)
	}
	if response.Context.Items[0].Explanation == nil || response.Context.Items[0].Explanation.SelectionReason == "" {
		t.Fatalf("explain context missing explanation: %+v", response.Context.Items[0])
	}
}

func TestContextSelectionBudgetIsHardCap(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "sel-budget-a", "budget concept alpha")
	createMemoryWithContent(t, repository, "sel-budget-b", "budget concept beta")
	params := selectionSearchParams("budget concept", false)
	params.MaxContextTokens = 12
	params.MaxChars = 12
	response, err := repository.SearchMemories(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if response.Context.Accounting.TokensUsed > 12 {
		t.Fatalf("token budget exceeded: %+v", response.Context.Accounting)
	}
	if len(response.Context.Items) > 1 {
		t.Fatalf("result budget not enforced: %+v", response.Context.Items)
	}
}

func TestContextSelectionProtectsExactPrimaryOverAggregate(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	source := createMemoryWithContent(t, repository, "sel-source", "zephyr protected source")
	aggregate := createMemoryWithContentInKind(t, repository, "sel-aggregate", "zephyr protected source summary", "scene")
	if aggregate.Kind != domain.MemoryKindScene {
		t.Fatalf("aggregate kind = %s", aggregate.Kind)
	}
	response, err := repository.SearchMemories(context.Background(), selectionSearchParams("zephyr protected source", true))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Context.Items) != 1 {
		t.Fatalf("selected = %+v", response.Context.Items)
	}
	if response.Context.Items[0].MemoryID != source.MemoryID {
		t.Fatalf("broad aggregate consumed the primary slot: %+v", response.Context.Items[0])
	}
	if len(response.Context.Items[0].SupplementaryMemoryIDs) == 0 || response.Context.Items[0].SupplementaryMemoryIDs[0] != aggregate.MemoryID {
		t.Fatalf("aggregate synthesis not attached as supplementary: %+v", response.Context.Items[0])
	}
}

func TestContextSelectionExcludesStaleAggregateByDefault(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	input := createTestMemory(t, repository, "stale-input")
	proposal := createAggregateProposalTest(t, repository, "stale-proposal", aggregateProposalParams(aggregateInput(input)))
	confirmed := confirmAggregateTest(t, repository, "stale-confirm", proposal)
	revise := ReviseMemoryParams{ExpectedRevisionID: input.CurrentRevision.RevisionID, Content: "changed input", Evidence: directEvidence("event-2"), Reason: "changed"}
	if _, _, err := repository.ReviseMemory(context.Background(), memoryIdentity("stale-revise", "/v1/memories/{memory_id}", revise), input.MemoryID, revise); err != nil {
		t.Fatal(err)
	}
	if aggregate, err := repository.GetAggregate(context.Background(), *confirmed.ResultMemoryID); err != nil || !aggregate.AggregateStale {
		t.Fatalf("aggregate not stale: %+v err=%v", aggregate, err)
	}

	excludeParams := selectionSearchParams("aggregate output", false)
	excludeParams.ExcludeStaleAggregates = true
	excluded, err := repository.SearchMemories(context.Background(), excludeParams)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range excluded.Context.Items {
		if item.MemoryID == *confirmed.ResultMemoryID {
			t.Fatalf("stale aggregate appeared in default context: %+v", item)
		}
	}
	params := selectionSearchParams("aggregate output", true)
	params.ExcludeStaleAggregates = false
	provenance, err := repository.SearchMemories(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range provenance.Context.Items {
		if item.MemoryID != *confirmed.ResultMemoryID {
			continue
		}
		found = true
		if !item.Stale || len(item.StaleReasons) == 0 || len(item.ProvenanceMemoryIDs) == 0 {
			t.Fatalf("debug stale aggregate missing reason/provenance: %+v", item)
		}
	}
	if !found {
		t.Fatalf("debug inclusion did not return stale aggregate: %+v", provenance.Context.Items)
	}
}

func createMemoryWithContentInKind(t *testing.T, repository *Repository, key, content, kind string) domain.Memory {
	t.Helper()
	params := CreateMemoryParams{SpaceID: "space-1", Kind: kind, Content: content, Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "context selection test"}
	memory, replay, err := repository.CreateMemory(context.Background(), memoryIdentity(key, "/v1/memories", params), params)
	if err != nil || replay {
		t.Fatalf("create %s memory = %+v, replay=%t, %v", kind, memory, replay, err)
	}
	return memory
}
