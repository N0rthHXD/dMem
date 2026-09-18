package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/N0rthHXD/dMem/internal/domain"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
)

// RecallCapability adapts the SQLite repository to the Retrieval module's coarse
// recall port. FTS5, sqlite-vec, and relational graph recall remain in this
// adapter and execute in one read snapshot.
type RecallCapability struct {
	repo *Repository
}

// NewRecallCapability wires the recall port over one repository.
func NewRecallCapability(repo *Repository) *RecallCapability {
	return &RecallCapability{repo: repo}
}

func (c *RecallCapability) RecallReferenceTime(ctx context.Context, reference string, explicit bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if reference == "" {
		reference = formatTime(c.repo.runtime.Now())
	}
	return reference, nil
}

func (c *RecallCapability) RecallCapabilities(ctx context.Context, request gwretrieval.CapabilityRequest) (gwretrieval.CapabilityStatus, error) {
	tx, err := c.repo.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return gwretrieval.CapabilityStatus{}, gatewayError(err)
	}
	defer tx.Rollback()
	semantic, ftsState, _, err := semanticSearchStatus(ctx, tx)
	if err != nil {
		return gwretrieval.CapabilityStatus{}, gatewayError(err)
	}
	metadata, err := readSearchMetadata(ctx, tx)
	if err != nil {
		return gwretrieval.CapabilityStatus{}, gatewayError(err)
	}
	status := gwretrieval.CapabilityStatus{
		Keyword:         ftsState == "ready",
		Semantic:        c.repo.semanticQueryable(semantic, 0),
		SemanticVersion: semantic.ActiveVersion,
		SemanticStale:   semantic.State == "stale",
	}
	if hasChannel(request.Requested, ChannelSemantic) && status.Semantic && !request.EmptyEligibility {
		scope, scopeErr := searchScope(ctx, tx, request.SpaceIDs, request.IncludeAncestors)
		policyGeneration := 0
		if scopeErr == nil {
			policyGeneration, scopeErr = c.repo.authorizeSemanticQueryScope(ctx, tx, scope)
		}
		if scopeErr != nil || len(scope) == 0 {
			if scopeErr != nil && !errors.Is(scopeErr, ErrPolicyDenied) {
				return gwretrieval.CapabilityStatus{}, gatewayError(scopeErr)
			}
			status.Semantic, status.SemanticReason = false, "semantic_policy_denied"
		} else if runtime := c.repo.emb(); policyGeneration > 0 && runtime.PolicyGeneration != policyGeneration {
			status.Semantic, status.SemanticReason = false, "semantic_policy_changed"
		}
	}
	if !status.Semantic && status.SemanticReason == "" {
		status.SemanticReason = "semantic_unavailable"
	}
	_, status.Graph, status.GraphReason = graphExecutable(metadata, request.GraphPolicy)
	if request.GraphPolicy == "off" {
		status.GraphReason = ""
	}
	if err := tx.Commit(); err != nil {
		return gwretrieval.CapabilityStatus{}, gatewayError(err)
	}
	return status, nil
}

// Recall executes one fully planned recall and returns the budgeted context plus
// execution metadata.
func (c *RecallCapability) Recall(ctx context.Context, params gwretrieval.RecallParams, processor gwretrieval.RecallProcessor) (domain.SearchResponse, error) {
	response, err := c.repo.executeRecall(ctx, SearchParams{
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
		DegradationReasons:     params.DegradationReasons,
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
		CursorState:            params.CursorState,
		Intent:                 params.Intent,
		GraphPolicy:            params.GraphPolicy,
		Plan:                   params.Plan,
		QueryTime:              params.QueryTime,
		QueryTimeSet:           params.QueryTimeSet,
		SkipDiversification:    params.SkipDiversification,
		telemetry:              params.Telemetry,
		processor:              processor,
	})
	return response, gatewayError(err)
}

func (c *RecallCapability) RecordRecallTelemetry(ctx context.Context, record gwretrieval.TelemetryRecord) error {
	return gatewayError(c.repo.recordQueryTelemetry(ctx, record))
}
