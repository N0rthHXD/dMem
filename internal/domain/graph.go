package domain

import "time"

// Graph extraction is the production step that turns a confirmed memory
// revision into entity / alias / mention / entity-fact / memory-relation
// proposals. The processor receives a stable revision snapshot and returns a
// validated, deduplicated candidate set that storage then resolves, classifies
// by risk, and either auto-confirms or queues for review.

type GraphExtractionPolicy struct {
	MaxEntities         int
	MaxFacts            int
	MaxRelations        int
	MaxAliasesPerEntity int
	MaxContentChars     int
}

type GraphExtractionInput struct {
	ProcessorIdentity string
	ProcessingVersion string
	JobID             string
	Attempt           int
	SpaceID           string
	MemoryID          string
	RevisionID        string
	Content           string
	Sensitivity       string
	CurrentState      string
	ValidFrom         *time.Time
	ValidUntil        *time.Time
	ContextMemories   []ProcessingMemory
	Policy            GraphExtractionPolicy
}

type GraphEntity struct {
	LocalID       string
	CanonicalName string
	EntityType    string
	Aliases       []string
	Confidence    float64
}

type GraphMention struct {
	EntityLocalID string
	Text          string
	StartRune     int
	EndRune       int
	Confidence    float64
}

type GraphFact struct {
	SubjectLocalID    string
	Predicate         string
	ObjectLocalID     string
	ObjectLiteral     string
	ObjectLiteralType string
	Confidence        float64
	ValidFrom         *time.Time
	ValidUntil        *time.Time
}

type GraphMemoryRelation struct {
	RelationType     string
	TargetMemoryID   string
	TargetRevisionID string
	Confidence       float64
	Evidence         []GraphRelationEvidence
}

type GraphRelationEvidence struct {
	StartRune int
	EndRune   int
}

type GraphExtractionOutcome string

const (
	GraphExtractionNoProcessor      GraphExtractionOutcome = "no_processor"
	GraphExtractionEmpty            GraphExtractionOutcome = "empty"
	GraphExtractionExtracted        GraphExtractionOutcome = "extracted"
	GraphExtractionTransientFailure GraphExtractionOutcome = "transient_failure"
	GraphExtractionPermanentFailure GraphExtractionOutcome = "permanent_failure"
	GraphExtractionInvalidOutput    GraphExtractionOutcome = "invalid_output"
	GraphExtractionPolicyRejected   GraphExtractionOutcome = "policy_rejected"
)

type GraphExtractionResult struct {
	Code              GraphExtractionOutcome
	Entities          []GraphEntity
	Mentions          []GraphMention
	Facts             []GraphFact
	Relations         []GraphMemoryRelation
	Message           string
	ReasonCode        string
	ProviderIdentity  string
	ModelIdentity     string
	ProviderRequestID string
	Usage             ProcessingUsage
}

// EntityFact is an authoritative structured fact between two entities (or an
// entity and a controlled literal). It is distinct from revision-level memory
// relations: entity facts describe typed relationships among resolved entities.
type EntityFact struct {
	FactID            string                 `json:"fact_id"`
	SpaceID           string                 `json:"space_id"`
	SubjectEntityID   string                 `json:"subject_entity_id"`
	Predicate         string                 `json:"predicate"`
	ObjectEntityID    *string                `json:"object_entity_id,omitempty"`
	ObjectLiteral     *string                `json:"object_literal,omitempty"`
	ObjectLiteralType *string                `json:"object_literal_type,omitempty"`
	SourceMemoryID    string                 `json:"source_memory_id"`
	SourceRevisionID  string                 `json:"source_revision_id"`
	Confidence        *float64               `json:"confidence,omitempty"`
	State             string                 `json:"state"`
	Source            string                 `json:"source"`
	SpecificationHash string                 `json:"specification_hash"`
	CreatedBy         string                 `json:"created_by"`
	ProcessingVersion string                 `json:"processing_version"`
	CreatedAt         string                 `json:"created_at"`
	UpdatedAt         string                 `json:"updated_at"`
	ValidFrom         *string                `json:"valid_from,omitempty"`
	ValidUntil        *string                `json:"valid_until,omitempty"`
	Evidence          []EntityFactEvidence   `json:"evidence"`
	StateEvents       []EntityFactStateEvent `json:"state_events"`
}

