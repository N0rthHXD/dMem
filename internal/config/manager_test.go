package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/N0rthHXD/dMem/internal/provider"
)

type testConfigBuilder struct {
	dataDir         string
	databaseFile    string
	workerCount     int
	processing      bool
	processingModel string
	embedding       bool
	embeddingModel  string
	embeddingDims   int
}

func (b testConfigBuilder) String() string {
	if b.databaseFile == "" {
		b.databaseFile = "dmem.db"
	}
	if b.workerCount == 0 {
		b.workerCount = 2
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "config_version = 1\n\n[instance]\ndata_dir = %q\ndatabase_filename = %q\n\n", b.dataDir, b.databaseFile)
	sb.WriteString("[server]\nshutdown_grace_period = \"15s\"\n\n")
	fmt.Fprintf(&sb, "[jobs]\nworker_count = %d\nqueue_soft_limit = 10000\nqueue_hard_limit = 20000\nmax_attempts = 3\nlease_duration = \"30s\"\nper_job_timeout = \"2m\"\nretry_initial_backoff = \"1s\"\nretry_max_backoff = \"5m\"\n\n", b.workerCount)
	sb.WriteString("[storage]\ndisk_soft_free_bytes = 1073741824\ndisk_hard_free_bytes = 268435456\n\n")
	if b.processing {
		model := b.processingModel
		if model == "" {
			model = "model-a"
		}
		fmt.Fprintf(&sb, "[processing]\nenabled = true\n\n[processing.provider]\nkind = \"openai-compatible\"\nendpoint = \"https://provider.example/v1/chat/completions\"\nmodel = %q\ntelemetry_disabled = true\n\n", model)
		sb.WriteString("[processing.provider.secret]\nsource = \"environment\"\nname = \"DMEM_PROCESSING_API_KEY\"\n\n[processing.provider.limits]\nmax_egress_input_chars = 12000\nmax_aggregate_input_chars = 48000\nmax_context_items = 8\nmax_candidate_chars = 12000\nmax_total_candidate_chars = 48000\nmax_response_bytes = 1048576\nrequest_timeout = \"30s\"\nmax_concurrent = 2\nrequests_per_minute = 60\n\n[processing.provider.policy]\ndefault = \"deny\"\ncontent_types = [\"application/json\", \"text/plain\"]\nsource_types = [\"manual\", \"message\"]\nsensitivities = [\"normal\"]\n\n[[processing.provider.policy.assignments]]\nselector = \"space\"\nspace_id = \"11111111-1111-4111-8111-111111111111\"\neffect = \"allow\"\ninclude_descendants = true\n\n")
	} else {
		sb.WriteString("[processing]\nenabled = false\n\n")
	}
	if b.embedding {
		model := b.embeddingModel
		if model == "" {
			model = "embedding-a"
		}
		dims := b.embeddingDims
		if dims == 0 {
			dims = 1024
		}
		fmt.Fprintf(&sb, "[embedding]\nenabled = true\n\n[embedding.provider]\nkind = \"openai-compatible\"\nendpoint = \"https://provider.example/v1/embeddings\"\nmodel = %q\ntelemetry_disabled = true\n\n", model)
		fmt.Fprintf(&sb, "[embedding.provider.secret]\nsource = \"environment\"\nname = \"DMEM_EMBEDDING_API_KEY\"\n\n[embedding.provider.options]\ndimensions = %d\nquery_instructions = \"query: \"\n\n", dims)
		sb.WriteString("[embedding.provider.limits]\nmax_egress_input_chars = 12000\nmax_response_bytes = 1048576\nrequest_timeout = \"30s\"\nmax_concurrent = 2\nrequests_per_minute = 60\n\n[embedding.provider.policy]\ndefault = \"deny\"\nquery = \"allow\"\ncontent_types = [\"text/plain\"]\nsource_types = [\"manual\", \"message\"]\nsensitivities = [\"normal\"]\n\n[[embedding.provider.policy.assignments]]\nselector = \"space_type\"\nspace_type = \"project\"\neffect = \"allow\"\npriority = 100\n\n")
	} else {
		sb.WriteString("[embedding]\nenabled = false\n\n")
	}
	return sb.String()
}

func reloadWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
}

func newManagerConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.toml")
}

func reloadEnabledManager(t *testing.T, path string) *Manager {
	t.Helper()
	data := t.TempDir()
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data}.String())
	m := NewManager(path, nil)
	r := m.Reload(context.Background(), TriggerStartup)
	if r.Outcome != OutcomeActivated {
		t.Fatalf("startup reload: outcome = %s, error = %s", r.Outcome, r.ErrorCode)
	}
	return m
}

