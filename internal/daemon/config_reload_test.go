package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/config"
)

type reloadConfig struct {
	dataDir                 string
	runtimeDir              string
	processing              bool
	processingModel         string
	processingAllowProject  bool
	embedding               bool
	embeddingModel          string
	embeddingAllowSpaceType string
}

func (c reloadConfig) TOML() string {
	var b strings.Builder
	fmt.Fprintf(&b, `config_version = 1

[instance]
data_dir = %q
runtime_dir = %q
database_filename = "dmem.db"
socket_filename = "dmem.sock"

[server]
request_timeout = "5s"
shutdown_grace_period = "5s"
max_connections = 4
max_request_bytes = 4194304

[jobs]
worker_count = 2
queue_soft_limit = 10000
queue_hard_limit = 20000
max_attempts = 3
lease_duration = "5s"
per_job_timeout = "10s"
retry_initial_backoff = "100ms"
retry_max_backoff = "1s"

[storage]
disk_soft_free_bytes = 1073741824
disk_hard_free_bytes = 268435456

`, c.dataDir, c.runtimeDir)
	if c.processing {
		model := c.processingModel
		if model == "" {
			model = "processing-a"
		}
		fmt.Fprintf(&b, `[processing]
version = "processor-v1"

[processing.provider]
enabled = true
kind = "openai-compatible"
endpoint = "https://provider.invalid/v1/chat/completions"
model = %q
telemetry_disabled = true

[processing.provider.secret]
source = "environment"
name = "DMEM_RELOAD_PROCESSING_SECRET"

[processing.provider.limits]
max_egress_input_chars = 12000
max_aggregate_input_chars = 48000
max_context_items = 8
max_candidate_chars = 12000
max_total_candidate_chars = 48000
max_response_bytes = 1048576
request_timeout = "30s"
max_concurrent = 2
requests_per_minute = 60

[processing.provider.policy]
default = "deny"
content_types = ["text/plain"]
source_types = ["manual"]
sensitivities = ["normal"]

`, model)

		if c.processingAllowProject {
			b.WriteString(`[[processing.provider.policy.assignments]]
selector = "space_type"
space_type = "project"
effect = "allow"
priority = 100

`)
		} else {
			b.WriteString(`[[processing.provider.policy.assignments]]
selector = "space"
space_id = "11111111-1111-4111-8111-111111111111"
effect = "allow"
include_descendants = true

`)
		}
	} else {
		b.WriteString(`[processing]
version = "builtin-noop-v1"

[processing.provider]
enabled = false

`)
	}
	if c.embedding {
		model := c.embeddingModel
		if model == "" {
			model = "embedding-a"
		}
		spaceType := c.embeddingAllowSpaceType
		if spaceType == "" {
			spaceType = "project"
		}
		fmt.Fprintf(&b, `[embedding.provider]
enabled = true
kind = "openai-compatible"
endpoint = "https://provider.invalid/v1/embeddings"
model = %q
telemetry_disabled = true

[embedding.provider.secret]
source = "environment"
name = "DMEM_RELOAD_EMBEDDING_SECRET"

[embedding.provider.options]
dimensions = 2
query_instructions = "query: "

[embedding.provider.limits]
max_egress_input_chars = 12000
max_response_bytes = 1048576
request_timeout = "30s"
max_concurrent = 2
requests_per_minute = 60

[embedding.provider.policy]
default = "deny"
query = "allow"
content_types = ["text/plain"]
source_types = ["manual"]
sensitivities = ["normal"]

[[embedding.provider.policy.assignments]]
selector = "space_type"
space_type = %q
effect = "allow"
priority = 100
`, model, spaceType)
	} else {
		b.WriteString(`[embedding.provider]
enabled = false
`)
	}
	return b.String()
}

