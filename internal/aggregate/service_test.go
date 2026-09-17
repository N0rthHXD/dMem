package aggregate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type fakeProvider struct {
	result domain.AggregateProviderResult
	err    error
	calls  []domain.AggregateProviderInput
}

func (f *fakeProvider) Build(_ context.Context, input domain.AggregateProviderInput) (domain.AggregateProviderResult, error) {
	f.calls = append(f.calls, input)
	return f.result, f.err
}

func aggregateTestRepository(t *testing.T) (*storage.Repository, string) {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "aggregate.db"), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	n := 0
	runtime := domain.Runtime{Now: func() time.Time { return time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC) }, ID: func() (string, error) { n++; return uuid(n), nil }}
	repository := storage.NewRepository(store, runtime, 10, 20, 3, "aggregate-v1")
	identity := storage.WriteIdentity{CallerID: "test", IdempotencyKey: "space", RequestID: uuid(100), Method: "POST", Route: "/v1/spaces", RequestHash: storage.HashRequest("space")}
	space, _, err := repository.CreateSpace(context.Background(), identity, storage.CreateSpaceParams{SpaceType: "user", ExternalID: "aggregate", DisplayName: "Aggregate"})
	if err != nil {
		t.Fatal(err)
	}
	eventIdentity := storage.WriteIdentity{CallerID: "test", IdempotencyKey: "event", RequestID: uuid(101), Method: "POST", Route: "/v1/events", RequestHash: storage.HashRequest("event")}
	event, _, err := repository.IngestEvent(context.Background(), eventIdentity, storage.IngestParams{SpaceID: space.SpaceID, SourceType: "manual", SourceName: "test", OccurredAt: "2026-08-25T00:00:00Z", Content: domain.Content{Kind: "text", MediaType: "text/plain", Text: "aggregate evidence"}, Metadata: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		key := uuid(200 + i)
		identity = storage.WriteIdentity{CallerID: "test", IdempotencyKey: key, RequestID: uuid(300 + i), Method: "POST", Route: "/v1/memories", RequestHash: storage.HashRequest(key)}
		_, _, err = repository.CreateMemory(context.Background(), identity, storage.CreateMemoryParams{SpaceID: space.SpaceID, Kind: "fact", Content: key, Sensitivity: "normal", Evidence: []storage.EvidenceParams{{EventID: event.EventID}}, Reason: "test"})
		if err != nil {
			t.Fatal(err)
		}
	}
	return repository, space.SpaceID
}

func uuid(n int) string { return "00000000-0000-4000-8000-" + fmt.Sprintf("%012d", n) }

func TestBuildSceneTopicBoundedOutsideWriteTransactionIdentity(t *testing.T) {
	repository, space := aggregateTestRepository(t)
	page, err := repository.ListMemories(context.Background(), storage.MemoryFilter{SpaceID: space, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{result: domain.AggregateProviderResult{Content: "scene output", Sensitivity: "normal", ProviderIdentity: "fake/local", ModelIdentity: "fake-v1"}}
	service := NewService(repository, provider, "aggregate/fake", "policy-v1", "aggregate-v1", 2)
	revisions := []string{page.Items[1].CurrentRevision.RevisionID, page.Items[0].CurrentRevision.RevisionID}
	identity := storage.WriteIdentity{CallerID: "test", IdempotencyKey: "build", RequestID: uuid(500), Method: "POST", Route: "/v1/aggregate-proposals", RequestHash: storage.HashRequest(revisions)}
	proposal, _, err := service.Build(context.Background(), identity, space, "scene", revisions)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 1 || len(provider.calls[0].Inputs) != 2 || provider.calls[0].Inputs[0].RevisionID > provider.calls[0].Inputs[1].RevisionID {
		t.Fatalf("provider calls=%+v", provider.calls)
	}
	if proposal.ProviderIdentity == nil || *proposal.ProviderIdentity != "fake/local" || proposal.ModelIdentity == nil || *proposal.ModelIdentity != "fake-v1" || proposal.ProcessorIdentity != "aggregate/fake" || proposal.PolicyVersion != "policy-v1" || proposal.ProcessingVersion != "aggregate-v1" || proposal.InputFingerprint == "" {
		t.Fatalf("proposal=%+v", proposal)
	}
	if _, _, err = service.Build(context.Background(), identity, space, "topic", append(revisions, revisions[0])); !errors.Is(err, storage.ErrInvalidAggregateProposal) {
		t.Fatalf("bound error=%v", err)
	}
}

func TestBuildCapturesOneProcessingSnapshot(t *testing.T) {
	repository, space := aggregateTestRepository(t)
	page, err := repository.ListMemories(context.Background(), storage.MemoryFilter{SpaceID: space, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	oldProvider := &blockingAggregateProvider{started: started, release: release, result: domain.AggregateProviderResult{Content: "old", Sensitivity: "normal", ProviderIdentity: "old/provider", ModelIdentity: "old-model"}}
	service := NewService(repository, oldProvider, "processor-old", "policy-old", "version-old", 2)
	identity := storage.WriteIdentity{CallerID: "test", IdempotencyKey: "captured", RequestID: uuid(700), Method: "POST", Route: "/v1/aggregate-proposals", RequestHash: storage.HashRequest("captured")}
	errCh := make(chan error, 1)
	go func() {
		_, _, err := service.Build(context.Background(), identity, space, "scene", []string{page.Items[0].CurrentRevision.RevisionID, page.Items[1].CurrentRevision.RevisionID})
		errCh <- err
	}()
	<-started
	service.SetProcessing(&fakeProvider{result: domain.AggregateProviderResult{Content: "new", Sensitivity: "normal", ProviderIdentity: "new/provider", ModelIdentity: "new-model"}}, "processor-new", "policy-new", "version-new", 1, 2, 0)
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	proposals, err := repository.ListAggregateProposals(context.Background(), storage.AggregateProposalFilter{SpaceID: space, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals.Items) != 1 {
		t.Fatalf("proposals = %+v", proposals.Items)
	}
	proposal := proposals.Items[0]
	if proposal.ProcessorIdentity != "processor-old" || proposal.PolicyVersion != "policy-old" || proposal.ProcessingVersion != "version-old" || proposal.ProviderIdentity == nil || *proposal.ProviderIdentity != "old/provider" {
		t.Fatalf("proposal used mixed processing configuration: %+v", proposal)
	}
}

type blockingAggregateProvider struct {
	started chan<- struct{}
	release <-chan struct{}
	result  domain.AggregateProviderResult
}

func (p *blockingAggregateProvider) Build(ctx context.Context, _ domain.AggregateProviderInput) (domain.AggregateProviderResult, error) {
	select {
	case p.started <- struct{}{}:
	case <-ctx.Done():
		return domain.AggregateProviderResult{}, ctx.Err()
	}
	select {
	case <-p.release:
		return p.result, nil
	case <-ctx.Done():
		return domain.AggregateProviderResult{}, ctx.Err()
	}
}
