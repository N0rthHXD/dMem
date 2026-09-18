package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

func TestRankingIdentityIsVersionedFeatureRanker(t *testing.T) {
	if rankingVersion != retrieval.RankingWeightsVersion {
		t.Fatalf("ranking version = %q", rankingVersion)
	}
}

// TestThreeChannelFusionChannelsAndGraphOnlyRecall verifies the three top-level
// channels stay independent and that a direct-policy graph candidate can enter
// with a graph signal family while carrying no primary channel score.
func TestThreeChannelFusionChannelsAndGraphOnlyRecall(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	enableGraphCandidates(repository)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	dual := createMemoryWithContent(t, repository, "fusion-dual", "automobile zephoria identifier")
	semanticOnly := createMemoryWithContent(t, repository, "fusion-semantic", "automobile handbook")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	keywordOnly := createMemoryWithContent(t, repository, "fusion-keyword", "zephoria identifier exact")
	graphMemory := createMemoryWithContent(t, repository, "fusion-graph", "board notes")
	seedEntity(t, repository, "entity-zephoria", "Zephoria", "person")
	seedMention(t, repository, "mention-zephoria", "entity-zephoria", graphMemory.MemoryID, graphMemory.CurrentRevision.RevisionID, 0, 8)
	if err := repository.RebuildGraphIndex(context.Background()); err != nil {
		t.Fatal(err)
	}

	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "car zephoria", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "semantic", "graph"}, GraphPolicy: retrieval.GraphDirect, Limit: 10, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if result.RankingVersion != retrieval.RankingWeightsVersion {
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
	graphResult := byID[graphMemory.MemoryID]
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
// relation-only expansion never establishes a candidate in features_only.
func TestGraphSeedsExcludeExpandedCandidates(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	source := createMemoryWithContent(t, repository, "seed-source", "seedword source content")
	target := createMemoryWithContent(t, repository, "seed-target", "unrelated target body")
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_relations(relation_id, space_id, relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, state, source, confidence, specification_hash, created_by, processing_version, created_at, updated_at) VALUES ('seed-rel', 'space-1', 'supports', ?, ?, ?, ?, 'active', 'human_confirmation', 0.9, 'hash', 'test', 'v1', '2026-08-21T11:00:00Z', '2026-08-21T11:00:00Z')`, source.MemoryID, source.CurrentRevision.RevisionID, target.MemoryID, target.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "seedword", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "graph"}, Limit: 10, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := searchResultsByID(result)[target.MemoryID]; present {
		t.Fatalf("relation-only candidate entered results: %v", resultIDs(result))
	}
}

func TestWeakGraphRankCannotEqualStrongGraphRank(t *testing.T) {
	strong := graphAggregateScore([]domain.GraphSignal{{Kind: "direct_entity_mention", EntityID: "e1", EntityConfidence: 0.95}}, nil)
	weak := graphAggregateScore([]domain.GraphSignal{{Kind: "direct_entity_mention", EntityID: "e1", EntityConfidence: 0.5}}, nil)
	if strong <= weak {
		t.Fatalf("strong graph score %v <= weak graph score %v", strong, weak)
	}
}

func TestPrimarySeedsRespectTopKAndDeduplication(t *testing.T) {
	var candidates []gwretrieval.Candidate
	for i := 0; i < 20; i++ {
		candidates = append(candidates, gwretrieval.Candidate{
			MemoryID: fmt.Sprintf("sem-%02d", i), RevisionID: fmt.Sprintf("rev-sem-%02d", i),
			SemanticRank: i + 1, SemanticRaw: 0.9 - float64(i)*0.01,
		})
	}
	for i := 0; i < 20; i++ {
		candidates = append(candidates, gwretrieval.Candidate{
			MemoryID: fmt.Sprintf("kw-%02d", i), RevisionID: fmt.Sprintf("rev-kw-%02d", i),
			KeywordRank: i + 1, KeywordRaw: -1 - float64(i)*0.01,
		})
	}
	processor := gwretrieval.NewService(gwretrieval.Ports{}, gwretrieval.Defaults{})
	prepared := processor.PreparePrimary(gwretrieval.PrimaryInput{Candidates: candidates, Calibration: retrieval.DefaultCalibration(), Weights: retrieval.DefaultWeights(), SemanticTop: 10, KeywordTop: 10, MaxSeeds: 15})
	seeds, primary := prepared.Seeds, prepared.PrimaryKeys
	if len(seeds) != 15 {
		t.Fatalf("seed count = %d, want 15", len(seeds))
	}
	if len(primary) != len(seeds) {
		t.Fatalf("primary set size %d != seed count %d", len(primary), len(seeds))
	}
	seen := map[string]struct{}{}
	for _, seed := range seeds {
		if _, duplicate := seen[seed]; duplicate {
			t.Fatalf("duplicate seed %q", seed)
		}
		seen[seed] = struct{}{}
	}
}

func TestPrimarySeedsExcludeRejectedSemanticCandidates(t *testing.T) {
	candidates := []gwretrieval.Candidate{
		{MemoryID: "good", RevisionID: "rev-good", SemanticRank: 1, SemanticRaw: 0.8},
		{MemoryID: "rejected", RevisionID: "rev-rejected", SemanticRank: 2, SemanticRaw: 0.2},
	}
	processor := gwretrieval.NewService(gwretrieval.Ports{}, gwretrieval.Defaults{})
	seeds := processor.PreparePrimary(gwretrieval.PrimaryInput{Candidates: candidates, Calibration: retrieval.DefaultCalibration(), Weights: retrieval.DefaultWeights(), SemanticTop: 10, KeywordTop: 10, MaxSeeds: 15}).Seeds
	for _, seed := range seeds {
		if seed == "rev-rejected" {
			t.Fatal("rejected semantic candidate became a seed")
		}
	}
}

func TestPrimarySeedsExcludeCandidatesBelowCalibratedFloors(t *testing.T) {
	weights := retrieval.DefaultWeights()
	calibration := retrieval.DefaultCalibration()
	calibration.AbsoluteFloor = 0
	processor := gwretrieval.NewService(gwretrieval.Ports{}, gwretrieval.Defaults{})
	seeds := processor.PreparePrimary(gwretrieval.PrimaryInput{
		Candidates:  []gwretrieval.Candidate{{MemoryID: "weak-semantic", RevisionID: "r1", SemanticRank: 1, SemanticRaw: weights.SemanticSeedFloor - 0.01}},
		Calibration: calibration, Weights: weights, SemanticTop: 10, KeywordTop: 10, MaxSeeds: 15,
	}).Seeds
	if len(seeds) != 0 {
		t.Fatalf("weak calibrated candidates became graph seeds: %v", seeds)
	}
}

func TestRelationOnlySignalClassification(t *testing.T) {
	if !graphRelationOnly([]domain.GraphSignal{{Kind: "memory_relation"}}) {
		t.Fatal("memory_relation should be relation-only")
	}
	if graphRelationOnly([]domain.GraphSignal{{Kind: "memory_relation"}, {Kind: "direct_entity_mention"}}) {
		t.Fatal("mention-backed candidate is not relation-only")
	}
	if graphRelationOnly([]domain.GraphSignal{{Kind: "exact_predicate_fact"}}) {
		t.Fatal("fact-backed candidate is not relation-only")
	}
}

func TestRankingProjectionFeaturesAreHydrated(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	memory := createMemoryWithContent(t, repository, "ranking-projections", "projectionfeature")
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_quality(memory_id,revision_id,evidence_strength,extraction_confidence,specificity,temporal_precision,source_diversity,review_status,contradiction_penalty,duplicate_density,quality,algorithm_version,updated_at) VALUES (?,?,1,1,1,1,1,1,0,0.6,0.8,'quality-v1','2026-08-21T12:00:00Z')`, memory.MemoryID, memory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "projectionfeature", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Explain: true, Limit: 5, MaxChars: 1000})
	if err != nil || len(result.Results) != 1 {
		t.Fatalf("search = %+v, %v", result, err)
	}
	features := result.Results[0].RankingFeatures
	if features[retrieval.FeatureQuality] != 0.8 || features[retrieval.FeatureDuplicateRisk] != 0.6 {
		t.Fatalf("projection features = %+v", features)
	}
}

