package httpapi

import (
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
)

// scopeCreateRequest is the wire representation of a scope creation command.
type scopeCreateRequest struct {
	ParentScopeID *string `json:"parent_scope_id"`
	ScopeType     string  `json:"scope_type"`
	ExternalID    string  `json:"external_id"`
	DisplayName   string  `json:"display_name"`
}

// scopeResource is the public representation of a scope. Public HTTP
// terminology uses "scope" consistently; internal storage may still use
// "space".
type scopeResource struct {
	ScopeID       string  `json:"scope_id"`
	ParentScopeID *string `json:"parent_scope_id"`
	ScopeType     string  `json:"scope_type"`
	ExternalID    string  `json:"external_id"`
	DisplayName   string  `json:"display_name"`
	CreatedAt     string  `json:"created_at"`
}

// newScopeResource converts a domain space into its public scope
// representation.
func newScopeResource(value domain.Space) scopeResource {
	return scopeResource{
		ScopeID:       value.SpaceID,
		ParentScopeID: value.ParentSpaceID,
		ScopeType:     value.SpaceType,
		ExternalID:    value.ExternalID,
		DisplayName:   value.DisplayName,
		CreatedAt:     value.CreatedAt,
	}
}

// scopeCreateResponse is the scope write envelope.
type scopeCreateResponse struct {
	OperationID string        `json:"operation_id"`
	Replayed    bool          `json:"replayed"`
	Scope       scopeResource `json:"scope"`
}

// scopeGetResponse is the single-scope read envelope.
type scopeGetResponse struct {
	Scope scopeResource `json:"scope"`
}

// handleCreateScope serves POST /v1/scopes.
func (h *Handler) handleCreateScope(ex *exchange, _ routeParams) {
	var request scopeCreateRequest
	if err := ex.decodeBody(&request); err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.scopes.Create(ex.context(), ex.call, scope.CreateCommand{
		ParentSpaceID: request.ParentScopeID,
		SpaceType:     request.ScopeType,
		ExternalID:    request.ExternalID,
		DisplayName:   request.DisplayName,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusCreated, scopeCreateResponse{
		OperationID: result.OperationID,
		Replayed:    result.Replay,
		Scope:       newScopeResource(result.Scope),
	})
}

// handleGetScope serves GET /v1/scopes/{scope_id}.
func (h *Handler) handleGetScope(ex *exchange, params routeParams) {
	scopeID, err := requirePathID(params, "scope_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	found, err := h.scopes.Get(ex.context(), ex.call, scope.GetQuery{SpaceID: scopeID})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.writeJSON(http.StatusOK, scopeGetResponse{Scope: newScopeResource(found)})
}
