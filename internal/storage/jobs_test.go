package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

type jobTestRuntime struct {
	now time.Time
	id  int
	mu  sync.Mutex
}

func (r *jobTestRuntime) runtime() domain.Runtime {
	return domain.Runtime{
		Now: func() time.Time { return r.now },
		ID: func() (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.id++
			return fmt.Sprintf("generated-%03d", r.id), nil
		},
	}
}

func newJobTestRepository(t *testing.T) (*Repository, *jobTestRuntime) {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "jobs.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runtime := &jobTestRuntime{now: time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)}
	return NewRepository(store, runtime.runtime(), 10, 20, 3, "v1"), runtime
}

func insertTestJob(t *testing.T, repository *Repository, id, status string, priority, attempts, maxAttempts int, nextRunAt, createdAt string) {
	t.Helper()
	_, err := repository.store.writer.Exec(`INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, priority, attempt_count, max_attempts, next_run_at, created_at) VALUES (?, ?, 'extract_memory', 'raw_event', ?, 'v1', ?, ?, ?, ?, ?, ?)`, id, "operation-"+id, "target-"+id, status, priority, attempts, maxAttempts, nextRunAt, createdAt)
	if err != nil {
		t.Fatal(err)
	}
}

func testWriteIdentity(key, hash string) WriteIdentity {
	return WriteIdentity{
		CallerID:       "caller",
		IdempotencyKey: key,
		RequestID:      "request-" + key,
		Method:         "POST",
		Route:          "/v1/jobs/{job_id}/action",
		RequestHash:    hash,
	}
}

func TestJOB001GetJobAndAtomicClaimOrdering(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	before := formatTime(clock.now.Add(-time.Minute))
	after := formatTime(clock.now.Add(time.Minute))
	insertTestJob(t, repository, "job-later", "pending", 5, 0, 3, now, now)
	insertTestJob(t, repository, "job-first", "retrying", 5, 1, 3, before, now)
	insertTestJob(t, repository, "job-low", "pending", 1, 0, 3, before, before)
	insertTestJob(t, repository, "job-future", "pending", 100, 0, 3, after, before)
	insertTestJob(t, repository, "job-waiting", "waiting", 200, 0, 3, before, before)

	job, err := repository.GetJob(ctx, "job-first")
	if err != nil || job.Status != "retrying" || job.AttemptCount != 1 || job.OperationID != "operation-job-first" {
		t.Fatalf("GetJob() = %+v, %v", job, err)
	}
	if _, err := repository.GetJob(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetJob(missing) error = %v; want ErrNotFound", err)
	}

	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker-a", LeaseDuration: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Job.JobID != "job-first" || claim.Job.Status != "running" || claim.Job.AttemptCount != 2 || claim.LeaseGeneration != 1 || claim.LeaseOwner != "worker-a" {
		t.Fatalf("ClaimJob() = %+v", claim)
	}
	if claim.LeaseUntil != formatTime(clock.now.Add(30*time.Second)) {
		t.Fatalf("lease until = %q", claim.LeaseUntil)
	}
	var attempts, generation int
	var worker string
	if err := repository.store.reader.QueryRow(`SELECT count(*), lease_generation, worker_id FROM job_attempts WHERE job_id='job-first'`).Scan(&attempts, &generation, &worker); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || generation != 1 || worker != "worker-a" {
		t.Fatalf("attempt = count %d, generation %d, worker %q", attempts, generation, worker)
	}
}

