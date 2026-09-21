package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
)

// eventIngestInput is the Provider Route raw-event ingestion body. It is a
// protocol-local DTO: raw event ingestion is not part of the versioned Agent
// Tool contract, so it does not share the tool schemas.
type eventIngestInput struct {
	ScopeID string            `json:"scope_id"`
	Source  eventSourceInput  `json:"source"`
	Content eventContentInput `json:"content"`
}

type eventSourceInput struct {
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	EventID    *string        `json:"event_id,omitempty"`
	StreamKey  string         `json:"stream_key,omitempty"`
	Sequence   *int64         `json:"sequence,omitempty"`
	OccurredAt string         `json:"occurred_at,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type eventContentInput struct {
	Kind         string          `json:"kind"`
	MediaType    string          `json:"media_type"`
	Text         string          `json:"text,omitempty"`
	Value        json.RawMessage `json:"value,omitempty"`
	ReferenceURI string          `json:"reference_uri,omitempty"`
	DeclaredSize *int64          `json:"declared_size,omitempty"`
	DeclaredHash string          `json:"declared_hash,omitempty"`
}

type eventOutput struct {
	OperationID string   `json:"operation_id"`
	EventID     string   `json:"event_id"`
	Status      string   `json:"status"`
	JobIDs      []string `json:"job_ids"`
	AcceptedAt  string   `json:"accepted_at"`
	Replayed    bool     `json:"replayed"`
}

type eventStatusOutput struct {
	EventID          string         `json:"event_id"`
	OperationID      string         `json:"operation_id"`
	ScopeID          string         `json:"scope_id"`
	SourceType       string         `json:"source_type"`
	SourceName       string         `json:"source_name"`
	SourceEventID    *string        `json:"source_event_id,omitempty"`
	OccurredAt       string         `json:"occurred_at"`
	ReceivedAt       string         `json:"received_at"`
	ProcessingStatus string         `json:"processing_status"`
	Content          domain.Content `json:"content"`
}

func (h *Handler) handleIngestEvent(ex *exchange, _ routeParams) {
	var input eventIngestInput
	if err := ex.decodeBody(&input); err != nil {
		ex.writeError(err)
		return
	}
	content, err := eventContent(input.Content)
	if err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.events.Ingest(ex.context(), ex.call, extraction.IngestCommand{
		SpaceID: input.ScopeID, SourceType: input.Source.Type, SourceName: input.Source.Name,
		SourceEventID: input.Source.EventID, SourceStreamKey: input.Source.StreamKey,
		Sequence: input.Source.Sequence, OccurredAt: input.Source.OccurredAt,
		Content: content, Metadata: input.Source.Metadata,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	jobIDs := result.JobIDs
	if jobIDs == nil {
		jobIDs = []string{}
	}
	ex.writeJSON(http.StatusAccepted, eventOutput{
		OperationID: result.OperationID, EventID: result.EventID, Status: result.Status,
		JobIDs: jobIDs, AcceptedAt: result.AcceptedAt, Replayed: result.Replay,
	})
}

func (h *Handler) handleGetEvent(ex *exchange, params routeParams) {
	eventID, err := requirePathID(params, "event_id")
	if err != nil {
		ex.writeError(err)
		return
	}
	event, err := h.events.GetEvent(ex.context(), ex.call, extraction.GetEventQuery{EventID: eventID})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.writeJSON(http.StatusOK, eventStatusOutput{
		EventID: event.EventID, OperationID: event.OperationID, ScopeID: event.SpaceID,
		SourceType: event.SourceType, SourceName: event.SourceName, SourceEventID: event.SourceEventID,
		OccurredAt: event.OccurredAt, ReceivedAt: event.ReceivedAt,
		ProcessingStatus: event.ProcessingStatus, Content: event.Content,
	})
}

// eventContent converts the wire content envelope into the domain value. The
// gateway revalidates the shape, so this only decodes the embedded JSON value.
func eventContent(input eventContentInput) (domain.Content, error) {
	content := domain.Content{
		Kind: input.Kind, MediaType: input.MediaType, Text: input.Text,
		ReferenceURI: input.ReferenceURI, DeclaredSize: input.DeclaredSize, DeclaredHash: input.DeclaredHash,
	}
	if len(input.Value) > 0 {
		value, err := decodeRawValue(input.Value)
		if err != nil {
			return domain.Content{}, err
		}
		content.Value = value
	}
	return content, nil
}
