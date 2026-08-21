package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/mattn/go-sqlite3"
)

const driverName = "dmem_sqlite3"

var ErrIncompatibleSchema = errors.New("incompatible schema version")

func init() {
	_ = requiredSQLiteBuildProfile
	sql.Register(driverName, &sqlite3.SQLiteDriver{ConnectHook: configureConnection})
}

type Options struct {
	ReadConnections int
	IntegrityCheck  func(context.Context, *sql.DB) error
}

type Store struct {
	writer *sql.DB
	reader *sql.DB
	path   string
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
	check := options.IntegrityCheck
	if check == nil {
		check = quickCheck
	}
	if err := check(ctx, writer); err != nil {
		writer.Close()
		return nil, fmt.Errorf("SQLite integrity check failed: %w", err)
	}

	reader, err := sql.Open(driverName, dsn)
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("open SQLite reader: %w", err)
	}
	reader.SetMaxOpenConns(options.ReadConnections)
	reader.SetMaxIdleConns(options.ReadConnections)
	if err := reader.PingContext(ctx); err != nil {
		reader.Close()
		writer.Close()
		return nil, fmt.Errorf("connect SQLite reader: %w", err)
	}
	return &Store{writer: writer, reader: reader, path: path}, nil
}

func configureConnection(conn *sqlite3.SQLiteConn) error {
	for _, pragma := range []string{
		"PRAGMA foreign_keys=ON",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
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
	return verifySchema(ctx, db)
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
	return nil
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

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return errors.Join(s.reader.Close(), s.writer.Close())
}
