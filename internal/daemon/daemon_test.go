package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/instance"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

func TestRetrievalContractsMapEffectiveConfiguration(t *testing.T) {
	cfg := config.Config{
		Semantic: config.SemanticConfig{CalibrationVersion: "semantic-custom-v1", AbsoluteFloor: 0.41, TopMargin: 0.07},
		Graph:    config.GraphConfig{Policy: "direct", MinEntityConfidence: 0, MinFactConfidence: 0.72, BoostCap: 0.04},
		Ranking: config.RankingConfig{
			Version: "ranking-custom-v1", SemanticWeight: 1.2, KeywordWeight: 0.3,
			ExactWeight: 0.2, TemporalWeight: 0.15, QualityWeight: 0.12, AgreementWeight: 0.06,
		},
	}
	weights, calibration, policy := retrievalContracts(cfg)
	if weights.Version != cfg.Ranking.Version || weights.Semantic != 1.2 || weights.Keyword != 0.3 || weights.Exact != 0.2 || weights.Temporal != 0.15 || weights.Quality != 0.12 || weights.Agreement != 0.06 {
		t.Fatalf("ranking contracts = %+v", weights)
	}
	if weights.GraphBoostGraphCap != 0.04 || weights.GraphOnlyAdmissionFloor != retrieval.DefaultWeights().GraphOnlyAdmissionFloor {
		t.Fatalf("graph ranking contracts = %+v", weights)
	}
	if calibration.Version != "semantic-custom-v1" || calibration.AbsoluteFloor != 0.41 || calibration.TopMargin != 0.07 {
		t.Fatalf("semantic calibration = %+v", calibration)
	}
	if policy != retrieval.GraphDirect {
		t.Fatalf("graph policy = %q", policy)
	}
}

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
	if health.OverallStatus != "healthy" || health.StorageState != "ready" || health.StorageIntegrityState == "" || health.QueueState != "ready" || health.ProcessorState != "not_configured" || health.ProcessorIdentity != "none" || health.SemanticIndexState != "not_configured" || health.BackupState != "ready" {
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
		TelemetryDisabled: true,
		Secret:            provider.SecretReference{Source: "environment", Name: "DMEM_HEALTH_TEST_SECRET"},
		Limits: provider.Limits{MaxEgressInputChars: 1000, MaxContextItems: 1, MaxResponseBytes: 4096,
			RequestTimeout: time.Second, MaxConcurrent: 1, RequestsPerMinute: 60},
		Policy: provider.Policy{
			Default:       "deny",
			ContentTypes:  []string{"text/plain"},
			SourceTypes:   []string{"manual"},
			Sensitivities: []string{"normal"},
			Assignments: []provider.PolicyAssignment{
				{Selector: "space", SpaceID: "11111111-1111-4111-8111-111111111111", Effect: "allow"},
			},
		},
	}
	t.Setenv(cfg.ProcessingProvider.Secret.Name, "health-secret-value")
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
	for _, forbidden := range []string{cfg.DataDir, cfg.DatabasePath, cfg.Endpoint, cfg.ProcessingProvider.Endpoint, cfg.ProcessingProvider.Secret.Name, "health-secret-value"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("health leaked configuration %q: %s", forbidden, text)
		}
	}
}

func TestManagedConfigurationStatusIsRedactedAndRecordsSIGHUP(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	cfg := reloadConfig{dataDir: filepath.Join(root, "data"), runtimeDir: filepath.Join(root, "runtime")}
	writeReloadConfig(t, path, cfg)
	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())

	result, err := d.Reload(context.Background(), config.TriggerSIGHUP)
	if err != nil || result.Outcome != config.OutcomeNoChange || result.Trigger != config.TriggerSIGHUP {
		t.Fatalf("SIGHUP reload = %+v, %v", result, err)
	}
	request, err := http.NewRequest(http.MethodGet, "http://localhost/v1/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Caller-ID", "daemon-test")
	response, err := unixClient(d.endpoint).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{path, cfg.dataDir, cfg.runtimeDir, "provider.invalid", "DMEM_RELOAD_PROCESSING_SECRET", "secret", "query"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("configuration status leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"trigger":"sighup"`) || !strings.Contains(text, `"reconciliation_state":"idle"`) {
		t.Fatalf("configuration status = %s", text)
	}
}

