package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/provider"
	vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

func decodeFloat32Blob(blob []byte) []float32 {
	values := make([]float32, len(blob)/4)
	for i := range values {
		values[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return values
}

func newVecTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestFloat32BlobRoundTrip(t *testing.T) {
	input := []float32{1.5, -2.0, 3.25, 0.0, -0.5}
	blob, err := vec.SerializeFloat32(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != len(input)*4 {
		t.Fatalf("blob length = %d, want %d", len(blob), len(input)*4)
	}
	decoded := decodeFloat32Blob(blob)
	if !reflect.DeepEqual(decoded, input) {
		t.Fatalf("round trip = %v, want %v", decoded, input)
	}

	db := newVecTestDB(t)
	var length int
	if err := db.QueryRow("SELECT vec_length(?)", blob).Scan(&length); err != nil {
		t.Fatal(err)
	}
	if length != len(input) {
		t.Fatalf("vec_length = %d, want %d", length, len(input))
	}
}

func TestVectorBlobDimensionMismatchRejected(t *testing.T) {
	db := newVecTestDB(t)
	if _, err := db.Exec("CREATE TABLE embeddings (dimension INTEGER NOT NULL, vector_blob BLOB NOT NULL CHECK (typeof(vector_blob)='blob' AND vec_length(vector_blob)=dimension))"); err != nil {
		t.Fatal(err)
	}
	blob, err := vec.SerializeFloat32([]float32{1, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO embeddings(dimension, vector_blob) VALUES (?, ?)", 3, blob); err == nil {
		t.Fatal("dimension mismatch accepted")
	}
	if _, err := db.Exec("INSERT INTO embeddings(dimension, vector_blob) VALUES (?, ?)", 4, blob); err != nil {
		t.Fatalf("matching dimension rejected: %v", err)
	}
}

func TestVectorBlobMalformedLengthRejected(t *testing.T) {
	db := newVecTestDB(t)
	if _, err := db.Exec("CREATE TABLE embeddings (dimension INTEGER NOT NULL, vector_blob BLOB NOT NULL CHECK (typeof(vector_blob)='blob' AND vec_length(vector_blob)=dimension))"); err != nil {
		t.Fatal(err)
	}
	for _, malformed := range [][]byte{{}, {1, 2, 3}} {
		if _, err := db.Exec("INSERT INTO embeddings(dimension, vector_blob) VALUES (?, ?)", 1, malformed); err == nil {
			t.Fatalf("malformed blob length accepted: %v", malformed)
		}
	}
}

func TestValidEmbeddingRejectsNonFiniteAndZero(t *testing.T) {
	for _, invalid := range [][]float32{
		{},
		{0, 0},
		{float32(math.NaN()), 0},
		{float32(math.Inf(1)), 0},
		{float32(math.Inf(-1)), 0},
	} {
		if _, err := validEmbedding(invalid); err == nil {
			t.Fatalf("invalid embedding accepted: %v", invalid)
		}
	}
	if _, err := validEmbedding([]float32{1, 0}); err != nil {
		t.Fatalf("valid embedding rejected: %v", err)
	}
}

func naiveCosineDistance(a, b []float32) float64 {
	var dot, aMag, bMag float32
	for i := range a {
		dot += a[i] * b[i]
		aMag += a[i] * a[i]
		bMag += b[i] * b[i]
	}
	return 1 - float64(dot)/(math.Sqrt(float64(aMag))*math.Sqrt(float64(bMag)))
}

func TestExactCosineMatchesNaiveBaseline(t *testing.T) {
	db := newVecTestDB(t)
	rng := rand.New(rand.NewSource(1))
	vectors := make([][]float32, 20)
	for i := range vectors {
		vectors[i] = make([]float32, 1024)
		for j := range vectors[i] {
			vectors[i][j] = rng.Float32()*2 - 1
		}
	}
	query := make([]float32, 1024)
	for j := range query {
		query[j] = rng.Float32()*2 - 1
	}
	queryBlob, err := vec.SerializeFloat32(query)
	if err != nil {
		t.Fatal(err)
	}
	for i, vector := range vectors {
		blob, err := vec.SerializeFloat32(vector)
		if err != nil {
			t.Fatal(err)
		}
		var distance float64
		if err := db.QueryRow("SELECT vec_distance_cosine(?, ?)", blob, queryBlob).Scan(&distance); err != nil {
			t.Fatal(err)
		}
		want := naiveCosineDistance(vector, query)
		if math.Abs(distance-want) > 1e-4 {
			t.Fatalf("vector %d distance = %f, want %f", i, distance, want)
		}
	}
}

func TestSimilarityDistanceConversion(t *testing.T) {
	db := newVecTestDB(t)
	same, _ := vec.SerializeFloat32([]float32{1, 0, 0})
	orthogonal, _ := vec.SerializeFloat32([]float32{0, 1, 0})
	var distance float64
	if err := db.QueryRow("SELECT vec_distance_cosine(?, ?)", same, same).Scan(&distance); err != nil {
		t.Fatal(err)
	}
	if similarity := 1 - distance; math.Abs(similarity-1) > 1e-6 {
		t.Fatalf("identical similarity = %f, want 1", similarity)
	}
	if err := db.QueryRow("SELECT vec_distance_cosine(?, ?)", same, orthogonal).Scan(&distance); err != nil {
		t.Fatal(err)
	}
	if similarity := 1 - distance; math.Abs(similarity-0) > 1e-6 {
		t.Fatalf("orthogonal similarity = %f, want 0", similarity)
	}
}

func TestSQLiteVecAvailableOnConnections(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "vec-conn.db"), Options{ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for name, db := range map[string]*sql.DB{"writer": store.writer, "reader": store.reader} {
		var version string
		if err := db.QueryRow("SELECT vec_version()").Scan(&version); err != nil {
			t.Fatalf("%s vec_version: %v", name, err)
		}
		if version == "" {
			t.Fatalf("%s returned empty version", name)
		}
	}
	maintenanceDB := newVecTestDB(t)
	var version string
	if err := maintenanceDB.QueryRow("SELECT vec_version()").Scan(&version); err != nil {
		t.Fatalf("maintenance connection vec_version: %v", err)
	}
}

func semanticRecallQuery(t *testing.T, repository *Repository, states []string, memoryTypesSet bool, memoryTypes []string, limit int) []semanticRecallItem {
	t.Helper()
	blob, err := vec.SerializeFloat32([]float32{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	params := SearchParams{
		Query: "car", SpaceIDs: []string{"space-1"}, States: states, Channels: []string{"semantic"},
		Limit: limit, MaxChars: 1000, MemoryTypesSet: memoryTypesSet, MemoryTypes: memoryTypes,
		ReferenceTime: formatTime(repository.runtime.Now()),
	}
	tx, err := repository.store.reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	runtime := repository.emb()
	items, err := semanticVectorRecall(context.Background(), tx, params, []string{"space-1"}, runtime.PolicyGeneration, runtime.CompatibilityIdentity, 2, blob, limit)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestSemanticRecallStableTieBreakUsesMemoryAndRevisionIDs(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMemoryWithContent(t, repository, "tie-a", "automobile tie a")
	createMemoryWithContent(t, repository, "tie-b", "automobile tie b")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	items := semanticRecallQuery(t, repository, []string{"active"}, false, nil, 10)
	if len(items) != 2 {
		t.Fatalf("recall items = %d, want 2", len(items))
	}
	if items[0].MemoryID > items[1].MemoryID {
		t.Fatalf("tie break not ordered by memory_id: %q > %q", items[0].MemoryID, items[1].MemoryID)
	}
	if items[0].Score != items[1].Score {
		t.Fatalf("tie scores differ: %f vs %f", items[0].Score, items[1].Score)
	}
}

func TestSemanticRecallAppliesStateTypeFiltersBeforeLimit(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	eligible := createMemoryWithContent(t, repository, "recall-filters-eligible", "automobile filters eligible")
	excluded := createMemoryWithContent(t, repository, "recall-filters-excluded", "automobile filters excluded")
	if _, err := repository.store.writer.Exec(`UPDATE memory_projections SET memory_type='decision' WHERE memory_id=?`, excluded.MemoryID); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	items := semanticRecallQuery(t, repository, []string{"active"}, true, []string{"fact"}, 1)
	if len(items) != 1 || items[0].MemoryID != eligible.MemoryID {
		t.Fatalf("filtered recall = %+v, want only %s", items, eligible.MemoryID)
	}
}

func TestSemanticRecallIsolatesCompatibilityIdentity(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	memory := createMemoryWithContent(t, repository, "compat-isolation", "automobile compat isolation")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`UPDATE memory_embeddings SET compatibility_identity='other-identity' WHERE memory_id=?`, memory.MemoryID); err != nil {
		t.Fatal(err)
	}
	items := semanticRecallQuery(t, repository, []string{"active"}, false, nil, 10)
	for _, item := range items {
		if item.MemoryID == memory.MemoryID {
			t.Fatalf("mismatched compatibility identity recalled: %+v", item)
		}
	}
}

func TestSemanticRecallIsolatesDimension(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	memory := createMemoryWithContent(t, repository, "dimension-isolation", "automobile dimension isolation")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	threeDim, err := vec.SerializeFloat32([]float32{1, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`UPDATE memory_embeddings SET dimension=3, vector_blob=? WHERE memory_id=?`, threeDim, memory.MemoryID); err != nil {
		t.Fatal(err)
	}
	items := semanticRecallQuery(t, repository, []string{"active"}, false, nil, 10)
	for _, item := range items {
		if item.MemoryID == memory.MemoryID {
			t.Fatalf("mismatched dimension recalled: %+v", item)
		}
	}
}

func TestSearchIsConsistentDuringRebuild(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	for i := 0; i < 5; i++ {
		createMemoryWithContent(t, repository, "consistent-"+string(rune('a'+i)), "automobile consistent content")
	}
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = repository.RebuildSemantic(context.Background())
	}()
	for i := 0; i < 20; i++ {
		result, err := repository.SearchMemories(context.Background(), semanticParams("car"))
		if err != nil {
			t.Fatalf("search during rebuild: %v", err)
		}
		if len(result.Results) != 5 {
			t.Fatalf("search during rebuild returned %d results, want 5", len(result.Results))
		}
	}
	<-done
	status, err := repository.SemanticStatus(context.Background())
	if err != nil || status.State != "ready" {
		t.Fatalf("status after concurrent rebuild = %+v, %v", status, err)
	}
}

func TestSemanticCorpusGenerationAdvancesWithLiveMembership(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	memory := createMemoryWithContent(t, repository, "live-generation", "automobile generation one")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil || status.ActiveVersion != 1 || status.State != "ready" {
		t.Fatalf("initial semantic generation = %+v, %v", status, err)
	}

	params := ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "automobile generation two", Evidence: directEvidence("event-1"), Reason: "generation test"}
	memory, _, _, err = repository.ReviseMemory(context.Background(), memoryIdentity("live-generation-revise", "/v1/memories/{memory_id}", params), memory.MemoryID, params)
	if err != nil {
		t.Fatal(err)
	}
	status, err = repository.SemanticStatus(context.Background())
	if err != nil || status.ActiveVersion != 2 || status.State != "stale" {
		t.Fatalf("projection semantic generation = %+v, %v", status, err)
	}
	if err := repository.EmbedMemory(context.Background(), memory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	status, err = repository.SemanticStatus(context.Background())
	if err != nil || status.ActiveVersion != 3 || status.State != "stale" {
		t.Fatalf("embedding semantic generation = %+v, %v", status, err)
	}
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = repository.SemanticStatus(context.Background())
	if err != nil || status.ActiveVersion != 4 || status.State != "ready" {
		t.Fatalf("published semantic generation = %+v, %v", status, err)
	}
}

// fixedDimensionEmbeddingProvider returns "automobile" vectors in a requested
// dimension so a dimension-only configuration change can be exercised end to
// end.
type fixedDimensionEmbeddingProvider struct{ dimension int }

func (p fixedDimensionEmbeddingProvider) Embed(_ context.Context, request provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	vectors := make([][]float32, len(request.Inputs()))
	for i, input := range request.Inputs() {
		vector := make([]float32, p.dimension)
		if p.dimension > 1 && (strings.Contains(input, "automobile") || strings.Contains(input, "car")) {
			vector[0] = 1
		} else if p.dimension > 0 {
			vector[p.dimension-1] = 1
		}
		vectors[i] = vector
	}
	return provider.NewEmbeddingResult(vectors, "dimension-test"), nil
}

// TestSemanticDimensionChangeInvalidatesVectorsAndRebuilds proves that a change
// to the requested output dimension alone is a vector-space change: the old
// vectors are neither queryable nor reusable, durable embedding work is
// re-bound to the new identity, and recall degrades instead of returning
// mismatched vectors until the rebuild lands.
func TestSemanticDimensionChangeInvalidatesVectorsAndRebuilds(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, fixedDimensionEmbeddingProvider{dimension: 2})
	memory := createMemoryWithContent(t, repository, "dimension-change", "automobile dimension change")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := repository.emb()
	beforeIdentity := repository.EmbeddingJobIdentity()
	activeRebuilds := func(identity string) int {
		t.Helper()
		var active int
		if err := repository.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='rebuild_semantic' AND processing_version=? AND status IN ('pending','waiting','running','retrying')`, identity).Scan(&active); err != nil {
			t.Fatal(err)
		}
		return active
	}
	completeRebuilds := activeRebuilds(beforeIdentity)
	if err := repository.EnsureSemanticRecoveryJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if activeRebuilds(beforeIdentity) != completeRebuilds {
		t.Fatalf("complete vector space scheduled extra rebuilds: %d -> %d", completeRebuilds, activeRebuilds(beforeIdentity))
	}

	repository.ConfigureSemantic(fixedDimensionEmbeddingProvider{dimension: 3}, "test-provider", "test-model", 3)
	afterIdentity := repository.EmbeddingJobIdentity()
	if afterIdentity == beforeIdentity || repository.emb().CompatibilityIdentity == before.CompatibilityIdentity {
		t.Fatalf("dimension change did not change the vector space: %q", afterIdentity)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if repository.semanticQueryable(status, 0) {
		t.Fatal("superseded vector space is still queryable")
	}
	degraded, err := repository.SearchMemories(context.Background(), hybridParams("automobile"))
	if err != nil || !reflect.DeepEqual(degraded.ExecutedChannels, []string{"keyword", "graph"}) || !degraded.Degraded {
		t.Fatalf("recall during dimension change = %+v, %v", degraded, err)
	}
	for _, result := range degraded.Results {
		if result.SemanticRawScore != nil {
			t.Fatalf("degraded recall reported a semantic score: %+v", result)
		}
	}

	if err := repository.EnsureSemanticRecoveryJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	var embeddings, rebuilds int
	if err := repository.store.reader.QueryRow(`SELECT sum(job_type='embed_memory'), sum(job_type='rebuild_semantic') FROM jobs WHERE processing_version=? AND status IN ('pending','waiting','running','retrying')`, afterIdentity).Scan(&embeddings, &rebuilds); err != nil {
		t.Fatal(err)
	}
	if embeddings != 1 || rebuilds != 1 {
		t.Fatalf("replacement jobs for the new dimension = embeddings %d rebuilds %d", embeddings, rebuilds)
	}
	var staleJobs int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type IN ('embed_memory','rebuild_semantic') AND processing_version=? AND status IN ('pending','waiting','running','retrying')`, beforeIdentity).Scan(&staleJobs); err != nil {
		t.Fatal(err)
	}
	if staleJobs != 0 {
		t.Fatalf("jobs for the superseded vector space are still active: %d", staleJobs)
	}
	readTx, err := repository.store.reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	dirty, dirtyErr := repository.semanticIndexDirty(context.Background(), readTx)
	_ = readTx.Rollback()
	if dirtyErr != nil || !dirty {
		t.Fatalf("semantic index dirty after dimension change = %t, %v", dirty, dirtyErr)
	}

	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = repository.SemanticStatus(context.Background())
	if err != nil || status.State != "ready" || status.Dimension != 3 || status.CompatibilityIdentity != repository.emb().CompatibilityIdentity {
		t.Fatalf("rebuilt dimension state = %+v, %v", status, err)
	}
	identity, err := repository.CurrentEmbeddingIdentity(context.Background(), memory.MemoryID)
	if err != nil || identity.Dimension != 3 {
		t.Fatalf("rebuilt embedding identity = %+v, %v", identity, err)
	}
	if _, err := repository.SearchMemories(context.Background(), semanticParams("automobile")); err != nil {
		t.Fatalf("semantic recall after dimension rebuild: %v", err)
	}
}

func TestSemanticQueryableRequiresCurrentIdentityPolicyAndDimension(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMemoryWithContent(t, repository, "queryable-identity", "automobile queryable")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.CompatibilityIdentity != testVectorSpaceIdentity() || !repository.semanticQueryable(status, status.Dimension) {
		t.Fatalf("published identity is not queryable: %+v", status)
	}

	checks := []SemanticStatus{status, status, status, status, status}
	checks[0].Provider = "other-provider"
	checks[1].Model = "other-model"
	checks[2].CompatibilityIdentity = "other-compatibility"
	checks[3].PolicyGeneration++
	checks[4].State = "degraded"
	for _, candidate := range checks {
		if repository.semanticQueryable(candidate, status.Dimension) {
			t.Fatalf("mismatched semantic identity is queryable: %+v", candidate)
		}
	}
	if repository.semanticQueryable(status, status.Dimension+1) {
		t.Fatal("mismatched query dimension is queryable")
	}
}

func TestHybridFallsBackWhenSemanticIdentityDoesNotMatchRuntime(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	createMemoryWithContent(t, repository, "identity-fallback", "identityfallback automobile")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	repository.SetEmbedding(provider, "other-provider", "other-model", "other-compatibility", "other-configuration", 1, 0)

	if _, err := repository.SearchMemories(context.Background(), semanticParams("identityfallback")); !errors.Is(err, ErrIndexDegraded) {
		t.Fatalf("semantic identity mismatch error = %v", err)
	}
	result, err := repository.SearchMemories(context.Background(), hybridParams("identityfallback"))
	if err != nil || !reflect.DeepEqual(result.ExecutedChannels, []string{"keyword", "graph"}) || !result.Degraded || len(result.Results) != 1 {
		t.Fatalf("identity mismatch fallback = %+v, %v", result, err)
	}
}
