package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/provider"
)

type semanticTestProvider struct {
	malformed bool
	err       error
	after     func()
	requests  []provider.EmbeddingRequest
}

func (p *semanticTestProvider) Embed(_ context.Context, request provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	p.requests = append(p.requests, request)
	if p.err != nil {
		return provider.EmbeddingResult{}, p.err
	}
	result := make([][]float32, len(request.Inputs()))
	for i, input := range request.Inputs() {
		if p.malformed {
			result[i] = []float32{float32(math.NaN()), 0}
		} else if strings.Contains(input, "automobile") || strings.Contains(input, "car") || strings.Contains(input, "TOKEN42") || strings.Contains(input, "scopeword") {
			result[i] = []float32{1, 0}
		} else {
			result[i] = []float32{0, 1}
		}
	}
	if p.after != nil {
		p.after()
	}
	return provider.NewEmbeddingResult(result, "semantic-test"), nil
}

func TestEmbedMemoryUsesAuthoritativeClassification(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	embedding := &semanticTestProvider{}
	configureSemanticTest(t, repository, embedding)
	memory := createTestMemory(t, repository, "classified-embedding")

	if _, err := repository.store.writer.Exec(`UPDATE memories SET sensitivity='sensitive' WHERE memory_id=?`, memory.MemoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`UPDATE raw_events SET source_type='tool_result' WHERE event_id='event-1'`); err != nil {
		t.Fatal(err)
	}
	if err := repository.EmbedMemory(context.Background(), memory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	if len(embedding.requests) != 1 || embedding.requests[0].Sensitivity() != "sensitive" || len(embedding.requests[0].SourceTypes()) != 1 || embedding.requests[0].SourceTypes()[0] != "tool_result" {
		t.Fatalf("embedding request classification = sensitivity %q, sources %v", embedding.requests[0].Sensitivity(), embedding.requests[0].SourceTypes())
	}
}

func TestEmbedMemoryRejectsStaleCapturedEmbeddingSnapshot(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	oldProvider := &semanticTestProvider{}
	configureSemanticTest(t, repository, oldProvider)
	repository.SetEmbedding(oldProvider, "provider-old", "model-old", "compat-old", "config-old", 1, 0)
	oldProvider.after = func() {
		repository.SetEmbedding(&semanticTestProvider{}, "provider-new", "model-new", "compat-new", "config-new", 2, 0)
	}
	memory := createTestMemory(t, repository, "captured-embedding")

	if err := repository.EmbedMemory(context.Background(), memory.CurrentRevision.RevisionID); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("EmbedMemory() error = %v, want ErrStaleGeneration", err)
	}
	var embeddings int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM memory_embeddings WHERE memory_id=?`, memory.MemoryID).Scan(&embeddings); err != nil {
		t.Fatal(err)
	}
	if embeddings != 0 {
		t.Fatalf("stale embedding published %d rows", embeddings)
	}
}

func TestEmbedMemoryWithoutEvidenceUsesExplicitManualClassification(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	embedding := &semanticTestProvider{}
	configureSemanticTest(t, repository, embedding)
	memory := createTestMemory(t, repository, "manual-no-evidence")
	revisionID := "manual-no-evidence-revision"
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_revisions(revision_id,memory_id,parent_revision_id,kind,display_content,canonical_content,canonical_hash,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES (?,?,?,'fact','initial searchable memory','initial searchable memory','manual-no-evidence-hash','normal',0,0,'test','test-v1','2026-08-21T12:00:00Z'); UPDATE memories SET current_revision_id=? WHERE memory_id=?; UPDATE memory_projections SET revision_id=?,content_hash='manual-no-evidence-hash' WHERE memory_id=?; UPDATE memory_fts SET revision_id=? WHERE memory_id=?`, revisionID, memory.MemoryID, memory.CurrentRevision.RevisionID, revisionID, memory.MemoryID, revisionID, memory.MemoryID, revisionID, memory.MemoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO embedding_eligibility(memory_id,revision_id,space_id,configuration_generation,policy_generation,eligible,reason_code,evaluated_at) VALUES (?,?,?,?,?,?,?,?)`, memory.MemoryID, revisionID, "space-1", 0, 0, 1, "test", formatTime(repository.runtime.Now())); err != nil {
		t.Fatal(err)
	}
	if err := repository.EmbedMemory(context.Background(), revisionID); err != nil {
		t.Fatal(err)
	}
	if len(embedding.requests) != 1 || len(embedding.requests[0].SourceTypes()) != 1 || embedding.requests[0].SourceTypes()[0] != "manual" {
		t.Fatalf("source types = %v", embedding.requests[0].SourceTypes())
	}
}

func TestEmbedMemoryPolicyRejectsAuthoritativeClassificationBeforeHTTP(t *testing.T) {
	for _, test := range []struct {
		name        string
		sensitivity string
		sourceType  string
	}{
		{"sensitive", "sensitive", "manual"},
		{"denied source", "normal", "tool_result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, _ := newMemoryTestRepository(t)
			memory := createTestMemory(t, repository, "policy-"+test.name)
			if _, err := repository.store.writer.Exec(`UPDATE memories SET sensitivity=? WHERE memory_id=?`, test.sensitivity, memory.MemoryID); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.store.writer.Exec(`UPDATE raw_events SET source_type=? WHERE event_id='event-1'`, test.sourceType); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer server.Close()
			t.Setenv("DMEM_TEST_EMBED_SECRET", "")
			client, err := provider.New(provider.Config{
				Enabled: true, Kind: provider.KindOpenAICompatible, Endpoint: server.URL, Model: "test-model",
				Secret:            provider.SecretReference{Source: "environment", Name: "DMEM_TEST_EMBED_SECRET"},
				TelemetryDisabled: true,
				Limits: provider.Limits{MaxEgressInputChars: 1000, MaxContextItems: 1, MaxResponseBytes: 4096,
					RequestTimeout: time.Second, MaxConcurrent: 1, RequestsPerMinute: 60},
				Policy: provider.Policy{Default: "deny", ContentTypes: []string{"text/plain"}, SourceTypes: []string{"manual"}, Sensitivities: []string{"normal"},
					Assignments: []provider.PolicyAssignment{{Selector: "space", SpaceID: "space-1", Effect: "allow"}}},
			}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			repository.ConfigureSemantic(client, client.Identity(), client.ModelIdentity())
			if err := repository.EmbedMemory(context.Background(), memory.CurrentRevision.RevisionID); !errors.Is(err, ErrEmbeddingPolicy) {
				t.Fatalf("EmbedMemory() error = %v", err)
			}
			if requests.Load() != 0 {
				t.Fatalf("HTTP requests = %d", requests.Load())
			}
		})
	}
}

func configureSemanticTest(t *testing.T, repository *Repository, embedding provider.EmbeddingProvider) {
	t.Helper()
	repository.ConfigureSemantic(embedding, "test-provider", "test-model")
}

func TestSemanticHealthDistinguishesConfigurationAndActiveFallback(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	if state, version := repository.SemanticHealth(context.Background()); state != "not_configured" || version != 0 {
		t.Fatalf("unconfigured health = %q/%d", state, version)
	}
	configureSemanticTest(t, repository, &semanticTestProvider{})
	if state, version := repository.SemanticHealth(context.Background()); state != "unavailable" || version != 0 {
		t.Fatalf("empty configured health = %q/%d", state, version)
	}
}

func TestSemanticRecoveryJobsAreProviderBoundDeduplicatedAndOrdered(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	memory := createTestMemory(t, repository, "semantic-recovery")
	if _, err := repository.store.writer.Exec(`UPDATE jobs SET status='cancelled', finished_at=? WHERE job_type='extract_memory' AND status IN ('pending','waiting','retrying')`, formatTime(repository.runtime.Now())); err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureSemanticRecoveryJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureSemanticRecoveryJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	var embeddings, rebuilds int
	if err := repository.store.reader.QueryRow(`SELECT sum(job_type='embed_memory'), sum(job_type='rebuild_semantic') FROM jobs WHERE processing_version='test-provider/test-model' AND status IN ('pending','waiting','running','retrying')`).Scan(&embeddings, &rebuilds); err != nil {
		t.Fatal(err)
	}
	if embeddings != 1 || rebuilds != 1 {
		t.Fatalf("active recovery jobs = embeddings %d rebuilds %d", embeddings, rebuilds)
	}
	claim, err := repository.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "recovery-worker", LeaseDuration: time.Minute})
	if err != nil || claim.Job.JobType != "embed_memory" || claim.Job.TargetID != memory.CurrentRevision.RevisionID || claim.ProcessingVersion != "test-provider/test-model" {
		t.Fatalf("first recovery claim = %+v, %v", claim, err)
	}
}

func TestSemanticSourceWritesAutomaticallyScheduleAndCoalesceJobs(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMemoryWithContent(t, repository, "auto-schedule-a", "automobile alpha")
	createMemoryWithContent(t, repository, "auto-schedule-b", "automobile beta")

	var embeddings, rebuilds, waitingRebuilds int
	if err := repository.store.reader.QueryRow(`SELECT sum(job_type='embed_memory'), sum(job_type='rebuild_semantic'), sum(job_type='rebuild_semantic' AND status='waiting') FROM jobs WHERE processing_version=? AND status IN ('pending','waiting','running','retrying')`, repository.embeddingIdentity()).Scan(&embeddings, &rebuilds, &waitingRebuilds); err != nil {
		t.Fatal(err)
	}
	if embeddings != 2 || rebuilds != 1 || waitingRebuilds != 1 {
		t.Fatalf("automatic semantic jobs = embeddings %d rebuilds %d waiting %d", embeddings, rebuilds, waitingRebuilds)
	}
}

func TestSemanticLifecycleWithoutSourceChangePreservesEmbeddingAndJobs(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	memory := createMemoryWithContent(t, repository, "semantic-lifecycle", "automobile lifecycle")
	if err := repository.EmbedMemory(context.Background(), memory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`UPDATE jobs SET status='cancelled', finished_at=? WHERE status IN ('pending','waiting','retrying')`, formatTime(repository.runtime.Now())); err != nil {
		t.Fatal(err)
	}

	params := ChangeMemoryStateParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Action: "contest", Reason: "lifecycle only"}
	if _, _, err := repository.ChangeMemoryState(context.Background(), memoryIdentity("semantic-lifecycle-contest", "/v1/memories/{memory_id}/state", params), memory.MemoryID, params); err != nil {
		t.Fatal(err)
	}
	var embeddingState, metadataState string
	var activeJobs int
	if err := repository.store.reader.QueryRow(`SELECT p.embedding_state, m.state, (SELECT count(*) FROM jobs WHERE job_type IN ('embed_memory','rebuild_semantic') AND status IN ('pending','waiting','running','retrying')) FROM memory_projections p CROSS JOIN semantic_index_metadata m WHERE p.memory_id=? AND m.singleton=1`, memory.MemoryID).Scan(&embeddingState, &metadataState, &activeJobs); err != nil {
		t.Fatal(err)
	}
	if embeddingState != "current" || metadataState != "ready" || activeJobs != 0 {
		t.Fatalf("lifecycle semantic state = embedding %q metadata %q active jobs %d", embeddingState, metadataState, activeJobs)
	}
}

func TestAutomaticSemanticJobsMakeMemorySearchable(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	if _, err := repository.store.writer.Exec(`UPDATE jobs SET status='cancelled', finished_at=? WHERE job_type='extract_memory' AND status IN ('pending','waiting','retrying')`, formatTime(repository.runtime.Now())); err != nil {
		t.Fatal(err)
	}
	memory := createMemoryWithContent(t, repository, "semantic-automatic-search", "automobile automatic")
	drainSemanticJobs(t, repository)

	result, err := repository.SearchMemories(context.Background(), semanticParams("car"))
	if err != nil || len(result.Results) != 1 || result.Results[0].Memory.MemoryID != memory.MemoryID {
		t.Fatalf("automatic semantic search = %+v, %v", result, err)
	}
}

func TestSemanticRebuildCompletionSchedulesSuccessorForBoundaryWrite(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	memory := createMemoryWithContent(t, repository, "semantic-completion-boundary", "automobile before")
	if err := repository.EmbedMemory(context.Background(), memory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	claim := claimIndexJob(t, repository, "semantic-boundary-rebuild", "rebuild_semantic", "index", semanticIndexName, time.Minute)
	if err := repository.RebuildSemanticClaimed(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	params := ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "automobile after", Evidence: directEvidence("event-1"), Reason: "completion boundary"}
	if _, _, err := repository.ReviseMemory(context.Background(), memoryIdentity("semantic-completion-write", "/v1/memories/{memory_id}", params), memory.MemoryID, params); err != nil {
		t.Fatal(err)
	}
	var rebuilds int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='rebuild_semantic' AND processing_version=? AND status IN ('pending','waiting','running','retrying')`, repository.embeddingIdentity()).Scan(&rebuilds); err != nil {
		t.Fatal(err)
	}
	if rebuilds != 1 {
		t.Fatalf("rebuilds before completion = %d; want running rebuild only", rebuilds)
	}
	if err := repository.CompleteJob(context.Background(), claim, JobResult{Code: "rebuilt"}); err != nil {
		t.Fatal(err)
	}
	var waiting, succeeded int
	if err := repository.store.reader.QueryRow(`SELECT sum(status='waiting'), sum(status='succeeded') FROM jobs WHERE job_type='rebuild_semantic' AND processing_version=?`, repository.embeddingIdentity()).Scan(&waiting, &succeeded); err != nil {
		t.Fatal(err)
	}
	if waiting != 1 || succeeded != 1 {
		t.Fatalf("boundary rebuild jobs = waiting %d succeeded %d", waiting, succeeded)
	}
	stale := claim
	stale.LeaseGeneration++
	if err := repository.CompleteJob(context.Background(), stale, JobResult{Code: "late"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale completion error = %v", err)
	}
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='rebuild_semantic' AND processing_version=? AND status IN ('pending','waiting','running','retrying')`, repository.embeddingIdentity()).Scan(&rebuilds); err != nil {
		t.Fatal(err)
	}
	if rebuilds != 1 {
		t.Fatalf("active rebuilds after stale completion = %d", rebuilds)
	}
}

func drainSemanticJobs(t *testing.T, repository *Repository) {
	t.Helper()
	for {
		if _, err := repository.PromoteWaitingJobs(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		claim, err := repository.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "semantic-test-worker", LeaseDuration: time.Minute})
		if errors.Is(err, ErrNotFound) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		switch claim.Job.JobType {
		case "embed_memory":
			err = repository.EmbedMemoryClaimed(context.Background(), claim, claim.Job.TargetID)
		case "rebuild_semantic":
			err = repository.RebuildSemanticClaimed(context.Background(), claim)
		default:
			t.Fatalf("unexpected job type %q", claim.Job.JobType)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.CompleteJob(context.Background(), claim, JobResult{Code: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSemanticRebuildSearchAndAuthoritativeExclusion(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	memory := createTestMemory(t, repository, "semantic-create")
	params := ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "automobile handbook", Evidence: directEvidence("event-1"), Reason: "semantic content"}
	memory, _, err := repository.ReviseMemory(context.Background(), memoryIdentity("semantic-revise", "/v1/memories/{memory_id}", params), memory.MemoryID, params)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil || status.State != "ready" || status.ActiveVersion != 1 || status.SourceRevisionCount != 1 {
		t.Fatalf("semantic status = %+v, %v", status, err)
	}
	result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "car", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"semantic"}, Limit: 10, MaxChars: 1000})
	if err != nil || len(result.Results) != 1 || result.Results[0].Memory.MemoryID != memory.MemoryID || result.Results[0].SelectionReason != "semantic_match" {
		t.Fatalf("semantic result = %+v, %v", result, err)
	}
	for metadataState, healthState := range map[string]string{"building": "rebuilding", "degraded": "degraded"} {
		if _, err := repository.store.writer.Exec(`UPDATE semantic_index_metadata SET state=? WHERE singleton=1`, metadataState); err != nil {
			t.Fatal(err)
		}
		if state, version := repository.SemanticHealth(context.Background()); state != healthState || version != 1 {
			t.Fatalf("%s health with active version = %q/%d", metadataState, state, version)
		}
		result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "car", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"semantic"}, Limit: 10, MaxChars: 1000})
		if metadataState == "building" {
			if err != nil || len(result.Results) != 1 {
				t.Fatalf("building live-corpus query = %+v, %v", result, err)
			}
		} else if !errors.Is(err, ErrIndexDegraded) {
			t.Fatalf("degraded semantic query error = %v", err)
		}
	}
	if _, err := repository.store.writer.Exec(`UPDATE semantic_index_metadata SET state='ready' WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	archive := ChangeMemoryStateParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Action: "archive", Reason: "test"}
	if _, _, err := repository.ChangeMemoryState(context.Background(), memoryIdentity("semantic-archive", "/v1/memories/{memory_id}/state", archive), memory.MemoryID, archive); err != nil {
		t.Fatal(err)
	}
	result, err = repository.SearchMemories(context.Background(), SearchParams{Query: "car", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"semantic"}, Limit: 10, MaxChars: 1000})
	if err != nil || len(result.Results) != 0 {
		t.Fatalf("archived semantic result = %+v, %v", result, err)
	}
}

