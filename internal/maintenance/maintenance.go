package maintenance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/daemon"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/instance"
	"github.com/N0rthHXD/dMem/internal/storage"
	_ "github.com/mattn/go-sqlite3"
)

const (
	FormatVersion = 1
	ManifestName  = "manifest.json"
	DatabaseName  = "database.sqlite"
	ChecksumsName = "checksums.json"
)

var (
	ErrUnsafePath          = errors.New("unsafe maintenance path")
	ErrInvalidBackup       = errors.New("invalid backup")
	ErrIncompatibleBackup  = errors.New("incompatible backup")
	ErrBackupFailed        = errors.New("backup failed")
	ErrRestoreFailed       = errors.New("restore failed")
	ErrOfflineRequired     = errors.New("offline maintenance required")
	ErrInsufficientStorage = errors.New("insufficient storage")
)

type Manifest struct {
	FormatVersion         int            `json:"format_version"`
	BackupID              string         `json:"backup_id"`
	CreatedAt             string         `json:"created_at"`
	DaemonVersion         string         `json:"daemon_version"`
	SchemaVersion         int            `json:"schema_version"`
	DatabaseChecksum      string         `json:"database_checksum"`
	SQLiteIntegrityResult string         `json:"sqlite_integrity_result"`
	UnfinishedJobSummary  map[string]int `json:"unfinished_job_summary"`
	ProcessingVersions    []string       `json:"processing_versions"`
	FTSIndexVersion       int            `json:"fts_index_version"`
	SemanticIndexVersion  int            `json:"semantic_index_version"`
	FTSRebuildRequired    bool           `json:"fts_rebuild_required"`
	SemanticRebuildNeeded bool           `json:"semantic_rebuild_required"`
	AuthorityCounts       map[string]int `json:"authority_counts"`
	RunningImportCount    int            `json:"running_import_count"`
}

type Checksums struct {
	Algorithm string            `json:"algorithm"`
	Files     map[string]string `json:"files"`
}

type BackupOptions struct {
	DaemonVersion string
	Now           func() time.Time
	ID            func() (string, error)
	MinimumFree   uint64
}

type RestoreOptions struct {
	DatabaseFilename string
	Now              func() time.Time
	MinimumFree      uint64
	ValidationHook   func(context.Context, string, string) error
	probe            func(context.Context, string, string) error
}

type RestoreResult struct {
	Manifest          Manifest
	NormalizedJobs    int
	NormalizedImports int
	FTSRebuilt        bool
}

var authorityTables = []string{
	"entities", "entity_events", "entity_aliases", "entity_alias_events", "entity_mentions",
	"entity_proposals", "entity_proposal_reviews", "entity_merge_proposals", "memory_relations",
	"relation_evidence", "relation_state_events", "relation_proposals", "relation_proposal_reviews",
	"evolution_proposals", "evolution_proposal_targets", "evolution_proposal_evidence",
	"evolution_proposal_reviews", "evolution_result_refs", "conflict_cases", "conflict_case_participants",
	"conflict_case_relations", "conflict_resolution_events", "conflict_resolution_result_refs",
	"feedback_events", "aggregate_descriptors", "aggregate_proposals", "aggregate_proposal_inputs",
	"aggregate_proposal_reviews", "export_operations", "import_sources", "import_operations",
	"import_space_mappings", "import_object_mappings", "imported_provenance", "import_checkpoints",
}

func Backup(ctx context.Context, store *storage.Store, target string, options BackupOptions) (Manifest, error) {
	if store == nil || options.DaemonVersion == "" {
		return Manifest{}, ErrBackupFailed
	}
	parent := filepath.Dir(filepath.Clean(target))
	if err := requireSecureDirectory(parent); err != nil {
		return Manifest{}, err
	}
	if _, err := os.Lstat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, ErrUnsafePath
	}
	if err := requireFreeSpace(parent, options.MinimumFree); err != nil {
		return Manifest{}, err
	}
	temporary, err := os.MkdirTemp(parent, ".dmem-backup-")
	if err != nil {
		return Manifest{}, ErrBackupFailed
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return Manifest{}, ErrBackupFailed
	}
	databasePath := filepath.Join(temporary, DatabaseName)
	if err := store.OnlineBackup(ctx, databasePath); err != nil {
		return Manifest{}, ErrBackupFailed
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		return Manifest{}, ErrBackupFailed
	}

	db, err := openMaintenanceDB(databasePath)
	if err != nil {
		return Manifest{}, ErrBackupFailed
	}
	manifest, err := inspectSnapshot(ctx, db)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		return Manifest{}, ErrBackupFailed
	}
	manifest.FormatVersion = FormatVersion
	manifest.DaemonVersion = options.DaemonVersion
	manifest.CreatedAt = now(options.Now).Format(time.RFC3339Nano)
	manifest.BackupID, err = backupID(options.ID)
	if err != nil {
		return Manifest{}, ErrBackupFailed
	}
	manifest.DatabaseChecksum, err = checksumFile(databasePath)
	if err != nil {
		return Manifest{}, ErrBackupFailed
	}
	if err := writeJSON(filepath.Join(temporary, ManifestName), manifest); err != nil {
		return Manifest{}, ErrBackupFailed
	}
	manifestChecksum, err := checksumFile(filepath.Join(temporary, ManifestName))
	if err != nil {
		return Manifest{}, ErrBackupFailed
	}
	checksums := Checksums{Algorithm: "SHA-256", Files: map[string]string{
		DatabaseName: manifest.DatabaseChecksum,
		ManifestName: manifestChecksum,
	}}
	if err := writeJSON(filepath.Join(temporary, ChecksumsName), checksums); err != nil {
		return Manifest{}, ErrBackupFailed
	}
	if err := syncTree(temporary); err != nil {
		return Manifest{}, ErrBackupFailed
	}
	if err := os.Rename(temporary, target); err != nil {
		return Manifest{}, ErrBackupFailed
	}
	if err := syncDirectory(parent); err != nil {
		_ = os.RemoveAll(target)
		_ = syncDirectory(parent)
		return Manifest{}, ErrBackupFailed
	}
	return manifest, nil
}

