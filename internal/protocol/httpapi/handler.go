package httpapi

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
)

// Handler is the Agent-neutral REST+JSON protocol adapter. It implements
// http.Handler and holds only gateway module interfaces.
type Handler struct {
	agentTools AgentToolService
	scopes     ScopeService

	routes []route
	newID  func() string
}

var _ http.Handler = (*Handler)(nil)

// New validates the dependencies and returns a handler over the frozen route
// set. Every gateway module interface is required: the initial surface has no
// optional routes, and a missing dependency must fail at construction rather
// than at request time.
func New(deps Dependencies) (*Handler, error) {
	switch {
	case deps.AgentTools == nil:
		return nil, fmt.Errorf("%w: agent tools", errMissingDependency)
	case deps.Scopes == nil:
		return nil, fmt.Errorf("%w: scopes", errMissingDependency)
	}
	h := &Handler{
		agentTools: deps.AgentTools,
		scopes:     deps.Scopes,
		newID:      newRequestID,
	}
	h.routes = h.buildRoutes()
	return h, nil
}

// Routes returns a copy of the frozen route metadata in declaration order.
func (h *Handler) Routes() []RouteInfo {
	infos := make([]RouteInfo, 0, len(h.routes))
	for _, r := range h.routes {
		infos = append(infos, r.RouteInfo)
	}
	return infos
}

// buildRoutes declares the initial Agent-neutral surface. Candidate review, job
// control, index rebuild, configuration reload, and graph maintenance routes are
// deliberately absent.
func (h *Handler) buildRoutes() []route {
	return []route{
		{
			RouteInfo: RouteInfo{
				Operation:   OperationCapabilities,
				Method:      http.MethodGet,
				Path:        "/v1/capabilities",
				Public:      true,
				Access:      AccessRead,
				Idempotency: IdempotencyNotRequired,
			},
			handle: h.handleCapabilities,
		},
		{
			RouteInfo: RouteInfo{
				Operation:    agenttool.OperationMemoryCommit,
				Method:       http.MethodPost,
				Path:         "/v1/memory-commits",
				Access:       AccessWrite,
				Idempotency:  IdempotencyRequired,
				MaxBodyBytes: MaxMemoryCommitBodyBytes,
			},
			handle: h.handleMemoryCommit,
		},
		{
			RouteInfo: RouteInfo{
				Operation:    agenttool.OperationRecall,
				Method:       http.MethodPost,
				Path:         "/v1/recall",
				Access:       AccessRead,
				Idempotency:  IdempotencyNotRequired,
				MaxBodyBytes: MaxRecallBodyBytes,
			},
			handle: h.handleRecall,
		},
		{
			RouteInfo: RouteInfo{
				Operation:    agenttool.OperationFeedback,
				Method:       http.MethodPost,
				Path:         "/v1/feedback",
				Access:       AccessWrite,
				Idempotency:  IdempotencyRequired,
				MaxBodyBytes: MaxFeedbackBodyBytes,
			},
			handle: h.handleFeedback,
		},
		{
			RouteInfo: RouteInfo{
				Operation:    agenttool.OperationForget,
				Method:       http.MethodPost,
				Path:         "/v1/forget",
				Access:       AccessWrite,
				Idempotency:  IdempotencyRequired,
				MaxBodyBytes: MaxForgetBodyBytes,
			},
			handle: h.handleForget,
		},
		{
			RouteInfo: RouteInfo{
				Operation:    scope.OperationEnsure,
				Method:       http.MethodPost,
				Path:         "/v1/scopes/ensure",
				Access:       AccessWrite,
				Idempotency:  IdempotencyRequired,
				MaxBodyBytes: MaxScopeBodyBytes,
			},
			handle: h.handleEnsureScope,
		},
		{
			RouteInfo: RouteInfo{
				Operation:   OperationScopeGet,
				Method:      http.MethodGet,
				Path:        "/v1/scopes/{scope_id}",
				Access:      AccessRead,
				Idempotency: IdempotencyNotRequired,
			},
			handle: h.handleGetScope,
		},
	}
}