type EntityFactEvidence struct {
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

type EntityFactStateEvent struct {
	StateEventID string  `json:"state_event_id"`
	OperationID  string  `json:"operation_id"`
	FromState    *string `json:"from_state,omitempty"`
	ToState      string  `json:"to_state"`
	Reason       string  `json:"reason"`
	CreatedBy    string  `json:"created_by"`
	CreatedAt    string  `json:"created_at"`
}

type EntityFactPage struct {
	Items      []EntityFact `json:"items"`
	NextCursor *string      `json:"next_cursor,omitempty"`
	IndexState string       `json:"index_state,omitempty"`
	Degraded   bool         `json:"degraded,omitempty"`
}

type FactProposalReview struct {
	ReviewID   string  `json:"review_id"`
	FromState  string  `json:"from_state"`
	ToState    string  `json:"to_state"`
	ReasonCode string  `json:"reason_code"`
	Note       *string `json:"note,omitempty"`
	ReviewerID string  `json:"reviewer_id"`
	FactID     *string `json:"fact_id,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

type FactProposal struct {
	ProposalID        string               `json:"proposal_id"`
	EventID           string               `json:"event_id"`
	SpaceID           string               `json:"space_id"`
	SubjectEntityID   string               `json:"subject_entity_id"`
	Predicate         string               `json:"predicate"`
	ObjectEntityID    *string              `json:"object_entity_id,omitempty"`
	ObjectLiteral     *string              `json:"object_literal,omitempty"`
	ObjectLiteralType *string              `json:"object_literal_type,omitempty"`
	SourceMemoryID    string               `json:"source_memory_id"`
	SourceRevisionID  string               `json:"source_revision_id"`
	Confidence        *float64             `json:"confidence,omitempty"`
	ValidFrom         *string              `json:"valid_from,omitempty"`
	ValidUntil        *string              `json:"valid_until,omitempty"`
	Evidence          []EntityFactEvidence `json:"evidence"`
	ProcessorIdentity string               `json:"processor_identity"`
	ProviderIdentity  *string              `json:"provider_identity,omitempty"`
	ModelIdentity     *string              `json:"model_identity,omitempty"`
	ProcessingVersion string               `json:"processing_version"`
	SpecificationHash string               `json:"specification_hash"`
	ReviewState       string               `json:"review_state"`
	AutoReviewVersion *string              `json:"auto_review_version,omitempty"`
	ResultFactID      *string              `json:"result_fact_id,omitempty"`
	CreatedAt         string               `json:"created_at"`
	ReviewedAt        *string              `json:"reviewed_at,omitempty"`
	Reviews           []FactProposalReview `json:"reviews"`
}

type FactProposalPage struct {
	Items      []FactProposal `json:"items"`
	NextCursor *string        `json:"next_cursor,omitempty"`
}

// ResolvedEntity is the output of query understanding: an alias mention in a
// natural-language query mapped to a known entity with a deterministic confidence.
type ResolvedEntity struct {
	EntityID   string  `json:"entity_id"`
	EntityType string  `json:"entity_type"`
	Alias      string  `json:"alias"`
	Confidence float64 `json:"confidence"`
	MatchKind  string  `json:"match_kind"`
	StartRune  int     `json:"start_rune"`
	EndRune    int     `json:"end_rune"`
}

// GraphSignal describes how a memory revision was recalled through the graph
// channel, preserving explainability of the traversal that produced it.
type GraphSignal struct {
	Kind              string  `json:"kind"`
	EntityID          string  `json:"entity_id,omitempty"`
	Predicate         string  `json:"predicate,omitempty"`
	FactID            string  `json:"fact_id,omitempty"`
	RelationID        string  `json:"relation_id,omitempty"`
	RelationType      string  `json:"relation_type,omitempty"`
	RelationDirection string  `json:"relation_direction,omitempty"`
	Hop               int     `json:"hop"`
	EntityConfidence  float64 `json:"entity_confidence,omitempty"`
	FactConfidence    float64 `json:"fact_confidence,omitempty"`
}

// GraphPath is a bounded traversal path through entity facts.
type GraphPath struct {
	SubjectEntityID string   `json:"subject_entity_id"`
	Steps           []string `json:"steps"`
	ObjectEntityID  string   `json:"object_entity_id"`
	Hop             int      `json:"hop"`
}
