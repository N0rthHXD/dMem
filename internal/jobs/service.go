package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/policy"
	"github.com/N0rthHXD/dMem/internal/storage"
	"github.com/N0rthHXD/dMem/pkg/logging"
)

const maintenanceInterval = time.Second

// Repository is the job-lifecycle port the worker owns. Extraction preparation
// and fenced commit are deliberately absent: they are consumed through the
// extraction-owned WorkStore contract so provider calls stay outside write
// transactions and outside this port.
type Repository interface {
	ClaimJob(context.Context, storage.ClaimJobParams) (storage.ClaimedJob, error)
	HeartbeatJob(context.Context, storage.ClaimedJob, time.Duration) (bool, error)
	EmbeddingScope(context.Context, storage.ClaimedJob) (string, bool, error)
	CompleteJob(context.Context, storage.ClaimedJob, storage.JobResult) error
	FailJob(context.Context, storage.ClaimedJob, storage.JobFailure) error
	CancelClaimedJob(context.Context, storage.ClaimedJob) error
	RecoverExpiredLeases(context.Context) (int, error)
	ExpireJobs(context.Context) (int, error)
	PromoteWaitingJobs(context.Context, int) (int, error)
}

type Service struct {
	repository         Repository
	work               extraction.WorkStore
	authorizer         policy.Authorizer
	disclosure         policy.DisclosureFilter
	processing         atomic.Pointer[processingRuntime]
	workerCount        int
	leaseDuration      time.Duration
	perJobTimeout      time.Duration
	retryInitial       time.Duration
	retryMax           time.Duration
	indexes            IndexRepository
	entityIndexes      EntityIndexRepository
	relationIndexes    RelationIndexRepository
	graphIndexes       GraphIndexRepository
	feedbackAggregates FeedbackAggregateRepository
	aggregates         AggregateRebuilder
	diskPressure       DiskPressureSource
	serviceID          uint64
	healthMu           sync.RWMutex
	processorState     string
	logger             *slog.Logger

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
	workers sync.WaitGroup
}

type processingRuntime struct {
	processor             Processor
	preparation           storage.ProcessingPreparationParams
	processorID           string
	processorEnabled      bool
	generation            int
	policyGeneration      int
	compatibilityIdentity string
	policyIdentity        string
	processingVersion     string
}

type ProcessingConfig struct {
	Processor              Processor
	ProcessorIdentity      string
	Enabled                bool
	MaxContentChars        int
	MaxTotalCandidateChars int
	MaxContextItems        int
	Generation             int
	PolicyGeneration       int
	CompatibilityIdentity  string
	PolicyIdentity         string
	ProcessingVersion      string
}

func (s *Service) currentProcessing() *processingRuntime {
	return s.processing.Load()
}

func (s *Service) SetProcessing(cfg ProcessingConfig) {
	processor := cfg.Processor
	enabled := cfg.Enabled && processor != nil && processor.Enabled()
	identity := "none"
	if enabled {
		identity = cfg.ProcessorIdentity
		if identity == "" {
			identity = processor.Identity()
		}
		if identity == "" {
			identity = "processor/in-process"
		}
	}
	maxContentChars := cfg.MaxContentChars
	if maxContentChars <= 0 {
		maxContentChars = 12000
	}
	maxContext := cfg.MaxContextItems
	if maxContext < 0 {
		maxContext = 0
	}
	rt := processingRuntime{
		processor:             processor,
		processorID:           identity,
		processorEnabled:      enabled,
		generation:            cfg.Generation,
		policyGeneration:      cfg.PolicyGeneration,
		compatibilityIdentity: cfg.CompatibilityIdentity,
		policyIdentity:        cfg.PolicyIdentity,
		processingVersion:     cfg.ProcessingVersion,
		preparation: storage.ProcessingPreparationParams{
			ProcessorIdentity:      identity,
			AllowedMemoryTypes:     []string{"fact", "preference", "plan", "procedure", "event"},
			MaxCandidates:          20,
			MaxCandidateChars:      maxContentChars,
			MaxTotalCandidateChars: max(cfg.MaxTotalCandidateChars, maxContentChars),
			MaxContentChars:        maxContentChars,
			MaxContextEvents:       maxContext,
			MaxContextMemories:     maxContext,
		},
	}
	s.processing.Store(&rt)
	s.healthMu.Lock()
	if rt.processorEnabled {
		s.processorState = "degraded"
	} else {
		s.processorState = "not_configured"
	}
	s.healthMu.Unlock()
}

var nextServiceID atomic.Uint64

type Dependencies struct {
	Processing         extraction.WorkStore
	Authorizer         policy.Authorizer
	Disclosure         policy.DisclosureFilter
	Indexes            IndexRepository
	EntityIndexes      EntityIndexRepository
	RelationIndexes    RelationIndexRepository
	GraphIndexes       GraphIndexRepository
	FeedbackAggregates FeedbackAggregateRepository
	AggregateRebuilder AggregateRebuilder
	DiskPressure       DiskPressureSource
	Logger             *slog.Logger
}

