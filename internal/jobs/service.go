package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/storage"
)

const maintenanceInterval = time.Second

type Repository interface {
	ClaimJob(context.Context, storage.ClaimJobParams) (storage.ClaimedJob, error)
	HeartbeatJob(context.Context, storage.ClaimedJob, time.Duration) (bool, error)
	CompleteJob(context.Context, storage.ClaimedJob, storage.JobResult) error
	FailJob(context.Context, storage.ClaimedJob, storage.JobFailure) error
	CancelClaimedJob(context.Context, storage.ClaimedJob) error
	RecoverExpiredLeases(context.Context) (int, error)
	ExpireJobs(context.Context) (int, error)
	PromoteWaitingJobs(context.Context, int) (int, error)
}

type Service struct {
	repository    Repository
	processor     Processor
	workerCount   int
	leaseDuration time.Duration
	perJobTimeout time.Duration
	retryInitial  time.Duration
	retryMax      time.Duration
	serviceID     uint64

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
	workers sync.WaitGroup
}

var nextServiceID atomic.Uint64

func NewService(repository Repository, cfg config.Config, processor Processor) (*Service, error) {
	if repository == nil {
		return nil, errors.New("job repository is required")
	}
	if cfg.WorkerCount <= 0 || cfg.LeaseDuration <= 0 || cfg.PerJobTimeout <= 0 || cfg.RetryInitialBackoff <= 0 || cfg.RetryMaxBackoff < cfg.RetryInitialBackoff {
		return nil, errors.New("invalid job service configuration")
	}
	if processor == nil {
		processor = NoopProcessor{}
	}
	return &Service{
		repository:    repository,
		processor:     processor,
		workerCount:   cfg.WorkerCount,
		leaseDuration: cfg.LeaseDuration,
		perJobTimeout: cfg.PerJobTimeout,
		retryInitial:  cfg.RetryInitialBackoff,
		retryMax:      cfg.RetryMaxBackoff,
		serviceID:     nextServiceID.Add(1),
		done:          make(chan struct{}),
	}, nil
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
			_ = s.maintain(ctx)
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
		if pressure, ok := s.repository.(interface {
			DiskPressure() (string, error)
		}); ok {
			state, err := pressure.DiskPressure()
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
			_, _ = s.repository.PromoteWaitingJobs(ctx, s.workerCount)
			continue
		}
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
	results := make(chan Outcome, 1)
	go func() {
		results <- s.processor.Process(processCtx, claim.Job)
	}()

	heartbeat := time.NewTicker(s.leaseDuration / 3)
	defer heartbeat.Stop()
	for {
		select {
		case outcome := <-results:
			if serviceCtx.Err() != nil {
				s.failShutdown(claim)
				return
			}
			if errors.Is(processCtx.Err(), context.DeadlineExceeded) {
				s.failTimeout(claim)
				return
			}
			s.applyOutcome(claim, outcome)
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
					s.failShutdown(claim)
				} else {
					s.fail(claim, Outcome{Code: OutcomeTransientFailure, Message: err.Error()})
				}
				return
			}
			if cancelRequested {
				cancel()
				_ = discardLeaseLost(s.repository.CancelClaimedJob(context.Background(), claim))
				return
			}
		case <-processCtx.Done():
			if serviceCtx.Err() != nil {
				s.failShutdown(claim)
			} else {
				s.failTimeout(claim)
			}
			return
		}
	}
}

func (s *Service) applyOutcome(claim storage.ClaimedJob, outcome Outcome) {
	switch outcome.Code {
	case OutcomeNoProcessor, OutcomeNoMemory, OutcomeCandidatesExtracted, OutcomeIndexRebuilt:
		_ = discardLeaseLost(s.repository.CompleteJob(context.Background(), claim, storage.JobResult{Code: string(outcome.Code), Value: outcome.Value}))
	case OutcomeTransientFailure:
		s.fail(claim, outcome)
	case OutcomePermanentFailure, OutcomeInvalidOutput, OutcomePolicyRejected:
		_ = discardLeaseLost(s.repository.FailJob(context.Background(), claim, storage.JobFailure{Code: string(outcome.Code), Message: outcome.Message}))
	default:
		_ = discardLeaseLost(s.repository.FailJob(context.Background(), claim, storage.JobFailure{Code: string(OutcomeInvalidOutput), Message: "processor returned an unknown outcome"}))
	}
}

func (s *Service) fail(claim storage.ClaimedJob, outcome Outcome) {
	s.failTransient(claim, string(outcome.Code), outcome.Message)
}

func (s *Service) failTimeout(claim storage.ClaimedJob) {
	s.failTransient(claim, "processor_timeout", "processor timed out")
}

func (s *Service) failShutdown(claim storage.ClaimedJob) {
	s.failTransient(claim, "processor_shutdown", "processor interrupted by shutdown")
}

func (s *Service) failTransient(claim storage.ClaimedJob, code, message string) {
	_ = discardLeaseLost(s.repository.FailJob(context.Background(), claim, storage.JobFailure{
		Code:      code,
		Message:   message,
		Transient: true,
		Backoff:   s.backoff(claim.Job.AttemptCount),
	}))
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

func discardLeaseLost(err error) error {
	if errors.Is(err, storage.ErrLeaseLost) {
		return nil
	}
	return err
}
