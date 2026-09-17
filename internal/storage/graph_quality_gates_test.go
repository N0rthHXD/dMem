package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

type graphQualityEntity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type graphQualityDocument struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Space   string `json:"space"`
}

type graphQualityFact struct {
	ID         string  `json:"id"`
	Subject    string  `json:"subject"`
	Predicate  string  `json:"predicate"`
	Object     string  `json:"object"`
	Memory     string  `json:"memory"`
	Confidence float64 `json:"confidence"`
	ValidFrom  *string `json:"valid_from"`
	ValidUntil *string `json:"valid_until"`
}

type graphQualityMention struct {
	ID         string  `json:"id"`
	Entity     string  `json:"entity"`
	Memory     string  `json:"memory"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Confidence float64 `json:"confidence"`
}

type graphQualityRelation struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"`
	Source     string  `json:"source"`
	Target     string  `json:"target"`
	Confidence float64 `json:"confidence"`
}

type graphQualityEvent struct {
	EventID      string   `json:"event_id"`
	Participants []string `json:"participants"`
	Place        string   `json:"place"`
	Start        string   `json:"start"`
	End          string   `json:"end"`
}

type graphPromotionCase struct {
	ID         string   `json:"id"`
	Query      string   `json:"query"`
	Entity     string   `json:"entity"`
	Policy     string   `json:"policy"`
	Predicates []string `json:"predicates"`
	Expected   []string `json:"expected"`
}

type graphQualityCorpus struct {
	Version        string                 `json:"version"`
	Space          string                 `json:"space"`
	SiblingSpace   string                 `json:"sibling_space"`
	Entities       []graphQualityEntity   `json:"entities"`
	Documents      []graphQualityDocument `json:"documents"`
	Facts          []graphQualityFact     `json:"facts"`
	Mentions       []graphQualityMention  `json:"mentions"`
	Relations      []graphQualityRelation `json:"relations"`
	Events         []graphQualityEvent    `json:"events"`
	Categories     []string               `json:"categories"`
	PromotionCases []graphPromotionCase   `json:"promotion_cases"`
}

type graphQualityGates struct {
	ConfigVersion string `json:"config_version"`
	CorpusVersion string `json:"corpus_version"`
	Quality       struct {
		MinimumPromotionPrecision float64 `json:"minimum_promotion_precision"`
		MaximumRankHarm           float64 `json:"maximum_rank_harm"`
		MaximumOneHopEdges        int     `json:"maximum_one_hop_edges"`
		MaximumPaths              int     `json:"maximum_paths"`
	} `json:"quality"`
	Performance struct {
		TargetFactCount            int     `json:"target_fact_count"`
		QuerySamples               int     `json:"query_samples"`
		MaximumGraphQueryP95MS     float64 `json:"maximum_graph_query_p95_ms"`
		MaximumGraphQueryHeapBytes uint64  `json:"maximum_graph_query_heap_bytes"`
		MaximumGraphStorageGrowth  float64 `json:"maximum_graph_storage_growth"`
	} `json:"performance"`
	Ablations struct {
		MentionOnly    string  `json:"mention_only"`
		FactOnly       string  `json:"fact_only"`
		RelationOnly   string  `json:"relation_only"`
		OneHop         string  `json:"one_hop"`
		TwoHop         string  `json:"two_hop"`
		ConfidenceGate float64 `json:"confidence_gate"`
		SeedBudget     int     `json:"seed_budget"`
		BoostCap       float64 `json:"boost_cap"`
	} `json:"ablations"`
}

func loadGraphQuality(t *testing.T) (graphQualityCorpus, graphQualityGates) {
	t.Helper()
	var corpus graphQualityCorpus
	var gates graphQualityGates
	readJSONFixture(t, "testdata/graph-quality-corpus-v1.json", &corpus)
	readJSONFixture(t, "testdata/graph-quality-gates-v1.json", &gates)
	if corpus.Version != gates.CorpusVersion || gates.ConfigVersion != "graph-quality-gates-v1" {
		t.Fatalf("graph quality identity mismatch: corpus=%q gates=%q", corpus.Version, gates.ConfigVersion)
	}
	return corpus, gates
}

