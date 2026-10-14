package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/conflict"
)

// ConflictCapability adapts the SQLite conflict case routines to the conflict
// module's store port. It translates storage failures into stable gateway error
// codes while retaining the original cause.
type ConflictCapability struct {
	repo *Repository
}

// NewConflictCapability wires the conflict administration store port over one
// repository.
func NewConflictCapability(repo *Repository) *ConflictCapability {
	return &ConflictCapability{repo: repo}
}

// ListConflictCases returns one bounded page of conflict cases.
func (c *ConflictCapability) ListConflictCases(ctx context.Context, filter conflict.ConflictFilter) (domain.ConflictCasePage, error) {
	page, err := c.repo.ListConflictCases(ctx, ConflictCaseFilter{
		SpaceID: filter.SpaceID,
		State:   filter.State,
		Limit:   filter.Limit,
		Cursor:  filter.Cursor,
	})
	if err != nil {
		return domain.ConflictCasePage{}, gatewayError(err)
	}
	return page, nil
}

// GetConflictCase returns one conflict case by identifier.
func (c *ConflictCapability) GetConflictCase(ctx context.Context, id string) (domain.ConflictCase, error) {
	value, err := c.repo.GetConflictCase(ctx, id)
	if err != nil {
		return domain.ConflictCase{}, gatewayError(err)
	}
	return value, nil
}

// ResolveConflict closes one open case under the expected-participant fence.
func (c *ConflictCapability) ResolveConflict(ctx context.Context, identity gateway.WriteIdentity, params conflict.ResolveParams) (domain.ConflictCase, string, bool, error) {
	resolved, operationID, replay, err := c.repo.ResolveConflict(ctx, gatewayWriteIdentity(identity), params.CaseID, ResolveConflictParams{
		ResolutionType:  params.ResolutionType,
		ExpectedTargets: params.ExpectedTargets,
		SelectedTarget:  params.SelectedTarget,
		ProposedContent: params.ProposedContent,
		ValidFrom:       params.ValidFrom,
		ValidUntil:      params.ValidUntil,
		Evidence:        resolutionEvidenceFromGateway(params.Evidence),
		ReplacementMode: params.ReplacementMode,
		ResultStates:    params.ResultStates,
		Reason:          params.Reason,
	})
	if err != nil {
		return domain.ConflictCase{}, "", replay, gatewayError(err)
	}
	return resolved, operationID, replay, nil
}

func resolutionEvidenceFromGateway(items []conflict.EvidenceParams) []domain.RevisionEvidence {
	result := make([]domain.RevisionEvidence, len(items))
	for i, item := range items {
		result[i] = domain.RevisionEvidence{EventID: item.EventID, Role: item.Role, StartOffset: item.StartOffset, EndOffset: item.EndOffset}
	}
	return result
}
