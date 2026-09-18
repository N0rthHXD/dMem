package gateway

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestErrorCodeInspectionHelpers(t *testing.T) {
	base := NewError(ErrorNotFound, "missing")
	if CodeOf(base) != ErrorNotFound || !IsCode(base, ErrorNotFound) || IsCode(base, ErrorConflict) {
		t.Fatalf("unexpected code inspection for %v", base)
	}
	if CodeOf(errors.New("plain")) != ErrorInternal {
		t.Fatal("non-gateway errors must classify as internal")
	}
	wrapped := fmt.Errorf("context: %w", base)
	if !IsCode(wrapped, ErrorNotFound) {
		t.Fatal("errors.Is-style unwrapping must find the gateway error")
	}
}

func TestErrorRetryabilityMetadata(t *testing.T) {
	if IsRetryable(NewError(ErrorInvalidInput, "bad")) {
		t.Fatal("invalid input must not be retryable by default")
	}
	if !IsRetryable(NewError(ErrorResourceExhausted, "full")) {
		t.Fatal("resource exhaustion must be retryable by default")
	}
	if !IsRetryable(NewError(ErrorCapabilityUnavailable, "down")) {
		t.Fatal("capability unavailability must be retryable by default")
	}
	if NewError(ErrorConflict, "conflict").WithRetryable(true).Retryable != true {
		t.Fatal("WithRetryable must override the default")
	}
}

func TestErrorRetryAfterAndDetails(t *testing.T) {
	base := NewError(ErrorResourceExhausted, "full")
	if _, ok := RetryAfter(base); ok {
		t.Fatal("no retry-after must be reported by default")
	}
	delayed := base.WithRetryAfter(3 * time.Second)
	delay, ok := RetryAfter(delayed)
	if !ok || delay != 3*time.Second {
		t.Fatalf("retry-after = %v, %v", delay, ok)
	}
	detailed := base.WithDetails(map[string]string{"resource": "disk"})
	details := DetailsOf(detailed)
	if details["resource"] != "disk" {
		t.Fatalf("details not preserved: %v", details)
	}
	details["resource"] = "mutated"
	if DetailsOf(detailed)["resource"] != "disk" {
		t.Fatal("DetailsOf must return a copy")
	}
	if DetailsOf(base) != nil {
		t.Fatal("absent details must return nil")
	}
}

func TestErrorWrappingKeepsCauseInternal(t *testing.T) {
	cause := errors.New("sqlite failure")
	base := WrapError(ErrorInternal, "storage operation failed", cause)
	if !errors.Is(base, cause) {
		t.Fatal("wrapped cause must remain inspectable with errors.Is")
	}
	if base.Message != "storage operation failed" {
		t.Fatalf("public message changed: %q", base.Message)
	}
	if base.Error() == base.Message {
		t.Fatal("Error() must include the internal cause for logs")
	}
}

func TestErrorWithMethodsDoNotMutateOriginal(t *testing.T) {
	base := NewError(ErrorConflict, "conflict")
	_ = base.WithRetryable(true)
	_ = base.WithDetails(map[string]string{"a": "b"})
	if base.Retryable || base.Details != nil {
		t.Fatal("With* methods must not mutate the receiver")
	}
}

func TestErrorCodeValid(t *testing.T) {
	for _, code := range []ErrorCode{
		ErrorInvalidInput, ErrorNotFound, ErrorConflict, ErrorIdempotencyConflict,
		ErrorPermissionDenied, ErrorResourceExhausted, ErrorCapabilityUnavailable, ErrorInternal,
	} {
		if !code.Valid() {
			t.Fatalf("code %q must be valid", code)
		}
	}
	if ErrorCode("other").Valid() {
		t.Fatal("unknown code must be invalid")
	}
}
