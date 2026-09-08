package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/N0rthHXD/dMem/internal/aggregate"
	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/application"
	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/identity"
	"github.com/N0rthHXD/dMem/internal/instance"
	"github.com/N0rthHXD/dMem/internal/jobs"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type Daemon struct {
	lock             *instance.Lock
	store            *storage.Store
	repository       *storage.Repository
	aggregateService *aggregate.Service
	manager          *config.Manager
	listener         net.Listener
	server           *http.Server
	handler          *api.Handler
	jobs             *jobs.Service
	endpoint         string
	socketInfo       os.FileInfo
	reloadMu         sync.Mutex
	lastAttempt      config.ActivationResult
	closeOnce        sync.Once
	closeErr         error
	applySnapshotErr error
}

func Start(ctx context.Context, cfg config.Config) (_ *Daemon, err error) {
	processor := jobs.Processor(jobs.NoopProcessor{})
	var aggregateProvider domain.AggregateProvider
	if cfg.ProcessingProvider.Enabled {
		client, err := provider.New(cfg.ProcessingProvider, nil)
		if err != nil {
			return nil, err
		}
		processor = jobs.NewProviderProcessor(client)
		aggregateProvider = client
	}
	return startService(ctx, cfg, processor, aggregateProvider, true)
}

// StartManaged starts a daemon from the manager's immutable active snapshot.
func StartManaged(ctx context.Context, manager *config.Manager) (_ *Daemon, err error) {
	if manager == nil {
		return nil, errors.New("configuration manager is required")
	}
	if manager.Snapshot() == nil {
		result := manager.Reload(ctx, config.TriggerStartup)
		if result.Outcome != config.OutcomeActivated && result.Outcome != config.OutcomeNoChange {
			return nil, fmt.Errorf("activate startup configuration: %s", result.ErrorCode)
		}
	}
	snapshot := manager.Snapshot()
	if snapshot == nil {
		return nil, errors.New("configuration manager has no active snapshot")
	}
	base := snapshot.Config
	base.ProcessingProvider = config.ProviderConfig{}
	base.EmbeddingProvider = config.ProviderConfig{}
	d, err := startService(ctx, base, jobs.NoopProcessor{}, nil, false)
	if err != nil {
		return nil, err
	}
	d.manager = manager
	if err := d.applySnapshot(ctx, snapshot, nil); err != nil {
		_ = d.Close(context.Background())
		return nil, err
	}
	if err := d.recordActivation(ctx, config.ActivationResult{
		AttemptID:         snapshot.ActivationID,
		Outcome:           config.OutcomeActivated,
		Trigger:           config.TriggerStartup,
		SourceIdentity:    snapshot.Config.SourceIdentity,
		EffectiveIdentity: snapshot.Config.EffectiveIdentity,
		StartedAt:         snapshot.ActivatedAt,
		FinishedAt:        snapshot.ActivatedAt,
	}, nil, snapshot); err != nil {
		_ = d.Close(context.Background())
		return nil, err
	}
	d.lastAttempt = config.ActivationResult{AttemptID: snapshot.ActivationID, Outcome: config.OutcomeActivated, Trigger: config.TriggerStartup, SourceIdentity: snapshot.Config.SourceIdentity, EffectiveIdentity: snapshot.Config.EffectiveIdentity, StartedAt: snapshot.ActivatedAt, FinishedAt: snapshot.ActivatedAt}
	if err := d.jobs.Start(ctx); err != nil {
		_ = d.Close(context.Background())
		return nil, err
	}
	return d, nil
}

func start(ctx context.Context, cfg config.Config, processor jobs.Processor) (_ *Daemon, err error) {
	return startService(ctx, cfg, processor, nil, true)
}

// StartStaging starts the production service assembly without workers or providers.
func StartStaging(ctx context.Context, cfg config.Config) (_ *Daemon, err error) {
	cfg.ProcessingProvider = config.ProviderConfig{}
	cfg.EmbeddingProvider = config.ProviderConfig{}
	return startService(ctx, cfg, jobs.NoopProcessor{}, nil, false)
}