type graphQualityFixture struct {
	repository *Repository
	corpus     graphQualityCorpus
	docIDs     map[string]string
}

func buildGraphQualityFixture(t *testing.T, hubFanout int) graphQualityFixture {
	t.Helper()
	corpus, _ := loadGraphQuality(t)
	repository, _ := newMemoryTestRepository(t)
	execSQL := func(label, query string, args ...any) {
		t.Helper()
		if _, err := repository.store.writer.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	seedGraphSpace(t, repository, corpus.SiblingSpace)
	for _, entity := range corpus.Entities {
		seedEntity(t, repository, entity.ID, entity.Name, entity.Type)
	}
	docIDs := map[string]string{}
	for _, document := range corpus.Documents {
		space := corpus.Space
		if document.Space == "sibling" {
			space = corpus.SiblingSpace
		} else if document.Space != "" {
			space = document.Space
		}
		params := CreateMemoryParams{SpaceID: space, Kind: "fact", Content: document.Content, Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "graph quality corpus"}
		memory, _, err := repository.CreateMemory(context.Background(), memoryIdentity("graph-quality-"+document.ID, "/v1/memories", params), params)
		if err != nil {
			t.Fatalf("create graph quality document %s: %v", document.ID, err)
		}
		docIDs[document.ID] = memory.MemoryID
	}
	// The stale document's current revision is advanced so its mention no longer
	// points at an eligible current revision.
	if staleID, ok := docIDs["doc-stale"]; ok {
		staleRevision := "graph-quality-stale-current"
		execSQL("stale revision override", `INSERT INTO memory_revisions(revision_id,memory_id,parent_revision_id,kind,display_content,canonical_content,canonical_hash,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES (?,?,NULL,'fact','stale advanced','stale advanced','graph-stale-hash','normal',0,0,'test','v1','2026-08-21T13:00:00Z'); UPDATE memories SET current_revision_id=?,updated_at='2026-08-21T13:00:00Z' WHERE memory_id=?; UPDATE memory_projections SET revision_id=?,display_content='stale advanced',content_hash='graph-stale-hash',updated_at='2026-08-21T13:00:00Z' WHERE memory_id=?`, staleRevision, staleID, staleRevision, staleID, staleRevision, staleID)
	}
	for _, fact := range corpus.Facts {
		memoryID, ok := docIDs[fact.Memory]
		if !ok {
			t.Fatalf("fact %s references unknown memory %s", fact.ID, fact.Memory)
		}
		revisionID := currentRevisionID(t, repository, memoryID)
		seedGraphFact(t, repository, fact.ID, fact.Subject, fact.Predicate, fact.Object, domain.Memory{MemoryID: memoryID, CurrentRevision: domain.MemoryRevision{RevisionID: revisionID}}, fact.Confidence, fact.ValidFrom, fact.ValidUntil)
	}
	for _, mention := range corpus.Mentions {
		execSQL("mention "+mention.ID, `INSERT INTO entity_mentions(mention_id, entity_id, memory_id, revision_id, start_offset, end_offset, mention_type, source, surface_text, confidence, created_by, processing_version, created_at) VALUES (?,?,?,?,?,?,'explicit','human_confirmation','surface',?, 'test','v1','2026-08-21T11:00:00Z')`, mention.ID, mention.Entity, docIDs[mention.Memory], currentRevisionID(t, repository, docIDs[mention.Memory]), mention.Start, mention.End, mention.Confidence)
	}
	for _, relation := range corpus.Relations {
		execSQL("relation "+relation.ID, `INSERT INTO memory_relations(relation_id, space_id, relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, state, source, confidence, specification_hash, created_by, processing_version, created_at, updated_at) VALUES (?,?,?,?,?,?,?,'active','human_confirmation',?,?, 'test','v1','2026-08-21T11:00:00Z','2026-08-21T11:00:00Z')`, relation.ID, corpus.Space, relation.Type, docIDs[relation.Source], currentRevisionID(t, repository, docIDs[relation.Source]), docIDs[relation.Target], currentRevisionID(t, repository, docIDs[relation.Target]), relation.Confidence, "rel-hash-"+relation.ID)
	}
	for _, event := range corpus.Events {
		for _, participant := range event.Participants {
			execSQL("event participant "+participant, `INSERT INTO event_participants(event_id, entity_id, participant_role, confidence, created_at) VALUES (?,?, 'participant', 0.9, '2026-08-21T11:00:00Z') ON CONFLICT DO NOTHING`, event.EventID, participant)
		}
		if event.Place != "" {
			execSQL("event place "+event.EventID, `INSERT INTO event_temporal_bindings(binding_id, event_id, binding_kind, place_entity_id, interval_start, interval_end, precision, confidence, created_at) VALUES (?,?,'occurred_at',?,NULL,NULL,'exact',0.9,'2026-08-21T11:00:00Z')`, "binding-place-"+event.EventID, event.EventID, event.Place)
		}
		if event.Start != "" {
			execSQL("event time "+event.EventID, `INSERT INTO event_temporal_bindings(binding_id, event_id, binding_kind, place_entity_id, interval_start, interval_end, precision, confidence, created_at) VALUES (?,?,'occurred_during',NULL,?,?,'exact',0.9,'2026-08-21T11:00:00Z')`, "binding-time-"+event.EventID, event.EventID, event.Start, event.End)
		}
	}
	// Hub scale corpus: entity-hub gains a bounded fan-out of typed facts.
	if hubFanout > 0 {
		hubMemory := createTestMemory(t, repository, "graph-quality-hub-root")
		_ = hubMemory
		for i := 0; i < hubFanout; i++ {
			entityID := "entity-hub-target-" + strconvItoa2(i)
			seedEntity(t, repository, entityID, "HubTarget"+strconvItoa2(i), "project")
			memory := createTestMemory(t, repository, "graph-quality-hub-fact-"+strconvItoa2(i))
			seedGraphFact(t, repository, "fact-hub-target-"+strconvItoa2(i), "entity-hub", "depends_on", entityID, memory, 0.9, nil, nil)
		}
	}
	if err := repository.RebuildFTS(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildEntityIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildRelationIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildGraphIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	return graphQualityFixture{repository: repository, corpus: corpus, docIDs: docIDs}
}

func currentRevisionID(t *testing.T, r *Repository, memoryID string) string {
	t.Helper()
	var revisionID string
	if err := r.store.reader.QueryRow(`SELECT current_revision_id FROM memories WHERE memory_id=?`, memoryID).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	return revisionID
}

func TestGraphQualityCorpusCoversCategories(t *testing.T) {
	corpus, _ := loadGraphQuality(t)
	fixture := buildGraphQualityFixture(t, 0)
	required := map[string]bool{}
	for _, category := range corpus.Categories {
		required[category] = false
	}
	if len(required) != 10 {
		t.Fatalf("corpus categories = %v", corpus.Categories)
	}
	// entity, event, conflict, stale, out_of_scope, ambiguous, relational,
	// multi_hop, temporal, hub are all represented by materialized rows.
	var entities, events, relations, facts int
	if err := fixture.repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM entities), (SELECT count(*) FROM event_participants), (SELECT count(*) FROM memory_relations), (SELECT count(*) FROM entity_facts WHERE state='active')`).Scan(&entities, &events, &relations, &facts); err != nil {
		t.Fatal(err)
	}
	if entities < len(corpus.Entities) || events == 0 || relations < 2 || facts < len(corpus.Facts) {
		t.Fatalf("graph corpus incomplete: entities=%d events=%d relations=%d facts=%d", entities, events, relations, facts)
	}
	// Ambiguous alias: both Jordan entities share the alias span.
	tx, err := fixture.repository.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolved, err := resolveQueryEntities(context.Background(), tx, "What did Jordan lead?", []string{corpus.Space})
	if err != nil {
		t.Fatal(err)
	}
	ambiguous := 0
	for _, entity := range resolved {
		if entity.Ambiguous {
			ambiguous++
		}
	}
	if ambiguous < 2 {
		t.Fatalf("ambiguous alias not represented: %+v", resolved)
	}
}

func graphOnlySearch(t *testing.T, r *Repository, query, space, policy string, states []string, limit int) domain.SearchResponse {
	t.Helper()
	params := SearchParams{Query: query, SpaceIDs: []string{space}, States: states, Channels: []string{"graph"}, GraphPolicy: retrieval.GraphPolicy(policy), Limit: limit, MaxChars: 100000}
	result, err := r.SearchMemories(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRetrievalQualityGraphPromotionPrecisionAndHarm(t *testing.T) {
	corpus, gates := loadGraphQuality(t)
	fixture := buildGraphQualityFixture(t, 0)
	r := fixture.repository
	r.SetRetrievalCalibration(retrieval.DefaultWeights(), retrieval.DefaultCalibration(), retrieval.GraphBoundedPath, 0.5, 0.5, 0)

	promotedTotal, promotedHit := 0, 0
	baselineRank, graphRank := 0.0, 0.0
	oneHopEdges, paths := 0, 0
	for _, test := range corpus.PromotionCases {
		expected := map[string]bool{}
		for _, doc := range test.Expected {
			expected[fixture.docIDs[doc]] = true
		}
		graph := graphOnlySearch(t, r, test.Query, corpus.Space, test.Policy, []string{"active"}, 10)
		for _, item := range graph.Results {
			// Promotion precision is measured over typed fact/path promotions,
			// not bare entity mentions, so a generic mention cannot inflate the
			// denominator.
			promoted := false
			for _, signal := range item.GraphSignals {
				switch signal.Kind {
				case "one_hop_fact", "exact_predicate_fact", "two_hop_path":
					promoted = true
					if signal.Kind == "two_hop_path" {
						paths++
					} else {
						oneHopEdges++
					}
				}
			}
			if !promoted {
				continue
			}
			promotedTotal++
			if expected[item.Memory.MemoryID] {
				promotedHit++
			}
		}
		keyword := graphOnlySearchWithChannels(t, r, test.Query, corpus.Space, []string{"keyword"}, []string{"active"}, 10)
		hybrid := graphOnlySearchWithChannels(t, r, test.Query, corpus.Space, []string{"keyword", "graph"}, []string{"active"}, 10)
		baselineRank += recallAtK(memoryIDs(keyword.Results), expected)
		graphRank += recallAtK(memoryIDs(hybrid.Results), expected)
	}
	if promotedTotal == 0 {
		t.Fatal("no graph-promoted candidates were produced")
	}
	precision := float64(promotedHit) / float64(promotedTotal)
	count := float64(len(corpus.PromotionCases))
	harm := (baselineRank - graphRank) / count
	t.Logf("graph promotion precision=%.3f promoted=%d harm=%.4f one_hop_edges=%d paths=%d", precision, promotedTotal, harm, oneHopEdges, paths)
	if precision < gates.Quality.MinimumPromotionPrecision {
		t.Fatalf("graph promotion precision %.3f < %.3f", precision, gates.Quality.MinimumPromotionPrecision)
	}
	if harm > gates.Quality.MaximumRankHarm {
		t.Fatalf("graph rank harm %.4f > %.4f", harm, gates.Quality.MaximumRankHarm)
	}
	if oneHopEdges > gates.Quality.MaximumOneHopEdges || paths > gates.Quality.MaximumPaths {
		t.Fatalf("graph edge/path counts exceed gates: edges=%d paths=%d", oneHopEdges, paths)
	}
}

func graphOnlySearchWithChannels(t *testing.T, r *Repository, query, space string, channels, states []string, limit int) domain.SearchResponse {
	t.Helper()
	result, err := r.SearchMemories(context.Background(), SearchParams{Query: query, SpaceIDs: []string{space}, States: states, Channels: channels, GraphPolicy: retrieval.GraphBoundedPath, Limit: limit, MaxChars: 100000})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func memoryIDs(results []domain.SearchResult) []string {
	ids := make([]string, len(results))
	for i := range results {
		ids[i] = results[i].Memory.MemoryID
	}
	return ids
}

// graphAblationMetrics is the deterministic ablation record. Only counts and
// ratios are stored so the baseline is stable across generated identifiers.
type graphAblationMetrics struct {
	MentionCandidates  int     `json:"mention_candidates"`
	FactCandidates     int     `json:"fact_candidates"`
	RelationCandidates int     `json:"relation_candidates"`
	OneHopPaths        int     `json:"one_hop_paths"`
	TwoHopPaths        int     `json:"two_hop_paths"`
	HubPenaltyDrop     float64 `json:"hub_penalty_drop"`
	ConfidenceGateKept int     `json:"confidence_gate_kept"`
	SeedBudget         int     `json:"seed_budget"`
	BoostCap           float64 `json:"boost_cap"`
}

type graphAblationBaseline struct {
	Version string               `json:"version"`
	Metrics graphAblationMetrics `json:"metrics"`
}

func computeGraphAblationMetrics(t *testing.T, fixture graphQualityFixture, gates graphQualityGates) graphAblationMetrics {
	t.Helper()
	r := fixture.repository
	r.SetRetrievalCalibration(retrieval.DefaultWeights(), retrieval.DefaultCalibration(), retrieval.GraphBoundedPath, 0.5, 0.5, 0)
	metrics := graphAblationMetrics{SeedBudget: 15, BoostCap: gates.Ablations.BoostCap}
	for _, test := range fixture.corpus.PromotionCases {
		graph := graphOnlySearch(t, r, test.Query, fixture.corpus.Space, test.Policy, []string{"active"}, 10)
		for _, item := range graph.Results {
			for _, signal := range item.GraphSignals {
				switch signal.Kind {
				case "direct_entity_mention":
					metrics.MentionCandidates++
				case "exact_predicate_fact", "one_hop_fact":
					metrics.FactCandidates++
				case "memory_relation":
					metrics.RelationCandidates++
				case "two_hop_path":
					metrics.TwoHopPaths++
				}
			}
			if len(item.GraphPaths) > 0 {
				metrics.OneHopPaths += len(item.GraphPaths[0].Edges) - 1
			}
		}
	}
	// Confidence gate ablation: raising the fact floor can only shrink the set.
	lowConfidence := graphOnlySearch(t, r, "Alice works on Alpha", fixture.corpus.Space, "direct", []string{"active"}, 10)
	r.SetRetrievalCalibration(retrieval.DefaultWeights(), retrieval.DefaultCalibration(), retrieval.GraphBoundedPath, gates.Ablations.ConfidenceGate, gates.Ablations.ConfidenceGate, 0)
	highConfidence := graphOnlySearch(t, r, "Alice works on Alpha", fixture.corpus.Space, "direct", []string{"active"}, 10)
	metrics.ConfidenceGateKept = len(highConfidence.Results)
	if len(lowConfidence.Results) > 0 && metrics.ConfidenceGateKept > len(lowConfidence.Results) {
		t.Fatalf("confidence gate admitted more candidates: low=%d high=%d", len(lowConfidence.Results), metrics.ConfidenceGateKept)
	}
	// Hub penalty ablation: the same evidence scores lower through a hub.
	rare := scoreFact(domain.GraphSignal{FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1})
	hub := scoreFact(domain.GraphSignal{FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 0.2, TemporalCompatibility: 1})
	if rare > 0 {
		metrics.HubPenaltyDrop = (rare - hub) / rare
	}
	if metrics.HubPenaltyDrop <= 0 {
		t.Fatal("hub penalty ablation produced no drop")
	}
	return metrics
}

func TestRetrievalQualityGraphAblations(t *testing.T) {
	corpus, gates := loadGraphQuality(t)
	fixture := buildGraphQualityFixture(t, 0)
	metrics := computeGraphAblationMetrics(t, fixture, gates)
	if metrics.MentionCandidates == 0 || metrics.FactCandidates == 0 || metrics.TwoHopPaths == 0 {
		t.Fatalf("ablation families are not all represented: %+v", metrics)
	}
	if metrics.SeedBudget != 15 || metrics.BoostCap <= 0 {
		t.Fatalf("seed budget / boost cap ablation identity invalid: %+v", metrics)
	}
	if metrics.ConfidenceGateKept == 0 {
		t.Fatalf("confidence gate ablation kept no candidates: %+v", metrics)
	}
	_ = corpus
	path := filepath.Join("testdata", "graph-ablation-baseline-v1.json")
	if out := os.Getenv("DMEM_GRAPH_BASELINE_OUT"); out != "" {
		body, err := json.MarshalIndent(graphAblationBaseline{Version: "graph-ablation-baseline-v1", Metrics: metrics}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, append(body, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	var baseline graphAblationBaseline
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("accepted ablation baseline missing: %v", err)
	}
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.Version != "graph-ablation-baseline-v1" || baseline.Metrics != metrics {
		t.Fatalf("ablation metrics drifted from accepted baseline:\n got %+v\nwant %+v", metrics, baseline.Metrics)
	}
}

func TestRetrievalPerformanceGraphTraversalResources(t *testing.T) {
	requirePerformanceGates(t)
	corpus, gates := loadGraphQuality(t)
	fixture := buildGraphQualityFixture(t, gates.Performance.TargetFactCount)
	r := fixture.repository
	r.SetRetrievalCalibration(retrieval.DefaultWeights(), retrieval.DefaultCalibration(), retrieval.GraphBoundedPath, 0.5, 0.5, 0)

	info, err := os.Stat(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	baseSize := info.Size()
	resolved := []domain.ResolvedEntity{{EntityID: "entity-hub", EntityType: "project", Alias: "Hub", Confidence: 0.9, MatchKind: "alias"}}
	params := SearchParams{SpaceIDs: []string{corpus.Space}, States: []string{"active"}, ReferenceTime: "2026-08-21T12:00:00Z"}
	policy := graphRecallPolicy{Policy: retrieval.GraphDirect, AllowNewCandidates: true, Primary: map[string]struct{}{}, MinFactConfidence: gates.Ablations.ConfidenceGate}
	observed := runGraphRecall(t, r, []string{corpus.Space}, policy, resolved, params)
	if len(observed.items) == 0 {
		t.Fatal("hub traversal returned no candidates")
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	durations := make([]time.Duration, gates.Performance.QuerySamples)
	for i := range durations {
		started := time.Now()
		result := runGraphRecall(t, r, []string{corpus.Space}, policy, resolved, params)
		durations[i] = time.Since(started)
		if len(result.items) == 0 {
			t.Fatal("hub traversal returned no candidates")
		}
	}
	runtime.ReadMemStats(&after)
	info, err = os.Stat(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	heap := uint64(0)
	if after.TotalAlloc > before.TotalAlloc {
		heap = after.TotalAlloc - before.TotalAlloc
	}
	p95 := percentile95(durations)
	growth := float64(info.Size()) / float64(baseSize)
	t.Logf("graph traversal resources: p95=%s heap=%d storage_growth=%.2fx facts=%d", p95, heap, growth, gates.Performance.TargetFactCount)
	if float64(p95)/float64(time.Millisecond) > gates.Performance.MaximumGraphQueryP95MS {
		t.Fatalf("graph query p95 %s exceeds %.0fms", p95, gates.Performance.MaximumGraphQueryP95MS)
	}
	if heap > gates.Performance.MaximumGraphQueryHeapBytes {
		t.Fatalf("graph query heap %d exceeds %d", heap, gates.Performance.MaximumGraphQueryHeapBytes)
	}
	if growth > gates.Performance.MaximumGraphStorageGrowth {
		t.Fatalf("graph storage growth %.2fx exceeds %.2fx", growth, gates.Performance.MaximumGraphStorageGrowth)
	}
}
