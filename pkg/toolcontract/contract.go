// Package toolcontract exposes the versioned Agent Tool Route wire contract.
package toolcontract

import "encoding/json"

const Version = "dmem-agent-tool-v1"

const (
	ToolRecall   = "dmem_recall"
	ToolRemember = "dmem_remember"
	ToolFeedback = "dmem_feedback"
	ToolForget   = "dmem_forget"
)

const (
	SchemaCapabilities   = "capabilities"
	SchemaError          = "error"
	SchemaRecallInput    = "recall-input"
	SchemaRecallOutput   = "recall-output"
	SchemaRememberInput  = "remember-input"
	SchemaRememberOutput = "remember-output"
	SchemaFeedbackInput  = "feedback-input"
	SchemaFeedbackOutput = "feedback-output"
	SchemaForgetInput    = "forget-input"
	SchemaForgetOutput   = "forget-output"
)

type Capabilities struct {
	ContractVersion          string   `json:"contract_version"`
	Tools                    []string `json:"tools"`
	PredicateRegistryVersion string   `json:"predicate_registry_version"`
	EvidenceDisclosure       string   `json:"evidence_disclosure"`
}

func V1Capabilities() Capabilities {
	return Capabilities{
		ContractVersion:          Version,
		Tools:                    []string{ToolRecall, ToolRemember, ToolFeedback, ToolForget},
		PredicateRegistryVersion: "predicate-registry-v2",
		EvidenceDisclosure:       "references_only",
	}
}

type RecallInput struct {
	Query         string   `json:"query"`
	ScopeIDs      []string `json:"scope_ids"`
	Intent        string   `json:"intent,omitempty"`
	Entities      []string `json:"entities,omitempty"`
	ReferenceTime *string  `json:"reference_time,omitempty"`
	Limit         *int     `json:"limit,omitempty"`
	Cursor        string   `json:"cursor,omitempty"`
}

type EvidenceReference struct {
	EvidenceID    string  `json:"evidence_id"`
	EventID       string  `json:"event_id"`
	Role          string  `json:"role"`
	EventTime     *string `json:"event_time,omitempty"`
	EventSequence *int64  `json:"event_sequence,omitempty"`
	StartOffset   *int64  `json:"start_offset,omitempty"`
	EndOffset     *int64  `json:"end_offset,omitempty"`
}

type RecalledMemory struct {
	MemoryID   string              `json:"memory_id"`
	RevisionID string              `json:"revision_id"`
	Kind       string              `json:"kind"`
	Content    string              `json:"content"`
	ValidFrom  *string             `json:"valid_from,omitempty"`
	ValidUntil *string             `json:"valid_until,omitempty"`
	Confidence float64             `json:"confidence"`
	Evidence   []EvidenceReference `json:"evidence"`
}

type RecallOutput struct {
	Memories             []RecalledMemory `json:"memories"`
	DegradedCapabilities []string         `json:"degraded_capabilities"`
	NextCursor           *string          `json:"next_cursor"`
}

type RememberInput struct {
	ScopeID   string               `json:"scope_id"`
	Source    Source               `json:"source"`
	Memories  []MemorySuggestion   `json:"memories"`
	Entities  []EntitySuggestion   `json:"entities,omitempty"`
	Facts     []FactSuggestion     `json:"facts,omitempty"`
	Relations []RelationSuggestion `json:"relations,omitempty"`
}

type Source struct {
	Type           string         `json:"type"`
	Name           string         `json:"name"`
	ConversationID string         `json:"conversation_id"`
	TurnID         string         `json:"turn_id"`
	ToolCallID     string         `json:"tool_call_id,omitempty"`
	OccurredAt     string         `json:"occurred_at"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type MemorySuggestion struct {
	LocalID     string   `json:"local_id"`
	Kind        string   `json:"kind"`
	Content     string   `json:"content"`
	Sensitivity string   `json:"sensitivity"`
	Confidence  float64  `json:"confidence"`
	SearchHints []string `json:"search_hints,omitempty"`
	ValidFrom   *string  `json:"valid_from,omitempty"`
	ValidUntil  *string  `json:"valid_until,omitempty"`
}

type EntitySuggestion struct {
	LocalID    string   `json:"local_id"`
	Type       string   `json:"type"`
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases,omitempty"`
	Confidence float64  `json:"confidence"`
}

