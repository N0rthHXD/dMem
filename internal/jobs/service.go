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
	"github.com/N0rthHXD/dMem/internal/storage"
)

const maintenanceInterval = time.Second

type Repository interface {
	ClaimJob(context.Context, storage.ClaimJobParams) (storage.ClaimedJob, error)
	HeartbeatJob(context.Context, storage.ClaimedJob, time.Duration) (bool, error)
	PrepareProcessing(context.Context, storage.ClaimedJob, storage.ProcessingPreparationParams) (domain.ProcessingInput, error)
	CommitProcessing(context.Context, storage.ClaimedJob, domain.ProcessingInput, storage.ProcessingCommit) error
	FailProcessing(context.Context, storage.ClaimedJob, domain.ProcessingInput, storage.JobFailure) error
	CompleteJob(context.Context, storage.ClaimedJob, storage.JobResult) error
	FailJob(context.Context, storage.ClaimedJob, storage.JobFailure) error
	CancelClaimedJob(context.Context, storage.ClaimedJob) error
	RecoverExpiredLeases(context.Context) (int, error)
	ExpireJobs(context.Context) (int, error)
	PromoteWaitingJobs(context.Context, int) (int, error)
}

type Service struct {
	repository         Repository
	processor          Processor
	workerCount        int
	leaseDuration      time.Duration
	perJobTimeout      time.Duration
	retryInitial       time.Duration
	retryMax           time.Duration
	preparation        storage.ProcessingPreparationParams
	indexes            IndexRepository
	entityIndexes      EntityIndexRepository
	relationIndexes    RelationIndexRepository
	feedbackAggregates FeedbackAggregateRepository
	aggregates         AggregateRebuilder
	diskPressure       DiskPressureSource
	serviceID          uint64
	processorID        string
	processorEnabled   bool
	healthMu           sync.RWMutex
	processorState     string
	logger             *slog.Logger

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
	workers sync.WaitGroup
}

var nextServiceID atomic.Uint64

type Dependencies struct {
	Indexes            IndexRepository
	EntityIndexes      EntityIndexRepository
	RelationIndexes    RelationIndexRepository
	FeedbackAggregates FeedbackAggregateRepository
	AggregateRebuilder AggregateRebuilder
	DiskPressure       DiskPressureSource
	Logger             *slog.Logger
}

func NewService(repository Repository, cfg config.Config, processor Processor, dependencies Dependencies) (*Service, error) {
	if repository == nil {
		return nil, errors.New("job repository is required")
	}
	if cfg.WorkerCount <= 0 || cfg.LeaseDuration <= 0 || cfg.PerJobTimeout <= 0 || cfg.RetryInitialBackoff <= 0 || cfg.RetryMaxBackoff < cfg.RetryInitialBackoff {
		return nil, errors.New("invalid job service configuration")
	}
	if processor == nil {
		processor = NoopProcessor{}
	}
	if dependencies.Logger == nil {
		dependencies.Logger = slog.Default()
	}
	maxContentChars, maxContext := cfg.ProcessingProvider.MaxContentChars, cfg.ProcessingProvider.MaxContextItems
	if maxContentChars <= 0 {
		maxContentChars = 12000
	}
	if maxContext < 0 {
		maxContext = 0
	}
	identity := processor.Identity()
	if identity == "" {
		identity = "processor/in-process"
	}
	enabled := processor.Enabled()
	state := "degraded"
	if !enabled {
		state = "not_configured"
		identity = "none"
	}
	return &Service{
		repository:    repository,
		processor:     processor,
		workerCount:   cfg.WorkerCount,
		leaseDuration: cfg.LeaseDuration,
		perJobTimeout: cfg.PerJobTimeout,
		retryInitial:  cfg.RetryInitialBackoff,
		retryMax:      cfg.RetryMaxBackoff,
		preparation: storage.ProcessingPreparationParams{
			ProcessorIdentity:  identity,
			AllowedMemoryTypes: []string{"fact", "preference", "constraint", "decision", "task", "event", "project_context", "agent_experience"},
			MaxCandidates:      20, MaxContentChars: maxContentChars, MaxContextEvents: maxContext, MaxContextMemories: maxContext,
		},
		indexes:            dependencies.Indexes,
		entityIndexes:      dependencies.EntityIndexes,
		relationIndexes:    dependencies.RelationIndexes,
		feedbackAggregates: dependencies.FeedbackAggregates,
		aggregates:         dependencies.AggregateRebuilder,
		diskPressure:       dependencies.DiskPressure,
		serviceID:          nextServiceID.Add(1),
		processorID:        identity,
		processorEnabled:   enabled,
		processorState:     state,
		logger:             dependencies.Logger,
		done:               make(chan struct{}),
	}, nil
}

