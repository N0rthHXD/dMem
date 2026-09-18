package retrieval

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

// RecallStore is the initial coarse persistence port for recall. It keeps FTS5,
// sqlite-vec, graph, cursor, and telemetry behavior behind one capability
// boundary; finer channels may be split later only if measured scale requires
// it.
type RecallStore interface {
	Recall(context.Context, RecallParams) (domain.SearchResponse, error)
}

// RecallParams is the transport-free recall input. The Retrieval service builds
// the deterministic query plan and passes it here so candidate membership and
// ranking stay bound to one plan identity.
type RecallParams struct {
	OperationID            string
	Query                  string
	SpaceIDs               []string
	IncludeAncestors       bool
	MemoryTypes            []string
	MemoryTypesSet         bool
	States                 []string
	ReferenceTime          string
	ReferenceTimeSet       bool
	Channels               []string
	EffectiveChannels      []string
	SemanticVersion        int
	Limit                  int
	MaxChars               int
	MaxContextTokens       int
	MaxSources             int
	MaxSensitive           int
	IncludeEvidence        bool
	ExcludeStaleAggregates bool
	Explain                bool
	Cursor                 string
	Intent                 string
	GraphPolicy            retrieval.GraphPolicy
	Plan                   retrieval.QueryPlan
	QueryTime              retrieval.TimeInterval
	QueryTimeSet           bool
	SkipDiversification    bool
}
