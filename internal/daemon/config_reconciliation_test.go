package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
	_ "github.com/mattn/go-sqlite3"
)

func TestManagedReloadPersistsReconciliationPolicyLineage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	initial := reloadConfig{dataDir: filepath.Join(root, "data"), runtimeDir: filepath.Join(root, "runtime")}
	writeReloadConfig(t, path, initial)
	t.Setenv("DMEM_RELOAD_PROCESSING_SECRET", "processing-secret")

	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())

	enabled := initial
	enabled.processing = true
	writeReloadConfig(t, path, enabled)
	result, err := d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("Reload() = %+v, %v", result, err)
	}

	run, err := d.repository.ReconciliationRun(context.Background(), result.AttemptID+"-processing-enable")
	if err != nil {
		t.Fatal(err)
	}
	if run.FromPolicyGeneration != 1 || run.ToPolicyGeneration != 2 || run.TargetPolicyIdentity == "" {
		t.Fatalf("reconciliation lineage = %+v", run)
	}
}

func ingestEvent(t *testing.T, client *http.Client, key, spaceID, text string) domain.IngestResponse {
	t.Helper()
	response := requestJSON(t, client, http.MethodPost, "/v1/events", key, map[string]any{
		"space_id": spaceID, "source_type": "manual", "source_name": "test",
		"occurred_at": "2026-08-21T00:00:00Z",
		"content":     map[string]any{"kind": "text", "media_type": "text/plain", "text": text},
	})
	var result domain.IngestResponse
	decodeResponse(t, response, &result)
	return result
}

func waitForJobCondition(t *testing.T, ctx context.Context, repo *storage.Repository, jobID string, check func(domain.Job) bool, desc string) domain.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err := repo.GetJob(ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if check(job) {
			return job
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", jobID, desc)
	return domain.Job{}
}

func driveAllReconciliation(t *testing.T, ctx context.Context, repo *storage.Repository) {
	t.Helper()
	for {
		run, err := repo.ClaimReconciliationRun(ctx)
		if errors.Is(err, storage.ErrNotFound) {
			return
		}
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
	}
}

func openDaemonDB(t *testing.T, d *Daemon) *sql.DB {
	t.Helper()
	dbPath := d.manager.Snapshot().Config.DatabasePath
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPolicyCorrectionRecoversBlockedProcessing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	initial := reloadConfig{
		dataDir:                filepath.Join(root, "data"),
		runtimeDir:             filepath.Join(root, "runtime"),
		processing:             true,
		processingAllowProject: false,
	}
	writeReloadConfig(t, path, initial)
	t.Setenv("DMEM_RELOAD_PROCESSING_SECRET", "processing-secret")

	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(d.endpoint)

	userSpace := createSpace(t, client, "user-key", map[string]any{
		"space_type": "user", "external_id": "owner", "display_name": "Owner",
	})
	project := createSpace(t, client, "project-key", map[string]any{
		"parent_space_id": userSpace.SpaceID, "space_type": "project",
		"external_id": "p1", "display_name": "Project",
	})

	accepted := ingestEvent(t, client, "event-1", project.SpaceID, "blocked content")
	if len(accepted.JobIDs) == 0 {
		t.Fatal("ingest produced no jobs")
	}
	jobID := accepted.JobIDs[0]

	blocked := waitForJobCondition(t, context.Background(), d.repository, jobID,
		func(j domain.Job) bool { return j.Status == "blocked_policy" },
		"blocked_policy")
	if blocked.BlockedReasonCode == "" {
		t.Fatalf("blocked job missing reason code: %+v", blocked)
	}

	capsBefore, err := d.repository.CurrentCapabilityStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	procBefore := capsBefore["processing"]
	if procBefore.BlockedWorkCount != 1 {
		t.Fatalf("blocked work count = %d; want 1", procBefore.BlockedWorkCount)
	}

	expanded := initial
	expanded.processingAllowProject = true
	writeReloadConfig(t, path, expanded)
	result, err := d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("reload = %+v, %v", result, err)
	}

	driveAllReconciliation(t, context.Background(), d.repository)

	recovered := waitForJobCondition(t, context.Background(), d.repository, jobID,
		func(j domain.Job) bool { return j.Status != "blocked_policy" },
		"not blocked_policy")
	if recovered.ReconciliationRunID == "" {
		t.Fatalf("job was not recovered by reconciliation: %+v", recovered)
	}

	capsAfter, err := d.repository.CurrentCapabilityStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	procAfter := capsAfter["processing"]
	if procAfter.ConfigurationIdentity != procBefore.ConfigurationIdentity {
		t.Fatalf("processing configuration identity changed: %q -> %q",
			procBefore.ConfigurationIdentity, procAfter.ConfigurationIdentity)
	}
	if procAfter.CompatibilityIdentity != procBefore.CompatibilityIdentity {
		t.Fatalf("processing compatibility identity changed: %q -> %q",
			procBefore.CompatibilityIdentity, procAfter.CompatibilityIdentity)
	}
	if procAfter.PolicyIdentity == procBefore.PolicyIdentity {
		t.Fatal("processing policy identity should have changed after policy expansion")
	}
	if procAfter.BlockedWorkCount != 0 {
		t.Fatalf("blocked work count after recovery = %d; want 0", procAfter.BlockedWorkCount)
	}
}