func Restore(ctx context.Context, backupDirectory, dataDirectory string, options RestoreOptions) (RestoreResult, error) {
	if options.DatabaseFilename == "" {
		options.DatabaseFilename = "dmem.db"
	}
	if filepath.Base(options.DatabaseFilename) != options.DatabaseFilename || options.DatabaseFilename == "." {
		return RestoreResult{}, ErrUnsafePath
	}
	if err := requireSecureDirectory(dataDirectory); err != nil {
		return RestoreResult{}, err
	}
	parent := filepath.Dir(filepath.Clean(dataDirectory))
	if err := requireSecureDirectory(parent); err != nil {
		return RestoreResult{}, err
	}
	lock, err := instance.Acquire(filepath.Join(dataDirectory, "daemon.lock"))
	if err != nil {
		if errors.Is(err, instance.ErrAlreadyRunning) {
			return RestoreResult{}, ErrOfflineRequired
		}
		return RestoreResult{}, ErrRestoreFailed
	}
	defer lock.Close()
	if err := requireSecureDirectory(backupDirectory); err != nil {
		return RestoreResult{}, err
	}
	if err := requireFreeSpace(parent, options.MinimumFree); err != nil {
		return RestoreResult{}, err
	}

	manifest, err := validateBackup(ctx, backupDirectory)
	if err != nil {
		return RestoreResult{}, err
	}
	staging, err := os.MkdirTemp(parent, ".dmem-restore-")
	if err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o700); err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	stagedDatabase := filepath.Join(staging, options.DatabaseFilename)
	if err := copyRegularFile(filepath.Join(backupDirectory, DatabaseName), stagedDatabase); err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	db, err := openMaintenanceDB(stagedDatabase)
	if err != nil {
		return RestoreResult{}, ErrInvalidBackup
	}
	normalized, normalizedImports, err := validateAndPrepare(ctx, db, now(options.Now))
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		return RestoreResult{}, ErrInvalidBackup
	}
	if err := syncTree(staging); err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	probe := options.probe
	if probe == nil {
		probe = probeStagingDaemon
	}
	if err := probe(ctx, staging, options.DatabaseFilename); err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	if options.ValidationHook != nil {
		if err := options.ValidationHook(ctx, staging, options.DatabaseFilename); err != nil {
			return RestoreResult{}, ErrRestoreFailed
		}
	}
	if err := syncTree(staging); err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	stagedLock, err := instance.Acquire(filepath.Join(staging, "daemon.lock"))
	if err != nil {
		return RestoreResult{}, ErrRestoreFailed
	}
	defer stagedLock.Close()
	if err := publishDataDirectory(dataDirectory, staging); err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{Manifest: manifest, NormalizedJobs: normalized, NormalizedImports: normalizedImports, FTSRebuilt: true}, nil
}