func TestGraphBoostCannotDemoteProtectedPrimaryExact(t *testing.T) {
	weights := retrieval.DefaultWeights()
	processor := gwretrieval.NewService(gwretrieval.Ports{}, gwretrieval.Defaults{})
	list := processor.Rank(gwretrieval.RankInput{Candidates: []gwretrieval.Candidate{
		{MemoryID: "graph", RevisionID: "r-graph", GraphScore: 1},
		{MemoryID: "exact", RevisionID: "r-exact", SemanticRank: 1, SemanticScore: 0.9, FTSContent: "exact"},
	}, Weights: weights, Terms: retrieval.CompileKeyword("exact").Terms}).Candidates
	if list[0].MemoryID != "exact" {
		t.Fatalf("graph-only candidate displaced protected primary exact match: %+v", list)
	}
	if list[1].RankOutput.GraphBoost != 0 {
		t.Fatalf("graph-only candidate received graph boost: %+v", list[1].RankOutput)
	}
}

func TestGraphOnlyBelowAdmissionFloorIsRemoved(t *testing.T) {
	weights := retrieval.DefaultWeights()
	processor := gwretrieval.NewService(gwretrieval.Ports{}, gwretrieval.Defaults{})
	got := processor.Rank(gwretrieval.RankInput{Candidates: []gwretrieval.Candidate{{MemoryID: "weak-graph", RevisionID: "r-weak-graph", GraphScore: weights.GraphOnlyAdmissionFloor - 0.01}}, Weights: weights}).Candidates
	if len(got) != 0 {
		t.Fatalf("below-floor graph-only candidate admitted: %+v", got)
	}
}

