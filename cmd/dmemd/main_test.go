package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/instance"
	"github.com/N0rthHXD/dMem/internal/maintenance"
)

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
	var output bytes.Buffer
	if err := runMaintenance(context.Background(), "init", []string{"--data-dir", dataDir, "--minimum-free", "0"}, &output); err != nil {
		t.Fatalf("init error = %v", err)
	}
	if !strings.Contains(output.String(), `"schema_version":1`) {
		t.Fatalf("init output = %q", output.String())
	}
	output.Reset()
	target := filepath.Join(backupParent, "backup-1")
	if err := runMaintenance(context.Background(), "backup", []string{"--data-dir", dataDir, "--target", target, "--minimum-free", "0"}, &output); err != nil {
		t.Fatalf("backup error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, maintenance.ManifestName)); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runMaintenance(context.Background(), "restore", []string{"--data-dir", dataDir, "--source", target, "--confirm-replace", "--minimum-free", "0"}, &output); err != nil {
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
	if err := runMaintenance(context.Background(), "restore", []string{"--data-dir", dataDir, "--source", backupDir}, &bytes.Buffer{}); !errors.Is(err, errUsage) {
		t.Fatalf("restore without confirmation error = %v", err)
	}
	lock, err := instance.Acquire(filepath.Join(dataDir, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	err = runMaintenance(context.Background(), "restore", []string{"--data-dir", dataDir, "--source", backupDir, "--confirm-replace", "--minimum-free", "0"}, &bytes.Buffer{})
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
	if err := runMaintenance(context.Background(), "init", []string{"--data-dir", filepath.Join(root, "data"), "--minimum-free", "0"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("init error = %v", err)
	}
}
