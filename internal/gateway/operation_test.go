package gateway

import (
	"errors"
	"testing"
)

func TestNewWriteIdentityValidatesRequiredFields(t *testing.T) {
	call := CallContext{Caller: CallerIdentity{SubjectID: "caller"}, Idempotency: &Idempotency{Key: "key"}}
	identity, err := NewWriteIdentity(call, OperationName("memory.remember"), hashCommand{Content: "text"})
	if err != nil {
		t.Fatalf("valid identity: %v", err)
	}
	if identity.Caller.SubjectID != "caller" || identity.IdempotencyKey != "key" || identity.Operation != "memory.remember" {
		t.Fatalf("identity fields not preserved: %+v", identity)
	}
	if identity.CommandHash == "" {
		t.Fatal("command hash must be populated")
	}
}

func TestNewWriteIdentityRejectsMissingCallerAndKey(t *testing.T) {
	if _, err := NewWriteIdentity(CallContext{}, OperationName("op"), struct{}{}); !errors.Is(err, ErrMissingCaller) {
		t.Fatalf("expected ErrMissingCaller, got %v", err)
	}
	noKey := CallContext{Caller: CallerIdentity{SubjectID: "caller"}}
	if _, err := NewWriteIdentity(noKey, OperationName("op"), struct{}{}); !errors.Is(err, ErrMissingIdempotency) {
		t.Fatalf("expected ErrMissingIdempotency, got %v", err)
	}
	call := CallContext{Caller: CallerIdentity{SubjectID: "caller"}, Idempotency: &Idempotency{Key: "key"}}
	if _, err := NewWriteIdentity(call, "", struct{}{}); err == nil {
		t.Fatal("expected an error for an empty operation name")
	}
}

func TestWriteIdentityHashExcludesCallContext(t *testing.T) {
	command := hashCommand{Content: "text"}
	first, err := NewWriteIdentity(CallContext{Caller: CallerIdentity{SubjectID: "caller"}, Idempotency: &Idempotency{Key: "key-1"}}, "op", command)
	if err != nil {
		t.Fatalf("first identity: %v", err)
	}
	second, err := NewWriteIdentity(CallContext{RequestID: "other", CorrelationID: "trace", Caller: CallerIdentity{SubjectID: "caller"}, Idempotency: &Idempotency{Key: "key-2"}}, "op", command)
	if err != nil {
		t.Fatalf("second identity: %v", err)
	}
	if first.CommandHash != second.CommandHash {
		t.Fatal("transport metadata must not change the canonical command hash")
	}
}
