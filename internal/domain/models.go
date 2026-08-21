package domain

import (
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
	JobID             string    `json:"job_id"`
	OperationID       string    `json:"operation_id"`
	JobType           string    `json:"job_type"`
	TargetType        string    `json:"target_type"`
	TargetID          string    `json:"target_id"`
	Status            string    `json:"status"`
	AttemptCount      int       `json:"attempt_count"`
	MaxAttempts       int       `json:"max_attempts"`
	ResultCode        *string   `json:"result_code"`
	LastErrorCode     *string   `json:"last_error_code"`
	CreatedAt         string    `json:"created_at"`
	StartedAt         *string   `json:"started_at"`
	FinishedAt        *string   `json:"finished_at"`
	ProcessingVersion string    `json:"-"`
	Priority          int       `json:"-"`
	NextRunAt         string    `json:"-"`
	CancelRequestedAt *string   `json:"-"`
	Result            any       `json:"-"`
	LastError         *JobError `json:"-"`
	ExpiresAt         *string   `json:"-"`
}

type MemoryEvidence struct {
	EventID      string `json:"event_id"`
	EvidenceType string `json:"evidence_type"`
	StartOffset  *int64 `json:"start_offset,omitempty"`
	EndOffset    *int64 `json:"end_offset,omitempty"`
}

type MemoryRevision struct {
	RevisionID       string           `json:"revision_id"`
	MemoryID         string           `json:"memory_id"`
	ParentRevisionID *string          `json:"parent_revision_id,omitempty"`
	Content          string           `json:"content"`
	ValidFrom        *string          `json:"valid_from,omitempty"`
	ValidUntil       *string          `json:"valid_until,omitempty"`
	CreatedBy        string           `json:"created_by"`
	CreatedAt        string           `json:"created_at"`
	Evidence         []MemoryEvidence `json:"evidence,omitempty"`
}

type Memory struct {
	MemoryID        string         `json:"memory_id"`
	SpaceID         string         `json:"space_id"`
	MemoryType      string         `json:"memory_type"`
	CurrentState    string         `json:"current_state"`
	Frozen          bool           `json:"frozen"`
	Sensitivity     string         `json:"sensitivity,omitempty"`
	CurrentRevision MemoryRevision `json:"current_revision"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
}

type MemoryPage struct {
	Items      []Memory `json:"items"`
	NextCursor *string  `json:"next_cursor,omitempty"`
}

type SearchResult struct {
	Memory          Memory  `json:"memory"`
	Rank            int     `json:"rank"`
	KeywordScore    float64 `json:"keyword_score"`
	SelectionReason string  `json:"selection_reason"`
}

type SearchResponse struct {
	Results    []SearchResult `json:"results"`
	Truncated  bool           `json:"truncated"`
	CharsUsed  int            `json:"chars_used"`
	IndexState string         `json:"index_state"`
	NextCursor *string        `json:"next_cursor"`
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
