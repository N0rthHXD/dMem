package gateway

import "fmt"

const (
	// DefaultPageLimit is used when a caller does not request a page size.
	DefaultPageLimit = 50
	// MaxPageLimit is the largest page size a gateway module accepts.
	MaxPageLimit = 200
)

// PageRequest is the protocol-neutral pagination input. Cursors are opaque to
// callers and are produced by the owning module.
type PageRequest struct {
	Limit  int
	Cursor string
}

// EffectiveLimit returns the bounded page size, applying the default when the
// caller did not request one.
func (p PageRequest) EffectiveLimit() int {
	if p.Limit <= 0 {
		return DefaultPageLimit
	}
	if p.Limit > MaxPageLimit {
		return MaxPageLimit
	}
	return p.Limit
}

// Validate reports whether the requested page size is within range. A zero limit
// is valid and means "use the default".
func (p PageRequest) Validate() error {
	if p.Limit < 0 || p.Limit > MaxPageLimit {
		return fmt.Errorf("gateway: page limit must be between 1 and %d", MaxPageLimit)
	}
	return nil
}
