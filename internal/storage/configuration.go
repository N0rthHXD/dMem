package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type RevisionRecord struct {
	RevisionID        string
	SourceIdentity    string
	EffectiveIdentity string
	ConfigVersion     int
	LoadedAt          time.Time
	RedactedDocument  []byte
}

type ActivationRecord struct {
	ActivationID         string
	RevisionID           string
	PreviousActivationID string
	Trigger              string
	RequestedBy          string
	Outcome              string
	ErrorCode            string
	Diagnostics          []byte
	StartedAt            time.Time
	FinishedAt           time.Time
}

type CapabilityStateRecord struct {
	Capability              string
	Enabled                 bool
	ConfigurationIdentity   string
	ConfigurationGeneration int
	CompatibilityIdentity   string
	PolicyIdentity          string
	PolicyGeneration        int
	HealthState             string
	ReconciliationState     string
	BlockedWorkCount        int
}

type ProviderPolicyRecord struct {
	PolicyIdentity       string
	Capability           string
	DefaultDecision      string
	AllowedContentTypes  []string
	AllowedSourceTypes   []string
	AllowedSensitivities []string
	QueryAllowed         bool
	CreatedAt            time.Time
}

type PolicyAssignmentRecord struct {
	AssignmentID       string
	PolicyIdentity     string
	Capability         string
	SelectorKind       string
	SpaceID            string
	SpaceType          string
	ExternalID         string
	Effect             string
	IncludeDescendants bool
	Priority           int
}

type ReconciliationRunRecord struct {
	RunID                       string
	Capability                  string
	ActivationID                string
	ChangeClass                 string
	FromConfigurationGeneration int
	ToConfigurationGeneration   int
	FromPolicyGeneration        int
	ToPolicyGeneration          int
	TargetPolicyIdentity        string
}

type ClaimedReconciliationRun struct {
	ReconciliationRunRecord
	Cursor            string
	ScanHighWatermark string
}

type ReconciliationBatch struct {
	Cursor, NextCursor           string
	Affected, Scheduled, Blocked int
	Complete                     bool
}

type ActivationCommit struct {
	Revision           RevisionRecord
	Activation         ActivationRecord
	Capabilities       []CapabilityStateRecord
	Policies           []ProviderPolicyRecord
	Assignments        []PolicyAssignmentRecord
	ReconciliationRuns []ReconciliationRunRecord
}

type EffectiveConfigurationState struct {
	ActivationID         string
	SourceIdentity       string
	EffectiveIdentity    string
	ActivationGeneration int
	ActivatedAt          string
	LastAttemptID        string
}

func (r *Repository) CommitActivation(ctx context.Context, commit ActivationCommit) error {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := persistRevision(ctx, tx, commit.Revision); err != nil {
		return fmt.Errorf("persist revision: %w", err)
	}
	if err := persistActivation(ctx, tx, commit.Activation); err != nil {
		return fmt.Errorf("persist activation: %w", err)
	}
	if commit.Activation.Outcome == "activated" {
		if err := persistEffectiveState(ctx, tx, commit.Activation, commit.Revision); err != nil {
			return fmt.Errorf("persist effective state: %w", err)
		}
	}
	for _, cap := range commit.Capabilities {
		for _, run := range commit.ReconciliationRuns {
			if run.Capability == cap.Capability {
				cap.ReconciliationState = "queued"
				break
			}
		}
		if err := persistCapabilityState(ctx, tx, cap, commit.Activation.ActivationID); err != nil {
			return fmt.Errorf("persist capability state %s: %w", cap.Capability, err)
		}
	}
	for _, policy := range commit.Policies {
		if err := persistProviderPolicy(ctx, tx, policy); err != nil {
			return fmt.Errorf("persist provider policy: %w", err)
		}
	}
	for _, asg := range commit.Assignments {
		if err := persistPolicyAssignment(ctx, tx, asg); err != nil {
			return fmt.Errorf("persist policy assignment: %w", err)
		}
	}
	for _, run := range commit.ReconciliationRuns {
		if err := persistReconciliationRun(ctx, tx, run, commit.Activation.ActivationID); err != nil {
			return fmt.Errorf("persist reconciliation run: %w", err)
		}
	}
	return tx.Commit()
}