func validateBackup(ctx context.Context, directory string) (Manifest, error) {
	allowed := map[string]bool{ManifestName: true, DatabaseName: true, ChecksumsName: true}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != len(allowed) {
		return Manifest{}, ErrInvalidBackup
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return Manifest{}, ErrInvalidBackup
		}
		if err := requireSecureFile(filepath.Join(directory, entry.Name())); err != nil {
			return Manifest{}, err
		}
	}
	var manifest Manifest
	if err := readJSON(filepath.Join(directory, ManifestName), &manifest); err != nil {
		return Manifest{}, ErrInvalidBackup
	}
	var checksums Checksums
	if err := readJSON(filepath.Join(directory, ChecksumsName), &checksums); err != nil || checksums.Algorithm != "SHA-256" || len(checksums.Files) != 2 {
		return Manifest{}, ErrInvalidBackup
	}
	for _, name := range []string{ManifestName, DatabaseName} {
		actual, err := checksumFile(filepath.Join(directory, name))
		if err != nil || checksums.Files[name] == "" || !strings.EqualFold(actual, checksums.Files[name]) {
			return Manifest{}, ErrInvalidBackup
		}
	}
	if manifest.FormatVersion != FormatVersion || manifest.SchemaVersion != storage.SchemaVersion {
		return Manifest{}, ErrIncompatibleBackup
	}
	if manifest.DatabaseChecksum != checksums.Files[DatabaseName] || manifest.BackupID == "" || manifest.DaemonVersion == "" || manifest.SQLiteIntegrityResult != "ok" || !manifest.FTSRebuildRequired || !manifest.SemanticRebuildNeeded {
		return Manifest{}, ErrInvalidBackup
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		return Manifest{}, ErrInvalidBackup
	}
	db, err := openMaintenanceDBReadOnly(filepath.Join(directory, DatabaseName))
	if err != nil {
		return Manifest{}, ErrInvalidBackup
	}
	inspected, inspectErr := inspectSnapshot(ctx, db)
	closeErr := db.Close()
	if inspectErr != nil || closeErr != nil || inspected.SchemaVersion != manifest.SchemaVersion || inspected.FTSIndexVersion != manifest.FTSIndexVersion || inspected.SemanticIndexVersion != manifest.SemanticIndexVersion || inspected.RunningImportCount != manifest.RunningImportCount || !reflect.DeepEqual(inspected.AuthorityCounts, manifest.AuthorityCounts) || !reflect.DeepEqual(inspected.UnfinishedJobSummary, manifest.UnfinishedJobSummary) || !reflect.DeepEqual(inspected.ProcessingVersions, manifest.ProcessingVersions) {
		return Manifest{}, ErrInvalidBackup
	}
	return manifest, nil
}

