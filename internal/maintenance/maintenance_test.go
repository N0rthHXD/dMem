package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/instance"
	"github.com/N0rthHXD/dMem/internal/jobs"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/storage"
	"github.com/N0rthHXD/dMem/internal/vector"
	_ "github.com/mattn/go-sqlite3"
)

func TestBackupRestoreDrill(t *testing.T) {
	ctx := context.Background()
	root := secureTempDir(t)
	backupParent := secureSubdir(t, root, "backups")
	dataDirectory := secureSubdir(t, root, "data")
	databasePath := filepath.Join(dataDirectory, "dmem.db")
	store, err := storage.Open(ctx, databasePath, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	seedDrillData(t, databasePath)

	backupDirectory := filepath.Join(backupParent, "backup-1")
	created := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	manifest, err := Backup(ctx, store, backupDirectory, BackupOptions{
		DaemonVersion: "test-v1",
		Now:           func() time.Time { return created },
		ID:            func() (string, error) { return "backup-id", nil },
	})
	if err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	if manifest.DatabaseChecksum == "" || manifest.SchemaVersion != storage.SchemaVersion || manifest.UnfinishedJobSummary["running"] != 1 || manifest.FTSIndexVersion != 1 || manifest.SemanticIndexVersion != 7 {
		t.Fatalf("manifest = %#v", manifest)
	}
	assertBackupModes(t, backupDirectory)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dataDirectory, "old-marker")
	if err := os.WriteFile(marker, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var validationErr error
	var probeErr error
	result, err := Restore(ctx, backupDirectory, dataDirectory, RestoreOptions{Now: func() time.Time { return created.Add(time.Hour) }, probe: func(ctx context.Context, staging, databaseFilename string) error {
		probeErr = probeStagingDaemon(ctx, staging, databaseFilename)
		return probeErr
	}, ValidationHook: func(ctx context.Context, staging, databaseFilename string) error {
		validationErr = validateSemanticRestore(ctx, staging, databaseFilename)
		return validationErr
	}})
	if err != nil {
		t.Fatalf("Restore() error = %v; probe = %v; validation = %v", err, probeErr, validationErr)
	}
	if result.NormalizedJobs != 1 || !result.FTSRebuilt {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old data marker survived replacement: %v", err)
	}

	restored, err := sql.Open("sqlite3", filepath.Join(dataDirectory, "dmem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var status, owner, until string
	var generation int
	if err := restored.QueryRow(`SELECT status, coalesce(lease_owner,''), coalesce(lease_until,''), lease_generation FROM jobs WHERE job_id='job-running'`).Scan(&status, &owner, &until, &generation); err != nil {
		t.Fatal(err)
	}
	validFencedState := status == "retrying" && generation == 8 || status == "succeeded" && generation == 9
	if !validFencedState || owner != "" || until != "" {
		t.Fatalf("restored job = status %q owner %q until %q generation %d", status, owner, until, generation)
	}
	var outcome string
	if err := restored.QueryRow(`SELECT outcome FROM job_attempts WHERE attempt_id='attempt-running'`).Scan(&outcome); err != nil || outcome != "lease_lost" {
		t.Fatalf("attempt outcome = %q, %v", outcome, err)
	}
	var indexed int
	if err := restored.QueryRow(`SELECT count(*) FROM memory_fts WHERE memory_id='memory-1' AND content='restorable content'`).Scan(&indexed); err != nil || indexed != 1 {
		t.Fatalf("FTS count = %d, %v", indexed, err)
	}
	var ftsVersion int
	expectedFTSVersion := manifest.FTSIndexVersion + 1
	if status == "succeeded" {
		expectedFTSVersion++
	}
	if err := restored.QueryRow(`SELECT index_version FROM index_metadata WHERE index_name='memory_fts'`).Scan(&ftsVersion); err != nil || ftsVersion != expectedFTSVersion {
		t.Fatalf("restored FTS version = %d, %v", ftsVersion, err)
	}
	var semanticState, embeddingState string
	var activeVersion sql.NullInt64
	if err := restored.QueryRow(`SELECT state, active_version FROM semantic_index_metadata WHERE singleton=1`).Scan(&semanticState, &activeVersion); err != nil || semanticState != "ready" || !activeVersion.Valid {
		t.Fatalf("semantic metadata = state %q active %v, %v", semanticState, activeVersion, err)
	}
	if err := restored.QueryRow(`SELECT embedding_state FROM memory_projections WHERE memory_id='memory-1'`).Scan(&embeddingState); err != nil || embeddingState != "current" {
		t.Fatalf("restored embedding state = %q, %v", embeddingState, err)
	}
	var embeddingJobs, rebuildJobs int
	if err := restored.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='embed_memory' AND target_id='revision-1' AND processing_version='restore-provider/restore-model' AND status='succeeded'`).Scan(&embeddingJobs); err != nil || embeddingJobs != 1 {
		t.Fatalf("restored embedding jobs = %d, %v", embeddingJobs, err)
	}
	if err := restored.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='rebuild_semantic' AND target_id='memory_semantic' AND processing_version='restore-provider/restore-model' AND status='succeeded'`).Scan(&rebuildJobs); err != nil || rebuildJobs != 1 {
		t.Fatalf("restored semantic rebuild jobs = %d, %v", rebuildJobs, err)
	}
	var probeRows int
	if err := restored.QueryRow(`SELECT count(*) FROM memories WHERE memory_id IN ('00000000-0000-4000-8000-000000000064','00000000-0000-4000-8000-000000000065')`).Scan(&probeRows); err != nil || probeRows != 0 {
		t.Fatalf("staging probe rows published = %d, %v", probeRows, err)
	}
	if _, err := os.Stat(filepath.Join(dataDirectory, "semantic-index", "active.json")); err != nil {
		t.Fatalf("restored semantic index missing: %v", err)
	}
	assertSQLiteChecks(t, restored)
}

type restoreEmbeddingProvider struct{}

func (restoreEmbeddingProvider) Embed(_ context.Context, request provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	vectors := make([][]float32, len(request.Inputs()))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return provider.NewEmbeddingResult(vectors, "restore-drill"), nil
}

func validateSemanticRestore(ctx context.Context, staging, databaseFilename string) error {
	store, err := storage.Open(ctx, filepath.Join(staging, databaseFilename), storage.Options{ReadConnections: 1})
	if err != nil {
		return err
	}
	defer store.Close()
	index, err := vector.Open(filepath.Join(staging, "semantic-index"))
	if err != nil {
		return err
	}
	counter := 0
	repository := storage.NewRepository(store, domain.Runtime{Now: time.Now, ID: func() (string, error) {
		counter++
		return fmt.Sprintf("restore-validation-%d", counter), nil
	}}, 100, 200, 3, "restore-validation")
	repository.ConfigureSemantic(restoreEmbeddingProvider{}, index, "restore-provider", "restore-model")
	if err := repository.EnsureSemanticRecoveryJobs(ctx); err != nil {
		return err
	}
	cfg, err := config.Default()
	if err != nil {
		return err
	}
	cfg.WorkerCount, cfg.DiskSoftFree, cfg.DiskHardFree = 1, 0, 0
	service, err := jobs.NewService(repository, cfg, jobs.NoopProcessor{}, jobs.Dependencies{Indexes: repository})
	if err != nil {
		return err
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := service.Start(serviceCtx); err != nil {
		return err
	}
	defer service.Shutdown(context.Background())
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		state, _ := repository.SemanticHealth(ctx)
		if state == "ready" {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("semantic restore validation timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
	for _, mode := range []string{"semantic", "hybrid"} {
		result, err := repository.SearchMemories(ctx, storage.SearchParams{Query: "restored concept", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Mode: mode, Limit: 10, MaxChars: 1000})
		if err != nil || result.ExecutedMode != mode || result.Degraded || len(result.Results) != 1 || result.Results[0].Memory.MemoryID != "memory-1" || result.Results[0].Memory.CurrentRevision.Content != "restorable content" {
			return errors.New("semantic restore retrieval validation failed")
		}
	}
	return nil
}

func TestRestoreRejectsChecksumDamageAndPreservesOriginal(t *testing.T) {
	ctx := context.Background()
	root := secureTempDir(t)
	backupParent := secureSubdir(t, root, "backups")
	dataDirectory := secureSubdir(t, root, "data")
	databasePath := filepath.Join(dataDirectory, "dmem.db")
	store, err := storage.Open(ctx, databasePath, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	backupDirectory := filepath.Join(backupParent, "backup")
	if _, err := Backup(ctx, store, backupDirectory, BackupOptions{DaemonVersion: "test", ID: func() (string, error) { return "id", nil }}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	original := []byte("must remain")
	marker := filepath.Join(dataDirectory, "marker")
	if err := os.WriteFile(marker, original, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(backupDirectory, DatabaseName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("damage")); err != nil {
		t.Fatal(err)
	}
	file.Close()

	_, err = Restore(ctx, backupDirectory, dataDirectory, RestoreOptions{})
	if !errors.Is(err, ErrInvalidBackup) {
		t.Fatalf("Restore() error = %v; want ErrInvalidBackup", err)
	}
	got, readErr := os.ReadFile(marker)
	if readErr != nil || string(got) != string(original) {
		t.Fatalf("original marker = %q, %v", got, readErr)
	}
	if strings.Contains(err.Error(), root) || strings.Contains(strings.ToLower(err.Error()), "sqlite") {
		t.Fatalf("error leaks implementation detail: %v", err)
	}
}

func TestBackupFailureDoesNotPublishAndRejectsUnsafePaths(t *testing.T) {
	ctx := context.Background()
	root := secureTempDir(t)
	dataDirectory := secureSubdir(t, root, "data")
	store, err := storage.Open(ctx, filepath.Join(dataDirectory, "dmem.db"), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	target := filepath.Join(root, "unpublished")
	_, err = Backup(ctx, store, target, BackupOptions{DaemonVersion: "test", MinimumFree: ^uint64(0)})
	if !errors.Is(err, ErrInsufficientStorage) {
		t.Fatalf("Backup() error = %v", err)
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed backup was published: %v", statErr)
	}

	unsafeParent := filepath.Join(root, "unsafe")
	if err := os.Mkdir(unsafeParent, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = Backup(ctx, store, filepath.Join(unsafeParent, "backup"), BackupOptions{DaemonVersion: "test"})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("unsafe parent error = %v", err)
	}
	symlink := filepath.Join(root, "backup-link")
	if err := os.Symlink(dataDirectory, symlink); err != nil {
		t.Fatal(err)
	}
	_, err = Restore(ctx, symlink, dataDirectory, RestoreOptions{})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink restore error = %v", err)
	}
}

func TestRestoreRejectsSchemaMismatchAndPreservesOriginal(t *testing.T) {
	ctx := context.Background()
	root := secureTempDir(t)
	backupParent := secureSubdir(t, root, "backups")
	dataDirectory := secureSubdir(t, root, "data")
	store, err := storage.Open(ctx, filepath.Join(dataDirectory, "dmem.db"), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	backupDirectory := filepath.Join(backupParent, "backup")
	if _, err := Backup(ctx, store, backupDirectory, BackupOptions{DaemonVersion: "test", ID: func() (string, error) { return "id", nil }}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	marker := filepath.Join(dataDirectory, "marker")
	os.WriteFile(marker, []byte("original"), 0o600)

	var manifest Manifest
	if err := readJSON(filepath.Join(backupDirectory, ManifestName), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion++
	if err := os.Remove(filepath.Join(backupDirectory, ManifestName)); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(backupDirectory, ManifestName), manifest); err != nil {
		t.Fatal(err)
	}
	var checksums Checksums
	if err := readJSON(filepath.Join(backupDirectory, ChecksumsName), &checksums); err != nil {
		t.Fatal(err)
	}
	checksums.Files[ManifestName], _ = checksumFile(filepath.Join(backupDirectory, ManifestName))
	os.Remove(filepath.Join(backupDirectory, ChecksumsName))
	if err := writeJSON(filepath.Join(backupDirectory, ChecksumsName), checksums); err != nil {
		t.Fatal(err)
	}
	_, err = Restore(ctx, backupDirectory, dataDirectory, RestoreOptions{})
	if !errors.Is(err, ErrIncompatibleBackup) {
		t.Fatalf("Restore() error = %v", err)
	}
	if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "original" {
		t.Fatalf("original changed: %q, %v", got, readErr)
	}
}

func TestRestoreRejectsActiveDataDirectoryAndPreservesOriginal(t *testing.T) {
	ctx := context.Background()
	root := secureTempDir(t)
	backupParent := secureSubdir(t, root, "backups")
	dataDirectory := secureSubdir(t, root, "data")
	store, err := storage.Open(ctx, filepath.Join(dataDirectory, "dmem.db"), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	backupDirectory := filepath.Join(backupParent, "backup")
	if _, err := Backup(ctx, store, backupDirectory, BackupOptions{DaemonVersion: "test", ID: func() (string, error) { return "id", nil }}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	marker := filepath.Join(dataDirectory, "marker")
	if err := os.WriteFile(marker, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := instance.Acquire(filepath.Join(dataDirectory, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	_, err = Restore(ctx, backupDirectory, dataDirectory, RestoreOptions{})
	if !errors.Is(err, ErrOfflineRequired) {
		t.Fatalf("Restore() error = %v; want ErrOfflineRequired", err)
	}
	if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "original" {
		t.Fatalf("original changed: %q, %v", got, readErr)
	}
}

func TestRestoreRejectsFailedStagingProbeAndPreservesOriginal(t *testing.T) {
	ctx := context.Background()
	root := secureTempDir(t)
	backupParent := secureSubdir(t, root, "backups")
	dataDirectory := secureSubdir(t, root, "data")
	store, err := storage.Open(ctx, filepath.Join(dataDirectory, "dmem.db"), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	backupDirectory := filepath.Join(backupParent, "backup")
	if _, err := Backup(ctx, store, backupDirectory, BackupOptions{DaemonVersion: "test", ID: func() (string, error) { return "id", nil }}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dataDirectory, "marker")
	if err := os.WriteFile(marker, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	probeCalled := false
	_, err = Restore(ctx, backupDirectory, dataDirectory, RestoreOptions{probe: func(context.Context, string, string) error {
		probeCalled = true
		return errors.New("isolation failed")
	}})
	if !errors.Is(err, ErrRestoreFailed) || !probeCalled {
		t.Fatalf("Restore() error/probe = %v/%v", err, probeCalled)
	}
	if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "original" {
		t.Fatalf("original changed: %q, %v", got, readErr)
	}
}

func TestP3DATA001ManifestInventoriesNewAuthority(t *testing.T) {
	ctx := context.Background()
	root := secureTempDir(t)
	backupParent := secureSubdir(t, root, "backups")
	dataDirectory := secureSubdir(t, root, "data")
	store, err := storage.Open(ctx, filepath.Join(dataDirectory, "dmem.db"), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedPhase3RecoveryData(t, filepath.Join(dataDirectory, "dmem.db"))
	manifest, err := Backup(ctx, store, filepath.Join(backupParent, "p3"), BackupOptions{DaemonVersion: "p3-test", ID: func() (string, error) { return "p3", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.AuthorityCounts) != len(phase3AuthorityTables) || manifest.AuthorityCounts["entities"] != 1 || manifest.AuthorityCounts["entity_mentions"] != 1 || manifest.AuthorityCounts["memory_relations"] != 1 || manifest.AuthorityCounts["feedback_events"] != 1 || manifest.AuthorityCounts["import_sources"] != 1 || manifest.RunningImportCount != 1 {
		t.Fatalf("P3-DATA-001 manifest inventory = %+v running imports=%d", manifest.AuthorityCounts, manifest.RunningImportCount)
	}
}

func TestP3DATA002RestoreFencesNewRunningWork(t *testing.T) {
	db, path := phase3RecoveryDatabase(t)
	normalizedJobs, normalizedImports, err := validateAndPrepare(context.Background(), db, time.Date(2026, 8, 25, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if normalizedJobs != 1 || normalizedImports != 1 {
		t.Fatalf("P3-DATA-002 normalized jobs/imports = %d/%d", normalizedJobs, normalizedImports)
	}
	var jobState, outcome, importState string
	var jobGeneration, importGeneration, committed, checkpoint int
	if err := db.QueryRow(`SELECT status,lease_generation FROM jobs WHERE job_id='p3-job'`).Scan(&jobState, &jobGeneration); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT outcome FROM job_attempts WHERE attempt_id='p3-attempt'`).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state,generation,committed_objects,(SELECT coalesce(max(committed_objects),0) FROM import_checkpoints WHERE import_operation_id='p3-import') FROM import_operations WHERE import_operation_id='p3-import'`).Scan(&importState, &importGeneration, &committed, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if jobState != "retrying" || jobGeneration != 5 || outcome != "lease_lost" || importState != "waiting" || importGeneration != 4 || committed != checkpoint {
		t.Fatalf("P3-DATA-002 fenced state = %s/%d %s %s/%d checkpoint=%d/%d (%s)", jobState, jobGeneration, outcome, importState, importGeneration, committed, checkpoint, path)
	}
}

func TestP3DATA003RestoreRebuildsDerivedHealthFromAuthority(t *testing.T) {
	db, _ := phase3RecoveryDatabase(t)
	if _, _, err := validateAndPrepare(context.Background(), db, time.Date(2026, 8, 25, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var entityState, relationState, feedbackState, semanticState string
	var entities, mentions, relations, feedback, fts int
	if err := db.QueryRow(`SELECT e.state,e.source_entity_count,e.source_mention_count,r.state,r.source_relation_count,f.state,(SELECT count(*) FROM feedback_aggregates),(SELECT state FROM semantic_index_metadata WHERE singleton=1),(SELECT count(*) FROM memory_fts) FROM entity_index_metadata e CROSS JOIN relation_index_metadata r CROSS JOIN feedback_aggregate_metadata f WHERE e.singleton=1 AND r.singleton=1 AND f.singleton=1`).Scan(&entityState, &entities, &mentions, &relationState, &relations, &feedbackState, &feedback, &semanticState, &fts); err != nil {
		t.Fatal(err)
	}
	if entityState != "ready" || entities != 1 || mentions != 1 || relationState != "ready" || relations != 1 || feedbackState != "ready" || feedback != 1 || semanticState != "absent" || fts != 2 {
		t.Fatalf("P3-DATA-003 health/counts = %s %d/%d, %s %d, %s %d, %s, fts=%d", entityState, entities, mentions, relationState, relations, feedbackState, feedback, semanticState, fts)
	}
}

func phase3RecoveryDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p3-recovery.db")
	store, err := storage.Open(context.Background(), path, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	seedPhase3RecoveryData(t, path)
	db, err := openMaintenanceDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func seedPhase3RecoveryData(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`INSERT INTO memory_spaces(space_id,space_type,external_id,display_name,created_at) VALUES ('p3-space','user','p3','P3','2026-08-25T10:00:00Z')`,
		`INSERT INTO raw_contents(content_id,content_kind,content_text,media_type,content_hash,size_bytes,created_at) VALUES ('p3-content','text','Alice source','text/plain','p3-content-hash',12,'2026-08-25T10:00:00Z')`,
		`INSERT INTO raw_events(event_id,operation_id,space_id,content_id,source_type,source_name,caller_id,occurred_at,received_at,created_at) VALUES ('p3-event','p3-op','p3-space','p3-content','manual','test','caller','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO memories(memory_id,space_id,memory_type,current_revision_id,current_state,created_at,updated_at) VALUES ('p3-memory-a','p3-space','fact','p3-revision-a','active','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_revisions(revision_id,memory_id,content,content_hash,created_by,processing_version,created_at) VALUES ('p3-revision-a','p3-memory-a','Alice owns project','p3-a','test','p3-v1','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_projections(memory_id,revision_id,space_id,memory_type,current_state,searchable,display_content,content_hash,updated_at) VALUES ('p3-memory-a','p3-revision-a','p3-space','fact','active',1,'Alice owns project','p3-a','2026-08-25T10:00:00Z')`,
		`INSERT INTO memories(memory_id,space_id,memory_type,current_revision_id,current_state,created_at,updated_at) VALUES ('p3-memory-b','p3-space','fact','p3-revision-b','active','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_revisions(revision_id,memory_id,content,content_hash,created_by,processing_version,created_at) VALUES ('p3-revision-b','p3-memory-b','Project deadline Friday','p3-b','test','p3-v1','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_projections(memory_id,revision_id,space_id,memory_type,current_state,searchable,display_content,content_hash,updated_at) VALUES ('p3-memory-b','p3-revision-b','p3-space','fact','active',1,'Project deadline Friday','p3-b','2026-08-25T10:00:00Z')`,
		`INSERT INTO entities(entity_id,space_id,entity_type,canonical_name,normalized_name,normalization_version,state,created_by,processing_version,created_at,updated_at) VALUES ('p3-entity','p3-space','person','Alice','alice','v1','active','test','p3-v1','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO entity_aliases(alias_id,entity_id,alias,normalized_alias,normalization_version,source,state,created_by,created_at,updated_at) VALUES ('p3-alias','p3-entity','Alice','alice','v1','human_confirmation','active','test','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO entity_mentions(mention_id,entity_id,memory_id,revision_id,start_offset,end_offset,mention_type,source,surface_text,created_by,processing_version,created_at) VALUES ('p3-mention','p3-entity','p3-memory-a','p3-revision-a',0,5,'explicit','human_confirmation','Alice','test','p3-v1','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_relations(relation_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,source,specification_hash,created_by,processing_version,created_at,updated_at) VALUES ('p3-relation','p3-space','supports','p3-memory-a','p3-revision-a','p3-memory-b','p3-revision-b','human_confirmation','p3-spec','test','p3-v1','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO feedback_events(feedback_id,operation_id,caller_id,memory_id,revision_id,feedback_type,reason_code,created_at) VALUES ('p3-feedback','p3-feedback-op','caller','p3-memory-a','p3-revision-a','useful','answered_query','2026-08-25T11:00:00Z')`,
		`INSERT INTO import_sources(source_id,source_identity,format_family,format_version,first_export_id,created_at) VALUES ('p3-source','source:p3','dmem-logical',1,'p3-export','2026-08-25T10:00:00Z')`,
		`INSERT INTO import_operations(import_operation_id,operation_id,source_id,export_id,target_space_id,package_hash,plan_hash,state,generation,total_objects,committed_objects,created_by,created_at,started_at,updated_at) VALUES ('p3-import','p3-import-op','p3-source','p3-export','p3-space','package','plan','running',3,0,0,'test','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,attempt_count,max_attempts,next_run_at,lease_owner,lease_until,lease_generation,created_at) VALUES ('p3-job','p3-job-op','extract_entity','raw_event','p3-event','p3-v1','running',1,3,'2026-08-25T10:00:00Z','worker','2099-01-01T00:00:00Z',4,'2026-08-25T10:00:00Z')`,
		`INSERT INTO job_attempts(attempt_id,job_id,lease_generation,worker_id,started_at) VALUES ('p3-attempt','p3-job',4,'worker','2026-08-25T10:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed Phase 3 recovery: %v", err)
		}
	}
}

func seedDrillData(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []string{
		`INSERT INTO memory_spaces(space_id, space_type, external_id, display_name, created_at) VALUES ('space-1','user','root','Root','2026-01-01T00:00:00Z')`,
		`INSERT INTO memories(memory_id,space_id,memory_type,current_revision_id,current_state,created_at,updated_at) VALUES ('memory-1','space-1','fact','revision-1','active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO memory_revisions(revision_id,memory_id,content,content_hash,created_by,processing_version,created_at) VALUES ('revision-1','memory-1','restorable content','hash','test','process-v1','2026-01-01T00:00:00Z')`,
		`INSERT INTO memory_projections(memory_id,revision_id,space_id,memory_type,current_state,searchable,display_content,content_hash,updated_at) VALUES ('memory-1','revision-1','space-1','fact','active',1,'restorable content','hash','2026-01-01T00:00:00Z')`,
		`UPDATE memory_projections SET embedding_state='current', embedding_provider='provider', embedding_model='model', embedding_dimension=2, embedding_content_hash='hash' WHERE memory_id='memory-1'`,
		`UPDATE semantic_index_metadata SET active_version=7, state='ready', provider_identity='provider', model_identity='model', dimension=2, source_revision_count=1 WHERE singleton=1`,
		`INSERT INTO memory_fts(memory_id,revision_id,content) VALUES ('stale','stale','stale')`,
		`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,attempt_count,max_attempts,next_run_at,lease_owner,lease_until,lease_generation,created_at) VALUES ('job-running','operation-1','rebuild_fts','index','memory_fts','process-v1','running',1,3,'2026-01-01T00:00:00Z','worker-1','2099-01-01T00:00:00Z',7,'2026-01-01T00:00:00Z')`,
		`INSERT INTO job_attempts(attempt_id,job_id,lease_generation,worker_id,started_at) VALUES ('attempt-running','job-running',7,'worker-1','2026-01-01T00:00:00Z')`,
		`INSERT INTO idempotency_records(idempotency_id,caller_id,http_method,normalized_route,idempotency_key,request_hash,operation_id,response_status,response_body,created_at) VALUES ('idem-1','caller','POST','/v1/test','key','hash','operation-1',201,'{"id":"same"}','2026-01-01T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("seed statement failed: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertBackupModes(t *testing.T, directory string) {
	t.Helper()
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("backup directory mode = %v, %v", info.Mode().Perm(), err)
	}
	for _, name := range []string{ManifestName, DatabaseName, ChecksumsName} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v", name, info.Mode().Perm(), err)
		}
	}
}

func assertSQLiteChecks(t *testing.T, db *sql.DB) {
	t.Helper()
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity = %q, %v", integrity, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after restore")
	}
}

func secureTempDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func secureSubdir(t *testing.T, parent, name string) string {
	t.Helper()
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
