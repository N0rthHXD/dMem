package policy

import "context"

// Store is the narrow persistence port for effective policy resolution.
type Store interface {
	// EffectivePolicy resolves the effective decision for one capability in one
	// scope under the currently active policy generation.
	EffectivePolicy(context.Context, Capability, string) (EffectivePolicy, error)
}

// EffectivePolicy is the resolved policy for one capability and scope.
type EffectivePolicy struct {
	Capability       Capability
	SpaceID          string
	Decision         string
	ReasonCode       string
	PolicyGeneration int
}