func TestJOB001ConcurrentClaimHasOneWinner(t *testing.T) {
	t.Parallel()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "job-race", "pending", 0, 0, 3, now, now)

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, workerID := range []string{"worker-a", "worker-b"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := repository.ClaimJob(context.Background(), ClaimJobParams{WorkerID: workerID, LeaseDuration: time.Minute})
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	winners, empty := 0, 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrNotFound):
			empty++
		default:
			t.Fatalf("ClaimJob() error = %v", err)
		}
	}
	if winners != 1 || empty != 1 {
		t.Fatalf("claim results winners=%d empty=%d", winners, empty)
	}
	var attempts, generation int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM job_attempts WHERE job_id='job-race'), lease_generation FROM jobs WHERE job_id='job-race'`).Scan(&attempts, &generation); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || generation != 1 {
		t.Fatalf("attempts/generation = %d/%d", attempts, generation)
	}
}

func TestJOB002HeartbeatCompletionAndGenerationFencing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "job-1", "pending", 0, 0, 3, now, now)
	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker-a", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	clock.now = clock.now.Add(10 * time.Second)
	cancelled, err := repository.HeartbeatJob(ctx, claim, 2*time.Minute)
	if err != nil || cancelled {
		t.Fatalf("HeartbeatJob() = %t, %v", cancelled, err)
	}
	var leaseUntil string
	if err := repository.store.reader.QueryRow(`SELECT lease_until FROM jobs WHERE job_id='job-1'`).Scan(&leaseUntil); err != nil {
		t.Fatal(err)
	}
	if leaseUntil != formatTime(clock.now.Add(2*time.Minute)) {
		t.Fatalf("lease_until = %q", leaseUntil)
	}

	stale := claim
	stale.LeaseGeneration++
	if _, err := repository.HeartbeatJob(ctx, stale, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale HeartbeatJob() error = %v", err)
	}
	if err := repository.CompleteJob(ctx, stale, JobResult{Code: "ok", Value: map[string]any{"count": 1}}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale CompleteJob() error = %v", err)
	}
	if err := repository.CompleteJob(ctx, claim, JobResult{Code: "ok", Value: map[string]any{"count": 1}}); err != nil {
		t.Fatal(err)
	}
	job, err := repository.GetJob(ctx, "job-1")
	if err != nil || job.Status != "succeeded" || job.ResultCode == nil || *job.ResultCode != "ok" || job.FinishedAt == nil {
		t.Fatalf("completed job = %+v, %v", job, err)
	}
	result, ok := job.Result.(map[string]any)
	if !ok || result["count"] != json.Number("1") {
		t.Fatalf("result = %#v", job.Result)
	}
	var outcome, attemptResult string
	if err := repository.store.reader.QueryRow(`SELECT outcome, result_code FROM job_attempts WHERE job_id='job-1'`).Scan(&outcome, &attemptResult); err != nil {
		t.Fatal(err)
	}
	if outcome != "succeeded" || attemptResult != "ok" {
		t.Fatalf("attempt outcome = %q, result = %q", outcome, attemptResult)
	}
}

func TestJOB003FailureRetriesWithBackoffThenStopsAtMaximum(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "job-retry", "pending", 0, 0, 2, now, now)
	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker-a", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FailJob(ctx, claim, JobFailure{Code: "busy", Message: "try later", Transient: true, Backoff: 45 * time.Second}); err != nil {
		t.Fatal(err)
	}
	job, err := repository.GetJob(ctx, "job-retry")
	if err != nil || job.Status != "retrying" || job.NextRunAt != formatTime(clock.now.Add(45*time.Second)) || job.FinishedAt != nil || job.LastError == nil || job.LastError.Code != "busy" {
		t.Fatalf("retrying job = %+v, %v", job, err)
	}
	if _, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "too-early", LeaseDuration: time.Minute}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("early ClaimJob() error = %v", err)
	}

	clock.now = clock.now.Add(45 * time.Second)
	claim, err = repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker-b", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FailJob(ctx, claim, JobFailure{Code: "busy", Message: "still busy", Transient: true, Backoff: time.Hour}); err != nil {
		t.Fatal(err)
	}
	job, err = repository.GetJob(ctx, "job-retry")
	if err != nil || job.Status != "failed" || job.AttemptCount != 2 || job.FinishedAt == nil {
		t.Fatalf("failed job = %+v, %v", job, err)
	}
	var retrying, failed int
	if err := repository.store.reader.QueryRow(`SELECT sum(outcome='retrying'), sum(outcome='failed') FROM job_attempts WHERE job_id='job-retry'`).Scan(&retrying, &failed); err != nil {
		t.Fatal(err)
	}
	if retrying != 1 || failed != 1 {
		t.Fatalf("attempt outcomes retrying=%d failed=%d", retrying, failed)
	}
}

func TestJOB004CancelPendingIsImmediateIdempotentAndAudited(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "job-cancel", "waiting", 0, 0, 3, now, now)
	identity := testWriteIdentity("cancel-key", HashRequest(map[string]string{"job_id": "job-cancel"}))

	job, replayed, err := repository.CancelJob(ctx, identity, "job-cancel")
	if err != nil || replayed || job.Status != "cancelled" || job.CancelRequestedAt == nil || job.FinishedAt == nil {
		t.Fatalf("CancelJob() = %+v, %t, %v", job, replayed, err)
	}
	replayedJob, replayed, err := repository.CancelJob(ctx, identity, "job-cancel")
	if err != nil || !replayed || replayedJob.Status != "cancelled" || *replayedJob.FinishedAt != *job.FinishedAt {
		t.Fatalf("replayed CancelJob() = %+v, %t, %v", replayedJob, replayed, err)
	}
	var idempotency, audits int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM idempotency_records), (SELECT count(*) FROM audit_events WHERE action='cancel_job' AND target_id='job-cancel')`).Scan(&idempotency, &audits); err != nil {
		t.Fatal(err)
	}
	if idempotency != 1 || audits != 1 {
		t.Fatalf("idempotency records = %d, audits = %d", idempotency, audits)
	}
	conflict := identity
	conflict.RequestHash = "different"
	if _, _, err := repository.CancelJob(ctx, conflict, "job-cancel"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("CancelJob(conflict) error = %v", err)
	}
}