func TestEmbeddingProviderDoesNotConfigureProcessing(t *testing.T) {
	cfg := testConfig(t)
	cfg.EmbeddingProvider = config.ProviderConfig{
		Enabled: true, Kind: "openai-compatible", Endpoint: "https://embedding.invalid/v1", Model: "embedding-model",
		TelemetryDisabled: true,
		Secret:            provider.SecretReference{Source: "environment", Name: "DMEM_EMBEDDING_TEST_SECRET"},
		Limits: provider.Limits{MaxEgressInputChars: 1000, MaxResponseBytes: 4096,
			RequestTimeout: time.Second, MaxConcurrent: 1, RequestsPerMinute: 60},
		Policy: provider.Policy{
			Default:       "deny",
			ContentTypes:  []string{"text/plain"},
			SourceTypes:   []string{"manual"},
			Sensitivities: []string{"normal"},
			Assignments: []provider.PolicyAssignment{
				{Selector: "space", SpaceID: "11111111-1111-4111-8111-111111111111", Effect: "allow"},
			},
		},
	}
	t.Setenv(cfg.EmbeddingProvider.Secret.Name, "embedding-secret-value")
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
	cfg.ProcessingProvider = config.ProviderConfig{Enabled: true, Secret: provider.SecretReference{Source: "environment", Name: "DMEM_MISSING_PROCESSING_SECRET"}}
	cfg.EmbeddingProvider = config.ProviderConfig{Enabled: true, Secret: provider.SecretReference{Source: "environment", Name: "DMEM_MISSING_EMBEDDING_SECRET"}}
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
	runtimeDir := filepath.Join(root, "runtime")
	parse := func(processingVersion string) config.Config {
		t.Helper()
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
			ProcessingVersion:   processingVersion,
			DiskSoftFree:        2,
			DiskHardFree:        1,
		}
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

func TestSIGHUPReloadRecordsTriggerAndConcurrentReloadsAreSerialized(t *testing.T) {
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

	result, err := d.Reload(context.Background(), config.TriggerSIGHUP)
	if err != nil || result.Outcome != config.OutcomeNoChange || result.Trigger != config.TriggerSIGHUP {
		t.Fatalf("SIGHUP reload = %+v, %v", result, err)
	}

	_, attempt := d.ConfigurationStatus()
	if attempt.Trigger != config.TriggerSIGHUP {
		t.Fatalf("status trigger = %q; want sighup", attempt.Trigger)
	}

	processing := initial
	processing.processing = true
	writeReloadConfig(t, path, processing)

	concurrent := 5
	results := make(chan config.ActivationResult, concurrent)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, _ := d.Reload(context.Background(), config.TriggerSIGHUP)
			results <- r
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	activatedCount := 0
	for r := range results {
		if r.Trigger != config.TriggerSIGHUP {
			t.Fatalf("concurrent reload trigger = %q; want sighup", r.Trigger)
		}
		if r.Outcome == config.OutcomeActivated {
			activatedCount++
		}
	}
	if activatedCount != 1 {
		t.Fatalf("concurrent SIGHUP reloads produced %d activations; want exactly 1 (serialized)", activatedCount)
	}
}

func TestApplySnapshotFailurePreservesPreviousEffectiveConfig(t *testing.T) {
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

	processing := initial
	processing.processing = true
	writeReloadConfig(t, path, processing)
	result, err := d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("processing reload = %+v, %v", result, err)
	}
	previousIdentity := d.manager.Snapshot().Config.EffectiveIdentity
	previousGeneration := d.manager.ActivationGeneration()

	d.applySnapshotErr = errors.New("injected applySnapshot failure")
	embedding := processing
	embedding.embedding = true
	writeReloadConfig(t, path, embedding)
	result, err = d.Reload(context.Background(), config.TriggerAPI)
	if err == nil {
		t.Fatal("expected applySnapshot failure error")
	}

	if d.manager.Snapshot().Config.EffectiveIdentity != previousIdentity {
		t.Fatalf("effective identity = %q; want %q (previous should be preserved)", d.manager.Snapshot().Config.EffectiveIdentity, previousIdentity)
	}
	if d.manager.ActivationGeneration() != previousGeneration {
		t.Fatalf("activation generation = %d; want %d (reverted)", d.manager.ActivationGeneration(), previousGeneration)
	}
	health := managedHealth(t, d)
	if health.ProcessorState != "degraded" || health.ProcessorIdentity != "openai-compatible/processing-a" {
		t.Fatalf("previous processing capability not preserved: %+v", health)
	}
}
