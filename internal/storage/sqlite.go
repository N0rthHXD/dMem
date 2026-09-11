package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3"
)

const driverName = "dmem_sqlite3"

var ErrIncompatibleSchema = errors.New("incompatible schema version")

const (
	integrityOff     = "off"
	integrityPending = "pending"
	integrityReady   = "ready"
	integrityFailed  = "failed"
)

func init() {
	_ = requiredSQLiteBuildProfile
	sql.Register(driverName, &sqlite3.SQLiteDriver{ConnectHook: configureConnection})
}

// IntegrityCheckMode selects whether the SQLite integrity check runs before
// Open returns, in the background, or not at all.
type IntegrityCheckMode int

const (
	IntegrityCheckSync IntegrityCheckMode = iota
	IntegrityCheckBackground
	IntegrityCheckOff
)

type Options struct {
	ReadConnections    int
	IntegrityCheck     func(context.Context, *sql.DB) error
	IntegrityCheckMode IntegrityCheckMode
}

type Store struct {
	writer *sql.DB
	reader *sql.DB
	path   string

	checkMu     sync.Mutex
	checkState  string
	checkErr    error
	checkDone   chan struct{}
	checkCancel context.CancelFunc
}

func Open(ctx context.Context, path string, options Options) (*Store, error) {
	if options.ReadConnections < 1 {
		options.ReadConnections = 1
	}
	info, statErr := os.Lstat(path)
	newDatabase := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !newDatabase {
		return nil, fmt.Errorf("inspect database: %w", statErr)
	}
	if !newDatabase && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("database path must not be a symlink")
	}
	if !newDatabase {
		if err := checkCompatibility(ctx, path); err != nil {
			return nil, err
		}
	}

	dsn := databaseDSN(path)
	writer, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	if err := writer.PingContext(ctx); err != nil {
		writer.Close()
		return nil, fmt.Errorf("connect SQLite writer: %w", err)
	}
	if err := verifyVectorExtension(ctx, writer); err != nil {
		writer.Close()
		return nil, err
	}
	if newDatabase {
		if _, err := writer.ExecContext(ctx, schemaV1); err != nil {
			writer.Close()
			return nil, fmt.Errorf("initialize schema v1: %w", err)
		}
	}
	if err := verifySchema(ctx, writer); err != nil {
		writer.Close()
		return nil, err
	}
	store := &Store{writer: writer, path: path, checkState: integrityReady}
	check := options.IntegrityCheck
	if check == nil {
		check = quickCheck
	}
	switch options.IntegrityCheckMode {
	case IntegrityCheckOff:
		store.checkState = integrityOff
	case IntegrityCheckBackground:
		store.startIntegrityCheck(check)
	default:
		if err := check(ctx, writer); err != nil {
			writer.Close()
			return nil, fmt.Errorf("SQLite integrity check failed: %w", err)
		}
	}

	reader, err := sql.Open(driverName, databaseReadDSN(path))
	if err != nil {
		store.cancelIntegrityCheck()
		writer.Close()
		return nil, fmt.Errorf("open SQLite reader: %w", err)
	}
	reader.SetMaxOpenConns(options.ReadConnections)
	reader.SetMaxIdleConns(options.ReadConnections)
	if err := reader.PingContext(ctx); err != nil {
		store.cancelIntegrityCheck()
		reader.Close()
		writer.Close()
		return nil, fmt.Errorf("connect SQLite reader: %w", err)
	}
	store.reader = reader
	return store, nil
}

// startIntegrityCheck runs the integrity check in the background on a
// dedicated read-only connection so it never blocks the daemon writer or
// starves the reader pool.
func (s *Store) startIntegrityCheck(check func(context.Context, *sql.DB) error) {
	dsn := "file:" + url.PathEscape(s.path) + "?mode=ro&_txlock=deferred"
	checkDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		s.checkState = integrityFailed
		s.checkErr = err
		return
	}
	checkDB.SetMaxOpenConns(1)
	checkDB.SetMaxIdleConns(1)
	checkCtx, cancel := context.WithCancel(context.Background())
	s.checkCancel = cancel
	s.checkState = integrityPending
	s.checkDone = make(chan struct{})
	go func() {
		defer close(s.checkDone)
		defer checkDB.Close()
		err := check(checkCtx, checkDB)
		s.checkMu.Lock()
		defer s.checkMu.Unlock()
		if err != nil {
			s.checkState = integrityFailed
			s.checkErr = err
			return
		}
		s.checkState = integrityReady
	}()
}

