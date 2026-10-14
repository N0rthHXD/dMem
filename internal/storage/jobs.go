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
	ErrStaleGeneration = errors.New("stale configuration generation")
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

type ProcessingPreparationParams struct {
	ProcessorIdentity      string
	AllowedMemoryTypes     []string
	MaxCandidates          int
	MaxCandidateChars      int
	MaxTotalCandidateChars int
	MaxContentChars        int
	MaxContextEvents       int
	MaxContextMemories     int
}

type ProcessingCommit struct {
	Result domain.ProcessingResult
}

type claimRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const jobSelect = `SELECT job_id, operation_id, job_type, target_type, target_id, processing_version, processor_identity, status, priority, attempt_count, max_attempts, next_run_at, cancel_requested_at, capability, configuration_generation, policy_generation, compatibility_identity, blocked_reason_code, reconciliation_run_id, provider_attempt_count, result_code, result_json, last_error_code, last_error_message, created_at, started_at, finished_at, expires_at FROM jobs`

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
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM jobs j WHERE status IN ('pending','retrying') AND next_run_at<=? AND ((capability IS NULL AND attempt_count<max_attempts) OR (capability IS NOT NULL AND provider_attempt_count<max_attempts)) AND (expires_at IS NULL OR expires_at>?) AND (job_type NOT IN ('embed_memory','rebuild_semantic') OR processing_version=?) AND (job_type<>'rebuild_semantic' OR (NOT EXISTS (SELECT 1 FROM jobs e WHERE e.job_type='embed_memory' AND e.processing_version=j.processing_version AND e.status IN ('pending','waiting','running','retrying')) AND NOT EXISTS (SELECT 1 FROM memory_projections p WHERE p.searchable=1 AND EXISTS (SELECT 1 FROM embedding_eligibility x WHERE x.memory_id=p.memory_id AND x.revision_id=p.revision_id AND x.policy_generation=? AND x.eligible=1) AND (p.embedding_state<>'current' OR p.embedding_provider<>? OR p.embedding_model<>?)))) ORDER BY priority DESC, next_run_at ASC, created_at ASC, job_id ASC LIMIT 1`, nowText, nowText, r.embeddingIdentity(), r.emb().PolicyGeneration, r.emb().ProviderIdentity, r.emb().ModelIdentity).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimedJob{}, ErrNotFound
	}
	if err != nil {
		return ClaimedJob{}, err
	}
	embedding := r.emb()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='running', attempt_count=attempt_count+1, lease_owner=?, lease_until=?, lease_generation=lease_generation+1, started_at=coalesce(started_at, ?), finished_at=NULL, configuration_generation=CASE WHEN job_type IN ('embed_memory','rebuild_semantic') THEN ? ELSE ? END, policy_generation=CASE WHEN job_type IN ('embed_memory','rebuild_semantic') THEN ? ELSE ? END WHERE job_id=? AND status IN ('pending','retrying') AND next_run_at<=? AND ((capability IS NULL AND attempt_count<max_attempts) OR (capability IS NOT NULL AND provider_attempt_count<max_attempts))`, params.WorkerID, leaseUntil, nowText, embedding.Generation, r.currentProcessingGeneration(), embedding.PolicyGeneration, r.currentProcessingPolicyGeneration(), jobID, nowText)
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
	if _, err := tx.ExecContext(ctx, `UPDATE raw_events SET processing_status='processing' WHERE event_id=(SELECT target_id FROM jobs WHERE job_id=? AND job_type='extract_memory' AND target_type='raw_event')`, jobID); err != nil {
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

func (r *Repository) PrepareProcessing(ctx context.Context, claim ClaimedJob, params ProcessingPreparationParams) (domain.ProcessingInput, error) {
	if claim.Job.JobType != "extract_memory" || claim.Job.TargetType != "raw_event" {
		return domain.ProcessingInput{}, ErrInvalidJobState
	}
	var status, owner, eventID, version string
	var generation int
	err := r.store.reader.QueryRowContext(ctx, `SELECT status, lease_owner, lease_generation, target_id, processing_version FROM jobs WHERE job_id=? AND lease_until>?`, claim.Job.JobID, formatTime(r.runtime.Now())).Scan(&status, &owner, &generation, &eventID, &version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ProcessingInput{}, ErrLeaseLost
		}
		return domain.ProcessingInput{}, err
	}
	if status != "running" || owner != claim.LeaseOwner || generation != claim.LeaseGeneration || eventID != claim.Job.TargetID || version != claim.ProcessingVersion {
		return domain.ProcessingInput{}, ErrLeaseLost
	}
	target, err := processingEvent(r.store.reader.QueryRowContext(ctx, eventSelect+` WHERE e.event_id=?`, eventID))
	if err != nil {
		return domain.ProcessingInput{}, err
	}
	if err := r.RequireEffectivePolicy(ctx, "processing", target.SpaceID); err != nil {
		return domain.ProcessingInput{}, err
	}
	input := domain.ProcessingInput{
		ProcessorIdentity: params.ProcessorIdentity, ProcessingVersion: version, JobID: claim.Job.JobID,
		Attempt: claim.Job.AttemptCount, Target: target,
		Policy: domain.ProcessingPolicy{AllowedMemoryTypes: append([]string(nil), params.AllowedMemoryTypes...), MaxCandidates: params.MaxCandidates, MaxContentChars: params.MaxContentChars, MaxCandidateChars: max(params.MaxCandidateChars, params.MaxContentChars), MaxTotalCandidateChars: max(params.MaxTotalCandidateChars, params.MaxContentChars)},
	}
	if params.MaxContextEvents > 0 {
		rows, err := r.store.reader.QueryContext(ctx, eventSelect+` WHERE e.space_id=? AND e.event_id<>? AND e.occurred_at<=? ORDER BY e.occurred_at DESC, e.source_stream_id IS NULL, e.source_stream_id, e.sequence IS NULL, e.sequence DESC, e.event_id DESC LIMIT ?`, target.SpaceID, target.EventID, target.OccurredAt.UTC().Format(time.RFC3339Nano), params.MaxContextEvents)
		if err != nil {
			return domain.ProcessingInput{}, err
		}
		for rows.Next() {
			item, err := processingEvent(rows)
			if err != nil {
				rows.Close()
				return domain.ProcessingInput{}, err
			}
			input.ContextEvents = append(input.ContextEvents, item)
		}
		if err := rows.Close(); err != nil {
			return domain.ProcessingInput{}, err
		}
		orderContextEvents(input.ContextEvents)
	}
	if params.MaxContextMemories > 0 {
		scope, err := memoryScope(ctx, r.store.reader, target.SpaceID, true)
		if err != nil {
			return domain.ProcessingInput{}, err
		}
		rows, err := r.store.reader.QueryContext(ctx, `SELECT m.memory_id, r.revision_id, m.space_id, r.display_content, m.current_state, m.sensitivity, r.valid_from, r.valid_until FROM memories m JOIN memory_revisions r ON r.revision_id=m.current_revision_id WHERE m.space_id IN (`+placeholders(len(scope))+`) AND m.current_state<>'deleted' ORDER BY m.updated_at DESC, m.memory_id DESC LIMIT ?`, appendStrings(scope, params.MaxContextMemories)...)
		if err != nil {
			return domain.ProcessingInput{}, err
		}
		for rows.Next() {
			var item domain.ProcessingMemory
			var validFrom, validUntil sql.NullString
			if err := rows.Scan(&item.MemoryID, &item.RevisionID, &item.SpaceID, &item.Content, &item.CurrentState, &item.Sensitivity, &validFrom, &validUntil); err != nil {
				rows.Close()
				return domain.ProcessingInput{}, err
			}
			if validFrom.Valid {
				value, err := time.Parse(time.RFC3339Nano, validFrom.String)
				if err != nil {
					rows.Close()
					return domain.ProcessingInput{}, err
				}
				item.ValidFrom = &value
			}
			if validUntil.Valid {
				value, err := time.Parse(time.RFC3339Nano, validUntil.String)
				if err != nil {
					rows.Close()
					return domain.ProcessingInput{}, err
				}
				item.ValidUntil = &value
			}
			item.ProposalCapable = item.SpaceID == target.SpaceID
			policy, err := r.EffectivePolicy(ctx, "processing", item.SpaceID)
			if errors.Is(err, ErrNotFound) {
				input.ContextMemories = append(input.ContextMemories, item)
				continue
			}
			if err != nil {
				rows.Close()
				return domain.ProcessingInput{}, err
			}
			if policy.Decision != "allow" {
				if input.OmissionSummary == nil {
					input.OmissionSummary = make(map[string]int)
				}
				input.OmissionSummary["context_memory_policy_denied"]++
				continue
			}
			input.ContextMemories = append(input.ContextMemories, item)
		}
		if err := rows.Close(); err != nil {
			return domain.ProcessingInput{}, err
		}
	}
	return input, nil
}