func TestPolicyExpansionDoesNotDuplicateCompletedWork(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	initial := reloadConfig{
		dataDir:                filepath.Join(root, "data"),
		runtimeDir:             filepath.Join(root, "runtime"),
		processing:             true,
		processingAllowProject: false,
	}
	writeReloadConfig(t, path, initial)
	t.Setenv("DMEM_RELOAD_PROCESSING_SECRET", "processing-secret")

	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(d.endpoint)

	userSpace := createSpace(t, client, "user-key", map[string]any{
		"space_type": "user", "external_id": "owner", "display_name": "Owner",
	})
	project := createSpace(t, client, "project-key", map[string]any{
		"parent_space_id": userSpace.SpaceID, "space_type": "project",
		"external_id": "p1", "display_name": "Project",
	})

	accepted := ingestEvent(t, client, "blocked-event", project.SpaceID, "blocked content")
	blockedJobID := accepted.JobIDs[0]
	waitForJobCondition(t, context.Background(), d.repository, blockedJobID,
		func(j domain.Job) bool { return j.Status == "blocked_policy" },
		"blocked_policy")

	db := openDaemonDB(t, d)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO raw_contents(content_id,content_kind,content_text,media_type,content_hash,size_bytes,created_at) VALUES ('done-content','text','done','text/plain','done-hash',4,?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO raw_events(event_id,operation_id,space_id,content_id,source_type,source_name,caller_id,occurred_at,received_at,created_at,processing_status) VALUES ('done-event','done-op',?,'done-content','manual','test','test',?,?,?,'stored')`, project.SpaceID, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,capability,configuration_generation,policy_generation,result_code,created_at,finished_at) VALUES ('done-job','done-op','extract_memory','raw_event','done-event','processor-v1','succeeded',3,?,'processing',1,1,'no_processor',?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}

	var succeededCount int
	if err := db.QueryRow(`SELECT count(*) FROM jobs WHERE job_id='done-job' AND status='succeeded'`).Scan(&succeededCount); err != nil || succeededCount != 1 {
		t.Fatalf("precondition: succeeded job count = %d, err=%v", succeededCount, err)
	}

	expanded := initial
	expanded.processingAllowProject = true
	writeReloadConfig(t, path, expanded)
	result, err := d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("reload = %+v, %v", result, err)
	}

	driveAllReconciliation(t, context.Background(), d.repository)

	waitForJobCondition(t, context.Background(), d.repository, blockedJobID,
		func(j domain.Job) bool { return j.Status != "blocked_policy" },
		"not blocked_policy")

	if err := db.QueryRow(`SELECT count(*) FROM jobs WHERE job_id='done-job' AND status='succeeded'`).Scan(&succeededCount); err != nil || succeededCount != 1 {
		t.Fatalf("completed job was duplicated or modified: succeeded count = %d, err=%v", succeededCount, err)
	}
	var duplicateSucceeded int
	if err := db.QueryRow(`SELECT count(*) FROM jobs WHERE target_id='done-event' AND job_type='extract_memory'`).Scan(&duplicateSucceeded); err != nil || duplicateSucceeded != 1 {
		t.Fatalf("duplicate extract_memory jobs for done-event: count = %d, err=%v", duplicateSucceeded, err)
	}
}

func TestReconciliationResumesAfterDaemonRestart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	denied := reloadConfig{
		dataDir:                filepath.Join(root, "data"),
		runtimeDir:             filepath.Join(root, "runtime"),
		processing:             true,
		embedding:              true,
		processingAllowProject: false,
	}
	writeReloadConfig(t, path, denied)
	t.Setenv("DMEM_RELOAD_PROCESSING_SECRET", "processing-secret")
	t.Setenv("DMEM_RELOAD_EMBEDDING_SECRET", "embedding-secret")

	d1, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	client := unixClient(d1.endpoint)

	userSpace := createSpace(t, client, "user-key", map[string]any{
		"space_type": "user", "external_id": "owner", "display_name": "Owner",
	})
	project := createSpace(t, client, "project-key", map[string]any{
		"parent_space_id": userSpace.SpaceID, "space_type": "project",
		"external_id": "p1", "display_name": "Project",
	})

	var blockedJobIDs []string
	for i := 0; i < 2; i++ {
		accepted := ingestEvent(t, client, fmt.Sprintf("event-%d", i), project.SpaceID, fmt.Sprintf("content-%d", i))
		jobID := accepted.JobIDs[0]
		waitForJobCondition(t, context.Background(), d1.repository, jobID,
			func(j domain.Job) bool { return j.Status == "blocked_policy" },
			"blocked_policy")
		blockedJobIDs = append(blockedJobIDs, jobID)
	}

	expanded := denied
	expanded.processingAllowProject = true
	writeReloadConfig(t, path, expanded)
	result, err := d1.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("reload = %+v, %v", result, err)
	}

	if err := d1.jobs.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	run, err := d1.repository.ClaimReconciliationRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.ChangeClass == "" {
		t.Fatalf("claimed run has no change class: %+v", run)
	}
	batch, err := d1.repository.Reconcile(context.Background(), run, 1)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Complete {
		t.Fatal("partial batch completed unexpectedly; need 2+ blocked jobs for restart test")
	}
	if run.Cursor == batch.NextCursor {
		t.Fatal("cursor did not advance after partial batch")
	}

	identityBefore := d1.manager.Snapshot().Config.EffectiveIdentity
	if err := d1.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(root, "data", "dmem.db")
	rawDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var remainingBlocked int
	if err := rawDB.QueryRow(`SELECT count(*) FROM jobs WHERE status='blocked_policy' AND job_type='extract_memory'`).Scan(&remainingBlocked); err != nil {
		t.Fatal(err)
	}
	var runningRuns int
	if err := rawDB.QueryRow(`SELECT count(*) FROM reconciliation_runs WHERE state='running'`).Scan(&runningRuns); err != nil {
		t.Fatal(err)
	}
	var embedCapCount int
	if err := rawDB.QueryRow(`SELECT count(*) FROM capability_states WHERE capability='embedding' AND enabled=1`).Scan(&embedCapCount); err != nil {
		t.Fatal(err)
	}
	rawDB.Close()
	if remainingBlocked == 0 {
		t.Fatal("no blocked work persisted across restart")
	}
	if runningRuns == 0 {
		t.Fatal("no running reconciliation run persisted across restart")
	}
	if embedCapCount == 0 {
		t.Fatal("embedding capability state not persisted across restart")
	}

	d2, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close(context.Background())

	identityAfter := d2.manager.Snapshot().Config.EffectiveIdentity
	if identityAfter != identityBefore {
		t.Fatalf("effective identity not restored: %q -> %q", identityBefore, identityAfter)
	}

	driveAllReconciliation(t, context.Background(), d2.repository)

	for _, jobID := range blockedJobIDs {
		recovered := waitForJobCondition(t, context.Background(), d2.repository, jobID,
			func(j domain.Job) bool { return j.Status != "blocked_policy" },
			"not blocked_policy")
		if recovered.ReconciliationRunID == "" {
			t.Fatalf("job %s was not recovered by reconciliation after restart: %+v", jobID, recovered)
		}
	}

	capsAfter, err := d2.repository.CurrentCapabilityStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capsAfter["processing"].BlockedWorkCount != 0 {
		t.Fatalf("blocked work count after restart recovery = %d; want 0", capsAfter["processing"].BlockedWorkCount)
	}
	semanticStatus, err := d2.repository.SemanticStatus(context.Background())
	if err != nil {
		t.Fatalf("semantic status after restart: %v", err)
	}
	if semanticStatus.Provider == "" {
		t.Fatal("semantic provider not recovered after restart")
	}
}

func TestStartupEmbeddingEnablementRecoversExistingMemories(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	disabled := reloadConfig{
		dataDir:    filepath.Join(root, "data"),
		runtimeDir: filepath.Join(root, "runtime"),
	}
	writeReloadConfig(t, path, disabled)
	t.Setenv("DMEM_RELOAD_EMBEDDING_SECRET", "embedding-secret")

	d1, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	client := unixClient(d1.endpoint)

	userSpace := createSpace(t, client, "user-key", map[string]any{
		"space_type": "user", "external_id": "owner", "display_name": "Owner",
	})
	project := createSpace(t, client, "project-key", map[string]any{
		"parent_space_id": userSpace.SpaceID, "space_type": "project",
		"external_id": "p1", "display_name": "Project",
	})

	event := ingestEvent(t, client, "startup-event", project.SpaceID, "startup content")
	memory, _ := createMemory(t, client, "startup-memory", memoryCreateBody(project.SpaceID, event.EventID, "recovered at startup"))
	if err := d1.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	enabled := disabled
	enabled.embedding = true
	enabled.embeddingAllowSpaceType = "project"
	writeReloadConfig(t, path, enabled)

	d2, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close(context.Background())

	deadline := time.Now().Add(10 * time.Second)
	var eligible int
	for time.Now().Before(deadline) {
		db := openDaemonDB(t, d2)
		_ = db.QueryRow(`SELECT count(*) FROM embedding_eligibility WHERE memory_id=? AND eligible=1`, memory.MemoryID).Scan(&eligible)
		db.Close()
		if eligible > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if eligible == 0 {
		t.Fatal("memory was not marked eligible after embedding enablement at startup")
	}
}

func TestPolicyExpansionRecoversEmbeddingEligibility(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	denied := reloadConfig{
		dataDir:                 filepath.Join(root, "data"),
		runtimeDir:              filepath.Join(root, "runtime"),
		processing:              true,
		processingAllowProject:  true,
		embedding:               true,
		embeddingAllowSpaceType: "user",
	}
	writeReloadConfig(t, path, denied)
	t.Setenv("DMEM_RELOAD_PROCESSING_SECRET", "processing-secret")
	t.Setenv("DMEM_RELOAD_EMBEDDING_SECRET", "embedding-secret")

	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(d.endpoint)

	userSpace := createSpace(t, client, "user-key", map[string]any{
		"space_type": "user", "external_id": "owner", "display_name": "Owner",
	})
	project := createSpace(t, client, "project-key", map[string]any{
		"parent_space_id": userSpace.SpaceID, "space_type": "project",
		"external_id": "p1", "display_name": "Project",
	})

	event := ingestEvent(t, client, "embed-event", project.SpaceID, "event content")
	memory, _ := createMemory(t, client, "embed-memory", memoryCreateBody(project.SpaceID, event.EventID, "eligible after expansion"))

	time.Sleep(500 * time.Millisecond)

	expanded := denied
	expanded.embeddingAllowSpaceType = "project"
	writeReloadConfig(t, path, expanded)
	result, err := d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("reload = %+v, %v", result, err)
	}

	driveAllReconciliation(t, context.Background(), d.repository)

	deadline := time.Now().Add(10 * time.Second)
	var eligible int
	for time.Now().Before(deadline) {
		db := openDaemonDB(t, d)
		_ = db.QueryRow(`SELECT count(*) FROM embedding_eligibility WHERE memory_id=? AND eligible=1`, memory.MemoryID).Scan(&eligible)
		db.Close()
		if eligible > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if eligible == 0 {
		t.Fatal("memory was not marked eligible after embedding policy expansion")
	}

	db := openDaemonDB(t, d)
	var pendingEmbed int
	if err := db.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='embed_memory' AND target_id=? AND status='pending'`, memory.CurrentRevision.RevisionID).Scan(&pendingEmbed); err != nil || pendingEmbed == 0 {
		t.Fatalf("no pending embed_memory job for recovered memory: count=%d, err=%v", pendingEmbed, err)
	}
}