func NewService(repository Repository, cfg config.Config, processor Processor, dependencies Dependencies) (*Service, error) {
	if repository == nil {
		return nil, errors.New("job repository is required")
	}
	if dependencies.Processing == nil {
		return nil, errors.New("extraction work store is required")
	}
	if cfg.WorkerCount <= 0 || cfg.LeaseDuration <= 0 || cfg.PerJobTimeout <= 0 || cfg.RetryInitialBackoff <= 0 || cfg.RetryMaxBackoff < cfg.RetryInitialBackoff {
		return nil, errors.New("invalid job service configuration")
	}
	maxContentChars, maxTotalCandidateChars, maxContext := cfg.ProcessingProvider.Limits.MaxCandidateChars, cfg.ProcessingProvider.Limits.MaxTotalCandidateChars, cfg.ProcessingProvider.Limits.MaxContextItems
	if maxContentChars <= 0 {
		maxContentChars = 12000
	}
	if maxTotalCandidateChars < maxContentChars {
		maxTotalCandidateChars = maxContentChars
	}
	if maxContext < 0 {
		maxContext = 0
	}
	enabled := processor != nil && processor.Enabled()
	identity := "none"
	if enabled {
		identity = processor.Identity()
		if identity == "" {
			identity = "processor/in-process"
		}
	}
	state := "degraded"
	if !enabled {
		state = "not_configured"
	}
	s := &Service{
		repository:         repository,
		work:               dependencies.Processing,
		authorizer:         dependencies.Authorizer,
		disclosure:         dependencies.Disclosure,
		workerCount:        cfg.WorkerCount,
		leaseDuration:      cfg.LeaseDuration,
		perJobTimeout:      cfg.PerJobTimeout,
		retryInitial:       cfg.RetryInitialBackoff,
		retryMax:           cfg.RetryMaxBackoff,
		indexes:            dependencies.Indexes,
		entityIndexes:      dependencies.EntityIndexes,
		relationIndexes:    dependencies.RelationIndexes,
		graphIndexes:       dependencies.GraphIndexes,
		feedbackAggregates: dependencies.FeedbackAggregates,
		aggregates:         dependencies.AggregateRebuilder,
		diskPressure:       dependencies.DiskPressure,
		serviceID:          nextServiceID.Add(1),
		processorState:     state,
		logger:             logging.WithComponent(dependencies.Logger, logging.ComponentJobs),
		done:               make(chan struct{}),
	}
	s.SetProcessing(ProcessingConfig{
		Processor:              processor,
		ProcessorIdentity:      identity,
		Enabled:                enabled,
		MaxContentChars:        maxContentChars,
		MaxTotalCandidateChars: maxTotalCandidateChars,
		MaxContextItems:        maxContext,
		ProcessingVersion:      cfg.ProcessingVersion,
	})
	return s, nil
}

func (s *Service) ProcessorHealth() (string, string) {
	rt := s.currentProcessing()
	id := "none"
	if rt != nil {
		id = rt.processorID
	}
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()
	return s.processorState, id
}

func (s *Service) recordProcessorResult(result domain.ProcessingResult) {
	rt := s.currentProcessing()
	if rt == nil || !rt.processorEnabled {
		return
	}
	state := ""
	switch result.Code {
	case domain.ProcessingNoMemory, domain.ProcessingCandidatesExtracted:
		state = "ready"
	case domain.ProcessingTransientFailure, domain.ProcessingPermanentFailure:
		state = "unavailable"
	case domain.ProcessingInvalidOutput, domain.ProcessingNoProcessor:
		state = "degraded"
	}
	if state != "" {
		s.healthMu.Lock()
		s.processorState = state
		s.healthMu.Unlock()
	}
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("job service already started")
	}
	if err := s.maintain(ctx); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	s.workers.Add(s.workerCount + 1)
	go s.maintenanceLoop(runCtx)
	for i := 0; i < s.workerCount; i++ {
		go s.worker(runCtx, i)
	}
	go func() {
		s.workers.Wait()
		close(s.done)
	}()
	return nil
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.cancel()
	done := s.done
	s.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) maintain(ctx context.Context) error {
	if _, err := s.repository.RecoverExpiredLeases(ctx); err != nil {
		return fmt.Errorf("recover expired job leases: %w", err)
	}
	if _, err := s.repository.ExpireJobs(ctx); err != nil {
		return fmt.Errorf("expire jobs: %w", err)
	}
	if _, err := s.repository.PromoteWaitingJobs(ctx, s.workerCount); err != nil {
		return fmt.Errorf("promote waiting jobs: %w", err)
	}
	if reconciler, ok := s.repository.(interface {
		ClaimReconciliationRun(context.Context) (storage.ClaimedReconciliationRun, error)
		Reconcile(context.Context, storage.ClaimedReconciliationRun, int) (storage.ReconciliationBatch, error)
	}); ok {
		for {
			run, err := reconciler.ClaimReconciliationRun(ctx)
			if errors.Is(err, storage.ErrNotFound) {
				break
			}
			if err != nil {
				return fmt.Errorf("claim reconciliation run: %w", err)
			}
			batch, err := reconciler.Reconcile(ctx, run, 100)
			if err != nil {
				return fmt.Errorf("reconcile run: %w", err)
			}
			if !batch.Complete {
				continue
			}
		}
	}
	return nil
}

