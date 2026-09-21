package domain

import (
	"context"
	"encoding/json"
	"time"
)

type Space struct {
	SpaceID       string  `json:"space_id"`
	ParentSpaceID *string `json:"parent_space_id"`
	SpaceType     string  `json:"space_type"`
	ExternalID    string  `json:"external_id"`
	DisplayName   string  `json:"display_name"`
	CreatedAt     string  `json:"created_at"`
}

type SpacePage struct {
	Items      []Space `json:"items"`
	NextCursor *string `json:"next_cursor,omitempty"`
}

type Content struct {
	Kind         string `json:"kind"`
	MediaType    string `json:"media_type"`
	Text         string `json:"text,omitempty"`
	Value        any    `json:"value,omitempty"`
	ReferenceURI string `json:"reference_uri,omitempty"`
	DeclaredSize *int64 `json:"declared_size,omitempty"`
	DeclaredHash string `json:"declared_hash,omitempty"`
}

func (c Content) MarshalJSON() ([]byte, error) {
	switch c.Kind {
	case "text":
		return json.Marshal(struct {
			Kind      string `json:"kind"`
			MediaType string `json:"media_type"`
			Text      string `json:"text"`
		}{c.Kind, c.MediaType, c.Text})
	case "json":
		return json.Marshal(struct {
			Kind      string `json:"kind"`
			MediaType string `json:"media_type"`
			Value     any    `json:"value"`
		}{c.Kind, c.MediaType, c.Value})
	case "reference":
		return json.Marshal(struct {
			Kind         string `json:"kind"`
			MediaType    string `json:"media_type"`
			ReferenceURI string `json:"reference_uri"`
			DeclaredSize *int64 `json:"declared_size,omitempty"`
			DeclaredHash string `json:"declared_hash,omitempty"`
		}{c.Kind, c.MediaType, c.ReferenceURI, c.DeclaredSize, c.DeclaredHash})
	default:
		type content Content
		return json.Marshal(content(c))
	}
}

type RawEvent struct {
	EventID          string         `json:"event_id"`
	OperationID      string         `json:"operation_id"`
	CallerID         string         `json:"caller_id"`
	SpaceID          string         `json:"space_id"`
	SourceType       string         `json:"source_type"`
	SourceName       string         `json:"source_name"`
	SourceEventID    *string        `json:"source_event_id,omitempty"`
	SourceStreamID   *string        `json:"source_stream_id,omitempty"`
	Sequence         *int64         `json:"sequence,omitempty"`
	OccurredAt       string         `json:"occurred_at"`
	ReceivedAt       string         `json:"received_at"`
	Content          Content        `json:"content"`
	Metadata         map[string]any `json:"metadata"`
	ProcessingStatus string         `json:"processing_status"`
}

type EventPage struct {
	Items      []RawEvent `json:"items"`
	NextCursor *string    `json:"next_cursor,omitempty"`
}

type IngestResponse struct {
	EventID     string   `json:"event_id"`
	OperationID string   `json:"operation_id"`
	Status      string   `json:"status"`
	JobIDs      []string `json:"job_ids"`
	AcceptedAt  string   `json:"accepted_at"`
}

type JobReference struct {
	OperationID string `json:"operation_id"`
	JobID       string `json:"job_id"`
}

type JobError struct {
	Code    string
	Message string
}

type Job struct {
	JobID                   string    `json:"job_id"`
	OperationID             string    `json:"operation_id"`
	JobType                 string    `json:"job_type"`
	TargetType              string    `json:"target_type"`
	TargetID                string    `json:"target_id"`
	Status                  string    `json:"status"`
	AttemptCount            int       `json:"attempt_count"`
	MaxAttempts             int       `json:"max_attempts"`
	ResultCode              *string   `json:"result_code"`
	LastErrorCode           *string   `json:"last_error_code"`
	CreatedAt               string    `json:"created_at"`
	StartedAt               *string   `json:"started_at"`
	FinishedAt              *string   `json:"finished_at"`
	ProcessingVersion       string    `json:"-"`
	Priority                int       `json:"-"`
	NextRunAt               string    `json:"-"`
	CancelRequestedAt       *string   `json:"-"`
	Result                  any       `json:"-"`
	LastError               *JobError `json:"-"`
	ExpiresAt               *string   `json:"-"`
	Capability              string    `json:"-"`
	ConfigurationGeneration int       `json:"-"`
	PolicyGeneration        int       `json:"-"`
	CompatibilityIdentity   string    `json:"-"`
	BlockedReasonCode       string    `json:"-"`
	ReconciliationRunID     string    `json:"-"`
	ProviderAttemptCount    int       `json:"-"`
}

type ProcessingContent struct {
	Kind         string
	MediaType    string
	Text         string
	DeclaredSize int64
	DeclaredHash string
}

type ProcessingEvent struct {
	EventID        string
	SpaceID        string
	SourceType     string
	SourceName     string
	SourceStreamID string
	Sequence       *int64
	Sensitivity    string
	OccurredAt     time.Time
	Content        ProcessingContent
}

type ProcessingMemory struct {
	MemoryID        string
	RevisionID      string
	SpaceID         string
	Content         string
	CurrentState    string
	Sensitivity     string
	ValidFrom       *time.Time
	ValidUntil      *time.Time
	ProposalCapable bool
}

