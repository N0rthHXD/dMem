// Package daemon is the runtime composition root. It assembles the SQLite
// capability adapters, the Memory Gateway services, provider adapters, workers,
// and the HTTP protocol handler from one immutable configuration snapshot.
//
// The runtime exposes the assembled http.Handler but never creates a listener:
// the transport layer (internal/server) owns sockets and TLS, and authentication
// and network edge authorization remain out of scope.
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/N0rthHXD/dMem/internal/aggregate"
	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/policy"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
	"github.com/N0rthHXD/dMem/internal/identity"
	"github.com/N0rthHXD/dMem/internal/instance"
	"github.com/N0rthHXD/dMem/internal/jobs"
	"github.com/N0rthHXD/dMem/internal/protocol/httpapi"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/retrieval"
	"github.com/N0rthHXD/dMem/internal/storage"
	"github.com/N0rthHXD/dMem/pkg/logging"
)

// Daemon is the assembled runtime. It owns the storage adapters, gateway
// services, job workers, provider clients, and the protocol handler. It does
// not own a listener or an http.Server.
type Daemon struct {
	lock             *instance.Lock
	store            *storage.Store
	repository       *storage.Repository
	aggregateService *aggregate.Service
	manager          *config.Manager
	retrievalService *gwretrieval.ServiceImpl
	handler          http.Handler
	jobs             *jobs.Service
	logger           *slog.Logger
	rootLogger       *slog.Logger
	reloadMu         sync.Mutex
	lastAttempt      config.ActivationResult
	closeOnce        sync.Once
	closeErr         error
	applySnapshotErr error
}

type Option func(*Daemon)

func WithLogger(logger *slog.Logger) Option {
	return func(d *Daemon) {
		d.logger = logger
	}
}

// StartManaged starts a daemon from the manager's immutable active snapshot.
func StartManaged(ctx context.Context, manager *config.Manager, opts ...Option) (_ *Daemon, err error) {
	if manager == nil {
		return nil, errors.New("configuration manager is required")
	}
	activation := config.ActivationResult{Outcome: config.OutcomeActivated, Trigger: config.TriggerStartup}
	if manager.Snapshot() == nil {
		activation = manager.Reload(ctx, config.TriggerStartup)
		if activation.Outcome != config.OutcomeActivated && activation.Outcome != config.OutcomeNoChange {
			return nil, fmt.Errorf("activate startup configuration: %s", activation.ErrorCode)
		}
	}
	snapshot := manager.Snapshot()
	if snapshot == nil {
		return nil, errors.New("configuration manager has no active snapshot")
	}
	if activation.AttemptID == "" {
		activation.AttemptID = snapshot.ActivationID
		activation.SourceIdentity = snapshot.Config.SourceIdentity
		activation.EffectiveIdentity = snapshot.Config.EffectiveIdentity
		activation.StartedAt = snapshot.ActivatedAt
		activation.FinishedAt = snapshot.ActivatedAt
	}
	base := snapshot.Config
	base.ProcessingProvider = config.ProviderConfig{}
	base.EmbeddingProvider = config.ProviderConfig{}
	d, err := startService(ctx, base, nil, nil, false, opts...)
	if err != nil {
		return nil, err
	}
	d.manager = manager
	if err := d.applySnapshot(ctx, snapshot, nil); err != nil {
		_ = d.Close(context.Background())
		return nil, err
	}
	if err := d.recordActivation(ctx, activation, nil, snapshot); err != nil {
		_ = d.Close(context.Background())
		return nil, err
	}
	if err := d.repository.EnsureSemanticRecoveryJobs(ctx); err != nil {
		_ = d.Close(context.Background())
		return nil, fmt.Errorf("prepare semantic recovery: %w", err)
	}
	d.logIndexRecovery(ctx)
	d.lastAttempt = activation
	if err := d.jobs.Start(ctx); err != nil {
		_ = d.Close(context.Background())
		return nil, err
	}
	return d, nil
}

func start(ctx context.Context, cfg config.Config, processor jobs.Processor, opts ...Option) (_ *Daemon, err error) {
	return startService(ctx, cfg, processor, nil, true, opts...)
}

// StartStaging starts the production service assembly without workers or providers.
func StartStaging(ctx context.Context, cfg config.Config, opts ...Option) (_ *Daemon, err error) {
	cfg.ProcessingProvider = config.ProviderConfig{}
	cfg.EmbeddingProvider = config.ProviderConfig{}
	return startService(ctx, cfg, nil, nil, false, opts...)
}

