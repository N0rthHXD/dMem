package gateway

import "errors"

// ErrMissingCaller is returned when an operation requires a caller identity but
// the supplied CallContext does not carry one.
var ErrMissingCaller = errors.New("gateway: caller identity is required")

// ErrMissingIdempotency is returned when a state-changing operation requires an
// idempotency key but the supplied CallContext does not carry one.
var ErrMissingIdempotency = errors.New("gateway: idempotency key is required")

// CallContext is the protocol-neutral context of one inbound gateway call. A
// protocol adapter builds it from wire metadata; gateway modules treat it as
// transport-independent identity and cancellation information.
type CallContext struct {
	RequestID     string
	CorrelationID string
	Caller        CallerIdentity
	Idempotency   *Idempotency
}

// CallerIdentity identifies the agent or client that originated a call. Subject
// and client are kept separate so future authorization can distinguish an end
// subject from the client acting on its behalf.
type CallerIdentity struct {
	SubjectID string
	ClientID  string
}

// CallerID returns the single stable caller identifier used for audit records.
// Subject identity takes precedence; the client identity is the fallback.
func (c CallerIdentity) CallerID() string {
	if c.SubjectID != "" {
		return c.SubjectID
	}
	return c.ClientID
}

// Idempotency carries the caller-supplied key that makes a state-changing
// operation replay-safe. It is nil for operations that are not idempotent.
type Idempotency struct {
	Key string
}

// HasCaller reports whether either caller identifier is present.
func (c CallContext) HasCaller() bool {
	return c.Caller.SubjectID != "" || c.Caller.ClientID != ""
}

// CallerID returns the single stable caller identifier used for audit records.
// Subject identity takes precedence; the client identity is the fallback.
func (c CallContext) CallerID() string {
	return c.Caller.CallerID()
}

// IdempotencyKey returns the idempotency key, or an empty string when absent.
func (c CallContext) IdempotencyKey() string {
	if c.Idempotency == nil {
		return ""
	}
	return c.Idempotency.Key
}
