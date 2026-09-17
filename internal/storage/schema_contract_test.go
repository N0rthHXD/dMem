package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestDocumentedSchemaMatchesEmbeddedSchema(t *testing.T) {
	documented, err := os.ReadFile(filepath.Join("..", "..", "docs", "storage", "schema-v1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if string(documented) != schemaV1 {
		t.Fatal("docs/storage/schema-v1.sql differs from embedded internal/storage/schema-v1.sql")
	}
}

func TestSchemaInitializationCatalogAndEnums(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "schema-contract.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	for _, statement := range []string{
		`INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES ('aggregate-job', 'op', 'build_aggregate', 'aggregate', 'aggregate-1', 'v1', 'pending', 1, 'now', 'now')`,
		`INSERT INTO memory_spaces(space_id, space_type, external_id, display_name, created_at) VALUES ('schema-user', 'user', 'schema-user', 'Schema User', 'now')`,
	} {
		if _, err := store.writer.Exec(statement); err != nil {
			t.Fatalf("valid schema enum rejected: %v", err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES ('bad-job', 'op', 'unknown', 'index', 'index', 'v1', 'pending', 1, 'now', 'now')`,
		`INSERT INTO feedback_events(feedback_id, operation_id, caller_id, memory_id, revision_id, feedback_purpose, feedback_type, reason_code, created_at) VALUES ('bad-feedback', 'op', 'caller', 'missing', 'missing', 'click_feedback', 'useful', 'other', 'now')`,
	} {
		if _, err := store.writer.Exec(statement); err == nil {
			t.Fatalf("invalid schema enum accepted: %s", statement)
		}
	}
}

func TestAggregateProposalInputsFreezeObservedState(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "aggregate-contract.db"), Options{})
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

func TestDeferredRevisionFKAndAuthorityGuards(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "authority-guards.db"), Options{})
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
		`INSERT INTO memories(memory_id, space_id, current_revision_id, current_state, created_at, updated_at) VALUES ('memory', 'user', 'revision', 'superseded', 'now', 'now')`,
		`INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES ('revision','memory','scene','content','content','hash','normal',0,0,'test','v1','now')`,
		`INSERT INTO feedback_events(feedback_id, operation_id, caller_id, memory_id, revision_id, feedback_purpose, feedback_type, reason_code, created_at) VALUES ('feedback', 'op', 'caller', 'memory', 'revision', 'memory_quality_feedback', 'confirm', 'other', 'now')`,
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

func TestConfigurationRevisionsTable(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "config-revisions.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	wantNotNull := map[string]bool{
		"revision_id": false, "source_identity": false, "config_version": false, "loaded_at": false,
	}
	if err := assertColumns(store, "configuration_revisions", wantNotNull, map[string]bool{"effective_identity": false, "redacted_document": false}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO configuration_revisions(revision_id, source_identity, effective_identity, config_version, loaded_at, redacted_document) VALUES ('rev-1', 'sha256:src', 'sha256:eff', 1, 'now', '{}')`,
		`INSERT INTO configuration_revisions(revision_id, source_identity, config_version, loaded_at) VALUES ('rev-2', 'sha256:src2', 1, 'now')`,
	} {
		if _, err := store.writer.Exec(statement); err != nil {
			t.Fatalf("valid configuration_revisions insert rejected: %v", err)
		}
	}
	if _, err := store.writer.Exec(`INSERT INTO configuration_revisions(revision_id, source_identity, config_version, loaded_at) VALUES ('rev-bad', 'src', 2, 'now')`); err == nil {
		t.Fatal("configuration_revisions accepted config_version other than 1")
	}
}

func TestConfigurationActivationsTable(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "config-activations.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	wantNotNull := map[string]bool{
		"activation_id": false, "trigger": false, "requested_by": false, "outcome": false,
		"started_at": false, "finished_at": false,
	}
	wantNullable := map[string]bool{
		"revision_id": false, "previous_activation_id": false, "error_code": false, "diagnostics": false,
	}
	if err := assertColumns(store, "configuration_activations", wantNotNull, wantNullable); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO configuration_revisions(revision_id, source_identity, config_version, loaded_at) VALUES ('rev-a', 'src', 1, 'now')`,
		`INSERT INTO configuration_activations(activation_id, revision_id, trigger, requested_by, outcome, started_at, finished_at) VALUES ('act-1', 'rev-a', 'startup', 'daemon', 'activated', 'now', 'now')`,
		`INSERT INTO configuration_activations(activation_id, trigger, requested_by, outcome, started_at, finished_at) VALUES ('act-2', 'api', 'operator', 'no_change', 'now', 'now')`,
	} {
		if _, err := store.writer.Exec(statement); err != nil {
			t.Fatalf("valid configuration_activations insert rejected: %v", err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO configuration_activations(activation_id, trigger, requested_by, outcome, started_at, finished_at) VALUES ('act-bad', 'watcher', 'daemon', 'activated', 'now', 'now')`,
		`INSERT INTO configuration_activations(activation_id, trigger, requested_by, outcome, started_at, finished_at) VALUES ('act-bad2', 'api', 'daemon', 'deferred', 'now', 'now')`,
	} {
		if _, err := store.writer.Exec(statement); err == nil {
			t.Fatalf("invalid configuration_activations enum accepted: %s", statement)
		}
	}
}