// Handler returns the protocol handler assembled from the gateway modules. The
// runtime does not create a listener; the caller owns transport and lifecycle.
func (d *Daemon) Handler() http.Handler {
	return d.handler
}

func startService(ctx context.Context, cfg config.Config, processor jobs.Processor, aggregateProvider domain.AggregateProvider, startWorkers bool, opts ...Option) (_ *Daemon, err error) {
	if err := prepareDir(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare data directory: %w", err)
	}

	lock, err := instance.Acquire(filepath.Join(cfg.DataDir, "daemon.lock"))
	if err != nil {
		return nil, err
	}
	d := &Daemon{lock: lock}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	d.rootLogger = d.logger
	d.logger = logging.WithComponent(d.logger, logging.ComponentDaemon)
	defer func() {
		if err != nil {
			_ = d.cleanup()
		}
	}()

	d.store, err = storage.Open(ctx, cfg.DatabasePath, storage.Options{
		ReadConnections:    cfg.WorkerCount,
		IntegrityCheckMode: storage.IntegrityCheckBackground,
	})
	if err != nil {
		return nil, err
	}
	store := d.store
	go func() {
		if err := store.WaitIntegrity(context.Background()); err != nil && !errors.Is(err, context.Canceled) && d.logger != nil {
			d.logger.Error("storage integrity check failed", logging.FieldError, err)
		}
	}()
	repository := storage.NewRepository(d.store, domain.Runtime{Now: time.Now, ID: identity.UUID}, cfg.QueueSoftLimit, cfg.QueueHardLimit, cfg.MaxAttempts, config.ProcessingVersionFor(cfg.ProcessingProvider))
	d.repository = repository
	repository.SetDiskFreeThresholds(cfg.DiskSoftFree, cfg.DiskHardFree)
	repository.SetProcessingEnabled(cfg.ProcessingProvider.Enabled)
	repository.SetGraphExtractionEnabled(cfg.ProcessingProvider.Enabled && cfg.GraphEnabled)
	d.logDiskThresholds(cfg.DiskSoftFree, cfg.DiskHardFree)
	if cfg.EmbeddingProvider.Enabled {
		embedding, providerErr := provider.New(cfg.EmbeddingProvider, nil)
		if providerErr != nil {
			return nil, providerErr
		}
		repository.ConfigureSemantic(embedding, cfg.EmbeddingProvider.Kind, cfg.EmbeddingProvider.Model, cfg.EmbeddingProvider.Options.Dimensions)
		if err := repository.EnsureSemanticRecoveryJobs(ctx); err != nil {
			return nil, fmt.Errorf("prepare semantic recovery: %w", err)
		}
		d.logIndexRecovery(ctx)
	}
	policyIdentity := config.PolicyIdentity(cfg.ProcessingProvider.Policy)
	if !cfg.ProcessingProvider.Enabled {
		policyIdentity = ""
	}
	aggregateService := aggregate.NewService(repository, aggregateProvider, "aggregate/provider", policyIdentity, config.ProcessingVersionFor(cfg.ProcessingProvider), cfg.ProcessingProvider.Limits.MaxContextItems)
	d.aggregateService = aggregateService
	policyService := policy.NewService(storage.NewPolicyCapability(repository))
	d.jobs, err = jobs.NewService(repository, cfg, processor, jobs.Dependencies{
		Processing: storage.NewExtractionWorkCapability(repository),
		Authorizer: policyService,
		Disclosure: policyService,
		Indexes:    repository, EntityIndexes: repository, RelationIndexes: repository, GraphIndexes: repository,
		FeedbackAggregates: repository, AggregateRebuilder: aggregateService, DiskPressure: repository,
		Logger: d.rootLogger,
	})
	if err != nil {
		return nil, err
	}
	if startWorkers {
		if err := d.jobs.Start(ctx); err != nil {
			return nil, err
		}
	}
	recall := storage.NewRecallCapability(repository)
	d.retrievalService = gwretrieval.NewService(gwretrieval.Ports{
		ReferenceTime: recall, Capabilities: recall, Candidates: recall, Telemetry: recall,
		Policy: retrievalPolicyAuthorizer{authorizer: policyService},
	}, recallDefaults(cfg))
	agentTools := agenttool.NewService(
		storage.NewAgentToolCapability(repository),
		agentToolRecallAdapter{service: d.retrievalService},
		agentToolPolicyAuthorizer{authorizer: policyService},
	)
	handler, err := httpapi.New(httpapi.Dependencies{
		AgentTools: agentTools,
		Scopes:     scope.NewService(storage.NewScopeCapability(repository)),
		Events:     extraction.NewService(storage.NewEventCapability(repository)),
		Reviews:    review.NewService(storage.NewReviewCapability(repository)),
	}, httpapi.WithLogger(d.rootLogger))
	if err != nil {
		return nil, err
	}
	d.handler = handler
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
			_ = d.repository.EnsureSemanticRecoveryJobs(ctx)
			d.logIndexRecovery(ctx)
		}
		return result, err
	}
	if err := d.recordActivation(ctx, result, previous, snapshot); err != nil {
		return result, err
	}
	if err := d.repository.EnsureSemanticRecoveryJobs(ctx); err != nil {
		return result, err
	}
	d.logReloadApplied(ctx, result)
	d.logIndexRecovery(ctx)
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
			RevisionID:           result.AttemptID,
			PreviousActivationID: previousActivationID,
			Trigger:              string(result.Trigger),
			RequestedBy:          "daemon",
			Outcome:              string(result.Outcome),
			ErrorCode:            result.ErrorCode,
			Diagnostics:          diagnostics,
			StartedAt:            result.StartedAt,
			FinishedAt:           result.FinishedAt,
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
	var processor jobs.Processor
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
	weights, calibration, graphPolicy := retrievalContracts(snapshot.Config)
	if d.retrievalService != nil {
		d.retrievalService.SetDefaults(recallDefaults(snapshot.Config))
	}
	d.repository.SetRetrievalCalibration(
		weights,
		calibration,
		graphPolicy,
		snapshot.Config.Graph.MinEntityConfidence,
		snapshot.Config.Graph.MinFactConfidence,
		snapshot.Config.Graph.BoostCap,
	)
	d.repository.SetConsolidation(
		snapshot.Config.Consolidation.Version,
		snapshot.Config.Consolidation.MaxCandidates,
		snapshot.Config.Consolidation.ExactHashEnabled,
		snapshot.Config.Consolidation.SameSourceEventCosine,
		snapshot.Config.Consolidation.SameSubjectKindCosine,
	)
	d.repository.SetProcessingGeneration(snapshot.ActivationGeneration, processingPolicyGeneration, snapshot.ProcessingCapability.CompatibilityIdentity)
	d.repository.SetProcessingVersion(config.ProcessingVersionFor(snapshot.Config.ProcessingProvider))
	d.repository.SetProcessingEnabled(snapshot.ProcessingCapability.Enabled)
	d.repository.SetGraphExtractionEnabled(snapshot.ProcessingCapability.Enabled && snapshot.Config.GraphEnabled)
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
		ProcessingVersion:      config.ProcessingVersionFor(snapshot.Config.ProcessingProvider),
	})
	d.aggregateService.SetProcessing(
		aggregateProvider,
		processingIdentity,
		snapshot.ProcessingCapability.PolicyIdentity,
		config.ProcessingVersionFor(snapshot.Config.ProcessingProvider),
		snapshot.ProcessingCapability.Limits.MaxContextItems,
		snapshot.ActivationGeneration,
		processingPolicyGeneration,
	)
	d.logCapabilities(ctx, snapshot)
	if embedding == nil {
		d.repository.SetEmbedding(nil, "", "", "", "", 0, 0)
		return nil
	}
	d.repository.SetEmbedding(
		embedding,
		snapshot.EmbeddingCapability.Kind,
		snapshot.EmbeddingCapability.Model,
		storage.EmbeddingCompatibilityIdentity(snapshot.EmbeddingCapability.CompatibilityIdentity, snapshot.EmbeddingCapability.Options.Dimensions),
		snapshot.EmbeddingCapability.ConfigurationIdentity,
		snapshot.ActivationGeneration,
		embeddingPolicyGeneration,
	)
	return nil
}

