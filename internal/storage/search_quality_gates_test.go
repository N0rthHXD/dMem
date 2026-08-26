package storage

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

type searchQualityCorpus struct {
	Version       string `json:"version"`
	PrimarySpace  string `json:"primary_space"`
	SiblingSpace  string `json:"sibling_space"`
	EntityQuery   string `json:"entity_query"`
	ConflictQuery string `json:"conflict_query"`
}

type searchQualityGates struct {
	ConfigVersion  string `json:"config_version"`
	CorpusVersion  string `json:"corpus_version"`
	RankingVersion string `json:"ranking_version"`
	Quality        struct {
		MaximumSiblingContamination          int     `json:"maximum_sibling_contamination"`
		MaximumStaleRevisionRecall           int     `json:"maximum_stale_revision_recall"`
		MinimumConflictRecall                float64 `json:"minimum_conflict_recall"`
		MinimumConflictReferenceCompleteness float64 `json:"minimum_conflict_reference_completeness"`
	} `json:"quality"`
	Performance struct {
		TargetMemoryCount                   int     `json:"target_memory_count"`
		QuerySamples                        int     `json:"query_samples"`
		MaximumQueryP95MS                   float64 `json:"maximum_query_p95_ms"`
		MaximumQueryHeapBytes               uint64  `json:"maximum_query_heap_bytes"`
		MaximumCandidates                   int     `json:"maximum_candidates"`
		MaximumAuthorityRebuildMS           float64 `json:"maximum_authority_rebuild_ms"`
		MaximumAuthorityRebuildHeapBytes    uint64  `json:"maximum_authority_rebuild_heap_bytes"`
		MaximumAuthorityDiskAmplification   float64 `json:"maximum_authority_disk_amplification"`
		PortabilityObjectCount              int     `json:"portability_object_count"`
		MinimumPortabilityObjectsPerSecond  float64 `json:"minimum_portability_objects_per_second"`
		MaximumPortabilityTempAmplification float64 `json:"maximum_portability_temp_amplification"`
		MaximumPortabilityCheckpoints       int     `json:"maximum_portability_checkpoints"`
	} `json:"performance"`
}

func loadSearchQualityGates(t *testing.T) (searchQualityCorpus, searchQualityGates) {
	t.Helper()
	var corpus searchQualityCorpus
	var gates searchQualityGates
	readJSONFixture(t, "testdata/search-quality-corpus-v1.json", &corpus)
	readJSONFixture(t, "testdata/search-quality-gates-v1.json", &gates)
	if corpus.Version != gates.CorpusVersion || gates.ConfigVersion != "search-quality-gates-v1" || gates.RankingVersion != rankingVersion || gates.Performance.TargetMemoryCount < 100 || gates.Performance.QuerySamples < 20 {
		t.Fatal("invalid search quality gate identity or target scale")
	}
	return corpus, gates
}