// ServeHTTP resolves protocol metadata, matches a route, prepares the gateway
// call context, and dispatches. Panic recovery happens at this boundary so a
// module failure can never escape as a broken connection.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	writer := &responseWriter{ResponseWriter: w}
	requestID, validRequestID := h.resolveRequestID(r)
	writer.Header().Set(requestIDHeader, requestID)
	ex := &exchange{handler: h, writer: writer, request: r, requestID: requestID}

	defer func() {
		if recovered := recover(); recovered != nil {
			ex.writeSafeError(http.StatusInternalServerError, codeInternal, "internal error", false, nil, nil)
		}
	}()

	if !validRequestID {
		ex.writeSafeError(http.StatusUnprocessableEntity, codeInvalidRequest, "X-Request-ID is invalid", false, nil, nil)
		return
	}

	matched, params, allowed := matchRoute(h.routes, r.URL.EscapedPath(), r.Method)
	if matched == nil {
		if len(allowed) > 0 {
			writer.Header().Set("Allow", strings.Join(allowed, ", "))
			ex.writeSafeError(http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed", false, nil, nil)
			return
		}
		ex.writeSafeError(http.StatusNotFound, codeNotFound, "route not found", false, nil, nil)
		return
	}
	ex.route = matched

	if matched.MaxBodyBytes == 0 && r.ContentLength != 0 {
		ex.writeSafeError(http.StatusBadRequest, codeUnexpectedBody, "request body is not accepted", false, nil, nil)
		return
	}

	call, err := h.buildCall(r, matched, requestID)
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.call = call

	matched.handle(ex, params)
}

// resolveRequestID validates a caller-supplied request ID or generates one. The
// returned identifier is always usable for the response; the boolean reports
// whether the caller-supplied value was accepted.
func (h *Handler) resolveRequestID(r *http.Request) (string, bool) {
	value := r.Header.Get(requestIDHeader)
	if value == "" {
		return h.newID(), true
	}
	if !validOpaqueToken(value) {
		return h.newID(), false
	}
	return value, true
}

// buildCall converts protocol metadata into the protocol-neutral gateway call
// context. It never derives idempotency from the HTTP method or route.
func (h *Handler) buildCall(r *http.Request, matched *route, requestID string) (gateway.CallContext, error) {
	call := gateway.CallContext{RequestID: requestID}

	if correlation := r.Header.Get(correlationHeader); correlation != "" {
		if !validOpaqueToken(correlation) {
			return gateway.CallContext{}, invalidHeader(correlationHeader)
		}
		call.CorrelationID = correlation
	}

	if !matched.Public {
		subject := r.Header.Get(callerSubjectHeader)
		client := r.Header.Get(callerClientHeader)
		if subject != "" && !validOpaqueToken(subject) {
			return gateway.CallContext{}, invalidHeader(callerSubjectHeader)
		}
		if client != "" && !validOpaqueToken(client) {
			return gateway.CallContext{}, invalidHeader(callerClientHeader)
		}
		if subject == "" && client == "" {
			return gateway.CallContext{}, &requestError{
				status:  http.StatusUnprocessableEntity,
				code:    codeInvalidRequest,
				message: "caller identity is required",
			}
		}
		call.Caller = gateway.CallerIdentity{SubjectID: subject, ClientID: client}
	}

	if matched.Idempotency == IdempotencyRequired {
		key := r.Header.Get(idempotencyHeader)
		if key == "" {
			return gateway.CallContext{}, &requestError{
				status:  http.StatusUnprocessableEntity,
				code:    codeInvalidRequest,
				message: "Idempotency-Key is required",
			}
		}
		if !validOpaqueToken(key) {
			return gateway.CallContext{}, invalidHeader(idempotencyHeader)
		}
		call.Idempotency = &gateway.Idempotency{Key: key}
	}

	return call, nil
}

// exchange is the per-request adapter state shared by the dispatch layer and the
// route handlers.
type exchange struct {
	handler   *Handler
	writer    *responseWriter
	request   *http.Request
	route     *route
	requestID string
	call      gateway.CallContext
}

// context returns the request context, propagating cancellation to the gateway.
func (ex *exchange) context() context.Context {
	return ex.request.Context()
}

// setReplay publishes the idempotent-replay outcome as a response header.
func (ex *exchange) setReplay(replay bool) {
	ex.writer.Header().Set(replayHeader, strconv.FormatBool(replay))
}

// requireJSONContentType enforces the frozen media type for write bodies.
func requireJSONContentType(r *http.Request) error {
	value := r.Header.Get("Content-Type")
	if value == "" {
		return &requestError{
			status:  http.StatusUnsupportedMediaType,
			code:    codeUnsupportedMediaType,
			message: "Content-Type must be application/json",
		}
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return &requestError{
			status:  http.StatusUnsupportedMediaType,
			code:    codeUnsupportedMediaType,
			message: "Content-Type must be application/json",
		}
	}
	return nil
}

// invalidHeader builds the shared error for a malformed protocol header.
func invalidHeader(name string) error {
	return &requestError{
		status:  http.StatusUnprocessableEntity,
		code:    codeInvalidRequest,
		message: name + " is invalid",
	}
}

// requirePathID validates an opaque path identifier.
func requirePathID(params routeParams, name string) (string, error) {
	value := params[name]
	if !validIdentifier(value) {
		return "", &requestError{
			status:  http.StatusUnprocessableEntity,
			code:    codeInvalidRequest,
			message: name + " is invalid",
		}
	}
	return value, nil
}
