package scope

import (
	"context"
	"errors"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeStore struct {
	ensureScope  domain.Space
	ensureReplay bool
	ensureErr    error
	ensureID     gateway.WriteIdentity
	ensureParams EnsureParams
	ensureCalls  int

	getScope domain.Space
	getErr   error
}

func (f *fakeStore) EnsureScope(_ context.Context, identity gateway.WriteIdentity, params EnsureParams) (domain.Space, bool, error) {
	f.ensureCalls++
	f.ensureID = identity
	f.ensureParams = params
	return f.ensureScope, f.ensureReplay, f.ensureErr
}

func (f *fakeStore) GetScope(context.Context, string) (domain.Space, error) {
	return f.getScope, f.getErr
}

func scopeCall(caller, key, requestID string) gateway.CallContext {
	call := gateway.CallContext{RequestID: requestID, Caller: gateway.CallerIdentity{SubjectID: caller}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func TestScopeEnsureIsAtomicAndIdempotent(t *testing.T) {
	store := &fakeStore{ensureScope: domain.Space{SpaceID: "space-1"}, ensureReplay: true}
	service := NewService(store)
	parent := "parent-1"

	if _, err := service.Ensure(context.Background(), scopeCall("caller-1", "key-1", "request-1"), EnsureCommand{SpaceType: "project"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing external_id error = %v", err)
	}

	command := EnsureCommand{ParentSpaceID: &parent, SpaceType: "project", ExternalID: "ext", DisplayName: "Project"}
	result, err := service.Ensure(context.Background(), scopeCall("caller-1", "key-1", "request-1"), command)
	if err != nil || result.Scope.SpaceID != "space-1" || !result.Replay {
		t.Fatalf("Ensure() = %+v error = %v", result, err)
	}
	if store.ensureCalls != 1 || store.ensureID.Operation != OperationEnsure || store.ensureID.IdempotencyKey != "key-1" {
		t.Fatalf("ensure identity = %+v calls=%d", store.ensureID, store.ensureCalls)
	}
	wantHash, _ := gateway.CanonicalHash(command)
	if store.ensureID.CommandHash != wantHash || store.ensureParams.ParentSpaceID == nil || *store.ensureParams.ParentSpaceID != "parent-1" || store.ensureParams.DisplayName != "Project" {
		t.Fatalf("ensure identity/params = %+v %+v", store.ensureID, store.ensureParams)
	}
}

func TestScopeEnsureClassifiesErrors(t *testing.T) {
	service := NewService(&fakeStore{ensureErr: gateway.NewError(gateway.ErrorConflict, "scope identity conflict")})
	command := EnsureCommand{SpaceType: "project", ExternalID: "ext", DisplayName: "Project"}
	if _, err := service.Ensure(context.Background(), scopeCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorConflict) {
		t.Fatalf("error = %v, want conflict", err)
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

func TestScopeEnsureClassifiesUnknownError(t *testing.T) {
	service := NewService(&fakeStore{ensureErr: errors.New("boom")})
	command := EnsureCommand{SpaceType: "project", ExternalID: "ext", DisplayName: "Project"}
	if _, err := service.Ensure(context.Background(), scopeCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("error = %v, want internal", err)
	}
}
