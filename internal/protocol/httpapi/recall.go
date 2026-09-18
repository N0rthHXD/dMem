package httpapi

import (
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/retrieval"
)

// recallRequest is the wire representation of a recall query. Optional fields
// use pointers where the gateway distinguishes "omitted" from "explicitly
// false" or "explicitly empty".
type recallRequest struct {
	Query                  string    `json:"query"`
	ScopeIDs               []string  `json:"scope_ids"`
	IncludeAncestors       *bool     `json:"include_ancestors,omitempty"`
	MemoryTypes            *[]string `json:"memory_types,omitempty"`
	States                 []string  `json:"states,omitempty"`
	ReferenceTime          *string   `json:"reference_time,omitempty"`
	Intent                 string    `json:"intent,omitempty"`
	MaxResults             *int      `json:"max_results,omitempty"`
	MaxChars               *int      `json:"max_chars,omitempty"`
	MaxContextTokens       *int      `json:"max_context_tokens,omitempty"`
	IncludeEvidence        *bool     `json:"include_evidence,omitempty"`
	Explain                *bool     `json:"explain,omitempty"`
	ExcludeStaleAggregates *bool     `json:"exclude_stale_aggregates,omitempty"`
	Cursor                 string    `json:"cursor,omitempty"`
	SkipDiversification    *bool     `json:"skip_diversification,omitempty"`
}

// recallResponse is the recall result envelope.
type recallResponse struct {
	Context   domain.ContextPackage          `json:"context"`
	Execution domain.RecallExecutionMetadata `json:"execution"`
}

// handleRecall serves POST /v1/recall.
func (h *Handler) handleRecall(ex *exchange, _ routeParams) {
	var request recallRequest
	if err := ex.decodeBody(&request); err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.recall.Recall(ex.context(), ex.call, retrieval.Query{
		OperationID:               ex.requestID,
		Query:                     request.Query,
		SpaceIDs:                  request.ScopeIDs,
		IncludeAncestors:          derefBool(request.IncludeAncestors),
		IncludeAncestorsSet:       request.IncludeAncestors != nil,
		MemoryTypes:               derefStrings(request.MemoryTypes),
		MemoryTypesSet:            request.MemoryTypes != nil,
		States:                    request.States,
		ReferenceTime:             derefString(request.ReferenceTime),
		ReferenceTimeSet:          request.ReferenceTime != nil,
		Intent:                    domain.RecallIntent(request.Intent),
		Limit:                     derefInt(request.MaxResults),
		LimitSet:                  request.MaxResults != nil,
		MaxChars:                  derefInt(request.MaxChars),
		MaxCharsSet:               request.MaxChars != nil,
		MaxContextTokens:          derefInt(request.MaxContextTokens),
		MaxContextTokensSet:       request.MaxContextTokens != nil,
		IncludeEvidence:           derefBool(request.IncludeEvidence),
		Explain:                   derefBool(request.Explain),
		ExcludeStaleAggregates:    derefBool(request.ExcludeStaleAggregates),
		ExcludeStaleAggregatesSet: request.ExcludeStaleAggregates != nil,
		Cursor:                    request.Cursor,
		SkipDiversification:       derefBool(request.SkipDiversification),
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.writeJSON(http.StatusOK, recallResponse{Context: result.Context, Execution: result.Execution})
}

func derefBool(value *bool) bool {
	if value == nil {
		return false
	}
	return *value
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefStrings(value *[]string) []string {
	if value == nil {
		return nil
	}
	return *value
}