func (s *Service) maintenanceLoop(ctx context.Context) {
	defer s.workers.Done()
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.maintain(ctx); err != nil {
				s.logPersistenceError(ctx, "periodic_maintenance", storage.ClaimedJob{}, err)
			}
		}
	}
}

func (s *Service) worker(ctx context.Context, index int) {
	defer s.workers.Done()
	workerID := fmt.Sprintf("jobs-%d-%d", s.serviceID, index)
	idle := time.NewTicker(100 * time.Millisecond)
	defer idle.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if s.diskPressure != nil {
			state, err := s.diskPressure.DiskPressure()
			if err != nil || state == "hard" || state == "soft" {
				select {
				case <-ctx.Done():
					return
				case <-idle.C:
					continue
				}
			}
		}
		claim, err := s.repository.ClaimJob(ctx, storage.ClaimJobParams{WorkerID: workerID, LeaseDuration: s.leaseDuration})
		if err == nil {
			s.processClaim(ctx, claim)
			if _, err := s.repository.PromoteWaitingJobs(ctx, s.workerCount); err != nil {
				s.logPersistenceError(ctx, "post_claim_promotion", claim, err)
			}
			continue
		}
		s.logPersistenceError(ctx, "claim_job", storage.ClaimedJob{}, err)
		if !errors.Is(err, storage.ErrNotFound) && ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
		}
	}
}

func (s *Service) processClaim(serviceCtx context.Context, claim storage.ClaimedJob) {
	processCtx, cancel := context.WithTimeout(serviceCtx, s.perJobTimeout)
	defer cancel()
	if claim.Job.JobType == "rebuild_fts" || claim.Job.JobType == "rebuild_semantic" || claim.Job.JobType == "rebuild_entity_index" || claim.Job.JobType == "rebuild_relation_index" || claim.Job.JobType == "rebuild_graph_index" || claim.Job.JobType == "embed_memory" || claim.Job.JobType == "aggregate_feedback" || claim.Job.JobType == "rebuild_feedback_aggregates" || claim.Job.JobType == "rebuild_aggregate" {
		s.processIndexClaim(processCtx, claim)
		return
	}
	if claim.Job.JobType == "extract_graph" {
		s.processGraphClaim(processCtx, claim)
		return
	}
	runtime := s.currentProcessing()
	if runtime.processor == nil || !runtime.processorEnabled {
		// A model job must never be completed by a no-op path. The Provider Route
		// reconciliation supersedes such jobs when the capability is disabled;
		// this is the fenced safety net for a job that slipped through.
		s.failDisabledModelJob(claim)
		return
	}
	input, err := s.work.Prepare(processCtx, workClaim(claim), extraction.PrepareRequest{
		ProcessorIdentity:      runtime.preparation.ProcessorIdentity,
		AllowedMemoryTypes:     runtime.preparation.AllowedMemoryTypes,
		MaxCandidates:          runtime.preparation.MaxCandidates,
		MaxCandidateChars:      runtime.preparation.MaxCandidateChars,
		MaxTotalCandidateChars: runtime.preparation.MaxTotalCandidateChars,
		MaxContentChars:        runtime.preparation.MaxContentChars,
		MaxContextEvents:       runtime.preparation.MaxContextEvents,
		MaxContextMemories:     runtime.preparation.MaxContextMemories,
	})
	if err != nil {
		if errors.Is(err, storage.ErrLeaseLost) {
			return
		}
		if errors.Is(err, storage.ErrPolicyDenied) {
			s.logPersistenceError(context.Background(), "block_processing_job", claim, s.work.Block(context.Background(), workClaim(claim), "target_policy_denied"))
			return
		}
		input = domain.ProcessingInput{ProcessorIdentity: runtime.preparation.ProcessorIdentity, ProcessingVersion: claim.ProcessingVersion, JobID: claim.Job.JobID, Target: domain.ProcessingEvent{EventID: claim.Job.TargetID}}
		s.failProcessing(claim, input, "preparation_failed", "processing preparation failed", true)
		return
	}
	if denied, err := s.policyDenied(processCtx, policy.CapabilityProcessing, input.Target.SpaceID); err != nil {
		s.failProcessing(claim, input, "policy_check_failed", "policy authorization failed", true)
		return
	} else if denied {
		s.logPersistenceError(context.Background(), "block_processing_job", claim, s.work.Block(context.Background(), workClaim(claim), "processing_policy_denied"))
		return
	}
	if err := s.filterContextMemories(processCtx, &input); err != nil {
		s.failProcessing(claim, input, "disclosure_filter_failed", "disclosure filtering failed", true)
		return
	}
	results := make(chan domain.ProcessingResult, 1)
	if preflight, ok := runtime.processor.(interface {
		Preflight(domain.ProcessingInput) *domain.ProcessingResult
	}); ok {
		if outcome := preflight.Preflight(input); outcome != nil {
			s.applyOutcome(claim, input, *outcome)
			return
		}
	}
	if recorder, ok := s.repository.(interface {
		MarkProviderAttempt(context.Context, storage.ClaimedJob) error
	}); ok {
		if err := recorder.MarkProviderAttempt(processCtx, claim); err != nil {
			if !errors.Is(err, storage.ErrLeaseLost) {
				s.failProcessing(claim, input, "attempt_record_failed", "provider attempt could not be recorded", true)
			}
			return
		}
	}
	go func() {
		results <- runtime.processor.Process(processCtx, input)
	}()

	heartbeat := time.NewTicker(s.leaseDuration / 3)
	defer heartbeat.Stop()
	for {
		select {
		case outcome := <-results:
			if serviceCtx.Err() != nil {
				s.failShutdown(claim, input)
				return
			}
			if errors.Is(processCtx.Err(), context.DeadlineExceeded) {
				s.failTimeout(claim, input)
				return
			}
			s.applyOutcome(claim, input, outcome)
			return
		case <-heartbeat.C:
			cancelRequested, err := s.repository.HeartbeatJob(serviceCtx, claim, s.leaseDuration)
			if errors.Is(err, storage.ErrLeaseLost) {
				cancel()
				return
			}
			if err != nil {
				cancel()
				if serviceCtx.Err() != nil {
					s.failShutdown(claim, input)
				} else {
					s.failProcessing(claim, input, "heartbeat_failed", "job heartbeat failed", true)
				}
				return
			}
			if cancelRequested {
				cancel()
				err := s.repository.CancelClaimedJob(context.Background(), claim)
				s.logPersistenceError(context.Background(), "cancel_claimed_job", claim, err)
				return
			}
		case <-processCtx.Done():
			if serviceCtx.Err() != nil {
				s.failShutdown(claim, input)
			} else {
				s.failTimeout(claim, input)
			}
			return
		}
	}
}