func TestManager_ActivatedOutcome(t *testing.T) {
	path := newManagerConfigPath(t)
	data := t.TempDir()
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data}.String())
	m := NewManager(path, nil)

	r1 := m.Reload(context.Background(), TriggerStartup)
	if r1.Outcome != OutcomeActivated {
		t.Fatalf("r1 outcome = %s, want activated (error %s)", r1.Outcome, r1.ErrorCode)
	}
	if r1.AttemptID == "" {
		t.Error("r1 AttemptID is empty; activation attempts must be identifiable")
	}
	if r1.EffectiveIdentity == "" {
		t.Error("r1 EffectiveIdentity is empty")
	}
	if m.ActivationGeneration() != 1 {
		t.Errorf("generation after r1 = %d, want 1", m.ActivationGeneration())
	}

	reloadWriteFile(t, path, testConfigBuilder{dataDir: data, processing: true}.String())
	r2 := m.Reload(context.Background(), TriggerAPI)
	if r2.Outcome != OutcomeActivated {
		t.Fatalf("r2 outcome = %s, want activated (error %s)", r2.Outcome, r2.ErrorCode)
	}
	if r2.AttemptID == "" || r2.AttemptID == r1.AttemptID {
		t.Error("r2 AttemptID must be a new non-empty identifier")
	}
	if len(r2.ChangedCapabilities) != 1 || r2.ChangedCapabilities[0] != "processing" {
		t.Errorf("r2 changed = %v, want [processing]", r2.ChangedCapabilities)
	}
	if r2.PreviousEffectiveIdentity != r1.EffectiveIdentity {
		t.Error("r2 PreviousEffectiveIdentity must equal r1 effective identity")
	}
	if m.ActivationGeneration() != 2 {
		t.Errorf("generation after r2 = %d, want 2", m.ActivationGeneration())
	}
	snap := m.Snapshot()
	if snap == nil || snap.ActivationGeneration != 2 {
		t.Error("current snapshot must reflect generation 2")
	}
}

func TestCapabilityDiffClassPolicyChangeUsesSchemaSupportedClass(t *testing.T) {
	next := ProviderConfig{
		Enabled: true, Kind: provider.KindOpenAICompatible, Endpoint: "https://provider.example/v1/chat/completions", Model: "model", TelemetryDisabled: true,
		Secret: provider.SecretReference{Source: "environment", Name: "DMEM_TEST_SECRET"},
		Policy: provider.Policy{Default: "deny", ContentTypes: []string{"text/plain"}, SourceTypes: []string{"manual"}, Sensitivities: []string{"normal"}, Assignments: []provider.PolicyAssignment{{Selector: "space", SpaceID: "space", Effect: "allow"}}},
	}
	previous := CapabilitySnapshot{
		Enabled:               true,
		ConfigurationIdentity: CapabilityIdentity(next),
		CompatibilityIdentity: CompatibilityIdentity(next, true),
		PolicyIdentity:        "policy-before",
	}
	if got := capabilityDiffClass("processing", previous, next); got != "processing:policy_narrow" {
		t.Fatalf("policy diff class = %q; want processing:policy_narrow", got)
	}
}

func TestManager_NoChangeOutcome(t *testing.T) {
	path := newManagerConfigPath(t)
	data := t.TempDir()
	content := testConfigBuilder{dataDir: data}.String()
	reloadWriteFile(t, path, content)
	m := NewManager(path, nil)

	r1 := m.Reload(context.Background(), TriggerStartup)
	if r1.Outcome != OutcomeActivated {
		t.Fatalf("startup: outcome = %s", r1.Outcome)
	}
	gen := m.ActivationGeneration()

	// Rewrite identical bytes with extra comments/formatting.
	reloadWriteFile(t, path, "# leading comment\n"+content)
	r2 := m.Reload(context.Background(), TriggerAPI)
	if r2.Outcome != OutcomeNoChange {
		t.Fatalf("r2 outcome = %s, want no_change (error %s)", r2.Outcome, r2.ErrorCode)
	}
	if len(r2.ChangedCapabilities) != 0 {
		t.Errorf("no_change changed = %v, want none", r2.ChangedCapabilities)
	}
	if m.ActivationGeneration() != gen {
		t.Errorf("generation changed on no_change: %d -> %d", gen, m.ActivationGeneration())
	}
	if r2.PreviousEffectiveIdentity != r1.EffectiveIdentity {
		t.Error("no_change PreviousEffectiveIdentity should equal prior effective identity")
	}
}