type ProcessingPolicy struct {
	AllowedMemoryTypes     []string
	MaxCandidates          int
	MaxContentChars        int
	MaxCandidateChars      int
	MaxTotalCandidateChars int
}

type ProcessingInput struct {
	ProcessorIdentity string
	ProcessingVersion string
	JobID             string
	Attempt           int
	Target            ProcessingEvent
	ContextEvents     []ProcessingEvent
	ContextMemories   []ProcessingMemory
	OmissionSummary   map[string]int
	Policy            ProcessingPolicy
}

type ProcessingUsage struct {
	InputTokens  int
	OutputTokens int
}

type ProcessingOutcome string

const (
	ProcessingNoProcessor         ProcessingOutcome = "no_processor"
	ProcessingNoMemory            ProcessingOutcome = "no_memory"
	ProcessingCandidatesExtracted ProcessingOutcome = "candidates_extracted"
	ProcessingTransientFailure    ProcessingOutcome = "transient_failure"
	ProcessingPermanentFailure    ProcessingOutcome = "permanent_failure"
	ProcessingInvalidOutput       ProcessingOutcome = "invalid_output"
	ProcessingPolicyRejected      ProcessingOutcome = "policy_rejected"
)

type ProcessingResult struct {
	Code                ProcessingOutcome
	Proposals           []MemoryCandidateProposal
	Message             string
	ReasonCode          string
	ProviderIdentity    string
	ModelIdentity       string
	PolicyVersion       string
	ProviderRequestID   string
	Usage               ProcessingUsage
	OmittedContextCount int
	OmissionSummary     map[string]int
}

type Memory struct {
	MemoryID                string         `json:"memory_id"`
	SpaceID                 string         `json:"space_id"`
	Kind                    MemoryKind     `json:"kind"`
	CurrentState            string         `json:"current_state"`
	Frozen                  bool           `json:"frozen"`
	Sensitivity             string         `json:"sensitivity,omitempty"`
	CurrentRevision         MemoryRevision `json:"current_revision"`
	CreatedAt               string         `json:"created_at"`
	UpdatedAt               string         `json:"updated_at"`
	ConflictRefs            []ConflictRef  `json:"conflict_refs,omitempty"`
	AggregateStale          bool           `json:"aggregate_stale,omitempty"`
	AggregateStaleReasons   []string       `json:"aggregate_stale_reasons,omitempty"`
	AggregateProvenanceRefs []string       `json:"aggregate_provenance_refs,omitempty"`
}

type MemoryPage struct {
	Items      []Memory `json:"items"`
	NextCursor *string  `json:"next_cursor,omitempty"`
}

type CandidateReview struct {
	ReviewID   string  `json:"review_id"`
	FromState  string  `json:"from_state"`
	ToState    string  `json:"to_state"`
	ReasonCode string  `json:"reason_code"`
	Note       *string `json:"note,omitempty"`
	ReviewerID string  `json:"reviewer_id"`
	MemoryID   *string `json:"memory_id,omitempty"`
	RevisionID *string `json:"revision_id,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

type Candidate struct {
	CandidateID         string             `json:"candidate_id"`
	EventID             string             `json:"event_id"`
	SpaceID             string             `json:"space_id"`
	Revision            MemoryRevision     `json:"revision"`
	ProcessorIdentity   string             `json:"processor_identity"`
	ProviderIdentity    *string            `json:"provider_identity,omitempty"`
	ModelIdentity       *string            `json:"model_identity,omitempty"`
	ProcessingVersion   string             `json:"processing_version"`
	SpecificationHash   string             `json:"specification_hash"`
	ReviewState         string             `json:"review_state"`
	ConfirmedMemoryID   *string            `json:"confirmed_memory_id,omitempty"`
	ConfirmedRevisionID *string            `json:"confirmed_revision_id,omitempty"`
	Evidence            []RevisionEvidence `json:"evidence"`
	Reviews             []CandidateReview  `json:"reviews"`
	CreatedAt           string             `json:"created_at"`
	ReviewedAt          *string            `json:"reviewed_at,omitempty"`
}

type CandidatePage struct {
	Items      []Candidate `json:"items"`
	NextCursor *string     `json:"next_cursor,omitempty"`
}

type EntityAlias struct {
	AliasID              string `json:"alias_id"`
	EntityID             string `json:"entity_id"`
	Alias                string `json:"alias"`
	NormalizedAlias      string `json:"normalized_alias"`
	NormalizationVersion string `json:"normalization_version"`
	Source               string `json:"source"`
	State                string `json:"state"`
	CreatedBy            string `json:"created_by"`
	CreatedAt            string `json:"created_at"`
}

type EntityMention struct {
	MentionID         string  `json:"mention_id"`
	EntityID          string  `json:"entity_id"`
	MemoryID          *string `json:"memory_id,omitempty"`
	RevisionID        *string `json:"revision_id,omitempty"`
	EventID           *string `json:"event_id,omitempty"`
	StartOffset       int     `json:"start_offset"`
	EndOffset         int     `json:"end_offset"`
	MentionType       string  `json:"mention_type"`
	Source            string  `json:"source"`
	SurfaceText       *string `json:"surface_text,omitempty"`
	CreatedBy         string  `json:"created_by"`
	ProcessingVersion string  `json:"processing_version"`
	CreatedAt         string  `json:"created_at"`
}

type Entity struct {
	EntityID             string          `json:"entity_id"`
	SpaceID              string          `json:"space_id"`
	EntityType           string          `json:"entity_type"`
	CanonicalName        string          `json:"canonical_name"`
	NormalizedName       string          `json:"normalized_name"`
	NormalizationVersion string          `json:"normalization_version"`
	State                string          `json:"state"`
	Sensitivity          string          `json:"sensitivity"`
	CreatedBy            string          `json:"created_by"`
	ProcessingVersion    string          `json:"processing_version"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
	Aliases              []EntityAlias   `json:"aliases"`
	Mentions             []EntityMention `json:"mentions,omitempty"`
}

