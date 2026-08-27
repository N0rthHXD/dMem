package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		`INSERT INTO feedback_events(feedback_id, operation_id, caller_id, memory_id, revision_id, feedback_type, reason_code, created_at) VALUES ('bad-feedback', 'op', 'caller', 'missing', 'missing', 'click', 'other', 'now')`,
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
		`INSERT INTO memories(memory_id, space_id, memory_type, current_revision_id, current_state, created_at, updated_at) VALUES ('elig-mem', 'elig-user', 'aggregate', 'elig-rev', 'active', 'now', 'now')`,
		`INSERT INTO memory_revisions(revision_id, memory_id, content, content_hash, created_by, processing_version, created_at) VALUES ('elig-rev', 'elig-mem', 'content', 'hash', 'test', 'v1', 'now')`,
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
