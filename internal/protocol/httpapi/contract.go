package httpapi

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/internal/gateway/conflict"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
)

// Operation names published by this adapter for routes that read state. Write
// routes use the owning gateway module's operation name so idempotency identity
// stays identical to the module contract. Read operations are not idempotent
// writes and their names are adapter-declared identifiers.
const (
	OperationCapabilities         gateway.OperationName = "agent_tool.capabilities"
	OperationScopeGet             gateway.OperationName = "scope.get"
	OperationEventGet             gateway.OperationName = "extraction.event_get"
	OperationCandidateList        gateway.OperationName = "review.candidate_list"
	OperationCandidateGet         gateway.OperationName = "review.candidate_get"
	OperationMemoryList           gateway.OperationName = "memory.list"
	OperationMemoryGet            gateway.OperationName = "memory.get"
	OperationMemoryRevisionList   gateway.OperationName = "memory.revision_list"
	OperationMemoryStateEventList gateway.OperationName = "memory.state_event_list"
	OperationConflictList         gateway.OperationName = "conflict.list"
	OperationConflictGet          gateway.OperationName = "conflict.get"
	OperationProposalList         gateway.OperationName = "review.proposal_list"
	OperationProposalGet          gateway.OperationName = "review.proposal_get"
	// OperationProposalConfirm and OperationProposalReject name the shared
	// proposal review routes. The persisted write identity still uses the
	// family-specific operation name from the review module
	// (review.entity_confirm, review.fact_confirm, ...), so replay detection is
	// identical to the module contract.
	OperationProposalConfirm gateway.OperationName = "review.proposal_confirm"
	OperationProposalReject  gateway.OperationName = "review.proposal_reject"
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

// Route body limits, in bytes. Read routes declare no body. The values are
// documented in docs/api/http-rest-contract.md as current implemented behavior.
const (
	MaxScopeBodyBytes           int64 = 64 << 10
	MaxMemoryCommitBodyBytes    int64 = 1 << 20
	MaxForgetBodyBytes          int64 = 64 << 10
	MaxFeedbackBodyBytes        int64 = 256 << 10
	MaxRecallBodyBytes          int64 = 256 << 10
	MaxEventIngestBodyBytes     int64 = 1 << 20
	MaxCandidateReviewBodyBytes int64 = 16 << 10
	MaxMemoryReviseBodyBytes    int64 = 64 << 10
	MaxMemoryStateBodyBytes     int64 = 64 << 10
	MaxConflictResolveBodyBytes int64 = 64 << 10
	MaxProposalReviewBodyBytes  int64 = 16 << 10
)

type AgentToolService interface {
	Recall(context.Context, gateway.CallContext, agenttool.RecallCommand) (agenttool.RecallResult, error)
	MemoryCommit(context.Context, gateway.CallContext, agenttool.MemoryCommitCommand) (agenttool.MemoryCommitResult, error)
	Feedback(context.Context, gateway.CallContext, agenttool.FeedbackCommand) (agenttool.FeedbackResult, error)
	Forget(context.Context, gateway.CallContext, agenttool.ForgetCommand) (agenttool.ForgetResult, error)
}

type ScopeService interface {
	Ensure(context.Context, gateway.CallContext, scope.EnsureCommand) (scope.EnsureResult, error)
	Get(context.Context, gateway.CallContext, scope.GetQuery) (domain.Space, error)
}

// ExtractionService is the Provider Route raw-event ingress surface. It is
// absent from the Agent Tool Route, which never submits raw events.
type ExtractionService interface {
	Ingest(context.Context, gateway.CallContext, extraction.IngestCommand) (extraction.IngestResult, error)
	GetEvent(context.Context, gateway.CallContext, extraction.GetEventQuery) (domain.RawEvent, error)
}

// ReviewService is the Provider Route candidate review surface. Review is a
// management operation: it is absent from the Agent Tool Route and the MCP tool
// set, and it never reaches a model provider.
type ReviewService interface {
	ListCandidates(context.Context, gateway.CallContext, review.ListCandidatesQuery) (domain.CandidatePage, error)
	GetCandidate(context.Context, gateway.CallContext, review.GetCandidateQuery) (domain.Candidate, error)
	ConfirmCandidate(context.Context, gateway.CallContext, review.ReviewCandidateCommand) (review.CandidateReviewResult, error)
	RejectCandidate(context.Context, gateway.CallContext, review.ReviewCandidateCommand) (review.CandidateReviewResult, error)
}

// MemoryService is the memory authority administration surface. It exposes
// bounded reads and fenced revision/lifecycle writes; it is absent from the
// Agent Tool Route, the MCP tool set, and the versioned tool contract.
type MemoryService interface {
	List(context.Context, gateway.CallContext, memory.ListQuery) (domain.MemoryPage, error)
	Get(context.Context, gateway.CallContext, memory.GetQuery) (domain.Memory, error)
	ListRevisions(context.Context, gateway.CallContext, memory.MemoryQuery) ([]domain.MemoryRevision, error)
	ListStateEvents(context.Context, gateway.CallContext, memory.MemoryQuery) ([]domain.MemoryStateEvent, error)
	Revise(context.Context, gateway.CallContext, memory.ReviseCommand) (memory.WriteResult, error)
	ChangeState(context.Context, gateway.CallContext, memory.StateChangeCommand) (memory.WriteResult, error)
}

// ConflictService is the conflict administration surface. It lists and reads
// explicit conflict cases and applies one fenced resolution write; it is absent
// from the Agent Tool Route, the MCP tool set, and the versioned tool contract.
type ConflictService interface {
	List(context.Context, gateway.CallContext, conflict.ListQuery) (domain.ConflictCasePage, error)
	Get(context.Context, gateway.CallContext, conflict.GetQuery) (domain.ConflictCase, error)
	Resolve(context.Context, gateway.CallContext, conflict.ResolveCommand) (conflict.ResolveResult, error)
}

// ProposalService is the proposal review surface of the Provider Route. It
// lists, reads, confirms, and rejects graph, evolution, and aggregate
// proposals; it is absent from the Agent Tool Route, the MCP tool set, and the
// versioned tool contract, and it never reaches a model provider.
type ProposalService interface {
	ListProposals(context.Context, gateway.CallContext, review.ListProposalsQuery) (review.ProposalPage, error)
	GetProposal(context.Context, gateway.CallContext, review.GetProposalQuery) (review.Proposal, error)
	ConfirmProposal(context.Context, gateway.CallContext, review.ReviewProposalCommand) (review.ProposalReviewResult, error)
	RejectProposal(context.Context, gateway.CallContext, review.ReviewProposalCommand) (review.ProposalReviewResult, error)
}

// Dependencies are the gateway module interfaces the adapter depends on. Every
// field is an interface owned by this package, never a concrete gateway service.
type Dependencies struct {
	AgentTools AgentToolService
	Scopes     ScopeService
	Events     ExtractionService
	Reviews    ReviewService
	Memories   MemoryService
	Conflicts  ConflictService
	Proposals  ProposalService
}

// RouteInfo is the declarative, inspectable metadata of one implemented route.
// It is returned by Handler.Routes and is the source of truth for route
// conformance tests. The route set is the current implementation, not a frozen
// external contract: paths and DTOs may change until the project is released.
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

// route couples route metadata with the implementation that serves it.
type route struct {
	RouteInfo
	handle handlerFunc
}

// errMissingDependency is returned by New when a required gateway module
// interface is absent.
var errMissingDependency = errors.New("httpapi: gateway module dependency is required")
