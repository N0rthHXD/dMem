package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type fakeJob struct {
	job             domain.Job
	generation      int
	cancelRequested bool
	failure         storage.JobFailure
}

type fakeRepository struct {
	mu           sync.Mutex
	jobs         []*fakeJob
	recovered    int
	expired      int
	promoted     int
	heartbeats   int
	heartbeatErr error
	failures     []storage.JobFailure
	maintenance  chan struct{}
	processing   *domain.ProcessingInput
}

type capturedLog struct {
	message string
	attrs   map[string]any
}

type captureHandler struct {
	mu      sync.Mutex
	records []capturedLog
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := make(map[string]any)
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.Any()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, capturedLog{message: record.Message, attrs: attrs})
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []capturedLog {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]capturedLog(nil), h.records...)
}

func (r *fakeRepository) ClaimJob(_ context.Context, params storage.ClaimJobParams) (storage.ClaimedJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.jobs {
		if (item.job.Status == "pending" || item.job.Status == "retrying") && item.job.AttemptCount < item.job.MaxAttempts {
			item.job.Status = "running"
			item.job.AttemptCount++
			item.generation++
			return storage.ClaimedJob{Job: item.job, LeaseOwner: params.WorkerID, LeaseGeneration: item.generation}, nil
		}
	}
	return storage.ClaimedJob{}, storage.ErrNotFound
}

func (r *fakeRepository) HeartbeatJob(_ context.Context, claim storage.ClaimedJob, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.find(claim.Job.JobID)
	if !validClaim(item, claim) {
		return false, storage.ErrLeaseLost
	}
	r.heartbeats++
	if r.heartbeatErr != nil {
		return false, r.heartbeatErr
	}
	return item.cancelRequested, nil
}

func (r *fakeRepository) PrepareProcessing(_ context.Context, claim storage.ClaimedJob, params storage.ProcessingPreparationParams) (domain.ProcessingInput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.find(claim.Job.JobID)
	if !validClaim(item, claim) {
		return domain.ProcessingInput{}, storage.ErrLeaseLost
	}
	if r.processing != nil {
		input := *r.processing
		input.ProcessorIdentity = params.ProcessorIdentity
		input.ProcessingVersion = claim.ProcessingVersion
		input.JobID = claim.Job.JobID
		input.Attempt = claim.Job.AttemptCount
		return input, nil
	}
	return domain.ProcessingInput{ProcessorIdentity: params.ProcessorIdentity, ProcessingVersion: claim.ProcessingVersion, JobID: claim.Job.JobID, Attempt: claim.Job.AttemptCount, Target: domain.ProcessingEvent{EventID: claim.Job.TargetID}, Policy: domain.ProcessingPolicy{AllowedMemoryTypes: params.AllowedMemoryTypes, MaxCandidates: params.MaxCandidates, MaxContentChars: params.MaxContentChars}}, nil
}

func (r *fakeRepository) CommitProcessing(_ context.Context, claim storage.ClaimedJob, _ domain.ProcessingInput, commit storage.ProcessingCommit) error {
	return r.CompleteJob(context.Background(), claim, storage.JobResult{Code: string(commit.Result.Code)})
}

func (r *fakeRepository) BlockJob(_ context.Context, claim storage.ClaimedJob, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.find(claim.Job.JobID)
	item.job.Status, item.job.BlockedReasonCode = "blocked_policy", reason
	return nil
}

func (r *fakeRepository) FailProcessing(_ context.Context, claim storage.ClaimedJob, _ domain.ProcessingInput, failure storage.JobFailure) error {
	return r.FailJob(context.Background(), claim, failure)
}

func (r *fakeRepository) CompleteJob(_ context.Context, claim storage.ClaimedJob, result storage.JobResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.find(claim.Job.JobID)
	if !validClaim(item, claim) {
		return storage.ErrLeaseLost
	}
	item.job.Status = "succeeded"
	code := result.Code
	item.job.ResultCode = &code
	return nil
}

func (r *fakeRepository) FailJob(_ context.Context, claim storage.ClaimedJob, failure storage.JobFailure) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.find(claim.Job.JobID)
	if !validClaim(item, claim) {
		return storage.ErrLeaseLost
	}
	item.failure = failure
	r.failures = append(r.failures, failure)
	code := failure.Code
	item.job.LastErrorCode = &code
	if failure.Transient && item.job.AttemptCount < item.job.MaxAttempts {
		item.job.Status = "retrying"
	} else {
		item.job.Status = "failed"
	}
	return nil
}