func (s *Service) applyOutcome(claim storage.ClaimedJob, input domain.ProcessingInput, result domain.ProcessingResult) {
	if err := validateProcessingResult(input, result); err != nil {
		s.recordProcessorResult(domain.ProcessingResult{Code: domain.ProcessingInvalidOutput})
		code := result.ReasonCode
		if code == "" {
			code = processingValidationCode(err)
		}
		s.failProcessing(claim, input, code, "processor returned invalid output", false)
		return
	}
	s.recordProcessorResult(result)
	switch result.Code {
	case domain.ProcessingNoMemory, domain.ProcessingCandidatesExtracted:
		err := s.work.Commit(context.Background(), workClaim(claim), input, extraction.CommitRequest{Result: result})
		if errors.Is(err, storage.ErrStaleGeneration) {
			s.reevaluateStale(claim)
			return
		}
		s.logPersistenceError(context.Background(), "commit_processing", claim, err)
	case domain.ProcessingNoProcessor:
		// A real provider reported that it is disabled mid-flight. Never commit
		// this as a stored success; the capability reconciliation that disabled
		// the route supersedes the job, and until then it is retryable.
		code := result.ReasonCode
		if code == "" {
			code = "provider_disabled"
		}
		s.failProcessing(claim, input, code, "processing provider is disabled", true)
	case domain.ProcessingPolicyRejected:
		s.logPersistenceError(context.Background(), "block_processing_job", claim, s.work.Block(context.Background(), workClaim(claim), result.ReasonCode))
	case domain.ProcessingTransientFailure:
		code := result.ReasonCode
		if code == "" {
			code = string(result.Code)
		}
		s.failProcessing(claim, input, code, "processor transient failure", true)
	case domain.ProcessingPermanentFailure, domain.ProcessingInvalidOutput:
		code := result.ReasonCode
		if code == "" {
			code = string(result.Code)
		}
		s.failProcessing(claim, input, code, "processor permanent failure", false)
	}
}

type GraphExtractor interface {
	PrepareGraphExtraction(context.Context, storage.ClaimedJob, storage.ProcessingPreparationParams) (domain.GraphExtractionInput, error)
	CommitGraphExtraction(context.Context, storage.ClaimedJob, domain.GraphExtractionInput, storage.GraphExtractionCommit) error
}