func inspectSnapshot(ctx context.Context, db *sql.DB) (Manifest, error) {
	if err := integrityCheck(ctx, db); err != nil {
		return Manifest{}, err
	}
	if err := foreignKeyCheck(ctx, db); err != nil {
		return Manifest{}, err
	}
	if err := storage.ValidateExactSchema(ctx, db); err != nil {
		return Manifest{}, err
	}
	var result Manifest
	if err := db.QueryRowContext(ctx, `SELECT schema_version FROM schema_metadata WHERE singleton=1 AND (SELECT count(*) FROM schema_metadata)=1`).Scan(&result.SchemaVersion); err != nil || result.SchemaVersion != storage.SchemaVersion {
		return Manifest{}, storage.ErrIncompatibleSchema
	}
	rows, err := db.QueryContext(ctx, `SELECT status, count(*) FROM jobs WHERE status IN ('pending','waiting','running','retrying') GROUP BY status ORDER BY status`)
	if err != nil {
		return Manifest{}, err
	}
	result.UnfinishedJobSummary = map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return Manifest{}, err
		}
		result.UnfinishedJobSummary[status] = count
	}
	if err := rows.Close(); err != nil {
		return Manifest{}, err
	}
	rows, err = db.QueryContext(ctx, `SELECT DISTINCT processing_version FROM (SELECT processing_version FROM memory_revisions UNION ALL SELECT processing_version FROM jobs) ORDER BY processing_version`)
	if err != nil {
		return Manifest{}, err
	}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return Manifest{}, err
		}
		result.ProcessingVersions = append(result.ProcessingVersions, version)
	}
	if err := rows.Close(); err != nil {
		return Manifest{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT index_version FROM index_metadata WHERE index_name='memory_fts'`).Scan(&result.FTSIndexVersion); err != nil {
		return Manifest{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT coalesce(active_version, 0) FROM semantic_index_metadata WHERE singleton=1`).Scan(&result.SemanticIndexVersion); err != nil {
		return Manifest{}, err
	}
	result.AuthorityCounts = make(map[string]int, len(authorityTables))
	for _, table := range authorityTables {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			return Manifest{}, err
		}
		result.AuthorityCounts[table] = count
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM import_operations WHERE state='running'`).Scan(&result.RunningImportCount); err != nil {
		return Manifest{}, err
	}
	result.SQLiteIntegrityResult = "ok"
	result.FTSRebuildRequired = true
	result.SemanticRebuildNeeded = true
	return result, nil
}

func validateAndPrepare(ctx context.Context, db *sql.DB, at time.Time) (int, int, error) {
	if _, err := inspectSnapshot(ctx, db); err != nil {
		return 0, 0, err
	}
	for _, query := range []string{
		`SELECT count(*) FROM memories m LEFT JOIN memory_projections p ON p.memory_id=m.memory_id AND p.revision_id=m.current_revision_id AND p.current_state=m.current_state AND p.space_id=m.space_id WHERE p.memory_id IS NULL`,
		`SELECT count(*) FROM memory_projections p LEFT JOIN memories m ON m.memory_id=p.memory_id WHERE m.memory_id IS NULL OR m.current_revision_id<>p.revision_id OR m.current_state<>p.current_state OR m.space_id<>p.space_id`,
		`SELECT count(*) FROM candidate_reviews r JOIN memory_revisions v ON v.revision_id=r.revision_id WHERE r.memory_id IS NOT NULL AND v.memory_id<>r.memory_id`,
		`SELECT count(*) FROM candidates c WHERE (c.review_state='pending' AND EXISTS (SELECT 1 FROM candidate_reviews r WHERE r.candidate_id=c.candidate_id)) OR (c.review_state<>'pending' AND NOT EXISTS (SELECT 1 FROM candidate_reviews r WHERE r.candidate_id=c.candidate_id AND r.to_state=c.review_state AND r.memory_id IS c.confirmed_memory_id AND r.revision_id IS c.confirmed_revision_id))`,
		`SELECT count(*) FROM idempotency_records WHERE NOT json_valid(response_body)`,
		`SELECT count(*) FROM entity_mentions em JOIN entities e ON e.entity_id=em.entity_id LEFT JOIN memories m ON m.memory_id=em.memory_id LEFT JOIN memory_revisions v ON v.revision_id=em.revision_id LEFT JOIN raw_events re ON re.event_id=em.event_id WHERE (em.revision_id IS NOT NULL AND (v.memory_id<>em.memory_id OR m.space_id<>e.space_id OR em.end_offset>length(v.content))) OR (em.event_id IS NOT NULL AND (re.space_id<>e.space_id OR em.end_offset>(SELECT length(coalesce(content_text,'')) FROM raw_contents WHERE content_id=re.content_id)))`,
		`SELECT count(*) FROM memory_relations r JOIN memories sm ON sm.memory_id=r.source_memory_id JOIN memories tm ON tm.memory_id=r.target_memory_id JOIN memory_revisions sv ON sv.revision_id=r.source_revision_id JOIN memory_revisions tv ON tv.revision_id=r.target_revision_id WHERE sv.memory_id<>r.source_memory_id OR tv.memory_id<>r.target_memory_id OR sm.space_id<>r.space_id OR tm.space_id<>r.space_id`,
		`SELECT count(*) FROM conflict_case_relations cr JOIN conflict_cases c ON c.case_id=cr.case_id JOIN memory_relations r ON r.relation_id=cr.relation_id WHERE r.relation_type<>'contradicts' OR r.space_id<>c.space_id`,
		`SELECT count(*) FROM conflict_cases c WHERE (c.state='open')<>(NOT EXISTS (SELECT 1 FROM conflict_resolution_events e WHERE e.case_id=c.case_id))`,
		`SELECT count(*) FROM feedback_events f JOIN memory_revisions v ON v.revision_id=f.revision_id WHERE v.memory_id<>f.memory_id`,
		`SELECT count(*) FROM aggregate_descriptors d JOIN memories a ON a.memory_id=d.memory_id WHERE a.memory_type<>'aggregate' OR a.sensitivity<>d.sensitivity`,
		`SELECT count(*) FROM aggregate_descriptors d JOIN memories a ON a.memory_id=d.memory_id WHERE NOT EXISTS (SELECT 1 FROM memory_relations r WHERE r.source_memory_id=d.memory_id AND r.source_revision_id=a.current_revision_id AND r.relation_type='derived_from') OR EXISTS (SELECT 1 FROM memory_relations r JOIN memories i ON i.memory_id=r.target_memory_id WHERE r.source_memory_id=d.memory_id AND r.source_revision_id=a.current_revision_id AND r.relation_type='derived_from' AND (r.space_id<>a.space_id OR (d.sensitivity='normal' AND i.sensitivity='sensitive')))`,
		`SELECT count(*) FROM aggregate_descriptors d JOIN memories a ON a.memory_id=d.memory_id JOIN aggregate_proposals p ON p.result_memory_id=d.memory_id AND p.result_revision_id=a.current_revision_id AND p.review_state='confirmed' WHERE (p.sensitivity='normal' AND EXISTS (SELECT 1 FROM aggregate_proposal_inputs i WHERE i.proposal_id=p.proposal_id AND i.input_sensitivity='sensitive')) OR EXISTS (SELECT memory_id,revision_id FROM aggregate_proposal_inputs WHERE proposal_id=p.proposal_id EXCEPT SELECT target_memory_id,target_revision_id FROM memory_relations WHERE source_memory_id=d.memory_id AND source_revision_id=a.current_revision_id AND relation_type='derived_from') OR EXISTS (SELECT target_memory_id,target_revision_id FROM memory_relations WHERE source_memory_id=d.memory_id AND source_revision_id=a.current_revision_id AND relation_type='derived_from' EXCEPT SELECT memory_id,revision_id FROM aggregate_proposal_inputs WHERE proposal_id=p.proposal_id)`,
		`SELECT count(*) FROM aggregate_freshness f JOIN aggregate_descriptors d ON d.aggregate_id=f.aggregate_id JOIN memories m ON m.memory_id=d.memory_id WHERE f.revision_id<>m.current_revision_id`,
		`SELECT count(*) FROM imported_provenance p JOIN import_object_mappings m ON m.mapping_id=p.mapping_id JOIN import_operations o ON o.import_operation_id=p.import_operation_id WHERE m.source_id<>o.source_id OR m.export_id<>o.export_id`,
		`SELECT count(*) FROM import_operations o WHERE o.committed_objects<>(SELECT coalesce(max(c.committed_objects),0) FROM import_checkpoints c WHERE c.import_operation_id=o.import_operation_id) OR o.committed_objects<>(SELECT count(*) FROM imported_provenance p WHERE p.import_operation_id=o.import_operation_id)`,
		`SELECT count(*) FROM import_checkpoints c JOIN import_operations o ON o.import_operation_id=c.import_operation_id WHERE c.generation>o.generation OR c.committed_objects>o.committed_objects`,
	} {
		var invalid int
		if err := db.QueryRowContext(ctx, query).Scan(&invalid); err != nil || invalid != 0 {
			return 0, 0, errors.New("domain invariant failed")
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	nowText := at.UTC().Format(time.RFC3339Nano)
	var semanticProvider, semanticModel sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT provider_identity, model_identity FROM semantic_index_metadata WHERE singleton=1`).Scan(&semanticProvider, &semanticModel); err != nil {
		return 0, 0, err
	}
	semanticIdentity := "restore/unconfigured"
	if semanticProvider.Valid && semanticProvider.String != "" && semanticModel.Valid && semanticModel.String != "" {
		semanticIdentity = semanticProvider.String + "/" + semanticModel.String
	}
	if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET finished_at=?, outcome='lease_lost', error_code='restore_fenced', error_message='attempt fenced by restore' WHERE finished_at IS NULL AND job_id IN (SELECT job_id FROM jobs WHERE status='running')`, nowText); err != nil {
		return 0, 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status=CASE WHEN cancel_requested_at IS NOT NULL THEN 'cancelled' WHEN attempt_count>=max_attempts THEN 'failed' ELSE 'retrying' END, lease_owner=NULL, lease_until=NULL, lease_generation=lease_generation+1, next_run_at=?, last_error_code='restore_fenced', last_error_message='attempt fenced by restore', finished_at=CASE WHEN cancel_requested_at IS NOT NULL OR attempt_count>=max_attempts THEN ? ELSE NULL END WHERE status='running'`, nowText, nowText)
	if err != nil {
		return 0, 0, err
	}
	normalized64, err := result.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	importResult, err := tx.ExecContext(ctx, `UPDATE import_operations SET state='waiting',generation=generation+1,last_error_code='restore_fenced',last_error_message='import attempt fenced by restore',finished_at=NULL,updated_at=? WHERE state='running'`, nowText)
	if err != nil {
		return 0, 0, err
	}
	normalizedImports64, err := importResult.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts`); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_fts(memory_id, revision_id, content) SELECT p.memory_id, p.revision_id, p.display_content FROM memory_projections p JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state WHERE p.searchable=1 ORDER BY p.memory_id`); err != nil {
		return 0, 0, err
	}
	var sourceCount, indexCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_projections WHERE searchable=1`).Scan(&sourceCount); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_fts`).Scan(&indexCount); err != nil || sourceCount != indexCount {
		return 0, 0, errors.New("FTS validation failed")
	}
	var mismatch int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT memory_id, revision_id, display_content AS content FROM memory_projections WHERE searchable=1 EXCEPT SELECT memory_id, revision_id, content FROM memory_fts)`).Scan(&mismatch); err != nil || mismatch != 0 {
		return 0, 0, errors.New("FTS validation failed")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT memory_id, revision_id, content FROM memory_fts EXCEPT SELECT memory_id, revision_id, display_content AS content FROM memory_projections WHERE searchable=1)`).Scan(&mismatch); err != nil || mismatch != 0 {
		return 0, 0, errors.New("FTS validation failed")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE index_metadata SET index_version=index_version+1, state='ready', source_revision_count=?, updated_at=? WHERE index_name='memory_fts'`, sourceCount, nowText); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE entity_index_metadata SET active_version=active_version+1,generation=generation+1,state='ready',source_entity_count=(SELECT count(*) FROM entities),source_mention_count=(SELECT count(*) FROM entity_mentions),updated_at=? WHERE singleton=1`, nowText); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE relation_index_metadata SET active_version=active_version+1,generation=generation+1,state='ready',source_relation_count=(SELECT count(*) FROM memory_relations),updated_at=? WHERE singleton=1`, nowText); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM feedback_aggregates`); err != nil {
		return 0, 0, err
	}
	var feedbackGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT generation+1 FROM feedback_aggregate_metadata WHERE singleton=1`).Scan(&feedbackGeneration); err != nil {
		return 0, 0, err
	}
	windowStart := at.UTC().AddDate(0, 0, -90).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `WITH latest AS (SELECT revision_id,caller_id,feedback_type,row_number() OVER (PARTITION BY revision_id,caller_id ORDER BY created_at DESC,feedback_id DESC) n FROM feedback_events WHERE created_at>=? AND created_at<=?), scores AS (SELECT revision_id,sum(feedback_type IN ('useful','correct')) positive,sum(feedback_type IN ('irrelevant','contest')) negative FROM latest WHERE n=1 GROUP BY revision_id) INSERT INTO feedback_aggregates(revision_id,algorithm_version,generation,positive_count,negative_count,bounded_usage_score,window_start,window_end,source_watermark,state,updated_at) SELECT revision_id,'v1',?,positive,negative,max(-1.0,min(1.0,(positive-negative)/8.0)),?,?,(SELECT max(created_at||'/'||feedback_id) FROM feedback_events WHERE created_at<=?),'current',? FROM scores ORDER BY revision_id`, windowStart, nowText, feedbackGeneration, windowStart, nowText, nowText, nowText); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE feedback_aggregate_metadata SET algorithm_version='v1',generation=?,state='ready',source_watermark=(SELECT max(created_at||'/'||feedback_id) FROM feedback_events WHERE created_at<=?),window_days=90,updated_at=? WHERE singleton=1`, feedbackGeneration, nowText, nowText); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM aggregate_freshness`); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO aggregate_freshness(aggregate_id,revision_id,state,input_fingerprint,input_snapshot_json,stale_reasons_json,generation,checked_at,updated_at) SELECT d.aggregate_id,m.current_revision_id,'fresh',p.input_fingerprint,(SELECT json_group_array(json_object('MemoryID',i.memory_id,'RevisionID',i.revision_id,'SpaceID',i.input_space_id,'State',i.input_state,'Sensitivity',i.input_sensitivity)) FROM aggregate_proposal_inputs i WHERE i.proposal_id=p.proposal_id ORDER BY i.input_order),'[]',1,?,? FROM aggregate_descriptors d JOIN memories m ON m.memory_id=d.memory_id JOIN aggregate_proposals p ON p.result_memory_id=d.memory_id AND p.result_revision_id=m.current_revision_id AND p.review_state='confirmed' ORDER BY d.aggregate_id`, nowText, nowText); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE aggregate_freshness SET state='stale',stale_reasons_json='["input_authority_changed"]' WHERE EXISTS (SELECT 1 FROM json_each(input_snapshot_json) s LEFT JOIN memories m ON m.memory_id=json_extract(s.value,'$.MemoryID') WHERE m.memory_id IS NULL OR m.current_revision_id<>json_extract(s.value,'$.RevisionID') OR m.space_id<>json_extract(s.value,'$.SpaceID') OR m.current_state<>json_extract(s.value,'$.State') OR m.sensitivity<>json_extract(s.value,'$.Sensitivity'))`); err != nil {
		return 0, 0, err
	}
	var descriptorCount, freshnessCount int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM aggregate_descriptors),(SELECT count(*) FROM aggregate_freshness)`).Scan(&descriptorCount, &freshnessCount); err != nil || descriptorCount != freshnessCount {
		return 0, 0, errors.New("aggregate freshness rebuild failed")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE semantic_index_metadata SET active_version=NULL, building_version=NULL, state='absent', provider_identity=NULL, model_identity=NULL, dimension=NULL, source_revision_count=0, updated_at=? WHERE singleton=1`, nowText); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memory_projections SET embedding_state='stale', embedding_provider=NULL, embedding_model=NULL, embedding_dimension=NULL, embedding_content_hash=NULL`); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status='cancelled', lease_owner=NULL, lease_until=NULL, finished_at=?, last_error_code='restore_semantic_fenced', last_error_message='semantic work replaced by restore recovery' WHERE job_type IN ('embed_memory','rebuild_semantic') AND status IN ('pending','waiting','retrying')`, nowText); err != nil {
		return 0, 0, err
	}
	var maxAttempts int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(max_attempts),3) FROM jobs`).Scan(&maxAttempts); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) SELECT 'restore-embed-'||lower(hex(randomblob(16))), 'restore-operation-'||lower(hex(randomblob(16))), 'embed_memory','memory_revision',p.revision_id,?,'waiting',100,?,?,? FROM memory_projections p JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state WHERE p.searchable=1 ORDER BY p.memory_id`, semanticIdentity, maxAttempts, nowText, nowText); err != nil {
		return 0, 0, err
	}
	if sourceCount > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) VALUES ('restore-semantic-'||lower(hex(randomblob(16))), 'restore-operation-'||lower(hex(randomblob(16))), 'rebuild_semantic','index','memory_semantic',?,'waiting',-100,?,?,?)`, semanticIdentity, maxAttempts, nowText, nowText); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	if err := integrityCheck(ctx, db); err != nil {
		return 0, 0, err
	}
	return int(normalized64), int(normalizedImports64), foreignKeyCheck(ctx, db)
}