func (s *Store) cancelIntegrityCheck() {
	if s.checkCancel != nil {
		s.checkCancel()
	}
	if s.checkDone != nil {
		<-s.checkDone
	}
}

// IntegrityStatus reports one of off, pending, ready, or failed. The error is
// the integrity failure when the state is failed.
func (s *Store) IntegrityStatus() (string, error) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if s.checkState == "" {
		return integrityReady, nil
	}
	return s.checkState, s.checkErr
}

// WaitIntegrity blocks until the background integrity check completes or the
// context is done.
func (s *Store) WaitIntegrity(ctx context.Context) error {
	if s.checkDone == nil {
		_, err := s.IntegrityStatus()
		return err
	}
	select {
	case <-s.checkDone:
		_, err := s.IntegrityStatus()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func configureConnection(conn *sqlite3.SQLiteConn) error {
	for _, pragma := range []string{
		"PRAGMA foreign_keys=ON",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA trusted_schema=OFF",
	} {
		if _, err := conn.Exec(pragma, nil); err != nil {
			return fmt.Errorf("apply %s: %w", pragma, err)
		}
	}
	return nil
}

func checkCompatibility(ctx context.Context, path string) error {
	dsn := "file:" + url.PathEscape(path) + "?mode=ro"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return fmt.Errorf("open database for compatibility check: %w", err)
	}
	defer db.Close()
	return ValidateExactSchema(ctx, db)
}

func verifySchema(ctx context.Context, db *sql.DB) error {
	var version int
	err := db.QueryRowContext(ctx, "SELECT schema_version FROM schema_metadata WHERE singleton = 1").Scan(&version)
	if err != nil {
		return fmt.Errorf("%w: read schema metadata: %v", ErrIncompatibleSchema, err)
	}
	if version != SchemaVersion {
		return fmt.Errorf("%w: expected %d, found %d", ErrIncompatibleSchema, SchemaVersion, version)
	}
	var rows int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_metadata").Scan(&rows); err != nil || rows != 1 {
		return fmt.Errorf("%w: schema metadata must contain exactly one row", ErrIncompatibleSchema)
	}
	for _, table := range []string{
		"aggregate_descriptors",
		"aggregate_freshness",
		"aggregate_proposal_inputs",
		"aggregate_proposal_reviews",
		"aggregate_proposals",
		"candidate_evidence",
		"candidate_related_memories",
		"candidate_reviews",
		"candidates",
		"capability_states",
		"configuration_activations",
		"configuration_revisions",
		"conflict_case_participants",
		"conflict_case_relations",
		"conflict_cases",
		"conflict_resolution_events",
		"conflict_resolution_result_refs",
		"corpus_metadata",
		"effective_configuration_state",
		"effective_space_policies",
		"embedding_eligibility",
		"entities",
		"entity_alias_events",
		"entity_aliases",
		"entity_events",
		"entity_index_metadata",
		"entity_mentions",
		"entity_merge_proposals",
		"entity_proposal_reviews",
		"entity_proposals",
		"evolution_proposal_evidence",
		"evolution_proposal_reviews",
		"evolution_proposal_targets",
		"evolution_proposals",
		"evolution_result_refs",
		"export_operations",
		"feedback_aggregate_metadata",
		"feedback_aggregates",
		"feedback_events",
		"import_checkpoints",
		"import_object_mappings",
		"import_operations",
		"import_sources",
		"import_space_mappings",
		"imported_provenance",
		"memory_embeddings",
		"memory_relations",
		"policy_assignments",
		"provider_policies",
		"reconciliation_runs",
		"relation_evidence",
		"relation_index_metadata",
		"relation_proposal_reviews",
		"relation_proposals",
		"relation_state_events",
		"semantic_index_metadata",
	} {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			return fmt.Errorf("%w: required table %s is missing", ErrIncompatibleSchema, table)
		}
	}
	return nil
}