func TestJOB005RunningCancelIsObservedAndCooperative(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "job-running", "pending", 0, 0, 3, now, now)
	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker-a", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	job, replayed, err := repository.CancelJob(ctx, testWriteIdentity("running-cancel", "hash"), "job-running")
	if err != nil || replayed || job.Status != "running" || job.CancelRequestedAt == nil {
		t.Fatalf("CancelJob(running) = %+v, %t, %v", job, replayed, err)
	}
	requested, err := repository.HeartbeatJob(ctx, claim, time.Minute)
	if err != nil || !requested {
		t.Fatalf("HeartbeatJob() = %t, %v", requested, err)
	}
	if err := repository.CancelClaimedJob(ctx, claim); err != nil {
		t.Fatal(err)
	}
	job, err = repository.GetJob(ctx, "job-running")
	if err != nil || job.Status != "cancelled" || job.FinishedAt == nil {
		t.Fatalf("cancelled job = %+v, %v", job, err)
	}
	if err := repository.CompleteJob(ctx, claim, JobResult{Code: "late"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("late CompleteJob() error = %v", err)
	}
	var outcome string
	if err := repository.store.reader.QueryRow(`SELECT outcome FROM job_attempts WHERE job_id='job-running'`).Scan(&outcome); err != nil || outcome != "cancelled" {
		t.Fatalf("attempt outcome = %q, %v", outcome, err)
	}
}

func TestJOB006ExplicitFailedRetryPreservesAttemptsAndAddsBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "job-failed", "failed", 0, 3, 3, now, now)
	if _, err := repository.store.writer.Exec(`UPDATE jobs SET last_error_code='terminal', last_error_message='failed', finished_at=? WHERE job_id='job-failed'`, now); err != nil {
		t.Fatal(err)
	}
	for generation := 1; generation <= 3; generation++ {
		if _, err := repository.store.writer.Exec(`INSERT INTO job_attempts(attempt_id, job_id, lease_generation, worker_id, started_at, finished_at, outcome) VALUES (?, 'job-failed', ?, 'worker', ?, ?, 'failed')`, fmt.Sprintf("attempt-%d", generation), generation, now, now); err != nil {
			t.Fatal(err)
		}
	}
	identity := testWriteIdentity("retry-key", "retry-hash")
	job, replayed, err := repository.RetryJob(ctx, identity, "job-failed")
	if err != nil || replayed || job.Status != "pending" || job.AttemptCount != 3 || job.MaxAttempts != 4 || job.LastError != nil || job.FinishedAt != nil {
		t.Fatalf("RetryJob() = %+v, %t, %v", job, replayed, err)
	}
	if job.NextRunAt != now {
		t.Fatalf("next_run_at = %q; want %q", job.NextRunAt, now)
	}
	job, replayed, err = repository.RetryJob(ctx, identity, "job-failed")
	if err != nil || !replayed || job.MaxAttempts != 4 {
		t.Fatalf("replayed RetryJob() = %+v, %t, %v", job, replayed, err)
	}
	var attempts int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM job_attempts WHERE job_id='job-failed'`).Scan(&attempts); err != nil || attempts != 3 {
		t.Fatalf("attempt count = %d, %v", attempts, err)
	}
	if _, _, err := repository.RetryJob(ctx, testWriteIdentity("second-retry", "hash"), "job-failed"); !errors.Is(err, ErrInvalidJobState) {
		t.Fatalf("RetryJob(non-failed) error = %v", err)
	}
}

func TestJOB007ExpiredLeaseRecoveryClosesAttemptAndFencesWorker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "job-recover", "pending", 0, 0, 3, now, now)
	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "dead-worker", LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	count, err := repository.RecoverExpiredLeases(ctx)
	if err != nil || count != 1 {
		t.Fatalf("RecoverExpiredLeases() = %d, %v", count, err)
	}
	job, err := repository.GetJob(ctx, "job-recover")
	if err != nil || job.Status != "retrying" || job.LastError == nil || job.LastError.Code != "lease_lost" {
		t.Fatalf("recovered job = %+v, %v", job, err)
	}
	if err := repository.CompleteJob(ctx, claim, JobResult{Code: "stale"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale CompleteJob() error = %v", err)
	}
	var outcome, errorCode string
	var generation int
	if err := repository.store.reader.QueryRow(`SELECT outcome, error_code FROM job_attempts WHERE job_id='job-recover'`).Scan(&outcome, &errorCode); err != nil {
		t.Fatal(err)
	}
	if err := repository.store.reader.QueryRow(`SELECT lease_generation FROM jobs WHERE job_id='job-recover'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if outcome != "lease_lost" || errorCode != "lease_lost" || generation != claim.LeaseGeneration+1 {
		t.Fatalf("recovery outcome=%q error=%q generation=%d", outcome, errorCode, generation)
	}
}

