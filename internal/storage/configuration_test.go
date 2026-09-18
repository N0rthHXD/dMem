package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

type configTestRuntime struct {
	now time.Time
	id  int
	mu  sync.Mutex
}

func (r *configTestRuntime) runtime() domain.Runtime {
	return domain.Runtime{
		Now: func() time.Time { return r.now },
		ID: func() (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.id++
			return fmt.Sprintf("config-id-%03d", r.id), nil
		},
	}
}

func newConfigTestRepository(t *testing.T) *Repository {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "config.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	rt := &configTestRuntime{now: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}
	return NewRepository(store, rt.runtime(), 100, 200, 3, "v1")
}

func TestCommitActivationPersistsAllEntitiesAtomically(t *testing.T) {
	t.Parallel()
	repo := newConfigTestRepository(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	commit := ActivationCommit{
		Revision: RevisionRecord{
			RevisionID:        "rev-1",
			SourceIdentity:    "sha256:source1",
			EffectiveIdentity: "sha256:eff1",
			ConfigVersion:     1,
			LoadedAt:          now,
			RedactedDocument:  []byte(`{"config_version":1}`),
		},
		Activation: ActivationRecord{
			ActivationID: "act-1",
			RevisionID:   "rev-1",
			Trigger:      "startup",
			RequestedBy:  "daemon",
			Outcome:      "activated",
			StartedAt:    now,
			FinishedAt:   now.Add(time.Second),
		},
		Capabilities: []CapabilityStateRecord{
			{Capability: "processing", Enabled: true, ConfigurationIdentity: "sha256:proc-cfg", ConfigurationGeneration: 1, CompatibilityIdentity: "sha256:proc-compat", PolicyIdentity: "sha256:proc-pol", PolicyGeneration: 1, HealthState: "ready", ReconciliationState: "idle"},
			{Capability: "embedding", Enabled: false, HealthState: "disabled", ReconciliationState: "idle"},
		},
		Policies: []ProviderPolicyRecord{
			{PolicyIdentity: "sha256:proc-pol", Capability: "processing", DefaultDecision: "deny", AllowedContentTypes: []string{"text/plain"}, AllowedSourceTypes: []string{"manual"}, AllowedSensitivities: []string{"normal"}, QueryAllowed: false, CreatedAt: now},
		},
		Assignments: []PolicyAssignmentRecord{
			{AssignmentID: "asg-1", PolicyIdentity: "sha256:proc-pol", Capability: "processing", SelectorKind: "space_type", SpaceType: "user", Effect: "allow", Priority: 0},
		},
		ReconciliationRuns: []ReconciliationRunRecord{
			{RunID: "run-1", Capability: "processing", ChangeClass: "enable", ToConfigurationGeneration: 1, ToPolicyGeneration: 1},
		},
	}
	if err := repo.CommitActivation(context.Background(), commit); err != nil {
		t.Fatalf("CommitActivation failed: %v", err)
	}

	state, err := repo.CurrentConfigurationState(context.Background())
	if err != nil {
		t.Fatalf("CurrentConfigurationState failed: %v", err)
	}
	if state.ActivationID != "act-1" {
		t.Fatalf("activation_id = %q; want act-1", state.ActivationID)
	}
	if state.ActivationGeneration != 1 {
		t.Fatalf("activation_generation = %d; want 1", state.ActivationGeneration)
	}
	if state.SourceIdentity != "sha256:source1" {
		t.Fatalf("source_identity = %q; want sha256:source1", state.SourceIdentity)
	}

	caps, err := repo.CurrentCapabilityStates(context.Background())
	if err != nil {
		t.Fatalf("CurrentCapabilityStates failed: %v", err)
	}
	proc, ok := caps["processing"]
	if !ok {
		t.Fatal("processing capability state missing")
	}
	if !proc.Enabled || proc.HealthState != "ready" {
		t.Fatalf("processing state = %+v; want enabled ready", proc)
	}
	if proc.ConfigurationIdentity != "sha256:proc-cfg" {
		t.Fatalf("processing configuration_identity = %q; want sha256:proc-cfg", proc.ConfigurationIdentity)
	}
	emb, ok := caps["embedding"]
	if !ok {
		t.Fatal("embedding capability state missing")
	}
	if emb.Enabled {
		t.Fatal("embedding should be disabled")
	}

	var policyCount int
	if err := repo.store.reader.QueryRowContext(context.Background(), `SELECT count(*) FROM provider_policies WHERE policy_identity='sha256:proc-pol'`).Scan(&policyCount); err != nil || policyCount != 1 {
		t.Fatalf("provider_policies count = %d, err=%v; want 1", policyCount, err)
	}
	var asgCount int
	if err := repo.store.reader.QueryRowContext(context.Background(), `SELECT count(*) FROM policy_assignments WHERE policy_identity='sha256:proc-pol'`).Scan(&asgCount); err != nil || asgCount != 1 {
		t.Fatalf("policy_assignments count = %d, err=%v; want 1", asgCount, err)
	}
	var runCount int
	if err := repo.store.reader.QueryRowContext(context.Background(), `SELECT count(*) FROM reconciliation_runs WHERE activation_id='act-1'`).Scan(&runCount); err != nil || runCount != 1 {
		t.Fatalf("reconciliation_runs count = %d, err=%v; want 1", runCount, err)
	}
	var actCount int
	if err := repo.store.reader.QueryRowContext(context.Background(), `SELECT count(*) FROM configuration_activations`).Scan(&actCount); err != nil || actCount != 1 {
		t.Fatalf("configuration_activations count = %d, err=%v; want 1", actCount, err)
	}
}

func TestCommitActivationSecondActivationIncrementsGeneration(t *testing.T) {
	t.Parallel()
	repo := newConfigTestRepository(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	first := ActivationCommit{
		Revision:   RevisionRecord{RevisionID: "rev-1", SourceIdentity: "sha256:src1", EffectiveIdentity: "sha256:eff1", ConfigVersion: 1, LoadedAt: now},
		Activation: ActivationRecord{ActivationID: "act-1", RevisionID: "rev-1", Trigger: "startup", RequestedBy: "daemon", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities: []CapabilityStateRecord{
			{Capability: "processing", Enabled: false, HealthState: "disabled", ReconciliationState: "idle"},
			{Capability: "embedding", Enabled: false, HealthState: "disabled", ReconciliationState: "idle"},
		},
	}
	if err := repo.CommitActivation(context.Background(), first); err != nil {
		t.Fatalf("first CommitActivation: %v", err)
	}
	second := ActivationCommit{
		Revision:   RevisionRecord{RevisionID: "rev-2", SourceIdentity: "sha256:src2", EffectiveIdentity: "sha256:eff2", ConfigVersion: 1, LoadedAt: now.Add(time.Minute)},
		Activation: ActivationRecord{ActivationID: "act-2", RevisionID: "rev-2", PreviousActivationID: "act-1", Trigger: "api", RequestedBy: "operator", Outcome: "activated", StartedAt: now.Add(time.Minute), FinishedAt: now.Add(time.Minute + time.Second)},
		Capabilities: []CapabilityStateRecord{
			{Capability: "processing", Enabled: true, ConfigurationIdentity: "sha256:proc2", ConfigurationGeneration: 2, CompatibilityIdentity: "sha256:compat2", PolicyIdentity: "sha256:pol2", PolicyGeneration: 2, HealthState: "ready", ReconciliationState: "idle"},
			{Capability: "embedding", Enabled: false, HealthState: "disabled", ReconciliationState: "idle"},
		},
	}
	if err := repo.CommitActivation(context.Background(), second); err != nil {
		t.Fatalf("second CommitActivation: %v", err)
	}
	state, err := repo.CurrentConfigurationState(context.Background())
	if err != nil {
		t.Fatalf("CurrentConfigurationState: %v", err)
	}
	if state.ActivationGeneration != 2 {
		t.Fatalf("activation_generation = %d; want 2", state.ActivationGeneration)
	}
	if state.ActivationID != "act-2" {
		t.Fatalf("activation_id = %q; want act-2", state.ActivationID)
	}
}

func TestCommitActivationRejectedDoesNotUpdateEffectiveState(t *testing.T) {
	t.Parallel()
	repo := newConfigTestRepository(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	first := ActivationCommit{
		Revision:   RevisionRecord{RevisionID: "rev-1", SourceIdentity: "sha256:src1", EffectiveIdentity: "sha256:eff1", ConfigVersion: 1, LoadedAt: now},
		Activation: ActivationRecord{ActivationID: "act-1", RevisionID: "rev-1", Trigger: "startup", RequestedBy: "daemon", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities: []CapabilityStateRecord{
			{Capability: "processing", Enabled: false, HealthState: "disabled", ReconciliationState: "idle"},
			{Capability: "embedding", Enabled: false, HealthState: "disabled", ReconciliationState: "idle"},
		},
	}
	if err := repo.CommitActivation(context.Background(), first); err != nil {
		t.Fatalf("first CommitActivation: %v", err)
	}
	rejected := ActivationCommit{
		Revision:   RevisionRecord{RevisionID: "rev-2", SourceIdentity: "sha256:src2", ConfigVersion: 1, LoadedAt: now.Add(time.Minute)},
		Activation: ActivationRecord{ActivationID: "act-rej", RevisionID: "rev-2", PreviousActivationID: "act-1", Trigger: "api", RequestedBy: "operator", Outcome: "rejected", ErrorCode: "config_invalid", StartedAt: now.Add(time.Minute), FinishedAt: now.Add(time.Minute + time.Second)},
	}
	if err := repo.CommitActivation(context.Background(), rejected); err != nil {
		t.Fatalf("rejected CommitActivation: %v", err)
	}
	state, err := repo.CurrentConfigurationState(context.Background())
	if err != nil {
		t.Fatalf("CurrentConfigurationState: %v", err)
	}
	if state.ActivationID != "act-1" {
		t.Fatalf("activation_id = %q; want act-1 (rejected should not update)", state.ActivationID)
	}
	if state.ActivationGeneration != 1 {
		t.Fatalf("activation_generation = %d; want 1", state.ActivationGeneration)
	}
}

func TestCommitActivationNoChangeDoesNotIncrementGeneration(t *testing.T) {
	t.Parallel()
	repo := newConfigTestRepository(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	first := ActivationCommit{
		Revision:   RevisionRecord{RevisionID: "rev-1", SourceIdentity: "sha256:src1", EffectiveIdentity: "sha256:eff1", ConfigVersion: 1, LoadedAt: now},
		Activation: ActivationRecord{ActivationID: "act-1", RevisionID: "rev-1", Trigger: "startup", RequestedBy: "daemon", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities: []CapabilityStateRecord{
			{Capability: "processing", Enabled: false, HealthState: "disabled", ReconciliationState: "idle"},
			{Capability: "embedding", Enabled: false, HealthState: "disabled", ReconciliationState: "idle"},
		},
	}
	if err := repo.CommitActivation(context.Background(), first); err != nil {
		t.Fatalf("first CommitActivation: %v", err)
	}
	noChange := ActivationCommit{
		Revision:   RevisionRecord{RevisionID: "rev-2", SourceIdentity: "sha256:src2", EffectiveIdentity: "sha256:eff1", ConfigVersion: 1, LoadedAt: now.Add(time.Minute)},
		Activation: ActivationRecord{ActivationID: "act-nc", RevisionID: "rev-2", PreviousActivationID: "act-1", Trigger: "sighup", RequestedBy: "daemon", Outcome: "no_change", StartedAt: now.Add(time.Minute), FinishedAt: now.Add(time.Minute + time.Second)},
	}
	if err := repo.CommitActivation(context.Background(), noChange); err != nil {
		t.Fatalf("no_change CommitActivation: %v", err)
	}
	state, err := repo.CurrentConfigurationState(context.Background())
	if err != nil {
		t.Fatalf("CurrentConfigurationState: %v", err)
	}
	if state.ActivationGeneration != 1 {
		t.Fatalf("activation_generation = %d; want 1 (no_change should not increment)", state.ActivationGeneration)
	}
	if state.ActivationID != "act-1" {
		t.Fatalf("activation_id = %q; want act-1", state.ActivationID)
	}
	var runCount int
	if err := repo.store.reader.QueryRow(`SELECT count(*) FROM reconciliation_runs`).Scan(&runCount); err != nil || runCount != 0 {
		t.Fatalf("no-change reconciliation runs = %d, err=%v; want 0", runCount, err)
	}
}

func TestReconciliationPersistsLineageAndResumesBoundedBatches(t *testing.T) {
	ctx := context.Background()
	repo := newConfigTestRepository(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	commit := ActivationCommit{
		Revision:           RevisionRecord{RevisionID: "revision", SourceIdentity: "source", EffectiveIdentity: "effective", ConfigVersion: 1, LoadedAt: now},
		Activation:         ActivationRecord{ActivationID: "activation", RevisionID: "revision", Trigger: "api", RequestedBy: "test", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities:       []CapabilityStateRecord{{Capability: "processing", Enabled: true, ConfigurationIdentity: "config", ConfigurationGeneration: 2, CompatibilityIdentity: "compat", PolicyIdentity: "policy-2", PolicyGeneration: 2, HealthState: "ready", ReconciliationState: "idle"}},
		Policies:           []ProviderPolicyRecord{{PolicyIdentity: "policy-2", Capability: "processing", DefaultDecision: "deny", AllowedContentTypes: []string{"text/plain"}, AllowedSourceTypes: []string{"manual"}, AllowedSensitivities: []string{"normal"}, CreatedAt: now}},
		Assignments:        []PolicyAssignmentRecord{{AssignmentID: "allow-root", PolicyIdentity: "policy-2", Capability: "processing", SelectorKind: "space_type", SpaceType: "user", Effect: "allow"}},
		ReconciliationRuns: []ReconciliationRunRecord{{RunID: "run", Capability: "processing", ChangeClass: "policy_expand", FromConfigurationGeneration: 1, ToConfigurationGeneration: 2, FromPolicyGeneration: 1, ToPolicyGeneration: 2, TargetPolicyIdentity: "policy-2"}},
	}
	if err := repo.CommitActivation(ctx, commit); err != nil {
		t.Fatal(err)
	}
	insertPolicySpace(t, repo, "root", "", "user", "root")
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("job-%d", i)
		eventID := fmt.Sprintf("event-%d", i)
		contentID := fmt.Sprintf("content-%d", i)
		if _, err := repo.store.writer.Exec(`INSERT INTO raw_contents(content_id,content_kind,content_text,media_type,content_hash,size_bytes,created_at) VALUES (?,'text','content','text/plain',?,7,?)`, contentID, contentID, formatTime(now)); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.store.writer.Exec(`INSERT INTO raw_events(event_id,operation_id,space_id,content_id,source_type,source_name,caller_id,occurred_at,received_at,created_at) VALUES (?,?,'root',?,'manual','test','test',?,?,?)`, eventID, "operation-"+eventID, contentID, formatTime(now), formatTime(now), formatTime(now)); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,capability,configuration_generation,policy_generation,blocked_reason_code,created_at) VALUES (?,?,'extract_memory','raw_event',?,'v1','blocked_policy',3,?,'processing',1,1,'target_policy_denied',?)`, id, "operation-"+id, eventID, formatTime(now), formatTime(now)); err != nil {
			t.Fatal(err)
		}
	}

	run, err := repo.ClaimReconciliationRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if run.FromPolicyGeneration != 1 || run.ToPolicyGeneration != 2 || run.TargetPolicyIdentity != "policy-2" {
		t.Fatalf("claimed lineage = %+v", run)
	}
	first, err := repo.Reconcile(ctx, run, 2)
	if err != nil || first.Complete || first.Affected != 2 || first.NextCursor == "" {
		t.Fatalf("first reconciliation = %+v, %v", first, err)
	}

	resumed, err := repo.ClaimReconciliationRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Cursor != first.NextCursor || resumed.ScanHighWatermark != run.ScanHighWatermark {
		t.Fatalf("resumed run = %+v; want cursor %q and high watermark %q", resumed, first.NextCursor, run.ScanHighWatermark)
	}
	last, err := repo.Reconcile(ctx, resumed, 2)
	if err != nil || !last.Complete || last.Affected != 1 {
		t.Fatalf("resumed reconciliation = %+v, %v", last, err)
	}
	var pending int
	if err := repo.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE status='pending' AND reconciliation_run_id='run'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	var runState, capabilityState string
	if err := repo.store.reader.QueryRow(`SELECT state FROM reconciliation_runs WHERE run_id='run'`).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if err := repo.store.reader.QueryRow(`SELECT reconciliation_state FROM capability_states WHERE capability='processing'`).Scan(&capabilityState); err != nil {
		t.Fatal(err)
	}
	if pending != 3 || runState != "completed" || capabilityState != "idle" {
		t.Fatalf("reconciliation state = pending %d, run %q, capability %q", pending, runState, capabilityState)
	}
}

func TestReconciliationRuntimeLimitsCompletesWithoutScanning(t *testing.T) {
	ctx := context.Background()
	repo := newConfigTestRepository(t)
	now := time.Now().UTC()
	commit := ActivationCommit{
		Revision:           RevisionRecord{RevisionID: "limits-revision", SourceIdentity: "source", EffectiveIdentity: "effective", ConfigVersion: 1, LoadedAt: now},
		Activation:         ActivationRecord{ActivationID: "limits-activation", RevisionID: "limits-revision", Trigger: "api", RequestedBy: "test", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities:       []CapabilityStateRecord{{Capability: "processing", Enabled: true, ConfigurationIdentity: "config", ConfigurationGeneration: 2, PolicyIdentity: "policy", PolicyGeneration: 1, HealthState: "ready", ReconciliationState: "idle"}},
		ReconciliationRuns: []ReconciliationRunRecord{{RunID: "limits-run", Capability: "processing", ChangeClass: "runtime_limits", ToConfigurationGeneration: 2, ToPolicyGeneration: 1, TargetPolicyIdentity: "policy"}},
	}
	if err := repo.CommitActivation(ctx, commit); err != nil {
		t.Fatal(err)
	}
	run, err := repo.ClaimReconciliationRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := repo.Reconcile(ctx, run, 1)
	if err != nil || !batch.Complete || batch.Affected != 0 {
		t.Fatalf("runtime limits reconciliation = %+v, %v", batch, err)
	}
}

func TestEmptyReconciliationCompletesDurably(t *testing.T) {
	repo := newConfigTestRepository(t)
	now := time.Now().UTC()
	commit := ActivationCommit{
		Revision:           RevisionRecord{RevisionID: "reconcile-revision", SourceIdentity: "source", EffectiveIdentity: "effective", ConfigVersion: 1, LoadedAt: now},
		Activation:         ActivationRecord{ActivationID: "reconcile-activation", RevisionID: "reconcile-revision", Trigger: "startup", RequestedBy: "test", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities:       []CapabilityStateRecord{{Capability: "processing", Enabled: true, ConfigurationIdentity: "config", ConfigurationGeneration: 1, PolicyIdentity: "policy", PolicyGeneration: 1, HealthState: "ready", ReconciliationState: "queued"}},
		Policies:           []ProviderPolicyRecord{{PolicyIdentity: "policy", Capability: "processing", DefaultDecision: "deny", AllowedContentTypes: []string{"text/plain"}, AllowedSourceTypes: []string{"manual"}, AllowedSensitivities: []string{"normal"}, CreatedAt: now}},
		ReconciliationRuns: []ReconciliationRunRecord{{RunID: "reconcile-run", Capability: "processing", ChangeClass: "policy_expand", ToConfigurationGeneration: 1, ToPolicyGeneration: 1}},
	}
	if err := repo.CommitActivation(context.Background(), commit); err != nil {
		t.Fatal(err)
	}
	run, err := repo.ClaimReconciliationRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := repo.Reconcile(context.Background(), run, 10)
	if err != nil || !batch.Complete {
		t.Fatalf("Reconcile() = %+v, %v", batch, err)
	}
	var state string
	var affected int
	if err := repo.store.reader.QueryRow(`SELECT state, affected_count FROM reconciliation_runs WHERE run_id='reconcile-run'`).Scan(&state, &affected); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || affected != 0 {
		t.Fatalf("run = %s/%d", state, affected)
	}
}

func TestSC004ReconciliationEnqueuesEligibleJobsPromptly(t *testing.T) {
	ctx := context.Background()
	repo := newConfigTestRepository(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	commit := ActivationCommit{
		Revision:           RevisionRecord{RevisionID: "sc004-rev", SourceIdentity: "src", EffectiveIdentity: "eff", ConfigVersion: 1, LoadedAt: now},
		Activation:         ActivationRecord{ActivationID: "sc004-act", RevisionID: "sc004-rev", Trigger: "api", RequestedBy: "test", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities:       []CapabilityStateRecord{{Capability: "processing", Enabled: true, ConfigurationIdentity: "cfg", ConfigurationGeneration: 2, CompatibilityIdentity: "compat", PolicyIdentity: "policy-expand", PolicyGeneration: 2, HealthState: "ready", ReconciliationState: "queued"}},
		Policies:           []ProviderPolicyRecord{{PolicyIdentity: "policy-expand", Capability: "processing", DefaultDecision: "deny", AllowedContentTypes: []string{"text/plain"}, AllowedSourceTypes: []string{"manual"}, AllowedSensitivities: []string{"normal"}, CreatedAt: now}},
		Assignments:        []PolicyAssignmentRecord{{AssignmentID: "allow-user", PolicyIdentity: "policy-expand", Capability: "processing", SelectorKind: "space_type", SpaceType: "user", Effect: "allow"}},
		ReconciliationRuns: []ReconciliationRunRecord{{RunID: "sc004-run", Capability: "processing", ChangeClass: "policy_expand", FromConfigurationGeneration: 1, ToConfigurationGeneration: 2, FromPolicyGeneration: 1, ToPolicyGeneration: 2, TargetPolicyIdentity: "policy-expand"}},
	}
	if err := repo.CommitActivation(ctx, commit); err != nil {
		t.Fatal(err)
	}
	insertPolicySpace(t, repo, "root", "", "user", "root")
	const jobCount = 200
	for i := 1; i <= jobCount; i++ {
		id := fmt.Sprintf("sc004-job-%03d", i)
		eventID := fmt.Sprintf("sc004-event-%03d", i)
		contentID := fmt.Sprintf("sc004-content-%03d", i)
		if _, err := repo.store.writer.Exec(`INSERT INTO raw_contents(content_id,content_kind,content_text,media_type,content_hash,size_bytes,created_at) VALUES (?,'text','content','text/plain',?,7,?)`, contentID, contentID, formatTime(now)); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.store.writer.Exec(`INSERT INTO raw_events(event_id,operation_id,space_id,content_id,source_type,source_name,caller_id,occurred_at,received_at,created_at) VALUES (?,?,'root',?,'manual','test','test',?,?,?)`, eventID, "op-"+eventID, contentID, formatTime(now), formatTime(now), formatTime(now)); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,capability,configuration_generation,policy_generation,blocked_reason_code,created_at) VALUES (?,?,'extract_memory','raw_event',?,'v1','blocked_policy',3,?,'processing',1,1,'target_policy_denied',?)`, id, "op-"+id, eventID, formatTime(now), formatTime(now)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,capability,configuration_generation,policy_generation,result_code,created_at,finished_at) VALUES ('sc004-done','op-done','extract_memory','raw_event','sc004-done-event','v1','succeeded',3,?,'processing',1,1,'no_processor',?,?)`, formatTime(now), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.store.writer.Exec(`INSERT INTO raw_contents(content_id,content_kind,content_text,media_type,content_hash,size_bytes,created_at) VALUES ('sc004-done-content','text','done','text/plain','done',4,?)`, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.store.writer.Exec(`INSERT INTO raw_events(event_id,operation_id,space_id,content_id,source_type,source_name,caller_id,occurred_at,received_at,created_at,processing_status) VALUES ('sc004-done-event','op-done','root','sc004-done-content','manual','test','test',?,?,?,'stored')`, formatTime(now), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	run, err := repo.ClaimReconciliationRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for {
		batch, err := repo.Reconcile(ctx, run, 100)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Complete {
			break
		}
		run.Cursor = batch.NextCursor
	}
	elapsed := time.Since(start)

	if elapsed > 30*time.Second {
		t.Fatalf("reconciliation of %d jobs took %v; want < 30s", jobCount, elapsed)
	}
	var pendingCount int
	if err := repo.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE status='pending' AND reconciliation_run_id='sc004-run'`).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if float64(pendingCount) < float64(jobCount)*0.95 {
		t.Fatalf("only %d of %d eligible jobs scheduled (%.1f%%; want >= 95%%)", pendingCount, jobCount, float64(pendingCount)/float64(jobCount)*100)
	}
	var succeededCount int
	if err := repo.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_id='sc004-done' AND status='succeeded'`).Scan(&succeededCount); err != nil || succeededCount != 1 {
		t.Fatalf("completed job was duplicated: succeeded count = %d, err=%v", succeededCount, err)
	}
}

func TestEmbeddingReconciliationSupersedesBlockedPolicyEmbedJobs(t *testing.T) {
	ctx := context.Background()
	repo := newConfigTestRepository(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	repo.SetEmbedding(nil, "embedding-provider", "embedding-model", "compat-v1", "config-v1", 2, 2)

	commit := ActivationCommit{
		Revision:   RevisionRecord{RevisionID: "embed-narrow-rev", SourceIdentity: "src", EffectiveIdentity: "eff", ConfigVersion: 1, LoadedAt: now},
		Activation: ActivationRecord{ActivationID: "embed-narrow-act", RevisionID: "embed-narrow-rev", Trigger: "api", RequestedBy: "test", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities: []CapabilityStateRecord{
			{Capability: "embedding", Enabled: true, ConfigurationIdentity: "config-v1", ConfigurationGeneration: 2, CompatibilityIdentity: "compat-v1", PolicyIdentity: "policy-narrow", PolicyGeneration: 2, HealthState: "ready", ReconciliationState: "queued"},
		},
		Policies: []ProviderPolicyRecord{
			{PolicyIdentity: "policy-narrow", Capability: "embedding", DefaultDecision: "deny", AllowedContentTypes: []string{"text/plain"}, AllowedSourceTypes: []string{"manual"}, AllowedSensitivities: []string{"normal"}, QueryAllowed: true, CreatedAt: now},
		},
		Assignments: []PolicyAssignmentRecord{
			{AssignmentID: "allow-user", PolicyIdentity: "policy-narrow", Capability: "embedding", SelectorKind: "space_type", SpaceType: "user", Effect: "allow"},
		},
		ReconciliationRuns: []ReconciliationRunRecord{
			{RunID: "embed-narrow-run", Capability: "embedding", ChangeClass: "policy_narrow", FromConfigurationGeneration: 1, ToConfigurationGeneration: 2, FromPolicyGeneration: 1, ToPolicyGeneration: 2, TargetPolicyIdentity: "policy-narrow"},
		},
	}
	if err := repo.CommitActivation(ctx, commit); err != nil {
		t.Fatal(err)
	}

	insertPolicySpace(t, repo, "user-root", "", "user", "owner")
	insertPolicySpace(t, repo, "proj-1", "user-root", "project", "p1")

	nowText := formatTime(now)
	tx, err := repo.store.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO memories(memory_id, space_id, current_revision_id, current_state, created_at, updated_at) VALUES ('mem-blocked', 'proj-1', 'rev-blocked', 'active', ?, ?)`, nowText, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES ('rev-blocked','mem-blocked','fact','blocked content','blocked content','hash-blocked','normal',0,0,'test','v1',?)`, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO memory_projections(memory_id, revision_id, space_id, memory_type, current_state, searchable, display_content, content_hash, embedding_state, updated_at) VALUES ('mem-blocked', 'rev-blocked', 'proj-1', 'fact', 'active', 1, 'blocked content', 'hash-blocked', 'stale', ?)`, nowText); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,capability,configuration_generation,policy_generation,blocked_reason_code,created_at) VALUES ('blocked-embed-job','op-blocked-embed','embed_memory','memory_revision','rev-blocked',?,'blocked_policy',0,3,?,'embedding',1,1,'target_policy_denied',?)`, repo.embeddingIdentity(), nowText, nowText); err != nil {
		t.Fatal(err)
	}

	run, err := repo.ClaimReconciliationRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if run.RunID != "embed-narrow-run" {
		t.Fatalf("claimed run = %q; want embed-narrow-run", run.RunID)
	}
	batch, err := repo.Reconcile(ctx, run, 100)
	if err != nil || !batch.Complete {
		t.Fatalf("reconciliation = %+v, %v", batch, err)
	}
	if batch.Blocked != 1 {
		t.Fatalf("blocked count = %d; want 1", batch.Blocked)
	}

	var jobStatus, blockedReason string
	if err := repo.store.reader.QueryRow(`SELECT status, blocked_reason_code FROM jobs WHERE job_id='blocked-embed-job'`).Scan(&jobStatus, &blockedReason); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "superseded" || blockedReason != "policy_ineligible" {
		t.Fatalf("blocked embed job = status %q, reason %q; want superseded/policy_ineligible", jobStatus, blockedReason)
	}

	var eligibleCount int
	if err := repo.store.reader.QueryRow(`SELECT count(*) FROM embedding_eligibility WHERE memory_id='mem-blocked' AND revision_id='rev-blocked' AND eligible=0`).Scan(&eligibleCount); err != nil || eligibleCount != 1 {
		t.Fatalf("eligibility record for denied memory = %d, err=%v; want 1 ineligible", eligibleCount, err)
	}
}

func TestRedactedDocumentExcludesSecretsEndpointAndQueryInstructions(t *testing.T) {
	ctx := context.Background()
	repo := newConfigTestRepository(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	secretName := "DMEM_EMBEDDING_REDACT_TEST_SECRET"
	endpoint := "https://provider.invalid/v1/embeddings"
	queryInstructions := "query: embed this text"
	redactedJSON := []byte(`{"config_version":1,"source_identity":"sha256:src","effective_identity":"sha256:eff","data_dir":"/data","database_filename":"dmem.db","processing":{"enabled":false,"state":"disabled","configuration_generation":1,"policy_generation":1,"blocked_work_count":0,"reconciliation_state":"idle"},"embedding":{"enabled":true,"kind":"openai-compatible","model":"embedding-a","configuration_identity":"sha256:cfg","compatibility_identity":"sha256:compat","policy_identity":"sha256:pol","secret_available":true,"policy_content_types":["text/plain"],"policy_source_types":["manual"],"policy_sensitivities":["normal"],"policy_assignment_count":1,"state":"ready","configuration_generation":1,"policy_generation":1,"blocked_work_count":0,"reconciliation_state":"idle"}}`)
	commit := ActivationCommit{
		Revision: RevisionRecord{
			RevisionID:        "redact-rev",
			SourceIdentity:    "sha256:src",
			EffectiveIdentity: "sha256:eff",
			ConfigVersion:     1,
			LoadedAt:          now,
			RedactedDocument:  redactedJSON,
		},
		Activation: ActivationRecord{ActivationID: "redact-act", RevisionID: "redact-rev", Trigger: "startup", RequestedBy: "daemon", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities: []CapabilityStateRecord{
			{Capability: "embedding", Enabled: true, ConfigurationIdentity: "sha256:cfg", ConfigurationGeneration: 1, CompatibilityIdentity: "sha256:compat", PolicyIdentity: "sha256:pol", PolicyGeneration: 1, HealthState: "ready", ReconciliationState: "idle"},
		},
	}
	if err := repo.CommitActivation(ctx, commit); err != nil {
		t.Fatal(err)
	}

	var persistedRedacted string
	if err := repo.store.reader.QueryRow(`SELECT redacted_document FROM configuration_revisions WHERE revision_id='redact-rev'`).Scan(&persistedRedacted); err != nil {
		t.Fatal(err)
	}
	if persistedRedacted == "" {
		t.Fatal("redacted_document not persisted")
	}
	for _, forbidden := range []string{secretName, endpoint, queryInstructions, "query_instructions", `"endpoint"`} {
		if strings.Contains(persistedRedacted, forbidden) {
			t.Fatalf("redacted_document leaked %q: %s", forbidden, persistedRedacted)
		}
	}
}