func (r *fakeRepository) CancelClaimedJob(_ context.Context, claim storage.ClaimedJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.find(claim.Job.JobID)
	if !validClaim(item, claim) || !item.cancelRequested {
		return storage.ErrLeaseLost
	}
	item.job.Status = "cancelled"
	return nil
}

func (r *fakeRepository) RecoverExpiredLeases(context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recovered++
	r.signalMaintenance()
	return 0, nil
}

func (r *fakeRepository) ExpireJobs(context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expired++
	return 0, nil
}

func (r *fakeRepository) PromoteWaitingJobs(_ context.Context, limit int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.promoted++
	count := 0
	for _, item := range r.jobs {
		if item.job.Status == "waiting" && count < limit {
			item.job.Status = "pending"
			count++
		}
	}
	return count, nil
}

func (r *fakeRepository) find(id string) *fakeJob {
	for _, item := range r.jobs {
		if item.job.JobID == id {
			return item
		}
	}
	return nil
}

func (r *fakeRepository) signalMaintenance() {
	if r.maintenance != nil {
		select {
		case r.maintenance <- struct{}{}:
		default:
		}
	}
}

func validClaim(item *fakeJob, claim storage.ClaimedJob) bool {
	return item != nil && item.job.Status == "running" && item.generation == claim.LeaseGeneration
}

func testConfig() config.Config {
	return config.Config{
		WorkerCount:         1,
		LeaseDuration:       60 * time.Millisecond,
		PerJobTimeout:       time.Second,
		RetryInitialBackoff: 10 * time.Millisecond,
		RetryMaxBackoff:     15 * time.Millisecond,
	}
}

func pendingJob(id string, maxAttempts int) *fakeJob {
	return &fakeJob{job: domain.Job{JobID: id, Status: "pending", MaxAttempts: maxAttempts}}
}

func startService(t *testing.T, repository Repository, processor Processor) *Service {
	t.Helper()
	service, err := NewService(repository, testConfig(), processor, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	return service
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNoopProcessorCompletesWithoutMemoryOutput(t *testing.T) {
	item := pendingJob("noop", 1)
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	startService(t, repository, nil)
	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return item.job.Status == "succeeded"
	})
	if item.job.ResultCode == nil || *item.job.ResultCode != string(domain.ProcessingNoProcessor) {
		t.Fatalf("result code = %v", item.job.ResultCode)
	}
}

