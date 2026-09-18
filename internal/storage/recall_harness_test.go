package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
)

// recallCapture decorates the SQLite candidate port so adapter-boundary tests
// can inspect the fully materialized response while the real Retrieval service
// still owns planning, capability selection, degradation, fusion, ranking,
// context selection, and cursor semantics.
type recallCapture struct {
	inner    gwretrieval.CandidateStore
	response domain.SearchResponse
}

func (c *recallCapture) Recall(ctx context.Context, params gwretrieval.RecallParams, processor gwretrieval.RecallProcessor) (domain.SearchResponse, error) {
	response, err := c.inner.Recall(ctx, params, processor)
	if err == nil {
		c.response = response
	}
	return response, err
}

// SearchMemories is a test-only adapter harness. Production callers use the
// Retrieval service directly over RecallCapability; these tests exercise the
// same real service and capability adapter while asserting on the adapter's
// materialized response.
func (r *Repository) SearchMemories(ctx context.Context, params SearchParams) (domain.SearchResponse, error) {
	capability := NewRecallCapability(r)
	capture := &recallCapture{inner: capability}
	_, _, graphPolicy := r.retrievalCalibration()
	service := gwretrieval.NewService(gwretrieval.Ports{
		ReferenceTime: capability,
		Capabilities:  capability,
		Candidates:    capture,
		Telemetry:     capability,
	}, gwretrieval.Defaults{
		MaxResults:       1000,
		MaxContextTokens: 1000000,
		MaxContextChars:  4000000,
		GraphPolicy:      graphPolicy,
	})
	if _, err := service.Recall(ctx, gateway.CallContext{RequestID: params.OperationID}, recallQueryFromParams(params)); err != nil {
		return domain.SearchResponse{}, recallLegacyError(err)
	}
	return capture.response, nil
}

func recallQueryFromParams(params SearchParams) gwretrieval.Query {
	return gwretrieval.Query{
		OperationID:               params.OperationID,
		Query:                     params.Query,
		SpaceIDs:                  params.SpaceIDs,
		IncludeAncestors:          params.IncludeAncestors,
		IncludeAncestorsSet:       true,
		MemoryTypes:               params.MemoryTypes,
		MemoryTypesSet:            params.MemoryTypesSet,
		States:                    params.States,
		ReferenceTime:             params.ReferenceTime,
		ReferenceTimeSet:          params.ReferenceTimeSet,
		Intent:                    domain.RecallIntent(params.Intent),
		Channels:                  params.Channels,
		Limit:                     params.Limit,
		MaxChars:                  params.MaxChars,
		MaxContextTokens:          params.MaxContextTokens,
		IncludeEvidence:           params.IncludeEvidence,
		Explain:                   params.Explain,
		ExcludeStaleAggregates:    params.ExcludeStaleAggregates,
		ExcludeStaleAggregatesSet: true,
		Cursor:                    params.Cursor,
		GraphPolicy:               string(params.GraphPolicy),
		SkipDiversification:       params.SkipDiversification,
	}
}

// recallLegacyError maps the stable gateway error set back to the storage
// sentinels used by the pre-existing adapter tests. Protocol adapters never do
// this; it exists only so the migrated tests keep their original assertions.
func recallLegacyError(err error) error {
	switch {
	case gateway.IsCode(err, gateway.ErrorInvalidInput):
		return ErrInvalidCursor
	case gateway.IsCode(err, gateway.ErrorCapabilityUnavailable):
		return ErrIndexDegraded
	case gateway.IsCode(err, gateway.ErrorPermissionDenied):
		return ErrPolicyDenied
	default:
		return err
	}
}
