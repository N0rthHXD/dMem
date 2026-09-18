package gateway

import "testing"

func TestCallContextCallerResolution(t *testing.T) {
	if (CallContext{}).HasCaller() {
		t.Fatal("empty call context must not report a caller")
	}
	subject := CallContext{Caller: CallerIdentity{SubjectID: "subject"}}
	if !subject.HasCaller() || subject.CallerID() != "subject" {
		t.Fatalf("subject caller resolved incorrectly: %+v", subject)
	}
	client := CallContext{Caller: CallerIdentity{ClientID: "client"}}
	if !client.HasCaller() || client.CallerID() != "client" {
		t.Fatalf("client caller resolved incorrectly: %+v", client)
	}
	both := CallContext{Caller: CallerIdentity{SubjectID: "subject", ClientID: "client"}}
	if both.CallerID() != "subject" {
		t.Fatalf("subject must take precedence over client, got %q", both.CallerID())
	}
}

func TestCallContextIdempotencyKey(t *testing.T) {
	if (CallContext{}).IdempotencyKey() != "" {
		t.Fatal("absent idempotency must resolve to empty key")
	}
	call := CallContext{Idempotency: &Idempotency{Key: "key-1"}}
	if call.IdempotencyKey() != "key-1" {
		t.Fatalf("idempotency key = %q", call.IdempotencyKey())
	}
}
