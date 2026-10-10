package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/provider"
)

func TestLoadRejectsSymlinkedConfig(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("testdata", "valid", "minimal.toml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("symlinked config file accepted")
	}
	parentLink := filepath.Join(dir, "parent-link")
	if err := os.Symlink(dir, parentLink); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(parentLink, "real.toml")); err == nil {
		t.Fatal("config under a symlinked directory accepted")
	}
}

func TestLoadRejectsNonRegularConfig(t *testing.T) {
	t.Parallel()
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("directory accepted as config file")
	}
}

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
	if cfg.ProcessingVersion != LocalProcessingVersion {
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

func TestPackagedDefaultConfigExpandsHome(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "packaging", "config.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "share", "dmem")
	if cfg.DataDir != want {
		t.Fatalf("DataDir = %q, want %q", cfg.DataDir, want)
	}
}

func TestConfigExpandsTildeHome(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "valid", "tilde_home.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "share", "dmem-tilde")
	if cfg.DataDir != want {
		t.Fatalf("DataDir = %q, want %q", cfg.DataDir, want)
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
database_filename = "dmem.db"

[server]
shutdown_grace_period = "15s"

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
enabled = false
graph_enabled = false

[embedding]
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
database_filename = "dmem.db"

[server]
shutdown_grace_period = "15s"

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
enabled = false
%s

[embedding]
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

func TestListenAddressDefaultsToLoopback(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("testdata", "valid", "minimal.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != DefaultListenAddress {
		t.Errorf("ListenAddress = %q, want %q", cfg.ListenAddress, DefaultListenAddress)
	}
	if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
		t.Errorf("TLS files = %q/%q, want empty", cfg.TLSCertFile, cfg.TLSKeyFile)
	}
}

func TestServerListenAddressValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		address string
		wantErr bool
	}{
		{"loopback ok", "127.0.0.1:7373", false},
		{"localhost ok", "localhost:7373", false},
		{"any host ok", "0.0.0.0:7373", false},
		{"bad port", "127.0.0.1:70000", true},
		{"missing port", "127.0.0.1", true},
		{"empty port", "127.0.0.1:", true},
		{"hostname rejected", "example.com:7373", true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeServerConfig(t, fmt.Sprintf("listen_address = %q\n", tc.address))
			_, err := Load(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestServerTLSValidation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	key := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		extra   string
		wantErr bool
	}{
		{"pair ok", fmt.Sprintf("tls_cert_file = %q\ntls_key_file = %q\n", cert, key), false},
		{"cert only", fmt.Sprintf("tls_cert_file = %q\n", cert), true},
		{"key only", fmt.Sprintf("tls_key_file = %q\n", key), true},
		{"relative cert", "tls_cert_file = \"cert.pem\"\ntls_key_file = \"/tmp/key.pem\"\n", true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeServerConfig(t, tc.extra)
			_, err := Load(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestServerFieldsAreRestartBound(t *testing.T) {
	t.Parallel()
	base, err := Load(filepath.Join("testdata", "valid", "minimal.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []func(*Config){
		func(c *Config) { c.ListenAddress = "127.0.0.1:9999" },
		func(c *Config) { c.TLSCertFile = "/tmp/cert.pem"; c.TLSKeyFile = "/tmp/key.pem" },
	}
	for i, edit := range cases {
		candidate := base
		edit(&candidate)
		if changed := restartFieldsChanged(candidate, base); len(changed) == 0 {
			t.Fatalf("case %d: server change not reported as restart-bound", i)
		}
	}
	if changed := restartFieldsChanged(base, base); len(changed) != 0 {
		t.Fatalf("identical config reported restart-bound changes: %v", changed)
	}
}

func writeServerConfig(t *testing.T, extraServer string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	doc := fmt.Sprintf(`config_version = 1

[instance]
data_dir = "/tmp/dmem-test/data"
database_filename = "dmem.db"

[server]
shutdown_grace_period = "15s"
%s
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
enabled = false

[embedding]
enabled = false
`, extraServer)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeProviderConfig(t *testing.T, block string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	doc := `config_version = 1

[instance]
data_dir = "/tmp/dmem-test/data"
database_filename = "dmem.db"

[server]
shutdown_grace_period = "15s"

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

` + block
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDisabledProviderRouteIgnoresProviderConfiguration proves the disabled
// Provider Route does not require or validate endpoint, model, secret, or
// egress policy.
func TestDisabledProviderRouteIgnoresProviderConfiguration(t *testing.T) {
	t.Parallel()
	cfg, err := Load(writeProviderConfig(t, `[processing]
enabled = false

[processing.provider]
endpoint = "not-a-url"
model = "ignored"

[embedding]
enabled = false

[embedding.provider]
kind = "unsupported"
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProcessingProvider.Enabled || cfg.EmbeddingProvider.Enabled {
		t.Fatal("providers should be disabled")
	}
	if cfg.ProcessingVersion != LocalProcessingVersion {
		t.Fatalf("ProcessingVersion = %q, want %q", cfg.ProcessingVersion, LocalProcessingVersion)
	}
}

// TestEnabledProviderRouteRequiresProviderConfiguration proves that enabling the
// route strictly requires a complete provider configuration.
func TestEnabledProviderRouteRequiresProviderConfiguration(t *testing.T) {
	t.Parallel()
	for name, block := range map[string]string{
		"missing provider": `[processing]
enabled = true

[embedding]
enabled = false
`,
		"incomplete provider": `[processing]
enabled = true

[processing.provider]
kind = "openai-compatible"
endpoint = "https://provider.example/v1/chat/completions"
model = "model-a"

[embedding]
enabled = false
`,
		"embedding without provider": `[processing]
enabled = false

[embedding]
enabled = true
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeProviderConfig(t, block)); err == nil {
				t.Fatal("Load() accepted an enabled route without a complete provider configuration")
			}
		})
	}
}

// TestEnabledProviderRouteDerivesProcessingVersion proves a configured model
// becomes the provenance version instead of a user-supplied label.
func TestEnabledProviderRouteDerivesProcessingVersion(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("testdata", "enabled-processing", "processing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProcessingVersion != "openai-compatible/model-a" {
		t.Fatalf("ProcessingVersion = %q, want the provider identity", cfg.ProcessingVersion)
	}
}
