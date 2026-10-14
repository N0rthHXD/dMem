package daemon

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
	"github.com/N0rthHXD/dMem/internal/identity"
	"github.com/N0rthHXD/dMem/internal/jobs"
	"github.com/N0rthHXD/dMem/internal/storage"
)

func TestAutoConfirmPolicyConversionFollowsConfiguration(t *testing.T) {
	t.Parallel()
	disabled := autoConfirmPolicy(config.ReviewConfig{
		AutoConfirmEnabled: false, AutoConfirmVersion: "auto-review-v1",
		AutoConfirmMinConfidence: 0.95, AutoConfirmKinds: []string{"fact"}, AutoConfirmMaxPerSpacePerHour: 20,
	})
	if disabled.Valid() {
		t.Fatal("a disabled review configuration produced a valid policy")
	}
	enabled := autoConfirmPolicy(config.ReviewConfig{
		AutoConfirmEnabled: true, AutoConfirmVersion: "auto-review-v1",
		AutoConfirmMinConfidence: 0.95, AutoConfirmKinds: []string{"fact", "scene"}, AutoConfirmMaxPerSpacePerHour: 20,
	})
	if !enabled.Valid() {
		t.Fatalf("enabled policy is invalid: %+v", enabled)
	}
	if len(enabled.Kinds) != 1 || enabled.Kinds[0] != domain.MemoryKindFact {
		t.Fatalf("kinds = %+v; want the aggregate kind dropped", enabled.Kinds)
	}
}

func TestProviderRouteAutoConfirmPublishesWithoutHumanReview(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "auto-confirm.db"), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := storage.NewRepository(store, domain.Runtime{Now: time.Now, ID: identity.UUID}, 100, 200, 3, "auto-confirm-v1")
	repository.SetProcessingEnabled(true)
	repository.SetProcessingGeneration(1, 0, "auto-confirm-compat")
	repository.SetAutoConfirmPolicy(autoConfirmPolicy(config.ReviewConfig{
		AutoConfirmEnabled: true, AutoConfirmVersion: "auto-review-v1",
		AutoConfirmMinConfidence: 0.9, AutoConfirmKinds: []string{"fact"}, AutoConfirmMaxPerSpacePerHour: 5,
	}))

	space, _, err := repository.CreateSpace(ctx, providerRouteIdentity("space", "/v1/spaces"), storage.CreateSpaceParams{SpaceType: "user", ExternalID: "auto-confirm", DisplayName: "Auto Confirm"})
	if err != nil {
		t.Fatal(err)
	}
	ingest := extraction.NewService(storage.NewEventCapability(repository))
	event, err := ingest.Ingest(ctx, gateway.CallContext{RequestID: "ingest", Caller: gateway.CallerIdentity{SubjectID: "collector"}, Idempotency: &gateway.Idempotency{Key: "event-1"}}, extraction.IngestCommand{
		SpaceID: space.SpaceID, SourceType: "manual", SourceName: "collector", OccurredAt: "2026-08-21T11:00:00Z",
		Content: domain.Content{Kind: "text", MediaType: "text/plain", Text: "Alice prefers PostgreSQL"},
	})
	if err != nil || len(event.JobIDs) != 1 {
		t.Fatalf("ingest = %+v error = %v", event, err)
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

	confirmed := waitForConfirmedCandidate(t, repository, space.SpaceID)
	if confirmed.ReviewState != "confirmed" || confirmed.ConfirmedMemoryID == nil || confirmed.ConfirmedRevisionID == nil {
		t.Fatalf("candidate = %+v; want published authority", confirmed)
	}
	if len(confirmed.Reviews) != 1 || confirmed.Reviews[0].ReviewerID != review.AutoConfirmReviewerID || confirmed.Reviews[0].ReasonCode != review.AutoConfirmReasonCode {
		t.Fatalf("reviews = %+v; want one system auto review", confirmed.Reviews)
	}
	if confirmed.AutoReviewVersion == nil || *confirmed.AutoReviewVersion != "auto-review-v1" {
		t.Fatalf("auto_review_version = %v; want the configured policy version", confirmed.AutoReviewVersion)
	}

	recall := storage.NewRecallCapability(repository)
	retrieval := gwretrieval.NewService(gwretrieval.Ports{ReferenceTime: recall, Capabilities: recall, Candidates: recall, Telemetry: recall}, gwretrieval.Defaults{MaxResults: 10, MaxContextTokens: 500, MaxContextChars: 2000})
	result, err := retrieval.Recall(ctx, gateway.CallContext{RequestID: "recall"}, gwretrieval.Query{Query: "PostgreSQL", SpaceIDs: []string{space.SpaceID}, Limit: 5, LimitSet: true})
	if err != nil {
		t.Fatalf("recall error = %v", err)
	}
	found := false
	for _, item := range result.Context.Items {
		if item.MemoryID == *confirmed.ConfirmedMemoryID {
			found = true
		}
	}
	if !found {
		t.Fatalf("recall = %+v; want the auto-confirmed memory", result.Context.Items)
	}
}

