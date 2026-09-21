package scope

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

var validScopeTypes = map[string]bool{
	"user":        true,
	"agent":       true,
	"application": true,
	"project":     true,
	"task":        true,
	"session":     true,
}

// ServiceImpl is the default protocol-independent Scope service. It owns scope
// identity validation, idempotency identity, and stable error mapping.
type ServiceImpl struct {
	store Store
}

// NewService wires the Scope service over a scope store port.
func NewService(store Store) *ServiceImpl {
	return &ServiceImpl{store: store}
}

// Get reads one scope by identifier.
func (s *ServiceImpl) Get(ctx context.Context, call gateway.CallContext, query GetQuery) (domain.Space, error) {
	if strings.TrimSpace(query.SpaceID) == "" {
		return domain.Space{}, gateway.NewError(gateway.ErrorInvalidInput, "space_id is required")
	}
	if s.store == nil {
		return domain.Space{}, gateway.NewError(gateway.ErrorCapabilityUnavailable, "scope lookup is unavailable")
	}
	found, err := s.store.GetScope(ctx, query.SpaceID)
	if err != nil {
		return domain.Space{}, normalizeError(err)
	}
	return found, nil
}

func (s *ServiceImpl) Ensure(ctx context.Context, call gateway.CallContext, command EnsureCommand) (EnsureResult, error) {
	if err := validateEnsure(command); err != nil {
		return EnsureResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationEnsure, command)
	if err != nil {
		return EnsureResult{}, gateway.WriteIdentityError(err)
	}
	ensured, replay, err := s.store.EnsureScope(ctx, identity, EnsureParams(command))
	if err != nil {
		return EnsureResult{}, normalizeError(err)
	}
	return EnsureResult{WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay}, Scope: ensured}, nil
}

func validateEnsure(command EnsureCommand) error {
	if !validScopeTypes[command.SpaceType] {
		return errors.New("space_type is invalid")
	}
	if command.ParentSpaceID != nil && (strings.TrimSpace(*command.ParentSpaceID) != *command.ParentSpaceID || utf8.RuneCountInString(*command.ParentSpaceID) < 1 || utf8.RuneCountInString(*command.ParentSpaceID) > 128) {
		return errors.New("parent_space_id is invalid")
	}
	if strings.TrimSpace(command.ExternalID) != command.ExternalID || utf8.RuneCountInString(command.ExternalID) < 1 || utf8.RuneCountInString(command.ExternalID) > 128 {
		return errors.New("external_id length must be between 1 and 128")
	}
	if strings.TrimSpace(command.DisplayName) != command.DisplayName || utf8.RuneCountInString(command.DisplayName) < 1 || utf8.RuneCountInString(command.DisplayName) > 256 {
		return errors.New("display_name length must be between 1 and 256")
	}
	return nil
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) {
		return err
	}
	return gateway.WrapError(gateway.ErrorInternal, "scope operation failed", err)
}
