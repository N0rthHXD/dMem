// Package retrieval defines the protocol-independent contracts for recall
// orchestration: query planning, capability selection, fusion, ranking, temporal
// interpretation, context selection, cursor semantics, and explain output.
package retrieval

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// OperationRecall is the stable operation name used to identify one recall
// operation in telemetry and cursors. Recall is a read, not an idempotent write.
const OperationRecall gateway.OperationName = "retrieval.recall"

// Service is the inbound, protocol-facing Retrieval module surface.
type Service interface {
	Recall(context.Context, gateway.CallContext, Query) (Result, error)
}

// Query is the transport-free recall request.
type Query struct {
	OperationID               string
	Query                     string
	SpaceIDs                  []string
	IncludeAncestors          bool
	IncludeAncestorsSet       bool
	MemoryTypes               []string
	MemoryTypesSet            bool
	States                    []string
	ReferenceTime             string
	ReferenceTimeSet          bool
	Intent                    domain.RecallIntent
	Channels                  []string
	Limit                     int
	LimitSet                  bool
	MaxChars                  int
	MaxCharsSet               bool
	MaxContextTokens          int
	MaxContextTokensSet       bool
	IncludeEvidence           bool
	Explain                   bool
	ExcludeStaleAggregates    bool
	ExcludeStaleAggregatesSet bool
	Cursor                    string
	GraphPolicy               string
	SkipDiversification       bool
}

// Result is the budgeted context package plus execution metadata.
type Result struct {
	Context   domain.ContextPackage
	Execution domain.RecallExecutionMetadata
}