func (s *Service) ProcessorHealth() (string, string) {
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()
	return s.processorState, s.processorID
}

func (s *Service) recordProcessorResult(result domain.ProcessingResult) {
	if !s.processorEnabled {
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

func (s *Service) Wait() error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	done := s.done
	s.mu.Unlock()
	<-done
	return nil
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
	if claim.Job.JobType == "rebuild_fts" || claim.Job.JobType == "rebuild_semantic" || claim.Job.JobType == "rebuild_entity_index" || claim.Job.JobType == "rebuild_relation_index" || claim.Job.JobType == "embed_memory" || claim.Job.JobType == "aggregate_feedback" || claim.Job.JobType == "rebuild_feedback_aggregates" || claim.Job.JobType == "rebuild_aggregate" {
		s.processIndexClaim(processCtx, claim)
		return
	}
	input, err := s.repository.PrepareProcessing(processCtx, claim, s.preparation)
	if err != nil {
		if errors.Is(err, storage.ErrLeaseLost) {
			return
		}
		input = domain.ProcessingInput{ProcessorIdentity: s.preparation.ProcessorIdentity, ProcessingVersion: claim.ProcessingVersion, JobID: claim.Job.JobID, Target: domain.ProcessingEvent{EventID: claim.Job.TargetID}}
		s.failProcessing(claim, input, "preparation_failed", "processing preparation failed", true)
		return
	}
	results := make(chan domain.ProcessingResult, 1)
	go func() {
		results <- s.processor.Process(processCtx, input)
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
		s.failProcessing(claim, input, string(domain.ProcessingInvalidOutput), "processor returned invalid output", false)
		return
	}
	s.recordProcessorResult(result)
	switch result.Code {
	case domain.ProcessingNoProcessor, domain.ProcessingNoMemory, domain.ProcessingCandidatesExtracted, domain.ProcessingPolicyRejected:
		err := s.repository.CommitProcessing(context.Background(), claim, input, storage.ProcessingCommit{Result: result})
		s.logPersistenceError(context.Background(), "commit_processing", claim, err)
	case domain.ProcessingTransientFailure:
		s.failProcessing(claim, input, string(result.Code), "processor transient failure", true)
	case domain.ProcessingPermanentFailure, domain.ProcessingInvalidOutput:
		s.failProcessing(claim, input, string(result.Code), "processor permanent failure", false)
	}
}

func (s *Service) processIndexClaim(ctx context.Context, claim storage.ClaimedJob) {
	operationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
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
			if err != nil {
				if errors.Is(err, storage.ErrEmbeddingPolicy) {
					persistErr := s.repository.CompleteJob(context.Background(), claim, storage.JobResult{Code: string(domain.ProcessingPolicyRejected)})
					s.logPersistenceError(context.Background(), "complete_index_job", claim, persistErr)
					return
				}
				s.failTransientJob(claim, string(domain.ProcessingTransientFailure), "index operation failed")
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

func (s *Service) failTimeout(claim storage.ClaimedJob, input domain.ProcessingInput) {
	s.recordProcessorResult(domain.ProcessingResult{Code: domain.ProcessingTransientFailure})
	s.failProcessing(claim, input, "processor_timeout", "processor timed out", true)
}

func (s *Service) failShutdown(claim storage.ClaimedJob, input domain.ProcessingInput) {
	s.failProcessing(claim, input, "processor_shutdown", "processor interrupted by shutdown", true)
}

func (s *Service) failProcessing(claim storage.ClaimedJob, input domain.ProcessingInput, code, message string, transient bool) {
	err := s.repository.FailProcessing(context.Background(), claim, input, storage.JobFailure{
		Code:      code,
		Message:   message,
		Transient: transient,
		Backoff:   s.backoff(claim.Job.AttemptCount),
	})
	s.logPersistenceError(context.Background(), "fail_processing", claim, err)
}

func (s *Service) failTransientJob(claim storage.ClaimedJob, code, message string) {
	err := s.repository.FailJob(context.Background(), claim, storage.JobFailure{Code: code, Message: message, Transient: true, Backoff: s.backoff(claim.Job.AttemptCount)})
	s.logPersistenceError(context.Background(), "fail_index_job", claim, err)
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
	if err == nil || errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrLeaseLost) || (ctx.Err() != nil && errors.Is(err, context.Canceled)) {
		return
	}
	attrs := []any{"component", "jobs", "operation", operation}
	if claim.Job.JobID != "" {
		attrs = append(attrs, "job_id", claim.Job.JobID, "lease_generation", claim.LeaseGeneration)
	}
	attrs = append(attrs, "error", err)
	s.logger.ErrorContext(ctx, "jobs persistence operation failed", attrs...)
}
