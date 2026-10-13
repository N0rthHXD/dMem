package retrieval

import (
	"context"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

type ReferenceTimeSource interface {
	RecallReferenceTime(context.Context, string, bool) (string, error)
}

type CapabilityInspector interface {
	RecallCapabilities(context.Context, CapabilityRequest) (CapabilityStatus, error)
}

type CandidateStore interface {
	Recall(context.Context, RecallParams, RecallProcessor) (domain.SearchResponse, error)
}

type TelemetrySink interface {
	RecordRecallTelemetry(context.Context, TelemetryRecord) error
}

// PolicyAuthorizer is the Retrieval-owned authorization port. It keeps recall
// and semantic-query decisions behind a narrow interface so the module never
// reads policy state directly and never retains another module's concrete
// service.
type PolicyAuthorizer interface {
	Authorize(context.Context, gateway.CallContext, PolicyRequest) (PolicyDecision, error)
}

// PolicyRequest is one retrieval authorization question.
type PolicyRequest struct {
	Capability string
	SpaceIDs   []string
}

// PolicyDecision is the resolved authorization outcome.
type PolicyDecision struct {
	Allowed    bool
	ReasonCode string
}

// Ports groups narrow, consumer-owned Retrieval dependencies. One SQLite
// adapter may implement all four without widening any individual contract. The
// Policy port is optional; when absent, retrieval keeps the offline default of
// allowing recall and semantic queries.
type Ports struct {
	ReferenceTime ReferenceTimeSource
	Capabilities  CapabilityInspector
	Candidates    CandidateStore
	Telemetry     TelemetrySink
	Policy        PolicyAuthorizer
}

// RecallProcessor owns the persistence-independent stages run inside the
// adapter's consistent read snapshot.
type RecallProcessor interface {
	Orchestrate(context.Context, PipelineInput) error
	PrepareCursor(RecallParams) (RecallParams, error)
	EncodeCursor(RecallParams, CursorBinding, CursorPosition) (string, error)
}

type PipelineInput struct {
	Params        RecallParams
	CursorBinding CursorBinding
	Primary       PrimaryInput
	ExpandGraph   func(PrimaryOutput) ([]Candidate, error)
	RankingInput  func([]Candidate) (RankInput, error)
	Materialize   func(RankOutput) (SelectionInput, error)
	Render        func(SelectionOutput) error
}

type Candidate struct {
	MemoryID, RevisionID, ContentHash                  string
	FTSContent, ProjectionContent                      string
	KeywordRaw, KeywordScore                           float64
	SemanticRaw, SemanticScore, SemanticPercentile     float64
	SemanticRejected                                   bool
	SemanticRejectReason                               string
	GraphScore, Score                                  float64
	KeywordRank, SemanticRank, GraphRank               int
	GraphSignals                                       []domain.GraphSignal
	GraphFacts                                         []domain.EntityFact
	GraphPaths                                         []domain.GraphPath
	Confidence, Specificity                            float64
	EventStart, EventEnd, ValidFrom, ValidUntil        *time.Time
	TimePrecision, TimeSource, SourceSpeaker, Language string
	Kind                                               string
	Features                                           retrieval.FeatureVector
	RankOutput                                         retrieval.RankOutput
	GraphOnly, Protected                               bool
	// ContentMatched and HintMatched report which FTS field sources produced a
	// keyword match. HintOnly marks a keyword candidate whose only match came
	// from the Agent Tool search-hint projection rather than canonical content.
	// They are deterministic ordering and explain inputs, never content signals.
	ContentMatched, HintMatched, HintOnly bool
	// Relaxed marks a keyword candidate recalled by demoting the query's
	// required clauses to optional because the exact expression returned no
	// row. Relaxed candidates never receive the exact-match feature.
	Relaxed                             bool
	Quality, DuplicateRisk, Overbreadth *float64
	LifecyclePenalty                    *float64
}

type PrimaryInput struct {
	Candidates   []Candidate
	Calibration  retrieval.Calibration
	Weights      retrieval.Weights
	QuestionType retrieval.QuestionType
	SemanticTop  int
	KeywordTop   int
	MaxSeeds     int
}

type PrimaryOutput struct {
	Candidates  []Candidate
	Seeds       []string
	PrimaryKeys map[string]struct{}
}

type RankInput struct {
	Candidates  []Candidate
	Params      RecallParams
	Weights     retrieval.Weights
	UsageScores map[string]float64
	Terms       []retrieval.KeywordTerm
}

type RankOutput struct {
	Candidates      []Candidate
	TemporalRemoved int
	Start           int
}

type SelectionInput struct {
	QuestionType        retrieval.QuestionType
	Budget              domain.ContextBudget
	Candidates          []domain.ContextCandidate
	SkipDiversification bool
}

type SelectionOutput struct {
	Selection retrieval.SelectOutput
}

// CapabilityRequest contains only the scope and policy inputs needed to inspect
// recall readiness before execution.
type CapabilityRequest struct {
	Requested        []string
	SpaceIDs         []string
	IncludeAncestors bool
	EmptyEligibility bool
	GraphPolicy      retrieval.GraphPolicy
}

// CapabilityStatus is the adapter's point-in-time view of recall readiness.
// Retrieval owns selection and degradation policy over this storage-derived
// state.
type CapabilityStatus struct {
	Keyword         bool
	Semantic        bool
	SemanticVersion int
	SemanticStale   bool
	SemanticReason  string
	Graph           bool
	GraphReason     string
}

// CapabilityUnavailable reports that a capability selected during inspection
// became unusable during execution. Retrieval may degrade and retry.
type CapabilityUnavailable struct {
	Capability string
	Reason     string
	Denied     bool
}

func (e *CapabilityUnavailable) Error() string {
	return e.Capability + " capability unavailable: " + e.Reason
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
	EntityHints            []string
	Channels               []string
	EffectiveChannels      []string
	DegradationReasons     []string
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
	CursorState            *CursorState
	Intent                 string
	GraphPolicy            retrieval.GraphPolicy
	Plan                   retrieval.QueryPlan
	QueryTime              retrieval.TimeInterval
	QueryTimeSet           bool
	SkipDiversification    bool
	Telemetry              *Telemetry
}

// CursorBinding is the adapter's opaque point-in-time identity for every
// generation that can change membership, ranking, or context selection.
type CursorBinding struct {
	Schema, QueryPlan, Membership, Ranking, Graph, Context, Feedback string
}

// CursorPosition identifies the deterministic ranked candidate after which a
// subsequent page starts.
type CursorPosition struct {
	MemoryID, RevisionID string
	Score                float64
}
