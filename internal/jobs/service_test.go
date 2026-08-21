package jobs

import (
	"context"
	"errors"
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
	mu          sync.Mutex
	jobs        []*fakeJob
	recovered   int
	expired     int
	promoted    int
	heartbeats  int
	failures    []storage.JobFailure
	maintenance chan struct{}
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
	return item.cancelRequested, nil
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
	service, err := NewService(repository, testConfig(), processor)
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
	if item.job.ResultCode == nil || *item.job.ResultCode != string(OutcomeNoProcessor) {
		t.Fatalf("result code = %v", item.job.ResultCode)
	}
}

type processorFunc func(context.Context, domain.Job) Outcome

func (f processorFunc) Process(ctx context.Context, job domain.Job) Outcome { return f(ctx, job) }

func TestTransientFailureUsesBoundedBackoffAndAttemptLimit(t *testing.T) {
	item := pendingJob("transient", 3)
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	startService(t, repository, processorFunc(func(context.Context, domain.Job) Outcome {
		return Outcome{Code: OutcomeTransientFailure, Message: "busy"}
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
		if failure.Code != string(OutcomeTransientFailure) || failure.Backoff != want[i] {
			t.Fatalf("failure %d = %+v", i, failure)
		}
	}
}

func TestRunningCancellationCancelsProcessor(t *testing.T) {
	item := pendingJob("cancel", 2)
	item.cancelRequested = true
	repository := &fakeRepository{jobs: []*fakeJob{item}}
	cancelled := make(chan struct{})
	startService(t, repository, processorFunc(func(ctx context.Context, _ domain.Job) Outcome {
		<-ctx.Done()
		close(cancelled)
		return Outcome{Code: OutcomeTransientFailure}
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
	service := startService(t, repository, processorFunc(func(ctx context.Context, _ domain.Job) Outcome {
		close(started)
		<-ctx.Done()
		return Outcome{Code: OutcomeTransientFailure}
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
	service := startService(t, repository, processorFunc(func(context.Context, domain.Job) Outcome {
		return Outcome{Code: OutcomeNoMemory}
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
