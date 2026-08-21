package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/instance"
)

func TestDaemonLifecycleAndHealth(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	info, err := os.Stat(cfg.RuntimeDir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime mode = %v, %v; want 0700", info.Mode().Perm(), err)
	}
	info, err = os.Stat(cfg.Endpoint)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}

	client := unixClient(cfg.Endpoint)
	response, err := client.Get("http://localhost/v1/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("health status/content type = %d/%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	var health api.Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !health.ProcessAlive || !health.Ready || health.Mode != "healthy" || health.SchemaCompatibility != "compatible" || health.FTSState != "ready" {
		t.Fatalf("unexpected health: %+v", health)
	}

	second, err := Start(context.Background(), cfg)
	if second != nil {
		second.Close(context.Background())
	}
	if !errors.Is(err, instance.ErrAlreadyRunning) {
		t.Fatalf("second Start() error = %v; want ErrAlreadyRunning", err)
	}
	if _, err := os.Stat(cfg.Endpoint); err != nil {
		t.Fatalf("second instance replaced active socket: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(cfg.Endpoint); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after close: %v", err)
	}
	lock, err := instance.Acquire(filepath.Join(cfg.DataDir, "daemon.lock"))
	if err != nil {
		t.Fatalf("lock remains after close: %v", err)
	}
	lock.Close()
}

func TestDaemonRemovesStaleSocket(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	if err := os.MkdirAll(cfg.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start() with stale socket error = %v", err)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	runtimeDir := filepath.Join(root, "runtime")
	return config.Config{
		DataDir:             dataDir,
		RuntimeDir:          runtimeDir,
		DatabasePath:        filepath.Join(dataDir, "dmem.db"),
		Endpoint:            filepath.Join(runtimeDir, "dmem.sock"),
		RequestTimeout:      5 * time.Second,
		ShutdownGracePeriod: 5 * time.Second,
		MaxConnections:      4,
		WorkerCount:         2,
		MaxRequestSize:      4 * 1024 * 1024,
		QueueSoftLimit:      10000,
		QueueHardLimit:      20000,
		MaxAttempts:         3,
		LeaseDuration:       5 * time.Second,
		PerJobTimeout:       10 * time.Second,
		RetryInitialBackoff: 100 * time.Millisecond,
		RetryMaxBackoff:     time.Second,
		ProcessingVersion:   "phase1-noop-v1",
	}
}

func unixClient(endpoint string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
		},
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}