func TestProcessorHealthTracksConfiguredOutcomes(t *testing.T) {
	service, err := NewService(&fakeRepository{}, testConfig(), processorFunc(func(context.Context, domain.ProcessingInput) domain.ProcessingResult {
		return domain.ProcessingResult{Code: domain.ProcessingNoMemory}
	}), Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if state, identity := service.ProcessorHealth(); state != "degraded" || identity != "processor/in-process" {
		t.Fatalf("initial health = %q/%q", state, identity)
	}
	service.recordProcessorResult(domain.ProcessingResult{Code: domain.ProcessingNoMemory})
	if state, _ := service.ProcessorHealth(); state != "ready" {
		t.Fatalf("successful health = %q", state)
	}
	service.recordProcessorResult(domain.ProcessingResult{Code: domain.ProcessingTransientFailure})
	if state, _ := service.ProcessorHealth(); state != "unavailable" {
		t.Fatalf("failed health = %q", state)
	}
	service.recordProcessorResult(domain.ProcessingResult{Code: domain.ProcessingInvalidOutput})
	if state, _ := service.ProcessorHealth(); state != "degraded" {
		t.Fatalf("invalid-output health = %q", state)
	}

	noop, err := NewService(&fakeRepository{}, testConfig(), NoopProcessor{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	noop.recordProcessorResult(domain.ProcessingResult{Code: domain.ProcessingTransientFailure})
	if state, identity := noop.ProcessorHealth(); state != "not_configured" || identity != "none" {
		t.Fatalf("noop health = %q/%q", state, identity)
	}
}

type processorFunc func(context.Context, domain.ProcessingInput) domain.ProcessingResult

func (processorFunc) Enabled() bool    { return true }
func (processorFunc) Identity() string { return "processor/in-process" }

func (f processorFunc) Process(ctx context.Context, input domain.ProcessingInput) domain.ProcessingResult {
	return f(ctx, input)
}

type identifiedProcessor struct {
	id      string
	process func(context.Context, domain.ProcessingInput) domain.ProcessingResult
}

func (p identifiedProcessor) Enabled() bool    { return true }
func (p identifiedProcessor) Identity() string { return p.id }
func (p identifiedProcessor) Process(ctx context.Context, input domain.ProcessingInput) domain.ProcessingResult {
	return p.process(ctx, input)
}

func TestProcessClaimCapturesOneProcessingRuntime(t *testing.T) {
	item := pendingJob("captured-runtime", 1)
	item.job.Status = "running"
	item.job.AttemptCount = 1
	item.generation = 1
	claim := storage.ClaimedJob{Job: item.job, ProcessingVersion: "v1", LeaseOwner: "worker", LeaseGeneration: 1}
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	service, err := NewService(repository, testConfig(), nil, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	service.SetProcessing(ProcessingConfig{
		Processor: identifiedProcessor{id: "processor-old", process: func(ctx context.Context, input domain.ProcessingInput) domain.ProcessingResult {
			if input.ProcessorIdentity != "processor-old" {
				t.Errorf("provider input identity = %q", input.ProcessorIdentity)
			}
			close(started)
			select {
			case <-release:
				return domain.ProcessingResult{Code: domain.ProcessingNoMemory}
			case <-ctx.Done():
				return domain.ProcessingResult{Code: domain.ProcessingTransientFailure}
			}
		}},
		ProcessorIdentity: "processor-old",
		Enabled:           true,
		ProcessingVersion: "v1",
	})
	done := make(chan struct{})
	go func() {
		service.processClaim(context.Background(), claim)
		close(done)
	}()
	<-started
	service.SetProcessing(ProcessingConfig{Processor: identifiedProcessor{id: "processor-new", process: func(context.Context, domain.ProcessingInput) domain.ProcessingResult {
		return domain.ProcessingResult{Code: domain.ProcessingNoMemory}
	}}, ProcessorIdentity: "processor-new", Enabled: true, ProcessingVersion: "v2"})
	close(release)
	<-done
	if item.job.ResultCode == nil || *item.job.ResultCode != string(domain.ProcessingNoMemory) {
		t.Fatalf("job result = %+v", item.job)
	}
}

func TestProcessingAllowsTargetWithFilteredContextAndReadOnlyAncestor(t *testing.T) {
	item := pendingJob("mixed-context", 1)
	now := time.Now()
	prepared := &domain.ProcessingInput{
		Target:          domain.ProcessingEvent{EventID: "target", SpaceID: "space-a", SourceType: "message", Sensitivity: "normal", OccurredAt: now, Content: domain.ProcessingContent{Kind: "text", MediaType: "text/plain", Text: "allowed target"}},
		ContextEvents:   []domain.ProcessingEvent{{EventID: "allowed-event", SpaceID: "space-a", SourceType: "message", Sensitivity: "normal", OccurredAt: now, Content: domain.ProcessingContent{Kind: "text", MediaType: "text/plain", Text: "allowed context"}}},
		ContextMemories: []domain.ProcessingMemory{{MemoryID: "ancestor", RevisionID: "r1", SpaceID: "ancestor-space", Content: "read-only context", CurrentState: "active", Sensitivity: "normal", ProposalCapable: false}},
		OmissionSummary: map[string]int{"context_sensitivity_not_allowed": 1, "context_memory_sensitivity_not_allowed": 1},
		Policy:          domain.ProcessingPolicy{AllowedMemoryTypes: []string{"fact"}, MaxCandidates: 1, MaxContentChars: 100},
	}
	repository := &fakeRepository{jobs: []*fakeJob{item}, processing: prepared}
	startService(t, repository, processorFunc(func(_ context.Context, input domain.ProcessingInput) domain.ProcessingResult {
		if input.Target.EventID != "target" || len(input.ContextEvents) != 1 || len(input.ContextMemories) != 1 || input.ContextMemories[0].ProposalCapable {
			t.Fatalf("processing input = %+v", input)
		}
		if input.OmissionSummary["context_sensitivity_not_allowed"] != 1 || input.OmissionSummary["context_memory_sensitivity_not_allowed"] != 1 {
			t.Fatalf("omissions = %#v", input.OmissionSummary)
		}
		return domain.ProcessingResult{Code: domain.ProcessingNoMemory, OmittedContextCount: 2, OmissionSummary: input.OmissionSummary}
	}))
	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return item.job.Status == "succeeded"
	})
}

func TestTransientFailureUsesBoundedBackoffAndAttemptLimit(t *testing.T) {
	item := pendingJob("transient", 3)
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	startService(t, repository, processorFunc(func(context.Context, domain.ProcessingInput) domain.ProcessingResult {
		return domain.ProcessingResult{Code: domain.ProcessingTransientFailure, ReasonCode: "request_timeout_or_cancelled", Message: "busy"}
	}))
	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return item.job.Status == "failed"
	})
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.failures) != 3 {
		t.Fatalf("failure count = %d", len(repository.failures))
	}
	want := []time.Duration{10 * time.Millisecond, 15 * time.Millisecond, 15 * time.Millisecond}
	for i, failure := range repository.failures {
		if failure.Code != "request_timeout_or_cancelled" || failure.Backoff != want[i] {
			t.Fatalf("failure %d = %+v", i, failure)
		}
	}
}