func TestEffectiveConfigurationStateSingleton(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "effective-config.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	wantNullable := map[string]bool{
		"singleton": false, "activation_id": false, "source_identity": false, "effective_identity": false,
		"activation_generation": false, "activated_at": false, "last_attempt_id": false,
	}
	if err := assertColumns(store, "effective_configuration_state", nil, wantNullable); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`INSERT INTO effective_configuration_state(singleton) VALUES (1)`); err != nil {
		t.Fatalf("seed effective_configuration_state rejected: %v", err)
	}
	if _, err := store.writer.Exec(`INSERT INTO effective_configuration_state(singleton) VALUES (2)`); err == nil {
		t.Fatal("effective_configuration_state accepted second singleton row")
	}
	if _, err := store.writer.Exec(`UPDATE effective_configuration_state SET activation_generation=0`); err == nil {
		t.Fatal("effective_configuration_state accepted non-positive activation_generation")
	}
}

func TestCapabilityStatesTable(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "capability-states.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "capability_states", map[string]bool{
		"capability": false, "enabled": false, "health_state": false, "reconciliation_state": false,
	}, map[string]bool{
		"configuration_identity": false, "configuration_generation": false, "compatibility_identity": false,
		"policy_identity": false, "policy_generation": false, "activation_id": false,
	}); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []string{"processing", "embedding"} {
		if _, err := store.writer.Exec(
			`INSERT INTO capability_states(capability, enabled, health_state, reconciliation_state) VALUES (?, 0, 'disabled', 'idle')`, cap,
		); err != nil {
			t.Fatalf("valid capability_states insert rejected for %s: %v", cap, err)
		}
	}
	if _, err := store.writer.Exec(`INSERT INTO capability_states(capability, enabled, health_state, reconciliation_state) VALUES ('transport', 0, 'disabled', 'idle')`); err == nil {
		t.Fatal("capability_states accepted unknown capability")
	}
	if _, err := store.writer.Exec(`UPDATE capability_states SET health_state='healthy' WHERE capability='processing'`); err == nil {
		t.Fatal("capability_states accepted unknown health_state")
	}
	if _, err := store.writer.Exec(`UPDATE capability_states SET reconciliation_state='paused' WHERE capability='processing'`); err == nil {
		t.Fatal("capability_states accepted unknown reconciliation_state")
	}
}

func TestProviderPoliciesAndAssignmentsTables(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "provider-policies.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "provider_policies", map[string]bool{
		"policy_identity": false, "capability": false, "default_decision": false,
		"allowed_content_types": false, "allowed_source_types": false, "allowed_sensitivities": false,
		"query_allowed": false, "created_at": false,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO provider_policies(policy_identity, capability, default_decision, allowed_content_types, allowed_source_types, allowed_sensitivities, query_allowed, created_at) VALUES ('pol-1', 'processing', 'deny', '[]', '[]', '[]', 0, 'now')`,
	); err != nil {
		t.Fatalf("valid provider_policies insert rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO provider_policies(policy_identity, capability, default_decision, allowed_content_types, allowed_source_types, allowed_sensitivities, query_allowed, created_at) VALUES ('pol-bad', 'processing', 'allow', '[]', '[]', '[]', 0, 'now')`,
	); err == nil {
		t.Fatal("provider_policies accepted default_decision other than deny")
	}
	if err := assertColumns(store, "policy_assignments", map[string]bool{
		"assignment_id": false, "policy_identity": false, "capability": false, "selector_kind": false,
		"effect": false, "include_descendants": false, "priority": false,
	}, map[string]bool{"space_id": false, "space_type": false, "external_id": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO policy_assignments(assignment_id, policy_identity, capability, selector_kind, space_id, effect, include_descendants, priority) VALUES ('asg-1', 'pol-1', 'processing', 'space', 'space-1', 'allow', 1, 0)`,
	); err != nil {
		t.Fatalf("valid policy_assignments insert rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO policy_assignments(assignment_id, policy_identity, capability, selector_kind, effect, include_descendants, priority) VALUES ('asg-bad', 'pol-1', 'processing', 'regex', 'allow', 0, 0)`,
	); err == nil {
		t.Fatal("policy_assignments accepted unknown selector_kind")
	}
}

func TestEffectiveSpacePoliciesTable(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "effective-space-policy.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.writer.Exec(
		`INSERT INTO memory_spaces(space_id, space_type, external_id, display_name, created_at) VALUES ('policy-user', 'user', 'policy-user', 'User', 'now')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := assertColumns(store, "effective_space_policies", map[string]bool{
		"capability": false, "space_id": false, "policy_generation": false, "decision": false,
		"reason_code": false, "evaluated_at": false,
	}, map[string]bool{"source_assignment_id": false, "source_space_id": false, "inheritance_depth": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO effective_space_policies(capability, space_id, policy_generation, decision, reason_code, evaluated_at) VALUES ('processing', 'policy-user', 1, 'deny', 'default_deny', 'now')`,
	); err != nil {
		t.Fatalf("valid effective_space_policies insert rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO effective_space_policies(capability, space_id, policy_generation, decision, reason_code, evaluated_at) VALUES ('processing', 'policy-user', 1, 'allow', 'override', 'now')`,
	); err == nil {
		t.Fatal("effective_space_policies accepted duplicate composite key")
	}
	if _, err := store.writer.Exec(
		`INSERT INTO effective_space_policies(capability, space_id, policy_generation, decision, reason_code, evaluated_at) VALUES ('processing', 'policy-user', 1, 'maybe', 'unknown', 'now')`,
	); err == nil {
		t.Fatal("effective_space_policies accepted unknown decision")
	}
}

