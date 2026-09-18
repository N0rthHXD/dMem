package policy

import (
	"context"
	"errors"
)

// ErrUnconfigured reports that no effective policy state exists for a
// capability or scope, so the deployment default applies. A SQLite adapter
// returns it instead of a storage sentinel so the policy module never inspects
// storage errors. Unconfigured policy is an allow: the offline default must be
// preserved.
var ErrUnconfigured = errors.New("policy: no effective policy is configured")

// Store is the narrow persistence port for effective policy resolution.
type Store interface {
	// EffectivePolicy resolves the effective decision for one capability in one
	// scope under the currently active policy generation. It returns
	// ErrUnconfigured when neither a capability state nor a scope policy exists.
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
