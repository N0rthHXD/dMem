package storage

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/vector"
)

type retrievalCorpus struct {
	Version         string           `json:"version"`
	Documents       []retrievalDoc   `json:"documents"`
	SemanticQueries []retrievalQuery `json:"semantic_queries"`
	NoAnswerQueries []retrievalQuery `json:"no_answer_queries"`
}

type retrievalDoc struct {
	ID              string   `json:"id"`
	Space           string   `json:"space"`
	Content         string   `json:"content"`
	ExactIdentifier string   `json:"exact_identifier,omitempty"`
	Concepts        []string `json:"concepts"`
}

type retrievalQuery struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Relevant []string `json:"relevant,omitempty"`
}

type retrievalGateConfig struct {
	ConfigVersion      string `json:"config_version"`
	CorpusVersion      string `json:"corpus_version"`
	ProviderVersion    string `json:"provider_version"`
	ProviderModel      string `json:"provider_model"`
	IndexFormatVersion int    `json:"index_format_version"`
	RankingVersion     string `json:"ranking_version"`
	Quality            struct {
		SemanticK                       int     `json:"semantic_k"`
		MinimumKeywordExactRecall       float64 `json:"minimum_keyword_exact_recall"`
		MinimumSemanticRecallAtK        float64 `json:"minimum_semantic_recall_at_k"`
		MinimumHybridRecallAtK          float64 `json:"minimum_hybrid_recall_at_k"`
		MinimumHybridMRR                float64 `json:"minimum_hybrid_mrr"`
		MaximumNoAnswerIrrelevantRecall float64 `json:"maximum_no_answer_irrelevant_recall"`
		MaximumSiblingContamination     int     `json:"maximum_sibling_contamination"`
	}
	Performance struct {
		TargetMemoryCount               int     `json:"target_memory_count"`
		QuerySamplesPerMode             int     `json:"query_samples_per_mode"`
		MaximumKeywordP95MS             float64 `json:"maximum_keyword_p95_ms"`
		MaximumSemanticP95MS            float64 `json:"maximum_semantic_p95_ms"`
		MaximumHybridP95MS              float64 `json:"maximum_hybrid_p95_ms"`
		MaximumRebuildMS                float64 `json:"maximum_rebuild_ms"`
		MaximumRebuildPeakHeapBytes     uint64  `json:"maximum_rebuild_peak_heap_bytes"`
		MaximumRebuildDiskAmplification float64 `json:"maximum_rebuild_disk_amplification"`
	}
	ConceptVocabulary map[string][]string `json:"concept_vocabulary"`
}

type deterministicEmbeddingProvider struct {
	terms     map[string]int
	dimension int
}

func newDeterministicEmbeddingProvider(config retrievalGateConfig) *deterministicEmbeddingProvider {
	concepts := make([]string, 0, len(config.ConceptVocabulary))
	for concept := range config.ConceptVocabulary {
		concepts = append(concepts, concept)
	}
	sort.Strings(concepts)
	terms := make(map[string]int)
	for axis, concept := range concepts {
		terms[concept] = axis
		for _, term := range config.ConceptVocabulary[concept] {
			terms[strings.ToLower(term)] = axis
		}
	}
	return &deterministicEmbeddingProvider{terms: terms, dimension: len(concepts)}
}

func (p *deterministicEmbeddingProvider) Embed(_ context.Context, request provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	vectors := make([][]float32, len(request.Inputs()))
	for n, input := range request.Inputs() {
		vector := make([]float32, p.dimension)
		for _, token := range strings.FieldsFunc(strings.ToLower(input), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if axis, ok := p.terms[token]; ok {
				vector[axis]++
			}
		}
		vectors[n] = vector
	}
	return provider.NewEmbeddingResult(vectors, "deterministic-concepts-v1"), nil
}

type retrievalFixture struct {
	repository *Repository
	indexDir   string
	docIDs     map[string]string
}

