package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

type ProcessingPreparationParams struct {
	ProcessorIdentity  string
	AllowedMemoryTypes []string
	MaxCandidates      int
	MaxContentChars    int
	MaxContextEvents   int
	MaxContextMemories int
}

type ProcessingCommit struct {
	Result domain.ProcessingResult
}

type claimRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
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
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM jobs j WHERE status IN ('pending','retrying') AND next_run_at<=? AND attempt_count<max_attempts AND (expires_at IS NULL OR expires_at>?) AND (job_type NOT IN ('embed_memory','rebuild_semantic') OR processing_version=?) AND (job_type<>'rebuild_semantic' OR (NOT EXISTS (SELECT 1 FROM jobs e WHERE e.job_type='embed_memory' AND e.processing_version=j.processing_version AND e.status IN ('pending','waiting','running','retrying')) AND NOT EXISTS (SELECT 1 FROM memory_projections p WHERE p.searchable=1 AND (p.embedding_state<>'current' OR p.embedding_provider<>? OR p.embedding_model<>?)))) ORDER BY priority DESC, next_run_at ASC, created_at ASC, job_id ASC LIMIT 1`, nowText, nowText, r.embeddingIdentity(), r.embeddingProviderIdentity, r.embeddingModelIdentity).Scan(&jobID)
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
	input := domain.ProcessingInput{
		ProcessorIdentity: params.ProcessorIdentity, ProcessingVersion: version, JobID: claim.Job.JobID,
		Attempt: claim.Job.AttemptCount, Target: target,
		Policy: domain.ProcessingPolicy{AllowedMemoryTypes: append([]string(nil), params.AllowedMemoryTypes...), MaxCandidates: params.MaxCandidates, MaxContentChars: params.MaxContentChars},
	}
	if params.MaxContextEvents > 0 {
		rows, err := r.store.reader.QueryContext(ctx, eventSelect+` WHERE e.space_id=? AND e.event_id<>? AND e.occurred_at<=? ORDER BY e.occurred_at DESC, e.event_id DESC LIMIT ?`, target.SpaceID, target.EventID, target.OccurredAt.UTC().Format(time.RFC3339Nano), params.MaxContextEvents)
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
	}
	if params.MaxContextMemories > 0 {
		scope, err := memoryScope(ctx, r.store.reader, target.SpaceID, true)
		if err != nil {
			return domain.ProcessingInput{}, err
		}
		rows, err := r.store.reader.QueryContext(ctx, `SELECT m.memory_id, r.revision_id, m.space_id, r.content, m.current_state, m.sensitivity, r.valid_from, r.valid_until FROM memories m JOIN memory_revisions r ON r.revision_id=m.current_revision_id WHERE m.space_id IN (`+placeholders(len(scope))+`) AND m.current_state<>'deleted' ORDER BY m.updated_at DESC, m.memory_id DESC LIMIT ?`, appendStrings(scope, params.MaxContextMemories)...)
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
		EventID: event.EventID, SpaceID: event.SpaceID, SourceType: event.SourceType, SourceName: event.SourceName, Sensitivity: "normal", OccurredAt: occurredAt,
		Content: domain.ProcessingContent{Kind: event.Content.Kind, MediaType: event.Content.MediaType, Text: content, DeclaredSize: declaredSize, DeclaredHash: event.Content.DeclaredHash},
	}, nil
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
	if claim.Job.JobType == "embed_memory" && completion.Code == "policy_rejected" {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status='cancelled', finished_at=?, last_error_code='embedding_policy_rejected', last_error_message='semantic rebuild blocked by embedding policy' WHERE job_type='rebuild_semantic' AND processing_version=? AND status IN ('pending','waiting','retrying')`, now, claim.ProcessingVersion); err != nil {
			return err
		}
	}
	if claim.Job.JobType == "rebuild_semantic" && claim.ProcessingVersion == r.embeddingIdentity() && r.embeddingProvider != nil && r.vectorIndex != nil {
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

func (r *Repository) CommitProcessing(ctx context.Context, claim ClaimedJob, input domain.ProcessingInput, commit ProcessingCommit) error {
	result := commit.Result
	status := "processed"
	if result.Code == domain.ProcessingNoProcessor || result.Code == domain.ProcessingPolicyRejected {
		status = "stored"
	}
	value := map[string]any{
		"candidate_count": len(result.Candidates), "provider_identity": result.ProviderIdentity,
		"model_identity": result.ModelIdentity, "policy_version": result.PolicyVersion,
		"provider_request_id": result.ProviderRequestID, "usage": result.Usage,
	}
	if result.ReasonCode != "" {
		value["reason_code"] = result.ReasonCode
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	idCount := 1
	for _, candidate := range result.Candidates {
		idCount += 1 + len(candidate.Evidence)
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
	for _, candidate := range result.Candidates {
		params := processingCandidateParams(input, result, candidate)
		if candidate.SuggestionKind != "new" {
			evolution, err := processingEvolutionParams(input, result, candidate)
			if err != nil {
				return err
			}
			if err := validateEvolutionProposal(ctx, tx, evolution); err != nil {
				return err
			}
			hash := evolutionSpecificationHash(evolution)
			var existing string
			err = tx.QueryRowContext(ctx, `SELECT proposal_id FROM evolution_proposals WHERE event_id=? AND processing_version=? AND action=? AND specification_hash=?`, evolution.EventID, evolution.ProcessingVersion, evolution.Action, hash).Scan(&existing)
			if err == nil {
				idIndex += 1 + len(candidate.Evidence)
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			proposalID := ids[idIndex]
			idIndex++
			var contentHash *string
			if evolution.ProposedContent != nil {
				h := sha256.Sum256([]byte(*evolution.ProposedContent))
				value := hex.EncodeToString(h[:])
				contentHash = &value
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_proposals(proposal_id,event_id,space_id,action,proposed_content,proposed_content_hash,valid_from,valid_until,memory_type,sensitivity,relation_specs_json,mention_specs_json,processor_identity,provider_identity,model_identity,processing_version,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?, '[]','[]',?,?,?,?,?,?)`, proposalID, evolution.EventID, evolution.SpaceID, evolution.Action, evolution.ProposedContent, contentHash, evolution.ValidFrom, evolution.ValidUntil, evolution.MemoryType, evolution.Sensitivity, evolution.ProcessorIdentity, evolution.ProviderIdentity, evolution.ModelIdentity, evolution.ProcessingVersion, hash, now); err != nil {
				return err
			}
			for i, target := range evolution.Targets {
				if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_proposal_targets(proposal_id,target_order,memory_id,expected_revision_id,created_at) VALUES (?,?,?,?,?)`, proposalID, i, target.MemoryID, target.ExpectedRevisionID, now); err != nil {
					return err
				}
			}
			for _, evidence := range evolution.Evidence {
				if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_proposal_evidence(evidence_id,proposal_id,evidence_type,event_id,start_offset,end_offset,created_at) VALUES (?,?,'raw_event_range',?,?,?,?)`, ids[idIndex], proposalID, evidence.EventID, evidence.StartOffset, evidence.EndOffset, now); err != nil {
					return err
				}
				idIndex++
			}
			continue
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
		contentHash := sha256.Sum256([]byte(params.Content))
		if _, err := tx.ExecContext(ctx, `INSERT INTO candidates(candidate_id,event_id,space_id,memory_type,content,content_hash,valid_from,valid_until,confidence,sensitivity,suggestion_kind,processor_identity,provider_identity,model_identity,processing_version,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, candidateID, params.EventID, params.SpaceID, params.MemoryType, params.Content, hex.EncodeToString(contentHash[:]), params.ValidFrom, params.ValidUntil, params.Confidence, params.Sensitivity, params.SuggestionKind, params.ProcessorIdentity, params.ProviderIdentity, params.ModelIdentity, params.ProcessingVersion, hash, now); err != nil {
			return err
		}
		for _, evidence := range params.Evidence {
			if _, err := tx.ExecContext(ctx, `INSERT INTO candidate_evidence(evidence_id,candidate_id,event_id,evidence_type,start_offset,end_offset,created_at) VALUES (?,?,?,?,?,?,?)`, ids[idIndex], candidateID, evidence.EventID, evidence.EvidenceType, evidence.StartOffset, evidence.EndOffset, now); err != nil {
				return candidateConstraintError(err)
			}
			idIndex++
		}
		for _, memoryID := range params.RelatedMemoryIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO candidate_related_memories(candidate_id,memory_id,created_at) VALUES (?,?,?)`, candidateID, memoryID, now); err != nil {
				return candidateConstraintError(err)
			}
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
	reason, _ := json.Marshal(map[string]string{"result_code": string(result.Code), "reason_code": result.ReasonCode, "provider_identity": result.ProviderIdentity, "model_identity": result.ModelIdentity, "policy_version": result.PolicyVersion, "provider_request_id": result.ProviderRequestID})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,reason,created_at) VALUES (?,?,?,?, 'process_event','raw_event',?,'succeeded',?,?)`, ids[0], claim.Job.OperationID, claim.AttemptID, input.ProcessorIdentity, input.Target.EventID, string(reason), now); err != nil {
		return err
	}
	return tx.Commit()
}

func processingEvolutionParams(input domain.ProcessingInput, result domain.ProcessingResult, candidate domain.ProcessingCandidate) (CreateEvolutionProposalParams, error) {
	memories := make(map[string]domain.ProcessingMemory, len(input.ContextMemories))
	for _, memory := range input.ContextMemories {
		memories[memory.MemoryID] = memory
	}
	targets := make([]domain.EvolutionTarget, len(candidate.RelatedMemoryIDs))
	for i, memoryID := range candidate.RelatedMemoryIDs {
		memory, ok := memories[memoryID]
		if !ok || memory.RevisionID == "" {
			return CreateEvolutionProposalParams{}, ErrInvalidEvolutionProposal
		}
		targets[i] = domain.EvolutionTarget{MemoryID: memoryID, ExpectedRevisionID: memory.RevisionID}
	}
	evidence := make([]domain.RelationEvidence, len(candidate.Evidence))
	for i, item := range candidate.Evidence {
		if item.StartOffset == nil || item.EndOffset == nil {
			return CreateEvolutionProposalParams{}, ErrInvalidEvolutionProposal
		}
		start, end := int(*item.StartOffset), int(*item.EndOffset)
		eventID := item.EventID
		evidence[i] = domain.RelationEvidence{EvidenceType: "raw_event_range", EventID: &eventID, StartOffset: &start, EndOffset: &end}
	}
	var content *string
	if candidate.SuggestionKind != "duplicate" {
		value := candidate.Content
		content = &value
	}
	memoryType := candidate.MemoryType
	providerIdentity, modelIdentity := stringPointer(result.ProviderIdentity), stringPointer(result.ModelIdentity)
	if result.ProviderIdentity == "" {
		providerIdentity = nil
	}
	if result.ModelIdentity == "" {
		modelIdentity = nil
	}
	return CreateEvolutionProposalParams{EventID: input.Target.EventID, SpaceID: candidate.SpaceID, Action: candidate.SuggestionKind, ProposedContent: content, ValidFrom: timePointerText(candidate.ValidFrom), ValidUntil: timePointerText(candidate.ValidUntil), MemoryType: &memoryType, Sensitivity: candidate.Sensitivity, ProcessorIdentity: input.ProcessorIdentity, ProviderIdentity: providerIdentity, ModelIdentity: modelIdentity, ProcessingVersion: input.ProcessingVersion, Targets: targets, Evidence: evidence}, nil
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
	err := tx.QueryRowContext(ctx, `SELECT target_id,processing_version FROM jobs WHERE job_id=? AND job_type='extract_memory' AND target_type='raw_event' AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, formatTime(r.runtime.Now())).Scan(&targetID, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if targetID != input.Target.EventID || version != input.ProcessingVersion || input.JobID != claim.Job.JobID {
		return ErrLeaseLost
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

func processingCandidateParams(input domain.ProcessingInput, result domain.ProcessingResult, candidate domain.ProcessingCandidate) CreateCandidateParams {
	validFrom, validUntil := timePointerText(candidate.ValidFrom), timePointerText(candidate.ValidUntil)
	confidence := candidate.Confidence
	providerIdentity, modelIdentity := stringPointer(result.ProviderIdentity), stringPointer(result.ModelIdentity)
	if result.ProviderIdentity == "" {
		providerIdentity = nil
	}
	if result.ModelIdentity == "" {
		modelIdentity = nil
	}
	evidence := make([]EvidenceParams, len(candidate.Evidence))
	for i, item := range candidate.Evidence {
		evidence[i] = EvidenceParams{EventID: item.EventID, EvidenceType: item.EvidenceType, StartOffset: item.StartOffset, EndOffset: item.EndOffset}
	}
	return CreateCandidateParams{EventID: input.Target.EventID, SpaceID: candidate.SpaceID, MemoryType: candidate.MemoryType, Content: candidate.Content, ValidFrom: validFrom, ValidUntil: validUntil, Confidence: &confidence, Sensitivity: candidate.Sensitivity, SuggestionKind: candidate.SuggestionKind, ProcessorIdentity: input.ProcessorIdentity, ProviderIdentity: providerIdentity, ModelIdentity: modelIdentity, ProcessingVersion: input.ProcessingVersion, Evidence: evidence, RelatedMemoryIDs: append([]string(nil), candidate.RelatedMemoryIDs...)}
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
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='pending' WHERE job_id IN (SELECT job_id FROM jobs j WHERE status='waiting' AND (job_type NOT IN ('embed_memory','rebuild_semantic') OR processing_version=?) AND (job_type<>'rebuild_semantic' OR (NOT EXISTS (SELECT 1 FROM jobs e WHERE e.job_type='embed_memory' AND e.processing_version=j.processing_version AND e.status IN ('pending','waiting','running','retrying')) AND NOT EXISTS (SELECT 1 FROM memory_projections p WHERE p.searchable=1 AND (p.embedding_state<>'current' OR p.embedding_provider<>? OR p.embedding_model<>?)))) ORDER BY priority DESC, created_at ASC, job_id ASC LIMIT ?)`, r.embeddingIdentity(), r.embeddingProviderIdentity, r.embeddingModelIdentity, limit)
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
