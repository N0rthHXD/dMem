package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestOpenInitializesSchemaAndSQLiteCapabilities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dmem.db")
	store, err := Open(ctx, path, Options{ReadConnections: 2})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	version, err := store.SchemaVersion(ctx)
	if err != nil || version != SchemaVersion {
		t.Fatalf("SchemaVersion() = %d, %v; want %d, nil", version, err, SchemaVersion)
	}

	var jsonValid int
	if err := store.reader.QueryRowContext(ctx, `SELECT json_valid('{"ok":true}')`).Scan(&jsonValid); err != nil || jsonValid != 1 {
		t.Fatalf("JSON capability = %d, %v", jsonValid, err)
	}
	if _, err := store.writer.ExecContext(ctx, "INSERT INTO memory_fts(memory_id, revision_id, content) VALUES ('m', 'r', 'searchable')"); err != nil {
		t.Fatalf("FTS5 insert: %v", err)
	}
	var matchCount int
	if err := store.reader.QueryRowContext(ctx, "SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'searchable'").Scan(&matchCount); err != nil || matchCount != 1 {
		t.Fatalf("FTS5 query count = %d, %v", matchCount, err)
	}
	if _, err := store.writer.ExecContext(ctx, "INSERT INTO memory_fts(memory_id, revision_id, content) VALUES ('mixed', 'mixed-r', 'SQLite 支持中英文混合检索')"); err != nil {
		t.Fatalf("FTS5 mixed-language insert: %v", err)
	}
	for _, query := range []string{"QLite", "中英文"} {
		if err := store.reader.QueryRowContext(ctx, "SELECT count(*) FROM memory_fts WHERE memory_fts MATCH ?", query).Scan(&matchCount); err != nil || matchCount != 1 {
			t.Errorf("FTS5 mixed-language query %q count = %d, %v; want 1, nil", query, matchCount, err)
		}
	}
	if _, err := store.writer.ExecContext(ctx, "INSERT INTO schema_metadata(singleton, schema_version, initialized_at) VALUES ('wrong', 1, 'now')"); err == nil {
		t.Fatal("STRICT table accepted a text primary key")
	}
	if _, err := store.writer.ExecContext(ctx, "INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES ('e', 'o', 'missing', 'missing', 'manual', 'test', 'test', 'now', 'now', 'now')"); err == nil {
		t.Fatal("foreign key violation was accepted")
	}

	for pragma, want := range map[string]string{
		"foreign_keys":   "1",
		"journal_mode":   "wal",
		"synchronous":    "1",
		"busy_timeout":   "5000",
		"trusted_schema": "0",
	} {
		var got string
		if err := store.writer.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
			t.Errorf("PRAGMA %s = %q, %v; want %q", pragma, got, err, want)
		}
	}
}

func TestOpenRejectsIncompatibleSchemaWithoutModification(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dmem.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE schema_metadata (singleton INTEGER PRIMARY KEY, schema_version INTEGER NOT NULL, initialized_at TEXT NOT NULL); INSERT INTO schema_metadata VALUES (1, 2, 'before')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	store, err := Open(context.Background(), path, Options{})
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, ErrIncompatibleSchema) {
		t.Fatalf("Open() error = %v; want ErrIncompatibleSchema", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("incompatible database was modified")
	}
}

func TestOpenRejectsStaleSchemaWithCurrentVersionNumber(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dmem.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE schema_metadata (singleton INTEGER PRIMARY KEY, schema_version INTEGER NOT NULL, initialized_at TEXT NOT NULL); INSERT INTO schema_metadata VALUES (1, 1, 'before')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path, Options{})
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, ErrIncompatibleSchema) {
		t.Fatalf("Open() error = %v; want ErrIncompatibleSchema", err)
	}
}

func TestOpenRejectsAlmostCurrentCatalogWithoutModification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dmem.db")
	store, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, `DROP INDEX audit_operation_idx`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path, Options{})
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, ErrIncompatibleSchema) {
		t.Fatalf("Open() error = %v; want ErrIncompatibleSchema", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("almost-current database was modified")
	}
}

func TestOpenRejectsFailedIntegrityCheck(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dmem.db")
	want := errors.New("corrupt")
	store, err := Open(context.Background(), path, Options{
		IntegrityCheck: func(context.Context, *sql.DB) error { return want },
	})
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, want) {
		t.Fatalf("Open() error = %v; want integrity error", err)
	}
}

func TestOpenRunsIntegrityCheckInBackground(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dmem.db")
	started := make(chan struct{})
	release := make(chan struct{})
	store, err := Open(ctx, path, Options{
		IntegrityCheckMode: IntegrityCheckBackground,
		IntegrityCheck: func(context.Context, *sql.DB) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	<-started
	if state, _ := store.IntegrityStatus(); state != integrityPending {
		t.Fatalf("IntegrityStatus() = %q; want %q", state, integrityPending)
	}
	close(release)
	if err := store.WaitIntegrity(ctx); err != nil {
		t.Fatalf("WaitIntegrity() error = %v", err)
	}
	if state, err := store.IntegrityStatus(); state != integrityReady || err != nil {
		t.Fatalf("IntegrityStatus() = %q, %v; want ready, nil", state, err)
	}
}

func TestOpenBackgroundIntegrityCheckReportsFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dmem.db")
	want := errors.New("corrupt")
	store, err := Open(ctx, path, Options{
		IntegrityCheckMode: IntegrityCheckBackground,
		IntegrityCheck:     func(context.Context, *sql.DB) error { return want },
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	if err := store.WaitIntegrity(ctx); !errors.Is(err, want) {
		t.Fatalf("WaitIntegrity() error = %v; want %v", err, want)
	}
	if state, err := store.IntegrityStatus(); state != integrityFailed || !errors.Is(err, want) {
		t.Fatalf("IntegrityStatus() = %q, %v; want failed, %v", state, err, want)
	}
}

func TestStorageStatusReportsFailedBackgroundIntegrityCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	want := errors.New("corrupt")
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dmem.db"), Options{
		IntegrityCheckMode: IntegrityCheckBackground,
		IntegrityCheck:     func(context.Context, *sql.DB) error { return want },
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.WaitIntegrity(ctx); !errors.Is(err, want) {
		t.Fatalf("WaitIntegrity() error = %v; want %v", err, want)
	}
	repository := NewRepository(store, domain.Runtime{Now: time.Now, ID: func() (string, error) { return "test-id", nil }}, 10, 20, 3, "v1")
	state, schema := repository.StorageStatus(ctx)
	if state != "unavailable" || schema != "compatible" {
		t.Fatalf("StorageStatus() = %q, %q; want unavailable, compatible", state, schema)
	}
}

func TestCloseCancelsBackgroundIntegrityCheck(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dmem.db")
	started := make(chan struct{})
	stopped := make(chan struct{})
	store, err := Open(context.Background(), path, Options{
		IntegrityCheckMode: IntegrityCheckBackground,
		IntegrityCheck: func(ctx context.Context, _ *sql.DB) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	<-started
	done := make(chan error, 1)
	go func() { done <- store.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not cancel the background integrity check")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("background integrity check did not observe cancellation")
	}
}
