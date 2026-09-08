package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// init registers the statically-linked sqlite-vec extension as a SQLite
// auto-extension. Every SQLite connection created in this process (including
// the writer, reader pool, maintenance, and restore-probe connections that use
// either the custom driver or the plain go-sqlite3 driver) will load sqlite-vec
// before it can run any statement. No dynamic extension loading is performed.
func init() {
	vec.Auto()
}

// verifyVectorExtension fails the store open when the linked sqlite-vec
// extension is unavailable. A store whose schema depends on vec_* functions
// must never silently degrade into incorrect retrieval results.
func verifyVectorExtension(ctx context.Context, db *sql.DB) error {
	var version string
	if err := db.QueryRowContext(ctx, "SELECT vec_version()").Scan(&version); err != nil {
		return fmt.Errorf("sqlite-vec extension unavailable: %w", err)
	}
	if version == "" {
		return errors.New("sqlite-vec extension returned an empty version")
	}
	var length int
	if err := db.QueryRowContext(ctx, "SELECT vec_length(?)", mustSerializeFloat32([]float32{1, 0})).Scan(&length); err != nil {
		return fmt.Errorf("sqlite-vec vec_length unavailable: %w", err)
	}
	if length != 2 {
		return fmt.Errorf("sqlite-vec vec_length returned unexpected dimension %d", length)
	}
	return nil
}

func mustSerializeFloat32(values []float32) []byte {
	blob, err := vec.SerializeFloat32(values)
	if err != nil {
		panic(err)
	}
	return blob
}
