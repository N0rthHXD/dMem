package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

var (
	ErrLeaseLost       = errors.New("job lease lost")
	ErrInvalidJobState = errors.New("invalid job state")
)

type ClaimJobParams struct {
	WorkerID      string
	LeaseDuration time.Duration
}

type ClaimedJob struct {
	Job               domain.Job
	ProcessingVersion string
	AttemptID         string
	LeaseOwner        string
	LeaseGeneration   int
	LeaseUntil        string
}

type JobResult struct {
	Code  string
	Value any
}

type JobFailure struct {
	Code      string
	Message   string
	Transient bool
	Backoff   time.Duration
}

const jobSelect = `SELECT job_id, operation_id, job_type, target_type, target_id, processing_version, processor_identity, status, priority, attempt_count, max_attempts, next_run_at, cancel_requested_at, result_code, result_json, last_error_code, last_error_message, created_at, started_at, finished_at, expires_at FROM jobs`

func (r *Repository) GetJob(ctx context.Context, jobID string) (domain.Job, error) {
	return scanJob(r.store.reader.QueryRowContext(ctx, jobSelect+` WHERE job_id=?`, jobID))
}

func (r *Repository) ClaimJob(ctx context.Context, params ClaimJobParams) (ClaimedJob, error) {
	if params.WorkerID == "" || params.LeaseDuration <= 0 {
		return ClaimedJob{}, fmt.Errorf("%w: invalid claim parameters", ErrInvalidJobState)
	}
	attemptID, err := r.runtime.ID()
	if err != nil {
		return ClaimedJob{}, err
	}
	now := r.runtime.Now().UTC()
	nowText, leaseUntil := formatTime(now), formatTime(now.Add(params.LeaseDuration))
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return ClaimedJob{}, err
	}
	defer tx.Rollback()

	var jobID string
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM jobs WHERE status IN ('pending','retrying') AND next_run_at<=? AND attempt_count<max_attempts AND (expires_at IS NULL OR expires_at>?) ORDER BY priority DESC, next_run_at ASC, created_at ASC, job_id ASC LIMIT 1`, nowText, nowText).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimedJob{}, ErrNotFound
	}
	if err != nil {
		return ClaimedJob{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='running', attempt_count=attempt_count+1, lease_owner=?, lease_until=?, lease_generation=lease_generation+1, started_at=coalesce(started_at, ?), finished_at=NULL WHERE job_id=? AND status IN ('pending','retrying') AND next_run_at<=? AND attempt_count<max_attempts`, params.WorkerID, leaseUntil, nowText, jobID, nowText)
	if err != nil {
		return ClaimedJob{}, err
	}
	if !oneRow(result) {
		return ClaimedJob{}, ErrLeaseLost
	}
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT lease_generation FROM jobs WHERE job_id=?`, jobID).Scan(&generation); err != nil {
		return ClaimedJob{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO job_attempts(attempt_id, job_id, lease_generation, worker_id, started_at) VALUES (?, ?, ?, ?, ?)`, attemptID, jobID, generation, params.WorkerID, nowText); err != nil {
		return ClaimedJob{}, err
	}
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` WHERE job_id=?`, jobID))
	if err != nil {
		return ClaimedJob{}, err
	}
	var processingVersion string
	if err := tx.QueryRowContext(ctx, `SELECT processing_version FROM jobs WHERE job_id=?`, jobID).Scan(&processingVersion); err != nil {
		return ClaimedJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClaimedJob{}, err
	}
	return ClaimedJob{Job: job, ProcessingVersion: processingVersion, AttemptID: attemptID, LeaseOwner: params.WorkerID, LeaseGeneration: generation, LeaseUntil: leaseUntil}, nil
}