func probeStagingDaemon(ctx context.Context, staging, databaseFilename string) error {
	runtimeDir, err := os.MkdirTemp("", ".dmem-restore-runtime-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(runtimeDir)
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		return err
	}
	databasePath := filepath.Join(staging, databaseFilename)
	pristinePath := filepath.Join(staging, ".restore-probe-pristine.sqlite")
	if err := copyRegularFile(databasePath, pristinePath); err != nil {
		return err
	}
	if err := addProbeCanaries(ctx, databasePath); err != nil {
		return err
	}

	cfg, err := config.Default()
	if err != nil {
		return err
	}
	cfg.DataDir = staging
	cfg.DatabasePath = databasePath
	cfg.RuntimeDir = runtimeDir
	cfg.Endpoint = filepath.Join(runtimeDir, "staging.sock")
	cfg.ProcessingProvider = config.ProviderConfig{}
	cfg.EmbeddingProvider = config.ProviderConfig{}
	cfg.DiskSoftFree, cfg.DiskHardFree = 0, 0
	daemonInstance, err := daemon.StartStaging(ctx, cfg)
	if err != nil {
		return err
	}
	probeErr := probeStagingHTTP(ctx, cfg.Endpoint)
	closeCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGracePeriod)
	closeErr := daemonInstance.Close(closeCtx)
	cancel()
	if probeErr != nil || closeErr != nil {
		return errors.Join(probeErr, closeErr)
	}
	if err := os.Rename(pristinePath, databasePath); err != nil {
		return err
	}
	db, err := openMaintenanceDB(cfg.DatabasePath)
	if err != nil {
		return err
	}
	_, validationErr := inspectSnapshot(ctx, db)
	closeErr = db.Close()
	return errors.Join(validationErr, closeErr)
}