func (d *Daemon) logReloadApplied(ctx context.Context, result config.ActivationResult) {
	if d.logger == nil {
		return
	}
	d.logger.InfoContext(ctx, "configuration reload applied",
		"trigger", string(result.Trigger),
		"attempt_id", result.AttemptID,
		"effective_identity", result.EffectiveIdentity,
		"previous_effective_identity", result.PreviousEffectiveIdentity,
		"changed_capabilities", result.ChangedCapabilities,
		"reconciliation_classes", result.ReconciliationClasses,
	)
}

func (d *Daemon) logCapabilities(ctx context.Context, snapshot *config.Snapshot) {
	if d.logger == nil || snapshot == nil {
		return
	}
	if !snapshot.ProcessingCapability.Enabled {
		d.logger.WarnContext(ctx, "processing provider disabled",
			"effective_identity", snapshot.Config.EffectiveIdentity,
		)
	}
	if !snapshot.EmbeddingCapability.Enabled {
		d.logger.WarnContext(ctx, "embedding provider disabled, semantic retrieval degraded",
			"effective_identity", snapshot.Config.EffectiveIdentity,
		)
	}
	if !snapshot.ProcessingCapability.Enabled || !snapshot.Config.GraphEnabled {
		d.logger.WarnContext(ctx, "graph extraction disabled, graph retrieval degraded",
			"effective_identity", snapshot.Config.EffectiveIdentity,
			"graph_enabled", snapshot.Config.GraphEnabled,
		)
	}
}

