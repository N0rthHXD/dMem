package daemon

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/instance"
	"github.com/N0rthHXD/dMem/internal/jobs"
	"github.com/N0rthHXD/dMem/internal/provider"
)

// runtimeConfig returns a complete, restart-safe runtime configuration rooted in
// a fresh temporary data directory. The runtime derives everything except the
// data directory from the configuration snapshot.
func runtimeConfig(t *testing.T) config.Config {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	return config.Config{
		ConfigVersion:       1,
		DataDir:             dataDir,
		DatabaseFilename:    "dmem.db",
		DatabasePath:        filepath.Join(dataDir, "dmem.db"),
		ShutdownGracePeriod: 5 * time.Second,
		WorkerCount:         2,
		QueueSoftLimit:      10000,
		QueueHardLimit:      20000,
		MaxAttempts:         3,
		LeaseDuration:       5 * time.Second,
		PerJobTimeout:       10 * time.Second,
		RetryInitialBackoff: 100 * time.Millisecond,
		RetryMaxBackoff:     time.Second,
		DiskSoftFree:        1 << 30,
		DiskHardFree:        1 << 28,
		ProcessingVersion:   config.DefaultProcessingVersion,
		GraphEnabled:        true,
	}
}

// managedConfig writes a complete TOML document for the managed startup path.
// The data directory is fixed so reloads never change a restart-bound field.
func managedConfig(dataDir string, processing bool, processingModel string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, `config_version = 1

[instance]
data_dir = %q
database_filename = "dmem.db"

[server]
shutdown_grace_period = "5s"

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

`, dataDir)
	if !processing {
		builder.WriteString(`[processing]
version = "builtin-noop-v1"

[processing.provider]
enabled = false

[embedding.provider]
enabled = false
`)
		return builder.String()
	}
	if processingModel == "" {
		processingModel = "processing-a"
	}
	fmt.Fprintf(&builder, `[processing]
version = "processor-v1"

[processing.provider]
enabled = true
kind = "openai-compatible"
endpoint = "https://provider.invalid/v1/chat/completions"
model = %q
telemetry_disabled = true

[processing.provider.secret]
source = "environment"
name = "DMEM_RUNTIME_PROCESSING_SECRET"

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

[[processing.provider.policy.assignments]]
selector = "space"
space_id = "11111111-1111-4111-8111-111111111111"
effect = "allow"
include_descendants = true

[embedding.provider]
enabled = false
`, processingModel)
	return builder.String()
}

func writeManagedConfig(t *testing.T, path, dataDir string, processing bool) {
	t.Helper()
	writeManagedConfigModel(t, path, dataDir, processing, "")
}

func writeManagedConfigModel(t *testing.T, path, dataDir string, processing bool, processingModel string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(managedConfig(dataDir, processing, processingModel)), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestRuntimeComposesHandlerAndLifecycle proves the runtime assembles the
// protocol handler and job workers without creating a listener, and that close
// releases the instance lock and the database.
func TestRuntimeComposesHandlerAndLifecycle(t *testing.T) {
	t.Parallel()
	cfg := runtimeConfig(t)
	d, err := start(context.Background(), cfg, jobs.NoopProcessor{})
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	if d.Handler() == nil {
		t.Fatal("runtime did not assemble an HTTP handler")
	}
	if d.jobs == nil || d.repository == nil || d.store == nil {
		t.Fatalf("runtime composition incomplete: %+v", d)
	}
	if state, identity := d.jobs.ProcessorHealth(); state != "not_configured" || identity != "none" {
		t.Fatalf("processor health = %q/%q; want not_configured/none", state, identity)
	}
	if err := d.store.WaitIntegrity(context.Background()); err != nil {
		t.Fatalf("integrity check failed: %v", err)
	}
	if status, err := d.store.IntegrityStatus(); err != nil || status == "" {
		t.Fatalf("integrity status = %q, %v", status, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	lock, err := instance.Acquire(filepath.Join(cfg.DataDir, "daemon.lock"))
	if err != nil {
		t.Fatalf("instance lock remains after close: %v", err)
	}
	lock.Close()
}

// TestRuntimeRejectsSecondInstance proves the single-instance lock still fences
// a second runtime over the same data directory.
func TestRuntimeRejectsSecondInstance(t *testing.T) {
	t.Parallel()
	cfg := runtimeConfig(t)
	first, err := start(context.Background(), cfg, jobs.NoopProcessor{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())

	second, err := start(context.Background(), cfg, jobs.NoopProcessor{})
	if second != nil {
		second.Close(context.Background())
	}
	if !errors.Is(err, instance.ErrAlreadyRunning) {
		t.Fatalf("second start error = %v; want ErrAlreadyRunning", err)
	}
}

// TestRuntimeManagedReloadAppliesSnapshots proves managed startup, configuration
// activation, provider update, and generation advancement all flow through the
// runtime without a listener.
func TestRuntimeManagedReloadAppliesSnapshots(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	path := filepath.Join(root, "config.toml")
	writeManagedConfig(t, path, dataDir, false)
	t.Setenv("DMEM_RUNTIME_PROCESSING_SECRET", "processing-secret")

	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatalf("StartManaged() error = %v", err)
	}
	defer d.Close(context.Background())

	if d.Handler() == nil {
		t.Fatal("managed runtime did not assemble a handler")
	}
	snapshot, attempt := d.ConfigurationStatus()
	if snapshot == nil || attempt.Outcome != config.OutcomeActivated {
		t.Fatalf("startup status = %+v, %+v", snapshot, attempt)
	}
	firstGeneration := snapshot.ActivationGeneration

	result, err := d.Reload(context.Background(), config.TriggerSIGHUP)
	if err != nil || result.Outcome != config.OutcomeNoChange || result.Trigger != config.TriggerSIGHUP {
		t.Fatalf("no-change reload = %+v, %v", result, err)
	}

	writeManagedConfig(t, path, dataDir, true)
	result, err = d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("processing reload = %+v, %v", result, err)
	}
	if len(result.ChangedCapabilities) != 1 || result.ChangedCapabilities[0] != "processing" {
		t.Fatalf("changed capabilities = %v; want [processing]", result.ChangedCapabilities)
	}
	snapshot, attempt = d.ConfigurationStatus()
	if snapshot.ActivationGeneration <= firstGeneration {
		t.Fatalf("activation generation did not advance: %d -> %d", firstGeneration, snapshot.ActivationGeneration)
	}
	if !snapshot.ProcessingCapability.Enabled {
		t.Fatal("processing capability not enabled after reload")
	}
	if state, identity := d.jobs.ProcessorHealth(); state != "degraded" || identity != "openai-compatible/processing-a" {
		t.Fatalf("processor health after reload = %q/%q", state, identity)
	}
	if attempt.Trigger != config.TriggerAPI {
		t.Fatalf("last attempt trigger = %q; want api", attempt.Trigger)
	}
}

// TestRuntimeApplySnapshotFailurePreservesPreviousConfiguration proves a failed
// capability update reverts the manager snapshot and leaves the previous
// provider generation active.
func TestRuntimeApplySnapshotFailurePreservesPreviousConfiguration(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	path := filepath.Join(root, "config.toml")
	writeManagedConfig(t, path, dataDir, false)
	t.Setenv("DMEM_RUNTIME_PROCESSING_SECRET", "processing-secret")

	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())

	writeManagedConfig(t, path, dataDir, true)
	if result, err := d.Reload(context.Background(), config.TriggerAPI); err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("processing reload = %+v, %v", result, err)
	}
	previousIdentity := d.manager.Snapshot().Config.EffectiveIdentity
	previousGeneration := d.manager.ActivationGeneration()

	d.applySnapshotErr = errors.New("injected applySnapshot failure")
	writeManagedConfigModel(t, path, dataDir, true, "processing-b")
	if _, err := d.Reload(context.Background(), config.TriggerAPI); err == nil {
		t.Fatal("expected applySnapshot failure")
	}
	if got := d.manager.Snapshot().Config.EffectiveIdentity; got != previousIdentity {
		t.Fatalf("effective identity = %q; want previous %q", got, previousIdentity)
	}
	if got := d.manager.ActivationGeneration(); got != previousGeneration {
		t.Fatalf("activation generation = %d; want previous %d", got, previousGeneration)
	}
	if state, identity := d.jobs.ProcessorHealth(); state != "degraded" || identity != "openai-compatible/processing-a" {
		t.Fatalf("previous processing capability not preserved: %q/%q", state, identity)
	}
}