// ValidateExactSchema requires the database catalog to match the current initialization schema.
func ValidateExactSchema(ctx context.Context, db *sql.DB) error {
	if err := verifySchema(ctx, db); err != nil {
		return err
	}
	expected, err := sql.Open(driverName, ":memory:")
	if err != nil {
		return fmt.Errorf("%w: initialize expected catalog", ErrIncompatibleSchema)
	}
	defer expected.Close()
	expected.SetMaxOpenConns(1)
	if _, err := expected.ExecContext(ctx, schemaV1); err != nil {
		return fmt.Errorf("%w: initialize expected catalog", ErrIncompatibleSchema)
	}
	want, err := readSchemaCatalog(ctx, expected)
	if err != nil {
		return fmt.Errorf("%w: read expected catalog", ErrIncompatibleSchema)
	}
	got, err := readSchemaCatalog(ctx, db)
	if err != nil {
		return fmt.Errorf("%w: read database catalog", ErrIncompatibleSchema)
	}
	if len(got) != len(want) {
		return fmt.Errorf("%w: database catalog differs", ErrIncompatibleSchema)
	}
	for index := range want {
		if got[index] != want[index] {
			return fmt.Errorf("%w: database catalog differs", ErrIncompatibleSchema)
		}
	}
	return nil
}

func readSchemaCatalog(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT type || char(0) || name || char(0) || tbl_name || char(0) || coalesce(sql, '') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var catalog []string
	for rows.Next() {
		var entry string
		if err := rows.Scan(&entry); err != nil {
			return nil, err
		}
		catalog = append(catalog, entry)
	}
	return catalog, rows.Err()
}

func quickCheck(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		if !strings.EqualFold(result, "ok") {
			return errors.New(result)
		}
	}
	return rows.Err()
}

func databaseDSN(path string) string {
	return "file:" + url.PathEscape(path) + "?_txlock=immediate"
}

func databaseReadDSN(path string) string {
	return "file:" + url.PathEscape(path) + "?_txlock=deferred"
}

func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.reader.QueryRowContext(ctx, "SELECT schema_version FROM schema_metadata WHERE singleton = 1").Scan(&version)
	return version, err
}

func (s *Store) IndexState(ctx context.Context) (string, error) {
	var state string
	err := s.reader.QueryRowContext(ctx, "SELECT state FROM index_metadata WHERE index_name = 'memory_fts'").Scan(&state)
	return state, err
}

// OnlineBackup writes a consistent snapshot of the store using SQLite's backup API.
func (s *Store) OnlineBackup(ctx context.Context, destination string) error {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	destinationDB, err := sql.Open("sqlite3", "file:"+url.PathEscape(destination)+"?mode=rwc")
	if err != nil {
		return err
	}
	destinationDB.SetMaxOpenConns(1)
	defer destinationDB.Close()

	sourceConn, err := s.writer.Conn(ctx)
	if err != nil {
		return err
	}
	defer sourceConn.Close()
	destinationConn, err := destinationDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer destinationConn.Close()

	return sourceConn.Raw(func(sourceDriver any) error {
		source, ok := sourceDriver.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("unexpected SQLite source connection")
		}
		return destinationConn.Raw(func(destinationDriver any) error {
			destination, ok := destinationDriver.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("unexpected SQLite destination connection")
			}
			backup, err := destination.Backup("main", source, "main")
			if err != nil {
				return err
			}
			defer backup.Finish()
			for {
				done, err := backup.Step(256)
				if err != nil {
					return err
				}
				if done {
					return backup.Finish()
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Millisecond):
				}
			}
		})
	})
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.cancelIntegrityCheck()
	return errors.Join(s.reader.Close(), s.writer.Close())
}