func (s *Service) processGraphClaim(serviceCtx context.Context, claim storage.ClaimedJob) {
	extractor, ok := s.repository.(GraphExtractor)
	if !ok {
		s.logPersistenceError(context.Background(), "complete_unsupported_graph_job", claim, s.repository.CompleteJob(context.Background(), claim, storage.JobResult{Code: "unsupported"}))
		return
	}
	processCtx, cancel := context.WithTimeout(serviceCtx, s.perJobTimeout)
	defer cancel()
	runtime := s.currentProcessing()
	if runtime.processor == nil || !runtime.processorEnabled {
		s.failDisabledModelJob(claim)
		return
	}
	input, err := extractor.PrepareGraphExtraction(processCtx, claim, runtime.preparation)
	if err != nil {
		if errors.Is(err, storage.ErrLeaseLost) {
			return
		}
		if errors.Is(err, storage.ErrPolicyDenied) {
			s.logPersistenceError(context.Background(), "block_graph_job", claim, s.work.Block(context.Background(), workClaim(claim), "target_policy_denied"))
			return
		}
		if errors.Is(err, storage.ErrRevisionConflict) || errors.Is(err, storage.ErrNotFound) {
			s.logPersistenceError(context.Background(), "complete_stale_graph_job", claim, s.repository.CompleteJob(context.Background(), claim, storage.JobResult{Code: "stale_revision"}))
			return
		}
		s.failTransientJob(claim, "preparation_failed", "graph extraction preparation failed")
		return
	}
	if denied, err := s.policyDenied(processCtx, policy.CapabilityProcessing, input.SpaceID); err != nil {
		s.failTransientJob(claim, "policy_check_failed", "policy authorization failed")
		return
	} else if denied {
		s.logPersistenceError(context.Background(), "block_graph_job", claim, s.work.Block(context.Background(), workClaim(claim), "processing_policy_denied"))
		return
	}
	if recorder, ok := s.repository.(interface {
		MarkProviderAttempt(context.Context, storage.ClaimedJob) error
	}); ok {
		if err := recorder.MarkProviderAttempt(processCtx, claim); err != nil {
			if !errors.Is(err, storage.ErrLeaseLost) {
				s.failTransientJob(claim, "attempt_record_failed", "provider attempt could not be recorded")
			}
			return
		}
	}
	results := make(chan domain.GraphExtractionResult, 1)
	go func() {
		if graphProcessor, ok := runtime.processor.(GraphProcessor); ok {
			results <- graphProcessor.ProcessGraph(processCtx, input)
			return
		}
		results <- domain.GraphExtractionResult{Code: domain.GraphExtractionNoProcessor}
	}()
	heartbeat := time.NewTicker(s.leaseDuration / 3)
	defer heartbeat.Stop()
	for {
		select {
		case outcome := <-results:
			if serviceCtx.Err() != nil {
				s.failTransientJob(claim, "processor_shutdown", "graph extraction interrupted by shutdown")
				return
			}
			if errors.Is(processCtx.Err(), context.DeadlineExceeded) {
				s.failTransientJob(claim, "processor_timeout", "graph extraction timed out")
				return
			}
			s.applyGraphOutcome(extractor, claim, input, outcome)
			return
		case <-heartbeat.C:
			cancelRequested, err := s.repository.HeartbeatJob(serviceCtx, claim, s.leaseDuration)
			if errors.Is(err, storage.ErrLeaseLost) {
				cancel()
				return
			}
			if err != nil {
				cancel()
				s.failTransientJob(claim, "heartbeat_failed", "job heartbeat failed")
				return
			}
			if cancelRequested {
				cancel()
				err := s.repository.CancelClaimedJob(context.Background(), claim)
				s.logPersistenceError(context.Background(), "cancel_claimed_job", claim, err)
				return
			}
		case <-processCtx.Done():
			cancel()
			s.failTransientJob(claim, string(domain.ProcessingTransientFailure), "graph extraction interrupted")
			return
		}
	}
}

func (s *Service) applyGraphOutcome(extractor GraphExtractor, claim storage.ClaimedJob, input domain.GraphExtractionInput, result domain.GraphExtractionResult) {
	s.recordProcessorResult(domain.ProcessingResult{Code: domain.ProcessingCandidatesExtracted})
	switch result.Code {
	case domain.GraphExtractionEmpty, domain.GraphExtractionExtracted:
		err := extractor.CommitGraphExtraction(context.Background(), claim, input, storage.GraphExtractionCommit{Result: result})
		if errors.Is(err, storage.ErrStaleGeneration) {
			s.reevaluateStale(claim)
			return
		}
		if errors.Is(err, storage.ErrRevisionConflict) {
			s.logPersistenceError(context.Background(), "complete_stale_graph_job", claim, s.repository.CompleteJob(context.Background(), claim, storage.JobResult{Code: "stale_revision"}))
			return
		}
		if errors.Is(err, storage.ErrLeaseLost) {
			return
		}
		if err != nil {
			s.failTransientJob(claim, "graph_commit_failed", "graph extraction commit failed")
			return
		}
	case domain.GraphExtractionNoProcessor:
		// The provider reported that it is disabled mid-flight; never record a
		// successful empty extraction for a route that is not running.
		s.failDisabledModelJob(claim)
	case domain.GraphExtractionPolicyRejected:
		reason := result.ReasonCode
		if reason == "" {
			reason = "target_policy_denied"
		}
		s.logPersistenceError(context.Background(), "block_graph_job", claim, s.work.Block(context.Background(), workClaim(claim), reason))
	case domain.GraphExtractionTransientFailure:
		code := result.ReasonCode
		if code == "" {
			code = string(result.Code)
		}
		s.failTransientJob(claim, code, "graph extraction transient failure")
	case domain.GraphExtractionInvalidOutput:
		code := result.ReasonCode
		if code == "" {
			code = string(result.Code)
		}
		s.failTransientJob(claim, code, "graph extraction produced invalid output")
	case domain.GraphExtractionPermanentFailure:
		code := result.ReasonCode
		if code == "" {
			code = string(result.Code)
		}
		s.logPersistenceError(context.Background(), "fail_graph_job", claim, s.repository.FailJob(context.Background(), claim, storage.JobFailure{Code: code, Message: "graph extraction failed", Transient: false}))
	}
}

