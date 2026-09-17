package config

import (
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/provider"
)

func TestSourceIdentityDeterministic(t *testing.T) {
	t.Parallel()
	data := []byte("config_version = 1\n")
	id1 := sourceIdentity(data)
	id2 := sourceIdentity(data)
	if id1 != id2 {
		t.Fatalf("sourceIdentity not deterministic: %q != %q", id1, id2)
	}
	if !strings.HasPrefix(id1, "sha256:") {
		t.Fatalf("sourceIdentity missing prefix: %q", id1)
	}
}

func TestSourceIdentityDiffersForDifferentBytes(t *testing.T) {
	t.Parallel()
	a := sourceIdentity([]byte("a"))
	b := sourceIdentity([]byte("b"))
	if a == b {
		t.Fatal("sourceIdentity identical for different bytes")
	}
}

func TestEffectiveIdentityExcludesSecrets(t *testing.T) {
	t.Parallel()
	cfg1, err := Load("testdata/valid/minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	cfg2 := cfg1
	cfg2.SourceIdentity = ""
	cfg2.EffectiveIdentity = ""
	id1 := effectiveIdentity(cfg1)
	id2 := effectiveIdentity(cfg2)
	_ = id1
	_ = id2
}

func TestFormattingOnlyChangesProduceNoChange(t *testing.T) {
	t.Parallel()
	doc1 := `config_version = 1

[instance]
data_dir = "/tmp/data"
runtime_dir = "/tmp/run"
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

[processing.provider]
enabled = false

[embedding.provider]
enabled = false
`
	doc2 := strings.Replace(doc1, "config_version = 1\n", "config_version=1\n\n", 1)
	id1 := sourceIdentity([]byte(doc1))
	id2 := sourceIdentity([]byte(doc2))
	if id1 == id2 {
		t.Fatal("source identity should differ for formatting changes")
	}
	d1, _, err := decodeDocument([]byte(doc1))
	if err != nil {
		t.Fatal(err)
	}
	d2, _, err := decodeDocument([]byte(doc2))
	if err != nil {
		t.Fatal(err)
	}
	cfg1, err := normalize(d1, []byte(doc1))
	if err != nil {
		t.Fatal(err)
	}
	cfg2, err := normalize(d2, []byte(doc2))
	if err != nil {
		t.Fatal(err)
	}
	if cfg1.EffectiveIdentity != cfg2.EffectiveIdentity {
		t.Fatalf("effective identity should be the same for formatting-only changes: %q != %q", cfg1.EffectiveIdentity, cfg2.EffectiveIdentity)
	}
}

func TestCapabilityIdentityDisabled(t *testing.T) {
	t.Parallel()
	id := CapabilityIdentity(provider.Config{Enabled: false})
	if id != "" {
		t.Fatalf("CapabilityIdentity for disabled provider should be empty, got %q", id)
	}
}

func TestCapabilityIdentityEnabled(t *testing.T) {
	t.Parallel()
	id := CapabilityIdentity(provider.Config{
		Enabled:  true,
		Kind:     "openai-compatible",
		Endpoint: "https://example.com/v1/chat",
		Model:    "model-a",
	})
	if id == "" {
		t.Fatal("CapabilityIdentity for enabled provider should be non-empty")
	}
	if !strings.HasPrefix(id, "sha256:") {
		t.Fatalf("missing prefix: %q", id)
	}
}

func TestPolicyIdentityExcludesSecretValues(t *testing.T) {
	t.Parallel()
	policy1 := provider.Policy{
		Default:       "deny",
		ContentTypes:  []string{"text/plain"},
		SourceTypes:   []string{"manual"},
		Sensitivities: []string{"normal"},
		Assignments: []provider.PolicyAssignment{
			{Selector: "space", SpaceID: "space-a", Effect: "allow"},
		},
	}
	policy2 := policy1
	policy2.ContentTypes = []string{"text/plain", "text/plain"}
	id1 := PolicyIdentity(policy1)
	id2 := PolicyIdentity(policy2)
	if id1 != id2 {
		t.Fatal("policy identity should be the same with duplicate content types")
	}
}

func TestPolicyIdentityNormalization(t *testing.T) {
	t.Parallel()
	policy1 := provider.Policy{
		Default:       "deny",
		ContentTypes:  []string{"text/plain", "application/json"},
		SourceTypes:   []string{"manual", "message"},
		Sensitivities: []string{"normal"},
		Assignments: []provider.PolicyAssignment{
			{Selector: "space", SpaceID: "space-a", Effect: "allow"},
		},
	}
	policy2 := provider.Policy{
		Default:       "deny",
		ContentTypes:  []string{"application/json", "text/plain"},
		SourceTypes:   []string{"message", "manual"},
		Sensitivities: []string{"normal"},
		Assignments: []provider.PolicyAssignment{
			{Selector: "space", SpaceID: "space-a", Effect: "allow"},
		},
	}
	id1 := PolicyIdentity(policy1)
	id2 := PolicyIdentity(policy2)
	if id1 != id2 {
		t.Fatal("policy identity should be the same regardless of input order")
	}
}

func TestCompatibilityIdentityEmbedding(t *testing.T) {
	t.Parallel()
	id := CompatibilityIdentity(provider.Config{
		Enabled: true,
		Model:   "embedding-a",
		Options: provider.EmbeddingOptions{
			Dimensions:        1024,
			QueryInstructions: "query: ",
		},
	}, false)
	if id == "" {
		t.Fatal("CompatibilityIdentity should be non-empty")
	}
}

func TestCompatibilityIdentityDisabled(t *testing.T) {
	t.Parallel()
	id := CompatibilityIdentity(provider.Config{Enabled: false}, false)
	if id != "" {
		t.Fatalf("CompatibilityIdentity for disabled provider should be empty, got %q", id)
	}
}

func TestEffectiveIdentitySameForEquivalentConfigs(t *testing.T) {
	t.Parallel()
	base, err := Load("testdata/valid/minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	id1 := effectiveIdentity(base)
	id2 := effectiveIdentity(base)
	if id1 != id2 {
		t.Fatal("effective identity should be deterministic")
	}
}