type EntityPage struct {
	Items      []Entity `json:"items"`
	NextCursor *string  `json:"next_cursor,omitempty"`
}

type EntityProposalReview struct {
	ReviewID               string  `json:"review_id"`
	FromState              string  `json:"from_state"`
	ToState                string  `json:"to_state"`
	ReasonCode             string  `json:"reason_code"`
	Note                   *string `json:"note,omitempty"`
	ReviewerID             string  `json:"reviewer_id"`
	ResultEntityID         *string `json:"result_entity_id,omitempty"`
	ResultCanonicalAliasID *string `json:"result_canonical_alias_id,omitempty"`
	ResultAliasID          *string `json:"result_alias_id,omitempty"`
	ResultMentionID        *string `json:"result_mention_id,omitempty"`
	CreatedAt              string  `json:"created_at"`
}

type EntityProposal struct {
	ProposalID             string                 `json:"proposal_id"`
	EventID                string                 `json:"event_id"`
	SpaceID                string                 `json:"space_id"`
	ProposalKind           string                 `json:"proposal_kind"`
	EntityID               *string                `json:"entity_id,omitempty"`
	EntityType             *string                `json:"entity_type,omitempty"`
	CanonicalName          *string                `json:"canonical_name,omitempty"`
	Alias                  *string                `json:"alias,omitempty"`
	NormalizationVersion   *string                `json:"normalization_version,omitempty"`
	MentionMemoryID        *string                `json:"mention_memory_id,omitempty"`
	MentionRevisionID      *string                `json:"mention_revision_id,omitempty"`
	MentionEventID         *string                `json:"mention_event_id,omitempty"`
	StartOffset            *int                   `json:"start_offset,omitempty"`
	EndOffset              *int                   `json:"end_offset,omitempty"`
	MentionType            *string                `json:"mention_type,omitempty"`
	Sensitivity            string                 `json:"sensitivity"`
	ProcessorIdentity      string                 `json:"processor_identity"`
	ProviderIdentity       *string                `json:"provider_identity,omitempty"`
	ModelIdentity          *string                `json:"model_identity,omitempty"`
	ProcessingVersion      string                 `json:"processing_version"`
	SpecificationHash      string                 `json:"specification_hash"`
	ReviewState            string                 `json:"review_state"`
	ResultEntityID         *string                `json:"result_entity_id,omitempty"`
	ResultCanonicalAliasID *string                `json:"result_canonical_alias_id,omitempty"`
	ResultAliasID          *string                `json:"result_alias_id,omitempty"`
	ResultMentionID        *string                `json:"result_mention_id,omitempty"`
	CreatedAt              string                 `json:"created_at"`
	ReviewedAt             *string                `json:"reviewed_at,omitempty"`
	Reviews                []EntityProposalReview `json:"reviews"`
}

type EntityProposalPage struct {
	Items      []EntityProposal `json:"items"`
	NextCursor *string          `json:"next_cursor,omitempty"`
}

type RelationEvidence struct {
	EvidenceID   string  `json:"evidence_id,omitempty"`
	EvidenceType string  `json:"evidence_type"`
	EventID      *string `json:"event_id,omitempty"`
	MemoryID     *string `json:"memory_id,omitempty"`
	RevisionID   *string `json:"revision_id,omitempty"`
	StartOffset  *int    `json:"start_offset,omitempty"`
	EndOffset    *int    `json:"end_offset,omitempty"`
	Note         *string `json:"note,omitempty"`
	CreatedBy    string  `json:"created_by,omitempty"`
	CreatedAt    string  `json:"created_at,omitempty"`
}

type RelationStateEvent struct {
	StateEventID string  `json:"state_event_id"`
	OperationID  string  `json:"operation_id"`
	FromState    *string `json:"from_state,omitempty"`
	ToState      string  `json:"to_state"`
	Reason       string  `json:"reason"`
	CreatedBy    string  `json:"created_by"`
	CreatedAt    string  `json:"created_at"`
}