func TestSemanticRejectsMalformedEmbedding(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{malformed: true})
	memory := createTestMemory(t, repository, "malformed-semantic")
	if err := repository.EmbedMemory(context.Background(), memory.CurrentRevision.RevisionID); err == nil {
		t.Fatal("malformed embedding was accepted")
	}
	if _, err := repository.CurrentEmbeddingIdentity(context.Background(), memory.MemoryID); err != ErrNotFound {
		t.Fatalf("current embedding after malformed output = %v", err)
	}
}

func TestClaimedEmbeddingDoesNotPublishAfterLeaseExpiry(t *testing.T) {
	repository, clock := newMemoryTestRepository(t)
	provider := &semanticTestProvider{after: func() { clock.now = clock.now.Add(2 * time.Minute) }}
	configureSemanticTest(t, repository, provider)
	memory := createTestMemory(t, repository, "expired-embedding")
	claim := claimIndexJob(t, repository, "embed-claim", "embed_memory", "memory_revision", memory.CurrentRevision.RevisionID, time.Minute)

	if err := repository.EmbedMemoryClaimed(context.Background(), claim, memory.CurrentRevision.RevisionID); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("EmbedMemoryClaimed() error = %v", err)
	}
	var embeddings, current int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memory_embeddings WHERE revision_id=?),(SELECT count(*) FROM memory_projections WHERE revision_id=? AND embedding_state='current')`, memory.CurrentRevision.RevisionID, memory.CurrentRevision.RevisionID).Scan(&embeddings, &current); err != nil {
		t.Fatal(err)
	}
	if embeddings != 0 || current != 0 {
		t.Fatalf("late embedding publication = embeddings %d, current projections %d", embeddings, current)
	}
}

func TestSemanticFailedRebuildPreservesUnchangedLiveGeneration(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	memory := createTestMemory(t, repository, "semantic-preserve")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.malformed = true
	if err := repository.RebuildSemantic(context.Background()); err == nil {
		t.Fatal("malformed rebuild succeeded")
	}
	provider.malformed = false
	status, err := repository.SemanticStatus(context.Background())
	if err != nil || status.ActiveVersion != 1 || status.State != "stale" {
		t.Fatalf("status after failed rebuild = %+v, %v", status, err)
	}
	result, err := repository.SearchMemories(context.Background(), semanticParams("initial"))
	if err != nil || len(result.Results) != 1 || result.Results[0].Memory.MemoryID != memory.MemoryID {
		t.Fatalf("search through unchanged live corpus = %+v, %v", result, err)
	}
}

func TestSemanticRebuildRecoversDegradedMetadata(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	memory := createTestMemory(t, repository, "semantic-restore")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`UPDATE semantic_index_metadata SET state='degraded', building_version=NULL WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := repository.SearchMemories(context.Background(), semanticParams("initial"))
	if err != nil || len(result.Results) != 1 || result.Results[0].Memory.MemoryID != memory.MemoryID || result.SemanticIndexVersion != 2 {
		t.Fatalf("rebuilt search = %+v, %v", result, err)
	}
}