func TestExpiredLeaseCannotCompleteOrFailBeforeRecovery(t *testing.T) {
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "expired-fence", "pending", 0, 0, 3, now, now)
	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "late-worker", LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Second)

	if err := repository.CompleteJob(ctx, claim, JobResult{Code: "late"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired CompleteJob() error = %v", err)
	}
	if err := repository.FailJob(ctx, claim, JobFailure{Code: "late", Message: "late", Transient: true}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired FailJob() error = %v", err)
	}
	job, err := repository.GetJob(ctx, claim.Job.JobID)
	if err != nil || job.Status != "running" || job.ResultCode != nil || job.LastError != nil {
		t.Fatalf("job before recovery = %+v, %v", job, err)
	}
	if count, err := repository.RecoverExpiredLeases(ctx); err != nil || count != 1 {
		t.Fatalf("RecoverExpiredLeases() = %d, %v", count, err)
	}
}

func TestStaleGenerationRequeuesWithoutConsumingProviderRetryBudget(t *testing.T) {
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "stale-generation", "pending", 0, 0, 3, now, now)
	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ReevaluateStaleJob(ctx, claim); err != nil {
		t.Fatal(err)
	}
	job, err := repository.GetJob(ctx, claim.Job.JobID)
	if err != nil || job.Status != "pending" || job.AttemptCount != 1 || job.ProviderAttemptCount != 0 {
		t.Fatalf("stale re-evaluation job = %+v, %v", job, err)
	}
	var outcome, code string
	if err := repository.store.reader.QueryRow(`SELECT outcome, error_code FROM job_attempts WHERE job_id=?`, claim.Job.JobID).Scan(&outcome, &code); err != nil {
		t.Fatal(err)
	}
	if outcome != "stale_generation" || code != "stale_generation" {
		t.Fatalf("attempt = outcome %q code %q", outcome, code)
	}
}

func TestRepeatedStaleGenerationsRemainClaimable(t *testing.T) {
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "repeat-stale", "pending", 0, 0, 1, now, now)
	if _, err := repository.store.writer.Exec(`UPDATE jobs SET capability='processing' WHERE job_id='repeat-stale'`); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: fmt.Sprintf("worker-%d", attempt), LeaseDuration: time.Minute})
		if err != nil {
			t.Fatalf("claim %d: %v", attempt, err)
		}
		if err := repository.ReevaluateStaleJob(ctx, claim); err != nil {
			t.Fatal(err)
		}
	}
	job, err := repository.GetJob(ctx, "repeat-stale")
	if err != nil || job.Status != "pending" || job.AttemptCount != 3 || job.ProviderAttemptCount != 0 {
		t.Fatalf("job = %+v, %v", job, err)
	}
}

