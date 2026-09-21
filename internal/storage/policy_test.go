package storage

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestDisabledCapabilityWithoutProviderPolicyResolvesAsNoPolicy pins the default
// deployment shape: a capability without a configured provider keeps NULL policy
// identity and generation, and every reader must treat that as an absent policy
// instead of failing the scan.
func TestDisabledCapabilityWithoutProviderPolicyResolvesAsNoPolicy(t *testing.T) {
	repo, _ := newMemoryTestRepository(t)
	commitPolicy(t, repo, nil)

	var identity sql.NullString
	var generation sql.NullInt64
	if err := repo.store.reader.QueryRowContext(context.Background(), `SELECT policy_identity, policy_generation FROM capability_states WHERE capability='embedding'`).Scan(&identity, &generation); err != nil {
		t.Fatal(err)
	}
	if identity.Valid || generation.Valid {
		t.Fatalf("embedding capability state = %v/%v; want NULL/NULL", identity, generation)
	}

	state, err := repo.currentCapabilityState(context.Background(), repo.store.reader, "embedding")
	if err != nil || state.Enabled || state.PolicyIdentity != "" || state.PolicyGeneration != 0 {
		t.Fatalf("currentCapabilityState() = %+v, err = %v", state, err)
	}
	decision, err := repo.EffectivePolicy(context.Background(), "embedding", "space-1")
	if err != nil || decision.Decision != "deny" || decision.ReasonCode != "default_deny" {
		t.Fatalf("EffectivePolicy() = %+v, err = %v", decision, err)
	}
	if err := repo.RequireSemanticQueryPolicy(context.Background(), "space-1"); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("RequireSemanticQueryPolicy() error = %v; want %v", err, ErrPolicyDenied)
	}
}

func TestEffectivePolicyResolution(t *testing.T) {
	repo := newConfigTestRepository(t)
	commitPolicy(t, repo, []PolicyAssignmentRecord{
		{AssignmentID: "root-allow", PolicyIdentity: "policy-1", Capability: "processing", SelectorKind: "space", SpaceID: "root", Effect: "allow", IncludeDescendants: true},
		{AssignmentID: "task-allow", PolicyIdentity: "policy-1", Capability: "processing", SelectorKind: "space", SpaceID: "task", Effect: "allow"},
		{AssignmentID: "task-deny", PolicyIdentity: "policy-1", Capability: "processing", SelectorKind: "space", SpaceID: "task", Effect: "deny"},
	})
	insertPolicySpace(t, repo, "root", "", "user", "owner")
	insertPolicySpace(t, repo, "project", "root", "project", "project-a")
	insertPolicySpace(t, repo, "task", "project", "task", "denied")

	project, err := repo.EffectivePolicy(context.Background(), "processing", "project")
	if err != nil || project.Decision != "allow" || project.InheritanceDepth == nil || *project.InheritanceDepth != 1 {
		t.Fatalf("project policy = %+v, err=%v", project, err)
	}
	task, err := repo.EffectivePolicy(context.Background(), "processing", "task")
	if err != nil || task.Decision != "deny" || task.ReasonCode != "space_assignment" {
		t.Fatalf("task policy = %+v, err=%v", task, err)
	}
}

func TestCreateSpacePersistsCurrentPolicy(t *testing.T) {
	repo := newConfigTestRepository(t)
	commitPolicy(t, repo, []PolicyAssignmentRecord{{AssignmentID: "project-allow", PolicyIdentity: "policy-1", Capability: "processing", SelectorKind: "space_type", SpaceType: "project", Effect: "allow", Priority: 1}})
	root, _, err := repo.CreateSpace(context.Background(), policyWriteIdentity("root"), CreateSpaceParams{SpaceType: "user", ExternalID: "owner", DisplayName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := repo.CreateSpace(context.Background(), policyWriteIdentity("project"), CreateSpaceParams{ParentSpaceID: &root.SpaceID, SpaceType: "project", ExternalID: "p", DisplayName: "Project"})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := repo.EffectivePolicy(context.Background(), "processing", project.SpaceID)
	if err != nil || decision.Decision != "allow" || decision.PolicyGeneration != 1 {
		t.Fatalf("policy = %+v, err=%v", decision, err)
	}
	var persisted string
	if err := repo.store.reader.QueryRowContext(context.Background(), `SELECT decision FROM effective_space_policies WHERE capability='processing' AND space_id=? AND policy_generation=1`, project.SpaceID).Scan(&persisted); err != nil || persisted != "allow" {
		t.Fatalf("persisted policy = %q, err=%v", persisted, err)
	}
}

func TestConcurrentSpaceCreationUsesOnePolicyGeneration(t *testing.T) {
	repo := newConfigTestRepository(t)
	commitPolicy(t, repo, []PolicyAssignmentRecord{{AssignmentID: "project-allow", PolicyIdentity: "policy-1", Capability: "processing", SelectorKind: "space_type", SpaceType: "project", Effect: "allow", Priority: 1}})
	root, _, err := repo.CreateSpace(context.Background(), policyWriteIdentity("root"), CreateSpaceParams{SpaceType: "user", ExternalID: "owner", DisplayName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := repo.CreateSpace(context.Background(), policyWriteIdentity(string(rune('a'+i))), CreateSpaceParams{ParentSpaceID: &root.SpaceID, SpaceType: "project", ExternalID: string(rune('a' + i)), DisplayName: "Project"})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := repo.store.reader.QueryRowContext(context.Background(), `SELECT count(*) FROM effective_space_policies WHERE capability='processing' AND policy_generation=1 AND decision='allow'`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("allowed policies = %d, err=%v", count, err)
	}
}

func commitPolicy(t *testing.T, repo *Repository, assignments []PolicyAssignmentRecord) {
	t.Helper()
	now := time.Now().UTC()
	err := repo.CommitActivation(context.Background(), ActivationCommit{
		Revision:     RevisionRecord{RevisionID: "revision-1", SourceIdentity: "source-1", EffectiveIdentity: "effective-1", ConfigVersion: 1, LoadedAt: now},
		Activation:   ActivationRecord{ActivationID: "activation-1", RevisionID: "revision-1", Trigger: "startup", RequestedBy: "test", Outcome: "activated", StartedAt: now, FinishedAt: now},
		Capabilities: []CapabilityStateRecord{{Capability: "processing", Enabled: true, ConfigurationIdentity: "config-1", ConfigurationGeneration: 1, CompatibilityIdentity: "compat-1", PolicyIdentity: "policy-1", PolicyGeneration: 1, HealthState: "ready", ReconciliationState: "idle"}, {Capability: "embedding", Enabled: false, HealthState: "disabled", ReconciliationState: "idle"}},
		Policies:     []ProviderPolicyRecord{{PolicyIdentity: "policy-1", Capability: "processing", DefaultDecision: "deny", AllowedContentTypes: []string{"text/plain"}, AllowedSourceTypes: []string{"manual"}, AllowedSensitivities: []string{"normal"}, CreatedAt: now}},
		Assignments:  assignments,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertPolicySpace(t *testing.T, repo *Repository, id, parent, kind, externalID string) {
	t.Helper()
	var parentValue any
	if parent != "" {
		parentValue = parent
	}
	if _, err := repo.store.writer.Exec(`INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES (?, ?, ?, ?, ?, ?)`, id, parentValue, kind, externalID, id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func policyWriteIdentity(key string) WriteIdentity {
	return WriteIdentity{CallerID: "test", IdempotencyKey: key, RequestID: key, Method: "POST", Route: "/v1/spaces", RequestHash: key}
}