func addProbeCanaries(ctx context.Context, databasePath string) error {
	db, err := openMaintenanceDB(databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const (
		rootID       = "00000000-0000-4000-8000-000000000061"
		firstSpace   = "00000000-0000-4000-8000-000000000062"
		secondSpace  = "00000000-0000-4000-8000-000000000063"
		firstMemory  = "00000000-0000-4000-8000-000000000064"
		secondMemory = "00000000-0000-4000-8000-000000000065"
	)
	var parent string
	err = tx.QueryRowContext(ctx, `SELECT space_id FROM memory_spaces WHERE space_type='user'`).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		parent = rootID
		if _, err = tx.ExecContext(ctx, `INSERT INTO memory_spaces(space_id,space_type,external_id,display_name,created_at) VALUES (?, 'user', 'restore-probe', 'Restore probe', '2000-01-01T00:00:00Z')`, parent); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for _, values := range [][2]string{{firstSpace, "first"}, {secondSpace, "second"}} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_spaces(space_id,parent_space_id,space_type,external_id,display_name,created_at) VALUES (?,?,'project',?,'Restore probe','2000-01-01T00:00:00Z')`, values[0], parent, "restore-probe-"+values[1]); err != nil {
			return err
		}
	}
	for _, values := range [][3]string{{firstMemory, firstSpace, "restoreprobe common firstonly"}, {secondMemory, secondSpace, "restoreprobe common secondonly"}} {
		revisionID := values[0] + "-revision"
		if _, err := tx.ExecContext(ctx, `INSERT INTO memories(memory_id,space_id,memory_type,current_revision_id,current_state,created_at,updated_at) VALUES (?,?, 'fact',?,'active','2000-01-01T00:00:00Z','2000-01-01T00:00:00Z')`, values[0], values[1], revisionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_revisions(revision_id,memory_id,content,content_hash,created_by,processing_version,created_at) VALUES (?,?,?,'restore-probe','restore-probe','restore-probe','2000-01-01T00:00:00Z')`, revisionID, values[0], values[2]); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_projections(memory_id,revision_id,space_id,memory_type,current_state,searchable,display_content,content_hash,updated_at) VALUES (?,?,?,'fact','active',1,?,'restore-probe','2000-01-01T00:00:00Z')`, values[0], revisionID, values[1], values[2]); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_fts(memory_id,revision_id,content) VALUES (?,?,?)`, values[0], revisionID, values[2]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func probeStagingHTTP(ctx context.Context, endpoint string) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/v1/health", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	var health struct {
		Ready               bool   `json:"ready"`
		ProcessAlive        bool   `json:"process_alive"`
		StorageState        string `json:"storage_state"`
		SchemaCompatibility string `json:"schema_compatibility"`
		FTSState            string `json:"fts_state"`
		ProcessorState      string `json:"processor_state"`
		SemanticIndexState  string `json:"semantic_index_state"`
		EntityIndexState    string `json:"entity_index_state"`
		RelationIndexState  string `json:"relation_index_state"`
		FeedbackState       string `json:"feedback_aggregate_state"`
		AggregateState      string `json:"aggregate_state"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&health)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil || response.StatusCode != http.StatusOK || !health.Ready || !health.ProcessAlive || health.StorageState != "ready" || health.SchemaCompatibility != "compatible" || health.FTSState != "ready" || health.ProcessorState != "not_configured" || health.SemanticIndexState != "rebuild_required" || health.EntityIndexState != "ready" || health.RelationIndexState != "ready" || health.FeedbackState != "ready" || health.AggregateState != "ready" {
		return errors.New("staging daemon is not ready")
	}
	for _, probe := range []struct {
		query, mode, executed string
		degraded              bool
		reasons               []string
		wantID                string
	}{
		{"firstonly", "keyword", "keyword", false, []string{}, "00000000-0000-4000-8000-000000000064"},
		{"common", "hybrid", "keyword", true, []string{"semantic_unavailable"}, "00000000-0000-4000-8000-000000000064"},
	} {
		body, _ := json.Marshal(map[string]any{"query": probe.query, "space_ids": []string{"00000000-0000-4000-8000-000000000062"}, "mode": probe.mode, "limit": 10})
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/v1/search", bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Caller-ID", "restore-probe")
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		var result domain.SearchResponse
		err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
		closeErr = response.Body.Close()
		if err != nil || closeErr != nil || response.StatusCode != http.StatusOK || result.RequestedMode != probe.mode || result.ExecutedMode != probe.executed || result.Degraded != probe.degraded || !reflect.DeepEqual(result.DegradationReasons, probe.reasons) || len(result.Results) != 1 || result.Results[0].Memory.MemoryID != probe.wantID {
			return errors.New("staging retrieval probe failed")
		}
	}
	return nil
}

func integrityCheck(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil || value != "ok" {
			return errors.New("integrity check failed")
		}
		count++
	}
	if count != 1 {
		return errors.New("integrity check failed")
	}
	return rows.Err()
}

func foreignKeyCheck(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("foreign key check failed")
	}
	return rows.Err()
}

func publishDataDirectory(current, staging string) error {
	parent := filepath.Dir(filepath.Clean(current))
	if err := renameExchange(current, staging); err != nil {
		return ErrRestoreFailed
	}
	if err := syncDirectory(parent); err != nil {
		_ = renameExchange(current, staging)
		_ = syncDirectory(parent)
		return ErrRestoreFailed
	}
	return nil
}

func renameExchange(first, second string) error {
	firstPointer, err := syscall.BytePtrFromString(first)
	if err != nil {
		return err
	}
	secondPointer, err := syscall.BytePtrFromString(second)
	if err != nil {
		return err
	}
	const renameExchangeFlag = 2
	sysRenameat2 := uintptr(316)
	if runtime.GOARCH == "arm64" {
		sysRenameat2 = 276
	}
	_, _, errno := syscall.Syscall6(
		sysRenameat2,
		^uintptr(99),
		uintptr(unsafe.Pointer(firstPointer)),
		^uintptr(99),
		uintptr(unsafe.Pointer(secondPointer)),
		renameExchangeFlag,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func requireSecureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByCurrentUser(info) {
		return ErrUnsafePath
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ErrUnsafePath
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != filepath.Clean(absolute) {
		return ErrUnsafePath
	}
	return nil
}

func requireSecureFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) {
		return ErrUnsafePath
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func requireFreeSpace(path string, minimum uint64) error {
	if minimum == 0 {
		return nil
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return ErrBackupFailed
	}
	if stat.Bavail*uint64(stat.Bsize) < minimum {
		return ErrInsufficientStorage
	}
	return nil
}

func openMaintenanceDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(path)+"?_foreign_keys=on&_journal_mode=DELETE&_synchronous=FULL&_busy_timeout=5000")
	if err == nil {
		err = db.Ping()
	}
	if err != nil {
		if db != nil {
			db.Close()
		}
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func openMaintenanceDBReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(path)+"?mode=ro&_foreign_keys=on")
	if err == nil {
		err = db.Ping()
	}
	if err != nil {
		if db != nil {
			db.Close()
		}
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func copyRegularFile(source, destination string) error {
	if err := requireSecureFile(source); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	return errors.Join(copyErr, syncErr, closeErr)
}

func writeJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	err = encoder.Encode(value)
	return errors.Join(err, file.Sync(), file.Close())
}

func readJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON content")
	}
	return nil
}

func checksumFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func syncTree(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := syncTree(filepath.Join(directory, entry.Name())); err != nil {
				return err
			}
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return errors.New("unexpected file type")
		}
		file, err := os.Open(filepath.Join(directory, entry.Name()))
		if err != nil {
			return err
		}
		err = errors.Join(file.Sync(), file.Close())
		if err != nil {
			return err
		}
	}
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func backupID(generate func() (string, error)) (string, error) {
	if generate != nil {
		return generate()
	}
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func now(clock func() time.Time) time.Time {
	if clock != nil {
		return clock().UTC()
	}
	return time.Now().UTC()
}
