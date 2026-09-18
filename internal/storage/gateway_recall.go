package storage

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
)

// RecallCapability adapts the SQLite repository to the Retrieval module's coarse
// recall port. FTS5, sqlite-vec, relational graph recall, cursor binding,
// context selection, and bounded telemetry all remain in this adapter behind the
// single Recall method.
type RecallCapability struct {
	repo *Repository
}

// NewRecallCapability wires the recall port over one repository.
func NewRecallCapability(repo *Repository) *RecallCapability {
	return &RecallCapability{repo: repo}
}

// Recall executes one fully planned recall and returns the budgeted context plus
// execution metadata.
func (c *RecallCapability) Recall(ctx context.Context, params gwretrieval.RecallParams) (domain.SearchResponse, error) {
	return c.repo.SearchMemories(ctx, SearchParams{
		OperationID:            params.OperationID,
		Query:                  params.Query,
		SpaceIDs:               params.SpaceIDs,
		IncludeAncestors:       params.IncludeAncestors,
		MemoryTypes:            params.MemoryTypes,
		MemoryTypesSet:         params.MemoryTypesSet,
		States:                 params.States,
		ReferenceTime:          params.ReferenceTime,
		ReferenceTimeSet:       params.ReferenceTimeSet,
		Channels:               params.Channels,
		EffectiveChannels:      params.EffectiveChannels,
		SemanticVersion:        params.SemanticVersion,
		Limit:                  params.Limit,
		MaxChars:               params.MaxChars,
		MaxContextTokens:       params.MaxContextTokens,
		MaxSources:             params.MaxSources,
		MaxSensitive:           params.MaxSensitive,
		IncludeEvidence:        params.IncludeEvidence,
		ExcludeStaleAggregates: params.ExcludeStaleAggregates,
		Explain:                params.Explain,
		Cursor:                 params.Cursor,
		Intent:                 params.Intent,
		GraphPolicy:            params.GraphPolicy,
		Plan:                   params.Plan,
		QueryTime:              params.QueryTime,
		QueryTimeSet:           params.QueryTimeSet,
		SkipDiversification:    params.SkipDiversification,
	})
}
