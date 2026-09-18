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
// type and identity validation, paging defaults, idempotency identity, and
// stable error mapping, and delegates persistence to the Store port.
type ServiceImpl struct {
	store Store
}

// NewService wires the Scope service over a scope store port.
func NewService(store Store) *ServiceImpl {
	return &ServiceImpl{store: store}
}

// Create validates the scope identity and atomically persists the scope, its
// initial effective policy, and the idempotency result.
func (s *ServiceImpl) Create(ctx context.Context, call gateway.CallContext, command CreateCommand) (CreateResult, error) {
	if err := validateCreate(command); err != nil {
		return CreateResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationCreate, command)
	if err != nil {
		return CreateResult{}, gateway.WriteIdentityError(err)
	}
	created, replay, err := s.store.CreateScope(ctx, identity, CreateParams{
		ParentSpaceID: command.ParentSpaceID,
		SpaceType:     command.SpaceType,
		ExternalID:    command.ExternalID,
		DisplayName:   command.DisplayName,
	})
	if err != nil {
		return CreateResult{}, normalizeError(err)
	}
	return CreateResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay},
		Scope:       created,
	}, nil
}

// Resolve looks up one scope by its stable external identity.
func (s *ServiceImpl) Resolve(ctx context.Context, call gateway.CallContext, query ResolveQuery) (domain.Space, error) {
	if err := validateResolve(query); err != nil {
		return domain.Space{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	resolved, err := s.store.ResolveScope(ctx, ResolveParams{
		ParentSpaceID: query.ParentSpaceID,
		SpaceType:     query.SpaceType,
		ExternalID:    query.ExternalID,
	})
	if err != nil {
		return domain.Space{}, normalizeError(err)
	}
	return resolved, nil
}

// Get reads one scope by identifier.
func (s *ServiceImpl) Get(ctx context.Context, call gateway.CallContext, query GetQuery) (domain.Space, error) {
	if strings.TrimSpace(query.SpaceID) == "" {
		return domain.Space{}, gateway.NewError(gateway.ErrorInvalidInput, "space_id is required")
	}
	found, err := s.store.GetScope(ctx, query.SpaceID)
	if err != nil {
		return domain.Space{}, normalizeError(err)
	}
	return found, nil
}

// List returns one bounded cursor page of scopes.
func (s *ServiceImpl) List(ctx context.Context, call gateway.CallContext, query ListQuery) (Page, error) {
	if err := query.Page.Validate(); err != nil {
		return Page{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	page, err := s.store.ListScopes(ctx, ListParams{
		ParentSpaceID: query.ParentSpaceID,
		Limit:         query.Page.EffectiveLimit(),
		Cursor:        query.Page.Cursor,
	})
	if err != nil {
		return Page{}, normalizeError(err)
	}
	return page, nil
}

func validateCreate(command CreateCommand) error {
	if !validScopeTypes[command.SpaceType] {
		return errors.New("space_type is invalid")
	}
	if utf8.RuneCountInString(command.ExternalID) < 1 || utf8.RuneCountInString(command.ExternalID) > 128 {
		return errors.New("external_id length must be between 1 and 128")
	}
	if utf8.RuneCountInString(command.DisplayName) < 1 || utf8.RuneCountInString(command.DisplayName) > 256 {
		return errors.New("display_name length must be between 1 and 256")
	}
	return nil
}

func validateResolve(query ResolveQuery) error {
	if !validScopeTypes[query.SpaceType] {
		return errors.New("space_type is invalid")
	}
	if utf8.RuneCountInString(query.ExternalID) < 1 || utf8.RuneCountInString(query.ExternalID) > 128 {
		return errors.New("external_id length must be between 1 and 128")
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