func startService(ctx context.Context, cfg config.Config, processor jobs.Processor, aggregateProvider domain.AggregateProvider, startWorkers bool) (_ *Daemon, err error) {
	if err := prepareDir(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare data directory: %w", err)
	}
	if err := prepareDir(cfg.RuntimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare runtime directory: %w", err)
	}

	lock, err := instance.Acquire(filepath.Join(cfg.DataDir, "daemon.lock"))
	if err != nil {
		return nil, err
	}
	d := &Daemon{lock: lock, endpoint: cfg.Endpoint}
	defer func() {
		if err != nil {
			_ = d.cleanup()
		}
	}()

	d.store, err = storage.Open(ctx, cfg.DatabasePath, storage.Options{ReadConnections: cfg.WorkerCount})
	if err != nil {
		return nil, err
	}
	ftsState, err := d.store.IndexState(ctx)
	if err != nil {
		return nil, fmt.Errorf("read FTS state: %w", err)
	}
	repository := storage.NewRepository(d.store, domain.Runtime{Now: time.Now, ID: identity.UUID}, cfg.QueueSoftLimit, cfg.QueueHardLimit, cfg.MaxAttempts, cfg.ProcessingVersion)
	d.repository = repository
	repository.SetDiskFreeThresholds(cfg.DiskSoftFree, cfg.DiskHardFree)
	if cfg.EmbeddingProvider.Enabled {
		embedding, providerErr := provider.New(cfg.EmbeddingProvider, nil)
		if providerErr != nil {
			return nil, providerErr
		}
		repository.ConfigureSemantic(embedding, cfg.EmbeddingProvider.Kind, cfg.EmbeddingProvider.Model)
		if err := repository.EnsureSemanticRecoveryJobs(ctx); err != nil {
			return nil, fmt.Errorf("prepare semantic recovery: %w", err)
		}
	}
	policyIdentity := config.PolicyIdentity(cfg.ProcessingProvider.Policy)
	if !cfg.ProcessingProvider.Enabled {
		policyIdentity = ""
	}
	aggregateService := aggregate.NewService(repository, aggregateProvider, "aggregate/provider", policyIdentity, cfg.ProcessingVersion, cfg.ProcessingProvider.Limits.MaxContextItems)
	d.aggregateService = aggregateService
	d.jobs, err = jobs.NewService(repository, cfg, processor, jobs.Dependencies{
		Indexes: repository, EntityIndexes: repository, RelationIndexes: repository,
		FeedbackAggregates: repository, AggregateRebuilder: aggregateService, DiskPressure: repository,
	})
	if err != nil {
		return nil, err
	}
	if startWorkers {
		if err := d.jobs.Start(ctx); err != nil {
			return nil, err
		}
	}
	if err := clearStaleSocket(cfg.Endpoint); err != nil {
		return nil, err
	}
	d.listener, err = net.Listen("unix", cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen on Unix socket: %w", err)
	}
	if err := os.Chmod(cfg.Endpoint, 0o600); err != nil {
		return nil, fmt.Errorf("set socket permissions: %w", err)
	}
	d.socketInfo, err = os.Lstat(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("inspect Unix socket: %w", err)
	}
	d.listener = limitListener(d.listener, cfg.MaxConnections)
	d.handler = api.NewHandler(ftsState, api.HandlerDependencies{
		Repository: repository, AggregateService: aggregateService, ProcessorHealth: d.jobs,
		MemoryService:  application.NewService(repository),
		MaxRequestSize: cfg.MaxRequestSize, ID: identity.UUID, Configuration: d,
	})
	d.server = &http.Server{
		Handler:           d.handler.Routes(),
		ReadHeaderTimeout: cfg.RequestTimeout,
		ReadTimeout:       cfg.RequestTimeout,
		WriteTimeout:      cfg.RequestTimeout,
		IdleTimeout:       cfg.RequestTimeout,
	}
	listener := d.listener
	go func() {
		if serveErr := d.server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			d.handler.SetReady(false)
		}
	}()
	return d, nil
}