func (s *Service) processIndexClaim(ctx context.Context, claim storage.ClaimedJob) {
	operationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if claim.Job.JobType == "embed_memory" {
		if spaceID, ok, err := s.repository.EmbeddingScope(operationCtx, claim); err != nil {
			s.failTransientJob(claim, "embedding_scope_failed", "embedding scope could not be resolved")
			return
		} else if ok {
			if denied, err := s.policyDenied(operationCtx, policy.CapabilityEmbedding, spaceID); err != nil {
				s.failTransientJob(claim, "policy_check_failed", "policy authorization failed")
				return
			} else if denied {
				s.logPersistenceError(context.Background(), "block_index_job", claim, s.work.Block(context.Background(), workClaim(claim), "embedding_policy_denied"))
				return
			}
		}
		if preparer, ok := s.repository.(interface {
			PrepareEmbeddingAttempt(context.Context, storage.ClaimedJob) error
		}); ok {
			if err := preparer.PrepareEmbeddingAttempt(operationCtx, claim); err != nil {
				if errors.Is(err, storage.ErrPolicyDenied) {
					s.logPersistenceError(context.Background(), "block_index_job", claim, s.work.Block(context.Background(), workClaim(claim), "embedding_policy_denied"))
				} else if !errors.Is(err, storage.ErrLeaseLost) {
					s.failTransientJob(claim, "attempt_record_failed", "provider attempt could not be recorded")
				}
				return
			}
		}
	}
	if claim.Job.JobType == "rebuild_aggregate" {
		// Aggregate rebuilds call the same processing provider, so they obey the
		// same policy decision and consume the same external-attempt budget.
		if scope, ok := s.repository.(interface {
			AggregateScope(context.Context, storage.ClaimedJob) (string, bool, error)
		}); ok {
			if spaceID, exists, err := scope.AggregateScope(operationCtx, claim); err != nil {
				s.failTransientJob(claim, "aggregate_scope_failed", "aggregate scope could not be resolved")
				return
			} else if exists {
				if denied, err := s.policyDenied(operationCtx, policy.CapabilityProcessing, spaceID); err != nil {
					s.failTransientJob(claim, "policy_check_failed", "policy authorization failed")
					return
				} else if denied {
					s.logPersistenceError(context.Background(), "block_index_job", claim, s.work.Block(context.Background(), workClaim(claim), "processing_policy_denied"))
					return
				}
			}
		}
		if recorder, ok := s.repository.(interface {
			MarkProviderAttempt(context.Context, storage.ClaimedJob) error
		}); ok {
			if err := recorder.MarkProviderAttempt(operationCtx, claim); err != nil {
				if !errors.Is(err, storage.ErrLeaseLost) {
					s.failTransientJob(claim, "attempt_record_failed", "provider attempt could not be recorded")
				}
				return
			}
		}
	}
	results := make(chan error, 1)
	go func() {
		var err error
		switch claim.Job.JobType {
		case "rebuild_fts":
			if claim.Job.TargetType != "index" || claim.Job.TargetID != "memory_fts" {
				err = errors.New("invalid FTS target")
			} else if s.indexes == nil {
				err = errors.New("FTS rebuild is unavailable")
			} else {
				err = s.indexes.RebuildFTSClaimed(operationCtx, claim)
			}
		case "rebuild_semantic":
			if claim.Job.TargetType != "index" || claim.Job.TargetID != "memory_semantic" {
				err = errors.New("invalid semantic target")
			} else if s.indexes == nil {
				err = errors.New("semantic rebuild is unavailable")
			} else {
				err = s.indexes.RebuildSemanticClaimed(operationCtx, claim)
			}
		case "rebuild_entity_index":
			if claim.Job.TargetType != "index" || claim.Job.TargetID != "entity_recall" {
				err = errors.New("invalid entity index target")
			} else if s.entityIndexes == nil {
				err = errors.New("entity index rebuild is unavailable")
			} else {
				err = s.entityIndexes.RebuildEntityIndexClaimed(operationCtx, claim)
			}
		case "rebuild_relation_index":
			if claim.Job.TargetType != "index" || claim.Job.TargetID != "relation_recall" {
				err = errors.New("invalid relation index target")
			} else if s.relationIndexes == nil {
				err = errors.New("relation index rebuild is unavailable")
			} else {
				err = s.relationIndexes.RebuildRelationIndexClaimed(operationCtx, claim)
			}
		case "rebuild_graph_index":
			if claim.Job.TargetType != "index" || claim.Job.TargetID != "graph_recall" {
				err = errors.New("invalid graph index target")
			} else if s.graphIndexes == nil {
				err = errors.New("graph index rebuild is unavailable")
			} else {
				err = s.graphIndexes.RebuildGraphIndexClaimed(operationCtx, claim)
			}
		case "rebuild_quality":
			if claim.Job.TargetType != "index" || claim.Job.TargetID != "memory_quality" {
				err = errors.New("invalid quality projection target")
			} else if projector, ok := s.repository.(interface {
				RebuildQualityClaimed(context.Context, storage.ClaimedJob) error
			}); !ok {
				err = errors.New("quality projection rebuild is unavailable")
			} else {
				err = projector.RebuildQualityClaimed(operationCtx, claim)
			}
		case "rebuild_duplicates":
			if claim.Job.TargetType != "index" || claim.Job.TargetID != "duplicate_clusters" {
				err = errors.New("invalid duplicate projection target")
			} else if projector, ok := s.repository.(interface {
				RebuildDuplicatesClaimed(context.Context, storage.ClaimedJob) error
			}); !ok {
				err = errors.New("duplicate projection rebuild is unavailable")
			} else {
				err = projector.RebuildDuplicatesClaimed(operationCtx, claim)
			}
		case "embed_memory":
			if claim.Job.TargetType != "memory_revision" {
				err = errors.New("invalid embedding target")
			} else if s.indexes == nil {
				err = errors.New("embedding is unavailable")
			} else {
				err = s.indexes.EmbedMemoryClaimed(operationCtx, claim, claim.Job.TargetID)
			}
		case "aggregate_feedback", "rebuild_feedback_aggregates":
			if claim.Job.TargetType != "feedback_revision" && claim.Job.TargetType != "index" {
				err = errors.New("invalid feedback aggregate target")
			} else if s.feedbackAggregates == nil {
				err = errors.New("feedback aggregate rebuild is unavailable")
			} else {
				err = s.feedbackAggregates.RebuildFeedbackAggregatesClaimed(operationCtx, claim)
			}
		case "rebuild_aggregate":
			if claim.Job.TargetType != "aggregate" || s.aggregates == nil {
				err = errors.New("invalid aggregate rebuild target")
			} else {
				err = s.aggregates.RebuildClaimed(operationCtx, claim)
			}
		default:
			err = errors.New("unsupported index job")
		}
		results <- err
	}()

	heartbeat := time.NewTicker(s.leaseDuration / 3)
	defer heartbeat.Stop()
	for {
		select {
		case err := <-results:
			if errors.Is(err, storage.ErrLeaseLost) {
				return
			}
			if errors.Is(err, storage.ErrStaleGeneration) {
				s.reevaluateStale(claim)
				return
			}
			if err != nil {
				switch {
				case errors.Is(err, storage.ErrEmbeddingPolicy), errors.Is(err, storage.ErrPolicyDenied):
					persistErr := s.work.Block(context.Background(), workClaim(claim), "embedding_policy_denied")
					s.logPersistenceError(context.Background(), "block_index_job", claim, persistErr)
				case errors.Is(err, storage.ErrProviderInvalidOutput):
					s.failPermanentJob(claim, string(domain.ProcessingInvalidOutput), "provider returned invalid output")
				case errors.Is(err, storage.ErrProviderPermanent):
					s.failPermanentJob(claim, string(domain.ProcessingPermanentFailure), "provider rejected the request permanently")
				default:
					s.failTransientJob(claim, string(domain.ProcessingTransientFailure), "index operation failed")
				}
				return
			}
			persistErr := s.repository.CompleteJob(context.Background(), claim, storage.JobResult{Code: outcomeIndexRebuilt})
			s.logPersistenceError(context.Background(), "complete_index_job", claim, persistErr)
			return
		case <-heartbeat.C:
			cancelRequested, err := s.repository.HeartbeatJob(ctx, claim, s.leaseDuration)
			if errors.Is(err, storage.ErrLeaseLost) {
				cancel()
				return
			}
			if err != nil {
				cancel()
				s.failTransientJob(claim, string(domain.ProcessingTransientFailure), "job heartbeat failed")
				return
			}
			if cancelRequested {
				cancel()
				err := s.repository.CancelClaimedJob(context.Background(), claim)
				s.logPersistenceError(context.Background(), "cancel_claimed_job", claim, err)
				return
			}
		case <-ctx.Done():
			cancel()
			s.failTransientJob(claim, string(domain.ProcessingTransientFailure), "index operation interrupted")
			return
		}
	}
}