func processingEvent(row rowScanner) (domain.ProcessingEvent, error) {
	event, err := scanEvent(row)
	if err != nil {
		return domain.ProcessingEvent{}, err
	}
	content := event.Content.Text
	if event.Content.Kind == "json" {
		encoded, err := json.Marshal(event.Content.Value)
		if err != nil {
			return domain.ProcessingEvent{}, err
		}
		content = string(encoded)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, event.OccurredAt)
	if err != nil {
		return domain.ProcessingEvent{}, err
	}
	var declaredSize int64
	if event.Content.DeclaredSize != nil {
		declaredSize = *event.Content.DeclaredSize
	}
	return domain.ProcessingEvent{
		EventID: event.EventID, SpaceID: event.SpaceID, SourceType: event.SourceType, SourceName: event.SourceName, SourceStreamID: stringValue(event.SourceStreamID), Sequence: event.Sequence, Sensitivity: "normal", OccurredAt: occurredAt,
		Content: domain.ProcessingContent{Kind: event.Content.Kind, MediaType: event.Content.MediaType, Text: content, DeclaredSize: declaredSize, DeclaredHash: event.Content.DeclaredHash},
	}, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func appendStrings(values []string, last any) []any {
	result := make([]any, 0, len(values)+1)
	for _, value := range values {
		result = append(result, value)
	}
	return append(result, last)
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

// MarkProviderAttempt records durable provenance immediately before a provider
// call. Policy-blocked work never reaches this method and therefore cannot
// consume the external-provider attempt budget.
func (r *Repository) MarkProviderAttempt(ctx context.Context, claim ClaimedJob) error {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var configurationGeneration, policyGeneration sql.NullInt64
	var compatibility sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT configuration_generation, policy_generation, compatibility_identity FROM jobs WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, formatTime(r.runtime.Now())).Scan(&configurationGeneration, &policyGeneration, &compatibility)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	now := formatTime(r.runtime.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET provider_attempt_count=provider_attempt_count+1 WHERE job_id=?`, claim.Job.JobID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE job_attempts SET configuration_generation=?, policy_generation=?, compatibility_identity=?, external_call_started_at=coalesce(external_call_started_at,?) WHERE job_id=? AND lease_generation=? AND worker_id=? AND finished_at IS NULL`, configurationGeneration, policyGeneration, compatibility, now, claim.Job.JobID, claim.LeaseGeneration, claim.LeaseOwner)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	return tx.Commit()
}

// EmbeddingScope returns the scope of the searchable memory projection targeted
// by an embedding job. The bool reports whether such a projection exists; the
// worker uses it to route the provider-egress decision through the policy
// interface before any embedding call.
func (r *Repository) EmbeddingScope(ctx context.Context, claim ClaimedJob) (string, bool, error) {
	var spaceID string
	err := r.store.reader.QueryRowContext(ctx, `SELECT space_id FROM memory_projections WHERE revision_id=? AND searchable=1`, claim.Job.TargetID).Scan(&spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return spaceID, true, nil
}

func (r *Repository) PrepareEmbeddingAttempt(ctx context.Context, claim ClaimedJob) error {
	var spaceID string
	err := r.store.reader.QueryRowContext(ctx, `SELECT space_id FROM memory_projections WHERE revision_id=? AND searchable=1`, claim.Job.TargetID).Scan(&spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := r.RequireEffectivePolicy(ctx, "embedding", spaceID); err != nil {
		return err
	}
	return r.MarkProviderAttempt(ctx, claim)
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
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='succeeded', lease_owner=NULL, lease_until=NULL, result_code=?, result_json=?, last_error_code=NULL, last_error_message=NULL, finished_at=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, completion.Code, string(encoded), now, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, now)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	if err := closeAttempt(ctx, tx, claim, now, "succeeded", completion.Code, "", ""); err != nil {
		return err
	}
	if claim.Job.JobType == "rebuild_semantic" && claim.ProcessingVersion == r.embeddingIdentity() && r.emb().Provider != nil {
		dirty, err := r.semanticIndexDirty(ctx, tx)
		if err != nil {
			return err
		}
		if dirty {
			if _, err := r.enqueueSemanticRebuild(ctx, tx, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *Repository) BlockJob(ctx context.Context, claim ClaimedJob, reason string) error {
	if !safeReasonCode(reason) {
		return fmt.Errorf("%w: blocked reason is required", ErrInvalidJobState)
	}
	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='blocked_policy', lease_owner=NULL, lease_until=NULL, result_code=NULL, result_json=NULL, blocked_reason_code=?, last_error_code=NULL, last_error_message=NULL, finished_at=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, reason, now, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, now)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	if _, err := tx.ExecContext(ctx, `UPDATE raw_events SET processing_status='stored' WHERE event_id=(SELECT target_id FROM jobs WHERE job_id=? AND job_type='extract_memory' AND target_type='raw_event')`, claim.Job.JobID); err != nil {
		return err
	}
	if err := closeAttempt(ctx, tx, claim, now, "blocked_policy", "", reason, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) SupersedeJob(ctx context.Context, jobID, reason string) error {
	if !safeReasonCode(reason) {
		return fmt.Errorf("%w: supersession reason is required", ErrInvalidJobState)
	}
	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM jobs WHERE job_id=?`, jobID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status == "superseded" {
		return tx.Commit()
	}
	if !contains([]string{"pending", "waiting", "retrying", "running", "blocked_policy"}, status) {
		return ErrInvalidJobState
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='superseded', lease_owner=NULL, lease_until=NULL, blocked_reason_code=?, result_code=NULL, result_json=NULL, last_error_code=NULL, last_error_message=NULL, finished_at=? WHERE job_id=? AND status=?`, reason, now, jobID, status)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	if status == "running" {
		if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET finished_at=?, outcome='superseded', error_code=?, error_message='job target superseded' WHERE job_id=? AND finished_at IS NULL`, now, reason, jobID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE raw_events SET processing_status='stored' WHERE event_id=(SELECT target_id FROM jobs WHERE job_id=? AND job_type='extract_memory' AND target_type='raw_event')`, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

func safeReasonCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char != '_' && char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

// ReevaluateStaleJob closes an obsolete attempt without treating it as a
// provider failure. The logical job is returned to pending for current-policy
// evaluation and therefore does not consume retry budget.
func (r *Repository) ReevaluateStaleJob(ctx context.Context, claim ClaimedJob) error {
	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='pending', lease_owner=NULL, lease_until=NULL, next_run_at=?, blocked_reason_code=NULL, finished_at=NULL WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, now, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, now)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	if err := closeAttempt(ctx, tx, claim, now, "stale_generation", "", "stale_generation", "configuration or policy generation changed"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE raw_events SET processing_status='stored' WHERE event_id=(SELECT target_id FROM jobs WHERE job_id=? AND job_type='extract_memory' AND target_type='raw_event')`, claim.Job.JobID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) CommitProcessing(ctx context.Context, claim ClaimedJob, input domain.ProcessingInput, commit ProcessingCommit) error {
	result := commit.Result
	status := "processed"
	if result.Code == domain.ProcessingNoProcessor {
		status = "stored"
	}
	value := map[string]any{
		"candidate_count": len(result.Proposals), "provider_identity": result.ProviderIdentity,
		"model_identity": result.ModelIdentity, "policy_version": result.PolicyVersion,
		"provider_request_id": result.ProviderRequestID, "usage": result.Usage,
	}
	if result.OmittedContextCount > 0 {
		value["omitted_context_count"] = result.OmittedContextCount
		value["omission_summary"] = result.OmissionSummary
	}
	if result.ReasonCode != "" {
		value["reason_code"] = result.ReasonCode
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	idCount := 1
	for _, proposal := range result.Proposals {
		idCount += 1 + len(proposal.Revision.Evidence)
	}
	ids, err := r.newIDs(idCount)
	if err != nil {
		return err
	}
	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.requireProcessingFence(ctx, tx, claim, input); err != nil {
		return err
	}
	idIndex := 1
	for _, proposal := range result.Proposals {
		if proposal.Action != domain.ConsolidationCreate {
			return ErrInvalidEvolutionProposal
		}
		params := processingCandidateParams(input, result, proposal)
		if err := domain.ValidateMemoryRevision(params.Revision); err != nil {
			return err
		}
		if err := requireCandidateSources(ctx, tx, params); err != nil {
			return err
		}
		hash := candidateSpecificationHash(params)
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT candidate_id FROM candidates WHERE event_id=? AND processing_version=? AND specification_hash=?`, params.EventID, params.ProcessingVersion, hash).Scan(&existing)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		candidateID := ids[idIndex]
		idIndex++
		structured, err := json.Marshal(params.Revision)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO candidates(candidate_id,event_id,space_id,memory_type,structured_revision_json,processor_identity,provider_identity,model_identity,processing_version,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, candidateID, params.EventID, params.SpaceID, string(params.Revision.Kind), string(structured), params.ProcessorIdentity, params.ProviderIdentity, params.ModelIdentity, params.ProcessingVersion, hash, now); err != nil {
			return err
		}
		for _, evidence := range params.Evidence {
			if _, err := tx.ExecContext(ctx, `INSERT INTO candidate_evidence(evidence_id,candidate_id,event_id,role,start_offset,end_offset,created_at) VALUES (?,?,?,?,?,?,?)`, ids[idIndex], candidateID, evidence.EventID, evidence.Role, evidence.StartOffset, evidence.EndOffset, now); err != nil {
				return candidateConstraintError(err)
			}
			idIndex++
		}
		if _, err := r.autoConfirmCandidate(ctx, tx, candidateID); err != nil {
			return err
		}
	}
	updated, err := tx.ExecContext(ctx, `UPDATE jobs SET status='succeeded',processor_identity=?,lease_owner=NULL,lease_until=NULL,result_code=?,result_json=?,last_error_code=NULL,last_error_message=NULL,finished_at=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, input.ProcessorIdentity, string(result.Code), string(encoded), now, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, now)
	if err != nil {
		return err
	}
	if !oneRow(updated) {
		return ErrLeaseLost
	}
	if _, err := tx.ExecContext(ctx, `UPDATE raw_events SET processing_status=? WHERE event_id=?`, status, input.Target.EventID); err != nil {
		return err
	}
	if err := closeAttempt(ctx, tx, claim, now, "succeeded", string(result.Code), "", ""); err != nil {
		return err
	}
	if result.OmittedContextCount > 0 {
		summary, err := json.Marshal(result.OmissionSummary)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET omitted_context_count=?, omission_summary=? WHERE job_id=? AND lease_generation=?`, result.OmittedContextCount, string(summary), claim.Job.JobID, claim.LeaseGeneration); err != nil {
			return err
		}
	}
	reason, _ := json.Marshal(map[string]string{"result_code": string(result.Code), "reason_code": result.ReasonCode, "provider_identity": result.ProviderIdentity, "model_identity": result.ModelIdentity, "policy_version": result.PolicyVersion, "provider_request_id": result.ProviderRequestID})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,reason,created_at) VALUES (?,?,?,?, 'process_event','raw_event',?,'succeeded',?,?)`, ids[0], claim.Job.OperationID, claim.AttemptID, input.ProcessorIdentity, input.Target.EventID, string(reason), now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) FailProcessing(ctx context.Context, claim ClaimedJob, input domain.ProcessingInput, failure JobFailure) error {
	now := r.runtime.Now().UTC()
	nowText := formatTime(now)
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.requireProcessingFence(ctx, tx, claim, input); err != nil {
		return err
	}
	var attempts, maxAttempts int
	if err := tx.QueryRowContext(ctx, `SELECT attempt_count,max_attempts FROM jobs WHERE job_id=?`, claim.Job.JobID).Scan(&attempts, &maxAttempts); err != nil {
		return err
	}
	jobStatus, attemptOutcome, eventStatus, finishedAt, nextRunAt := "failed", "failed", "failed", any(nowText), nowText
	if failure.Transient && attempts < maxAttempts {
		jobStatus, attemptOutcome, eventStatus, finishedAt, nextRunAt = "retrying", "retrying", "processing", nil, formatTime(now.Add(failure.Backoff))
	}
	updated, err := tx.ExecContext(ctx, `UPDATE jobs SET status=?,processor_identity=?,lease_owner=NULL,lease_until=NULL,next_run_at=?,result_code=NULL,result_json=NULL,last_error_code=?,last_error_message=?,finished_at=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, jobStatus, input.ProcessorIdentity, nextRunAt, failure.Code, failure.Message, finishedAt, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, nowText)
	if err != nil {
		return err
	}
	if !oneRow(updated) {
		return ErrLeaseLost
	}
	if _, err := tx.ExecContext(ctx, `UPDATE raw_events SET processing_status=? WHERE event_id=?`, eventStatus, input.Target.EventID); err != nil {
		return err
	}
	if err := closeAttempt(ctx, tx, claim, nowText, attemptOutcome, "", failure.Code, failure.Message); err != nil {
		return err
	}
	auditID, err := r.runtime.ID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,reason,created_at) VALUES (?,?,?,?, 'process_event','raw_event',?,'failed',?,?)`, auditID, claim.Job.OperationID, claim.AttemptID, input.ProcessorIdentity, input.Target.EventID, failure.Code, nowText); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) requireProcessingFence(ctx context.Context, tx *sql.Tx, claim ClaimedJob, input domain.ProcessingInput) error {
	var targetID, version string
	var cfgGen, polGen sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT target_id,processing_version,configuration_generation,policy_generation FROM jobs WHERE job_id=? AND job_type='extract_memory' AND target_type='raw_event' AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, formatTime(r.runtime.Now())).Scan(&targetID, &version, &cfgGen, &polGen)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if targetID != input.Target.EventID || version != input.ProcessingVersion || input.JobID != claim.Job.JobID {
		return ErrLeaseLost
	}
	if r.currentProcessingGeneration() > 0 || cfgGen.Valid || polGen.Valid {
		if int(cfgGen.Int64) != r.currentProcessingGeneration() || int(polGen.Int64) != r.currentProcessingPolicyGeneration() {
			return ErrStaleGeneration
		}
	}
	return nil
}

func (r *Repository) requireJobClaim(ctx context.Context, query claimRowQuerier, claim ClaimedJob) error {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, formatTime(r.runtime.Now())).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	return err
}

func processingCandidateParams(input domain.ProcessingInput, result domain.ProcessingResult, proposal domain.MemoryCandidateProposal) CreateCandidateParams {
	revision := proposal.Revision
	providerIdentity, modelIdentity := stringPointer(result.ProviderIdentity), stringPointer(result.ModelIdentity)
	if result.ProviderIdentity == "" {
		providerIdentity = nil
	}
	if result.ModelIdentity == "" {
		modelIdentity = nil
	}
	ordered := append([]domain.RevisionEvidence(nil), revision.Evidence...)
	domain.OrderRevisionEvidence(ordered)
	evidence := make([]EvidenceParams, len(ordered))
	for i, item := range ordered {
		evidence[i] = EvidenceParams{EventID: item.EventID, Role: item.Role, StartOffset: item.StartOffset, EndOffset: item.EndOffset}
	}
	return CreateCandidateParams{EventID: input.Target.EventID, SpaceID: input.Target.SpaceID, ProcessorIdentity: input.ProcessorIdentity, ProviderIdentity: providerIdentity, ModelIdentity: modelIdentity, ProcessingVersion: input.ProcessingVersion, Evidence: evidence, Revision: revision}
}

func timePointerText(value *time.Time) *string {
	if value == nil {
		return nil
	}
	text := formatTime(value.UTC())
	return &text
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
	if err := tx.QueryRowContext(ctx, `SELECT attempt_count, max_attempts FROM jobs WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, nowText).Scan(&attempts, &maxAttempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	status, outcome, finishedAt, nextRunAt := "failed", "failed", any(nowText), nowText
	if failure.Transient && attempts < maxAttempts {
		status, outcome, finishedAt, nextRunAt = "retrying", "retrying", nil, formatTime(now.Add(failure.Backoff))
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status=?, lease_owner=NULL, lease_until=NULL, next_run_at=?, result_code=NULL, result_json=NULL, last_error_code=?, last_error_message=?, finished_at=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, status, nextRunAt, failure.Code, failure.Message, finishedAt, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, nowText)
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
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='cancelled', lease_owner=NULL, lease_until=NULL, finished_at=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>? AND cancel_requested_at IS NOT NULL`, now, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, now)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	if _, err := tx.ExecContext(ctx, `UPDATE raw_events SET processing_status='stored' WHERE event_id=(SELECT target_id FROM jobs WHERE job_id=? AND job_type='extract_memory' AND target_type='raw_event')`, claim.Job.JobID); err != nil {
		return err
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
		status, eventStatus, finishedAt := "retrying", "processing", any(nil)
		if job.cancelled {
			status, eventStatus, finishedAt = "cancelled", "stored", now
		} else if job.exhausted {
			status, eventStatus, finishedAt = "failed", "failed", now
		}
		if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET finished_at=?, outcome='lease_lost', error_code='lease_lost', error_message='worker lease expired' WHERE job_id=? AND lease_generation=? AND finished_at IS NULL`, now, job.id, job.generation); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status=?, lease_owner=NULL, lease_until=NULL, lease_generation=lease_generation+1, next_run_at=?, last_error_code='lease_lost', last_error_message='worker lease expired', finished_at=? WHERE job_id=? AND status='running' AND lease_generation=?`, status, now, finishedAt, job.id, job.generation); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE raw_events SET processing_status=? WHERE event_id=(SELECT target_id FROM jobs WHERE job_id=? AND job_type='extract_memory' AND target_type='raw_event')`, eventStatus, job.id); err != nil {
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
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE status IN ('pending','retrying') AND (job_type NOT IN ('embed_memory','rebuild_semantic') OR processing_version=?)`, r.embeddingIdentity()).Scan(&runnable); err != nil {
		return 0, err
	}
	if available := r.queueSoftLimit - runnable; available < limit {
		limit = available
	}
	if limit <= 0 {
		return 0, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='pending' WHERE job_id IN (SELECT job_id FROM jobs j WHERE status='waiting' AND (job_type NOT IN ('embed_memory','rebuild_semantic') OR processing_version=?) AND (job_type<>'rebuild_semantic' OR (NOT EXISTS (SELECT 1 FROM jobs e WHERE e.job_type='embed_memory' AND e.processing_version=j.processing_version AND e.status IN ('pending','waiting','running','retrying')) AND NOT EXISTS (SELECT 1 FROM memory_projections p WHERE p.searchable=1 AND EXISTS (SELECT 1 FROM embedding_eligibility x WHERE x.memory_id=p.memory_id AND x.revision_id=p.revision_id AND x.policy_generation=? AND x.eligible=1) AND (p.embedding_state<>'current' OR p.embedding_provider<>? OR p.embedding_model<>?)))) ORDER BY priority DESC, created_at ASC, job_id ASC LIMIT ?)`, r.embeddingIdentity(), r.emb().PolicyGeneration, r.emb().ProviderIdentity, r.emb().ModelIdentity, limit)
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
	var capability, compatIdentity, blockedReason, reconRun sql.NullString
	var configGen, policyGen, providerAttempts sql.NullInt64
	var processor, cancelled, resultCode, resultJSON, errorCode, errorMessage, started, finished, expires sql.NullString
	err := row.Scan(&job.JobID, &job.OperationID, &job.JobType, &job.TargetType, &job.TargetID, &processingVersion, &processor, &status, &priority, &job.AttemptCount, &job.MaxAttempts, &nextRunAt, &cancelled, &capability, &configGen, &policyGen, &compatIdentity, &blockedReason, &reconRun, &providerAttempts, &resultCode, &resultJSON, &errorCode, &errorMessage, &createdAt, &started, &finished, &expires)
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
	if capability.Valid {
		job.Capability = capability.String
	}
	if configGen.Valid {
		job.ConfigurationGeneration = int(configGen.Int64)
	}
	if policyGen.Valid {
		job.PolicyGeneration = int(policyGen.Int64)
	}
	if compatIdentity.Valid {
		job.CompatibilityIdentity = compatIdentity.String
	}
	if blockedReason.Valid {
		job.BlockedReasonCode = blockedReason.String
	}
	if reconRun.Valid {
		job.ReconciliationRunID = reconRun.String
	}
	if providerAttempts.Valid {
		job.ProviderAttemptCount = int(providerAttempts.Int64)
	}
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