func TestManager_RejectedOutcome(t *testing.T) {
	path := newManagerConfigPath(t)
	data := t.TempDir()
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data}.String())
	m := NewManager(path, nil)
	if r := m.Reload(context.Background(), TriggerStartup); r.Outcome != OutcomeActivated {
		t.Fatalf("startup: outcome = %s", r.Outcome)
	}
	gen := m.ActivationGeneration()
	prior := m.Snapshot()

	// Write an invalid config (bad version).
	reloadWriteFile(t, path, strings.Replace(testConfigBuilder{dataDir: data}.String(), "config_version = 1", "config_version = 2", 1))
	r := m.Reload(context.Background(), TriggerAPI)
	if r.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %s, want rejected", r.Outcome)
	}
	if r.ErrorCode == "" {
		t.Error("rejected reload must report an error code")
	}
	if m.ActivationGeneration() != gen {
		t.Errorf("generation changed on rejection: %d -> %d", gen, m.ActivationGeneration())
	}
	if m.Snapshot() != prior {
		t.Error("rejected reload must not replace the current snapshot pointer")
	}
}

func TestManager_RestartRequiredRejected(t *testing.T) {
	path := newManagerConfigPath(t)
	data := t.TempDir()
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data}.String())
	m := NewManager(path, nil)
	if r := m.Reload(context.Background(), TriggerStartup); r.Outcome != OutcomeActivated {
		t.Fatalf("startup: outcome = %s", r.Outcome)
	}
	gen := m.ActivationGeneration()
	prior := m.Snapshot()
	priorEffective := prior.Config.EffectiveIdentity

	// Change a restart-bound field (data_dir).
	otherData := t.TempDir()
	reloadWriteFile(t, path, testConfigBuilder{dataDir: otherData}.String())
	r := m.Reload(context.Background(), TriggerAPI)
	if r.Outcome != OutcomeRejected {
		t.Fatalf("outcome = %s, want rejected for restart-bound change", r.Outcome)
	}
	if r.ErrorCode != "config_restart_required" {
		t.Errorf("error code = %q, want config_restart_required", r.ErrorCode)
	}
	if m.ActivationGeneration() != gen {
		t.Errorf("generation changed on restart-required rejection: %d -> %d", gen, m.ActivationGeneration())
	}
	if m.Snapshot() != prior {
		t.Error("restart-required rejection must preserve the previous snapshot")
	}
	if m.Snapshot().Config.EffectiveIdentity != priorEffective {
		t.Error("effective identity must be unchanged after restart-required rejection")
	}

	// Changing worker_count is also restart-bound.
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data, workerCount: 4}.String())
	r2 := m.Reload(context.Background(), TriggerAPI)
	if r2.Outcome != OutcomeRejected || r2.ErrorCode != "config_restart_required" {
		t.Fatalf("worker_count change: outcome=%s code=%q, want rejected/config_restart_required", r2.Outcome, r2.ErrorCode)
	}
	if m.ActivationGeneration() != gen {
		t.Errorf("generation changed after worker_count rejection: %d -> %d", gen, m.ActivationGeneration())
	}
}

func TestManager_PreviousSnapshotPreserved(t *testing.T) {
	path := newManagerConfigPath(t)
	data := t.TempDir()
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data, processing: true}.String())
	m := NewManager(path, nil)
	if r := m.Reload(context.Background(), TriggerStartup); r.Outcome != OutcomeActivated {
		t.Fatalf("startup: outcome = %s", r.Outcome)
	}
	prior := m.Snapshot()
	priorGen := m.ActivationGeneration()
	priorProc := prior.ProcessingCapability.ConfigurationIdentity

	// Remove the config file so reload fails.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove config: %v", err)
	}
	r := m.Reload(context.Background(), TriggerAPI)
	if r.Outcome != OutcomeRejected && r.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %s, want rejected or failed", r.Outcome)
	}
	if m.Snapshot() != prior {
		t.Error("failed reload must not replace the current snapshot pointer")
	}
	if m.ActivationGeneration() != priorGen {
		t.Errorf("generation changed on failed reload: %d -> %d", priorGen, m.ActivationGeneration())
	}
	if got := m.Snapshot().ProcessingCapability.ConfigurationIdentity; got != priorProc {
		t.Errorf("processing capability changed after failed reload: %q != %q", got, priorProc)
	}
}

func TestManager_IndependentEmbeddingEnable(t *testing.T) {
	path := newManagerConfigPath(t)
	data := t.TempDir()
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data}.String())
	m := NewManager(path, nil)
	m.Reload(context.Background(), TriggerStartup)

	// Enable only embedding.
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data, embedding: true}.String())
	r := m.Reload(context.Background(), TriggerAPI)
	if r.Outcome != OutcomeActivated {
		t.Fatalf("outcome = %s, want activated", r.Outcome)
	}
	if len(r.ChangedCapabilities) != 1 || r.ChangedCapabilities[0] != "embedding" {
		t.Errorf("changed = %v, want [embedding]", r.ChangedCapabilities)
	}
	snap := m.Snapshot()
	if !snap.EmbeddingCapability.Enabled {
		t.Error("embedding capability should be enabled")
	}
	if snap.ProcessingCapability.Enabled {
		t.Error("processing capability should remain disabled")
	}
}