// Reload applies one complete candidate file revision. Rejected and no-change
// outcomes leave all runtime capability snapshots untouched.
func (d *Daemon) Reload(ctx context.Context, trigger config.ActivationTrigger) (config.ActivationResult, error) {
	if d.manager == nil {
		return config.ActivationResult{Outcome: config.OutcomeFailed, Trigger: trigger, ErrorCode: "config_unmanaged"}, errors.New("daemon was not started with a configuration manager")
	}
	d.reloadMu.Lock()
	defer d.reloadMu.Unlock()
	previous := d.manager.Snapshot()
	result := d.manager.Reload(ctx, trigger)
	d.lastAttempt = result
	if result.Outcome != config.OutcomeActivated {
		if err := d.recordNonActivatedActivation(ctx, result, previous); err != nil {
			return result, err
		}
		return result, nil
	}
	snapshot := d.manager.Snapshot()
	if err := d.applySnapshot(ctx, snapshot, previous); err != nil {
		if previous != nil {
			d.manager.Revert(previous)
			_ = d.applySnapshot(ctx, previous, nil)
		}
		return result, err
	}
	if err := d.recordActivation(ctx, result, previous, snapshot); err != nil {
		return result, err
	}
	return result, nil
}

func (d *Daemon) ConfigurationStatus() (*config.Snapshot, config.ActivationResult) {
	d.reloadMu.Lock()
	defer d.reloadMu.Unlock()
	if d.manager == nil {
		return nil, config.ActivationResult{}
	}
	return d.manager.Snapshot(), d.lastAttempt
}

func (d *Daemon) ReloadConfiguration(ctx context.Context) (config.ActivationResult, error) {
	return d.Reload(ctx, config.TriggerAPI)
}

