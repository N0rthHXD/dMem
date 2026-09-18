package scope

import (
	"context"
	"errors"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeStore struct {
	createScope  domain.Space
	createReplay bool
	createErr    error
	createID     gateway.WriteIdentity
	createParams CreateParams
	createCalls  int

	resolveScope  domain.Space
	resolveErr    error
	resolveParams ResolveParams

	getScope domain.Space
	getErr   error

	listPage   Page
	listErr    error
	listParams ListParams
}

func (f *fakeStore) CreateScope(_ context.Context, identity gateway.WriteIdentity, params CreateParams) (domain.Space, bool, error) {
	f.createCalls++
	f.createID = identity
	f.createParams = params
	return f.createScope, f.createReplay, f.createErr
}

func (f *fakeStore) ResolveScope(_ context.Context, params ResolveParams) (domain.Space, error) {
	f.resolveParams = params
	return f.resolveScope, f.resolveErr
}

func (f *fakeStore) GetScope(context.Context, string) (domain.Space, error) {
	return f.getScope, f.getErr
}

func (f *fakeStore) ListScopes(_ context.Context, params ListParams) (Page, error) {
	f.listParams = params
	return f.listPage, f.listErr
}

func scopeCall(caller, key, requestID string) gateway.CallContext {
	call := gateway.CallContext{RequestID: requestID, Caller: gateway.CallerIdentity{SubjectID: caller}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func TestScopeCreateValidatesAndPropagatesIdentity(t *testing.T) {
	store := &fakeStore{createScope: domain.Space{SpaceID: "space-1", SpaceType: "project"}}
	service := NewService(store)

	for name, command := range map[string]CreateCommand{
		"bad type":     {SpaceType: "galaxy", ExternalID: "ext", DisplayName: "Name"},
		"empty ext":    {SpaceType: "project", DisplayName: "Name"},
		"long display": {SpaceType: "project", ExternalID: "ext", DisplayName: string(make([]rune, 257))},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Create(context.Background(), scopeCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("error = %v, want invalid_input", err)
			}
		})
	}
	if store.createCalls != 0 {
		t.Fatalf("store called %d times for invalid input", store.createCalls)
	}

	command := CreateCommand{SpaceType: "project", ExternalID: "ext", DisplayName: "Name"}
	result, err := service.Create(context.Background(), scopeCall("caller-1", "key-1", "request-1"), command)
	if err != nil || result.Scope.SpaceID != "space-1" || result.Replay {
		t.Fatalf("Create() = %+v error = %v", result, err)
	}
	if store.createID.Operation != OperationCreate || store.createID.IdempotencyKey != "key-1" {
		t.Fatalf("write identity = %+v", store.createID)
	}
	wantHash, _ := gateway.CanonicalHash(command)
	if store.createID.CommandHash != wantHash {
		t.Fatalf("command hash = %q, want %q", store.createID.CommandHash, wantHash)
	}
}

func TestScopeResolvePassesExternalIdentity(t *testing.T) {
	store := &fakeStore{resolveScope: domain.Space{SpaceID: "space-1"}}
	service := NewService(store)
	parent := "parent-1"

	if _, err := service.Resolve(context.Background(), scopeCall("caller-1", "", "request-1"), ResolveQuery{SpaceType: "project"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing external_id error = %v", err)
	}

	resolved, err := service.Resolve(context.Background(), scopeCall("caller-1", "", "request-1"), ResolveQuery{ParentSpaceID: &parent, SpaceType: "project", ExternalID: "ext"})
	if err != nil || resolved.SpaceID != "space-1" {
		t.Fatalf("Resolve() = %+v error = %v", resolved, err)
	}
	if store.resolveParams.ParentSpaceID == nil || *store.resolveParams.ParentSpaceID != "parent-1" || store.resolveParams.SpaceType != "project" || store.resolveParams.ExternalID != "ext" {
		t.Fatalf("resolve params = %+v", store.resolveParams)
	}
}

func TestScopeResolveClassifiesNotFound(t *testing.T) {
	service := NewService(&fakeStore{resolveErr: gateway.NewError(gateway.ErrorNotFound, "no scope")})
	if _, err := service.Resolve(context.Background(), scopeCall("caller-1", "", "request-1"), ResolveQuery{SpaceType: "project", ExternalID: "ext"}); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("error = %v, want not_found", err)
	}
}

func TestScopeGetValidatesIdentifier(t *testing.T) {
	service := NewService(&fakeStore{getScope: domain.Space{SpaceID: "space-1"}})
	if _, err := service.Get(context.Background(), scopeCall("caller-1", "", "request-1"), GetQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing id error = %v", err)
	}
	if _, err := service.Get(context.Background(), scopeCall("caller-1", "", "request-1"), GetQuery{SpaceID: "space-1"}); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestScopeListAppliesBoundedLimit(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)

	if _, err := service.List(context.Background(), scopeCall("caller-1", "", "request-1"), ListQuery{Page: gateway.PageRequest{Limit: gateway.MaxPageLimit + 1}}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("over-limit error = %v", err)
	}

	if _, err := service.List(context.Background(), scopeCall("caller-1", "", "request-1"), ListQuery{}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listParams.Limit != gateway.DefaultPageLimit {
		t.Fatalf("default limit = %d, want %d", store.listParams.Limit, gateway.DefaultPageLimit)
	}

	if _, err := service.List(context.Background(), scopeCall("caller-1", "", "request-1"), ListQuery{Page: gateway.PageRequest{Limit: 10, Cursor: "cursor-1"}}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listParams.Limit != 10 || store.listParams.Cursor != "cursor-1" {
		t.Fatalf("list params = %+v", store.listParams)
	}
}

func TestScopeListClassifiesUnknownError(t *testing.T) {
	service := NewService(&fakeStore{listErr: errors.New("boom")})
	if _, err := service.List(context.Background(), scopeCall("caller-1", "", "request-1"), ListQuery{}); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("error = %v, want internal", err)
	}
}
