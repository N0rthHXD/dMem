package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/gateway/policy"
)

// PolicyCapability adapts the SQLite repository to the Policy module's effective
// policy port. It resolves concrete authorization and disclosure data; decision
// policy remains in the gateway Policy service.
type PolicyCapability struct {
	repo *Repository
}

// NewPolicyCapability wires the effective policy port over one repository.
func NewPolicyCapability(repo *Repository) *PolicyCapability {
	return &PolicyCapability{repo: repo}
}

// EffectivePolicy resolves the effective decision for one capability in one
// scope under the currently active policy generation.
func (c *PolicyCapability) EffectivePolicy(ctx context.Context, capability policy.Capability, spaceID string) (policy.EffectivePolicy, error) {
	effective, err := c.repo.EffectivePolicy(ctx, string(capability), spaceID)
	if err != nil {
		return policy.EffectivePolicy{}, err
	}
	return policy.EffectivePolicy{
		Capability:       capability,
		SpaceID:          effective.SpaceID,
		Decision:         effective.Decision,
		ReasonCode:       effective.ReasonCode,
		PolicyGeneration: effective.PolicyGeneration,
	}, nil
}