func (d *Daemon) recordActivation(ctx context.Context, result config.ActivationResult, previous, snapshot *config.Snapshot) error {
	if d.repository == nil || snapshot == nil || result.AttemptID == "" {
		return nil
	}
	redacted, err := snapshot.Redacted().JSON()
	if err != nil {
		return err
	}
	previousActivationID := ""
	previousPolicyGenerations := map[string]int{}
	if previous != nil {
		previousActivationID = previous.ActivationID
		if states, err := d.repository.CurrentCapabilityStates(ctx); err == nil {
			for capability, state := range states {
				previousPolicyGenerations[capability] = state.PolicyGeneration
			}
		}
	}
	processingPolicyGeneration := d.policyGeneration(ctx, previous, snapshot, "processing")
	embeddingPolicyGeneration := d.policyGeneration(ctx, previous, snapshot, "embedding")
	commit := storage.ActivationCommit{
		Revision: storage.RevisionRecord{
			RevisionID:        result.AttemptID,
			SourceIdentity:    result.SourceIdentity,
			EffectiveIdentity: result.EffectiveIdentity,
			ConfigVersion:     snapshot.Config.ConfigVersion,
			LoadedAt:          result.FinishedAt,
			RedactedDocument:  redacted,
		},
		Activation: storage.ActivationRecord{
			ActivationID:         result.AttemptID,
			RevisionID:           result.AttemptID,
			PreviousActivationID: previousActivationID,
			Trigger:              string(result.Trigger),
			RequestedBy:          "daemon",
			Outcome:              string(result.Outcome),
			ErrorCode:            result.ErrorCode,
			StartedAt:            result.StartedAt,
			FinishedAt:           result.FinishedAt,
		},
		Capabilities: []storage.CapabilityStateRecord{
			capabilityState("processing", snapshot.ProcessingCapability, snapshot.ActivationGeneration, processingPolicyGeneration),
			capabilityState("embedding", snapshot.EmbeddingCapability, snapshot.ActivationGeneration, embeddingPolicyGeneration),
		},
	}
	for _, capability := range []struct {
		name     string
		snapshot config.CapabilitySnapshot
	}{{"processing", snapshot.ProcessingCapability}, {"embedding", snapshot.EmbeddingCapability}} {
		if !capability.snapshot.Enabled {
			continue
		}
		commit.Policies = append(commit.Policies, storage.ProviderPolicyRecord{
			PolicyIdentity: capability.snapshot.PolicyIdentity, Capability: capability.name, DefaultDecision: capability.snapshot.Policy.Default,
			AllowedContentTypes: capability.snapshot.Policy.ContentTypes, AllowedSourceTypes: capability.snapshot.Policy.SourceTypes,
			AllowedSensitivities: capability.snapshot.Policy.Sensitivities, QueryAllowed: capability.snapshot.Policy.Query == "allow", CreatedAt: result.FinishedAt,
		})
		for i, assignment := range capability.snapshot.Policy.Assignments {
			commit.Assignments = append(commit.Assignments, storage.PolicyAssignmentRecord{
				AssignmentID: policyAssignmentID(capability.snapshot.PolicyIdentity, capability.name, i), PolicyIdentity: capability.snapshot.PolicyIdentity,
				Capability: capability.name, SelectorKind: assignment.Selector, SpaceID: assignment.SpaceID, SpaceType: assignment.SpaceType,
				ExternalID: assignment.ExternalID, Effect: assignment.Effect, IncludeDescendants: assignment.IncludeDescendants, Priority: assignment.Priority,
			})
		}
	}
	previousGeneration := 0
	if previous != nil {
		previousGeneration = previous.ActivationGeneration
	}
	for _, class := range result.ReconciliationClasses {
		capability, changeClass, ok := strings.Cut(class, ":")
		if !ok {
			continue
		}
		fromPolicyGeneration, toPolicyGeneration := 0, 0
		targetPolicyIdentity := ""
		switch capability {
		case "processing":
			toPolicyGeneration = processingPolicyGeneration
			targetPolicyIdentity = snapshot.ProcessingCapability.PolicyIdentity
			fromPolicyGeneration = previousPolicyGenerations[capability]
		case "embedding":
			toPolicyGeneration = embeddingPolicyGeneration
			targetPolicyIdentity = snapshot.EmbeddingCapability.PolicyIdentity
			fromPolicyGeneration = previousPolicyGenerations[capability]
		}
		commit.ReconciliationRuns = append(commit.ReconciliationRuns, storage.ReconciliationRunRecord{
			RunID:                       result.AttemptID + "-" + capability + "-" + changeClass,
			Capability:                  capability,
			ChangeClass:                 changeClass,
			FromConfigurationGeneration: previousGeneration,
			ToConfigurationGeneration:   snapshot.ActivationGeneration,
			FromPolicyGeneration:        fromPolicyGeneration,
			ToPolicyGeneration:          toPolicyGeneration,
			TargetPolicyIdentity:        targetPolicyIdentity,
		})
	}
	return d.repository.CommitActivation(ctx, commit)
}

func (d *Daemon) recordNonActivatedActivation(ctx context.Context, result config.ActivationResult, previous *config.Snapshot) error {
	if d.repository == nil || result.AttemptID == "" {
		return nil
	}
	previousActivationID := ""
	if previous != nil {
		previousActivationID = previous.ActivationID
	}
	var diagnostics []byte
	if len(result.Diagnostics) > 0 {
		encoded, err := json.Marshal(result.Diagnostics)
		if err != nil {
			return err
		}
		diagnostics = encoded
	}
	commit := storage.ActivationCommit{
		Revision: storage.RevisionRecord{
			RevisionID:        result.AttemptID,
			SourceIdentity:    result.SourceIdentity,
			EffectiveIdentity: result.EffectiveIdentity,
			ConfigVersion:     1,
			LoadedAt:          result.FinishedAt,
		},
		Activation: storage.ActivationRecord{
			ActivationID:         result.AttemptID,
			RevisionID:            result.AttemptID,
			PreviousActivationID: previousActivationID,
			Trigger:               string(result.Trigger),
			RequestedBy:           "daemon",
			Outcome:               string(result.Outcome),
			ErrorCode:             result.ErrorCode,
			Diagnostics:           diagnostics,
			StartedAt:             result.StartedAt,
			FinishedAt:            result.FinishedAt,
		},
	}
	return d.repository.CommitActivation(ctx, commit)
}