func TestTransientFailureWithoutReasonUsesOutcomeCode(t *testing.T) {
	item := pendingJob("transient-fallback", 1)
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	startService(t, repository, processorFunc(func(context.Context, domain.ProcessingInput) domain.ProcessingResult {
		return domain.ProcessingResult{Code: domain.ProcessingTransientFailure}
	}))
	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return item.job.Status == "failed"
	})
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if item.failure.Code != string(domain.ProcessingTransientFailure) {
		t.Fatalf("failure = %+v", item.failure)
	}
}

type graphExtractorStub struct{}

func (graphExtractorStub) PrepareGraphExtraction(context.Context, storage.ClaimedJob, storage.ProcessingPreparationParams) (domain.GraphExtractionInput, error) {
	return domain.GraphExtractionInput{}, nil
}

func (graphExtractorStub) CommitGraphExtraction(context.Context, storage.ClaimedJob, domain.GraphExtractionInput, storage.GraphExtractionCommit) error {
	return nil
}

func TestGraphInvalidOutputRetriesTransiently(t *testing.T) {
	item := pendingJob("graph-invalid-output", 3)
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	service, err := NewService(repository, testConfig(), NoopProcessor{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimJob(context.Background(), storage.ClaimJobParams{WorkerID: "graph-worker", LeaseDuration: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	service.applyGraphOutcome(graphExtractorStub{}, claim, domain.GraphExtractionInput{}, domain.GraphExtractionResult{Code: domain.GraphExtractionInvalidOutput, ReasonCode: "invalid_graph_mention"})
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.failures) != 1 || !repository.failures[0].Transient {
		t.Fatalf("graph invalid output should be transient, failures=%+v", repository.failures)
	}
	if item.job.Status != "retrying" {
		t.Fatalf("job status=%s, want retrying", item.job.Status)
	}
}

func TestGraphPermanentFailureDoesNotRetry(t *testing.T) {
	item := pendingJob("graph-permanent", 3)
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	service, err := NewService(repository, testConfig(), NoopProcessor{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimJob(context.Background(), storage.ClaimJobParams{WorkerID: "graph-worker", LeaseDuration: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	service.applyGraphOutcome(graphExtractorStub{}, claim, domain.GraphExtractionInput{}, domain.GraphExtractionResult{Code: domain.GraphExtractionPermanentFailure, ReasonCode: "invalid_graph_extract_request"})
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.failures) != 1 || repository.failures[0].Transient {
		t.Fatalf("graph permanent failure should not be transient, failures=%+v", repository.failures)
	}
	if item.job.Status != "failed" {
		t.Fatalf("job status=%s, want failed", item.job.Status)
	}
}

func TestProcessorMessageIsNeverPersisted(t *testing.T) {
	item := pendingJob("malicious", 1)
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	startService(t, repository, processorFunc(func(context.Context, domain.ProcessingInput) domain.ProcessingResult {
		return domain.ProcessingResult{Code: domain.ProcessingTransientFailure, Message: "secret\nforged audit entry"}
	}))
	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return item.job.Status == "failed"
	})
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if item.failure.Message != "processor transient failure" {
		t.Fatalf("persisted failure message = %q", item.failure.Message)
	}
}

func TestInvalidProcessorResultPreservesValidationCode(t *testing.T) {
	item := pendingJob("invalid-output", 1)
	item.job.Status, item.job.AttemptCount = "running", 1
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	service, err := NewService(repository, testConfig(), nil, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	input := validProcessingInput()
	result := validProcessingResult()
	result.Proposals[0].Revision.Evidence[0].EventID = "event-3"
	service.applyOutcome(storage.ClaimedJob{Job: item.job}, input, result)
	if item.failure.Code != "invalid_evidence_reference" || item.failure.Message != "processor returned invalid output" || item.failure.Transient {
		t.Fatalf("failure = %+v", item.failure)
	}
}

type blockingIndexRepository struct {
	started   chan struct{}
	cancelled chan struct{}
}

type failingIndexRepository struct{ err error }

type rebuildRecorder struct{ calls chan string }

func (r rebuildRecorder) RebuildEntityIndexClaimed(context.Context, storage.ClaimedJob) error {
	r.calls <- "entity"
	return nil
}

func (r rebuildRecorder) RebuildRelationIndexClaimed(context.Context, storage.ClaimedJob) error {
	r.calls <- "relation"
	return nil
}

func (r rebuildRecorder) RebuildFeedbackAggregatesClaimed(context.Context, storage.ClaimedJob) error {
	r.calls <- "feedback"
	return nil
}

func (r rebuildRecorder) RebuildClaimed(context.Context, storage.ClaimedJob) error {
	r.calls <- "aggregate"
	return nil
}

func (r failingIndexRepository) RebuildFTSClaimed(context.Context, storage.ClaimedJob) error {
	return r.err
}

func (r failingIndexRepository) EmbedMemoryClaimed(context.Context, storage.ClaimedJob, string) error {
	return r.err
}

func (r failingIndexRepository) RebuildSemanticClaimed(context.Context, storage.ClaimedJob) error {
	return r.err
}

func TestEmbeddingPolicyRejectionBlocksWithoutRetry(t *testing.T) {
	item := pendingJob("embedding-policy", 3)
	item.job.JobType, item.job.TargetType, item.job.TargetID = "embed_memory", "memory_revision", "revision-1"
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	service, err := NewService(repository, testConfig(), nil, Dependencies{Indexes: failingIndexRepository{err: storage.ErrEmbeddingPolicy}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return item.job.Status == "blocked_policy"
	})
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if item.job.AttemptCount != 1 || item.job.BlockedReasonCode != "embedding_policy_denied" || len(repository.failures) != 0 {
		t.Fatalf("job = %+v, failures = %+v", item.job, repository.failures)
	}
}

func TestExplicitRebuildDependenciesDispatchIndependently(t *testing.T) {
	tests := []struct {
		name, jobType, targetType, targetID, call string
		dependencies                              func(rebuildRecorder) Dependencies
	}{
		{"entity", "rebuild_entity_index", "index", "entity_recall", "entity", func(r rebuildRecorder) Dependencies { return Dependencies{EntityIndexes: r} }},
		{"relation", "rebuild_relation_index", "index", "relation_recall", "relation", func(r rebuildRecorder) Dependencies { return Dependencies{RelationIndexes: r} }},
		{"feedback", "aggregate_feedback", "feedback_revision", "revision-1", "feedback", func(r rebuildRecorder) Dependencies { return Dependencies{FeedbackAggregates: r} }},
		{"feedback rebuild", "rebuild_feedback_aggregates", "index", "feedback_aggregates", "feedback", func(r rebuildRecorder) Dependencies { return Dependencies{FeedbackAggregates: r} }},
		{"aggregate", "rebuild_aggregate", "aggregate", "aggregate-1", "aggregate", func(r rebuildRecorder) Dependencies { return Dependencies{AggregateRebuilder: r} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := pendingJob(test.name, 1)
			item.job.JobType, item.job.TargetType, item.job.TargetID = test.jobType, test.targetType, test.targetID
			repository := &fakeRepository{jobs: []*fakeJob{item}}
			calls := make(chan string, 1)
			service, err := NewService(repository, testConfig(), nil, test.dependencies(rebuildRecorder{calls: calls}))
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = service.Shutdown(ctx)
			})
			select {
			case call := <-calls:
				if call != test.call {
					t.Fatalf("call = %q, want %q", call, test.call)
				}
			case <-time.After(time.Second):
				t.Fatal("rebuild dependency was not called")
			}
			waitFor(t, func() bool {
				repository.mu.Lock()
				defer repository.mu.Unlock()
				return item.job.Status == "succeeded"
			})
		})
	}
}

func (r *blockingIndexRepository) run(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	close(r.cancelled)
	return ctx.Err()
}

func (r *blockingIndexRepository) RebuildFTSClaimed(ctx context.Context, _ storage.ClaimedJob) error {
	return r.run(ctx)
}

func (r *blockingIndexRepository) EmbedMemoryClaimed(ctx context.Context, _ storage.ClaimedJob, _ string) error {
	return r.run(ctx)
}

func (r *blockingIndexRepository) RebuildSemanticClaimed(ctx context.Context, _ storage.ClaimedJob) error {
	return r.run(ctx)
}

func TestIndexClaimHeartbeatsAndCancellationStopsOperation(t *testing.T) {
	item := pendingJob("index-cancel", 2)
	item.job.JobType, item.job.TargetType, item.job.TargetID = "rebuild_fts", "index", "memory_fts"
	item.cancelRequested = true
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	indexes := &blockingIndexRepository{started: make(chan struct{}), cancelled: make(chan struct{})}
	service, err := NewService(repository, testConfig(), nil, Dependencies{Indexes: indexes})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	<-indexes.started
	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return item.job.Status == "cancelled" && repository.heartbeats > 0
	})
	select {
	case <-indexes.cancelled:
	case <-time.After(time.Second):
		t.Fatal("index operation context was not cancelled")
	}
}

func TestIndexClaimLeaseLossCancelsWithoutTerminalMutation(t *testing.T) {
	item := pendingJob("index-lease-loss", 2)
	item.job.JobType, item.job.TargetType, item.job.TargetID = "embed_memory", "memory_revision", "revision-1"
	repository := &fakeRepository{jobs: []*fakeJob{item}, heartbeatErr: storage.ErrLeaseLost}
	indexes := &blockingIndexRepository{started: make(chan struct{}), cancelled: make(chan struct{})}
	service, err := NewService(repository, testConfig(), nil, Dependencies{Indexes: indexes})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	<-indexes.started
	select {
	case <-indexes.cancelled:
	case <-time.After(time.Second):
		t.Fatal("index operation was not cancelled after lease loss")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if item.job.Status != "running" || len(repository.failures) != 0 || item.job.ResultCode != nil {
		t.Fatalf("job mutated after lease loss: %+v, failures=%d", item.job, len(repository.failures))
	}
}

func TestRunningCancellationCancelsProcessor(t *testing.T) {
	item := pendingJob("cancel", 2)
	item.cancelRequested = true
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	cancelled := make(chan struct{})
	startService(t, repository, processorFunc(func(ctx context.Context, _ domain.ProcessingInput) domain.ProcessingResult {
		<-ctx.Done()
		close(cancelled)
		return domain.ProcessingResult{Code: domain.ProcessingTransientFailure}
	}))
	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return item.job.Status == "cancelled"
	})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("processor context was not cancelled")
	}
}

