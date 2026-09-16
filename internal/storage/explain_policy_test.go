package storage

import (
	"context"
	"testing"
)

// TestExplainEnforcesScopeAndEvidencePolicy proves explain mode obeys the same
// scope and evidence policy as normal recall: denied spaces never appear in
// results, selected context, or rejected candidates, and hidden evidence stays
// hidden unless the caller requested it.
func TestExplainEnforcesScopeAndEvidencePolicy(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createTestSpace(t, repository, "space-2")
	visible := createMemoryWithContent(t, repository, "explain-visible", "scopeword visible")
	hidden := createMemoryInSpace(t, repository, "explain-hidden", "space-2", "scopeword hidden")

	result, err := repository.SearchMemories(context.Background(), SearchParams{
		OperationID: "explain-scope", Query: "scopeword", SpaceIDs: []string{"space-1"},
		IncludeAncestors: false, States: []string{"active"}, Channels: []string{"keyword"},
		Explain: true, IncludeEvidence: false, Limit: 5, MaxChars: 1000, MaxContextTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := searchResultsByID(result)[hidden.MemoryID]; present {
		t.Fatal("explain returned a memory from a denied space")
	}
	if len(result.Context.Items) == 0 || result.Context.Items[0].MemoryID != visible.MemoryID {
		t.Fatalf("selected context = %+v", result.Context.Items)
	}
	for _, item := range result.Context.Items {
		if len(item.Evidence) != 0 {
			t.Fatalf("explain exposed hidden evidence without include_evidence: %+v", item.Evidence)
		}
		if item.Explanation == nil {
			t.Fatal("explain metadata missing")
		}
	}
	for _, rejected := range result.Context.Rejected {
		if rejected.MemoryID == hidden.MemoryID {
			t.Fatal("explain rejected list leaked a denied-space memory")
		}
	}
	if result.Context.Items[0].Explanation != nil && result.Context.Items[0].Explanation.SelectionReason == "" {
		t.Fatal("explain selection reason missing")
	}
	explanation := result.Context.Items[0].Explanation
	if len(explanation.FeatureContributions) == 0 {
		t.Fatal("explain feature contributions missing")
	}
	if explanation.ContextSelection.Utility != result.Context.Items[0].Utility || !explanation.ContextSelection.Representative {
		t.Fatalf("context selection explanation = %+v", explanation.ContextSelection)
	}

	withEvidence, err := repository.SearchMemories(context.Background(), SearchParams{
		OperationID: "explain-evidence", Query: "scopeword", SpaceIDs: []string{"space-1"},
		IncludeAncestors: false, States: []string{"active"}, Channels: []string{"keyword"},
		Explain: true, IncludeEvidence: true, Limit: 5, MaxChars: 1000, MaxContextTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(withEvidence.Context.Items) != 1 || len(withEvidence.Context.Items[0].Evidence) == 0 {
		t.Fatalf("requested evidence not attached: %+v", withEvidence.Context.Items)
	}
}

// TestExplainSensitivityBudgetDoesNotLeakRejectedContent proves a candidate
// rejected by the sensitivity budget appears only as a bounded id/reason record
// and never as display content or evidence through explain output.
func TestExplainSensitivityBudgetDoesNotLeakRejectedContent(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createSensitiveMemory(t, repository, "explain-sens-a", "space-1", "secretword alpha", "sensitive")
	createSensitiveMemory(t, repository, "explain-sens-b", "space-1", "secretword beta", "sensitive")

	result, err := repository.SearchMemories(context.Background(), SearchParams{
		OperationID: "explain-sensitivity", Query: "secretword", SpaceIDs: []string{"space-1"},
		States: []string{"active"}, Channels: []string{"keyword"}, Explain: true,
		IncludeEvidence: true, Limit: 5, MaxChars: 1000, MaxContextTokens: 1000, MaxSensitive: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Context.Items) > 1 {
		t.Fatalf("sensitivity budget admitted %d items", len(result.Context.Items))
	}
	if len(result.Context.Items)+len(result.Context.Rejected) < 2 {
		t.Fatalf("expected one rejected sensitive candidate, items=%d rejected=%d", len(result.Context.Items), len(result.Context.Rejected))
	}
	for _, rejected := range result.Context.Rejected {
		if rejected.MemoryID == "" || rejected.RevisionID == "" || rejected.Reason == "" {
			t.Fatalf("rejected decision missing bounded reason: %+v", rejected)
		}
	}
}

func createTestSpace(t *testing.T, repository *Repository, spaceID string) {
	t.Helper()
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES (?, 'space-1', 'project', ?, ?, '2026-08-21T11:00:00Z')`, spaceID, spaceID, spaceID); err != nil {
		t.Fatal(err)
	}
}

func createSensitiveMemory(t *testing.T, repository *Repository, key, spaceID, content, sensitivity string) {
	t.Helper()
	params := CreateMemoryParams{SpaceID: spaceID, Kind: "fact", Content: content, Sensitivity: sensitivity, Evidence: directEvidence("event-1"), Reason: "explain policy test"}
	if _, _, err := repository.CreateMemory(context.Background(), memoryIdentity(key, "/v1/memories", params), params); err != nil {
		t.Fatal(err)
	}
}