func TestBlockJobRejectsUnsafeReasonCode(t *testing.T) {
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "unsafe-block", "pending", 0, 0, 3, now, now)
	claim, err := repository.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "worker", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.BlockJob(context.Background(), claim, "private content\nleak"); !errors.Is(err, ErrInvalidJobState) {
		t.Fatalf("BlockJob() error = %v", err)
	}
}

func TestSupersedeBlockedJobClosesAttemptAndPreventsRequeue(t *testing.T) {
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "superseded-job", "pending", 0, 0, 1, now, now)
	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.BlockJob(ctx, claim, "target_policy_denied"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SupersedeJob(ctx, "superseded-job", "target_superseded"); err != nil {
		t.Fatal(err)
	}
	job, err := repository.GetJob(ctx, "superseded-job")
	if err != nil || job.Status != "superseded" || job.BlockedReasonCode != "target_superseded" || job.FinishedAt == nil {
		t.Fatalf("superseded job = %+v, %v", job, err)
	}
	if _, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "other-worker", LeaseDuration: time.Minute}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ClaimJob(superseded) error = %v", err)
	}
}

func TestSupersedeRunningJobFencesLateProviderResult(t *testing.T) {
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "running-superseded-job", "pending", 0, 0, 1, now, now)
	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SupersedeJob(ctx, claim.Job.JobID, "target_superseded"); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteJob(ctx, claim, JobResult{Code: "late"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("CompleteJob() error = %v", err)
	}
	var outcome, code string
	if err := repository.store.reader.QueryRow(`SELECT outcome, error_code FROM job_attempts WHERE job_id=?`, claim.Job.JobID).Scan(&outcome, &code); err != nil {
		t.Fatal(err)
	}
	if outcome != "superseded" || code != "target_superseded" {
		t.Fatalf("attempt = outcome %q code %q", outcome, code)
	}
}

func TestMarkProviderAttemptPersistsCallProvenanceAndBudget(t *testing.T) {
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	insertTestJob(t, repository, "provider-attempt-job", "pending", 0, 0, 2, now, now)
	if _, err := repository.store.writer.Exec(`UPDATE jobs SET capability='processing', configuration_generation=7, policy_generation=9, compatibility_identity='compatibility-v1' WHERE job_id='provider-attempt-job'`); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`UPDATE jobs SET configuration_generation=7, policy_generation=9 WHERE job_id=?`, claim.Job.JobID); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkProviderAttempt(ctx, claim); err != nil {
		t.Fatal(err)
	}
	var configurationGeneration, policyGeneration, attempts int
	var compatibility, startedAt string
	if err := repository.store.reader.QueryRow(`SELECT a.configuration_generation, a.policy_generation, a.compatibility_identity, a.external_call_started_at, j.provider_attempt_count FROM job_attempts a JOIN jobs j ON j.job_id=a.job_id WHERE a.job_id=?`, claim.Job.JobID).Scan(&configurationGeneration, &policyGeneration, &compatibility, &startedAt, &attempts); err != nil {
		t.Fatal(err)
	}
	if configurationGeneration != 7 || policyGeneration != 9 || compatibility != "compatibility-v1" || startedAt != now || attempts != 1 {
		t.Fatalf("provider attempt = configuration=%d policy=%d compatibility=%q started=%q attempts=%d", configurationGeneration, policyGeneration, compatibility, startedAt, attempts)
	}
}