func (d *Daemon) logDiskThresholds(softFree, hardFree uint64) {
	if d.logger == nil {
		return
	}
	pressure, err := d.repository.DiskPressure()
	if err != nil {
		pressure = "unknown"
	}
	d.logger.Info("disk free thresholds configured",
		"soft_free_bytes", softFree,
		"hard_free_bytes", hardFree,
		"pressure", pressure,
	)
}

func (d *Daemon) logIndexRecovery(ctx context.Context) {
	if d.logger == nil || d.repository == nil {
		return
	}
	semanticState, semanticVersion := d.repository.SemanticHealth(ctx)
	graphState, graphVersion := d.repository.GraphIndexHealth(ctx)
	entityState, entityVersion := d.repository.EntityIndexHealth(ctx)
	relationState, relationVersion := d.repository.RelationIndexHealth(ctx)
	d.logger.InfoContext(ctx, "index recovery result",
		"semantic_state", semanticState,
		"semantic_version", semanticVersion,
		"graph_state", graphState,
		"graph_version", graphVersion,
		"entity_state", entityState,
		"entity_version", entityVersion,
		"relation_state", relationState,
		"relation_version", relationVersion,
	)
}

func retrievalContracts(cfg config.Config) (retrieval.Weights, retrieval.Calibration, retrieval.GraphPolicy) {
	weights := retrieval.DefaultWeights()
	weights.Version = cfg.Ranking.Version
	weights.Semantic = cfg.Ranking.SemanticWeight
	weights.Keyword = cfg.Ranking.KeywordWeight
	weights.Exact = cfg.Ranking.ExactWeight
	weights.Temporal = cfg.Ranking.TemporalWeight
	weights.Quality = cfg.Ranking.QualityWeight
	weights.Agreement = cfg.Ranking.AgreementWeight
	weights.GraphBoostGraphCap = cfg.Graph.BoostCap
	calibration := retrieval.Calibration{
		Version:        cfg.Semantic.CalibrationVersion,
		AbsoluteFloor:  cfg.Semantic.AbsoluteFloor,
		TopMargin:      cfg.Semantic.TopMargin,
		ByKind:         map[string]float64{},
		ByLanguage:     map[string]float64{},
		ByQuestionType: map[string]float64{},
	}
	return weights, calibration, retrieval.GraphPolicy(cfg.Graph.Policy)
}

// retrievalPolicyAuthorizer adapts the Policy module's authorization contract to
// the Retrieval module's consumer-owned port. Neither module imports the other;
// the composition root owns this translation.
type retrievalPolicyAuthorizer struct{ authorizer policy.Authorizer }

func (a retrievalPolicyAuthorizer) Authorize(ctx context.Context, call gateway.CallContext, request gwretrieval.PolicyRequest) (gwretrieval.PolicyDecision, error) {
	decision, err := a.authorizer.Authorize(ctx, call, policy.Request{Capability: policy.Capability(request.Capability), SpaceIDs: request.SpaceIDs})
	if err != nil {
		return gwretrieval.PolicyDecision{}, err
	}
	return gwretrieval.PolicyDecision{Allowed: decision.Allowed, ReasonCode: decision.ReasonCode}, nil
}

// agentToolRecallAdapter adapts the shared Retrieval service to the Agent Tool
// module's consumer-owned recall port. It owns only parameter and result
// translation; the recall pipeline, ranking, and degradation stay in one place.
type agentToolRecallAdapter struct{ service *gwretrieval.ServiceImpl }

