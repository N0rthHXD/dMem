package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/identity"
	"github.com/N0rthHXD/dMem/internal/jobs"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/storage"
)

// routeProcessor is an in-process Provider Route processor. It stands in for the
// configured provider adapter so the integration test exercises the durable job
// pipeline, the fenced commit, review, embedding, and recall without a network
// dependency.
type routeProcessor struct {
	calls int
}

func (p *routeProcessor) Enabled() bool    { return true }
func (p *routeProcessor) Identity() string { return "provider-route/test" }

func (p *routeProcessor) Process(_ context.Context, input domain.ProcessingInput) domain.ProcessingResult {
	p.calls++
	return domain.ProcessingResult{
		Code: domain.ProcessingCandidatesExtracted, ProviderIdentity: "provider-route/test", ModelIdentity: "model-test",
		Proposals: []domain.MemoryCandidateProposal{{
			Action: domain.ConsolidationCreate,
			Revision: domain.MemoryRevision{
				Kind: domain.MemoryKindFact, DisplayContent: "Alice prefers PostgreSQL", CanonicalContent: "alice prefers postgresql",
				Sensitivity: "normal", Confidence: 0.9,
				Evidence: []domain.RevisionEvidence{{EvidenceID: "evidence-1", EventID: input.Target.EventID, Role: domain.EvidenceRoleDirect}},
			},
		}},
	}
}

// routeEmbedding is a deterministic local embedding provider for the semantic
// half of the Provider Route integration test.
type routeEmbedding struct{}

func (routeEmbedding) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.NewEmbeddingResult([][]float32{{1, 0, 0, 0}}, "provider-route/test"), nil
}

func providerRouteIdentity(key, route string) storage.WriteIdentity {
	return storage.WriteIdentity{CallerID: "provider-route-test", IdempotencyKey: key, RequestID: "request-" + key, Method: "POST", Route: route, RequestHash: storage.HashRequest(key)}
}

// TestProviderRouteEventToCandidateReviewEmbeddingAndRecall drives the enabled
// Provider Route end to end: a raw event becomes a durable job, the processor
// result becomes a reviewable candidate, confirmation publishes the revision,
// the embedding index activates, and the shared recall pipeline returns it.
func TestProviderRouteEventToCandidateReviewEmbeddingAndRecall(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "provider-route.db"), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := storage.NewRepository(store, domain.Runtime{Now: time.Now, ID: identity.UUID}, 100, 200, 3, "provider-route-v1")
	repository.SetProcessingEnabled(true)
	repository.SetProcessingGeneration(1, 0, "provider-route-compat")
	repository.SetEmbedding(routeEmbedding{}, "provider-route", "model-test", storage.EmbeddingCompatibilityIdentity("provider-route/model-test", 0), "provider-route/model-test", 0, 0)

	space, _, err := repository.CreateSpace(ctx, providerRouteIdentity("space", "/v1/spaces"), storage.CreateSpaceParams{SpaceType: "user", ExternalID: "provider-route", DisplayName: "Provider Route"})
	if err != nil {
		t.Fatal(err)
	}
	ingest := extraction.NewService(storage.NewEventCapability(repository))
	event, err := ingest.Ingest(ctx, gateway.CallContext{RequestID: "ingest", Caller: gateway.CallerIdentity{SubjectID: "collector"}, Idempotency: &gateway.Idempotency{Key: "event-1"}}, extraction.IngestCommand{
		SpaceID: space.SpaceID, SourceType: "manual", SourceName: "collector", OccurredAt: "2026-08-21T11:00:00Z",
		Content: domain.Content{Kind: "text", MediaType: "text/plain", Text: "Alice prefers PostgreSQL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(event.JobIDs) != 1 || event.Status != "accepted" {
		t.Fatalf("ingest = %+v, want one accepted extraction job", event)
	}

	processor := &routeProcessor{}
	service, err := jobs.NewService(repository, config.Config{WorkerCount: 1, LeaseDuration: 5 * time.Second, PerJobTimeout: 10 * time.Second, RetryInitialBackoff: 50 * time.Millisecond, RetryMaxBackoff: time.Second}, processor, jobs.Dependencies{Processing: storage.NewExtractionWorkCapability(repository), Indexes: repository})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Shutdown(shutdown)
	})

	candidateID := waitForCandidate(t, repository, space.SpaceID)
	if processor.calls != 1 {
		t.Fatalf("processor calls = %d, want 1", processor.calls)
	}
	confirmed, _, replay, err := repository.ConfirmCandidate(ctx, providerRouteIdentity("confirm-1", "provider_route.candidate_review"), candidateID, storage.ReviewCandidateParams{ReasonCode: "reviewed"})
	if err != nil || replay {
		t.Fatalf("ConfirmCandidate() replay=%t error=%v", replay, err)
	}
	if confirmed.CurrentRevision.RevisionID == "" {
		t.Fatalf("confirmed memory = %+v", confirmed)
	}

	if err := repository.RebuildSemantic(ctx); err != nil {
		t.Fatalf("RebuildSemantic() error = %v", err)
	}
	if state, version := repository.SemanticHealth(ctx); state != "ready" || version < 1 {
		t.Fatalf("semantic health = %q/%d, want ready", state, version)
	}

	recall := storage.NewRecallCapability(repository)
	retrieval := gwretrieval.NewService(gwretrieval.Ports{ReferenceTime: recall, Capabilities: recall, Candidates: recall, Telemetry: recall}, gwretrieval.Defaults{MaxResults: 10, MaxContextTokens: 500, MaxContextChars: 2000})
	result, err := retrieval.Recall(ctx, gateway.CallContext{RequestID: "recall"}, gwretrieval.Query{Query: "PostgreSQL", SpaceIDs: []string{space.SpaceID}, Limit: 5, LimitSet: true})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	found := false
	for _, item := range result.Context.Items {
		if item.MemoryID == confirmed.MemoryID {
			found = true
		}
	}
	if !found {
		t.Fatalf("recall did not return the provider-route memory: %+v", result.Context.Items)
	}
}

func waitForCandidate(t *testing.T, repository *storage.Repository, spaceID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		page, err := repository.ListCandidates(context.Background(), storage.CandidateFilter{SpaceID: spaceID, ReviewState: "pending", Limit: 10})
		if err == nil && len(page.Items) > 0 {
			return page.Items[0].CandidateID
		}
		if time.Now().After(deadline) {
			t.Fatalf("no candidate produced for space %s (error %v)", spaceID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
