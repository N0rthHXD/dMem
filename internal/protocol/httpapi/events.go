package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
)

// eventIngestRequest is the wire representation of a raw-event ingress command.
type eventIngestRequest struct {
	ScopeID         string         `json:"scope_id"`
	SourceType      string         `json:"source_type"`
	SourceName      string         `json:"source_name"`
	SourceEventID   *string        `json:"source_event_id,omitempty"`
	SourceStreamKey string         `json:"source_stream_key,omitempty"`
	Sequence        *int64         `json:"sequence,omitempty"`
	OccurredAt      string         `json:"occurred_at,omitempty"`
	Content         contentRequest `json:"content"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// contentRequest is the wire representation of raw event content. Presence of
// the optional text and value members is tracked so the adapter can faithfully
// convert representation into the domain value; content-kind rules are enforced
// by the Extraction gateway module.
type contentRequest struct {
	Kind         string          `json:"kind"`
	MediaType    string          `json:"media_type"`
	Text         *string         `json:"text,omitempty"`
	Value        json.RawMessage `json:"value,omitempty"`
	ReferenceURI string          `json:"reference_uri,omitempty"`
	DeclaredSize *int64          `json:"declared_size,omitempty"`
	DeclaredHash string          `json:"declared_hash,omitempty"`
}

// toDomain converts the wire content into the protocol-neutral domain value.
func (c contentRequest) toDomain() (domain.Content, error) {
	content := domain.Content{
		Kind:         c.Kind,
		MediaType:    c.MediaType,
		ReferenceURI: c.ReferenceURI,
		DeclaredSize: c.DeclaredSize,
		DeclaredHash: c.DeclaredHash,
	}
	if c.Text != nil {
		content.Text = *c.Text
	}
	value, err := decodeRawValue(c.Value)
	if err != nil {
		return domain.Content{}, err
	}
	content.Value = value
	return content, nil
}

// eventResource is the public representation of a raw event. It renames the
// storage-era space field to the public scope terminology.
type eventResource struct {
	EventID          string         `json:"event_id"`
	OperationID      string         `json:"operation_id"`
	CallerID         string         `json:"caller_id"`
	ScopeID          string         `json:"scope_id"`
	SourceType       string         `json:"source_type"`
	SourceName       string         `json:"source_name"`
	SourceEventID    *string        `json:"source_event_id,omitempty"`
	SourceStreamID   *string        `json:"source_stream_id,omitempty"`
	Sequence         *int64         `json:"sequence,omitempty"`
	OccurredAt       string         `json:"occurred_at"`
	ReceivedAt       string         `json:"received_at"`
	Content          domain.Content `json:"content"`
	Metadata         map[string]any `json:"metadata"`
	ProcessingStatus string         `json:"processing_status"`
}

// newEventResource converts a domain raw event into its public representation.
func newEventResource(value domain.RawEvent) eventResource {
	return eventResource{
		EventID:          value.EventID,
		OperationID:      value.OperationID,
		CallerID:         value.CallerID,
		ScopeID:          value.SpaceID,
		SourceType:       value.SourceType,
		SourceName:       value.SourceName,
		SourceEventID:    value.SourceEventID,
		SourceStreamID:   value.SourceStreamID,
		Sequence:         value.Sequence,
		OccurredAt:       value.OccurredAt,
		ReceivedAt:       value.ReceivedAt,
		Content:          value.Content,
		Metadata:         value.Metadata,
		ProcessingStatus: value.ProcessingStatus,
	}
}

// eventIngestResponse is the accepted-event envelope.
type eventIngestResponse struct {
	OperationID string   `json:"operation_id"`
	Replayed    bool     `json:"replayed"`
	EventID     string   `json:"event_id"`
	Status      string   `json:"status"`
	JobIDs      []string `json:"job_ids"`
	AcceptedAt  string   `json:"accepted_at"`
}

// eventGetResponse is the single-event read envelope.
type eventGetResponse struct {
	Event eventResource `json:"event"`
}

// handleIngestEvent serves POST /v1/events.
func (h *Handler) handleIngestEvent(ex *exchange, _ routeParams) {
	var request eventIngestRequest
	if err := ex.decodeBody(&request); err != nil {
		ex.writeError(err)
		return
	}
	content, err := request.Content.toDomain()
	if err != nil {
		ex.writeError(err)
		return
	}
	result, err := h.events.Ingest(ex.context(), ex.call, extraction.IngestCommand{
		SpaceID:         request.ScopeID,
		SourceType:      request.SourceType,
		SourceName:      request.SourceName,
		SourceEventID:   request.SourceEventID,
		SourceStreamKey: request.SourceStreamKey,
		Sequence:        request.Sequence,
		OccurredAt:      request.OccurredAt,
		Content:         content,
		Metadata:        request.Metadata,
	})
	if err != nil {
		ex.writeError(err)
		return
	}
	ex.setReplay(result.Replay)
	ex.writeJSON(http.StatusAccepted, eventIngestResponse{
		OperationID: result.OperationID,
		Replayed:    result.Replay,
		EventID:     result.EventID,
		Status:      result.Status,
		JobIDs:      result.JobIDs,
		AcceptedAt:  result.AcceptedAt,
	})
}

// handleGetEvent serves GET /v1/events/{event_id}.
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
	ex.writeJSON(http.StatusOK, eventGetResponse{Event: newEventResource(event)})
}
