package aggregate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/storage"
)

// aggregateTestRepository builds one space with an event, two input memories,
// and a confirmed aggregate proposal, then marks the aggregate stale so the
// durable rebuild job exists. It returns the repository, the space, and a
// claimed rebuild job.
func aggregateTestRepository(t *testing.T) (*storage.Repository, storage.ClaimedJob) {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "aggregate.db"), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	n := 0
	runtime := domain.Runtime{Now: func() time.Time { return time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC) }, ID: func() (string, error) { n++; return uuid(n), nil }}
	repository := storage.NewRepository(store, runtime, 10, 20, 3, "aggregate-v1")
	space, _, err := repository.CreateSpace(context.Background(), aggregateIdentity("space", "/v1/spaces"), storage.CreateSpaceParams{SpaceType: "user", ExternalID: "aggregate", DisplayName: "Aggregate"})
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := repository.IngestEvent(context.Background(), aggregateIdentity("event", "/v1/events"), storage.IngestParams{SpaceID: space.SpaceID, SourceType: "manual", SourceName: "test", OccurredAt: "2026-08-25T00:00:00Z", Content: domain.Content{Kind: "text", MediaType: "text/plain", Text: "aggregate evidence"}, Metadata: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var memories []domain.Memory
	for i := 0; i < 2; i++ {
		key := fmt.Sprintf("aggregate-input-%d", i)
		memory, _, err := repository.CreateMemory(context.Background(), aggregateIdentity(key, "/v1/memories"), storage.CreateMemoryParams{SpaceID: space.SpaceID, Kind: "fact", Content: key, Sensitivity: "normal", Evidence: []storage.EvidenceParams{{EventID: event.EventID}}, Reason: "test"})
		if err != nil {
			t.Fatal(err)
		}
		memories = append(memories, memory)
	}
	inputs := []domain.AggregateInput{
		{MemoryID: memories[0].MemoryID, RevisionID: memories[0].CurrentRevision.RevisionID, SpaceID: memories[0].SpaceID, Content: memories[0].CurrentRevision.DisplayContent, CurrentState: memories[0].CurrentState, Sensitivity: memories[0].Sensitivity},
		{MemoryID: memories[1].MemoryID, RevisionID: memories[1].CurrentRevision.RevisionID, SpaceID: memories[1].SpaceID, Content: memories[1].CurrentRevision.DisplayContent, CurrentState: memories[1].CurrentState, Sensitivity: memories[1].Sensitivity},
	}
	params := storage.CommitAggregateProposalParams{SpaceID: space.SpaceID, AggregateKind: "scene", Content: "aggregate output", Sensitivity: "normal", PolicyVersion: "aggregate-policy-v1", ProcessorIdentity: "aggregate/fake", ProviderIdentity: "fake/local", ModelIdentity: "fake-v1", ProcessingVersion: "aggregate-v1", Inputs: inputs}
	proposal, _, err := repository.CommitAggregateProposal(context.Background(), aggregateIdentity("proposal", "/v1/aggregate-proposals"), params, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repository.ConfirmAggregateProposal(context.Background(), aggregateIdentity("confirm", "/v1/aggregate-proposals/{proposal_id}/confirm"), proposal.ProposalID, storage.ReviewAggregateProposalParams{ReasonCode: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repository.ReviseMemory(context.Background(), aggregateIdentity("revise", "/v1/memories/{memory_id}"), memories[0].MemoryID, storage.ReviseMemoryParams{ExpectedRevisionID: memories[0].CurrentRevision.RevisionID, Content: "aggregate-input-0 changed", Evidence: []storage.EvidenceParams{{EventID: event.EventID}}, Reason: "changed"}); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimJob(context.Background(), storage.ClaimJobParams{WorkerID: "aggregate-worker", LeaseDuration: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Job.JobType != "rebuild_aggregate" {
		t.Fatalf("claimed job = %s, want rebuild_aggregate", claim.Job.JobType)
	}
	return repository, claim
}

func aggregateIdentity(key, route string) storage.WriteIdentity {
	return storage.WriteIdentity{CallerID: "test", IdempotencyKey: key, RequestID: uuid(1000), Method: "POST", Route: route, RequestHash: storage.HashRequest(key)}
}

func uuid(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func aggregateTestClient(t *testing.T, server *httptest.Server) *provider.Client {
	t.Helper()
	t.Setenv("DMEM_TEST_AGGREGATE_SECRET", "secret")
	client, err := provider.New(provider.Config{
		Enabled: true, Kind: provider.KindOpenAICompatible, Endpoint: server.URL, Model: "model-a",
		Secret: provider.SecretReference{Source: "environment", Name: "DMEM_TEST_AGGREGATE_SECRET"}, TelemetryDisabled: true,
		Limits: provider.Limits{MaxEgressInputChars: 1000, MaxAggregateInputChars: 1000, MaxContextItems: 8, MaxCandidateChars: 1000, MaxTotalCandidateChars: 1000, MaxResponseBytes: 64 * 1024,
			RequestTimeout: time.Second, MaxConcurrent: 2, RequestsPerMinute: 60000},
		Policy: provider.Policy{Default: "deny", ContentTypes: []string{"text/plain"}, SourceTypes: []string{"manual"}, Sensitivities: []string{"normal"},
			Assignments: []provider.PolicyAssignment{{Selector: "space", SpaceID: "space", Effect: "allow"}}},
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestRebuildClaimedCommitsProposalFromProviderOutput(t *testing.T) {
	repository, claim := aggregateTestRepository(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"content\":\"rebuilt scene\",\"sensitivity\":\"normal\"}"}}]}`))
	}))
	defer server.Close()
	service := NewService(repository, aggregateTestClient(t, server), "aggregate/provider", "aggregate-policy-v1", "aggregate-v1", 2)
	if err := service.RebuildClaimed(context.Background(), claim); err != nil {
		t.Fatalf("RebuildClaimed() error = %v", err)
	}
}

func TestRebuildClaimedClassifiesProviderFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		want   error
	}{
		{"permanent rejection", http.StatusBadRequest, storage.ErrProviderPermanent},
		{"transient outage", http.StatusServiceUnavailable, storage.ErrProviderTransient},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, claim := aggregateTestRepository(t)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			service := NewService(repository, aggregateTestClient(t, server), "aggregate/provider", "aggregate-policy-v1", "aggregate-v1", 2)
			err := service.RebuildClaimed(context.Background(), claim)
			if !errors.Is(err, test.want) {
				t.Fatalf("RebuildClaimed() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRebuildClaimedWithoutProviderIsUnsupported(t *testing.T) {
	repository, claim := aggregateTestRepository(t)
	service := NewService(repository, nil, "aggregate/provider", "aggregate-policy-v1", "aggregate-v1", 2)
	if err := service.RebuildClaimed(context.Background(), claim); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("RebuildClaimed() error = %v, want ErrUnsupported", err)
	}
}