func (s *Service) reevaluateStale(claim storage.ClaimedJob) {
	repository, ok := s.repository.(interface {
		ReevaluateStaleJob(context.Context, storage.ClaimedJob) error
	})
	if !ok {
		return
	}
	s.logPersistenceError(context.Background(), "reevaluate_stale_job", claim, repository.ReevaluateStaleJob(context.Background(), claim))
}

func (s *Service) failTimeout(claim storage.ClaimedJob, input domain.ProcessingInput) {
	s.recordProcessorResult(domain.ProcessingResult{Code: domain.ProcessingTransientFailure})
	s.failProcessing(claim, input, "processor_timeout", "processor timed out", true)
}

func (s *Service) failShutdown(claim storage.ClaimedJob, input domain.ProcessingInput) {
	s.failProcessing(claim, input, "processor_shutdown", "processor interrupted by shutdown", true)
}

func (s *Service) failProcessing(claim storage.ClaimedJob, input domain.ProcessingInput, code, message string, transient bool) {
	err := s.work.Fail(context.Background(), workClaim(claim), input, extraction.Failure{
		Code:      code,
		Message:   message,
		Transient: transient,
		Backoff:   s.backoff(claim.Job.AttemptCount),
	})
	s.logPersistenceError(context.Background(), "fail_processing", claim, err)
}

// policyDenied asks the Policy module whether a provider-backed capability may
// run in the given scopes. A nil authorizer means no policy module is composed,
// which preserves the offline default. The in-transaction policy fence in the
// extraction adapter still runs before commit, so this is the decision point,
// not the only enforcement.
func (s *Service) policyDenied(ctx context.Context, capability policy.Capability, spaceIDs ...string) (bool, error) {
	if s.authorizer == nil || len(spaceIDs) == 0 {
		return false, nil
	}
	decision, err := s.authorizer.Authorize(ctx, workerCall(), policy.Request{Capability: capability, SpaceIDs: spaceIDs})
	if err != nil {
		return false, err
	}
	return !decision.Allowed, nil
}

