package knowledge

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// ReadStore is the narrow read-only persistence port the Knowledge module needs.
type ReadStore interface {
	GetEntity(context.Context, GetEntityQuery) (domain.Entity, error)
	ListEntities(context.Context, ListEntitiesQuery) (domain.EntityPage, error)
	GetRelation(context.Context, GetRelationQuery) (domain.MemoryRelation, error)
	ListRelations(context.Context, ListRelationsQuery) (domain.RelationPage, error)
	GetEntityFact(context.Context, GetEntityFactQuery) (domain.EntityFact, error)
	ListEntityFacts(context.Context, ListEntityFactsQuery) (domain.EntityFactPage, error)
	GetConflictCase(context.Context, GetConflictCaseQuery) (domain.ConflictCase, error)
	ListConflictCases(context.Context, ListConflictCasesQuery) (domain.ConflictCasePage, error)
}

// ReviewStore is the narrow mutation persistence port for proposal review and
// conflict resolution. Each method is atomic and reports an idempotent replay.
type ReviewStore interface {
	ReviewEntityProposal(context.Context, gateway.WriteIdentity, string, ReviewParams) (domain.EntityProposal, bool, error)
	ReviewRelationProposal(context.Context, gateway.WriteIdentity, string, ReviewParams) (domain.RelationProposal, bool, error)
	ReviewFactProposal(context.Context, gateway.WriteIdentity, string, ReviewParams) (domain.FactProposal, bool, error)
	ReviewEvolutionProposal(context.Context, gateway.WriteIdentity, string, ReviewParams) (domain.EvolutionProposal, bool, error)
	ReviewAggregateProposal(context.Context, gateway.WriteIdentity, string, ReviewParams) (domain.AggregateProposal, bool, error)
	ResolveConflict(context.Context, gateway.WriteIdentity, string, domain.ConflictResolutionCommand) (domain.ConflictCase, bool, error)
}

// ReviewParams is the transport-free review input.
type ReviewParams struct {
	ReasonCode string
	Note       *string
}
