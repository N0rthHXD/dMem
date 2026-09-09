package storage

import (
	"context"
	"testing"
)

func TestRRFV3FormulaIsExactThreeChannel(t *testing.T) {
	tests := []struct {
		name                      string
		keywordRank, semanticRank int
		graphRank                 int
		want                      float64
	}{
		{"no hits", 0, 0, 0, 0},
		{"keyword only", 1, 0, 0, 1 / float64(61)},
		{"semantic only", 0, 3, 0, 1 / float64(63)},
		{"graph only", 0, 0, 5, 1 / float64(65)},
		{"keyword+semantic", 1, 1, 0, 2 / float64(61)},
		{"all three", 2, 1, 1, 1/float64(62) + 2/float64(61)},
	}
	for _, test := range tests {
		got := rrfScore(test.keywordRank, test.semanticRank, test.graphRank)
		if diff := got - test.want; diff > 1e-15 || diff < -1e-15 {
			t.Fatalf("%s: rrfScore=%v want %v", test.name, got, test.want)
		}
	}
	if rankingVersion != "rrf-v3" {
		t.Fatalf("rankingVersion=%q, want rrf-v3", rankingVersion)
	}
}

// TestThreeChannelFusionChannelsAndGraphOnlyRecall verifies the three top-level
// channels stay independent: entity facts and memory relations only surface as
// graph signals, and a graph-only candidate contributes exactly one RRF term.
func TestThreeChannelFusionChannelsAndGraphOnlyRecall(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	dual := createMemoryWithContent(t, repository, "fusion-dual", "automobile zephoria identifier")
	semanticOnly := createMemoryWithContent(t, repository, "fusion-semantic", "automobile handbook")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Created after the rebuild so they carry no embedding.
	keywordOnly := createMemoryWithContent(t, repository, "fusion-keyword", "zephoria identifier exact")
	graphMemory := createMemoryWithContent(t, repository, "fusion-graph", "board notes")
	seedEntity(t, repository, "entity-zephoria", "Zephoria", "person")
	seedMention(t, repository, "mention-zephoria", "entity-zephoria", graphMemory.MemoryID, graphMemory.CurrentRevision.RevisionID, 0, 8)

	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "car zephoria", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "semantic", "graph"}, Limit: 10, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if result.RankingVersion != "rrf-v3" {
		t.Fatalf("ranking version = %q", result.RankingVersion)
	}
	byID := searchResultsByID(result)
	if len(byID) != 4 {
		t.Fatalf("union IDs = %v", resultIDs(result))
	}
	assertChannels(t, byID[keywordOnly.MemoryID].MatchedChannels, "keyword")
	assertChannels(t, byID[semanticOnly.MemoryID].MatchedChannels, "semantic")
	assertChannels(t, byID[dual.MemoryID].MatchedChannels, "keyword", "semantic")
	assertChannels(t, byID[graphMemory.MemoryID].MatchedChannels, "graph")
	if byID[dual.MemoryID].SelectionReason != "channel_fusion" {
		t.Fatalf("dual selection reason = %q", byID[dual.MemoryID].SelectionReason)
	}
	graphResult := byID[graphMemory.MemoryID]
	if graphResult.KeywordRelevance != nil || graphResult.SemanticScore != nil {
		t.Fatalf("graph-only result leaked channel scores: %+v", graphResult)
	}
	if len(graphResult.GraphSignals) != 1 || graphResult.GraphSignals[0].Kind != "direct_entity_mention" {
		t.Fatalf("graph-only signals = %+v", graphResult.GraphSignals)
	}
	for _, item := range result.Results {
		for _, channel := range item.MatchedChannels {
			if channel != "keyword" && channel != "semantic" && channel != "graph" {
				t.Fatalf("unexpected top-level channel %q: %+v", channel, item)
			}
		}
	}
}

// TestGraphSeedsExcludeExpandedCandidates pins the strict seed contract:
// a memory relation expansion result must not become a seed for further
// expansion, so relation targets appear exactly once through the graph rank.
func TestGraphSeedsExcludeExpandedCandidates(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	source := createTestMemory(t, repository, "seed-source")
	target := createTestMemory(t, repository, "seed-target")
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_relations(relation_id, space_id, relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, state, source, confidence, specification_hash, created_by, processing_version, created_at, updated_at) VALUES ('seed-rel', 'space-1', 'supports', ?, ?, ?, ?, 'active', 'human_confirmation', 0.9, 'hash', 'test', 'v1', '2026-08-21T11:00:00Z', '2026-08-21T11:00:00Z')`, source.MemoryID, source.CurrentRevision.RevisionID, target.MemoryID, target.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "initial", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "graph"}, Limit: 10, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	byID := searchResultsByID(result)
	graphSignals := len(byID[target.MemoryID].GraphSignals)
	if graphSignals != 1 {
		t.Fatalf("relation target graph signals = %d, want exactly one expansion", graphSignals)
	}
	if !contains(byID[target.MemoryID].MatchedChannels, "graph") {
		t.Fatalf("relation target channels = %+v", byID[target.MemoryID].MatchedChannels)
	}
}
