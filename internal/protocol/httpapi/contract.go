package httpapi

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/feedback"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	"github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
)

// Operation names published by this adapter for routes that read state. Write
// routes use the owning gateway module's operation name so idempotency identity
// stays identical to the module contract. Read operations are not idempotent
// writes and their names are adapter-declared identifiers.
const (
	OperationMemoryGet gateway.OperationName = "memory.get"
	OperationEventGet  gateway.OperationName = "extraction.event.get"
	OperationScopeGet  gateway.OperationName = "scope.get"
)

// AccessMode classifies a route as a read or a state-changing write.
type AccessMode string

const (
	// AccessRead is a route that does not change authoritative state.
	AccessRead AccessMode = "read"
	// AccessWrite is a route that may change authoritative state.
	AccessWrite AccessMode = "write"
)

// IdempotencyPolicy declares whether a route requires an Idempotency-Key.
type IdempotencyPolicy string

const (
	// IdempotencyNotRequired is used by read routes.
	IdempotencyNotRequired IdempotencyPolicy = "not_required"
	// IdempotencyRequired is used by every write route.
	IdempotencyRequired IdempotencyPolicy = "required"
)

// Route body limits, in bytes. Read routes declare no body. The values are part
// of the frozen contract in docs/api/http-rest-contract.md.
const (
	MaxScopeBodyBytes    int64 = 64 << 10
	MaxMemoryBodyBytes   int64 = 1 << 20
	MaxForgetBodyBytes   int64 = 64 << 10
	MaxFeedbackBodyBytes int64 = 256 << 10
	MaxRecallBodyBytes   int64 = 256 << 10
	MaxEventBodyBytes    int64 = 4 << 20
)

// MemoryService is the consumer-owned view of the Memory gateway module. The
// adapter declares only the operations it uses. A concrete Memory service
// satisfies it structurally.
type MemoryService interface {
	Remember(context.Context, gateway.CallContext, memory.RememberCommand) (memory.RememberResult, error)
	Get(context.Context, gateway.CallContext, memory.GetQuery) (domain.Memory, error)
	Forget(context.Context, gateway.CallContext, memory.ForgetCommand) (memory.ForgetResult, error)
}

// RecallService is the consumer-owned view of the Retrieval gateway module.
type RecallService interface {
	Recall(context.Context, gateway.CallContext, retrieval.Query) (retrieval.Result, error)
}

// FeedbackService is the consumer-owned view of the Feedback gateway module.
// Aggregate inspection is not exposed by the initial Agent surface.
type FeedbackService interface {
	Append(context.Context, gateway.CallContext, feedback.AppendCommand) (feedback.AppendResult, error)
}

// EventService is the consumer-owned view of the Extraction ingress module.
// Worker-facing prepare, commit, block, and fail ports are never reachable here.
type EventService interface {
	Ingest(context.Context, gateway.CallContext, extraction.IngestCommand) (extraction.IngestResult, error)
	GetEvent(context.Context, gateway.CallContext, extraction.GetEventQuery) (domain.RawEvent, error)
}

// ScopeService is the consumer-owned view of the Scope gateway module. Resolve
// and List are not exposed by the initial Agent surface.
type ScopeService interface {
	Create(context.Context, gateway.CallContext, scope.CreateCommand) (scope.CreateResult, error)
	Get(context.Context, gateway.CallContext, scope.GetQuery) (domain.Space, error)
}

// Dependencies are the gateway module interfaces the adapter depends on. Every
// field is an interface owned by this package, never a concrete gateway service.
type Dependencies struct {
	Memory   MemoryService
	Recall   RecallService
	Feedback FeedbackService
	Events   EventService
	Scopes   ScopeService
}

// RouteInfo is the declarative, inspectable metadata of one frozen route. It is
// returned by Handler.Routes and is the source of truth for route conformance
// tests.
type RouteInfo struct {
	// Operation is the stable gateway operation name.
	Operation gateway.OperationName
	// Method is the HTTP method.
	Method string
	// Path is the route pattern, using {name} for path parameters.
	Path string
	// Public reports whether the route may be called without caller identity.
	Public bool
	// Access classifies the route as a read or a write.
	Access AccessMode
	// Idempotency declares whether the route requires an Idempotency-Key.
	Idempotency IdempotencyPolicy
	// MaxBodyBytes is the request body limit; zero means no body is accepted.
	MaxBodyBytes int64
}

// handlerFunc is the route implementation. It receives the prepared exchange
// and the decoded path parameters.
type handlerFunc func(*exchange, routeParams)

// routeParams holds the decoded path parameters of one request.
type routeParams map[string]string

// route couples frozen metadata with the implementation that serves it.
type route struct {
	RouteInfo
	handle handlerFunc
}

// errMissingDependency is returned by New when a required gateway module
// interface is absent.
var errMissingDependency = errors.New("httpapi: gateway module dependency is required")