func searchQualityGateFixture(t *testing.T) (*Repository, searchQualityCorpus, searchQualityGates) {
	t.Helper()
	corpus, gates := loadSearchQualityGates(t)
	r, _ := newMemoryTestRepository(t)
	statements := []string{
		`INSERT INTO memory_spaces(space_id,parent_space_id,space_type,external_id,display_name,created_at) VALUES ('space-sibling','space-1','project','sibling','Sibling','2026-08-25T10:00:00Z')`,
		`INSERT INTO entities(entity_id,space_id,entity_type,canonical_name,normalized_name,normalization_version,state,created_by,processing_version,created_at,updated_at) VALUES ('entity-alice','space-1','person','Alice','alice','v1','active','test','v1','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO entity_aliases(alias_id,entity_id,alias,normalized_alias,normalization_version,source,state,created_by,created_at,updated_at) VALUES ('alias-alice','entity-alice','Alice','alice','v1','human_confirmation','active','test','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := r.store.writer.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	create := func(key, space, content, state string) (string, string) {
		params := CreateMemoryParams{SpaceID: space, MemoryType: "fact", Content: content, Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "search quality gate"}
		memory, _, err := r.CreateMemory(context.Background(), memoryIdentity(key, "/v1/memories", params), params)
		if err != nil {
			t.Fatal(err)
		}
		if state != "active" {
			if _, _, err = r.ChangeMemoryState(context.Background(), memoryIdentity(key+"-state", "/v1/memories/{memory_id}/state", state), memory.MemoryID, ChangeMemoryStateParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Action: "contest", Reason: "gate conflict"}); err != nil {
				t.Fatal(err)
			}
			memory, err = r.GetMemory(context.Background(), memory.MemoryID)
			if err != nil {
				t.Fatal(err)
			}
		}
		return memory.MemoryID, memory.CurrentRevision.RevisionID
	}
	entityMemory, entityRevision := create("quality-entity-current", corpus.PrimarySpace, "Alice owns PROJECT-ALPHA", "active")
	staleMemory, staleRevision := create("quality-stale", corpus.PrimarySpace, "obsolete Alice archive token", "active")
	_, siblingRevision := create("quality-sibling", corpus.SiblingSpace, "Alice PROJECT-ALPHA weekly launch brief", "active")
	conflictA, conflictRevisionA := create("quality-conflict-a", corpus.PrimarySpace, "launch date is Friday", "contested")
	conflictB, conflictRevisionB := create("quality-conflict-b", corpus.PrimarySpace, "launch date is Monday", "contested")
	if _, err := r.store.writer.Exec(`INSERT INTO memory_revisions(revision_id,memory_id,parent_revision_id,content,content_hash,created_by,processing_version,created_at) VALUES ('revision-stale-current',? ,?,'current archive token','current-hash','test','v1','2026-08-25T11:00:00Z'); UPDATE memories SET current_revision_id='revision-stale-current',updated_at='2026-08-25T11:00:00Z' WHERE memory_id=?; UPDATE memory_projections SET revision_id='revision-stale-current',display_content='current archive token',content_hash='current-hash',updated_at='2026-08-25T11:00:00Z' WHERE memory_id=?`, staleMemory, staleRevision, staleMemory, staleMemory); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.writer.Exec(`INSERT INTO entity_mentions(mention_id,entity_id,memory_id,revision_id,start_offset,end_offset,mention_type,source,surface_text,created_by,processing_version,created_at) VALUES ('mention-current','entity-alice',?,?,0,5,'explicit','human_confirmation','Alice','test','v1','2026-08-25T10:00:00Z'),('mention-stale','entity-alice',(SELECT memory_id FROM memory_revisions WHERE revision_id=?),?,9,14,'explicit','human_confirmation','Alice','test','v1','2026-08-25T10:00:00Z'),('mention-sibling','entity-alice',(SELECT memory_id FROM memory_revisions WHERE revision_id=?),?,0,5,'explicit','human_confirmation','Alice','test','v1','2026-08-25T10:00:00Z')`, entityMemory, entityRevision, staleRevision, staleRevision, siblingRevision, siblingRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.writer.Exec(`INSERT INTO memory_relations(relation_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,source,specification_hash,created_by,processing_version,created_at,updated_at) VALUES ('conflict-relation','space-1','contradicts',?,?,?,?,'human_confirmation','conflict','test','v1','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z'); INSERT INTO conflict_cases(case_id,space_id,state,opened_by,processing_version,created_at) VALUES ('conflict-case','space-1','open','test','v1','2026-08-25T10:00:00Z'); INSERT INTO conflict_case_participants(case_id,participant_order,memory_id,revision_id,input_state,input_sensitivity,created_at) VALUES ('conflict-case',0,?,?,'contested','normal','2026-08-25T10:00:00Z'),('conflict-case',1,?,?,'contested','normal','2026-08-25T10:00:00Z'); INSERT INTO conflict_case_relations(case_id,relation_id,created_at) VALUES ('conflict-case','conflict-relation','2026-08-25T10:00:00Z')`, conflictA, conflictRevisionA, conflictB, conflictRevisionB, conflictA, conflictRevisionA, conflictB, conflictRevisionB); err != nil {
		t.Fatal(err)
	}
	if err := r.RebuildFTS(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r, corpus, gates
}

func TestSearchQualitySiblingSpaceContaminationZero(t *testing.T) {
	r, corpus, gates := searchQualityGateFixture(t)
	result, err := r.SearchMemories(context.Background(), SearchParams{Query: corpus.EntityQuery, SpaceIDs: []string{corpus.PrimarySpace}, States: []string{"active", "contested"}, Mode: "keyword", Limit: 20, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	contamination := 0
	for _, item := range result.Results {
		if item.Memory.SpaceID == corpus.SiblingSpace {
			contamination++
		}
	}
	if contamination > gates.Quality.MaximumSiblingContamination {
		t.Fatalf("entity sibling contamination=%d, want 0", contamination)
	}
}

func TestSearchQualityStaleRevisionRecallZero(t *testing.T) {
	r, corpus, gates := searchQualityGateFixture(t)
	result, err := r.SearchMemories(context.Background(), SearchParams{Query: corpus.EntityQuery, SpaceIDs: []string{corpus.PrimarySpace}, States: []string{"active"}, Mode: "keyword", Limit: 20, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	stale := 0
	for _, item := range result.Results {
		if item.Memory.CurrentRevision.Content == "obsolete Alice archive token" {
			stale++
		}
	}
	if stale > gates.Quality.MaximumStaleRevisionRecall {
		t.Fatalf("stale relation recall=%d, want 0", stale)
	}
}

func TestSearchQualityConflictRecallAndReferenceCompleteness(t *testing.T) {
	r, corpus, gates := searchQualityGateFixture(t)
	result, err := r.SearchMemories(context.Background(), SearchParams{Query: corpus.ConflictQuery, SpaceIDs: []string{corpus.PrimarySpace}, States: []string{"contested"}, Mode: "keyword", Limit: 20, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	recalled, referenced := 0, 0
	for _, item := range result.Results {
		recalled++
		if len(item.ConflictRefs) == 1 {
			referenced++
		}
	}
	recall, completeness := float64(recalled)/2, float64(referenced)/2
	if recall < gates.Quality.MinimumConflictRecall || completeness < gates.Quality.MinimumConflictReferenceCompleteness {
		t.Fatalf("conflict recall=%.2f references=%.2f, both want 1.0", recall, completeness)
	}
}

func TestSearchPerformanceMultichannelQueryResources(t *testing.T) {
	_, gates := loadSearchQualityGates(t)
	documents := performanceDocuments(gates.Performance.TargetMemoryCount)
	_, retrievalConfig := loadRetrievalEvaluation(t)
	fixture := newRetrievalFixture(t, retrievalConfig, documents)
	if err := fixture.repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	durations := make([]time.Duration, gates.Performance.QuerySamples)
	maximumCandidates := 0
	for i := range durations {
		started := time.Now()
		results := retrievalSearch(t, fixture.repository, "travel", "hybrid", gates.Performance.MaximumCandidates)
		durations[i] = time.Since(started)
		if len(results) > maximumCandidates {
			maximumCandidates = len(results)
		}
	}
	runtime.ReadMemStats(&after)
	heap := uint64(0)
	if after.TotalAlloc > before.TotalAlloc {
		heap = after.TotalAlloc - before.TotalAlloc
	}
	p95 := percentile95(durations)
	t.Logf("search quality performance: p95=%s heap=%d candidates=%d scale=%d", p95, heap, maximumCandidates, len(documents))
	if float64(p95)/float64(time.Millisecond) > gates.Performance.MaximumQueryP95MS || heap > gates.Performance.MaximumQueryHeapBytes || maximumCandidates > gates.Performance.MaximumCandidates {
		t.Fatal("search quality performance thresholds exceeded")
	}
}

func TestSearchPerformanceAuthorityProjectionRebuildResources(t *testing.T) {
	r, _, gates := searchQualityGateFixture(t)
	info, err := os.Stat(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	baseSize := info.Size()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	if err := r.RebuildFTS(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.RebuildEntityIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.RebuildRelationIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.RebuildFeedbackAggregates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.RebuildAggregateFreshness(context.Background()); err != nil {
		t.Fatal(err)
	}
	duration := time.Since(started)
	runtime.ReadMemStats(&after)
	info, err = os.Stat(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	heap := uint64(0)
	if after.TotalAlloc > before.TotalAlloc {
		heap = after.TotalAlloc - before.TotalAlloc
	}
	amplification := float64(info.Size()) / float64(baseSize)
	t.Logf("derived index rebuild performance: duration=%s heap=%d disk=%.2fx", duration, heap, amplification)
	if float64(duration)/float64(time.Millisecond) > gates.Performance.MaximumAuthorityRebuildMS || heap > gates.Performance.MaximumAuthorityRebuildHeapBytes || amplification > gates.Performance.MaximumAuthorityDiskAmplification {
		t.Fatal("derived index rebuild performance thresholds exceeded")
	}
}