func TestProtectedPrimaryDoesNotOverrideStrongerPrimary(t *testing.T) {
	processor := gwretrieval.NewService(gwretrieval.Ports{}, gwretrieval.Defaults{})
	list := processor.Rank(gwretrieval.RankInput{Candidates: []gwretrieval.Candidate{
		{MemoryID: "protected", RevisionID: "r-protected", SemanticRank: 1, SemanticScore: 0.6, FTSContent: "exact"},
		{MemoryID: "stronger", RevisionID: "r-stronger", SemanticRank: 2, SemanticScore: 1},
	}, Weights: retrieval.DefaultWeights(), Terms: retrieval.CompileKeyword("exact").Terms}).Candidates
	if list[0].MemoryID != "stronger" {
		t.Fatalf("protected rule displaced stronger primary: %+v", list)
	}
}

// TestRankedFinalizerChannelRulesAreConsistentAcrossPaths pins the unified
// finalizer contract: matched channels and selection reason derive only from the
// candidate's channel fields, so the same corpus yields the same rule table on
// the keyword-only, semantic-only, and hybrid paths.
func TestRankedFinalizerChannelRulesAreConsistentAcrossPaths(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	memory := createMemoryWithContent(t, repository, "finalizer-both", "automobile finalizetoken")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}

	keyword, err := repository.SearchMemories(context.Background(), SearchParams{Query: "finalizetoken", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 5, MaxChars: 1000})
	if err != nil || len(keyword.Results) != 1 || keyword.Results[0].Memory.MemoryID != memory.MemoryID {
		t.Fatalf("keyword path = %+v, %v", keyword, err)
	}
	keywordResult := keyword.Results[0]
	assertChannels(t, keywordResult.MatchedChannels, "keyword")
	if keywordResult.SelectionReason != "keyword_match" || keywordResult.KeywordRelevance == nil || keywordResult.SemanticScore != nil || keywordResult.RankContribution.Final <= 0 {
		t.Fatalf("keyword finalize rules = %+v", keywordResult)
	}

	semantic, err := repository.SearchMemories(context.Background(), SearchParams{Query: "car", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"semantic"}, Limit: 5, MaxChars: 1000})
	if err != nil || len(semantic.Results) != 1 || semantic.Results[0].Memory.MemoryID != memory.MemoryID {
		t.Fatalf("semantic path = %+v, %v", semantic, err)
	}
	semanticResult := semantic.Results[0]
	assertChannels(t, semanticResult.MatchedChannels, "semantic")
	if semanticResult.SelectionReason != "semantic_match" || semanticResult.SemanticScore == nil || semanticResult.KeywordRelevance != nil || semanticResult.RankContribution.Final <= 0 {
		t.Fatalf("semantic finalize rules = %+v", semanticResult)
	}

	hybrid, err := repository.SearchMemories(context.Background(), SearchParams{Query: "car finalizetoken", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "semantic"}, Limit: 5, MaxChars: 1000})
	if err != nil || len(hybrid.Results) != 1 || hybrid.Results[0].Memory.MemoryID != memory.MemoryID {
		t.Fatalf("hybrid path = %+v, %v", hybrid, err)
	}
	hybridResult := hybrid.Results[0]
	assertChannels(t, hybridResult.MatchedChannels, "keyword", "semantic")
	if hybridResult.SelectionReason != "channel_fusion" || hybridResult.KeywordRelevance == nil || hybridResult.SemanticScore == nil || hybridResult.RankContribution.Final <= 0 {
		t.Fatalf("hybrid finalize rules = %+v", hybridResult)
	}
}