func TestGracefulShutdownRetriesInterruptedClaim(t *testing.T) {
	item := pendingJob("shutdown", 2)
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	started := make(chan struct{})
	service := startService(t, repository, processorFunc(func(ctx context.Context, _ domain.ProcessingInput) domain.ProcessingResult {
		close(started)
		<-ctx.Done()
		return domain.ProcessingResult{Code: domain.ProcessingTransientFailure}
	}))
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if item.job.Status != "retrying" || item.failure.Code != "processor_shutdown" || !item.failure.Transient {
		t.Fatalf("job = %+v, failure = %+v", item.job, item.failure)
	}
}

func TestStartupAndPeriodicMaintenance(t *testing.T) {
	waiting := &fakeJob{job: domain.Job{JobID: "waiting", Status: "waiting", MaxAttempts: 1}}
	repository := &fakeRepository{jobs: []*fakeJob{waiting}, maintenance: make(chan struct{}, 4)}
	service := startService(t, repository, processorFunc(func(context.Context, domain.ProcessingInput) domain.ProcessingResult {
		return domain.ProcessingResult{Code: domain.ProcessingNoMemory}
	}))

	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return repository.recovered >= 1 && repository.expired >= 1 && repository.promoted >= 1 && waiting.job.Status == "succeeded"
	})
	select {
	case <-repository.maintenance:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("periodic maintenance did not run")
	}
	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return repository.recovered >= 2 && repository.expired >= 2 && repository.promoted >= 2
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPersistenceErrorLoggingIsStructured(t *testing.T) {
	handler := &captureHandler{}
	service, err := NewService(&fakeRepository{}, testConfig(), nil, Dependencies{Logger: slog.New(handler)})
	if err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("database unavailable")
	claim := storage.ClaimedJob{
		Job:             domain.Job{JobID: "job-123", TargetID: "private-target"},
		LeaseOwner:      "private-worker",
		LeaseGeneration: 7,
	}

	service.logPersistenceError(context.Background(), "commit_processing", claim, persistErr)

	records := handler.snapshot()
	if len(records) != 1 {
		t.Fatalf("log count = %d", len(records))
	}
	record := records[0]
	if record.message != "jobs persistence operation failed" {
		t.Fatalf("message = %q", record.message)
	}
	if record.attrs["component"] != "jobs" || record.attrs["operation"] != "commit_processing" {
		t.Fatalf("base attrs = %#v", record.attrs)
	}
	if record.attrs["job_id"] != "job-123" || record.attrs["lease_generation"] != int64(7) {
		t.Fatalf("claim attrs = %#v", record.attrs)
	}
	if record.attrs["error"] != persistErr {
		t.Fatalf("error attr = %#v", record.attrs["error"])
	}
	if _, ok := record.attrs["lease_owner"]; ok {
		t.Fatalf("lease owner was logged: %#v", record.attrs)
	}
	if _, ok := record.attrs["target_id"]; ok {
		t.Fatalf("target ID was logged: %#v", record.attrs)
	}
}

