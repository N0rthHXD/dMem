package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/daemon"
	"github.com/N0rthHXD/dMem/internal/instance"
	"github.com/N0rthHXD/dMem/internal/maintenance"
	_ "github.com/mattn/go-sqlite3"
)

func writeTestConfig(t *testing.T, dataDir string) string {
	t.Helper()
	cfgDir := filepath.Join(t.TempDir(), "dmem")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	content := fmt.Sprintf(`config_version = 1

[instance]
data_dir = "%s"
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
version = "builtin-noop-v1"

[processing.provider]
enabled = false

[embedding.provider]
enabled = false
`, dataDir)
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func TestInitBackupAndRestoreCommands(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "data")
	backupParent := filepath.Join(root, "backups")
	if err := os.Mkdir(backupParent, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeTestConfig(t, dataDir)
	var output bytes.Buffer
	if err := runMaintenance(context.Background(), "init", []string{}, &output, cfgPath); err != nil {
		t.Fatalf("init error = %v", err)
	}
	if !strings.Contains(output.String(), `"schema_version":1`) {
		t.Fatalf("init output = %q", output.String())
	}
	output.Reset()
	target := filepath.Join(backupParent, "backup-1")
	if err := runMaintenance(context.Background(), "backup", []string{"--target", target}, &output, cfgPath); err != nil {
		t.Fatalf("backup error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, maintenance.ManifestName)); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runMaintenance(context.Background(), "restore", []string{"--source", target, "--confirm-replace"}, &output, cfgPath); err != nil {
		t.Fatalf("restore error = %v", err)
	}
	if !strings.Contains(output.String(), `"fts_rebuilt":true`) {
		t.Fatalf("restore output = %q", output.String())
	}
}

func TestRestoreCommandRequiresConfirmationAndOfflineLock(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "data")
	backupDir := filepath.Join(root, "backup")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeTestConfig(t, dataDir)
	if err := runMaintenance(context.Background(), "restore", []string{"--source", backupDir}, &bytes.Buffer{}, cfgPath); !errors.Is(err, errUsage) {
		t.Fatalf("restore without confirmation error = %v", err)
	}
	lock, err := instance.Acquire(filepath.Join(dataDir, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	err = runMaintenance(context.Background(), "restore", []string{"--source", backupDir, "--confirm-replace"}, &bytes.Buffer{}, cfgPath)
	if !errors.Is(err, maintenance.ErrOfflineRequired) {
		t.Fatalf("active restore error = %v", err)
	}
	if got := maintenanceError(err); got != "maintenance: offline_required" || strings.Contains(got, root) {
		t.Fatalf("maintenance error = %q", got)
	}
}

func TestMaintenanceParsingIgnoresProviderEnvironment(t *testing.T) {
	t.Setenv("DMEM_PROCESSING_PROVIDER_API_KEY", "processing-secret")
	t.Setenv("DMEM_EMBEDDING_PROVIDER_API_KEY", "embedding-secret")
	t.Setenv("DMEM_PROVIDER_ENDPOINT", "https://provider.example")
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeTestConfig(t, filepath.Join(root, "data"))
	if err := runMaintenance(context.Background(), "init", []string{}, &bytes.Buffer{}, cfgPath); err != nil {
		t.Fatalf("init error = %v", err)
	}
}

func TestExtractGlobalConfig(t *testing.T) {
	path, rest, err := extractGlobalConfig([]string{"--config", "/tmp/dmem.toml", "config", "validate"})
	if err != nil || path != "/tmp/dmem.toml" || strings.Join(rest, " ") != "config validate" {
		t.Fatalf("parse = %q, %q, %v", path, rest, err)
	}
	for _, args := range [][]string{{"--config"}, {"--config", "a", "--config=b", "status"}, {"--config=", "status"}} {
		if _, _, err := extractGlobalConfig(args); !errors.Is(err, errUsage) {
			t.Fatalf("extractGlobalConfig(%q) error = %v", args, err)
		}
	}
}

func TestRunConfigValidate(t *testing.T) {
	configPath := writeTestConfig(t, filepath.Join(t.TempDir(), "data"))
	var output bytes.Buffer
	if err := runConfig(configPath, []string{"validate"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"source_identity":"sha256:`) || !strings.Contains(output.String(), `"effective_identity":"sha256:`) {
		t.Fatalf("validate output = %q", output.String())
	}
	if err := runConfig(configPath, []string{"validate", "extra"}, &output); !errors.Is(err, errUsage) {
		t.Fatalf("invalid config command error = %v", err)
	}
	if err := runConfig(configPath, []string{"status"}, &output); !errors.Is(err, errUsage) {
		t.Fatalf("removed status command error = %v", err)
	}
}

func TestHandleSIGHUPReloadsTriggersReloadAndCoalesces(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	cfgPath := writeTestConfig(t, dataDir)

	manager := config.NewManager(cfgPath, nil)
	d, err := daemon.StartManaged(context.Background(), manager)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())

	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "dmem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	signals := make(chan os.Signal, 1)
	go handleSIGHUPReloads(signals, d)

	signals <- syscall.SIGHUP
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, attempt := d.ConfigurationStatus()
		if attempt.Trigger == config.TriggerSIGHUP {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, attempt := d.ConfigurationStatus()
	if attempt.Trigger != config.TriggerSIGHUP {
		t.Fatalf("SIGHUP did not trigger reload: %+v", attempt)
	}

	var initialCount int
	if err := db.QueryRow(`SELECT count(*) FROM configuration_activations WHERE trigger='sighup'`).Scan(&initialCount); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		select {
		case signals <- syscall.SIGHUP:
		default:
		}
	}
	time.Sleep(500 * time.Millisecond)
	var finalCount int
	if err := db.QueryRow(`SELECT count(*) FROM configuration_activations WHERE trigger='sighup'`).Scan(&finalCount); err != nil {
		t.Fatal(err)
	}
	if finalCount-initialCount > 2 {
		t.Fatalf("coalescing failed: %d reloads for 10 rapid signals; want at most 2", finalCount-initialCount)
	}

	close(signals)
}