func TestExplainSelectionReasonsRemainStable(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMemoryWithContent(t, repository, "reason-keyword", "reasonword unique")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	keywordResult, err := repository.SearchMemories(context.Background(), SearchParams{Query: "reasonword", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Explain: true, Limit: 5, MaxChars: 1000})
	if err != nil || len(keywordResult.Results) == 0 || keywordResult.Results[0].SelectionReason != "keyword_match" {
		t.Fatalf("keyword reason = %+v, %v", keywordResult, err)
	}
	if len(keywordResult.Results[0].RankingFeatures) == 0 {
		t.Fatal("explain features missing")
	}
	explain := keywordResult.Results[0]
	if explain.KeywordRawBM25 == nil || len(explain.KeywordMatchedFields) != 1 || explain.KeywordTermCoverage == nil || explain.RankContribution.Final <= 0 {
		t.Fatalf("keyword explain provenance missing: %+v", explain)
	}
}

func TestGraphScoreFamiliesSurviveExplainAndTelemetry(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	enableGraphCandidates(repository)
	memory := createMemoryWithContent(t, repository, "graph-telemetry", "graphtelemetry body")
	seedEntity(t, repository, "entity-graphtelemetry", "Graphtelemetry", "person")
	seedMention(t, repository, "mention-graphtelemetry", "entity-graphtelemetry", memory.MemoryID, memory.CurrentRevision.RevisionID, 0, 14)
	if err := repository.RebuildGraphIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := repository.SearchMemories(context.Background(), SearchParams{OperationID: "graph-telemetry-op", Query: "graphtelemetry", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "graph"}, GraphPolicy: retrieval.GraphDirect, Explain: true, Limit: 5, MaxChars: 1000})
	if err != nil || len(result.Results) == 0 {
		t.Fatalf("graph telemetry search = %+v, %v", result, err)
	}
	item := result.Results[0]
	if item.GraphScore == nil || *item.GraphScore <= 0 || *item.GraphScore > 1 {
		t.Fatalf("normalized graph score not preserved: %+v", item.GraphScore)
	}
	if len(item.GraphSignals) == 0 || item.GraphSignals[0].Kind != "direct_entity_mention" {
		t.Fatalf("graph signal family not preserved: %+v", item.GraphSignals)
	}
	var graphJSON string
	if err := repository.store.reader.QueryRow(`SELECT graph_counts_json FROM query_telemetry WHERE operation_id='graph-telemetry-op'`).Scan(&graphJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(graphJSON, "families") || !strings.Contains(graphJSON, "direct_entity_mention") || !strings.Contains(graphJSON, "score") {
		t.Fatalf("telemetry graph payload = %s", graphJSON)
	}
}

func TestRecallPersistsBoundedQueryTelemetry(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMemoryWithContent(t, repository, "telemetry-memory", "telemetryword exact")
	result, err := repository.SearchMemories(context.Background(), SearchParams{OperationID: "telemetry-operation", Query: "telemetryword", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Explain: true, Limit: 5, MaxChars: 1000})
	if err != nil || len(result.Results) != 1 {
		t.Fatalf("search = %+v, %v", result, err)
	}
	var telemetryCount, decisionCount int
	var rankingIdentity, contextIdentity string
	if err := repository.store.reader.QueryRow(`SELECT count(*),ranking_identity,context_identity FROM query_telemetry WHERE operation_id='telemetry-operation'`).Scan(&telemetryCount, &rankingIdentity, &contextIdentity); err != nil {
		t.Fatal(err)
	}
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM context_decisions WHERE operation_id='telemetry-operation'`).Scan(&decisionCount); err != nil {
		t.Fatal(err)
	}
	if telemetryCount != 1 || decisionCount != 1 || rankingIdentity != result.RankingIdentity || contextIdentity != result.ContextIdentity {
		t.Fatalf("telemetry=%d decisions=%d identities=%q/%q result=%q/%q", telemetryCount, decisionCount, rankingIdentity, contextIdentity, result.RankingIdentity, result.ContextIdentity)
	}
}
