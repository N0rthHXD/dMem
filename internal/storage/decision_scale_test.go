package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

type decisionScaleGates struct {
	ConfigVersion string `json:"config_version"`
	Performance   struct {
		TargetMemoryCount         int     `json:"target_memory_count"`
		QuerySamples              int     `json:"query_samples"`
		MaximumConsolidationP95MS float64 `json:"maximum_consolidation_p95_ms"`
		MaximumFeatureP95MS       float64 `json:"maximum_feature_p95_ms"`
		MaximumMMRSelectionP95MS  float64 `json:"maximum_mmr_selection_p95_ms"`
		MaximumWriteAmplification float64 `json:"maximum_write_amplification"`
		MaximumDecisionHeapBytes  int64   `json:"maximum_decision_heap_bytes"`
	} `json:"performance"`
}

func loadDecisionScaleGates(t *testing.T) decisionScaleGates {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "decision-scale-gates-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gates decisionScaleGates
	if err := json.Unmarshal(data, &gates); err != nil {
		t.Fatal(err)
	}
	if gates.ConfigVersion != "decision-scale-gates-v1" {
		t.Fatalf("decision scale gates version = %q", gates.ConfigVersion)
	}
	return gates
}

func sqlitePageBytes(t *testing.T, repository *Repository) int64 {
	t.Helper()
	var pageCount, pageSize int64
	if err := repository.store.writer.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		t.Fatal(err)
	}
	if err := repository.store.writer.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	return pageCount * pageSize
}

func decisionPercentile95(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	index := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

// TestDecisionScaleGates measures write amplification, consolidation candidate
// recall, feature calculation, MMR selection, and decision-stage allocation at
// the target scale. Thresholds live in
// testdata/decision-scale-gates-v1.json so they are versioned and reviewable.
func TestDecisionScaleGates(t *testing.T) {
	requirePerformanceGates(t)
	gates := loadDecisionScaleGates(t)
	repository, _ := newMemoryTestRepository(t)

	pageBefore := sqlitePageBytes(t, repository)
	var contentBytes int64
	var last domain.Memory
	for i := 0; i < gates.Performance.TargetMemoryCount; i++ {
		content := fmt.Sprintf("scaleword item %04d unique", i)
		contentBytes += int64(len(content))
		last = createMemoryWithContent(t, repository, fmt.Sprintf("scale-%04d", i), content)
	}
	pageAfter := sqlitePageBytes(t, repository)
	if contentBytes == 0 {
		t.Fatal("no content written")
	}
	amplification := float64(pageAfter-pageBefore) / float64(contentBytes)
	if amplification > gates.Performance.MaximumWriteAmplification {
		t.Fatalf("write amplification = %.1f, gate %.1f", amplification, gates.Performance.MaximumWriteAmplification)
	}

	params := repository.planQuery(context.Background(), SearchParams{Query: "scaleword", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Limit: 20, MaxChars: 10000})
	candidates := make([]*searchCandidate, 200)
	for i := range candidates {
		candidates[i] = &searchCandidate{
			memoryID: fmt.Sprintf("feature-%03d", i), revisionID: fmt.Sprintf("feature-rev-%03d", i),
			keywordRank: i + 1, keywordRaw: -float64(i), semanticRank: i + 1, semanticRaw: 0.9 - float64(i)*0.001,
			ftsContent: "scaleword item",
		}
	}
	var featureSamples []float64
	for sample := 0; sample < gates.Performance.QuerySamples; sample++ {
		start := time.Now()
		for _, candidate := range candidates {
			_ = repository.buildFeatures(candidate, params, params.Plan)
		}
		featureSamples = append(featureSamples, float64(time.Since(start).Milliseconds()))
	}
	if p95 := decisionPercentile95(featureSamples); p95 > gates.Performance.MaximumFeatureP95MS {
		t.Fatalf("feature calculation p95 = %.1fms, gate %.1fms", p95, gates.Performance.MaximumFeatureP95MS)
	}

	contextCandidates := make([]domain.ContextCandidate, 150)
	for i := range contextCandidates {
		contextCandidates[i] = domain.ContextCandidate{
			MemoryID: fmt.Sprintf("context-%03d", i), RevisionID: fmt.Sprintf("context-rev-%03d", i),
			SpaceID: "space-1", Kind: domain.MemoryKindFact, Relevance: 0.9 - float64(i)*0.001,
			Rank: i + 1, EstimatedTokens: 10, EstimatedChars: 10, LifecycleState: "active",
		}
	}
	var mmrSamples []float64
	for sample := 0; sample < gates.Performance.QuerySamples; sample++ {
		start := time.Now()
		_ = retrieval.SelectContext(retrieval.SelectInput{
			QuestionType: retrieval.QuestionLookup,
			Budget:       domain.ContextBudget{MaxTokens: 100000, MaxChars: 100000, MaxResults: 50, MaxSources: 100, MaxSensitive: 100},
			Candidates:   contextCandidates,
		})
		mmrSamples = append(mmrSamples, float64(time.Since(start).Milliseconds()))
	}
	if p95 := decisionPercentile95(mmrSamples); p95 > gates.Performance.MaximumMMRSelectionP95MS {
		t.Fatalf("MMR selection p95 = %.1fms, gate %.1fms", p95, gates.Performance.MaximumMMRSelectionP95MS)
	}

	tx, err := repository.store.reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var consolidationSamples []float64
	var heapBefore, heapAfter runtime.MemStats
	runtime.ReadMemStats(&heapBefore)
	for sample := 0; sample < gates.Performance.QuerySamples; sample++ {
		start := time.Now()
		if _, err := repository.boundedConsolidationRecall(context.Background(), tx, "space-1", last.CurrentRevision); err != nil {
			t.Fatal(err)
		}
		consolidationSamples = append(consolidationSamples, float64(time.Since(start).Milliseconds()))
	}
	runtime.ReadMemStats(&heapAfter)
	if p95 := decisionPercentile95(consolidationSamples); p95 > gates.Performance.MaximumConsolidationP95MS {
		t.Fatalf("consolidation recall p95 = %.1fms, gate %.1fms", p95, gates.Performance.MaximumConsolidationP95MS)
	}
	if allocated := int64(heapAfter.TotalAlloc - heapBefore.TotalAlloc); allocated > gates.Performance.MaximumDecisionHeapBytes {
		t.Fatalf("decision allocation = %d bytes, gate %d", allocated, gates.Performance.MaximumDecisionHeapBytes)
	}
}
