package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/provider"
)

func TestLoadValidMinimal(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("testdata", "valid", "minimal.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ConfigVersion != 1 {
		t.Errorf("ConfigVersion = %d", cfg.ConfigVersion)
	}
	if cfg.ProcessingProvider.Enabled || cfg.EmbeddingProvider.Enabled {
		t.Error("providers should be disabled in minimal config")
	}
	if cfg.ProcessingVersion != DefaultProcessingVersion {
		t.Errorf("ProcessingVersion = %q", cfg.ProcessingVersion)
	}
}

func TestPolicyAssignmentValidationAndCanonicalIdentity(t *testing.T) {
	policy := provider.Policy{Default: "deny", Query: "allow", ContentTypes: []string{"text/plain"}, SourceTypes: []string{"manual"}, Sensitivities: []string{"normal"}, Assignments: []provider.PolicyAssignment{{Selector: "space_type", SpaceType: "project", Effect: "allow", Priority: 1}}}
	if err := validatePolicy(policy, false); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	invalid := policy
	invalid.Assignments = []provider.PolicyAssignment{{Selector: "space", SpaceID: "space", ExternalID: "unexpected", Effect: "allow"}}
	if err := validatePolicy(invalid, false); err == nil {
		t.Fatal("mixed selector fields accepted")
	}
	reordered := policy
	reordered.Assignments = append([]provider.PolicyAssignment{{Selector: "external_id", ExternalID: "project-a", Effect: "deny", Priority: 2}}, policy.Assignments...)
	policy.Assignments = append(policy.Assignments, reordered.Assignments[0])
	if PolicyIdentity(policy) != PolicyIdentity(reordered) {
		t.Fatal("canonical policy identity changed with assignment order")
	}
}

func TestLoadValidComplete(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("testdata", "valid", "complete.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.ProcessingProvider.Enabled {
		t.Error("processing provider should be enabled")
	}
	if !cfg.EmbeddingProvider.Enabled {
		t.Error("embedding provider should be enabled")
	}
	if cfg.SourceIdentity == "" {
		t.Error("SourceIdentity should be set")
	}
	if cfg.EffectiveIdentity == "" {
		t.Error("EffectiveIdentity should be set")
	}
}

func TestExampleConfigValidates(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "operations", "config.example.toml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProcessingProvider.Enabled || cfg.EmbeddingProvider.Enabled {
		t.Error("providers should be disabled in example config")
	}
}

func TestProvidersDisabledByDefault(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("testdata", "valid", "minimal.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProcessingProvider.Enabled || cfg.EmbeddingProvider.Enabled {
		t.Fatal("providers are enabled by default")
	}
}

func TestRejectsBadVersion(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join("testdata", "invalid", "bad_version.toml"))
	if err == nil {
		t.Fatal("Load() accepted bad config version")
	}
}

func TestRejectsMissingDataDir(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join("testdata", "invalid", "missing_data_dir.toml"))
	if err == nil {
		t.Fatal("Load() accepted missing data_dir")
	}
}

func TestRejectsBadDuration(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join("testdata", "invalid", "bad_duration.toml"))
	if err == nil {
		t.Fatal("Load() accepted bad duration")
	}
}

func TestRejectsRelativePath(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join("testdata", "invalid", "relative_path.toml"))
	if err == nil {
		t.Fatal("Load() accepted relative path")
	}
}

func TestRejectsUnknownField(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join("testdata", "unknown-field", "extra_in_instance.toml"))
	if err == nil {
		t.Fatal("Load() accepted unknown field in instance table")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if ve.Code != "config_unknown_field" && ve.Code != "config_invalid_syntax" {
		t.Errorf("error code = %q, want config_unknown_field", ve.Code)
	}
}

func TestRejectsUnknownTable(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join("testdata", "unknown-field", "extra_table.toml"))
	if err == nil {
		t.Fatal("Load() accepted unknown table")
	}
}

func TestRejectsDuplicateTable(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join("testdata", "duplicate-field", "duplicate_table.toml"))
	if err == nil {
		t.Fatal("Load() accepted duplicate table")
	}
}

func TestRejectsDuplicateKey(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join("testdata", "duplicate-field", "duplicate_key.toml"))
	if err == nil {
		t.Fatal("Load() accepted duplicate key")
	}
}

func TestEnabledProcessing(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("testdata", "enabled-processing", "processing.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.ProcessingProvider.Enabled {
		t.Error("processing provider should be enabled")
	}
	if cfg.EmbeddingProvider.Enabled {
		t.Error("embedding provider should be disabled")
	}
	if cfg.ProcessingProvider.Kind != "openai-compatible" {
		t.Errorf("Kind = %q", cfg.ProcessingProvider.Kind)
	}
}