func (a agentToolRecallAdapter) Recall(ctx context.Context, call gateway.CallContext, params agenttool.RecallParams) (agenttool.RecallRecord, error) {
	query := gwretrieval.Query{
		OperationID:     params.OperationID,
		Query:           params.Query,
		SpaceIDs:        append([]string(nil), params.ScopeIDs...),
		Intent:          params.Intent,
		EntityHints:     append([]string(nil), params.EntityHints...),
		Limit:           params.Limit,
		LimitSet:        params.Limit > 0,
		IncludeEvidence: params.IncludeEvidence,
		Cursor:          params.Cursor,
	}
	if params.ReferenceTime != nil {
		query.ReferenceTime = params.ReferenceTime.UTC().Format(time.RFC3339Nano)
		query.ReferenceTimeSet = true
	}
	result, err := a.service.Recall(ctx, call, query)
	if err != nil {
		return agenttool.RecallRecord{}, err
	}
	record := agenttool.RecallRecord{
		OperationID:           result.Execution.OperationID,
		RequestedCapabilities: agentToolCapabilities(result.Execution.RequestedCapabilities),
		ExecutedCapabilities:  agentToolCapabilities(result.Execution.ExecutedCapabilities),
		DegradationReasons:    append([]string(nil), result.Execution.DegradationReasons...),
		NextCursor:            result.Context.NextCursor,
	}
	record.Memories = make([]agenttool.RecallMemory, len(result.Context.Items))
	for i, item := range result.Context.Items {
		record.Memories[i] = agenttool.RecallMemory{
			MemoryID: item.MemoryID, RevisionID: item.RevisionID, Kind: item.Kind, Content: item.DisplayContent,
			ValidFrom: item.ValidFrom, ValidUntil: item.ValidUntil, Confidence: item.Confidence, Evidence: item.Evidence,
		}
	}
	return record, nil
}

func agentToolCapabilities(values []string) []agenttool.Capability {
	result := make([]agenttool.Capability, len(values))
	for i, value := range values {
		result[i] = agenttool.Capability(value)
	}
	return result
}

// agentToolPolicyAuthorizer adapts the Policy module to the Agent Tool module's
// consumer-owned authorization port.
type agentToolPolicyAuthorizer struct{ authorizer policy.Authorizer }

func (a agentToolPolicyAuthorizer) Authorize(ctx context.Context, call gateway.CallContext, request agenttool.AuthorizationRequest) (agenttool.AuthorizationDecision, error) {
	decision, err := a.authorizer.Authorize(ctx, call, policy.Request{Capability: policy.Capability(request.Capability), SpaceIDs: request.ScopeIDs})
	if err != nil {
		return agenttool.AuthorizationDecision{}, err
	}
	return agenttool.AuthorizationDecision{Allowed: decision.Allowed, ReasonCode: decision.ReasonCode}, nil
}

func recallDefaults(cfg config.Config) gwretrieval.Defaults {
	defaults := gwretrieval.Defaults{
		MaxResults:       cfg.Context.MaxResults,
		MaxContextTokens: cfg.Context.MaxContextTokens,
		MaxContextChars:  cfg.Context.MaxContextChars,
		GraphPolicy:      retrieval.GraphPolicy(cfg.Graph.Policy),
	}
	if defaults.MaxResults == 0 {
		defaults.MaxResults = 20
	}
	if defaults.MaxContextTokens == 0 {
		defaults.MaxContextTokens = 4000
	}
	if defaults.MaxContextChars == 0 {
		defaults.MaxContextChars = 16000
	}
	return defaults
}

func (d *Daemon) Close(ctx context.Context) error {
	d.closeOnce.Do(func() {
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
	if d.jobs != nil {
		errs = append(errs, d.jobs.Shutdown(context.Background()))
		d.jobs = nil
	}
	if d.store != nil {
		errs = append(errs, d.store.Close())
		d.store = nil
	}
	if d.lock != nil {
		errs = append(errs, d.lock.Close())
		d.lock = nil
	}
	return errors.Join(errs...)
}

func prepareDir(path string, mode os.FileMode) error {
	if err := rejectSymlinkComponents(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	if err := rejectSymlinkComponents(path); err != nil {
		return err
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

// rejectSymlinkComponents fails when any existing component of path is a
// symlink. Missing components are allowed so callers can create fresh
// directories without ever creating through a link.
func rejectSymlinkComponents(path string) error {
	clean := filepath.Clean(path)
	current := string(filepath.Separator)
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("directory path must not contain symlinks: %s", current)
		}
	}
	return nil
}