func (r *Repository) HeartbeatJob(ctx context.Context, claim ClaimedJob, leaseDuration time.Duration) (bool, error) {
	if leaseDuration <= 0 {
		return false, fmt.Errorf("%w: invalid lease duration", ErrInvalidJobState)
	}
	leaseUntil := formatTime(r.runtime.Now().UTC().Add(leaseDuration))
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := formatTime(r.runtime.Now())
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET lease_until=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, leaseUntil, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, now)
	if err != nil {
		return false, err
	}
	if !oneRow(result) {
		return false, ErrLeaseLost
	}
	var requested sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT cancel_requested_at FROM jobs WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=?`, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration).Scan(&requested); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrLeaseLost
		}
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return requested.Valid, nil
}

func (r *Repository) CompleteJob(ctx context.Context, claim ClaimedJob, completion JobResult) error {
	encoded, err := json.Marshal(completion.Value)
	if err != nil {
		return err
	}
	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='succeeded', lease_owner=NULL, lease_until=NULL, result_code=?, result_json=?, last_error_code=NULL, last_error_message=NULL, finished_at=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=?`, completion.Code, string(encoded), now, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	if err := closeAttempt(ctx, tx, claim, now, "succeeded", completion.Code, "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) FailJob(ctx context.Context, claim ClaimedJob, failure JobFailure) error {
	now := r.runtime.Now().UTC()
	nowText := formatTime(now)
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var attempts, maxAttempts int
	if err := tx.QueryRowContext(ctx, `SELECT attempt_count, max_attempts FROM jobs WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=?`, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration).Scan(&attempts, &maxAttempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	status, outcome, finishedAt, nextRunAt := "failed", "failed", any(nowText), nowText
	if failure.Transient && attempts < maxAttempts {
		status, outcome, finishedAt, nextRunAt = "retrying", "retrying", nil, formatTime(now.Add(failure.Backoff))
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status=?, lease_owner=NULL, lease_until=NULL, next_run_at=?, result_code=NULL, result_json=NULL, last_error_code=?, last_error_message=?, finished_at=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=?`, status, nextRunAt, failure.Code, failure.Message, finishedAt, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	if err := closeAttempt(ctx, tx, claim, nowText, outcome, "", failure.Code, failure.Message); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) CancelClaimedJob(ctx context.Context, claim ClaimedJob) error {
	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='cancelled', lease_owner=NULL, lease_until=NULL, finished_at=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND cancel_requested_at IS NOT NULL`, now, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	if err := closeAttempt(ctx, tx, claim, now, "cancelled", "", "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) CancelJob(ctx context.Context, identity WriteIdentity, jobID string) (domain.Job, bool, error) {
	return r.jobHTTPTransition(ctx, identity, jobID, "cancel_job", func(ctx context.Context, tx *sql.Tx, now string) error {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM jobs WHERE job_id=?`, jobID).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		switch status {
		case "pending", "waiting", "retrying":
			_, err := tx.ExecContext(ctx, `UPDATE jobs SET status='cancelled', cancel_requested_at=coalesce(cancel_requested_at, ?), finished_at=coalesce(finished_at, ?) WHERE job_id=?`, now, now, jobID)
			return err
		case "running":
			_, err := tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested_at=coalesce(cancel_requested_at, ?) WHERE job_id=?`, now, jobID)
			return err
		case "cancelled":
			return nil
		default:
			return ErrInvalidJobState
		}
	})
}

func (r *Repository) RetryJob(ctx context.Context, identity WriteIdentity, jobID string) (domain.Job, bool, error) {
	return r.jobHTTPTransition(ctx, identity, jobID, "retry_job", func(ctx context.Context, tx *sql.Tx, now string) error {
		result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='pending', max_attempts=max_attempts+1, next_run_at=?, processor_identity=NULL, cancel_requested_at=NULL, result_code=NULL, result_json=NULL, last_error_code=NULL, last_error_message=NULL, finished_at=NULL WHERE job_id=? AND status='failed'`, now, jobID)
		if err != nil {
			return err
		}
		if oneRow(result) {
			return nil
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE job_id=?`, jobID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		return ErrInvalidJobState
	})
}

func (r *Repository) RecoverExpiredLeases(ctx context.Context) (int, error) {
	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT job_id, lease_generation, cancel_requested_at IS NOT NULL, attempt_count>=max_attempts FROM jobs WHERE status='running' AND lease_until<=? ORDER BY lease_until, job_id`, now)
	if err != nil {
		return 0, err
	}
	type expired struct {
		id                   string
		generation           int
		cancelled, exhausted bool
	}
	var jobs []expired
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.id, &item.generation, &item.cancelled, &item.exhausted); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, job := range jobs {
		status, finishedAt := "retrying", any(nil)
		if job.cancelled {
			status, finishedAt = "cancelled", now
		} else if job.exhausted {
			status, finishedAt = "failed", now
		}
		if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET finished_at=?, outcome='lease_lost', error_code='lease_lost', error_message='worker lease expired' WHERE job_id=? AND lease_generation=? AND finished_at IS NULL`, now, job.id, job.generation); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status=?, lease_owner=NULL, lease_until=NULL, lease_generation=lease_generation+1, next_run_at=?, last_error_code='lease_lost', last_error_message='worker lease expired', finished_at=? WHERE job_id=? AND status='running' AND lease_generation=?`, status, now, finishedAt, job.id, job.generation); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(jobs), nil
}

func (r *Repository) PromoteWaitingJobs(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var runnable int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE status IN ('pending','retrying')`).Scan(&runnable); err != nil {
		return 0, err
	}
	if available := r.queueSoftLimit - runnable; available < limit {
		limit = available
	}
	if limit <= 0 {
		return 0, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='pending' WHERE job_id IN (SELECT job_id FROM jobs WHERE status='waiting' ORDER BY priority DESC, created_at ASC, job_id ASC LIMIT ?)`, limit)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(count), nil
}

func (r *Repository) ExpireJobs(ctx context.Context) (int, error) {
	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='expired', finished_at=?, last_error_code='expired', last_error_message='job expired' WHERE status IN ('pending','waiting','retrying') AND expires_at<=?`, now, now)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(count), nil
}