func TestSemanticRebuildRejectsConcurrentRevisionBeforeActivation(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	memory := createTestMemory(t, repository, "semantic-concurrent")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	repository.semanticBuildHook = func() {
		params := ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "concurrent automobile revision", Evidence: directEvidence("event-1"), Reason: "race"}
		var err error
		memory, _, err = repository.ReviseMemory(context.Background(), memoryIdentity("semantic-race", "/v1/memories/{memory_id}", params), memory.MemoryID, params)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.RebuildSemantic(context.Background()); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("concurrent rebuild error = %v", err)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil || status.ActiveVersion != 2 || status.State != "stale" {
		t.Fatalf("status after concurrent revision = %+v, %v", status, err)
	}
	result, err := repository.SearchMemories(context.Background(), semanticParams("car"))
	if err != nil || len(result.Results) != 0 {
		t.Fatalf("stale active vector result = %+v, %v", result, err)
	}
}

func TestClaimedSemanticRebuildDoesNotActivateAfterLeaseExpiry(t *testing.T) {
	repository, clock := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createTestMemory(t, repository, "semantic-lease-base")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}

	repository.semanticBuildHook = func() { clock.now = clock.now.Add(2 * time.Minute) }
	claim := claimIndexJob(t, repository, "semantic-claim", "rebuild_semantic", "index", semanticIndexName, time.Minute)
	if err := repository.RebuildSemanticClaimed(context.Background(), claim); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("RebuildSemanticClaimed() error = %v", err)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveVersion != 1 {
		t.Fatalf("late activation = metadata %d", status.ActiveVersion)
	}
}

