package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
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
	if health.OverallStatus != "healthy" || health.StorageState != "ready" || health.QueueState != "ready" || health.ProcessorState != "not_configured" || health.ProcessorIdentity != "none" || health.SemanticIndexState != "not_configured" || health.BackupState != "ready" {
		t.Fatalf("unexpected dimension health: %+v", health)
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

func TestConfiguredHealthIsLocalAndDoesNotLeakConfiguration(t *testing.T) {
	cfg := testConfig(t)
	cfg.ProcessingVersion = "health-provider-v1"
	cfg.ProcessingProvider = config.ProviderConfig{
		Enabled: true, Kind: "openai-compatible", Endpoint: "https://provider.invalid/private/path", Model: "health-model",
		SecretEnv: "DMEM_HEALTH_TEST_SECRET", AllowedSpaces: []string{"11111111-1111-4111-8111-111111111111"}, AllowedContentTypes: []string{"text/plain"},
		AllowedSourceTypes: []string{"manual"}, AllowedSensitivities: []string{"normal"}, TelemetryDisabled: true,
		EgressPolicyVersion: "health-v1", MaxContentChars: 1000, MaxContextItems: 1, MaxResponseSize: 4096,
		RequestTimeout: time.Second, MaxConcurrent: 1, RequestsPerMinute: 60,
	}
	t.Setenv(cfg.ProcessingProvider.SecretEnv, "health-secret-value")
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())

	response, err := unixClient(cfg.Endpoint).Get("http://localhost/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	var health api.Health
	if err := json.Unmarshal(raw, &health); err != nil {
		t.Fatal(err)
	}
	if health.ProcessorState != "degraded" || health.ProcessorIdentity != "openai-compatible/health-model" || health.SemanticIndexState != "not_configured" || health.QueryAvailable != true || health.Mode != "degraded" {
		t.Fatalf("configured health = %+v", health)
	}
	text := string(raw)
	for _, forbidden := range []string{cfg.DataDir, cfg.DatabasePath, cfg.Endpoint, cfg.ProcessingProvider.Endpoint, cfg.ProcessingProvider.SecretEnv, "health-secret-value"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("health leaked configuration %q: %s", forbidden, text)
		}
	}
}

func TestEmbeddingProviderDoesNotConfigureProcessing(t *testing.T) {
	cfg := testConfig(t)
	cfg.EmbeddingProvider = config.ProviderConfig{
		Enabled: true, Kind: "openai-compatible", Endpoint: "https://embedding.invalid/v1", Model: "embedding-model",
		SecretEnv: "DMEM_EMBEDDING_TEST_SECRET", AllowedSpaces: []string{"11111111-1111-4111-8111-111111111111"}, AllowedContentTypes: []string{"text/plain"},
		AllowedSourceTypes: []string{"manual"}, AllowedSensitivities: []string{"normal"}, TelemetryDisabled: true,
		EgressPolicyVersion: "embedding-v1", MaxContentChars: 1000, MaxResponseSize: 4096,
		RequestTimeout: time.Second, MaxConcurrent: 1, RequestsPerMinute: 60,
	}
	t.Setenv(cfg.EmbeddingProvider.SecretEnv, "embedding-secret-value")
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())

	response, err := unixClient(cfg.Endpoint).Get("http://localhost/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var health api.Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.ProcessorState != "not_configured" || health.ProcessorIdentity != "none" || health.SemanticIndexState != "unavailable" {
		t.Fatalf("embedding-only health = %+v", health)
	}
}

func TestStartStagingClearsBothProviders(t *testing.T) {
	cfg := testConfig(t)
	cfg.ProcessingProvider = config.ProviderConfig{Enabled: true, SecretEnv: "DMEM_MISSING_PROCESSING_SECRET"}
	cfg.EmbeddingProvider = config.ProviderConfig{Enabled: true, SecretEnv: "DMEM_MISSING_EMBEDDING_SECRET"}
	d, err := StartStaging(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProcessingVersionChangeAfterRestartCreatesDistinctJobResult(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	parse := func(processingVersion string) config.Config {
		t.Helper()
		cfg, err := config.Parse(flag.NewFlagSet("test", flag.ContinueOnError), []string{
			"-data-dir", dataDir,
			"-runtime-dir", filepath.Join(root, "runtime"),
			"-processing-version", processingVersion,
			"-disk-soft-free", "2", "-disk-hard-free", "1",
		})
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	firstConfig := parse("extract-rules-v1")
	first, err := Start(context.Background(), firstConfig)
	if err != nil {
		t.Fatal(err)
	}
	client := unixClient(firstConfig.Endpoint)
	space := createSpace(t, client, "version-root", map[string]any{"space_type": "user", "external_id": "version-root", "display_name": "Version Root"})
	event := ingestManualEvent(t, client, "version-event", space.SpaceID, "versioned processing")
	firstJob := waitForJobStatus(t, client, event.JobIDs[0], "succeeded")
	if firstJob.ResultCode == nil || *firstJob.ResultCode != "no_processor" {
		t.Fatalf("first job = %+v", firstJob)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	secondConfig := parse("extract-rules-v2")
	second, err := Start(context.Background(), secondConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	client = unixClient(secondConfig.Endpoint)
	response := requestJSON(t, client, http.MethodPost, "/v1/events/"+event.EventID+"/replay", "version-v2-replay", nil)
	if response.StatusCode != http.StatusAccepted || response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("new-version replay status/header = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var replay domain.JobReference
	decodeResponse(t, response, &replay)
	if replay.JobID == firstJob.JobID {
		t.Fatalf("new-version replay reused job %q", replay.JobID)
	}
	secondJob := waitForJobStatus(t, client, replay.JobID, "succeeded")
	if secondJob.ResultCode == nil || *secondJob.ResultCode != "no_processor" {
		t.Fatalf("second job = %+v", secondJob)
	}

	db, err := sql.Open("sqlite3", secondConfig.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT job_id, processing_version FROM jobs WHERE target_id=? AND status='succeeded' AND result_code='no_processor' ORDER BY processing_version`, event.EventID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	versions := map[string]string{}
	for rows.Next() {
		var jobID, processingVersion string
		if err := rows.Scan(&jobID, &processingVersion); err != nil {
			t.Fatal(err)
		}
		versions[jobID] = processingVersion
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[firstJob.JobID] != "extract-rules-v1" || versions[secondJob.JobID] != "extract-rules-v2" {
		t.Fatalf("persisted versioned results = %#v", versions)
	}
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
		ProcessingVersion:   config.DefaultProcessingVersion,
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
