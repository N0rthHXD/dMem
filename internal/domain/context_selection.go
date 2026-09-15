package domain

import (
	"time"
	"unicode/utf8"
)

// ContextSelectionVersion identifies the budgeted context-selection contract.
// It is versioned independently from ranking: a selection change must not be
// able to replay a cursor or checkpoint bound to a different selection stage.
const ContextSelectionVersion = "context-selection-v2"

// ContextTokenFormatVersion identifies the deterministic context token-estimation
// format. Retrieval budgets and downstream generation must use the same identity
// so a selected package cannot silently exceed a generation window.
const ContextTokenFormatVersion = "context-tokens-v1"

// EstimateContextTokens returns the deterministic token estimate for context
// content under ContextTokenFormatVersion. The estimate counts Unicode code
// points so CJK and ASCII content are measured consistently across the Go
// retrieval boundary and the evaluation adapters.
func EstimateContextTokens(content string) int {
	return utf8.RuneCountInString(content)
}

// ContextBudget is the hard resource envelope for one selection pass. Every
// bound is an upper limit, not a target: selection must never pad output to
// reach a bound.
type ContextBudget struct {
	MaxTokens    int `json:"max_tokens"`
	MaxChars     int `json:"max_chars"`
	MaxResults   int `json:"max_results"`
	MaxSources   int `json:"max_sources"`
	MaxSensitive int `json:"max_sensitive"`
}

// ContextCandidate is one ranked candidate entering budgeted selection. It
// carries the provenance needed to cluster duplicates and compute coverage;
// relevance has already been computed by the ranking stage.
type ContextCandidate struct {
	MemoryID         string        `json:"memory_id"`
	RevisionID       string        `json:"revision_id"`
	SpaceID          string        `json:"space_id,omitempty"`
	ClusterID        string        `json:"cluster_id,omitempty"`
	Kind             MemoryKind    `json:"kind"`
	DisplayContent   string        `json:"display_content"`
	CanonicalHash    string        `json:"canonical_hash,omitempty"`
	PrimaryEvidence  []string      `json:"primary_evidence,omitempty"`
	SourceIDs        []string      `json:"source_ids,omitempty"`
	SessionIDs       []string      `json:"session_ids,omitempty"`
	EntityIDs        []string      `json:"entity_ids,omitempty"`
	SemanticVector   []float64     `json:"semantic_vector,omitempty"`
	EventTimeStart   *time.Time    `json:"event_time_start,omitempty"`
	EventTimeEnd     *time.Time    `json:"event_time_end,omitempty"`
	ValidFrom        *time.Time    `json:"valid_from,omitempty"`
	ValidUntil       *time.Time    `json:"valid_until,omitempty"`
	TimePrecision    TimePrecision `json:"time_precision,omitempty"`
	TimeSource       string        `json:"time_source,omitempty"`
	SourceSpeaker    string        `json:"source_speaker,omitempty"`
	Confidence       float64       `json:"confidence"`
	LifecycleState   string        `json:"lifecycle_state"`
	Sensitivity      string        `json:"sensitivity,omitempty"`
	Rank             int           `json:"rank"`
	Relevance        float64       `json:"relevance"`
	ExactPrimary     bool          `json:"exact_primary"`
	EstimatedTokens  int           `json:"estimated_tokens"`
	EstimatedChars   int           `json:"estimated_chars"`
	Aggregate        bool          `json:"aggregate"`
	AggregateSources []string      `json:"aggregate_sources,omitempty"`
}

// SelectedContextItem is one selected context slot. It records the
// representative revision, collapsed corroborating members, and the utility
// decomposition that justified consuming a slot.
type SelectedContextItem struct {
	Rank                   int
	ClusterID              string
	Representative         bool
	MemoryID               string
	RevisionID             string
	Kind                   MemoryKind
	DisplayContent         string
	EventTimeStart         *time.Time
	EventTimeEnd           *time.Time
	ValidFrom              *time.Time
	ValidUntil             *time.Time
	TimePrecision          TimePrecision
	TimeSource             string
	SourceSpeaker          string
	Evidence               []string
	Confidence             float64
	LifecycleState         string
	EstimatedTokens        int
	EstimatedChars         int
	CorroboratingMemoryIDs []string
	SupplementaryMemoryIDs []string
	Utility                float64
	Explanation            ContextSelectionExplanation
}

// RejectedContextCandidate records why a ranked candidate did not consume a
// context slot. Rejections are exposed only through policy-safe explain/debug
// output, never in the default recall response.
type RejectedContextCandidate struct {
	MemoryID   string  `json:"memory_id"`
	RevisionID string  `json:"revision_id"`
	Rank       int     `json:"rank"`
	ClusterID  string  `json:"cluster_id,omitempty"`
	Score      float64 `json:"score"`
	Reason     string  `json:"reason"`
}

// ContextBudgetAccounting reports deterministic hard-budget consumption.
type ContextBudgetAccounting struct {
	TokensUsed         int `json:"tokens_used"`
	CharsUsed          int `json:"chars_used"`
	ResultsUsed        int `json:"results_used"`
	SourcesUsed        int `json:"sources_used"`
	SensitiveUsed      int `json:"sensitive_used"`
	TokensRemaining    int `json:"tokens_remaining"`
	CharsRemaining     int `json:"chars_remaining"`
	ResultsRemaining   int `json:"results_remaining"`
	SourcesRemaining   int `json:"sources_remaining"`
	SensitiveRemaining int `json:"sensitive_remaining"`
}

// ContextSelectionExplanation decomposes one selected item's utility so the
// decision is reproducible and explainable.
type ContextSelectionExplanation struct {
	Utility          float64 `json:"utility"`
	Relevance        float64 `json:"relevance"`
	EvidenceCoverage float64 `json:"evidence_coverage"`
	SourceCoverage   float64 `json:"source_coverage"`
	EntityCoverage   float64 `json:"entity_coverage"`
	SessionCoverage  float64 `json:"session_coverage"`
	Redundancy       float64 `json:"redundancy"`
	ContextCost      float64 `json:"context_cost"`
	CollapsedMembers int     `json:"collapsed_members"`
	ExactPrimary     bool    `json:"exact_primary"`
	Representative   bool    `json:"representative"`
}