func TestFirstClaimedSemanticBuildRollsBackActivationAfterLeaseExpiry(t *testing.T) {
	repository, clock := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	memory := createTestMemory(t, repository, "semantic-first-lease")
	if err := repository.EmbedMemory(context.Background(), memory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	repository.semanticActivateHook = func() { clock.now = clock.now.Add(2 * time.Minute) }
	claim := claimIndexJob(t, repository, "semantic-first-claim", "rebuild_semantic", "index", semanticIndexName, time.Minute)

	if err := repository.RebuildSemanticClaimed(context.Background(), claim); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("RebuildSemanticClaimed() error = %v", err)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveVersion != 0 {
		t.Fatalf("late first activation = metadata %d", status.ActiveVersion)
	}
}

func claimIndexJob(t *testing.T, repository *Repository, id, jobType, targetType, targetID string, lease time.Duration) ClaimedJob {
	t.Helper()
	now := formatTime(repository.runtime.Now())
	if _, err := repository.store.writer.Exec(`DELETE FROM jobs WHERE status IN ('pending','waiting','retrying')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) VALUES (?,?,?,?,?,?,'pending',1001,3,?,?)`, id, "operation-"+id, jobType, targetType, targetID, repository.embeddingIdentity(), now, now); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "index-worker", LeaseDuration: lease})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Job.JobID != id {
		t.Fatalf("claimed job = %q; want %q", claim.Job.JobID, id)
	}
	return claim
}

