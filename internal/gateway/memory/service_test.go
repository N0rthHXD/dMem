package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeStore struct {
	filter      ListFilter
	listPage    domain.MemoryPage
	listErr     error
	getValue    domain.Memory
	getErr      error
	getDeleted  bool
	revisions   []domain.MemoryRevision
	revisionErr error
	events      []domain.MemoryStateEvent
	eventErr    error
	reviseValue domain.Memory
	reviseOp    string
	reviseRep   bool
	reviseErr   error
	reviseID    gateway.WriteIdentity
	reviseP     ReviseParams
	stateValue  domain.Memory
	stateOp     string
	stateRep    bool
	stateErr    error
	stateID     gateway.WriteIdentity
	stateP      StateChangeParams
}

func (f *fakeStore) ListMemories(_ context.Context, filter ListFilter) (domain.MemoryPage, error) {
	f.filter = filter
	return f.listPage, f.listErr
}

func (f *fakeStore) GetMemory(_ context.Context, _ string, includeDeleted bool) (domain.Memory, error) {
	f.getDeleted = includeDeleted
	return f.getValue, f.getErr
}

func (f *fakeStore) ListMemoryRevisions(context.Context, string) ([]domain.MemoryRevision, error) {
	return f.revisions, f.revisionErr
}

func (f *fakeStore) ListMemoryStateEvents(context.Context, string) ([]domain.MemoryStateEvent, error) {
	return f.events, f.eventErr
}

func (f *fakeStore) ReviseMemory(_ context.Context, identity gateway.WriteIdentity, params ReviseParams) (domain.Memory, string, bool, error) {
	f.reviseID, f.reviseP = identity, params
	return f.reviseValue, f.reviseOp, f.reviseRep, f.reviseErr
}

func (f *fakeStore) ChangeMemoryState(_ context.Context, identity gateway.WriteIdentity, params StateChangeParams) (domain.Memory, string, bool, error) {
	f.stateID, f.stateP = identity, params
	return f.stateValue, f.stateOp, f.stateRep, f.stateErr
}

