package daemon

import (
	"context"
	"net/http"
	"testing"

	"github.com/N0rthHXD/dMem/internal/application"
	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestEventStreamSequenceAndSourceStreamAPI(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	root := createSpace(t, client, "stream-root", map[string]any{"space_type": "user", "external_id": "owner", "display_name": "Owner"})

	ingest := func(key, text string, extra map[string]any) domain.RawEvent {
		body := map[string]any{
			"space_id": root.SpaceID, "source_type": "message", "source_name": "chat", "occurred_at": "2026-08-21T00:00:00Z",
			"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": text},
		}
		for k, v := range extra {
			body[k] = v
		}
		response := requestJSON(t, client, http.MethodPost, "/v1/events", key, body)
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("ingest %s status = %d", key, response.StatusCode)
		}
		var accepted domain.IngestResponse
		decodeResponse(t, response, &accepted)
		response = requestJSON(t, client, http.MethodGet, "/v1/events/"+accepted.EventID, "", nil)
		var event domain.RawEvent
		decodeResponse(t, response, &event)
		return event
	}

	first := ingest("stream-first", "first", map[string]any{"source_stream_key": "chat-1"})
	second := ingest("stream-second", "second", map[string]any{"source_stream_key": "chat-1"})
	if first.Sequence == nil || *first.Sequence != 0 {
		t.Fatalf("first sequence = %v; want 0", first.Sequence)
	}
	if second.Sequence == nil || *second.Sequence != 1 {
		t.Fatalf("second sequence = %v; want 1", second.Sequence)
	}
	if first.SourceStreamID == nil || second.SourceStreamID == nil || *first.SourceStreamID != *second.SourceStreamID {
		t.Fatalf("stream identity = %v / %v", first.SourceStreamID, second.SourceStreamID)
	}

	supplied := ingest("stream-supplied", "supplied", map[string]any{"source_stream_key": "chat-1", "sequence": 9})
	if supplied.Sequence == nil || *supplied.Sequence != 9 {
		t.Fatalf("supplied sequence = %v; want 9", supplied.Sequence)
	}

	body := map[string]any{
		"space_id": root.SpaceID, "source_type": "message", "source_name": "chat", "occurred_at": "2026-08-21T00:00:00Z",
		"source_stream_key": "chat-1", "sequence": -1,
		"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "negative"},
	}
	response := requestJSON(t, client, http.MethodPost, "/v1/events", "stream-negative", body)
	assertError(t, response, http.StatusUnprocessableEntity, "invalid_request")

	rememberBody := map[string]any{
		"space_id": root.SpaceID, "content": "remembered with stream", "kind": "fact",
		"source_name": "agent", "source_stream_key": "remember-stream", "sequence": 3,
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/remember", "stream-remember", rememberBody)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("remember status = %d", response.StatusCode)
	}
	var remembered application.RememberResult
	decodeResponse(t, response, &remembered)
	response = requestJSON(t, client, http.MethodGet, "/v1/events/"+remembered.EventID, "", nil)
	var event domain.RawEvent
	decodeResponse(t, response, &event)
	if event.Sequence == nil || *event.Sequence != 3 || event.SourceStreamID == nil {
		t.Fatalf("remembered event = %+v; want sequence 3 with a stream", event)
	}
}
