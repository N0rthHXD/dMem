package config

import (
	"flag"
	"path/filepath"
	"testing"
	"time"
)

func TestParseResolvesDerivedPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	runtimeDir := filepath.Join(root, "runtime")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg, err := Parse(fs, []string{"-data-dir", dataDir, "-runtime-dir", runtimeDir})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.DatabasePath != filepath.Join(dataDir, "dmem.db") {
		t.Errorf("DatabasePath = %q", cfg.DatabasePath)
	}
	if cfg.Endpoint != filepath.Join(runtimeDir, "dmem.sock") {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
}

func TestParseJobConfiguration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg, err := Parse(fs, []string{
		"-data-dir", filepath.Join(root, "data"),
		"-runtime-dir", filepath.Join(root, "runtime"),
		"-lease-duration", "10s",
		"-per-job-timeout", "30s",
		"-retry-initial-backoff", "250ms",
		"-retry-max-backoff", "2s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LeaseDuration != 10*time.Second || cfg.PerJobTimeout != 30*time.Second || cfg.RetryInitialBackoff != 250*time.Millisecond || cfg.RetryMaxBackoff != 2*time.Second {
		t.Fatalf("job config = %+v", cfg)
	}
}

func TestParseRejectsInvalidJobConfiguration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	_, err := Parse(fs, []string{
		"-data-dir", filepath.Join(root, "data"),
		"-runtime-dir", filepath.Join(root, "runtime"),
		"-lease-duration", "30s",
		"-per-job-timeout", "10s",
	})
	if err == nil {
		t.Fatal("Parse() accepted per-job timeout shorter than lease duration")
	}
}

func TestParseRejectsSocketOutsideRuntimeDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	_, err := Parse(fs, []string{
		"-data-dir", filepath.Join(root, "data"),
		"-runtime-dir", filepath.Join(root, "runtime"),
		"-endpoint", filepath.Join(root, "elsewhere", "dmem.sock"),
	})
	if err == nil {
		t.Fatal("Parse() accepted endpoint outside runtime directory")
	}
}
