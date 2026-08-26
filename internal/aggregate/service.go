package aggregate

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

var ErrUnsupported = errors.New("aggregate kind unsupported")

type Service struct {
	repository *storage.Repository
	provider   domain.AggregateProvider
	processor  string
	policy     string
	version    string
	maxInputs  int
}

func NewService(repository *storage.Repository, provider domain.AggregateProvider, processor, policy, version string, maxInputs int) *Service {
	if maxInputs <= 0 {
		maxInputs = 32
	}
	return &Service{repository: repository, provider: provider, processor: processor, policy: policy, version: version, maxInputs: maxInputs}
}

func (s *Service) Build(ctx context.Context, identity storage.WriteIdentity, space, kind string, revisions []string) (domain.AggregateProposal, bool, error) {
	if s.provider == nil {
		return domain.AggregateProposal{}, false, ErrUnsupported
	}
	prepared, err := s.repository.PrepareAggregateBuild(ctx, space, kind, revisions, s.maxInputs)
	if err != nil {
		return domain.AggregateProposal{}, false, err
	}
	result, err := s.provider.Build(ctx, domain.AggregateProviderInput{SpaceID: space, Kind: kind, PolicyVersion: s.policy, ProcessingVersion: s.version, Inputs: prepared})
	if err != nil {
		return domain.AggregateProposal{}, false, err
	}
	params := storage.CommitAggregateProposalParams{SpaceID: space, AggregateKind: kind, Inputs: prepared, Content: result.Content, ValidFrom: result.ValidFrom, ValidUntil: result.ValidUntil, Sensitivity: result.Sensitivity, PolicyVersion: s.policy, ProcessorIdentity: s.processor, ProviderIdentity: result.ProviderIdentity, ModelIdentity: result.ModelIdentity, ProcessingVersion: s.version}
	return s.repository.CommitAggregateProposal(ctx, identity, params, nil)
}

func (s *Service) RebuildClaimed(ctx context.Context, claim storage.ClaimedJob) error {
	if s.provider == nil {
		return ErrUnsupported
	}
	space, kind, revisions, err := s.repository.PrepareAggregateRebuild(ctx, claim, s.maxInputs)
	if err != nil {
		return err
	}
	prepared, err := s.repository.PrepareAggregateBuild(ctx, space, kind, revisions, s.maxInputs)
	if err != nil {
		return err
	}
	result, err := s.provider.Build(ctx, domain.AggregateProviderInput{SpaceID: space, Kind: kind, PolicyVersion: s.policy, ProcessingVersion: s.version, Inputs: prepared})
	if err != nil {
		return err
	}
	params := storage.CommitAggregateProposalParams{SpaceID: space, AggregateKind: kind, Inputs: prepared, Content: result.Content, ValidFrom: result.ValidFrom, ValidUntil: result.ValidUntil, Sensitivity: result.Sensitivity, PolicyVersion: s.policy, ProcessorIdentity: s.processor, ProviderIdentity: result.ProviderIdentity, ModelIdentity: result.ModelIdentity, ProcessingVersion: s.version}
	_, _, err = s.repository.CommitAggregateProposal(ctx, storage.WriteIdentity{}, params, &claim)
	return err
}