func TestEnabledEmbedding(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("testdata", "enabled-embedding", "embedding.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.EmbeddingProvider.Enabled {
		t.Error("embedding provider should be enabled")
	}
	if cfg.ProcessingProvider.Enabled {
		t.Error("processing provider should be disabled")
	}
	if cfg.EmbeddingProvider.Options.Dimensions != 1024 {
		t.Errorf("Dimensions = %d", cfg.EmbeddingProvider.Options.Dimensions)
	}
	if cfg.EmbeddingProvider.Options.QueryInstructions != "query: " {
		t.Errorf("QueryInstructions = %q", cfg.EmbeddingProvider.Options.QueryInstructions)
	}
}

func TestGraphEnabledDefaultsToTrue(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("testdata", "valid", "minimal.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.GraphEnabled {
		t.Error("GraphEnabled should default to true when omitted")
	}
}

func TestGraphEnabledExplicitFalse(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	doc := `config_version = 1

[instance]
data_dir = "/tmp/dmem-test/data"
runtime_dir = "/tmp/dmem-test/run"
database_filename = "dmem.db"
socket_filename = "dmem.sock"

[server]
request_timeout = "30s"
shutdown_grace_period = "15s"
max_connections = 64
max_request_bytes = 4194304

[jobs]
worker_count = 2
queue_soft_limit = 10000
queue_hard_limit = 20000
max_attempts = 3
lease_duration = "30s"
per_job_timeout = "2m"
retry_initial_backoff = "1s"
retry_max_backoff = "5m"

[storage]
disk_soft_free_bytes = 1073741824
disk_hard_free_bytes = 268435456

[processing]
version = "builtin-noop-v1"
graph_enabled = false

[processing.provider]
enabled = false

[embedding.provider]
enabled = false
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GraphEnabled {
		t.Error("GraphEnabled should be false when explicitly disabled")
	}
}

func TestGraphEnabledChangesEffectiveIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	enabledPath := filepath.Join(dir, "enabled.toml")
	disabledPath := filepath.Join(dir, "disabled.toml")
	base := `config_version = 1

[instance]
data_dir = "/tmp/dmem-test/data"
runtime_dir = "/tmp/dmem-test/run"
database_filename = "dmem.db"
socket_filename = "dmem.sock"

[server]
request_timeout = "30s"
shutdown_grace_period = "15s"
max_connections = 64
max_request_bytes = 4194304

[jobs]
worker_count = 2
queue_soft_limit = 10000
queue_hard_limit = 20000
max_attempts = 3
lease_duration = "30s"
per_job_timeout = "2m"
retry_initial_backoff = "1s"
retry_max_backoff = "5m"

[storage]
disk_soft_free_bytes = 1073741824
disk_hard_free_bytes = 268435456

[processing]
version = "builtin-noop-v1"
%s

[processing.provider]
enabled = false

[embedding.provider]
enabled = false
`
	if err := os.WriteFile(enabledPath, []byte(fmt.Sprintf(base, "graph_enabled = true")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disabledPath, []byte(fmt.Sprintf(base, "graph_enabled = false")), 0o600); err != nil {
		t.Fatal(err)
	}
	enabledCfg, err := Load(enabledPath)
	if err != nil {
		t.Fatal(err)
	}
	disabledCfg, err := Load(disabledPath)
	if err != nil {
		t.Fatal(err)
	}
	if enabledCfg.EffectiveIdentity == disabledCfg.EffectiveIdentity {
		t.Fatal("effective identity should change when graph_enabled changes")
	}
}

func TestDurationsParsed(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("testdata", "valid", "minimal.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequestTimeout != 30*time.Second {
		t.Errorf("RequestTimeout = %v", cfg.RequestTimeout)
	}
	if cfg.ShutdownGracePeriod != 15*time.Second {
		t.Errorf("ShutdownGracePeriod = %v", cfg.ShutdownGracePeriod)
	}
	if cfg.LeaseDuration != 30*time.Second {
		t.Errorf("LeaseDuration = %v", cfg.LeaseDuration)
	}
}

func TestDerivedPaths(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("testdata", "valid", "minimal.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabasePath != filepath.Join(cfg.DataDir, "dmem.db") {
		t.Errorf("DatabasePath = %q", cfg.DatabasePath)
	}
	if cfg.Endpoint != filepath.Join(cfg.RuntimeDir, "dmem.sock") {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
}

func TestNoSettingEnvironmentOverrides(t *testing.T) {
	t.Setenv("DMEM_DATA_DIR", "/tmp/should-be-ignored")
	t.Setenv("DMEM_PROCESSING_VERSION", "should-be-ignored")
	cfg, err := Load(filepath.Join("testdata", "valid", "minimal.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir == "/tmp/should-be-ignored" {
		t.Error("DMEM_DATA_DIR env override was not ignored")
	}
	if cfg.ProcessingVersion == "should-be-ignored" {
		t.Error("DMEM_PROCESSING_VERSION env override was not ignored")
	}
}