func writeCall(key string) gateway.CallContext {
	call := gateway.CallContext{RequestID: "request-1", Caller: gateway.CallerIdentity{SubjectID: "operator"}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func TestListValidatesQuery(t *testing.T) {
	service := NewService(&fakeStore{listPage: domain.MemoryPage{Items: []domain.Memory{}}})
	if _, err := service.List(context.Background(), writeCall(""), ListQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing scope_id error = %v", err)
	}
	if _, err := service.List(context.Background(), writeCall(""), ListQuery{SpaceID: "s", State: "bogus"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("invalid state error = %v", err)
	}
	if _, err := service.List(context.Background(), writeCall(""), ListQuery{SpaceID: "s", Page: gateway.PageRequest{Limit: 500}}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("invalid page error = %v", err)
	}
}

func TestListAppliesPageAndFilter(t *testing.T) {
	store := &fakeStore{listPage: domain.MemoryPage{Items: []domain.Memory{{MemoryID: "memory-1"}}}}
	service := NewService(store)
	page, err := service.List(context.Background(), writeCall(""), ListQuery{SpaceID: "scope-1", State: "archived", IncludeAncestors: true, Page: gateway.PageRequest{Limit: 10, Cursor: "cursor-1"}})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("List() = %+v error = %v", page, err)
	}
	if store.filter.SpaceID != "scope-1" || store.filter.State != "archived" || !store.filter.IncludeAncestors || store.filter.Limit != 10 || store.filter.Cursor != "cursor-1" {
		t.Fatalf("filter = %+v", store.filter)
	}
}

func TestListAppliesDefaultPageLimit(t *testing.T) {
	store := &fakeStore{listPage: domain.MemoryPage{}}
	service := NewService(store)
	if _, err := service.List(context.Background(), writeCall(""), ListQuery{SpaceID: "scope-1"}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.filter.Limit != gateway.DefaultPageLimit {
		t.Fatalf("limit = %d", store.filter.Limit)
	}
}

func TestGetIncludesDeletedDetail(t *testing.T) {
	store := &fakeStore{getValue: domain.Memory{MemoryID: "memory-1", CurrentState: "deleted"}}
	service := NewService(store)
	value, err := service.Get(context.Background(), writeCall(""), GetQuery{MemoryID: "memory-1"})
	if err != nil || value.CurrentState != "deleted" {
		t.Fatalf("Get() = %+v error = %v", value, err)
	}
	if !store.getDeleted {
		t.Fatal("management detail must resolve logically deleted memories")
	}
	if _, err := service.Get(context.Background(), writeCall(""), GetQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing memory_id error = %v", err)
	}
}

func TestListRevisionsAndStateEventsNormalizeEmpty(t *testing.T) {
	service := NewService(&fakeStore{})
	revisions, err := service.ListRevisions(context.Background(), writeCall(""), MemoryQuery{MemoryID: "memory-1"})
	if err != nil || revisions == nil || len(revisions) != 0 {
		t.Fatalf("ListRevisions() = %+v error = %v", revisions, err)
	}
	events, err := service.ListStateEvents(context.Background(), writeCall(""), MemoryQuery{MemoryID: "memory-1"})
	if err != nil || events == nil || len(events) != 0 {
		t.Fatalf("ListStateEvents() = %+v error = %v", events, err)
	}
	if _, err := service.ListRevisions(context.Background(), writeCall(""), MemoryQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing memory_id error = %v", err)
	}
}

func TestReviseValidatesCommand(t *testing.T) {
	service := NewService(&fakeStore{})
	valid := ReviseCommand{MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "new content", Reason: "fix typo"}
	cases := map[string]ReviseCommand{
		"missing memory":   {ExpectedRevisionID: "revision-1", Content: "c", Reason: "r"},
		"missing revision": {MemoryID: "memory-1", Content: "c", Reason: "r"},
		"empty content":    {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Reason: "r"},
		"empty reason":     {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "c"},
		"bad window":       {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "c", Reason: "r", ValidFrom: strptr("2026-01-02T00:00:00Z"), ValidUntil: strptr("2026-01-01T00:00:00Z")},
		"bad timestamp":    {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "c", Reason: "r", ValidFrom: strptr("yesterday")},
		"empty evidence":   {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "c", Reason: "r", Evidence: []Evidence{}},
		"bad role":         {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "c", Reason: "r", Evidence: []Evidence{{EventID: "event-1", Role: "bogus"}}},
		"half span":        {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "c", Reason: "r", Evidence: []Evidence{{EventID: "event-1", StartOffset: int64ptr(1)}}},
	}
	if err := validateRevise(valid); err != nil {
		t.Fatalf("valid command error = %v", err)
	}
	for name, command := range cases {
		if _, err := service.Revise(context.Background(), writeCall("key-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
			t.Errorf("%s error = %v", name, err)
		}
	}
}

func TestReviseRequiresIdempotencyAndCaller(t *testing.T) {
	service := NewService(&fakeStore{})
	command := ReviseCommand{MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "new content", Reason: "fix typo"}
	if _, err := service.Revise(context.Background(), writeCall(""), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing idempotency error = %v", err)
	}
	noCaller := gateway.CallContext{Idempotency: &gateway.Idempotency{Key: "key-1"}}
	if _, err := service.Revise(context.Background(), noCaller, command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing caller error = %v", err)
	}
}

func TestReviseInheritsCurrentEvidenceWhenOmitted(t *testing.T) {
	store := &fakeStore{
		getValue:    domain.Memory{CurrentRevision: domain.MemoryRevision{Evidence: []domain.RevisionEvidence{{EvidenceID: "evidence-1", EventID: "event-1", Role: domain.EvidenceRoleDirect}}}},
		reviseValue: domain.Memory{MemoryID: "memory-1"},
		reviseOp:    "op-1",
		reviseRep:   true,
	}
	service := NewService(store)
	result, err := service.Revise(context.Background(), writeCall("key-1"), ReviseCommand{MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "new content", Reason: "fix typo"})
	if err != nil {
		t.Fatalf("Revise() error = %v", err)
	}
	if result.OperationID != "op-1" || !result.Replay || result.Memory.MemoryID != "memory-1" {
		t.Fatalf("result = %+v", result)
	}
	if len(store.reviseP.Evidence) != 1 || store.reviseP.Evidence[0].EventID != "event-1" || store.reviseP.Evidence[0].Role != domain.EvidenceRoleDirect {
		t.Fatalf("inherited evidence = %+v", store.reviseP.Evidence)
	}
	if store.reviseID.Operation != OperationRevise || store.reviseID.IdempotencyKey != "key-1" {
		t.Fatalf("identity = %+v", store.reviseID)
	}
	if store.getDeleted {
		t.Fatal("evidence inheritance must not resolve deleted memories")
	}
}

func TestReviseUsesExplicitEvidence(t *testing.T) {
	store := &fakeStore{reviseValue: domain.Memory{MemoryID: "memory-1"}, reviseOp: "op-1"}
	service := NewService(store)
	command := ReviseCommand{MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "new content", Reason: "fix typo",
		Evidence: []Evidence{{EventID: "event-2", Role: domain.EvidenceRoleDerived}}}
	if _, err := service.Revise(context.Background(), writeCall("key-1"), command); err != nil {
		t.Fatalf("Revise() error = %v", err)
	}
	if len(store.reviseP.Evidence) != 1 || store.reviseP.Evidence[0].EventID != "event-2" {
		t.Fatalf("evidence = %+v", store.reviseP.Evidence)
	}
}

func TestReviseNormalizesStoreErrors(t *testing.T) {
	service := NewService(&fakeStore{getValue: domain.Memory{}, getErr: errors.New("boom")})
	if _, err := service.Revise(context.Background(), writeCall("key-1"), ReviseCommand{MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Content: "c", Reason: "r"}); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("store error = %v", err)
	}
}

func TestChangeStateValidatesCommand(t *testing.T) {
	service := NewService(&fakeStore{})
	if err := validateStateChange(StateChangeCommand{MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Action: StateArchive, Reason: "cleanup"}); err != nil {
		t.Fatalf("valid command error = %v", err)
	}
	cases := map[string]StateChangeCommand{
		"missing memory":      {ExpectedRevisionID: "revision-1", Action: StateArchive, Reason: "r"},
		"missing revision":    {MemoryID: "memory-1", Action: StateArchive, Reason: "r"},
		"bad action":          {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Action: "destroy", Reason: "r"},
		"missing reason":      {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Action: StateArchive},
		"restore no revision": {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Action: StateRestore, Reason: "r"},
		"restore mismatch":    {MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Action: StateArchive, RestoreRevisionID: strptr("revision-0"), Reason: "r"},
	}
	for name, command := range cases {
		if _, err := service.ChangeState(context.Background(), writeCall("key-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
			t.Errorf("%s error = %v", name, err)
		}
	}
}

func TestChangeStateMapsIdentityAndResult(t *testing.T) {
	store := &fakeStore{stateValue: domain.Memory{MemoryID: "memory-1", CurrentState: "archived"}, stateOp: "op-2"}
	service := NewService(store)
	result, err := service.ChangeState(context.Background(), writeCall("key-2"), StateChangeCommand{MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Action: StateArchive, Reason: "cleanup"})
	if err != nil {
		t.Fatalf("ChangeState() error = %v", err)
	}
	if result.OperationID != "op-2" || result.Replay || result.Memory.CurrentState != "archived" {
		t.Fatalf("result = %+v", result)
	}
	if store.stateID.Operation != OperationStateChange || store.stateID.IdempotencyKey != "key-2" {
		t.Fatalf("identity = %+v", store.stateID)
	}
	if store.stateP.Action != "archive" || store.stateP.RestoreRevisionID != nil {
		t.Fatalf("params = %+v", store.stateP)
	}
}

func TestChangeStateRestorePassesRevision(t *testing.T) {
	store := &fakeStore{stateValue: domain.Memory{MemoryID: "memory-1"}}
	service := NewService(store)
	if _, err := service.ChangeState(context.Background(), writeCall("key-3"), StateChangeCommand{MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Action: StateRestore, RestoreRevisionID: strptr("revision-0"), Reason: "recover"}); err != nil {
		t.Fatalf("ChangeState() error = %v", err)
	}
	if store.stateP.Action != "restore" || store.stateP.RestoreRevisionID == nil || *store.stateP.RestoreRevisionID != "revision-0" {
		t.Fatalf("params = %+v", store.stateP)
	}
}

func strptr(value string) *string { return &value }

func int64ptr(value int64) *int64 { return &value }