func TestHybridUnionDedupAndAuthoritativeVectorFiltering(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	dual := createMemoryWithContent(t, repository, "hybrid-dual", "automobile TOKEN42")
	semanticOnly := createMemoryWithContent(t, repository, "hybrid-semantic", "automobile handbook")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	keywordOnly := createMemoryWithContent(t, repository, "hybrid-keyword", "TOKEN42 exact identifier")
	result, err := repository.SearchMemories(context.Background(), hybridParams("TOKEN42"))
	if err != nil {
		t.Fatal(err)
	}
	byID := searchResultsByID(result)
	if len(byID) != 3 {
		t.Fatalf("hybrid union IDs = %v", resultIDs(result))
	}
	assertChannels(t, byID[keywordOnly.MemoryID].MatchedChannels, "keyword")
	assertChannels(t, byID[semanticOnly.MemoryID].MatchedChannels, "semantic")
	assertChannels(t, byID[dual.MemoryID].MatchedChannels, "keyword", "semantic")

	if _, err := repository.store.writer.Exec(`UPDATE memory_embeddings SET content_hash='stale-hash' WHERE memory_id=?`, semanticOnly.MemoryID); err != nil {
		t.Fatal(err)
	}
	result, err = repository.SearchMemories(context.Background(), semanticParams("car"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range result.Results {
		if item.Memory.MemoryID == semanticOnly.MemoryID {
			t.Fatalf("stale content hash vector returned: %+v", item)
		}
	}
}

func TestSemanticEligibilityAppliedBeforeBoundedVectorRecall(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	repository.ConfigureSemantic(recallVectorProvider{}, "test-provider", "test-model")
	eligible := createMemoryWithContent(t, repository, "bounded-eligible", "automobile eligible content")
	ineligible := createMemoryWithContent(t, repository, "bounded-ineligible", "automobile ineligible content")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	setEmbeddingEligibility(t, repository, eligible, 1, true)
	setEmbeddingEligibility(t, repository, ineligible, 1, false)
	repository.SetEmbedding(repository.emb().Provider, "test-provider", "test-model", EmbeddingCompatibilityIdentity("test-provider/test-model"), "test-provider/test-model", 1, 1)
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}

	params := semanticParams("car")
	params.Limit = 1
	result, err := repository.SearchMemories(context.Background(), params)
	if err != nil || len(result.Results) != 1 || result.Results[0].Memory.MemoryID != eligible.MemoryID {
		t.Fatalf("bounded eligible semantic result = %+v, %v", result, err)
	}
}

type recallVectorProvider struct{}

func (recallVectorProvider) Embed(_ context.Context, request provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	vectors := make([][]float32, len(request.Inputs()))
	for i, input := range request.Inputs() {
		switch {
		case strings.Contains(input, "ineligible"):
			vectors[i] = []float32{1, 0}
		case strings.Contains(input, "eligible"):
			vectors[i] = []float32{1, 0.5}
		default:
			vectors[i] = []float32{1, 0}
		}
	}
	return provider.NewEmbeddingResult(vectors, "recall"), nil
}

func TestHybridKeywordEligibilityAppliedBeforeBoundedRecall(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	ineligible := createMemoryWithContent(t, repository, "bounded-keyword-ineligible", strings.Repeat("boundedword ", 20))
	eligible := createMemoryWithContent(t, repository, "bounded-keyword-eligible", "boundedword")
	if _, err := repository.store.writer.Exec(`UPDATE memory_projections SET memory_type='decision' WHERE memory_id=?`, ineligible.MemoryID); err != nil {
		t.Fatal(err)
	}
	params := hybridParams("boundedword")
	params.MemoryTypesSet, params.MemoryTypes = true, []string{"fact"}
	params.ReferenceTime = formatTime(repository.runtime.Now())
	tx, err := repository.store.reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := repository.keywordRecall(context.Background(), tx, params, []string{"space-1"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].memoryID != eligible.MemoryID {
		t.Fatalf("bounded eligible keyword result = %+v", result)
	}
}

func TestSemanticQueryEmbeddingHoldsNoSQLiteReaderTransaction(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	var readers atomic.Int32
	provider := &semanticTestProvider{after: func() { readers.Store(int32(repository.store.reader.Stats().InUse)) }}
	configureSemanticTest(t, repository, provider)
	createMemoryWithContent(t, repository, "embed-reader", "automobile reader transaction")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	readers.Store(-1)
	result, err := repository.SearchMemories(context.Background(), semanticParams("car"))
	if err != nil || len(result.Results) != 1 {
		t.Fatalf("semantic search = %+v, %v", result, err)
	}
	if readers.Load() != 0 {
		t.Fatalf("SQLite readers in use during Embed = %d", readers.Load())
	}
}

func TestSearchModesExcludeSiblingSpaces(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES ('space-local', 'space-1', 'project', 'local', 'Local', '2026-08-21T11:00:00Z'), ('space-sibling', 'space-1', 'project', 'sibling', 'Sibling', '2026-08-21T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	inScope := createMemoryInSpace(t, repository, "scope-local", "space-local", "automobile scopeword")
	createMemoryInSpace(t, repository, "scope-sibling", "space-sibling", "automobile scopeword")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, channels := range [][]string{{"keyword"}, {"semantic"}, {"keyword", "semantic", "graph"}} {
		params := hybridParams("scopeword")
		params.Channels = channels
		params.SpaceIDs = []string{"space-local"}
		params.IncludeAncestors = false
		result, err := repository.SearchMemories(context.Background(), params)
		if err != nil || len(result.Results) != 1 || result.Results[0].Memory.MemoryID != inScope.MemoryID {
			t.Fatalf("%s sibling isolation = %+v, %v", strings.Join(channels, "+"), result, err)
		}
	}
}

func TestHybridFallbacksAndNeitherChannelAvailable(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	createMemoryWithContent(t, repository, "fallback", "automobile fallbackword")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}

	provider.err = errors.New("provider unavailable")
	result, err := repository.SearchMemories(context.Background(), hybridParams("fallbackword"))
	if err != nil || !reflect.DeepEqual(result.ExecutedChannels, []string{"keyword", "graph"}) || !result.Degraded || len(result.Results) != 1 {
		t.Fatalf("semantic-to-keyword fallback = %+v, %v", result, err)
	}
	if _, err := repository.SearchMemories(context.Background(), semanticParams("fallbackword")); !errors.Is(err, ErrIndexDegraded) {
		t.Fatalf("semantic unavailable error = %v", err)
	}
	provider.err, provider.malformed = nil, true
	result, err = repository.SearchMemories(context.Background(), hybridParams("fallbackword"))
	if err != nil || !reflect.DeepEqual(result.ExecutedChannels, []string{"keyword", "graph"}) || !result.Degraded || len(result.Results) != 1 {
		t.Fatalf("malformed semantic fallback = %+v, %v", result, err)
	}
	provider.malformed = false
	provider.err = nil
	if _, err := repository.store.writer.Exec(`UPDATE index_metadata SET state='degraded' WHERE index_name='memory_fts'`); err != nil {
		t.Fatal(err)
	}
	result, err = repository.SearchMemories(context.Background(), hybridParams("car"))
	if err != nil || !reflect.DeepEqual(result.ExecutedChannels, []string{"semantic", "graph"}) || !result.Degraded || len(result.Results) != 1 {
		t.Fatalf("keyword-to-semantic fallback = %+v, %v", result, err)
	}
	repository.setEmbeddingProvider(nil)
	result, err = repository.SearchMemories(context.Background(), hybridParams("fallbackword"))
	if err != nil || !result.Degraded || len(result.Results) != 0 || !contains(result.DegradationReasons, "semantic_unavailable") || !contains(result.DegradationReasons, "keyword_unavailable") || !reflect.DeepEqual(result.ExecutedChannels, []string{"graph"}) {
		t.Fatalf("neither primary channel partial result = %+v, %v", result, err)
	}
}

func TestHybridKeywordFallbackCursorPaginates(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	createMatchingMemories(t, repository, "fallback-page", "fallbackpage", minSelectionWindow+1)
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.err = errors.New("provider unavailable")
	params := hybridParams("fallbackpage")
	params.Limit = 1
	first, err := repository.SearchMemories(context.Background(), params)
	if err != nil || !reflect.DeepEqual(first.ExecutedChannels, []string{"keyword", "graph"}) || first.NextCursor == nil || len(first.Results) != selectionWindow(params.Limit) {
		t.Fatalf("first fallback page = %+v, %v", first, err)
	}
	params.Cursor = *first.NextCursor
	second, err := repository.SearchMemories(context.Background(), params)
	if err != nil || !reflect.DeepEqual(second.ExecutedChannels, []string{"keyword", "graph"}) || len(second.Results) != 1 || second.Results[0].Memory.MemoryID == first.Results[0].Memory.MemoryID {
		t.Fatalf("second fallback page = %+v, %v", second, err)
	}
}

func TestSemanticRebuildPublishesEmptyVersionAfterLastMemoryArchived(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	memory := createMemoryWithContent(t, repository, "empty-index", "automobile empty index")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	archive := ChangeMemoryStateParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Action: "archive", Reason: "empty corpus"}
	if _, _, err := repository.ChangeMemoryState(context.Background(), memoryIdentity("empty-index-archive", "/v1/memories/{memory_id}/state", archive), memory.MemoryID, archive); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil || status.State != "ready" || status.ActiveVersion != 3 || status.SourceRevisionCount != 0 {
		t.Fatalf("empty semantic status = %+v, %v", status, err)
	}
	result, err := repository.SearchMemories(context.Background(), semanticParams("car"))
	if err != nil || len(result.Results) != 0 {
		t.Fatalf("empty semantic results = %+v, %v", result, err)
	}
}

func TestSemanticRebuildUsesOnlyCurrentEligibleCorpus(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	eligible := createMemoryWithContent(t, repository, "eligible-corpus", "automobile eligible corpus")
	excluded := createMemoryWithContent(t, repository, "excluded-corpus", "automobile excluded corpus")
	setEmbeddingEligibility(t, repository, excluded, 0, false)

	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil || status.State != "ready" || status.SourceRevisionCount != 1 {
		t.Fatalf("eligible semantic status = %+v, %v", status, err)
	}
	if len(provider.requests) != 1 || !strings.Contains(provider.requests[0].Inputs()[0], "automobile eligible corpus") || !strings.Contains(provider.requests[0].Inputs()[0], "kind:fact") {
		t.Fatalf("embedding requests = %+v", provider.requests)
	}
	result, err := repository.SearchMemories(context.Background(), semanticParams("car"))
	if err != nil || len(result.Results) != 1 || result.Results[0].Memory.MemoryID != eligible.MemoryID {
		t.Fatalf("eligible semantic results = %+v, %v", result, err)
	}
}

func TestSemanticRebuildAllowsInitiallyEmptyEligibleCorpus(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	memory := createMemoryWithContent(t, repository, "empty-eligible-corpus", "automobile excluded corpus")
	setEmbeddingEligibility(t, repository, memory, 0, false)

	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil || status.State != "ready" || status.ActiveVersion != 1 || status.SourceRevisionCount != 0 {
		t.Fatalf("empty eligible semantic status = %+v, %v", status, err)
	}
}

func TestSemanticEligibilityExpansionReusesCompatibleVectors(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	memory := createMemoryWithContent(t, repository, "eligible-expand", "automobile expansion")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := len(provider.requests)
	setEmbeddingEligibility(t, repository, memory, 1, true)
	repository.SetEmbedding(provider, "test-provider", "test-model", EmbeddingCompatibilityIdentity("test-provider/test-model"), "test-provider/test-model", 1, 1)

	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != requests {
		t.Fatalf("policy expansion re-embedded compatible vector: %d -> %d", requests, len(provider.requests))
	}
}

func TestSemanticEligibilityNarrowingFiltersLiveCorpusImmediately(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	allowed := createMemoryWithContent(t, repository, "eligible-narrow-allowed", "automobile allowed")
	excluded := createMemoryWithContent(t, repository, "eligible-narrow-excluded", "automobile excluded")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	setEmbeddingEligibility(t, repository, allowed, 1, true)
	setEmbeddingEligibility(t, repository, excluded, 1, false)
	repository.SetEmbedding(provider, "test-provider", "test-model", EmbeddingCompatibilityIdentity("test-provider/test-model"), "test-provider/test-model", 1, 1)

	if _, err := repository.SearchMemories(context.Background(), semanticParams("car")); !errors.Is(err, ErrIndexDegraded) {
		t.Fatalf("semantic query before policy generation publish = %v", err)
	}
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := repository.SearchMemories(context.Background(), semanticParams("car"))
	if err != nil || len(result.Results) != 1 || result.Results[0].Memory.MemoryID != allowed.MemoryID {
		t.Fatalf("narrowed semantic results = %+v, %v", result, err)
	}
}

func TestSemanticCompatibilityChangeRebuildsEligibleCorpus(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	memory := createMemoryWithContent(t, repository, "eligible-compatible", "automobile compatibility")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	repository.SetEmbedding(provider, "test-provider-v2", "test-model-v2", "compat-v2", "config-v2", 2, 0)

	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity, err := repository.CurrentEmbeddingIdentity(context.Background(), memory.MemoryID)
	if err != nil || identity.Provider != "test-provider-v2" || identity.Model != "test-model-v2" {
		t.Fatalf("compatible embedding identity = %+v, %v", identity, err)
	}
	status, err := repository.SemanticStatus(context.Background())
	if err != nil || status.ActiveVersion != 3 || status.Provider != "test-provider-v2" || status.Model != "test-model-v2" || status.CompatibilityIdentity != "compat-v2" {
		t.Fatalf("compatibility rebuild status = %+v, %v", status, err)
	}
}

func TestEmbeddingPolicyRejectionPreservesEligibleRebuild(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMemoryWithContent(t, repository, "policy-rebuild", "automobile policy")
	claim, err := repository.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "policy-worker", LeaseDuration: time.Minute})
	if err != nil || claim.Job.JobType != "embed_memory" {
		t.Fatalf("embedding claim = %+v, %v", claim, err)
	}
	if err := repository.CompleteJob(context.Background(), claim, JobResult{Code: "policy_rejected"}); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='rebuild_semantic' AND processing_version=? AND status IN ('pending','waiting','running','retrying')`, repository.embeddingIdentity()).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active rebuilds after policy rejection = %d", active)
	}
}

func TestHybridFallsBackWhenVectorSearchFails(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMemoryWithContent(t, repository, "vector-fallback", "vectorfallback")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	repository.semanticSearchErr = errors.New("vector distance unavailable")
	result, err := repository.SearchMemories(context.Background(), hybridParams("vectorfallback"))
	if err != nil || !reflect.DeepEqual(result.ExecutedChannels, []string{"keyword", "graph"}) || !result.Degraded || len(result.Results) != 1 {
		t.Fatalf("vector search fallback = %+v, %v", result, err)
	}
}

func TestSemanticQueryNormalizesCompleteScopeAndSeparatesQueryClassification(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	configureSemanticQueryPolicy(t, repository, "space-query-a", "space-query-b")
	insertPolicySpace(t, repository, "space-query-a", "space-1", "project", "query-a")
	insertPolicySpace(t, repository, "space-query-b", "space-1", "project", "query-b")
	first := createMemoryInSpace(t, repository, "query-scope-a", "space-query-a", "automobile queryscope")
	second := createMemoryInSpace(t, repository, "query-scope-b", "space-query-b", "automobile queryscope")
	setEmbeddingEligibilityForSpace(t, repository, first, "space-query-a", 1, true)
	setEmbeddingEligibilityForSpace(t, repository, second, "space-query-b", 1, true)
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}

	params := semanticParams("car")
	params.SpaceIDs = []string{"space-query-b", "space-query-a"}
	result, err := repository.SearchMemories(context.Background(), params)
	if err != nil || len(result.Results) != 2 {
		t.Fatalf("normalized semantic search = %+v, %v", result, err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("semantic query did not call provider")
	}
	request := provider.requests[len(provider.requests)-1]
	if request.SpaceID() != "space-query-a" || strings.Join(request.SourceTypes(), ",") != "semantic_query" {
		t.Fatalf("query request = space %q source types %v", request.SpaceID(), request.SourceTypes())
	}

	params.SpaceIDs = []string{"space-query-a", "space-query-b"}
	reordered, err := repository.SearchMemories(context.Background(), params)
	if err != nil || strings.Join(resultIDs(reordered), ",") != strings.Join(resultIDs(result), ",") {
		t.Fatalf("order-invariant semantic search = %+v, %v; first = %+v", reordered, err, result)
	}
}

func TestSemanticQueryDenialExplicitlyDegradesHybrid(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	provider := &semanticTestProvider{}
	configureSemanticTest(t, repository, provider)
	configureSemanticQueryPolicy(t, repository, "space-query-allowed")
	insertPolicySpace(t, repository, "space-query-allowed", "space-1", "project", "allowed")
	insertPolicySpace(t, repository, "space-query-denied", "space-1", "project", "denied")
	allowed := createMemoryInSpace(t, repository, "query-allowed", "space-query-allowed", "automobile deniedscope")
	setEmbeddingEligibilityForSpace(t, repository, allowed, "space-query-allowed", 1, true)
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	createMemoryInSpace(t, repository, "query-denied", "space-query-denied", "automobile deniedscope")
	requests := len(provider.requests)

	params := semanticParams("deniedscope")
	params.SpaceIDs = []string{"space-query-allowed", "space-query-denied"}
	if _, err := repository.SearchMemories(context.Background(), params); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("semantic denied scope error = %v", err)
	}
	if len(provider.requests) != requests {
		t.Fatalf("denied semantic scope called provider: %d -> %d", requests, len(provider.requests))
	}

	params.Channels = []string{"keyword", "semantic", "graph"}
	result, err := repository.SearchMemories(context.Background(), params)
	if err != nil || !reflect.DeepEqual(result.ExecutedChannels, []string{"keyword", "graph"}) || !result.Degraded || strings.Join(result.DegradationReasons, ",") != "semantic_policy_denied" || len(result.Results) != 2 {
		t.Fatalf("hybrid denied scope = %+v, %v", result, err)
	}
	if len(provider.requests) != requests {
		t.Fatalf("hybrid denied scope called provider: %d -> %d", requests, len(provider.requests))
	}
}

func TestHybridCursorStableInvalidAfterVersionSwitchAndHonorsBudgets(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMatchingMemories(t, repository, "cursor", "automobile pageword", minSelectionWindow+1)
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	params := hybridParams("pageword")
	params.Limit, params.MaxChars = 1, 100
	first, err := repository.SearchMemories(context.Background(), params)
	if err != nil || len(first.Results) != selectionWindow(params.Limit) || first.NextCursor == nil || first.CharsUsed > params.MaxChars {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	params.Cursor = *first.NextCursor
	second, err := repository.SearchMemories(context.Background(), params)
	if err != nil || len(second.Results) != 1 || second.Results[0].Memory.MemoryID == first.Results[0].Memory.MemoryID || second.CharsUsed > params.MaxChars {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	oldCursor := *first.NextCursor
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	params.Cursor = oldCursor
	if _, err := repository.SearchMemories(context.Background(), params); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor after version switch error = %v", err)
	}

	params = hybridParams("pageword")
	params.Limit, params.MaxChars = 2, len("automobile pageword one")
	budgeted, err := repository.SearchMemories(context.Background(), params)
	if err != nil || len(budgeted.Context.Items) > params.Limit || budgeted.Context.Accounting.CharsUsed > params.MaxChars || !budgeted.Truncated {
		t.Fatalf("simultaneous budgets = %+v, %v", budgeted, err)
	}
}

func TestFTSRebuildAdvancesVersionAndRejectsOldKeywordCursor(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMatchingMemories(t, repository, "fts-version", "versionword", minSelectionWindow+1)
	params := SearchParams{Query: "versionword", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 1, MaxChars: 1000}
	first, err := repository.SearchMemories(context.Background(), params)
	if err != nil || first.FTSIndexVersion != 1 || first.NextCursor == nil {
		t.Fatalf("first search = %+v, %v", first, err)
	}

	if err := repository.RebuildFTS(context.Background()); err != nil {
		t.Fatal(err)
	}
	params.Cursor = *first.NextCursor
	if _, err := repository.SearchMemories(context.Background(), params); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("search with pre-rebuild cursor error = %v", err)
	}
	params.Cursor = ""
	after, err := repository.SearchMemories(context.Background(), params)
	if err != nil || after.FTSIndexVersion != 2 {
		t.Fatalf("post-rebuild search = %+v, %v", after, err)
	}
}

func TestKeywordCursorBindsCorpusAndRankingVersions(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	if _, err := repository.store.writer.Exec(`UPDATE feedback_aggregate_metadata SET generation=7 WHERE singleton=1; UPDATE entity_index_metadata SET active_version=8 WHERE singleton=1; UPDATE relation_index_metadata SET active_version=9 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	createMatchingMemories(t, repository, "cursor-contract", "contractword", minSelectionWindow+1)
	params := SearchParams{Query: "contractword", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 1, MaxChars: 1000}
	first, err := repository.SearchMemories(context.Background(), params)
	if err != nil || first.NextCursor == nil || first.CorpusGeneration == 0 || first.FeedbackAggregateVersion != 7 {
		t.Fatalf("versioned response = %+v, %v", first, err)
	}
	for _, update := range []string{
		`UPDATE corpus_metadata SET generation=generation+1 WHERE singleton=1`,
		`UPDATE feedback_aggregate_metadata SET generation=generation+1 WHERE singleton=1`,
		`UPDATE entity_index_metadata SET active_version=active_version+1 WHERE singleton=1`,
		`UPDATE relation_index_metadata SET active_version=active_version+1 WHERE singleton=1`,
	} {
		params.Cursor = *first.NextCursor
		if _, err := repository.store.writer.Exec(update); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.SearchMemories(context.Background(), params); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("cursor remained valid after %q: %v", update, err)
		}
		if _, err := repository.store.writer.Exec(`UPDATE corpus_metadata SET generation=3 WHERE singleton=1; UPDATE feedback_aggregate_metadata SET generation=7 WHERE singleton=1; UPDATE entity_index_metadata SET active_version=8 WHERE singleton=1; UPDATE relation_index_metadata SET active_version=9 WHERE singleton=1`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHybridCursorBindsCorpusAndRankingVersions(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMatchingMemories(t, repository, "hybrid-contract", "hybridcontract", minSelectionWindow+1)
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	params := hybridParams("hybridcontract")
	params.Limit = 1
	first, err := repository.SearchMemories(context.Background(), params)
	if err != nil || first.NextCursor == nil {
		t.Fatalf("hybrid first page = %+v, %v", first, err)
	}
	params.Cursor = *first.NextCursor
	if _, err := repository.store.writer.Exec(`UPDATE relation_index_metadata SET active_version=active_version+1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SearchMemories(context.Background(), params); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("hybrid cursor after relation version change = %v", err)
	}
}

func semanticParams(query string) SearchParams {
	return SearchParams{Query: query, SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"semantic"}, Limit: 10, MaxChars: 1000}
}

func hybridParams(query string) SearchParams {
	params := semanticParams(query)
	params.Channels = []string{"keyword", "semantic", "graph"}
	return params
}

func createMemoryWithContent(t *testing.T, repository *Repository, key, content string) domain.Memory {
	return createMemoryInSpace(t, repository, key, "space-1", content)
}

// createMatchingMemories fills a repository with enough distinct current
// candidates to exceed the ranked selection window. Cursor tests need a full
// window before a NextCursor is produced.
func createMatchingMemories(t *testing.T, repository *Repository, prefix, token string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		createMemoryWithContent(t, repository, fmt.Sprintf("%s-%03d", prefix, i), fmt.Sprintf("%s item %03d", token, i))
	}
}

func createMemoryInSpace(t *testing.T, repository *Repository, key, spaceID, content string) domain.Memory {
	t.Helper()
	params := CreateMemoryParams{SpaceID: spaceID, Kind: "fact", Content: content, Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "semantic test"}
	memory, replay, err := repository.CreateMemory(context.Background(), memoryIdentity(key, "/v1/memories", params), params)
	if err != nil || replay {
		t.Fatalf("create semantic memory = %+v, replay=%t, %v", memory, replay, err)
	}
	return memory
}

func setEmbeddingEligibility(t *testing.T, repository *Repository, memory domain.Memory, generation int, eligible bool) {
	setEmbeddingEligibilityForSpace(t, repository, memory, "space-1", generation, eligible)
}

func setEmbeddingEligibilityForSpace(t *testing.T, repository *Repository, memory domain.Memory, spaceID string, generation int, eligible bool) {
	t.Helper()
	tx, err := repository.store.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO embedding_eligibility(memory_id,revision_id,space_id,configuration_generation,policy_generation,eligible,reason_code,evaluated_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(memory_id,revision_id,policy_generation) DO UPDATE SET space_id=excluded.space_id,configuration_generation=excluded.configuration_generation,eligible=excluded.eligible,reason_code=excluded.reason_code,evaluated_at=excluded.evaluated_at WHERE embedding_eligibility.space_id<>excluded.space_id OR coalesce(embedding_eligibility.configuration_generation,-1)<>coalesce(excluded.configuration_generation,-1) OR embedding_eligibility.eligible<>excluded.eligible OR embedding_eligibility.reason_code<>excluded.reason_code`, memory.MemoryID, memory.CurrentRevision.RevisionID, spaceID, generation, generation, boolInt(eligible), "test", formatTime(repository.runtime.Now()))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	if changed > 0 {
		if err := advanceSemanticCorpusGeneration(context.Background(), tx, formatTime(repository.runtime.Now()), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func configureSemanticQueryPolicy(t *testing.T, repository *Repository, allowedSpaces ...string) {
	t.Helper()
	now := repository.runtime.Now()
	assignments := make([]PolicyAssignmentRecord, len(allowedSpaces))
	for i, spaceID := range allowedSpaces {
		assignments[i] = PolicyAssignmentRecord{AssignmentID: "query-allow-" + spaceID, PolicyIdentity: "query-policy", Capability: "embedding", SelectorKind: "space", SpaceID: spaceID, Effect: "allow"}
	}
	err := repository.CommitActivation(context.Background(), ActivationCommit{
		Revision:   RevisionRecord{RevisionID: "query-revision", SourceIdentity: "query-source", EffectiveIdentity: "query-effective", ConfigVersion: 1, LoadedAt: now},
		Activation: ActivationRecord{ActivationID: "query-activation", RevisionID: "query-revision", Trigger: "startup", RequestedBy: "test", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities: []CapabilityStateRecord{
			{Capability: "processing", Enabled: false, HealthState: "disabled", ReconciliationState: "idle"},
			{Capability: "embedding", Enabled: true, ConfigurationIdentity: "query-config", ConfigurationGeneration: 1, CompatibilityIdentity: EmbeddingCompatibilityIdentity("test-provider/test-model"), PolicyIdentity: "query-policy", PolicyGeneration: 1, HealthState: "ready", ReconciliationState: "idle"},
		},
		Policies:    []ProviderPolicyRecord{{PolicyIdentity: "query-policy", Capability: "embedding", DefaultDecision: "deny", AllowedContentTypes: []string{"text/plain"}, AllowedSourceTypes: []string{"manual"}, AllowedSensitivities: []string{"normal"}, QueryAllowed: true, CreatedAt: now}},
		Assignments: assignments,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := repository.emb()
	repository.SetEmbedding(runtime.Provider, "test-provider", "test-model", EmbeddingCompatibilityIdentity("test-provider/test-model"), "test-provider/test-model", 1, 1)
}

func searchResultsByID(response domain.SearchResponse) map[string]domain.SearchResult {
	result := make(map[string]domain.SearchResult, len(response.Results))
	for _, item := range response.Results {
		result[item.Memory.MemoryID] = item
	}
	return result
}

func resultIDs(response domain.SearchResponse) []string {
	result := make([]string, len(response.Results))
	for i, item := range response.Results {
		result[i] = item.Memory.MemoryID
	}
	return result
}

func assertChannels(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("channels = %v, want %v", got, want)
	}
}

func TestEmbeddingDocumentAndQueryFormatsAreVersioned(t *testing.T) {
	if embeddingDocumentFormatVersion != "embedding-document-v2" || embeddingQueryFormatVersion != "embedding-query-v1" {
		t.Fatalf("embedding format versions = %q/%q", embeddingDocumentFormatVersion, embeddingQueryFormatVersion)
	}
	document := embeddingDocument(domain.MemoryRevision{
		Kind: domain.MemoryKindEvent, CanonicalContent: "canonical body", EventType: "meeting",
		Language: "en", SourceSpeaker: "Alice", TimePrecision: domain.TimePrecisionDay,
		SubjectEntityIDs: []string{"alice"}, Attributes: map[string]string{"location": "hq"},
	})
	for _, want := range []string{"kind:event", "canonical body", "event_type:meeting", "language:en", "speaker:Alice", "time_precision:day", "subject:alice", "attribute:location:hq"} {
		if !strings.Contains(document, want) {
			t.Fatalf("embedding document missing %q: %q", want, document)
		}
	}
	if !strings.HasPrefix(embeddingQueryDocument("find the report"), "query:") {
		t.Fatalf("query document = %q", embeddingQueryDocument("find the report"))
	}
}

func TestEmbeddingCompatibilityBindsDocumentAndQueryFormats(t *testing.T) {
	identity := EmbeddingCompatibilityIdentity("provider/model")
	if identity == "" || identity == "provider/model" || identity != EmbeddingCompatibilityIdentity("provider/model") {
		t.Fatalf("embedding compatibility identity = %q", identity)
	}
}

func TestANNWindowIsExactlyReranked(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	relevant := createMemoryWithContent(t, repository, "ann-relevant", "automobile handbook")
	createMemoryWithContent(t, repository, "ann-other", "generic manual")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	repository.SetSemanticWindowRecall(func(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, policyGeneration int, compatibilityIdentity string, dimension int, queryBlob []byte, limit int) ([]semanticRecallItem, error) {
		rows, err := tx.QueryContext(ctx, `SELECT memory_id, revision_id, content_hash FROM memory_projections WHERE searchable=1 ORDER BY memory_id DESC`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var window []semanticRecallItem
		for rows.Next() {
			var item semanticRecallItem
			if err := rows.Scan(&item.MemoryID, &item.RevisionID, &item.ContentHash); err != nil {
				return nil, err
			}
			item.Score = 0.01
			window = append(window, item)
		}
		return window, rows.Err()
	})
	result, err := repository.SearchMemories(context.Background(), semanticParams("car"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) == 0 || result.Results[0].Memory.MemoryID != relevant.MemoryID {
		t.Fatalf("exact rerank result = %+v", result.Results)
	}
}

func TestExplainPopulatesFeatureAndCalibrationProvenance(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	createMemoryWithContent(t, repository, "explain-provenance", "automobile explainable")
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	params := semanticParams("car explainable")
	params.Explain = true
	result, err := repository.SearchMemories(context.Background(), params)
	if err != nil || len(result.Results) == 0 {
		t.Fatalf("explain search = %+v, %v", result, err)
	}
	item := result.Results[0]
	if len(item.RankingFeatures) == 0 {
		t.Fatalf("ranking features missing: %+v", item)
	}
	if item.SemanticRawScore == nil || item.SemanticPercentile == nil {
		t.Fatalf("semantic provenance missing: %+v", item)
	}
	if item.SemanticCalibrationIdentity == "" {
		t.Fatal("calibration identity missing")
	}
}
