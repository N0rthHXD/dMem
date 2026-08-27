package aggregate

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

var ErrUnsupported = errors.New("aggregate kind unsupported")

type processingSnapshot struct {
	provider         domain.AggregateProvider
	processor        string
	policy           string
	version          string
	maxInputs        int
	generation       int
	policyGeneration int
}

type Service struct {
	repository *storage.Repository
	processing atomic.Pointer[processingSnapshot]
}

func NewService(repository *storage.Repository, provider domain.AggregateProvider, processor, policy, version string, maxInputs int) *Service {
	if maxInputs <= 0 {
		maxInputs = 32
	}
	s := &Service{repository: repository}
	s.processing.Store(&processingSnapshot{provider: provider, processor: processor, policy: policy, version: version, maxInputs: maxInputs})
	return s
}

func (s *Service) SetProcessing(provider domain.AggregateProvider, processor, policy, version string, maxInputs int, generation, policyGeneration int) {
	if maxInputs <= 0 {
		maxInputs = 32
	}
	s.processing.Store(&processingSnapshot{provider: provider, processor: processor, policy: policy, version: version, maxInputs: maxInputs, generation: generation, policyGeneration: policyGeneration})
}

func (s *Service) currentProcessing() *processingSnapshot {
	return s.processing.Load()
}

func (s *Service) Build(ctx context.Context, identity storage.WriteIdentity, space, kind string, revisions []string) (domain.AggregateProposal, bool, error) {
	rt := s.currentProcessing()
	if rt.provider == nil {
		return domain.AggregateProposal{}, false, ErrUnsupported
	}
	prepared, err := s.repository.PrepareAggregateBuild(ctx, space, kind, revisions, rt.maxInputs)
	if err != nil {
		return domain.AggregateProposal{}, false, err
	}
	result, err := rt.provider.Build(ctx, domain.AggregateProviderInput{SpaceID: space, Kind: kind, PolicyVersion: rt.policy, ProcessingVersion: rt.version, Inputs: prepared})
	if err != nil {
		return domain.AggregateProposal{}, false, err
	}
	params := storage.CommitAggregateProposalParams{SpaceID: space, AggregateKind: kind, Inputs: prepared, Content: result.Content, ValidFrom: result.ValidFrom, ValidUntil: result.ValidUntil, Sensitivity: result.Sensitivity, PolicyVersion: rt.policy, ProcessorIdentity: rt.processor, ProviderIdentity: result.ProviderIdentity, ModelIdentity: result.ModelIdentity, ProcessingVersion: rt.version}
	return s.repository.CommitAggregateProposal(ctx, identity, params, nil)
}

func (s *Service) RebuildClaimed(ctx context.Context, claim storage.ClaimedJob) error {
	rt := s.currentProcessing()
	if rt.provider == nil {
		return ErrUnsupported
	}
	space, kind, revisions, err := s.repository.PrepareAggregateRebuild(ctx, claim, rt.maxInputs)
	if err != nil {
		return err
	}
	prepared, err := s.repository.PrepareAggregateBuild(ctx, space, kind, revisions, rt.maxInputs)
	if err != nil {
		return err
	}
	result, err := rt.provider.Build(ctx, domain.AggregateProviderInput{SpaceID: space, Kind: kind, PolicyVersion: rt.policy, ProcessingVersion: rt.version, Inputs: prepared})
	if err != nil {
		return err
	}
	params := storage.CommitAggregateProposalParams{SpaceID: space, AggregateKind: kind, Inputs: prepared, Content: result.Content, ValidFrom: result.ValidFrom, ValidUntil: result.ValidUntil, Sensitivity: result.Sensitivity, PolicyVersion: rt.policy, ProcessorIdentity: rt.processor, ProviderIdentity: result.ProviderIdentity, ModelIdentity: result.ModelIdentity, ProcessingVersion: rt.version}
	_, _, err = s.repository.CommitAggregateProposal(ctx, storage.WriteIdentity{}, params, &claim)
	return err
}
