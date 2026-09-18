package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeAuthority struct {
	rememberRecord RememberRecord
	rememberReplay bool
	rememberErr    error
	rememberParams RememberParams
	rememberID     gateway.WriteIdentity
	rememberCalls  int
	getMemory      domain.Memory
	getErr         error
	forgetMemory   domain.Memory
	forgetReplay   bool
	forgetErr      error
	forgetID       gateway.WriteIdentity
	forgetMemoryID string
	forgetReason   string
	respectCancel  bool
}

func (f *fakeAuthority) Remember(ctx context.Context, identity gateway.WriteIdentity, params RememberParams) (RememberRecord, bool, error) {
	f.rememberCalls++
	f.rememberID = identity
	f.rememberParams = params
	if f.respectCancel {
		if err := ctx.Err(); err != nil {
			return RememberRecord{}, false, err
		}
	}
	return f.rememberRecord, f.rememberReplay, f.rememberErr
}

func (f *fakeAuthority) GetMemory(context.Context, string) (domain.Memory, error) {
	return f.getMemory, f.getErr
}

func (f *fakeAuthority) ForgetMemory(_ context.Context, identity gateway.WriteIdentity, memoryID, reason string) (domain.Memory, bool, error) {
	f.forgetID = identity
	f.forgetMemoryID = memoryID
	f.forgetReason = reason
	return f.forgetMemory, f.forgetReplay, f.forgetErr
}

func testCall(caller, key, requestID string) gateway.CallContext {
	call := gateway.CallContext{RequestID: requestID, Caller: gateway.CallerIdentity{SubjectID: caller}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func TestRememberAppliesProtocolNeutralDefaults(t *testing.T) {
	authority := &fakeAuthority{rememberRecord: RememberRecord{Memory: domain.Memory{MemoryID: "memory-1"}, EventID: "event-1"}}
	service := NewService(authority)
	service.now = func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) }

	command := RememberCommand{SpaceID: "space-1", Content: "content"}
	result, err := service.Remember(context.Background(), testCall("caller-1", "key-1", "request-1"), command)
	if err != nil {
		t.Fatalf("Remember() error = %v", err)
	}
	if result.Memory.MemoryID != "memory-1" || result.EventID != "event-1" || result.Replay {
		t.Fatalf("result = %+v", result)
	}
	params := authority.rememberParams
	if params.Kind != DefaultKind || params.Sensitivity != DefaultSensitivity || params.SourceName != "caller-1" || params.Reason != DefaultReason {
		t.Fatalf("defaults = %+v", params)
	}
	if params.OccurredAt != "2026-08-21T12:00:00Z" || params.Metadata == nil {
		t.Fatalf("time/metadata defaults = %+v", params)
	}
	if authority.rememberID.Operation != OperationRemember || authority.rememberID.IdempotencyKey != "key-1" {
		t.Fatalf("write identity = %+v", authority.rememberID)
	}
	wantHash, _ := gateway.CanonicalHash(command)
	if authority.rememberID.CommandHash != wantHash {
		t.Fatalf("command hash = %q, want raw-command hash %q", authority.rememberID.CommandHash, wantHash)
	}
	if result.OperationID != "request-1" {
		t.Fatalf("operation id = %q", result.OperationID)
	}
}

func TestRememberRejectsInvalidInputBeforePersistence(t *testing.T) {
	authority := &fakeAuthority{}
	service := NewService(authority)
	cases := map[string]RememberCommand{
		"missing space":   {Content: "content"},
		"empty content":   {SpaceID: "space-1", Content: "   "},
		"bad kind":        {SpaceID: "space-1", Content: "content", Kind: "aggregate"},
		"bad sensitivity": {SpaceID: "space-1", Content: "content", Sensitivity: "secret"},
	}
	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Remember(context.Background(), testCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("error = %v, want invalid_input", err)
			}
		})
	}
	if authority.rememberCalls != 0 {
		t.Fatalf("authority called %d times for invalid input", authority.rememberCalls)
	}
}

func TestRememberRequiresCallerAndIdempotency(t *testing.T) {
	service := NewService(&fakeAuthority{})
	valid := RememberCommand{SpaceID: "space-1", Content: "content", Kind: "fact", Sensitivity: "normal", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z", Reason: "reason"}

	if _, err := service.Remember(context.Background(), testCall("", "key-1", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing caller error = %v", err)
	}
	if _, err := service.Remember(context.Background(), testCall("caller-1", "", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing idempotency error = %v", err)
	}
}

func TestRememberPropagatesCancellation(t *testing.T) {
	authority := &fakeAuthority{respectCancel: true}
	service := NewService(authority)
	valid := RememberCommand{SpaceID: "space-1", Content: "content", Kind: "fact", Sensitivity: "normal", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z", Reason: "reason"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.Remember(ctx, testCall("caller-1", "key-1", "request-1"), valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRememberClassifiesPortErrors(t *testing.T) {
	authority := &fakeAuthority{rememberErr: gateway.NewError(gateway.ErrorNotFound, "missing scope")}
	service := NewService(authority)
	valid := RememberCommand{SpaceID: "space-1", Content: "content", Kind: "fact", Sensitivity: "normal", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z", Reason: "reason"}
	if _, err := service.Remember(context.Background(), testCall("caller-1", "key-1", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("gateway error passthrough = %v", err)
	}

	authority.rememberErr = errors.New("disk on fire")
	if _, err := service.Remember(context.Background(), testCall("caller-1", "key-1", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("unknown error classification = %v", err)
	}
}

func TestForgetValidatesAndPropagatesIdentity(t *testing.T) {
	authority := &fakeAuthority{forgetMemory: domain.Memory{MemoryID: "memory-1", CurrentState: "deleted"}}
	service := NewService(authority)

	if _, err := service.Forget(context.Background(), testCall("caller-1", "key-1", "request-1"), ForgetCommand{MemoryID: "memory-1"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing reason error = %v", err)
	}

	result, err := service.Forget(context.Background(), testCall("caller-1", "key-1", "request-1"), ForgetCommand{MemoryID: "memory-1", Reason: "user request"})
	if err != nil || result.Memory.CurrentState != "deleted" || result.Replay {
		t.Fatalf("Forget() = %+v error = %v", result, err)
	}
	if authority.forgetID.Operation != OperationForget || authority.forgetMemoryID != "memory-1" || authority.forgetReason != "user request" {
		t.Fatalf("forget identity = %+v", authority.forgetID)
	}
}

func TestGetValidatesAndReturnsMemory(t *testing.T) {
	service := NewService(&fakeAuthority{getMemory: domain.Memory{MemoryID: "memory-1"}})
	if _, err := service.Get(context.Background(), testCall("caller-1", "", "request-1"), GetQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing id error = %v", err)
	}
	memory, err := service.Get(context.Background(), testCall("caller-1", "", "request-1"), GetQuery{MemoryID: "memory-1"})
	if err != nil || memory.MemoryID != "memory-1" {
		t.Fatalf("Get() = %+v error = %v", memory, err)
	}
}