// filterContextMemories removes context items that the Policy module forbids
// disclosing to the provider. Filtering happens before the provider call and is
// reported as a bounded omission instead of silent truncation.
func (s *Service) filterContextMemories(ctx context.Context, input *domain.ProcessingInput) error {
	if s.disclosure == nil || len(input.ContextMemories) == 0 {
		return nil
	}
	groups := make(map[string][]domain.ProcessingMemory)
	for _, memory := range input.ContextMemories {
		groups[memory.SpaceID] = append(groups[memory.SpaceID], memory)
	}
	allowed := make(map[string]struct{}, len(input.ContextMemories))
	denied := 0
	for spaceID, memories := range groups {
		items := make([]policy.DisclosureItem, 0, len(memories))
		for _, memory := range memories {
			items = append(items, policy.DisclosureItem{MemoryID: memory.MemoryID, RevisionID: memory.RevisionID, Sensitivity: memory.Sensitivity})
		}
		result, err := s.disclosure.Filter(ctx, workerCall(), policy.DisclosureRequest{SpaceID: spaceID, Items: items})
		if err != nil {
			return err
		}
		denied += len(result.Denied)
		for _, item := range result.Allowed {
			allowed[item.MemoryID+"\x00"+item.RevisionID] = struct{}{}
		}
	}
	if denied == 0 {
		return nil
	}
	kept := input.ContextMemories[:0]
	for _, memory := range input.ContextMemories {
		if _, ok := allowed[memory.MemoryID+"\x00"+memory.RevisionID]; ok {
			kept = append(kept, memory)
		}
	}
	input.ContextMemories = kept
	if input.OmissionSummary == nil {
		input.OmissionSummary = make(map[string]int)
	}
	input.OmissionSummary["context_memory_disclosure_denied"] += denied
	return nil
}

func workerCall() gateway.CallContext {
	return gateway.CallContext{Caller: gateway.CallerIdentity{SubjectID: "jobs/worker"}}
}

// workClaim maps a claimed job onto the extraction-owned, transport-free work
// identity. Provider calls happen outside the commit transaction and every
// commit, block, and failure is fenced by this claim.
func workClaim(claim storage.ClaimedJob) extraction.WorkClaim {
	return extraction.WorkClaim{
		JobID:             claim.Job.JobID,
		OperationID:       claim.Job.OperationID,
		JobType:           claim.Job.JobType,
		TargetType:        claim.Job.TargetType,
		TargetID:          claim.Job.TargetID,
		Attempt:           claim.Job.AttemptCount,
		MaxAttempts:       claim.Job.MaxAttempts,
		ProcessingVersion: claim.ProcessingVersion,
		LeaseOwner:        claim.LeaseOwner,
		LeaseGeneration:   claim.LeaseGeneration,
	}
}

func (s *Service) failTransientJob(claim storage.ClaimedJob, code, message string) {
	err := s.repository.FailJob(context.Background(), claim, storage.JobFailure{Code: code, Message: message, Transient: true, Backoff: s.backoff(claim.Job.AttemptCount)})
	s.logPersistenceError(context.Background(), "fail_index_job", claim, err)
}

// failPermanentJob terminates a claimed job whose failure cannot be fixed by
// retrying, so permanent provider rejections and malformed model output do not
// consume the retry budget forever.
func (s *Service) failPermanentJob(claim storage.ClaimedJob, code, message string) {
	err := s.repository.FailJob(context.Background(), claim, storage.JobFailure{Code: code, Message: message, Transient: false})
	s.logPersistenceError(context.Background(), "fail_index_job", claim, err)
}

// failDisabledModelJob terminates a model job that was claimed while the dMem
// Provider Route had no processor. It is not a successful no-op: the job is
// recorded as a permanent failure and no extraction result is committed.
func (s *Service) failDisabledModelJob(claim storage.ClaimedJob) {
	err := s.repository.FailJob(context.Background(), claim, storage.JobFailure{Code: "processing_disabled", Message: "processing provider is not enabled", Transient: false})
	s.logPersistenceError(context.Background(), "fail_disabled_model_job", claim, err)
}

func (s *Service) backoff(attempt int) time.Duration {
	backoff := s.retryInitial
	for i := 1; i < attempt && backoff < s.retryMax; i++ {
		if backoff > s.retryMax/2 {
			return s.retryMax
		}
		backoff *= 2
	}
	if backoff > s.retryMax {
		return s.retryMax
	}
	return backoff
}

func (s *Service) logPersistenceError(ctx context.Context, operation string, claim storage.ClaimedJob, err error) {
	if s.logger == nil {
		return
	}
	if err == nil || errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrLeaseLost) || (ctx.Err() != nil && errors.Is(err, context.Canceled)) {
		return
	}
	attrs := []any{logging.FieldOperation, operation}
	if claim.Job.JobID != "" {
		attrs = append(attrs, "job_id", claim.Job.JobID, "lease_generation", claim.LeaseGeneration)
	}
	attrs = append(attrs, logging.FieldError, err)
	s.logger.ErrorContext(ctx, "jobs persistence operation failed", attrs...)
}
