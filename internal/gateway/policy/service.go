package policy

import (
	"context"
	"errors"
	"strings"

	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Decision reason codes published by the Policy module. They are stable,
// content-safe identifiers that may cross a protocol boundary.
const (
	// ReasonDefaultAllow marks an allow produced because no effective policy
	// state exists. This preserves offline defaults.
	ReasonDefaultAllow = "default_allow"
	// ReasonAllowed marks an allow produced by an explicit policy assignment.
	ReasonAllowed = "allowed"
	// ReasonEvidenceDisclosureDenied marks a filtered disclosure.
	ReasonEvidenceDisclosureDenied = "evidence_disclosure_denied"
	// ReasonSensitiveDenied marks a filtered sensitive item.
	ReasonSensitiveDenied = "sensitive_export_denied"
)

// ServiceImpl is the default protocol-independent Policy service. It owns
// authorization decisions and disclosure partitioning over the effective
// policy data exposed by the Store port. It never reads the database directly
// and never returns a storage error.
type ServiceImpl struct {
	store Store
}

var (
	_ Authorizer       = (*ServiceImpl)(nil)
	_ DisclosureFilter = (*ServiceImpl)(nil)
)

// NewService wires the Policy service over an effective-policy store port.
func NewService(store Store) *ServiceImpl {
	return &ServiceImpl{store: store}
}

// Authorize resolves whether a caller may exercise one capability in every
// requested scope. An unconfigured capability or scope allows, preserving the
// offline default. A single explicit deny denies the whole request so a
// multi-scope operation cannot partially bypass policy.
func (s *ServiceImpl) Authorize(ctx context.Context, call gateway.CallContext, request Request) (Decision, error) {
	if !request.Capability.Valid() {
		return Decision{}, gateway.NewError(gateway.ErrorInvalidInput, "policy capability is invalid")
	}
	if len(request.SpaceIDs) == 0 {
		return Decision{}, gateway.NewError(gateway.ErrorInvalidInput, "at least one scope is required")
	}
	decision := Decision{Allowed: true, ReasonCode: ReasonDefaultAllow}
	configured := false
	seen := make(map[string]struct{}, len(request.SpaceIDs))
	for _, spaceID := range request.SpaceIDs {
		if strings.TrimSpace(spaceID) == "" {
			return Decision{}, gateway.NewError(gateway.ErrorInvalidInput, "scope identifiers must not be empty")
		}
		if _, exists := seen[spaceID]; exists {
			continue
		}
		seen[spaceID] = struct{}{}
		effective, err := s.store.EffectivePolicy(ctx, request.Capability, spaceID)
		if errors.Is(err, ErrUnconfigured) {
			continue
		}
		if err != nil {
			return Decision{}, normalizeError(err)
		}
		if effective.Decision != "allow" {
			return Decision{
				Allowed:          false,
				ReasonCode:       effective.ReasonCode,
				PolicyGeneration: effective.PolicyGeneration,
			}, nil
		}
		configured = true
		if effective.PolicyGeneration > decision.PolicyGeneration {
			decision.PolicyGeneration = effective.PolicyGeneration
		}
	}
	if configured {
		decision.ReasonCode = ReasonAllowed
	}
	return decision, nil
}

// Filter partitions items that may cross a disclosure boundary. Evidence
// disclosure is checked for the scope, and sensitive items additionally require
// sensitive-export authorization. Both capabilities allow when unconfigured, so
// an offline deployment discloses exactly as before.
func (s *ServiceImpl) Filter(ctx context.Context, call gateway.CallContext, request DisclosureRequest) (DisclosureResult, error) {
	if strings.TrimSpace(request.SpaceID) == "" {
		return DisclosureResult{}, gateway.NewError(gateway.ErrorInvalidInput, "scope is required")
	}
	disclosure, err := s.Authorize(ctx, call, Request{Capability: CapabilityEvidenceDisclosure, SpaceIDs: []string{request.SpaceID}})
	if err != nil {
		return DisclosureResult{}, err
	}
	sensitive, err := s.Authorize(ctx, call, Request{Capability: CapabilitySensitiveExport, SpaceIDs: []string{request.SpaceID}})
	if err != nil {
		return DisclosureResult{}, err
	}
	result := DisclosureResult{Allowed: []DisclosureItem{}, Denied: []DisclosureDenial{}}
	for _, item := range request.Items {
		switch {
		case !disclosure.Allowed:
			result.Denied = append(result.Denied, DisclosureDenial{Item: item, ReasonCode: reasonOr(disclosure.ReasonCode, ReasonEvidenceDisclosureDenied)})
		case item.Sensitivity == "sensitive" && !sensitive.Allowed:
			result.Denied = append(result.Denied, DisclosureDenial{Item: item, ReasonCode: reasonOr(sensitive.ReasonCode, ReasonSensitiveDenied)})
		default:
			result.Allowed = append(result.Allowed, item)
		}
	}
	return result, nil
}

func reasonOr(reason, fallback string) string {
	if reason == "" {
		return fallback
	}
	return reason
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return gateway.WrapError(gateway.ErrorInternal, "policy operation failed", err)
}