func (r *Repository) jobHTTPTransition(ctx context.Context, identity WriteIdentity, jobID, action string, transition func(context.Context, *sql.Tx, string) error) (domain.Job, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.Job{}, false, err
	} else if found {
		var job domain.Job
		expectedStatus := 200
		if action == "retry_job" {
			expectedStatus = 202
		}
		if status != expectedStatus || json.Unmarshal([]byte(body), &job) != nil {
			return domain.Job{}, false, errors.New("invalid stored idempotency response")
		}
		return job, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.Job{}, false, err
	}
	operationID, err := r.runtime.ID()
	if err != nil {
		return domain.Job{}, false, err
	}
	auditID, err := r.runtime.ID()
	if err != nil {
		return domain.Job{}, false, err
	}
	now := formatTime(r.runtime.Now())
	if err := transition(ctx, tx, now); err != nil {
		return domain.Job{}, false, err
	}
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelect+` WHERE job_id=?`, jobID))
	if err != nil {
		return domain.Job{}, false, err
	}
	body, err := json.Marshal(job)
	if err != nil {
		return domain.Job{}, false, err
	}
	responseStatus := 200
	if action == "retry_job" {
		responseStatus = 202
	}
	if err := writeIdempotency(ctx, tx, identity, operationID, responseStatus, body, now); err != nil {
		return domain.Job{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, created_at) VALUES (?, ?, ?, ?, ?, 'job', ?, 'succeeded', ?)`, auditID, operationID, identity.RequestID, identity.CallerID, action, jobID, now); err != nil {
		return domain.Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, nil
}

func closeAttempt(ctx context.Context, tx *sql.Tx, claim ClaimedJob, now, outcome, resultCode, errorCode, errorMessage string) error {
	result, err := tx.ExecContext(ctx, `UPDATE job_attempts SET finished_at=?, outcome=?, result_code=?, error_code=?, error_message=? WHERE job_id=? AND lease_generation=? AND worker_id=? AND finished_at IS NULL`, now, outcome, nullableString(resultCode), nullableString(errorCode), nullableString(errorMessage), claim.Job.JobID, claim.LeaseGeneration, claim.LeaseOwner)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	return nil
}

func scanJob(row rowScanner) (domain.Job, error) {
	var job domain.Job
	var processingVersion, status, nextRunAt, createdAt string
	var priority int
	var processor, cancelled, resultCode, resultJSON, errorCode, errorMessage, started, finished, expires sql.NullString
	err := row.Scan(&job.JobID, &job.OperationID, &job.JobType, &job.TargetType, &job.TargetID, &processingVersion, &processor, &status, &priority, &job.AttemptCount, &job.MaxAttempts, &nextRunAt, &cancelled, &resultCode, &resultJSON, &errorCode, &errorMessage, &createdAt, &started, &finished, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, ErrNotFound
	}
	if err != nil {
		return domain.Job{}, err
	}
	job.Status = status
	job.ProcessingVersion = processingVersion
	job.Priority = priority
	job.NextRunAt = nextRunAt
	job.CancelRequestedAt = nullStringPointer(cancelled)
	job.ResultCode = nullStringPointer(resultCode)
	job.LastErrorCode = nullStringPointer(errorCode)
	job.CreatedAt = createdAt
	job.StartedAt = nullStringPointer(started)
	job.FinishedAt = nullStringPointer(finished)
	job.ExpiresAt = nullStringPointer(expires)
	if resultJSON.Valid {
		decoder := json.NewDecoder(strings.NewReader(resultJSON.String))
		decoder.UseNumber()
		if err := decoder.Decode(&job.Result); err != nil {
			return domain.Job{}, err
		}
	}
	if errorCode.Valid || errorMessage.Valid {
		job.LastError = &domain.JobError{Code: errorCode.String, Message: errorMessage.String}
	}
	return job, nil
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func oneRow(result sql.Result) bool {
	count, err := result.RowsAffected()
	return err == nil && count == 1
}