func persistRevision(ctx context.Context, tx *sql.Tx, rev RevisionRecord) error {
	var redacted any
	if rev.RedactedDocument != nil {
		redacted = string(rev.RedactedDocument)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO configuration_revisions(revision_id, source_identity, effective_identity, config_version, loaded_at, redacted_document) VALUES (?, ?, ?, ?, ?, ?)`,
		rev.RevisionID, rev.SourceIdentity, nullableString(rev.EffectiveIdentity), rev.ConfigVersion, formatTime(rev.LoadedAt), redacted)
	return err
}

func persistActivation(ctx context.Context, tx *sql.Tx, act ActivationRecord) error {
	var diagnostics any
	if act.Diagnostics != nil {
		diagnostics = string(act.Diagnostics)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO configuration_activations(activation_id, revision_id, previous_activation_id, trigger, requested_by, outcome, error_code, diagnostics, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		act.ActivationID, nullableString(act.RevisionID), nullableString(act.PreviousActivationID),
		act.Trigger, act.RequestedBy, act.Outcome, nullableString(act.ErrorCode), diagnostics,
		formatTime(act.StartedAt), formatTime(act.FinishedAt))
	return err
}

func persistEffectiveState(ctx context.Context, tx *sql.Tx, act ActivationRecord, rev RevisionRecord) error {
	result, err := tx.ExecContext(ctx,
		`UPDATE effective_configuration_state SET activation_id=?, source_identity=?, effective_identity=?, activation_generation=coalesce(activation_generation, 0) + 1, activated_at=?, last_attempt_id=? WHERE singleton=1`,
		act.ActivationID, rev.SourceIdentity, nullableString(rev.EffectiveIdentity), formatTime(act.FinishedAt), act.ActivationID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO effective_configuration_state(singleton, activation_id, source_identity, effective_identity, activation_generation, activated_at, last_attempt_id) VALUES (1, ?, ?, ?, 1, ?, ?)`,
			act.ActivationID, rev.SourceIdentity, nullableString(rev.EffectiveIdentity), formatTime(act.FinishedAt), act.ActivationID)
	}
	return err
}