func TestJobMaintenancePromotionRecoveryTerminalStatesAndExpiration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	old := formatTime(clock.now.Add(-time.Hour))
	insertTestJob(t, repository, "waiting-low", "waiting", 1, 0, 3, now, old)
	insertTestJob(t, repository, "waiting-high", "waiting", 5, 0, 3, now, now)
	count, err := repository.PromoteWaitingJobs(ctx, 1)
	if err != nil || count != 1 {
		t.Fatalf("PromoteWaitingJobs() = %d, %v", count, err)
	}
	high, _ := repository.GetJob(ctx, "waiting-high")
	low, _ := repository.GetJob(ctx, "waiting-low")
	if high.Status != "pending" || low.Status != "waiting" {
		t.Fatalf("promotion statuses high=%q low=%q", high.Status, low.Status)
	}

	claim, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker", LeaseDuration: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Job.JobID != "waiting-high" {
		t.Fatalf("first claim = %q; want promoted high-priority job", claim.Job.JobID)
	}
	if err := repository.CompleteJob(ctx, claim, JobResult{Code: "done"}); err != nil {
		t.Fatal(err)
	}
	insertTestJob(t, repository, "expired-pending", "pending", 0, 0, 3, old, old)
	if _, err := repository.store.writer.Exec(`UPDATE jobs SET expires_at=? WHERE job_id='expired-pending'`, old); err != nil {
		t.Fatal(err)
	}
	count, err = repository.ExpireJobs(ctx)
	if err != nil || count != 1 {
		t.Fatalf("ExpireJobs() = %d, %v", count, err)
	}
	expired, err := repository.GetJob(ctx, "expired-pending")
	if err != nil || expired.Status != "expired" || expired.FinishedAt == nil {
		t.Fatalf("expired job = %+v, %v", expired, err)
	}

	insertTestJob(t, repository, "recover-exhausted", "pending", 0, 0, 1, now, now)
	exhausted, err := repository.ClaimJob(ctx, ClaimJobParams{WorkerID: "dead", LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	if count, err := repository.RecoverExpiredLeases(ctx); err != nil || count != 1 {
		t.Fatalf("RecoverExpiredLeases(exhausted) = %d, %v", count, err)
	}
	exhaustedJob, _ := repository.GetJob(ctx, exhausted.Job.JobID)
	if exhaustedJob.Status != "failed" || exhaustedJob.FinishedAt == nil {
		t.Fatalf("exhausted recovered job = %+v", exhaustedJob)
	}
}

func TestBlockedPolicyEmbedJobDoesNotBlockSemanticRebuild(t *testing.T) {
	ctx := context.Background()
	repo, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	repo.SetEmbedding(nil, "embedding-provider", "embedding-model", "compat-v1", "config-v1", 1, 1)

	embedIdentity := repo.embeddingIdentity()
	if _, err := repo.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,capability,configuration_generation,policy_generation,blocked_reason_code,created_at) VALUES ('blocked-embed','op-blocked','embed_memory','memory_revision','rev-blocked',?,'blocked_policy',0,3,?,'embedding',1,1,'target_policy_denied',?)`, embedIdentity, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,capability,configuration_generation,policy_generation,created_at) VALUES ('rebuild-job','op-rebuild','rebuild_semantic','index','memory_semantic',?,'pending',0,3,?,'embedding',1,1,?)`, embedIdentity, now, now); err != nil {
		t.Fatal(err)
	}

	claim, err := repo.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatalf("ClaimJob() error = %v; rebuild_semantic should be claimable despite blocked_policy embed job", err)
	}
	if claim.Job.JobID != "rebuild-job" {
		t.Fatalf("ClaimJob() = %q; want rebuild-job", claim.Job.JobID)
	}

	if err := repo.CompleteJob(ctx, claim, JobResult{Code: "rebuilt"}); err != nil {
		t.Fatalf("CompleteJob() error = %v", err)
	}
	rebuilt, err := repo.GetJob(ctx, "rebuild-job")
	if err != nil || rebuilt.Status != "succeeded" {
		t.Fatalf("rebuild job = %+v, %v", rebuilt, err)
	}
}

func TestPendingEmbedJobStillBlocksSemanticRebuild(t *testing.T) {
	ctx := context.Background()
	repo, clock := newJobTestRepository(t)
	now := formatTime(clock.now)
	repo.SetEmbedding(nil, "embedding-provider", "embedding-model", "compat-v1", "config-v1", 1, 1)

	embedIdentity := repo.embeddingIdentity()
	if _, err := repo.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,capability,configuration_generation,policy_generation,created_at) VALUES ('pending-embed','op-pending','embed_memory','memory_revision','rev-pending',?,'pending',0,3,?,'embedding',1,1,?)`, embedIdentity, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,capability,configuration_generation,policy_generation,created_at) VALUES ('rebuild-blocked','op-rebuild-blocked','rebuild_semantic','index','memory_semantic',?,'pending',0,3,?,'embedding',1,1,?)`, embedIdentity, now, now); err != nil {
		t.Fatal(err)
	}

	claim, err := repo.ClaimJob(ctx, ClaimJobParams{WorkerID: "worker", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	if claim.Job.JobID != "pending-embed" {
		t.Fatalf("ClaimJob() = %q; want pending-embed (rebuild should be blocked by pending embed)", claim.Job.JobID)
	}
}
