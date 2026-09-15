package storage

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/provider"
	vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

type benchProvider struct{ dimension int }

func (p benchProvider) Embed(_ context.Context, request provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	vectors := make([][]float32, len(request.Inputs()))
	for i := range vectors {
		vectors[i] = make([]float32, p.dimension)
		for j := range vectors[i] {
			vectors[i][j] = 0.5
		}
	}
	return provider.NewEmbeddingResult(vectors, "bench"), nil
}

type benchCorpus struct {
	repository *Repository
	store      *Store
	queryBlob  []byte
	dimension  int
}

var benchID atomic.Int64

func benchRuntime() domain.Runtime {
	return domain.Runtime{
		Now: func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) },
		ID: func() (string, error) {
			return fmt.Sprintf("bench-%d", benchID.Add(1)), nil
		},
	}
}

func newBenchCorpus(b *testing.B, count, dimension int, eligibleRatio float64) *benchCorpus {
	b.Helper()
	store, err := Open(context.Background(), filepath.Join(b.TempDir(), "bench.db"), Options{ReadConnections: 16})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	repository := NewRepository(store, benchRuntime(), 100, 200, 3, "bench-v1")
	repository.ConfigureSemantic(benchProvider{dimension: dimension}, "bench-provider", "bench-model")

	if _, err := store.writer.Exec(`INSERT INTO memory_spaces(space_id, space_type, external_id, display_name, created_at) VALUES ('bench-space','user','bench','Bench','2026-01-01T00:00:00Z')`); err != nil {
		b.Fatal(err)
	}

	rng := rand.New(rand.NewSource(42))
	query := make([]float32, dimension)
	for i := range query {
		query[i] = rng.Float32()*2 - 1
	}
	queryBlob, err := vec.SerializeFloat32(query)
	if err != nil {
		b.Fatal(err)
	}

	tx, err := store.writer.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()

	insertMemory, err := tx.Prepare(`INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,created_at,updated_at) VALUES (?,?,?,'active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	if err != nil {
		b.Fatal(err)
	}
	defer insertMemory.Close()
	insertRevision, err := tx.Prepare(`INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES (?,?,'fact',?,?,'hash','normal',0,0,'bench','bench-v1','2026-01-01T00:00:00Z')`)
	if err != nil {
		b.Fatal(err)
	}
	defer insertRevision.Close()
	insertProjection, err := tx.Prepare(`INSERT INTO memory_projections(memory_id,revision_id,space_id,memory_type,current_state,searchable,display_content,content_hash,embedding_state,embedding_provider,embedding_model,embedding_dimension,updated_at) VALUES (?,?, 'bench-space','fact','active',1,?,'hash','current','bench-provider','bench-model',?,'2026-01-01T00:00:00Z')`)
	if err != nil {
		b.Fatal(err)
	}
	defer insertProjection.Close()
	insertEligibility, err := tx.Prepare(`INSERT INTO embedding_eligibility(memory_id,revision_id,space_id,configuration_generation,policy_generation,eligible,reason_code,evaluated_at) VALUES (?,?, 'bench-space',0,0,?,'bench','2026-01-01T00:00:00Z')`)
	if err != nil {
		b.Fatal(err)
	}
	defer insertEligibility.Close()
	insertEmbedding, err := tx.Prepare(`INSERT INTO memory_embeddings(memory_id,revision_id,content_hash,compatibility_identity,configuration_identity,provider_identity,model_identity,dimension,vector_blob,processed_at) VALUES (?,?,'hash','bench-provider/bench-model','bench-provider/bench-model','bench-provider','bench-model',?,?, '2026-01-01T00:00:00Z')`)
	if err != nil {
		b.Fatal(err)
	}
	defer insertEmbedding.Close()

	for i := 0; i < count; i++ {
		memoryID := fmt.Sprintf("bench-memory-%07d", i)
		revisionID := fmt.Sprintf("bench-revision-%07d", i)
		content := fmt.Sprintf("benchmark record %d for semantic recall", i)
		vector := make([]float32, dimension)
		for j := range vector {
			vector[j] = rng.Float32()*2 - 1
		}
		blob, err := vec.SerializeFloat32(vector)
		if err != nil {
			b.Fatal(err)
		}
		eligible := int(0)
		if float64(i) < float64(count)*eligibleRatio {
			eligible = 1
		}
		if _, err := insertMemory.Exec(memoryID, "bench-space", revisionID); err != nil {
			b.Fatal(err)
		}
		if _, err := insertRevision.Exec(revisionID, memoryID, content, content); err != nil {
			b.Fatal(err)
		}
		if _, err := insertProjection.Exec(memoryID, revisionID, content, dimension); err != nil {
			b.Fatal(err)
		}
		if _, err := insertEligibility.Exec(memoryID, revisionID, eligible); err != nil {
			b.Fatal(err)
		}
		if _, err := insertEmbedding.Exec(memoryID, revisionID, dimension, blob); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return &benchCorpus{repository: repository, store: store, queryBlob: queryBlob, dimension: dimension}
}

func (c *benchCorpus) recall(b *testing.B, topK int) {
	b.Helper()
	params := SearchParams{
		Query: "benchmark", SpaceIDs: []string{"bench-space"}, States: []string{"active"},
		Channels: []string{"semantic"}, Limit: 10, MaxChars: 100000,
		ReferenceTime: formatTime(time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)),
	}
	runtime := c.repository.emb()
	tx, err := c.store.reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	items, err := semanticVectorRecall(context.Background(), tx, params, []string{"bench-space"}, runtime.PolicyGeneration, runtime.CompatibilityIdentity, c.dimension, c.queryBlob, topK)
	if err != nil {
		b.Fatal(err)
	}
	if len(items) == 0 {
		b.Fatal("recall returned no results")
	}
}

func benchmarkSemanticRecall(b *testing.B, count int, eligibleRatio float64, topK int) {
	b.Helper()
	corpus := newBenchCorpus(b, count, 1024, eligibleRatio)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		corpus.recall(b, topK)
	}
}

func BenchmarkSemanticRecall(b *testing.B) {
	for _, tc := range []struct {
		name          string
		count         int
		eligibleRatio float64
	}{
		{"1Kx1024", 1000, 1.0},
		{"10Kx1024", 10000, 1.0},
		{"100Kx1024", 100000, 1.0},
	} {
		b.Run(tc.name, func(b *testing.B) {
			for _, topK := range []int{10, 100, 1000} {
				b.Run(fmt.Sprintf("top%d", topK), func(b *testing.B) {
					benchmarkSemanticRecall(b, tc.count, tc.eligibleRatio, topK)
				})
			}
		})
	}
}

func BenchmarkSemanticRecallEligibleRatio(b *testing.B) {
	for _, ratio := range []float64{0.01, 0.1, 1.0} {
		b.Run(fmt.Sprintf("eligible%.0f", ratio*100), func(b *testing.B) {
			benchmarkSemanticRecall(b, 10000, ratio, 100)
		})
	}
}

func BenchmarkSemanticRecallConcurrency(b *testing.B) {
	for _, concurrency := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("concurrency%d", concurrency), func(b *testing.B) {
			corpus := newBenchCorpus(b, 10000, 1024, 1.0)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					corpus.recall(b, 100)
				}
			})
		})
	}
}

func BenchmarkSemanticRebuild(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("rebuild%d", count), func(b *testing.B) {
			corpus := newBenchCorpus(b, count, 1024, 1.0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := corpus.repository.RebuildSemantic(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