func waitForConfirmedCandidate(t *testing.T, repository *storage.Repository, spaceID string) domain.Candidate {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		page, err := repository.ListCandidates(context.Background(), storage.CandidateFilter{SpaceID: spaceID, ReviewState: "confirmed", Limit: 10})
		if err == nil && len(page.Items) > 0 {
			return page.Items[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no auto-confirmed candidate produced for space %s (error %v)", spaceID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagedConfigAutoConfirmPublishesCandidateWithoutReviewCall(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	path := filepath.Join(root, "config.toml")
	document := managedConfig(dataDir, true, "") + `
[review.auto_confirm]
enabled = true
version = "auto-review-v1"
min_confidence = 0.9
kinds = ["fact"]
max_per_space_per_hour = 5

[[processing.provider.policy.assignments]]
selector = "space_type"
space_type = "user"
effect = "allow"
`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("DMEM_RUNTIME_PROCESSING_SECRET", "processing-secret")

	instance, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatalf("StartManaged() error = %v", err)
	}
	t.Cleanup(func() { _ = instance.Close(context.Background()) })
	snapshot, _ := instance.ConfigurationStatus()
	if snapshot == nil || !snapshot.Config.Review.AutoConfirmEnabled {
		t.Fatalf("activated snapshot does not carry the review policy: %+v", snapshot)
	}

	processor := &mixedRouteProcessor{}
	instance.jobs.SetProcessing(jobs.ProcessingConfig{
		Processor: processor, ProcessorIdentity: "mixed-route/test", Enabled: true,
		MaxContentChars: 4096, MaxTotalCandidateChars: 8192, MaxContextItems: 4,
		ProcessingVersion: "mixed-route-v1",
	})

	handler := instance.Handler()
	scopeID := ensureScope(t, handler, "managed-auto-confirm")
	event := callHandler(t, handler, http.MethodPost, "/v1/events", "managed-auto-confirm-event", map[string]any{
		"scope_id": scopeID,
		"source":   map[string]any{"type": "manual", "name": "collector", "occurred_at": "2026-08-21T11:00:00Z"},
		"content":  map[string]any{"kind": "text", "media_type": "text/plain", "text": "Alice prefers PostgreSQL"},
	}, true)
	if event.Code != http.StatusAccepted {
		t.Fatalf("event ingest status = %d: %s", event.Code, event.Body.String())
	}

	confirmed := waitForConfirmedCandidate(t, instance.repository, scopeID)
	if confirmed.ConfirmedMemoryID == nil || len(confirmed.Reviews) != 1 || confirmed.Reviews[0].ReviewerID != review.AutoConfirmReviewerID {
		t.Fatalf("candidate = %+v; want one system auto review", confirmed)
	}
	if confirmed.AutoReviewVersion == nil || *confirmed.AutoReviewVersion != "auto-review-v1" {
		t.Fatalf("auto_review_version = %v; want the configured policy version", confirmed.AutoReviewVersion)
	}
	pending, err := instance.repository.ListCandidates(context.Background(), storage.CandidateFilter{SpaceID: scopeID, ReviewState: "pending", Limit: 10})
	if err != nil || len(pending.Items) != 0 {
		t.Fatalf("pending candidates = %+v, error = %v; want none", pending.Items, err)
	}
}
