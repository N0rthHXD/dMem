package conflict

import (
	"context"
	"errors"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeStore struct {
	filter     ConflictFilter
	listPage   domain.ConflictCasePage
	listErr    error
	getValue   domain.ConflictCase
	getErr     error
	resolveVal domain.ConflictCase
	resolveOp  string
	resolveRep bool
	resolveErr error
	resolveID  gateway.WriteIdentity
	resolveP   ResolveParams
}

func (f *fakeStore) ListConflictCases(_ context.Context, filter ConflictFilter) (domain.ConflictCasePage, error) {
	f.filter = filter
	return f.listPage, f.listErr
}

func (f *fakeStore) GetConflictCase(context.Context, string) (domain.ConflictCase, error) {
	return f.getValue, f.getErr
}

func (f *fakeStore) ResolveConflict(_ context.Context, identity gateway.WriteIdentity, params ResolveParams) (domain.ConflictCase, string, bool, error) {
	f.resolveID, f.resolveP = identity, params
	return f.resolveVal, f.resolveOp, f.resolveRep, f.resolveErr
}

func resolveCall(key string) gateway.CallContext {
	call := gateway.CallContext{RequestID: "request-1", Caller: gateway.CallerIdentity{SubjectID: "operator"}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func targets() []domain.EvolutionTarget {
	return []domain.EvolutionTarget{
		{MemoryID: "memory-1", ExpectedRevisionID: "revision-1"},
		{MemoryID: "memory-2", ExpectedRevisionID: "revision-2"},
	}
}

func keepBothCommand() ResolveCommand {
	return ResolveCommand{
		CaseID:          "case-1",
		ResolutionType:  ResolutionKeepBoth,
		ExpectedTargets: targets(),
		ResultStates:    map[string]string{"memory-1": "active", "memory-2": "active"},
		Reason:          "both remain valid",
	}
}

func TestListValidatesQuery(t *testing.T) {
	service := NewService(&fakeStore{listPage: domain.ConflictCasePage{Items: []domain.ConflictCase{}}})
	if _, err := service.List(context.Background(), resolveCall(""), ListQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing scope_id error = %v", err)
	}
	if _, err := service.List(context.Background(), resolveCall(""), ListQuery{SpaceID: "s", State: "unsupported"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("invalid state error = %v", err)
	}
	if _, err := service.List(context.Background(), resolveCall(""), ListQuery{SpaceID: "s", Page: gateway.PageRequest{Limit: 500}}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("invalid page error = %v", err)
	}
}

func TestListAppliesFilterAndNormalizesEmpty(t *testing.T) {
	store := &fakeStore{listPage: domain.ConflictCasePage{}}
	service := NewService(store)
	page, err := service.List(context.Background(), resolveCall(""), ListQuery{SpaceID: "scope-1", State: "open", Page: gateway.PageRequest{Limit: 10, Cursor: "cursor-1"}})
	if err != nil || page.Items == nil {
		t.Fatalf("List() = %+v error = %v", page, err)
	}
	if store.filter.SpaceID != "scope-1" || store.filter.State != "open" || store.filter.Limit != 10 || store.filter.Cursor != "cursor-1" {
		t.Fatalf("filter = %+v", store.filter)
	}
}

func TestGetValidatesIdentifier(t *testing.T) {
	service := NewService(&fakeStore{getValue: domain.ConflictCase{CaseID: "case-1"}})
	if _, err := service.Get(context.Background(), resolveCall(""), GetQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing case_id error = %v", err)
	}
	if _, err := service.Get(context.Background(), resolveCall(""), GetQuery{CaseID: "case-1"}); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestResolveValidatesCommand(t *testing.T) {
	service := NewService(&fakeStore{})
	if err := validateResolve(keepBothCommand()); err != nil {
		t.Fatalf("valid command error = %v", err)
	}
	destructive := ResolveCommand{
		CaseID: "case-1", ResolutionType: ResolutionReconcile, ExpectedTargets: targets(),
		ProposedContent: strptr("reconciled"), Evidence: []Evidence{{EventID: "event-1"}},
		ResultStates: map[string]string{"memory-1": "superseded", "memory-2": "superseded"}, Reason: "reconcile",
	}
	if err := validateResolve(destructive); err != nil {
		t.Fatalf("valid reconcile error = %v", err)
	}
	cases := map[string]ResolveCommand{
		"missing case":         {ResolutionType: ResolutionKeepBoth, ExpectedTargets: targets(), ResultStates: map[string]string{"memory-1": "active"}, Reason: "r"},
		"unsupported type":     {CaseID: "case-1", ResolutionType: "bogus", ExpectedTargets: targets(), ResultStates: map[string]string{"memory-1": "active"}, Reason: "r"},
		"missing reason":       {CaseID: "case-1", ResolutionType: ResolutionKeepBoth, ExpectedTargets: targets(), ResultStates: map[string]string{"memory-1": "active"}},
		"missing targets":      {CaseID: "case-1", ResolutionType: ResolutionKeepBoth, ResultStates: map[string]string{"memory-1": "active"}, Reason: "r"},
		"duplicate target":     {CaseID: "case-1", ResolutionType: ResolutionKeepBoth, ExpectedTargets: []domain.EvolutionTarget{{MemoryID: "m", ExpectedRevisionID: "r"}, {MemoryID: "m", ExpectedRevisionID: "r2"}}, ResultStates: map[string]string{"m": "active"}, Reason: "r"},
		"keep_both inputs":     {CaseID: "case-1", ResolutionType: ResolutionKeepBoth, ExpectedTargets: targets(), ProposedContent: strptr("x"), ResultStates: map[string]string{"memory-1": "active"}, Reason: "r"},
		"destructive no body":  {CaseID: "case-1", ResolutionType: ResolutionReconcile, ExpectedTargets: targets(), ResultStates: map[string]string{"memory-1": "superseded"}, Reason: "r"},
		"reconcile selects":    {CaseID: "case-1", ResolutionType: ResolutionReconcile, ExpectedTargets: targets(), SelectedTarget: &domain.EvolutionTarget{MemoryID: "memory-1", ExpectedRevisionID: "revision-1"}, ProposedContent: strptr("x"), Evidence: []Evidence{{EventID: "event-1"}}, ResultStates: map[string]string{"memory-1": "superseded"}, Reason: "r"},
		"supersede no mode":    {CaseID: "case-1", ResolutionType: ResolutionSupersede, ExpectedTargets: targets(), SelectedTarget: &domain.EvolutionTarget{MemoryID: "memory-1", ExpectedRevisionID: "revision-1"}, ProposedContent: strptr("x"), Evidence: []Evidence{{EventID: "event-1"}}, ResultStates: map[string]string{"memory-1": "superseded"}, Reason: "r"},
		"missing result state": {CaseID: "case-1", ResolutionType: ResolutionKeepBoth, ExpectedTargets: targets(), Reason: "r"},
		"bad result state":     {CaseID: "case-1", ResolutionType: ResolutionKeepBoth, ExpectedTargets: targets(), ResultStates: map[string]string{"memory-1": "deleted"}, Reason: "r"},
		"bad evidence":         {CaseID: "case-1", ResolutionType: ResolutionKeepBoth, ExpectedTargets: targets(), Evidence: []Evidence{{EventID: "event-1", StartOffset: int64ptr(1)}}, ResultStates: map[string]string{"memory-1": "active"}, Reason: "r"},
	}
	for name, command := range cases {
		if _, err := service.Resolve(context.Background(), resolveCall("key-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
			t.Errorf("%s error = %v", name, err)
		}
	}
}

func TestResolveRequiresIdempotencyAndCaller(t *testing.T) {
	service := NewService(&fakeStore{})
	if _, err := service.Resolve(context.Background(), resolveCall(""), keepBothCommand()); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing idempotency error = %v", err)
	}
	noCaller := gateway.CallContext{Idempotency: &gateway.Idempotency{Key: "key-1"}}
	if _, err := service.Resolve(context.Background(), noCaller, keepBothCommand()); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing caller error = %v", err)
	}
}

func TestResolveMapsIdentityAndResult(t *testing.T) {
	store := &fakeStore{resolveVal: domain.ConflictCase{CaseID: "case-1", State: "resolved"}, resolveOp: "op-1", resolveRep: true}
	service := NewService(store)
	result, err := service.Resolve(context.Background(), resolveCall("key-1"), keepBothCommand())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if result.OperationID != "op-1" || !result.Replay || result.Case.State != "resolved" {
		t.Fatalf("result = %+v", result)
	}
	if store.resolveID.Operation != OperationResolve || store.resolveID.IdempotencyKey != "key-1" {
		t.Fatalf("identity = %+v", store.resolveID)
	}
	if store.resolveP.ResolutionType != "keep_both" || len(store.resolveP.ExpectedTargets) != 2 || store.resolveP.Reason != "both remain valid" {
		t.Fatalf("params = %+v", store.resolveP)
	}
	if store.resolveP.Evidence == nil {
		t.Fatalf("evidence should be a non-nil empty list, got %#v", store.resolveP.Evidence)
	}
}

func TestResolveNormalizesStoreErrors(t *testing.T) {
	service := NewService(&fakeStore{resolveErr: errors.New("boom")})
	if _, err := service.Resolve(context.Background(), resolveCall("key-1"), keepBothCommand()); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("store error = %v", err)
	}
}

func strptr(value string) *string { return &value }

func int64ptr(value int64) *int64 { return &value }