func TestBlockedPolicyJobStatuses(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "blocked-policy.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	for _, status := range []string{"blocked_policy", "superseded"} {
		if _, err := store.writer.Exec(
			`INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES (?, 'op', 'embed_memory', 'memory_revision', 'mem', 'v1', ?, 1, 'now', 'now')`,
			"job-"+status, status,
		); err != nil {
			t.Fatalf("jobs rejected new status %s: %v", status, err)
		}
	}
	if err := assertColumns(store, "jobs", map[string]bool{
		"provider_attempt_count": false,
	}, map[string]bool{
		"capability": false, "configuration_generation": false, "policy_generation": false,
		"compatibility_identity": false, "blocked_reason_code": false,
		"reconciliation_run_id": false,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJobAttemptGenerationFields(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "attempt-generations.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "job_attempts", map[string]bool{
		"omitted_context_count": false,
	}, map[string]bool{
		"configuration_generation": false, "policy_generation": false, "effective_decision_id": false,
		"compatibility_identity": false, "external_call_started_at": false, "omission_summary": false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES ('attempt-job', 'op', 'embed_memory', 'memory_revision', 'mem', 'v1', 'pending', 1, 'now', 'now')`,
	); err != nil {
		t.Fatal(err)
	}
	for i, outcome := range []string{"blocked_policy", "stale_generation"} {
		if _, err := store.writer.Exec(
			`INSERT INTO job_attempts(attempt_id, job_id, lease_generation, worker_id, started_at, outcome) VALUES (?, 'attempt-job', ?, 'worker', 'now', ?)`,
			"attempt-"+outcome, i+1, outcome,
		); err != nil {
			t.Fatalf("job_attempts rejected new outcome %s: %v", outcome, err)
		}
	}
}

func TestEmbeddingEligibilityTable(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "embedding-eligibility.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.writer.Exec(
		`INSERT INTO memory_spaces(space_id, space_type, external_id, display_name, created_at) VALUES ('elig-user', 'user', 'elig-user', 'User', 'now')`,
	); err != nil {
		t.Fatal(err)
	}
	tx, err := store.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO memories(memory_id, space_id, current_revision_id, current_state, created_at, updated_at) VALUES ('elig-mem', 'elig-user', 'elig-rev', 'active', 'now', 'now')`,
		`INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES ('elig-rev','elig-mem','scene','content','content','hash','normal',0,0,'test','v1','now')`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			tx.Rollback()
			t.Fatalf("eligibility seed insert failed: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("eligibility seed commit failed: %v", err)
	}
	if err := assertColumns(store, "embedding_eligibility", map[string]bool{
		"memory_id": false, "revision_id": false, "policy_generation": false,
		"eligible": false, "reason_code": false, "evaluated_at": false,
	}, map[string]bool{"space_id": false, "configuration_generation": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO embedding_eligibility(memory_id, revision_id, space_id, policy_generation, eligible, reason_code, evaluated_at) VALUES ('elig-mem', 'elig-rev', 'elig-user', 1, 1, 'allowed', 'now')`,
	); err != nil {
		t.Fatalf("valid embedding_eligibility insert rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO embedding_eligibility(memory_id, revision_id, space_id, policy_generation, eligible, reason_code, evaluated_at) VALUES ('elig-mem', 'elig-rev', 'elig-user', 1, 0, 'dup', 'now')`,
	); err == nil {
		t.Fatal("embedding_eligibility accepted duplicate composite key")
	}
}

func TestSemanticIndexGenerationFields(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "semantic-generations.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "semantic_index_metadata", map[string]bool{
		"eligible_revision_count": false, "embedded_revision_count": false, "excluded_revision_count": false,
	}, map[string]bool{
		"compatibility_identity": false, "policy_generation": false, "corpus_fingerprint": false,
		"created_at": false, "activated_at": false,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryEmbeddingsUsesLiveFloat32BlobStorage(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "semantic-blobs.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "memory_embeddings", map[string]bool{
		"memory_id": false, "revision_id": false, "content_hash": false,
		"compatibility_identity": false, "dimension": false, "vector_blob": false,
	}, map[string]bool{
		"configuration_identity": false, "provider_identity": false,
		"model_identity": false, "processed_at": false,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.reader.Query(`PRAGMA table_info(memory_embeddings)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "index_version" || name == "vector_json" {
			t.Fatalf("obsolete embedding column %q remains", name)
		}
		if name == "vector_blob" && columnType != "BLOB" {
			t.Fatalf("vector_blob type = %q, want BLOB", columnType)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestReconciliationRunsTable(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "reconciliation-runs.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "reconciliation_runs", map[string]bool{
		"run_id": false, "capability": false, "activation_id": false, "change_class": false,
		"state": false, "affected_count": false, "scheduled_count": false, "blocked_count": false,
		"created_at": false,
	}, map[string]bool{
		"from_configuration_generation": false, "to_configuration_generation": false,
		"from_policy_generation": false, "to_policy_generation": false,
		"cursor": false, "scan_high_watermark": false,
		"started_at": false, "updated_at": false, "finished_at": false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO configuration_activations(activation_id, trigger, requested_by, outcome, started_at, finished_at) VALUES ('recon-act', 'api', 'daemon', 'activated', 'now', 'now')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO reconciliation_runs(run_id, capability, activation_id, change_class, state, affected_count, scheduled_count, blocked_count, created_at) VALUES ('run-1', 'embedding', 'recon-act', 'policy_expand', 'pending', 0, 0, 0, 'now')`,
	); err != nil {
		t.Fatalf("valid reconciliation_runs insert rejected: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO reconciliation_runs(run_id, capability, activation_id, change_class, state, affected_count, scheduled_count, blocked_count, created_at) VALUES ('run-bad', 'transport', 'recon-act', 'policy_expand', 'pending', 0, 0, 0, 'now')`,
		`INSERT INTO reconciliation_runs(run_id, capability, activation_id, change_class, state, affected_count, scheduled_count, blocked_count, created_at) VALUES ('run-bad2', 'embedding', 'recon-act', 'full_rebuild', 'pending', 0, 0, 0, 'now')`,
		`INSERT INTO reconciliation_runs(run_id, capability, activation_id, change_class, state, affected_count, scheduled_count, blocked_count, created_at) VALUES ('run-bad3', 'embedding', 'recon-act', 'policy_expand', 'deferred', 0, 0, 0, 'now')`,
	} {
		if _, err := store.writer.Exec(statement); err == nil {
			t.Fatalf("invalid reconciliation_runs enum accepted: %s", statement)
		}
	}
}

func TestConfigurationTablesHaveNoSecretColumns(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "no-secrets.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	for _, table := range []string{
		"configuration_revisions", "configuration_activations", "effective_configuration_state",
		"capability_states", "provider_policies", "policy_assignments", "effective_space_policies",
		"embedding_eligibility", "reconciliation_runs",
	} {
		rows, err := store.reader.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatalf("read %s columns: %v", table, err)
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, dataType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				t.Fatalf("scan %s column info: %v", table, err)
			}
			upper := strings.ToUpper(name)
			if strings.Contains(upper, "SECRET") || strings.Contains(upper, "API_KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "PASSWORD") {
				rows.Close()
				t.Fatalf("%s.%s looks like a secret-bearing column", table, name)
			}
		}
		rows.Close()
	}
}

func seedContractAuthority(t *testing.T, store *Store, suffix string) (spaceID, eventID, memoryID, revisionID string) {
	t.Helper()
	spaceID = "space-" + suffix
	eventID = "event-" + suffix
	memoryID = "memory-" + suffix
	revisionID = "revision-" + suffix
	contentID := "content-" + suffix
	tx, err := store.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		fmt.Sprintf(`INSERT INTO memory_spaces(space_id, space_type, external_id, display_name, created_at) VALUES ('%s', 'user', '%s', 'Contract User', 'now')`, spaceID, spaceID),
		fmt.Sprintf(`INSERT INTO raw_contents(content_id, content_kind, content_text, media_type, content_hash, size_bytes, created_at) VALUES ('%s', 'text', 'hello', 'text/plain', 'hash-%s', 5, 'now')`, contentID, suffix),
		fmt.Sprintf(`INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES ('%s', 'op-%s', '%s', '%s', 'message', 'source', 'caller', 'now', 'now', 'now')`, eventID, suffix, spaceID, contentID),
		fmt.Sprintf(`INSERT INTO memories(memory_id, space_id, current_revision_id, current_state, created_at, updated_at) VALUES ('%s', '%s', '%s', 'active', 'now', 'now')`, memoryID, spaceID, revisionID),
		fmt.Sprintf(`INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES ('%s','%s','scene','content','content','hash-%s','normal',0,0,'test','v1','now')`, revisionID, memoryID, suffix),
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			tx.Rollback()
			t.Fatalf("seed contract authority failed: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("seed contract authority commit failed: %v", err)
	}
	return spaceID, eventID, memoryID, revisionID
}

func TestOrderedEventStreamSchema(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "event-streams.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "source_streams", map[string]bool{
		"source_stream_id": false, "space_id": false, "source_type": false,
		"source_name": false, "source_stream_key": false, "created_at": false,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := assertColumns(store, "raw_events", nil, map[string]bool{
		"source_stream_id": false, "sequence": false,
	}); err != nil {
		t.Fatal(err)
	}
	assertIndex(t, store, "raw_events", "raw_events_stream_sequence")

	space, _, _, _ := seedContractAuthority(t, store, "stream")
	if _, err := store.writer.Exec(
		`INSERT INTO source_streams(source_stream_id, space_id, source_type, source_name, source_stream_key, created_at) VALUES ('stream-1', ?, 'conversation', 'chat', 'chat-1', 'now')`, space,
	); err != nil {
		t.Fatalf("valid source_streams insert rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO source_streams(source_stream_id, space_id, source_type, source_name, source_stream_key, created_at) VALUES ('stream-dup', ?, 'conversation', 'chat', 'chat-1', 'now')`, space,
	); err == nil {
		t.Fatal("source_streams accepted duplicate stream key in the same space")
	}
	if _, err := store.writer.Exec(
		`INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES ('event-stream-2', 'op-stream-2', ?, 'content-stream', 'message', 'source', 'caller', 'now', 'now', 'now')`, space,
	); err != nil {
		t.Fatalf("second stream event insert rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`UPDATE raw_events SET source_stream_id='stream-1', sequence=0 WHERE event_id='event-stream'`,
	); err != nil {
		t.Fatalf("raw_events stream assignment rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`UPDATE raw_events SET source_stream_id='stream-1', sequence=1 WHERE event_id='event-stream-2'`,
	); err != nil {
		t.Fatalf("distinct stream sequence rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`UPDATE raw_events SET sequence=0 WHERE event_id='event-stream-2'`,
	); err == nil {
		t.Fatal("raw_events accepted duplicate sequence within a source stream")
	}
}

func TestStructuredRevisionSchema(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "structured-revisions.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "memory_revisions", map[string]bool{
		"kind": false, "display_content": false, "canonical_content": false, "canonical_hash": false,
		"sensitivity": false, "specificity": false, "confidence": false,
	}, map[string]bool{
		"event_type": false, "time_precision": false, "time_source": false,
		"event_time_start": false, "event_time_end": false, "source_speaker": false,
		"language": false,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, memoryID, _ := seedContractAuthority(t, store, "structured")

	valid := `INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,event_type,event_time_start,event_time_end,time_precision,time_source,source_speaker,language,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES ('rev-ok',?,'event','Alice met Bob','alice met bob','ch-1','meeting','2024-03-01T09:00:00Z','2024-03-01T10:00:00Z','exact','statement','alice','en','normal',0.8,0.9,'test','v1','now')`
	if _, err := store.writer.Exec(valid, memoryID); err != nil {
		t.Fatalf("valid structured revision rejected: %v", err)
	}
	invalidKind := `INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES ('rev-bad-kind',?,'constraint','raw','raw','h-k','normal',0,0,'test','v1','now')`
	if _, err := store.writer.Exec(invalidKind, memoryID); err == nil {
		t.Fatal("memory_revisions accepted unsupported kind")
	}
	invalidPrecision := `INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,time_precision,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES ('rev-bad-precision',?,'fact','raw','raw','h-p','fortnight','normal',0,0,'test','v1','now')`
	if _, err := store.writer.Exec(invalidPrecision, memoryID); err == nil {
		t.Fatal("memory_revisions accepted unsupported time_precision")
	}
	invertedWindow := `INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,event_time_start,event_time_end,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES ('rev-bad-window',?,'event','raw','raw','h-w','2024-03-01T10:00:00Z','2024-03-01T09:00:00Z','normal',0,0,'test','v1','now')`
	if _, err := store.writer.Exec(invertedWindow, memoryID); err == nil {
		t.Fatal("memory_revisions accepted inverted event time window")
	}
}

func TestRevisionAttributesSchema(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "revision-attributes.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "revision_attributes", map[string]bool{
		"revision_id": false, "attribute_key": false, "attribute_value": false, "created_at": false,
	}, nil); err != nil {
		t.Fatal(err)
	}
	_, _, _, revisionID := seedContractAuthority(t, store, "attributes")
	if _, err := store.writer.Exec(
		`INSERT INTO revision_attributes(revision_id, attribute_key, attribute_value, created_at) VALUES (?, 'role', 'engineer', 'now')`, revisionID,
	); err != nil {
		t.Fatalf("valid revision_attributes insert rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO revision_attributes(revision_id, attribute_key, attribute_value, created_at) VALUES (?, 'role', 'manager', 'now')`, revisionID,
	); err == nil {
		t.Fatal("revision_attributes accepted duplicate attribute key")
	}
}

func TestEvidenceRoleSchema(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "evidence-roles.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "memory_evidence", map[string]bool{"role": true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := assertColumns(store, "candidate_evidence", map[string]bool{"role": true}, nil); err != nil {
		t.Fatal(err)
	}
	_, eventID, _, revisionID := seedContractAuthority(t, store, "evidence")
	if _, err := store.writer.Exec(
		`INSERT INTO memory_evidence(evidence_id, revision_id, event_id, role, created_at) VALUES ('ev-context', ?, ?, 'context', 'now')`, revisionID, eventID,
	); err != nil {
		t.Fatalf("valid evidence role rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO memory_evidence(evidence_id, revision_id, event_id, role, created_at) VALUES ('ev-bad', ?, ?, 'bogus', 'now')`, revisionID, eventID,
	); err == nil {
		t.Fatal("memory_evidence accepted unsupported evidence role")
	}
}

func TestDuplicateClusterSchema(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "duplicate-clusters.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "duplicate_clusters", map[string]bool{
		"cluster_id": false, "space_id": false, "representative_memory_id": false,
		"representative_revision_id": false, "generation": false, "member_count": false,
		"algorithm_version": false, "created_at": false, "updated_at": false,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := assertColumns(store, "duplicate_cluster_members", map[string]bool{
		"cluster_id": false, "memory_id": false, "revision_id": false, "reason_code": false, "created_at": false,
	}, map[string]bool{"similarity": false}); err != nil {
		t.Fatal(err)
	}
	space, _, memoryID, revisionID := seedContractAuthority(t, store, "cluster")
	if _, err := store.writer.Exec(
		`INSERT INTO duplicate_clusters(cluster_id, space_id, representative_memory_id, representative_revision_id, member_count, algorithm_version, created_at, updated_at) VALUES ('cluster-1', ?, ?, ?, 1, 'duplicate-v1', 'now', 'now')`, space, memoryID, revisionID,
	); err != nil {
		t.Fatalf("valid duplicate_clusters insert rejected: %v", err)
	}
	if _, err := store.writer.Exec(
		`INSERT INTO duplicate_cluster_members(cluster_id, memory_id, revision_id, reason_code, similarity, created_at) VALUES ('cluster-1', ?, ?, 'same_logical_memory', 0.99, 'now')`, memoryID, revisionID,
	); err != nil {
		t.Fatalf("valid duplicate_cluster_members insert rejected: %v", err)
	}
}

func TestMemoryQualityProjectionSchema(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "memory-quality.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "memory_quality", map[string]bool{
		"memory_id": false, "revision_id": false, "evidence_strength": false,
		"extraction_confidence": false, "specificity": false, "temporal_precision": false,
		"source_diversity": false, "review_status": false, "contradiction_penalty": false,
		"duplicate_density": false, "quality": false, "generation": false,
		"algorithm_version": false, "updated_at": false,
	}, nil); err != nil {
		t.Fatal(err)
	}
	_, _, memoryID, revisionID := seedContractAuthority(t, store, "quality")
	valid := `INSERT INTO memory_quality(memory_id, revision_id, evidence_strength, extraction_confidence, specificity, temporal_precision, source_diversity, review_status, contradiction_penalty, duplicate_density, quality, algorithm_version, updated_at) VALUES (?, ?, 0.9, 0.8, 0.7, 0.6, 0.5, 1.0, 0.1, 0.2, 0.75, 'quality-v1', 'now')`
	if _, err := store.writer.Exec(valid, memoryID, revisionID); err != nil {
		t.Fatalf("valid memory_quality insert rejected: %v", err)
	}
	invalid := `INSERT INTO memory_quality(memory_id, revision_id, evidence_strength, extraction_confidence, specificity, temporal_precision, source_diversity, review_status, contradiction_penalty, duplicate_density, quality, algorithm_version, updated_at) VALUES ('missing', 'missing', 0.9, 0.8, 0.7, 0.6, 0.5, 1.0, 0.1, 0.2, 1.5, 'quality-v1', 'now')`
	if _, err := store.writer.Exec(invalid); err == nil {
		t.Fatal("memory_quality accepted out-of-bounds quality")
	}
}

func TestTypedPredicateAndEventSchema(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "typed-graph.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "predicate_definitions", map[string]bool{
		"predicate": false, "version": false, "subject_types_json": false,
		"object_entity_types_json": false, "direction": false, "cardinality": false,
		"transitivity": false, "temporal_policy": false, "allowed_path_positions_json": false,
		"auto_confirm": false, "created_at": false,
	}, map[string]bool{"object_literal_type": false, "inverse_predicate": false}); err != nil {
		t.Fatal(err)
	}
	if err := assertColumns(store, "event_participants", map[string]bool{
		"event_id": false, "entity_id": false, "participant_role": false, "confidence": false, "created_at": false,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := assertColumns(store, "event_temporal_bindings", map[string]bool{
		"binding_id": false, "event_id": false, "binding_kind": false, "precision": false, "confidence": false, "created_at": false,
	}, map[string]bool{"place_entity_id": false, "interval_start": false, "interval_end": false}); err != nil {
		t.Fatal(err)
	}
	if err := assertColumns(store, "entity_mentions", nil, map[string]bool{"confidence": false}); err != nil {
		t.Fatal(err)
	}
	validPredicate := `INSERT INTO predicate_definitions(predicate, version, subject_types_json, object_entity_types_json, direction, cardinality, transitivity, temporal_policy, allowed_path_positions_json, auto_confirm, created_at) VALUES ('schema_probe', 'v1', '["person"]', '["organization"]', 'directed', 'many', 'none', 'valid_time', '["first","second"]', 'exact_match', 'now')`
	if _, err := store.writer.Exec(validPredicate); err != nil {
		t.Fatalf("valid predicate_definitions insert rejected: %v", err)
	}
	invalidDirection := `INSERT INTO predicate_definitions(predicate, version, subject_types_json, object_entity_types_json, direction, cardinality, transitivity, temporal_policy, allowed_path_positions_json, auto_confirm, created_at) VALUES ('bad_dir', 'v1', '["person"]', '["organization"]', 'sideways', 'many', 'none', 'valid_time', '[]', 'never', 'now')`
	if _, err := store.writer.Exec(invalidDirection); err == nil {
		t.Fatal("predicate_definitions accepted unsupported direction")
	}
}

func TestCurrentSchemaPredicateSnapshot(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "predicate-snapshot.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	rows, err := store.reader.Query(`SELECT predicate,description FROM predicates ORDER BY predicate`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	descriptions := map[string]string{}
	for rows.Next() {
		var predicate, description string
		if err := rows.Scan(&predicate, &description); err != nil {
			t.Fatal(err)
		}
		got = append(got, predicate)
		descriptions[predicate] = description
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := domain.PredicateNames()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("schema predicates = %v, want %v", got, want)
	}
	for _, definition := range domain.PredicateDefinitions() {
		if descriptions[definition.Name] != definition.Description {
			t.Fatalf("schema predicate %q description = %q, want %q", definition.Name, descriptions[definition.Name], definition.Description)
		}
	}
}

func TestTypedGraphAuthorityAndBroadRelationRemoval(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "typed-graph-authority.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "entity_facts", map[string]bool{
		"fact_id": false, "space_id": false, "subject_entity_id": false, "predicate": false,
		"source_memory_id": false, "source_revision_id": false, "state": false, "source": false,
		"specification_hash": false, "created_by": false, "processing_version": false,
		"created_at": false, "updated_at": false,
	}, map[string]bool{
		"object_entity_id": false, "object_literal": false, "object_literal_type": false,
		"confidence": false, "valid_from": false, "valid_until": false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := assertColumns(store, "entity_fact_evidence", map[string]bool{
		"evidence_id": false, "fact_id": false, "evidence_type": false, "created_by": false, "created_at": false,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := assertColumns(store, "relation_evidence", map[string]bool{
		"evidence_id": false, "relation_id": false, "evidence_type": false, "created_by": false, "created_at": false,
	}, nil); err != nil {
		t.Fatal(err)
	}

	sourceSpace, _, _, _ := seedContractAuthority(t, store, "relation-source")
	tx, err := store.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO memories(memory_id, space_id, current_revision_id, current_state, created_at, updated_at) VALUES ('memory-relation-target', '` + sourceSpace + `', 'revision-relation-target', 'active', 'now', 'now')`,
		`INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES ('revision-relation-target','memory-relation-target','scene','content','content','hash-relation-target','normal',0,0,'test','v1','now')`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			tx.Rollback()
			t.Fatalf("seed relation target failed: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	relation := `INSERT INTO memory_relations(relation_id, space_id, relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, source, specification_hash, created_by, processing_version, created_at, updated_at) VALUES ('relation-broad', ?, ?, 'memory-relation-source', 'revision-relation-source', 'memory-relation-target', 'revision-relation-target', 'human_confirmation', 'spec', 'test', 'v1', 'now', 'now')`
	if _, err := store.writer.Exec(relation, sourceSpace, "supports"); err != nil {
		t.Fatalf("typed relation insert rejected: %v", err)
	}
	if _, err := store.writer.Exec(relation, sourceSpace, "free_association"); err == nil {
		t.Fatal("memory_relations accepted an unregistered relation type")
	}
	proposal := `INSERT INTO relation_proposals(proposal_id, event_id, space_id, relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, evidence_json, processor_identity, processing_version, specification_hash, created_at) VALUES ('proposal-broad', 'event-relation-source', ?, ?, 'memory-relation-source', 'revision-relation-source', 'memory-relation-target', 'revision-relation-target', '[]', 'processor', 'v1', 'spec', 'now')`
	if _, err := store.writer.Exec(proposal, sourceSpace, "free_association"); err == nil {
		t.Fatal("relation_proposals accepted an unregistered relation type")
	}
}

func TestQueryTelemetrySchema(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "query-telemetry.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "query_telemetry", map[string]bool{
		"telemetry_id": false, "operation_id": false, "query_plan_version": false, "query_plan_identity": false,
		"query_hash": false, "query_type": false, "requested_scope_json": false, "reference_time": false,
		"candidate_budget": false, "context_budget": false,
		"requested_capabilities_json": false, "executed_capabilities_json": false,
		"candidate_counts_json": false, "rejection_counts_json": false, "score_summary_json": false,
		"graph_counts_json": false, "duplicate_cluster_count": false, "context_tokens": false,
		"context_chars": false, "ranking_identity": false, "context_identity": false, "feedback_identity": false,
		"degradation_reasons_json": false, "phase_latency_ms_json": false, "created_at": false,
	}, nil); err != nil {
		t.Fatal(err)
	}
	valid := `INSERT INTO query_telemetry(telemetry_id, operation_id, query_plan_version, query_plan_identity, query_hash, query_type, requested_scope_json, reference_time, candidate_budget, context_budget, requested_capabilities_json, executed_capabilities_json, candidate_counts_json, rejection_counts_json, score_summary_json, graph_counts_json, duplicate_cluster_count, context_tokens, context_chars, ranking_identity, context_identity, feedback_identity, degradation_reasons_json, phase_latency_ms_json, created_at) VALUES ('tel-1', 'op-1', 'test-plan', 'sha256:p', 'sha256:q', 'lookup', '["space-1"]', '2026-01-01T00:00:00Z', 20, 4000, '["semantic","keyword"]', '["semantic","keyword"]', '{"semantic":10,"keyword":5}', '{"scope":1}', '{"max":0.9}', '{"seeds":2}', 1, 128, 512, 'sha256:r', 'sha256:c', 'feedback-v2', '[]', '{"plan":0.1}', 'now')`
	if _, err := store.writer.Exec(valid); err != nil {
		t.Fatalf("valid query_telemetry insert rejected: %v", err)
	}
}

func TestContextDecisionSchema(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "context-decisions.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := assertColumns(store, "context_decisions", map[string]bool{
		"decision_id": false, "operation_id": false, "memory_id": false, "revision_id": false,
		"selection_state": false, "selection_reason": false, "context_identity": false,
		"estimated_tokens": false, "created_at": false,
	}, map[string]bool{"result_rank": false, "duplicate_cluster_id": false}); err != nil {
		t.Fatal(err)
	}
	_, _, memoryID, revisionID := seedContractAuthority(t, store, "context")
	valid := `INSERT INTO context_decisions(decision_id, operation_id, memory_id, revision_id, result_rank, selection_state, selection_reason, context_identity, estimated_tokens, created_at) VALUES ('dec-1', 'op-1', ?, ?, 1, 'selected', 'primary_relevance', 'sha256:c', 64, 'now')`
	if _, err := store.writer.Exec(valid, memoryID, revisionID); err != nil {
		t.Fatalf("valid context_decisions insert rejected: %v", err)
	}
	invalid := `INSERT INTO context_decisions(decision_id, operation_id, memory_id, revision_id, selection_state, selection_reason, context_identity, estimated_tokens, created_at) VALUES ('dec-bad', 'op-1', ?, ?, 'maybe', 'primary_relevance', 'sha256:c', 64, 'now')`
	if _, err := store.writer.Exec(invalid, memoryID, revisionID); err == nil {
		t.Fatal("context_decisions accepted unsupported selection_state")
	}
}

func TestMemoryKindEnumEnforcement(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "kind-enums.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	space, eventID, _, _ := seedContractAuthority(t, store, "kind-enums")
	for i, kind := range []string{"fact", "event", "preference", "plan", "procedure", "scene", "topic", "timeline"} {
		candidate := `INSERT INTO candidates(candidate_id, event_id, space_id, memory_type, structured_revision_json, processor_identity, processing_version, specification_hash, created_at) VALUES (?, ?, ?, ?, '{"kind":"fact"}', 'processor', 'v1', ?, 'now')`
		if _, err := store.writer.Exec(candidate, fmt.Sprintf("candidate-kind-%d", i), eventID, space, kind, fmt.Sprintf("candidate-spec-%d", i)); err != nil {
			t.Fatalf("candidates rejected replacement kind %q: %v", kind, err)
		}
		proposal := `INSERT INTO evolution_proposals(proposal_id, event_id, space_id, action, proposed_content, proposed_content_hash, memory_type, processor_identity, processing_version, specification_hash, created_at) VALUES (?, ?, ?, 'supplement', 'content', 'hash', ?, 'processor', 'v1', ?, 'now')`
		if _, err := store.writer.Exec(proposal, fmt.Sprintf("proposal-kind-%d", i), eventID, space, kind, fmt.Sprintf("proposal-spec-%d", i)); err != nil {
			t.Fatalf("evolution_proposals rejected replacement kind %q: %v", kind, err)
		}
	}
	for i, invalid := range []string{"not-a-kind"} {
		candidate := `INSERT INTO candidates(candidate_id, event_id, space_id, memory_type, structured_revision_json, processor_identity, processing_version, specification_hash, created_at) VALUES (?, ?, ?, ?, '{"kind":"fact"}', 'processor', 'v1', ?, 'now')`
		if _, err := store.writer.Exec(candidate, fmt.Sprintf("candidate-invalid-%d", i), eventID, space, invalid, fmt.Sprintf("candidate-invalid-spec-%d", i)); err == nil {
			t.Fatalf("candidates accepted invalid memory_type %q", invalid)
		}
		proposal := `INSERT INTO evolution_proposals(proposal_id, event_id, space_id, action, proposed_content, proposed_content_hash, memory_type, processor_identity, processing_version, specification_hash, created_at) VALUES (?, ?, ?, 'supplement', 'content', 'hash', ?, 'processor', 'v1', ?, 'now')`
		if _, err := store.writer.Exec(proposal, fmt.Sprintf("proposal-invalid-%d", i), eventID, space, invalid, fmt.Sprintf("proposal-invalid-spec-%d", i)); err == nil {
			t.Fatalf("evolution_proposals accepted invalid memory_type %q", invalid)
		}
	}
}

func TestCandidateStructuredRevisionIsRequired(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "candidate-revision.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	space, eventID, _, _ := seedContractAuthority(t, store, "candidate-revision")
	insert := `INSERT INTO candidates(candidate_id, event_id, space_id, memory_type, structured_revision_json, processor_identity, processing_version, specification_hash, created_at) VALUES (?, ?, ?, 'fact', ?, 'processor', 'v1', ?, 'now')`
	for i, encoded := range []string{"", "{}"} {
		if _, err := store.writer.Exec(insert, fmt.Sprintf("candidate-revision-%d", i), eventID, space, encoded, fmt.Sprintf("candidate-revision-spec-%d", i)); err == nil {
			t.Fatalf("candidates accepted structured revision %q", encoded)
		}
	}
}

func assertIndex(t *testing.T, store *Store, table, index string) {
	t.Helper()
	rows, err := store.reader.Query(`PRAGMA index_list(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if name == index {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("index %s on %s is missing", index, table)
}

func assertColumns(store *Store, table string, notNull map[string]bool, nullable map[string]bool) error {
	rows, err := store.reader.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return fmt.Errorf("read %s columns: %w", table, err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var cid, notNullVal, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNullVal, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan %s column info: %w", table, err)
		}
		seen[name] = true
		if _, ok := notNull[name]; ok {
			if notNullVal != 1 {
				return fmt.Errorf("%s.%s is nullable but must be NOT NULL", table, name)
			}
			notNull[name] = true
		}
		if _, ok := nullable[name]; ok {
			nullable[name] = true
		}
	}
	for col, found := range notNull {
		if !found {
			return fmt.Errorf("%s.%s is missing", table, col)
		}
	}
	for col, found := range nullable {
		if !found {
			return fmt.Errorf("%s.%s is missing", table, col)
		}
	}
	return nil
}
