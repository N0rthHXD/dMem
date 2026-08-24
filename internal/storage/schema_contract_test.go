package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP3CONTRACT002DocumentedSchemaMatchesEmbeddedSchema(t *testing.T) {
	documented, err := os.ReadFile(filepath.Join("..", "..", "docs", "storage", "schema-v1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if string(documented) != schemaV1 {
		t.Fatal("docs/storage/schema-v1.sql differs from embedded internal/storage/schema-v1.sql")
	}
}

func TestP3CONTRACT002InitializationCatalogAndEnums(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "p3-contract.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	for _, statement := range []string{
		`INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES ('p3-job', 'op', 'build_aggregate', 'aggregate', 'aggregate-1', 'v1', 'pending', 1, 'now', 'now')`,
		`INSERT INTO memory_spaces(space_id, space_type, external_id, display_name, created_at) VALUES ('p3-user', 'user', 'p3-user', 'P3 User', 'now')`,
	} {
		if _, err := store.writer.Exec(statement); err != nil {
			t.Fatalf("valid Phase 3 enum rejected: %v", err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES ('bad-job', 'op', 'unknown', 'index', 'index', 'v1', 'pending', 1, 'now', 'now')`,
		`INSERT INTO feedback_events(feedback_id, operation_id, caller_id, memory_id, revision_id, feedback_type, reason_code, created_at) VALUES ('bad-feedback', 'op', 'caller', 'missing', 'missing', 'click', 'other', 'now')`,
	} {
		if _, err := store.writer.Exec(statement); err == nil {
			t.Fatalf("invalid Phase 3 enum accepted: %s", statement)
		}
	}
}

func TestP3CONTRACT002AggregateProposalInputsFreezeObservedState(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "p3-aggregate-contract.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	rows, err := store.reader.Query(`PRAGMA table_info(aggregate_proposal_inputs)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]bool{"input_space_id": false, "input_state": false, "input_sensitivity": false}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if _, ok := want[name]; ok {
			want[name] = notNull == 1
		}
	}
	for column, valid := range want {
		if !valid {
			t.Fatalf("aggregate_proposal_inputs.%s is missing or nullable", column)
		}
	}
}

func TestP3CONTRACT002DeferredRevisionFKAndAuthorityGuards(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "p3-guards.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	tx, err := store.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO memory_spaces(space_id, space_type, external_id, display_name, created_at) VALUES ('user', 'user', 'user', 'User', 'now')`,
		`INSERT INTO memories(memory_id, space_id, memory_type, current_revision_id, current_state, created_at, updated_at) VALUES ('memory', 'user', 'aggregate', 'revision', 'superseded', 'now', 'now')`,
		`INSERT INTO memory_revisions(revision_id, memory_id, content, content_hash, created_by, processing_version, created_at) VALUES ('revision', 'memory', 'content', 'hash', 'test', 'v1', 'now')`,
		`INSERT INTO feedback_events(feedback_id, operation_id, caller_id, memory_id, revision_id, feedback_type, reason_code, created_at) VALUES ('feedback', 'op', 'caller', 'memory', 'revision', 'useful', 'answered_query', 'now')`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			tx.Rollback()
			t.Fatalf("deferred initialization statement failed: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("deferred revision FK commit failed: %v", err)
	}
	if _, err := store.writer.Exec(`UPDATE feedback_events SET reason_code='other' WHERE feedback_id='feedback'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("feedback update error = %v; want append-only rejection", err)
	}
}