func TestManager_DisableCapability(t *testing.T) {
	path := newManagerConfigPath(t)
	data := t.TempDir()
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data, processing: true}.String())
	m := NewManager(path, nil)
	m.Reload(context.Background(), TriggerStartup)

	// Disable processing.
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data}.String())
	r := m.Reload(context.Background(), TriggerAPI)
	if r.Outcome != OutcomeActivated {
		t.Fatalf("outcome = %s, want activated", r.Outcome)
	}
	if len(r.ChangedCapabilities) != 1 || r.ChangedCapabilities[0] != "processing" {
		t.Errorf("changed = %v, want [processing]", r.ChangedCapabilities)
	}
	if len(r.ReconciliationClasses) != 1 || r.ReconciliationClasses[0] != "processing:disable" {
		t.Errorf("reconciliation classes = %v, want [processing:disable]", r.ReconciliationClasses)
	}
	if m.Snapshot().ProcessingCapability.Enabled {
		t.Error("processing capability should be disabled after reload")
	}
}

// TestManager_EmbeddingDimensionChangeIsVisibleToRuntime proves that a change
// to the requested embedding dimension alone is an activated change that the
// runtime can detect: the compatibility identity stored in the capability
// snapshot changes even though the provider, model, and query instructions are
// unchanged, so the runtime binds subsequent embedding work to a new vector
// space instead of reusing vectors from the previous one.
func TestManager_EmbeddingDimensionChangeIsVisibleToRuntime(t *testing.T) {
	path := newManagerConfigPath(t)
	data := t.TempDir()
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data, embedding: true, embeddingDims: 1024}.String())
	m := NewManager(path, nil)
	if r := m.Reload(context.Background(), TriggerStartup); r.Outcome != OutcomeActivated {
		t.Fatalf("startup outcome = %s", r.Outcome)
	}
	before := m.Snapshot().EmbeddingCapability
	if before.CompatibilityIdentity == "" {
		t.Fatal("enabled embedding capability has no compatibility identity")
	}

	reloadWriteFile(t, path, testConfigBuilder{dataDir: data, embedding: true, embeddingDims: 768}.String())
	r := m.Reload(context.Background(), TriggerAPI)
	if r.Outcome != OutcomeActivated {
		t.Fatalf("dimension reload outcome = %s, error = %s", r.Outcome, r.ErrorCode)
	}
	if len(r.ChangedCapabilities) != 1 || r.ChangedCapabilities[0] != "embedding" {
		t.Fatalf("changed capabilities = %v, want [embedding]", r.ChangedCapabilities)
	}
	after := m.Snapshot().EmbeddingCapability
	if after.CompatibilityIdentity == before.CompatibilityIdentity {
		t.Fatalf("dimension change kept the same compatibility identity %q", after.CompatibilityIdentity)
	}
	if after.Options.Dimensions != 768 {
		t.Fatalf("active embedding dimension = %d, want 768", after.Options.Dimensions)
	}
	if after.Kind != before.Kind || after.Model != before.Model {
		t.Fatalf("dimension change altered provider/model: %+v", after)
	}
}

func TestManager_ConcurrentReloadsSerialized(t *testing.T) {
	path := newManagerConfigPath(t)
	data := t.TempDir()
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data}.String())
	m := NewManager(path, nil)
	m.Reload(context.Background(), TriggerStartup)

	// All goroutines reload the same processing-enabled config.
	reloadWriteFile(t, path, testConfigBuilder{dataDir: data, processing: true}.String())
	const n = 16
	var wg sync.WaitGroup
	var activated, noChange int32
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			r := m.Reload(context.Background(), TriggerAPI)
			switch r.Outcome {
			case OutcomeActivated:
				atomic.AddInt32(&activated, 1)
			case OutcomeNoChange:
				atomic.AddInt32(&noChange, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&activated); got != 1 {
		t.Errorf("expected exactly one activated reload, got %d", got)
	}
	if got := atomic.LoadInt32(&noChange); got != n-1 {
		t.Errorf("expected %d no_change reloads, got %d", n-1, got)
	}
	if m.ActivationGeneration() != 2 {
		t.Errorf("generation = %d, want 2", m.ActivationGeneration())
	}
	if !m.Snapshot().ProcessingCapability.Enabled {
		t.Error("final snapshot should have processing enabled")
	}
}