func capabilityState(capability string, snapshot config.CapabilitySnapshot, generation, policyGeneration int) storage.CapabilityStateRecord {
	state := "disabled"
	if snapshot.Enabled {
		state = "ready"
	}
	return storage.CapabilityStateRecord{
		Capability:              capability,
		Enabled:                 snapshot.Enabled,
		ConfigurationIdentity:   snapshot.ConfigurationIdentity,
		ConfigurationGeneration: generation,
		CompatibilityIdentity:   snapshot.CompatibilityIdentity,
		PolicyIdentity:          snapshot.PolicyIdentity,
		PolicyGeneration:        policyGeneration,
		HealthState:             state,
		ReconciliationState:     "idle",
	}
}

func (d *Daemon) policyGeneration(ctx context.Context, previous, snapshot *config.Snapshot, capability string) int {
	if previous == nil {
		return snapshot.ActivationGeneration
	}
	var before, after config.CapabilitySnapshot
	if capability == "processing" {
		before, after = previous.ProcessingCapability, snapshot.ProcessingCapability
	} else {
		before, after = previous.EmbeddingCapability, snapshot.EmbeddingCapability
	}
	if before.Enabled && after.Enabled && before.PolicyIdentity == after.PolicyIdentity {
		if states, err := d.repository.CurrentCapabilityStates(ctx); err == nil {
			if state, ok := states[capability]; ok && state.PolicyGeneration > 0 {
				return state.PolicyGeneration
			}
		}
		return previous.ActivationGeneration
	}
	return snapshot.ActivationGeneration
}

func policyAssignmentID(policyIdentity, capability string, index int) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", policyIdentity, capability, index)))
	return fmt.Sprintf("policy-%x", hash[:])
}

func (d *Daemon) applySnapshot(ctx context.Context, snapshot, previous *config.Snapshot) error {
	if err := d.applySnapshotErr; err != nil {
		d.applySnapshotErr = nil
		return err
	}
	if snapshot == nil || d.jobs == nil || d.aggregateService == nil || d.repository == nil {
		return errors.New("daemon runtime is incomplete")
	}
	var processor jobs.Processor = jobs.NoopProcessor{}
	var aggregateProvider domain.AggregateProvider
	processingIdentity := "none"
	if snapshot.ProcessingCapability.Enabled {
		client, err := provider.New(snapshot.Config.ProcessingProvider, nil)
		if err != nil {
			return fmt.Errorf("build processing provider: %w", err)
		}
		processor = jobs.NewProviderProcessor(client)
		aggregateProvider = client
		processingIdentity = processor.Identity()
	}

	var embedding *provider.Client
	if snapshot.EmbeddingCapability.Enabled {
		client, err := provider.New(snapshot.Config.EmbeddingProvider, nil)
		if err != nil {
			return fmt.Errorf("build embedding provider: %w", err)
		}
		embedding = client
	}

	processingPolicyGeneration := d.policyGeneration(ctx, previous, snapshot, "processing")
	embeddingPolicyGeneration := d.policyGeneration(ctx, previous, snapshot, "embedding")
	d.repository.SetProcessingGeneration(snapshot.ActivationGeneration, processingPolicyGeneration, snapshot.ProcessingCapability.CompatibilityIdentity)
	d.jobs.SetProcessing(jobs.ProcessingConfig{
		Processor:              processor,
		ProcessorIdentity:      processingIdentity,
		Enabled:                snapshot.ProcessingCapability.Enabled,
		MaxContentChars:        snapshot.ProcessingCapability.Limits.MaxCandidateChars,
		MaxTotalCandidateChars: snapshot.ProcessingCapability.Limits.MaxTotalCandidateChars,
		MaxContextItems:        snapshot.ProcessingCapability.Limits.MaxContextItems,
		Generation:             snapshot.ActivationGeneration,
		PolicyGeneration:       processingPolicyGeneration,
		CompatibilityIdentity:  snapshot.ProcessingCapability.CompatibilityIdentity,
		PolicyIdentity:         snapshot.ProcessingCapability.PolicyIdentity,
		ProcessingVersion:      snapshot.Config.ProcessingVersion,
	})
	d.aggregateService.SetProcessing(
		aggregateProvider,
		processingIdentity,
		snapshot.ProcessingCapability.PolicyIdentity,
		snapshot.Config.ProcessingVersion,
		snapshot.ProcessingCapability.Limits.MaxContextItems,
		snapshot.ActivationGeneration,
		processingPolicyGeneration,
	)
	if embedding == nil {
		d.repository.SetEmbedding(nil, "", "", "", "", 0, 0)
		return nil
	}
	d.repository.SetEmbedding(
		embedding,
		snapshot.EmbeddingCapability.Kind,
		snapshot.EmbeddingCapability.Model,
		snapshot.EmbeddingCapability.CompatibilityIdentity,
		snapshot.EmbeddingCapability.ConfigurationIdentity,
		snapshot.ActivationGeneration,
		embeddingPolicyGeneration,
	)
	if err := d.repository.EnsureSemanticRecoveryJobs(ctx); err != nil {
		return fmt.Errorf("prepare semantic recovery: %w", err)
	}
	return nil
}