func TestPersistenceErrorLoggingSuppressesExpectedErrors(t *testing.T) {
	handler := &captureHandler{}
	service, err := NewService(&fakeRepository{}, testConfig(), nil, Dependencies{Logger: slog.New(handler)})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		nil,
		storage.ErrNotFound,
		fmt.Errorf("wrapped: %w", storage.ErrLeaseLost),
	} {
		service.logPersistenceError(context.Background(), "claim_job", storage.ClaimedJob{}, err)
	}
	shutdownCtx, cancel := context.WithCancel(context.Background())
	cancel()
	service.logPersistenceError(shutdownCtx, "claim_job", storage.ClaimedJob{}, fmt.Errorf("shutdown: %w", context.Canceled))
	if records := handler.snapshot(); len(records) != 0 {
		t.Fatalf("unexpected logs = %#v", records)
	}
}

func TestServiceAcceptsFullyStructuredCandidateFields(t *testing.T) {
	eventStart := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	eventEnd := time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)
	validFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	validUntil := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	start, end := int64(0), int64(5)
	input := domain.ProcessingInput{
		Target: domain.ProcessingEvent{EventID: "event-1", SpaceID: "space-1", Content: domain.ProcessingContent{Kind: "text", MediaType: "text/plain", Text: "alice prefers tea"}},
		Policy: domain.ProcessingPolicy{AllowedMemoryTypes: []string{"fact", "event"}, MaxCandidates: 2, MaxContentChars: 200},
	}
	processor := processorFunc(func(context.Context, domain.ProcessingInput) domain.ProcessingResult {
		return domain.ProcessingResult{Code: domain.ProcessingCandidatesExtracted, Proposals: []domain.MemoryCandidateProposal{{
			Action: domain.ConsolidationCreate,
			Revision: domain.MemoryRevision{
				Kind: domain.MemoryKindEvent, DisplayContent: "Alice prefers tea", CanonicalContent: "alice prefers tea",
				SubjectEntityIDs: []string{"alice"}, ObjectEntityIDs: []string{"tea"}, EventType: "preference",
				Attributes:     map[string]string{"preference": "tea"},
				EventTimeStart: &eventStart, EventTimeEnd: &eventEnd, TimePrecision: domain.TimePrecisionExact, TimeSource: "relative_anchor",
				ValidFrom: &validFrom, ValidUntil: &validUntil, SourceSpeaker: "alice", Language: "en",
				Sensitivity: "normal", Specificity: 0.7, Confidence: 0.9,
				Evidence: []domain.RevisionEvidence{{EvidenceID: "ev-1", EventID: "event-1", Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}},
			},
		}}}
	})
	item := pendingJob("structured", 1)
	repository := &fakeRepository{jobs: []*fakeJob{item}, processing: &input}
	startService(t, repository, processor)
	waitFor(t, func() bool {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		return item.job.Status == "succeeded"
	})
	if item.job.ResultCode == nil || *item.job.ResultCode != string(domain.ProcessingCandidatesExtracted) {
		t.Fatalf("structured candidate was not published: %v", item.job.ResultCode)
	}
}