// TestRuntimeConcurrentReloadsAreSerialized proves reload serialization keeps a
// single activation per candidate revision.
func TestRuntimeConcurrentReloadsAreSerialized(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	path := filepath.Join(root, "config.toml")
	writeManagedConfig(t, path, dataDir, false)
	t.Setenv("DMEM_RUNTIME_PROCESSING_SECRET", "processing-secret")

	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())

	writeManagedConfig(t, path, dataDir, true)
	const concurrent = 5
	results := make(chan config.ActivationResult, concurrent)
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, _ := d.Reload(context.Background(), config.TriggerSIGHUP)
			results <- result
		}()
	}
	close(start)
	group.Wait()
	close(results)

	activated := 0
	for result := range results {
		if result.Trigger != config.TriggerSIGHUP {
			t.Fatalf("concurrent reload trigger = %q; want sighup", result.Trigger)
		}
		if result.Outcome == config.OutcomeActivated {
			activated++
		}
	}
	if activated != 1 {
		t.Fatalf("concurrent reloads produced %d activations; want exactly 1", activated)
	}
}

// TestStartStagingClearsProviders proves the staging assembly never resolves a
// provider secret or builds a provider client.
func TestStartStagingClearsProviders(t *testing.T) {
	t.Parallel()
	cfg := runtimeConfig(t)
	cfg.ProcessingProvider = config.ProviderConfig{Enabled: true, Secret: provider.SecretReference{Source: "environment", Name: "DMEM_STAGING_MISSING_PROCESSING"}}
	cfg.EmbeddingProvider = config.ProviderConfig{Enabled: true, Secret: provider.SecretReference{Source: "environment", Name: "DMEM_STAGING_MISSING_EMBEDDING"}}
	d, err := StartStaging(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartStaging() error = %v", err)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareDirRejectsSymlinkedComponents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareDir(filepath.Join(link, "data"), 0o700); err == nil {
		t.Fatal("prepareDir accepted a symlinked directory component")
	}
	if _, err := os.Stat(filepath.Join(target, "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepareDir created a directory through a symlink: %v", err)
	}
}

// TestRuntimeSourceHasNoListenerOrSocketOwnership is a static guard proving the
// runtime composition root never creates a Unix listener, an http.Server, a
// socket file, or a connection limiter.
func TestRuntimeSourceHasNoListenerOrSocketOwnership(t *testing.T) {
	forbidden := []string{
		"net.Listen",
		"http.Server",
		"ListenAndServe",
		"ModeSocket",
		"SetUnlinkOnClose",
		"limitListener",
		"clearStaleSocket",
	}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if importPath == "net" {
				t.Errorf("%s imports net; the runtime must not own a listener", path)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, ok := selector.X.(*ast.Ident); ok {
				full := ident.Name + "." + selector.Sel.Name
				for _, candidate := range forbidden {
					if full == candidate {
						t.Errorf("%s references forbidden runtime ownership %q", path, full)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk daemon tree: %v", err)
	}
}