type EntityReference struct {
	LocalID  string `json:"local_id,omitempty"`
	EntityID string `json:"entity_id,omitempty"`
}

type FactObject struct {
	Entity       *EntityReference `json:"entity,omitempty"`
	StringValue  *string          `json:"string,omitempty"`
	NumberValue  *json.Number     `json:"number,omitempty"`
	BooleanValue *bool            `json:"boolean,omitempty"`
}

type FactSuggestion struct {
	LocalID        string          `json:"local_id"`
	Subject        EntityReference `json:"subject"`
	Predicate      string          `json:"predicate"`
	Object         FactObject      `json:"object"`
	EvidenceMemory string          `json:"evidence_memory"`
	Confidence     float64         `json:"confidence"`
	ValidFrom      *string         `json:"valid_from,omitempty"`
	ValidUntil     *string         `json:"valid_until,omitempty"`
}

type MemoryReference struct {
	LocalID    string `json:"local_id,omitempty"`
	MemoryID   string `json:"memory_id,omitempty"`
	RevisionID string `json:"revision_id,omitempty"`
}

type RelationSuggestion struct {
	LocalID        string          `json:"local_id"`
	Type           string          `json:"type"`
	Source         MemoryReference `json:"source"`
	Target         MemoryReference `json:"target"`
	EvidenceMemory string          `json:"evidence_memory"`
	Confidence     float64         `json:"confidence"`
}

type WriteMetadata struct {
	OperationID string `json:"operation_id"`
	Replayed    bool   `json:"replayed"`
}

type CommitOutcome struct {
	LocalID    string `json:"local_id"`
	Status     string `json:"status"`
	MemoryID   string `json:"memory_id,omitempty"`
	RevisionID string `json:"revision_id,omitempty"`
	ProposalID string `json:"proposal_id,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
}

type RememberOutput struct {
	WriteMetadata
	SourceEventID string          `json:"source_event_id"`
	Memories      []CommitOutcome `json:"memories"`
	Entities      []CommitOutcome `json:"entities"`
	Facts         []CommitOutcome `json:"facts"`
	Relations     []CommitOutcome `json:"relations"`
}

type FeedbackInput struct {
	ScopeID           string `json:"scope_id"`
	MemoryID          string `json:"memory_id"`
	RevisionID        string `json:"revision_id"`
	Kind              string `json:"kind"`
	Reason            string `json:"reason,omitempty"`
	RecallOperationID string `json:"recall_operation_id,omitempty"`
}

type FeedbackOutput struct {
	WriteMetadata
	FeedbackID string `json:"feedback_id"`
	MemoryID   string `json:"memory_id"`
	RevisionID string `json:"revision_id"`
	Kind       string `json:"kind"`
}

type ForgetInput struct {
	ScopeID            string `json:"scope_id"`
	MemoryID           string `json:"memory_id"`
	ExpectedRevisionID string `json:"expected_revision_id"`
	Reason             string `json:"reason"`
}

type ForgetOutput struct {
	WriteMetadata
	MemoryID   string `json:"memory_id"`
	RevisionID string `json:"revision_id"`
	State      string `json:"state"`
}

type ErrorResponse struct {
	Error ContractError `json:"error"`
}

type ContractError struct {
	Code         string            `json:"code"`
	Message      string            `json:"message"`
	RequestID    string            `json:"request_id"`
	Retryable    bool              `json:"retryable"`
	RetryAfterMS *int64            `json:"retry_after_ms,omitempty"`
	Details      map[string]string `json:"details,omitempty"`
}