func loadRetrievalEvaluation(t *testing.T) (retrievalCorpus, retrievalGateConfig) {
	t.Helper()
	var corpus retrievalCorpus
	var config retrievalGateConfig
	readJSONFixture(t, "testdata/retrieval-corpus-v1.json", &corpus)
	readJSONFixture(t, "testdata/retrieval-gates-v1.json", &config)
	if corpus.Version != config.CorpusVersion || config.ConfigVersion != "retrieval-gates-v1" || config.ProviderVersion != "deterministic-concepts-v1" || config.IndexFormatVersion != vector.FormatVersion || config.RankingVersion != rankingVersion {
		t.Fatalf("evaluation identity mismatch: corpus=%q config=%q provider=%q index=%d ranking=%q", corpus.Version, config.ConfigVersion, config.ProviderVersion, config.IndexFormatVersion, config.RankingVersion)
	}
	if len(config.ConceptVocabulary) < 2 || config.Quality.SemanticK < 1 || config.Performance.TargetMemoryCount < 100 || config.Performance.QuerySamplesPerMode < 20 {
		t.Fatal("retrieval gate configuration is not a meaningful target scale")
	}
	return corpus, config
}

func readJSONFixture(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

func newRetrievalFixture(t *testing.T, config retrievalGateConfig, documents []retrievalDoc) retrievalFixture {
	t.Helper()
	repository, _ := newMemoryTestRepository(t)
	indexDir := filepath.Join(t.TempDir(), "vectors")
	index, err := vector.Open(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	repository.ConfigureSemantic(newDeterministicEmbeddingProvider(config), index, config.ProviderVersion, config.ProviderModel)
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES ('space-sibling', 'space-1', 'project', 'project-sibling', 'Sibling', '2026-08-21T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]string, len(documents))
	for n, document := range documents {
		spaceID := "space-1"
		if document.Space == "sibling" {
			spaceID = "space-sibling"
		}
		params := CreateMemoryParams{SpaceID: spaceID, MemoryType: "fact", Content: document.Content, Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "retrieval evaluation"}
		memory, _, err := repository.CreateMemory(context.Background(), memoryIdentity("retrieval-"+document.ID, "/v1/memories", params), params)
		if err != nil {
			t.Fatalf("create corpus document %s (%d): %v", document.ID, n, err)
		}
		ids[document.ID] = memory.MemoryID
	}
	return retrievalFixture{repository: repository, indexDir: indexDir, docIDs: ids}
}

func retrievalSearch(t *testing.T, repository *Repository, query, mode string, limit int) []string {
	t.Helper()
	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: query, SpaceIDs: []string{"space-1"}, States: []string{"active"}, Mode: mode, Limit: limit, MaxChars: 100000})
	if err != nil {
		t.Fatalf("%s search %q: %v", mode, query, err)
	}
	ids := make([]string, len(result.Results))
	for n := range result.Results {
		ids[n] = result.Results[n].Memory.MemoryID
	}
	return ids
}

func TestRetrievalQualityKeywordExactRecall(t *testing.T) {
	corpus, config := loadRetrievalEvaluation(t)
	fixture := newRetrievalFixture(t, config, corpus.Documents)
	hits := 0
	for _, document := range corpus.Documents {
		if document.Space != "primary" {
			continue
		}
		if document.ExactIdentifier == "" {
			t.Fatalf("primary document %s has no exact identifier judgment", document.ID)
		}
		if includesID(retrievalSearch(t, fixture.repository, `"`+document.ExactIdentifier+`"`, "keyword", 5), fixture.docIDs[document.ID]) {
			hits++
		}
	}
	recall := float64(hits) / float64(len(corpus.Documents)-1)
	if recall < config.Quality.MinimumKeywordExactRecall {
		t.Fatalf("exact keyword recall = %.3f, want >= %.3f", recall, config.Quality.MinimumKeywordExactRecall)
	}
}

func TestRetrievalQualitySemanticRecallAndHybridRanking(t *testing.T) {
	corpus, config := loadRetrievalEvaluation(t)
	fixture := newRetrievalFixture(t, config, corpus.Documents)
	if err := fixture.repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	var semanticRecall, hybridRecall, reciprocalRank float64
	for _, query := range corpus.SemanticQueries {
		relevant := make(map[string]bool, len(query.Relevant))
		for _, id := range query.Relevant {
			relevant[fixture.docIDs[id]] = true
		}
		semantic := retrievalSearch(t, fixture.repository, query.Text, "semantic", config.Quality.SemanticK)
		hybrid := retrievalSearch(t, fixture.repository, query.Text, "hybrid", config.Quality.SemanticK)
		semanticRecall += recallAtK(semantic, relevant)
		hybridRecall += recallAtK(hybrid, relevant)
		reciprocalRank += mrr(hybrid, relevant)
	}
	count := float64(len(corpus.SemanticQueries))
	semanticRecall, hybridRecall, reciprocalRank = semanticRecall/count, hybridRecall/count, reciprocalRank/count
	if semanticRecall < config.Quality.MinimumSemanticRecallAtK || hybridRecall < config.Quality.MinimumHybridRecallAtK || reciprocalRank < config.Quality.MinimumHybridMRR {
		t.Fatalf("retrieval quality below threshold: semantic recall@%d=%.3f hybrid recall@%d=%.3f hybrid MRR=%.3f", config.Quality.SemanticK, semanticRecall, config.Quality.SemanticK, hybridRecall, reciprocalRank)
	}
}

func TestRetrievalQualityNoAnswerIrrelevantRecall(t *testing.T) {
	corpus, config := loadRetrievalEvaluation(t)
	fixture := newRetrievalFixture(t, config, corpus.Documents)
	if err := fixture.repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	irrelevant := 0
	for _, query := range corpus.NoAnswerQueries {
		if len(retrievalSearch(t, fixture.repository, query.Text, "semantic", config.Quality.SemanticK)) > 0 {
			irrelevant++
		}
	}
	rate := float64(irrelevant) / float64(len(corpus.NoAnswerQueries))
	if rate > config.Quality.MaximumNoAnswerIrrelevantRecall {
		t.Fatalf("no-answer irrelevant recall = %.3f, want <= %.3f", rate, config.Quality.MaximumNoAnswerIrrelevantRecall)
	}
}

func TestRetrievalQualitySiblingContaminationZero(t *testing.T) {
	corpus, config := loadRetrievalEvaluation(t)
	fixture := newRetrievalFixture(t, config, corpus.Documents)
	if err := fixture.repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	siblingID := fixture.docIDs["sibling-private"]
	contamination := 0
	for _, mode := range []string{"keyword", "semantic", "hybrid"} {
		if includesID(retrievalSearch(t, fixture.repository, "vacation travel", mode, 20), siblingID) {
			contamination++
		}
	}
	if contamination > config.Quality.MaximumSiblingContamination {
		t.Fatalf("sibling contamination = %d, want <= %d", contamination, config.Quality.MaximumSiblingContamination)
	}
}

func TestRetrievalPerformanceQueryP95AtTargetScale(t *testing.T) {
	_, config := loadRetrievalEvaluation(t)
	documents := performanceDocuments(config.Performance.TargetMemoryCount)
	fixture := newRetrievalFixture(t, config, documents)
	if err := fixture.repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	limits := map[string]float64{"keyword": config.Performance.MaximumKeywordP95MS, "semantic": config.Performance.MaximumSemanticP95MS, "hybrid": config.Performance.MaximumHybridP95MS}
	for _, mode := range []string{"keyword", "semantic", "hybrid"} {
		retrievalSearch(t, fixture.repository, "travel", mode, 10)
		durations := make([]time.Duration, config.Performance.QuerySamplesPerMode)
		for n := range durations {
			started := time.Now()
			retrievalSearch(t, fixture.repository, "travel", mode, 10)
			durations[n] = time.Since(started)
		}
		p95 := percentile95(durations)
		t.Logf("query performance: mode=%s p95=%s scale=%d samples=%d", mode, p95, len(documents), len(durations))
		if float64(p95)/float64(time.Millisecond) > limits[mode] {
			t.Fatalf("%s p95 %s exceeds %.0fms", mode, p95, limits[mode])
		}
	}
}

func TestRetrievalPerformanceSemanticRebuildResources(t *testing.T) {
	_, config := loadRetrievalEvaluation(t)
	documents := performanceDocuments(config.Performance.TargetMemoryCount)
	fixture := newRetrievalFixture(t, config, documents)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var peakHeap, peakDisk atomic.Uint64
	peakHeap.Store(before.HeapAlloc)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			updateMaximum(&peakHeap, stats.HeapAlloc)
			updateMaximum(&peakDisk, directoryBytes(fixture.indexDir))
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	started := time.Now()
	err := fixture.repository.RebuildSemantic(context.Background())
	duration := time.Since(started)
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	updateMaximum(&peakDisk, directoryBytes(fixture.indexDir))
	heapGrowth := uint64(0)
	if peakHeap.Load() > before.HeapAlloc {
		heapGrowth = peakHeap.Load() - before.HeapAlloc
	}
	rawPayload := uint64(len(documents) * newDeterministicEmbeddingProvider(config).dimension * 8)
	diskAmplification := float64(peakDisk.Load()) / float64(rawPayload)
	t.Logf("semantic rebuild performance: duration=%s peak_heap_growth=%d peak_disk=%d raw_vector_payload=%d disk_amplification=%.2fx", duration, heapGrowth, peakDisk.Load(), rawPayload, diskAmplification)
	if float64(duration)/float64(time.Millisecond) > config.Performance.MaximumRebuildMS || heapGrowth > config.Performance.MaximumRebuildPeakHeapBytes || diskAmplification > config.Performance.MaximumRebuildDiskAmplification {
		t.Fatalf("semantic rebuild thresholds exceeded: duration=%s heap=%d disk amplification=%.2fx", duration, heapGrowth, diskAmplification)
	}
}

func performanceDocuments(count int) []retrievalDoc {
	documents := make([]retrievalDoc, count)
	for n := range documents {
		documents[n] = retrievalDoc{ID: "perf-" + zeroPad(n), Space: "primary", Content: "Travel itinerary benchmark record PERF-" + zeroPad(n) + " for flight plans", Concepts: []string{"travel"}}
	}
	return documents
}

func zeroPad(value int) string {
	const digits = "000000"
	raw := strings.TrimLeft(digits+strconvItoa(value), "0")
	if raw == "" {
		raw = "0"
	}
	return digits[:max(0, len(digits)-len(raw))] + raw
}

func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	n := len(digits)
	for value > 0 {
		n--
		digits[n] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[n:])
}

func recallAtK(results []string, relevant map[string]bool) float64 {
	hits := 0
	for _, id := range results {
		if relevant[id] {
			hits++
		}
	}
	return float64(hits) / float64(len(relevant))
}

func mrr(results []string, relevant map[string]bool) float64 {
	for n, id := range results {
		if relevant[id] {
			return 1 / float64(n+1)
		}
	}
	return 0
}

func includesID(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func percentile95(values []time.Duration) time.Duration {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values[int(math.Ceil(float64(len(values))*0.95))-1]
}

func updateMaximum(target *atomic.Uint64, value uint64) {
	for current := target.Load(); value > current && !target.CompareAndSwap(current, value); current = target.Load() {
	}
}

func directoryBytes(path string) uint64 {
	var total uint64
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += uint64(info.Size())
		}
		return nil
	})
	return total
}
