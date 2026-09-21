package httpapi

import (
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

func (h *Handler) handleEnsureScope(ex *exchange, _ routeParams) {
	var input toolcontract.ScopeEnsureInput
	if err := ex.decodeContractBody(toolcontract.SchemaScopeEnsureInput, &input); err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.scopes.Ensure(ex.context(), ex.call, scope.EnsureCommand{
		ParentSpaceID: input.ParentScopeID, SpaceType: input.ScopeType, ExternalID: input.ExternalID, DisplayName: input.DisplayName,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusOK, toolcontract.ScopeEnsureOutput{
		WriteMetadata: toolcontract.WriteMetadata{OperationID: result.OperationID, Replayed: result.Replay}, Scope: scopeDTO(result.Scope),
	})
}

func (h *Handler) handleGetScope(ex *exchange, params routeParams) {
	scopeID, err := requirePathID(params, "scope_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	value, err := h.scopes.Get(ex.context(), ex.call, scope.GetQuery{SpaceID: scopeID})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.writeJSON(http.StatusOK, toolcontract.ScopeOutput{Scope: scopeDTO(value)})
}

func scopeDTO(value domain.Space) toolcontract.Scope {
	return toolcontract.Scope{ScopeID: value.SpaceID, ParentScopeID: value.ParentSpaceID, ScopeType: value.SpaceType,
		ExternalID: value.ExternalID, DisplayName: value.DisplayName, CreatedAt: value.CreatedAt}
}