type MemoryRelation struct {
	RelationID        string               `json:"relation_id"`
	SpaceID           string               `json:"space_id"`
	RelationType      string               `json:"relation_type"`
	SourceMemoryID    string               `json:"source_memory_id"`
	SourceRevisionID  string               `json:"source_revision_id"`
	TargetMemoryID    string               `json:"target_memory_id"`
	TargetRevisionID  string               `json:"target_revision_id"`
	State             string               `json:"state"`
	Source            string               `json:"source"`
	Confidence        *float64             `json:"confidence,omitempty"`
	SpecificationHash string               `json:"specification_hash"`
	CreatedBy         string               `json:"created_by"`
	ProcessingVersion string               `json:"processing_version"`
	CreatedAt         string               `json:"created_at"`
	UpdatedAt         string               `json:"updated_at"`
	Evidence          []RelationEvidence   `json:"evidence"`
	StateEvents       []RelationStateEvent `json:"state_events"`
}

type RelationPage struct {
	Items      []MemoryRelation `json:"items"`
	NextCursor *string          `json:"next_cursor,omitempty"`
	IndexState string           `json:"index_state,omitempty"`
	Degraded   bool             `json:"degraded,omitempty"`
}

type RelationProposalReview struct {
	ReviewID   string  `json:"review_id"`
	FromState  string  `json:"from_state"`
	ToState    string  `json:"to_state"`
	ReasonCode string  `json:"reason_code"`
	Note       *string `json:"note,omitempty"`
	ReviewerID string  `json:"reviewer_id"`
	RelationID *string `json:"relation_id,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

type RelationProposal struct {
	ProposalID        string                   `json:"proposal_id"`
	EventID           string                   `json:"event_id"`
	SpaceID           string                   `json:"space_id"`
	RelationType      string                   `json:"relation_type"`
	SourceMemoryID    string                   `json:"source_memory_id"`
	SourceRevisionID  string                   `json:"source_revision_id"`
	TargetMemoryID    string                   `json:"target_memory_id"`
	TargetRevisionID  string                   `json:"target_revision_id"`
	Confidence        *float64                 `json:"confidence,omitempty"`
	Evidence          []RelationEvidence       `json:"evidence"`
	ProcessorIdentity string                   `json:"processor_identity"`
	ProviderIdentity  *string                  `json:"provider_identity,omitempty"`
	ModelIdentity     *string                  `json:"model_identity,omitempty"`
	ProcessingVersion string                   `json:"processing_version"`
	SpecificationHash string                   `json:"specification_hash"`
	ReviewState       string                   `json:"review_state"`
	ResultRelationID  *string                  `json:"result_relation_id,omitempty"`
	CreatedAt         string                   `json:"created_at"`
	ReviewedAt        *string                  `json:"reviewed_at,omitempty"`
	Reviews           []RelationProposalReview `json:"reviews"`
}

type RelationProposalPage struct {
	Items      []RelationProposal `json:"items"`
	NextCursor *string            `json:"next_cursor,omitempty"`
}

type AggregateInput struct {
	MemoryID     string `json:"memory_id"`
	RevisionID   string `json:"revision_id"`
	SpaceID      string `json:"space_id,omitempty"`
	Content      string `json:"content,omitempty"`
	CurrentState string `json:"current_state,omitempty"`
	Sensitivity  string `json:"sensitivity,omitempty"`
}

type AggregateProviderInput struct {
	SpaceID, Kind, PolicyVersion, ProcessingVersion string
	Inputs                                          []AggregateInput
}

type AggregateProviderResult struct {
	Content, Sensitivity, ProviderIdentity, ModelIdentity string
	ValidFrom, ValidUntil                                 *string
}

type AggregateProvider interface {
	Build(context.Context, AggregateProviderInput) (AggregateProviderResult, error)
}

type AggregateProposalReview struct {
	ReviewID   string  `json:"review_id"`
	FromState  string  `json:"from_state"`
	ToState    string  `json:"to_state"`
	ReasonCode string  `json:"reason_code"`
	Note       *string `json:"note,omitempty"`
	ReviewerID string  `json:"reviewer_id"`
	MemoryID   *string `json:"memory_id,omitempty"`
	RevisionID *string `json:"revision_id,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

type AggregateProposal struct {
	ProposalID        string                    `json:"proposal_id"`
	SpaceID           string                    `json:"space_id"`
	AggregateKind     string                    `json:"aggregate_kind"`
	Content           string                    `json:"content"`
	InputFingerprint  string                    `json:"input_fingerprint"`
	ValidFrom         *string                   `json:"valid_from,omitempty"`
	ValidUntil        *string                   `json:"valid_until,omitempty"`
	Sensitivity       string                    `json:"sensitivity"`
	PolicyVersion     string                    `json:"policy_version"`
	ProcessorIdentity string                    `json:"processor_identity"`
	ProviderIdentity  *string                   `json:"provider_identity,omitempty"`
	ModelIdentity     *string                   `json:"model_identity,omitempty"`
	ProcessingVersion string                    `json:"processing_version"`
	SpecificationHash string                    `json:"specification_hash"`
	ReviewState       string                    `json:"review_state"`
	ResultMemoryID    *string                   `json:"result_memory_id,omitempty"`
	ResultRevisionID  *string                   `json:"result_revision_id,omitempty"`
	Inputs            []AggregateInput          `json:"inputs"`
	Reviews           []AggregateProposalReview `json:"reviews"`
	CreatedAt         string                    `json:"created_at"`
	ReviewedAt        *string                   `json:"reviewed_at,omitempty"`
}

type AggregateProposalPage struct {
	Items      []AggregateProposal `json:"items"`
	NextCursor *string             `json:"next_cursor,omitempty"`
}

type AggregateProvenance struct {
	RelationID        string `json:"relation_id"`
	MemoryID          string `json:"memory_id"`
	RevisionID        string `json:"revision_id"`
	CurrentRevisionID string `json:"current_revision_id"`
	CurrentState      string `json:"current_state"`
	Sensitivity       string `json:"sensitivity"`
}

type Aggregate struct {
	AggregateID       string                `json:"aggregate_id"`
	Memory            Memory                `json:"memory"`
	AggregateKind     string                `json:"aggregate_kind"`
	PolicyVersion     string                `json:"policy_version"`
	ProcessingVersion string                `json:"processing_version"`
	Sensitivity       string                `json:"sensitivity"`
	Freshness         string                `json:"freshness"`
	AggregateStale    bool                  `json:"aggregate_stale"`
	StaleReasons      []string              `json:"stale_reasons"`
	InputFingerprint  string                `json:"input_fingerprint"`
	Generation        int64                 `json:"generation"`
	Provenance        []AggregateProvenance `json:"provenance"`
	CheckedAt         string                `json:"checked_at"`
}

type AggregatePage struct {
	Items      []Aggregate `json:"items"`
	NextCursor *string     `json:"next_cursor,omitempty"`
}

type AggregateHealth struct {
	State           string  `json:"state"`
	FreshCount      int     `json:"fresh_count"`
	StaleCount      int     `json:"stale_count"`
	PendingRebuilds int     `json:"pending_rebuilds"`
	OldestStaleAt   *string `json:"oldest_stale_at,omitempty"`
}

type EvolutionTarget struct {
	MemoryID           string `json:"memory_id"`
	ExpectedRevisionID string `json:"expected_revision_id"`
}

type EvolutionResultRef struct {
	ResultType string `json:"result_type"`
	ResultID   string `json:"result_id"`
}

type EvolutionReview struct {
	ReviewID        string               `json:"review_id"`
	FromState       string               `json:"from_state"`
	ToState         string               `json:"to_state"`
	ReplacementMode *string              `json:"replacement_mode,omitempty"`
	ReasonCode      string               `json:"reason_code"`
	Note            *string              `json:"note,omitempty"`
	ReviewerID      string               `json:"reviewer_id"`
	CreatedAt       string               `json:"created_at"`
	Results         []EvolutionResultRef `json:"results"`
}

type EvolutionProposal struct {
	ProposalID        string             `json:"proposal_id"`
	EventID           string             `json:"event_id"`
	SpaceID           string             `json:"space_id"`
	Action            string             `json:"action"`
	ProposedContent   *string            `json:"proposed_content,omitempty"`
	ValidFrom         *string            `json:"valid_from,omitempty"`
	ValidUntil        *string            `json:"valid_until,omitempty"`
	MemoryType        *string            `json:"memory_type,omitempty"`
	Sensitivity       string             `json:"sensitivity"`
	RelationSpecs     json.RawMessage    `json:"relation_specs"`
	MentionSpecs      json.RawMessage    `json:"mention_specs"`
	ProcessorIdentity string             `json:"processor_identity"`
	ProviderIdentity  *string            `json:"provider_identity,omitempty"`
	ModelIdentity     *string            `json:"model_identity,omitempty"`
	ProcessingVersion string             `json:"processing_version"`
	SpecificationHash string             `json:"specification_hash"`
	ReviewState       string             `json:"review_state"`
	CreatedAt         string             `json:"created_at"`
	ReviewedAt        *string            `json:"reviewed_at,omitempty"`
	Targets           []EvolutionTarget  `json:"targets"`
	Evidence          []RelationEvidence `json:"evidence"`
	Reviews           []EvolutionReview  `json:"reviews"`
}

type EvolutionProposalPage struct {
	Items      []EvolutionProposal `json:"items"`
	NextCursor *string             `json:"next_cursor,omitempty"`
}

type ConflictParticipant struct {
	MemoryID   string `json:"memory_id"`
	RevisionID string `json:"revision_id"`
}

type ConflictResolutionEvent struct {
	ResolutionEventID string               `json:"resolution_event_id"`
	ResolutionType    string               `json:"resolution_type"`
	FromState         string               `json:"from_state"`
	ToState           string               `json:"to_state"`
	Reason            string               `json:"reason"`
	ResolvedBy        string               `json:"resolved_by"`
	CreatedAt         string               `json:"created_at"`
	Results           []EvolutionResultRef `json:"results"`
}

type ConflictResolutionCommand struct {
	ResolutionType  string             `json:"resolution_type"`
	ExpectedTargets []EvolutionTarget  `json:"expected_targets"`
	SelectedTarget  *EvolutionTarget   `json:"selected_target,omitempty"`
	ProposedContent *string            `json:"proposed_content,omitempty"`
	ValidFrom       *string            `json:"valid_from,omitempty"`
	ValidUntil      *string            `json:"valid_until,omitempty"`
	Evidence        []RevisionEvidence `json:"evidence,omitempty"`
	ReplacementMode *string            `json:"replacement_mode,omitempty"`
	ResultStates    map[string]string  `json:"result_states"`
	Reason          string             `json:"reason"`
}

type ConflictCase struct {
	CaseID            string                   `json:"case_id"`
	SpaceID           string                   `json:"space_id"`
	State             string                   `json:"state"`
	OpenedBy          string                   `json:"opened_by"`
	ProcessingVersion string                   `json:"processing_version"`
	CreatedAt         string                   `json:"created_at"`
	ClosedAt          *string                  `json:"closed_at,omitempty"`
	Participants      []ConflictParticipant    `json:"participants"`
	Relations         []MemoryRelation         `json:"relations"`
	Resolution        *ConflictResolutionEvent `json:"resolution,omitempty"`
}

type ConflictCasePage struct {
	Items      []ConflictCase `json:"items"`
	NextCursor *string        `json:"next_cursor,omitempty"`
}

type ConflictRef struct {
	CaseID          string `json:"case_id"`
	RelationID      string `json:"relation_id"`
	OtherMemoryID   string `json:"other_memory_id"`
	OtherRevisionID string `json:"other_revision_id"`
}

type SearchResult struct {
	Memory           Memory        `json:"memory"`
	Rank             int           `json:"rank"`
	KeywordRelevance *float64      `json:"keyword_relevance,omitempty"`
	SemanticScore    *float64      `json:"semantic_relevance,omitempty"`
	MatchedChannels  []string      `json:"matched_channels"`
	GraphSignals     []GraphSignal `json:"graph_signals,omitempty"`
	GraphFacts       []EntityFact  `json:"graph_facts,omitempty"`
	GraphPaths       []GraphPath   `json:"graph_paths,omitempty"`
	SelectionReason  string        `json:"selection_reason"`
	ConflictRefs     []ConflictRef `json:"conflict_refs,omitempty"`
	// ProtectedPrimary marks a strong direct primary match that graph-only
	// candidates and broad aggregates must not displace during selection.
	ProtectedPrimary bool `json:"-"`
	// KeywordHintMatched marks a keyword candidate that matched the Agent Tool
	// search-hint projection. Hints are auxiliary, so this grants only a bounded
	// selection relevance and never canonical exact-match protection.
	KeywordHintMatched bool `json:"-"`
	// Explain fields are populated only when explain mode is requested.
	RankingFeatures             map[string]float64 `json:"ranking_features,omitempty"`
	GraphScore                  *float64           `json:"graph_score,omitempty"`
	SemanticRawScore            *float64           `json:"semantic_raw_score,omitempty"`
	SemanticPercentile          *float64           `json:"semantic_percentile,omitempty"`
	SemanticRejectionReason     string             `json:"semantic_rejection_reason,omitempty"`
	SemanticCalibrationIdentity string             `json:"semantic_calibration_identity,omitempty"`
	TemporalPrecision           string             `json:"temporal_precision,omitempty"`
	TemporalSourceWording       string             `json:"temporal_source_wording,omitempty"`
	TemporalTimeSource          string             `json:"temporal_time_source,omitempty"`
	QueryTimeStart              *time.Time         `json:"query_time_start,omitempty"`
	QueryTimeEnd                *time.Time         `json:"query_time_end,omitempty"`
	ReferenceTimeAnchor         *time.Time         `json:"reference_time_anchor,omitempty"`
	RankContribution            RankContribution   `json:"rank_contribution,omitempty"`
	KeywordRawBM25              *float64           `json:"keyword_raw_bm25,omitempty"`
	KeywordMatchedFields        []string           `json:"keyword_matched_fields,omitempty"`
	KeywordExactMatch           bool               `json:"keyword_exact_match,omitempty"`
	KeywordTermCoverage         *float64           `json:"keyword_term_coverage,omitempty"`
	FeatureContributions        map[string]float64 `json:"feature_contributions,omitempty"`
}

type RankContribution struct {
	Primary           float64 `json:"primary"`
	GraphBoost        float64 `json:"graph_boost"`
	GraphOnly         float64 `json:"graph_only"`
	Penalty           float64 `json:"penalty"`
	Feedback          float64 `json:"feedback"`
	Final             float64 `json:"final"`
	GraphOnlyAdmitted bool    `json:"graph_only_admitted"`
}

type SearchResponse struct {
	Results                  []SearchResult   `json:"results"`
	Truncated                bool             `json:"truncated"`
	CharsUsed                int              `json:"chars_used"`
	NextCursor               *string          `json:"next_cursor"`
	RequestedChannels        []string         `json:"requested_channels"`
	ExecutedChannels         []string         `json:"executed_channels"`
	Degraded                 bool             `json:"degraded"`
	DegradationReasons       []string         `json:"degradation_reasons"`
	FTSIndexVersion          int              `json:"fts_index_version"`
	SemanticIndexVersion     int              `json:"semantic_index_version"`
	GraphIndexVersion        int64            `json:"graph_index_version"`
	GraphIndexState          string           `json:"graph_index_state"`
	RankingVersion           string           `json:"ranking_version"`
	QueryPlanIdentity        string           `json:"query_plan_identity"`
	RankingIdentity          string           `json:"ranking_identity"`
	ContextIdentity          string           `json:"context_identity"`
	CorpusGeneration         int64            `json:"corpus_generation"`
	FeedbackAggregateVersion int64            `json:"feedback_aggregate_version"`
	FeedbackIdentity         string           `json:"feedback_identity"`
	ResolvedEntities         []ResolvedEntity `json:"resolved_entities,omitempty"`
	ReferenceTime            string           `json:"-"`
	// Context is the structured, budgeted selection built after ranking. It is
	// the default recall output; raw Results remain internal.
	Context ContextPackage `json:"-"`
	// ContextSelectionIdentity is the versioned selection-policy identity.
	ContextSelectionIdentity string `json:"-"`
}

type RecallIntent string

const (
	RecallIntentAuto         RecallIntent = "auto"
	RecallIntentLookup       RecallIntent = "lookup"
	RecallIntentTemporal     RecallIntent = "temporal"
	RecallIntentEntity       RecallIntent = "entity"
	RecallIntentRelational   RecallIntent = "relational"
	RecallIntentMultiHop     RecallIntent = "multi_hop"
	RecallIntentProcedural   RecallIntent = "procedural"
	RecallIntentExploratory  RecallIntent = "exploratory"
	RecallIntentVerification RecallIntent = "verification"
)

func (i RecallIntent) Valid() bool {
	switch i {
	case RecallIntentAuto, RecallIntentLookup, RecallIntentTemporal, RecallIntentEntity,
		RecallIntentRelational, RecallIntentMultiHop, RecallIntentProcedural,
		RecallIntentExploratory, RecallIntentVerification:
		return true
	default:
		return false
	}
}

type RecallExplanation struct {
	MatchedSignals              []string                    `json:"matched_signals"`
	SelectionReason             string                      `json:"selection_reason"`
	Features                    map[string]float64          `json:"features,omitempty"`
	FeatureContributions        map[string]float64          `json:"feature_contributions,omitempty"`
	ContextSelection            ContextSelectionExplanation `json:"context_selection"`
	GraphScore                  *float64                    `json:"graph_score,omitempty"`
	SemanticRawScore            *float64                    `json:"semantic_raw_score,omitempty"`
	SemanticPercentile          *float64                    `json:"semantic_percentile,omitempty"`
	SemanticRejectionReason     string                      `json:"semantic_rejection_reason,omitempty"`
	SemanticCalibrationIdentity string                      `json:"semantic_calibration_identity,omitempty"`
	TemporalPrecision           string                      `json:"temporal_precision,omitempty"`
	TemporalSourceWording       string                      `json:"temporal_source_wording,omitempty"`
	TemporalTimeSource          string                      `json:"temporal_time_source,omitempty"`
	QueryTimeStart              *time.Time                  `json:"query_time_start,omitempty"`
	QueryTimeEnd                *time.Time                  `json:"query_time_end,omitempty"`
	ReferenceTimeAnchor         *time.Time                  `json:"reference_time_anchor,omitempty"`
	RankContribution            RankContribution            `json:"rank_contribution"`
	GraphSignals                []GraphSignal               `json:"graph_signals,omitempty"`
	GraphFacts                  []EntityFact                `json:"graph_facts,omitempty"`
	GraphPaths                  []GraphPath                 `json:"graph_paths,omitempty"`
	KeywordRawBM25              *float64                    `json:"keyword_raw_bm25,omitempty"`
	KeywordMatchedFields        []string                    `json:"keyword_matched_fields,omitempty"`
	KeywordExactMatch           bool                        `json:"keyword_exact_match,omitempty"`
	KeywordTermCoverage         *float64                    `json:"keyword_term_coverage,omitempty"`
}

type ContextItem struct {
	Rank                   int                `json:"rank"`
	MemoryID               string             `json:"memory_id"`
	RevisionID             string             `json:"revision_id"`
	ClusterID              string             `json:"cluster_id,omitempty"`
	Kind                   MemoryKind         `json:"kind"`
	DisplayContent         string             `json:"display_content"`
	EventTimeStart         *time.Time         `json:"event_time_start,omitempty"`
	EventTimeEnd           *time.Time         `json:"event_time_end,omitempty"`
	ValidFrom              *time.Time         `json:"valid_from,omitempty"`
	ValidUntil             *time.Time         `json:"valid_until,omitempty"`
	SourceSpeaker          string             `json:"source_speaker,omitempty"`
	TimePrecision          TimePrecision      `json:"time_precision,omitempty"`
	TimeSource             string             `json:"time_source,omitempty"`
	Evidence               []RevisionEvidence `json:"evidence,omitempty"`
	Confidence             float64            `json:"confidence"`
	LifecycleState         string             `json:"lifecycle_state"`
	EstimatedTokens        int                `json:"estimated_tokens"`
	CorroboratingMemoryIDs []string           `json:"corroborating_memory_ids,omitempty"`
	SupplementaryMemoryIDs []string           `json:"supplementary_memory_ids,omitempty"`
	ProvenanceMemoryIDs    []string           `json:"provenance_memory_ids,omitempty"`
	Utility                float64            `json:"utility,omitempty"`
	Stale                  bool               `json:"stale,omitempty"`
	StaleReasons           []string           `json:"stale_reasons,omitempty"`
	GraphExplanation       string             `json:"graph_explanation,omitempty"`
	Explanation            *RecallExplanation `json:"explanation,omitempty"`
}

type ContextPackage struct {
	Items      []ContextItem              `json:"items"`
	TokensUsed int                        `json:"tokens_used"`
	CharsUsed  int                        `json:"chars_used"`
	Truncated  bool                       `json:"truncated"`
	NextCursor *string                    `json:"next_cursor"`
	Accounting ContextBudgetAccounting    `json:"accounting,omitempty"`
	Rejected   []RejectedContextCandidate `json:"rejected,omitempty"`
}

type RecallExecutionMetadata struct {
	OperationID              string       `json:"operation_id"`
	Intent                   RecallIntent `json:"intent"`
	ReferenceTime            string       `json:"reference_time"`
	RequestedCapabilities    []string     `json:"requested_capabilities"`
	ExecutedCapabilities     []string     `json:"executed_capabilities"`
	Degraded                 bool         `json:"degraded"`
	DegradationReasons       []string     `json:"degradation_reasons"`
	QueryPlanIdentity        string       `json:"query_plan_identity"`
	RankingIdentity          string       `json:"ranking_identity"`
	ContextSelectionIdentity string       `json:"context_selection_identity"`
	RankingVersion           string       `json:"ranking_version"`
	FeedbackIdentity         string       `json:"feedback_identity"`
	FeedbackAggregateVersion int64        `json:"feedback_aggregate_version"`
	FTSIndexVersion          int          `json:"fts_index_version"`
	SemanticIndexVersion     int          `json:"semantic_index_version"`
	GraphIndexVersion        int64        `json:"graph_index_version"`
	GraphIndexState          string       `json:"graph_index_state"`
}

type RecallResponse struct {
	Context   ContextPackage          `json:"context"`
	Execution RecallExecutionMetadata `json:"execution"`
}

// FeedbackDecisionContext is the privacy-safe, identity-bound retrieval or
// answer decision that produced a result. It deliberately excludes query text
// and unpermitted entity identifiers.
type FeedbackDecisionContext struct {
	SearchOperationID string   `json:"search_operation_id"`
	QueryHash         string   `json:"query_hash"`
	QueryType         string   `json:"query_type"`
	EntityIDs         []string `json:"entity_ids,omitempty"`
	RankingIdentity   string   `json:"ranking_identity"`
	FeatureIdentity   string   `json:"feature_identity"`
	ContextIdentity   string   `json:"context_identity"`
	ResultRank        int      `json:"result_rank"`
	SelectionState    string   `json:"selection_state"`
	MatchedSignals    []string `json:"matched_signals"`
	AnswerIdentity    string   `json:"answer_identity,omitempty"`
}

type FeedbackEvent struct {
	FeedbackID   string                   `json:"feedback_id"`
	OperationID  string                   `json:"operation_id"`
	CallerID     string                   `json:"caller_id"`
	MemoryID     string                   `json:"memory_id"`
	RevisionID   string                   `json:"revision_id"`
	Purpose      string                   `json:"feedback_purpose"`
	FeedbackType string                   `json:"feedback_type"`
	ReasonCode   string                   `json:"reason_code"`
	Note         *string                  `json:"note,omitempty"`
	Decision     *FeedbackDecisionContext `json:"decision,omitempty"`
	CreatedAt    string                   `json:"created_at"`
}

type ProposalOutcome struct {
	Status     string  `json:"status"`
	ProposalID *string `json:"proposal_id,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

type FeedbackAppendResponse struct {
	Feedback FeedbackEvent   `json:"feedback"`
	Proposal ProposalOutcome `json:"proposal"`
}

type FeedbackPage struct {
	Items      []FeedbackEvent `json:"items"`
	NextCursor *string         `json:"next_cursor,omitempty"`
}

type FeedbackAggregate struct {
	RevisionID        string  `json:"revision_id"`
	AlgorithmVersion  string  `json:"algorithm_version"`
	Generation        int64   `json:"generation"`
	PositiveCount     int     `json:"positive_count"`
	NegativeCount     int     `json:"negative_count"`
	BoundedUsageScore float64 `json:"bounded_usage_score"`
	WindowStart       string  `json:"window_start"`
	WindowEnd         string  `json:"window_end"`
	SourceWatermark   *string `json:"source_watermark,omitempty"`
	State             string  `json:"state"`
	UpdatedAt         string  `json:"updated_at"`
}

type FeedbackAggregateHealth struct {
	State            string  `json:"state"`
	AlgorithmVersion string  `json:"algorithm_version"`
	Generation       int64   `json:"generation"`
	SourceWatermark  *string `json:"source_watermark,omitempty"`
	WindowDays       int     `json:"window_days"`
	MinimumSupport   int     `json:"minimum_support"`
}

type MemoryStateEvent struct {
	StateEventID string  `json:"state_event_id"`
	MemoryID     string  `json:"memory_id"`
	RevisionID   string  `json:"revision_id"`
	OperationID  string  `json:"operation_id"`
	FromState    *string `json:"from_state,omitempty"`
	ToState      string  `json:"to_state"`
	FromFrozen   *bool   `json:"from_frozen,omitempty"`
	ToFrozen     bool    `json:"to_frozen"`
	Reason       string  `json:"reason"`
	CreatedBy    string  `json:"created_by"`
	CreatedAt    string  `json:"created_at"`
}

type Runtime struct {
	Now func() time.Time
	ID  func() (string, error)
}