func writeReloadConfig(t *testing.T, path string, cfg reloadConfig) {
	t.Helper()
	if err := os.WriteFile(path, []byte(cfg.TOML()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func managedHealth(t *testing.T, d *Daemon) api.Health {
	t.Helper()
	response, err := unixClient(d.endpoint).Get("http://localhost/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	var health api.Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	return health
}

func TestManagedDaemonReloadsCapabilitiesAtomically(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	initial := reloadConfig{dataDir: filepath.Join(root, "data"), runtimeDir: filepath.Join(root, "runtime")}
	writeReloadConfig(t, path, initial)
	t.Setenv("DMEM_RELOAD_PROCESSING_SECRET", "processing-secret")
	t.Setenv("DMEM_RELOAD_EMBEDDING_SECRET", "embedding-secret")

	manager := config.NewManager(path, nil)
	d, err := StartManaged(context.Background(), manager)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())

	health := managedHealth(t, d)
	if health.ProcessorState != "not_configured" || health.ProcessorIdentity != "none" || health.SemanticIndexState != "not_configured" {
		t.Fatalf("initial health = %+v", health)
	}

	processing := initial
	processing.processing = true
	processing.processingModel = "processing-a"
	writeReloadConfig(t, path, processing)
	result, err := d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated || len(result.ChangedCapabilities) != 1 || result.ChangedCapabilities[0] != "processing" {
		t.Fatalf("processing reload = %+v, %v", result, err)
	}
	health = managedHealth(t, d)
	if health.ProcessorState != "degraded" || health.ProcessorIdentity != "openai-compatible/processing-a" || health.SemanticIndexState != "not_configured" {
		t.Fatalf("processing health = %+v", health)
	}

	embedding := processing
	embedding.embedding = true
	embedding.embeddingModel = "embedding-a"
	writeReloadConfig(t, path, embedding)
	result, err = d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated || len(result.ChangedCapabilities) != 1 || result.ChangedCapabilities[0] != "embedding" {
		t.Fatalf("embedding reload = %+v, %v", result, err)
	}
	health = managedHealth(t, d)
	if health.ProcessorIdentity != "openai-compatible/processing-a" || health.SemanticIndexState == "not_configured" {
		t.Fatalf("embedding health = %+v", health)
	}

	updated := embedding
	updated.embeddingModel = "embedding-b"
	writeReloadConfig(t, path, updated)
	result, err = d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated || len(result.ChangedCapabilities) != 1 || result.ChangedCapabilities[0] != "embedding" {
		t.Fatalf("embedding update = %+v, %v", result, err)
	}

	disabled := updated
	disabled.processing = false
	writeReloadConfig(t, path, disabled)
	result, err = d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated || len(result.ChangedCapabilities) != 1 || result.ChangedCapabilities[0] != "processing" {
		t.Fatalf("processing disable = %+v, %v", result, err)
	}
	health = managedHealth(t, d)
	if health.ProcessorState != "not_configured" || health.ProcessorIdentity != "none" || health.SemanticIndexState == "not_configured" {
		t.Fatalf("disabled processing health = %+v", health)
	}

	previous := manager.Snapshot()
	invalid := strings.Replace(updated.TOML(), "model = \"embedding-b\"", "endpoint = \"http://invalid\"", 1)
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeRejected {
		t.Fatalf("invalid reload = %+v, %v", result, err)
	}
	if manager.Snapshot() != previous {
		t.Fatal("invalid complete file reload replaced the active snapshot")
	}
	health = managedHealth(t, d)
	if health.ProcessorState != "not_configured" || health.SemanticIndexState == "not_configured" {
		t.Fatalf("invalid reload changed active capabilities: %+v", health)
	}
}

func TestSC014ReloadBecomesEffectiveWithinTenSeconds(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	initial := reloadConfig{dataDir: filepath.Join(root, "data"), runtimeDir: filepath.Join(root, "runtime")}
	writeReloadConfig(t, path, initial)
	t.Setenv("DMEM_RELOAD_PROCESSING_SECRET", "processing-secret")

	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())

	enabled := initial
	enabled.processing = true
	writeReloadConfig(t, path, enabled)
	start := time.Now()
	result, err := d.Reload(context.Background(), config.TriggerAPI)
	elapsed := time.Since(start)
	if err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("valid reload = %+v, %v", result, err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("valid reload took %v; want < 10s (SC-014)", elapsed)
	}

	identityBefore := d.manager.Snapshot().Config.EffectiveIdentity
	invalid := strings.Replace(enabled.TOML(), `model = "processing-a"`, `endpoint = "http://invalid"`, 1)
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	result, err = d.Reload(context.Background(), config.TriggerAPI)
	elapsed = time.Since(start)
	if err != nil || result.Outcome != config.OutcomeRejected {
		t.Fatalf("invalid reload = %+v, %v", result, err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("invalid reload took %v; want < 10s (SC-014)", elapsed)
	}
	if d.manager.Snapshot().Config.EffectiveIdentity != identityBefore {
		t.Fatal("invalid reload changed effective identity; expected zero effective-setting changes")
	}
}