func persistCapabilityState(ctx context.Context, tx *sql.Tx, cap CapabilityStateRecord, activationID string) error {
	enabled := 0
	if cap.Enabled {
		enabled = 1
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO capability_states(capability, enabled, configuration_identity, configuration_generation, compatibility_identity, policy_identity, policy_generation, activation_id, health_state, reconciliation_state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(capability) DO UPDATE SET enabled=excluded.enabled, configuration_identity=excluded.configuration_identity, configuration_generation=excluded.configuration_generation, compatibility_identity=excluded.compatibility_identity, policy_identity=excluded.policy_identity, policy_generation=excluded.policy_generation, activation_id=excluded.activation_id, health_state=excluded.health_state, reconciliation_state=excluded.reconciliation_state`,
		cap.Capability, enabled, nullableString(cap.ConfigurationIdentity), nullableInt(cap.ConfigurationGeneration),
		nullableString(cap.CompatibilityIdentity), nullableString(cap.PolicyIdentity), nullableInt(cap.PolicyGeneration),
		nullableString(activationID), cap.HealthState, cap.ReconciliationState)
	return err
}

func persistProviderPolicy(ctx context.Context, tx *sql.Tx, policy ProviderPolicyRecord) error {
	contentTypes, _ := json.Marshal(policy.AllowedContentTypes)
	sourceTypes, _ := json.Marshal(policy.AllowedSourceTypes)
	sensitivities, _ := json.Marshal(policy.AllowedSensitivities)
	queryAllowed := 0
	if policy.QueryAllowed {
		queryAllowed = 1
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO provider_policies(policy_identity, capability, default_decision, allowed_content_types, allowed_source_types, allowed_sensitivities, query_allowed, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(policy_identity, capability) DO NOTHING`,
		policy.PolicyIdentity, policy.Capability, policy.DefaultDecision, string(contentTypes), string(sourceTypes), string(sensitivities), queryAllowed, formatTime(policy.CreatedAt))
	return err
}

func persistPolicyAssignment(ctx context.Context, tx *sql.Tx, asg PolicyAssignmentRecord) error {
	includeDescendants := 0
	if asg.IncludeDescendants {
		includeDescendants = 1
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO policy_assignments(assignment_id, policy_identity, capability, selector_kind, space_id, space_type, external_id, effect, include_descendants, priority) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(assignment_id) DO NOTHING`,
		asg.AssignmentID, asg.PolicyIdentity, asg.Capability, asg.SelectorKind,
		nullableString(asg.SpaceID), nullableString(asg.SpaceType), nullableString(asg.ExternalID),
		asg.Effect, includeDescendants, asg.Priority)
	return err
}

func persistReconciliationRun(ctx context.Context, tx *sql.Tx, run ReconciliationRunRecord, activationID string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO reconciliation_runs(run_id, capability, activation_id, change_class, from_configuration_generation, to_configuration_generation, from_policy_generation, to_policy_generation, target_policy_identity, state, affected_count, scheduled_count, blocked_count, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0, 0, 0, ?)`,
		run.RunID, run.Capability, activationID, run.ChangeClass,
		nullableInt(run.FromConfigurationGeneration), nullableInt(run.ToConfigurationGeneration),
		nullableInt(run.FromPolicyGeneration), nullableInt(run.ToPolicyGeneration), nullableString(run.TargetPolicyIdentity),
		formatTime(time.Now()))
	return err
}

func (r *Repository) CurrentConfigurationState(ctx context.Context) (EffectiveConfigurationState, error) {
	var state EffectiveConfigurationState
	var activationID, sourceIdentity, effectiveIdentity, activatedAt, lastAttempt sql.NullString
	var generation sql.NullInt64
	err := r.store.reader.QueryRowContext(ctx,
		`SELECT activation_id, source_identity, effective_identity, activation_generation, activated_at, last_attempt_id FROM effective_configuration_state WHERE singleton=1`).Scan(
		&activationID, &sourceIdentity, &effectiveIdentity, &generation, &activatedAt, &lastAttempt)
	if errors.Is(err, sql.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	state.ActivationID = activationID.String
	state.SourceIdentity = sourceIdentity.String
	state.EffectiveIdentity = effectiveIdentity.String
	if generation.Valid {
		state.ActivationGeneration = int(generation.Int64)
	}
	state.ActivatedAt = activatedAt.String
	state.LastAttemptID = lastAttempt.String
	return state, nil
}

func (r *Repository) CurrentCapabilityStates(ctx context.Context) (map[string]CapabilityStateRecord, error) {
	rows, err := r.store.reader.QueryContext(ctx,
		`SELECT cs.capability, cs.enabled, cs.configuration_identity, cs.configuration_generation, cs.compatibility_identity, cs.policy_identity, cs.policy_generation, cs.activation_id, cs.health_state, cs.reconciliation_state,
			(SELECT count(*) FROM jobs j WHERE j.status='blocked_policy' AND ((cs.capability='processing' AND j.job_type='extract_memory') OR j.capability=cs.capability))
		 FROM capability_states cs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]CapabilityStateRecord)
	for rows.Next() {
		var cap CapabilityStateRecord
		var enabled int
		var configIdentity, compatIdentity, policyID, activationID sql.NullString
		var configGen, policyGen sql.NullInt64
		if err := rows.Scan(&cap.Capability, &enabled, &configIdentity, &configGen, &compatIdentity, &policyID, &policyGen, &activationID, &cap.HealthState, &cap.ReconciliationState, &cap.BlockedWorkCount); err != nil {
			return nil, err
		}
		cap.Enabled = enabled == 1
		cap.ConfigurationIdentity = configIdentity.String
		cap.CompatibilityIdentity = compatIdentity.String
		cap.PolicyIdentity = policyID.String
		if configGen.Valid {
			cap.ConfigurationGeneration = int(configGen.Int64)
		}
		if policyGen.Valid {
			cap.PolicyGeneration = int(policyGen.Int64)
		}
		result[cap.Capability] = cap
	}
	return result, rows.Err()
}

func (r *Repository) ReconciliationRun(ctx context.Context, runID string) (ClaimedReconciliationRun, error) {
	var run ClaimedReconciliationRun
	err := r.store.reader.QueryRowContext(ctx, `SELECT run_id, capability, activation_id, change_class, coalesce(from_configuration_generation,0), coalesce(to_configuration_generation,0), coalesce(from_policy_generation,0), coalesce(to_policy_generation,0), coalesce(target_policy_identity,''), coalesce(cursor,''), coalesce(scan_high_watermark,'') FROM reconciliation_runs WHERE run_id=?`, runID).Scan(&run.RunID, &run.Capability, &run.ActivationID, &run.ChangeClass, &run.FromConfigurationGeneration, &run.ToConfigurationGeneration, &run.FromPolicyGeneration, &run.ToPolicyGeneration, &run.TargetPolicyIdentity, &run.Cursor, &run.ScanHighWatermark)
	if errors.Is(err, sql.ErrNoRows) {
		return run, ErrNotFound
	}
	return run, err
}

// ClaimReconciliationRun claims one durable run. A restarted daemon can claim a
// running row again because every batch commits its cursor before returning.
func (r *Repository) ClaimReconciliationRun(ctx context.Context) (ClaimedReconciliationRun, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return ClaimedReconciliationRun{}, err
	}
	defer tx.Rollback()
	var run ClaimedReconciliationRun
	err = tx.QueryRowContext(ctx, `SELECT run_id, capability, activation_id, change_class, coalesce(from_configuration_generation,0), coalesce(to_configuration_generation,0), coalesce(from_policy_generation,0), coalesce(to_policy_generation,0), coalesce(target_policy_identity,''), coalesce(cursor,''), coalesce(scan_high_watermark,'') FROM reconciliation_runs WHERE state IN ('pending','running') ORDER BY state='running', created_at, run_id LIMIT 1`).Scan(&run.RunID, &run.Capability, &run.ActivationID, &run.ChangeClass, &run.FromConfigurationGeneration, &run.ToConfigurationGeneration, &run.FromPolicyGeneration, &run.ToPolicyGeneration, &run.TargetPolicyIdentity, &run.Cursor, &run.ScanHighWatermark)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimedReconciliationRun{}, ErrNotFound
	}
	if err != nil {
		return ClaimedReconciliationRun{}, err
	}
	now := formatTime(r.runtime.Now())
	if run.ScanHighWatermark == "" {
		query := `SELECT coalesce(max(job_id),'') FROM jobs WHERE status='blocked_policy' AND job_type='extract_memory'`
		if run.Capability == "embedding" {
			query = `SELECT coalesce(max(memory_id),'') FROM memory_projections WHERE searchable=1`
		}
		if err := tx.QueryRowContext(ctx, query).Scan(&run.ScanHighWatermark); err != nil {
			return ClaimedReconciliationRun{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE reconciliation_runs SET state='running', scan_high_watermark=?, started_at=coalesce(started_at,?), updated_at=? WHERE run_id=? AND state IN ('pending','running')`, nullableString(run.ScanHighWatermark), now, now, run.RunID); err != nil {
		return ClaimedReconciliationRun{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE capability_states SET reconciliation_state='running' WHERE capability=?`, run.Capability); err != nil {
		return ClaimedReconciliationRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClaimedReconciliationRun{}, err
	}
	return run, nil
}

// CompleteReconciliationRun advances the durable cursor or completes the run.
func (r *Repository) CompleteReconciliationRun(ctx context.Context, run ClaimedReconciliationRun, cursor string, affected, scheduled, blocked int, complete bool) error {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(r.runtime.Now())
	state, finished := "running", any(nil)
	if complete {
		state, finished = "completed", now
	}
	result, err := tx.ExecContext(ctx, `UPDATE reconciliation_runs SET state=?, cursor=?, affected_count=affected_count+?, scheduled_count=scheduled_count+?, blocked_count=blocked_count+?, updated_at=?, finished_at=? WHERE run_id=? AND state='running'`, state, nullableString(cursor), affected, scheduled, blocked, now, finished, run.RunID)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrNotFound
	}
	if complete {
		if _, err := tx.ExecContext(ctx, `UPDATE capability_states SET reconciliation_state='idle' WHERE capability=?`, run.Capability); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Reconcile runs a bounded policy/configuration recovery scan. It changes only
// derived job and eligibility projections, never authority records.
func (r *Repository) Reconcile(ctx context.Context, run ClaimedReconciliationRun, limit int) (ReconciliationBatch, error) {
	if limit < 1 {
		limit = 100
	}
	batch := ReconciliationBatch{Cursor: run.Cursor}
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return batch, err
	}
	defer tx.Rollback()
	finish := func() (ReconciliationBatch, error) {
		state, finished := "running", any(nil)
		if batch.Complete {
			state, finished = "completed", formatTime(r.runtime.Now())
		}
		result, err := tx.ExecContext(ctx, `UPDATE reconciliation_runs SET state=?, cursor=?, affected_count=affected_count+?, scheduled_count=scheduled_count+?, blocked_count=blocked_count+?, updated_at=?, finished_at=? WHERE run_id=? AND state='running' AND coalesce(cursor,'')=?`, state, nullableString(batch.NextCursor), batch.Affected, batch.Scheduled, batch.Blocked, formatTime(r.runtime.Now()), finished, run.RunID, run.Cursor)
		if err != nil || !oneRow(result) {
			if err == nil {
				err = ErrNotFound
			}
			return batch, err
		}
		if batch.Complete {
			if _, err := tx.ExecContext(ctx, `UPDATE capability_states SET reconciliation_state=CASE WHEN EXISTS (SELECT 1 FROM reconciliation_runs WHERE capability=? AND run_id<>? AND state IN ('pending','running')) THEN 'queued' ELSE 'idle' END WHERE capability=?`, run.Capability, run.RunID, run.Capability); err != nil {
				return batch, err
			}
		}
		return batch, tx.Commit()
	}
	if run.Capability == "processing" {
		if run.ChangeClass == "disable" || run.ChangeClass == "runtime_limits" {
			batch.Complete = true
			return finish()
		}
		rows, err := tx.QueryContext(ctx, `SELECT j.job_id, e.space_id FROM jobs j JOIN raw_events e ON e.event_id=j.target_id WHERE j.status='blocked_policy' AND j.job_type='extract_memory' AND j.job_id>? AND j.job_id<=? ORDER BY j.job_id LIMIT ?`, run.Cursor, run.ScanHighWatermark, limit)
		if err != nil {
			return batch, err
		}
		defer rows.Close()
		for rows.Next() {
			var jobID, spaceID string
			if err := rows.Scan(&jobID, &spaceID); err != nil {
				return batch, err
			}
			batch.Affected++
			policy, err := r.resolveEffectivePolicy(ctx, tx, "processing", spaceID, run.TargetPolicyIdentity, run.ToPolicyGeneration)
			if err != nil {
				return batch, err
			}
			batch.NextCursor = jobID
			if policy.Decision != "allow" {
				batch.Blocked++
				continue
			}
			result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='pending', next_run_at=?, blocked_reason_code=NULL, policy_generation=?, configuration_generation=?, reconciliation_run_id=?, finished_at=NULL WHERE job_id=? AND status='blocked_policy'`, formatTime(r.runtime.Now()), policy.PolicyGeneration, run.ToConfigurationGeneration, run.RunID, jobID)
			if err != nil {
				return batch, err
			}
			if oneRow(result) {
				batch.Scheduled++
			}
		}
		if err := rows.Err(); err != nil {
			return batch, err
		}
		batch.Complete = batch.Affected < limit || batch.NextCursor == run.ScanHighWatermark
		return finish()
	}
	if run.Capability != "embedding" {
		return batch, fmt.Errorf("unknown reconciliation capability %q", run.Capability)
	}
	if run.ChangeClass == "disable" || run.ChangeClass == "runtime_limits" {
		batch.Complete = true
		return finish()
	}
	rows, err := tx.QueryContext(ctx, `SELECT memory_id, revision_id, space_id FROM memory_projections WHERE searchable=1 AND memory_id>? AND memory_id<=? ORDER BY memory_id, revision_id LIMIT ?`, run.Cursor, run.ScanHighWatermark, limit)
	if err != nil {
		return batch, err
	}
	defer rows.Close()
	for rows.Next() {
		var memoryID, revisionID, spaceID string
		if err := rows.Scan(&memoryID, &revisionID, &spaceID); err != nil {
			return batch, err
		}
		batch.Affected++
		policy, err := r.resolveEffectivePolicy(ctx, tx, "embedding", spaceID, run.TargetPolicyIdentity, run.ToPolicyGeneration)
		if err != nil {
			return batch, err
		}
		eligible := policy.Decision == "allow"
		reason := policy.ReasonCode
		if _, err := tx.ExecContext(ctx, `INSERT INTO embedding_eligibility(memory_id,revision_id,space_id,configuration_generation,policy_generation,eligible,reason_code,evaluated_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(memory_id,revision_id,policy_generation) DO UPDATE SET eligible=excluded.eligible,reason_code=excluded.reason_code,evaluated_at=excluded.evaluated_at`, memoryID, revisionID, spaceID, run.ToConfigurationGeneration, policy.PolicyGeneration, boolInt(eligible), reason, formatTime(r.runtime.Now())); err != nil {
			return batch, err
		}
		batch.NextCursor = memoryID
		if !eligible {
			batch.Blocked++
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status='superseded', blocked_reason_code='policy_ineligible', finished_at=? WHERE job_type='embed_memory' AND target_id=? AND status='blocked_policy'`, formatTime(r.runtime.Now()), revisionID); err != nil {
				return batch, err
			}
			continue
		}
		var compatible int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM memory_embeddings WHERE memory_id=? AND revision_id=? AND compatibility_identity=(SELECT compatibility_identity FROM capability_states WHERE capability='embedding'))`, memoryID, revisionID).Scan(&compatible); err != nil {
			return batch, err
		}
		if compatible == 0 {
			ids, err := r.newIDs(2)
			if err != nil {
				return batch, err
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,capability,configuration_generation,policy_generation,reconciliation_run_id,created_at) VALUES (?,?,'embed_memory','memory_revision',?,?,'pending',100,?,?,'embedding',?,?,?,?) ON CONFLICT DO NOTHING`, ids[0], ids[1], revisionID, r.embeddingIdentity(), r.maxAttempts, formatTime(r.runtime.Now()), run.ToConfigurationGeneration, policy.PolicyGeneration, run.RunID, formatTime(r.runtime.Now()))
			if err != nil {
				return batch, err
			}
			if oneRow(result) {
				batch.Scheduled++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return batch, err
	}
	batch.Complete = batch.Affected < limit || batch.NextCursor == run.ScanHighWatermark
	if batch.Complete {
		if _, err := r.enqueueSemanticRebuild(ctx, tx, formatTime(r.runtime.Now())); err != nil {
			return batch, err
		}
	}
	return finish()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (r *Repository) LatestActivationAttempt(ctx context.Context) (ActivationRecord, error) {
	var act ActivationRecord
	var revisionID, prevActivation, errorCode, diagnostics sql.NullString
	var startedAt, finishedAt string
	err := r.store.reader.QueryRowContext(ctx,
		`SELECT activation_id, revision_id, previous_activation_id, trigger, requested_by, outcome, error_code, diagnostics, started_at, finished_at FROM configuration_activations ORDER BY started_at DESC LIMIT 1`).Scan(
		&act.ActivationID, &revisionID, &prevActivation, &act.Trigger, &act.RequestedBy, &act.Outcome,
		&errorCode, &diagnostics, &startedAt, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return act, ErrNotFound
	}
	if err != nil {
		return act, err
	}
	act.RevisionID = revisionID.String
	act.PreviousActivationID = prevActivation.String
	act.ErrorCode = errorCode.String
	if diagnostics.Valid {
		act.Diagnostics = []byte(diagnostics.String)
	}
	act.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	act.FinishedAt, _ = time.Parse(time.RFC3339Nano, finishedAt)
	return act, nil
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