func (d *Daemon) Close(ctx context.Context) error {
	d.closeOnce.Do(func() {
		if d.handler != nil {
			d.handler.SetReady(false)
		}
		if d.server != nil {
			d.closeErr = d.server.Shutdown(ctx)
		}
		if d.jobs != nil {
			d.closeErr = errors.Join(d.closeErr, d.jobs.Shutdown(ctx))
			d.jobs = nil
		}
		d.closeErr = errors.Join(d.closeErr, d.cleanup())
	})
	return d.closeErr
}

func (d *Daemon) cleanup() error {
	var errs []error
	if d.listener != nil {
		if err := d.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
		d.listener = nil
	}
	if d.jobs != nil {
		errs = append(errs, d.jobs.Shutdown(context.Background()))
		d.jobs = nil
	}
	if d.store != nil {
		errs = append(errs, d.store.Close())
		d.store = nil
	}
	if d.socketInfo != nil {
		if current, err := os.Lstat(d.endpoint); err == nil && os.SameFile(current, d.socketInfo) {
			errs = append(errs, os.Remove(d.endpoint))
		}
		d.socketInfo = nil
	}
	if d.lock != nil {
		errs = append(errs, d.lock.Close())
		d.lock = nil
	}
	return errors.Join(errs...)
}

func prepareDir(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if filepath.Clean(resolved) != filepath.Clean(path) {
		return errors.New("directory path must not contain symlinks")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("directory must be owned by the current user")
	}
	return os.Chmod(path, mode)
}

func clearStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing endpoint: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("existing endpoint is not a Unix socket")
	}
	conn, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		return errors.New("Unix socket is already served by an active process")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale Unix socket: %w", err)
	}
	return nil
}

type limitedListener struct {
	net.Listener
	sem       chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func limitListener(listener net.Listener, maximum int) net.Listener {
	return &limitedListener{
		Listener: listener,
		sem:      make(chan struct{}, maximum),
		done:     make(chan struct{}),
	}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.sem <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: conn, release: func() { <-l.sem }}, nil
}

func (l *limitedListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type limitedConn struct {
	net.Conn
	releaseOnce sync.Once
	release     func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.releaseOnce.Do(c.release)
	return err
}
