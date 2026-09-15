package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func streamIngest(t *testing.T, repository *Repository, key string, params IngestParams) domain.IngestResponse {
	t.Helper()
	result, replay, err := repository.IngestEvent(context.Background(), memoryIdentity(key, "/v1/events", params), params)
	if err != nil || replay {
		t.Fatalf("IngestEvent(%q) replay=%t error=%v", key, replay, err)
	}
	return result
}

func ingestParams(spaceID, streamKey, text, occurredAt string, sequence *int64) IngestParams {
	return IngestParams{
		SpaceID:         spaceID,
		SourceType:      "message",
		SourceName:      "chat",
		SourceStreamKey: streamKey,
		OccurredAt:      occurredAt,
		Sequence:        sequence,
		Content:         domain.Content{Kind: "text", MediaType: "text/plain", Text: text},
	}
}

func requireSequence(t *testing.T, repository *Repository, eventID string, want int64) domain.RawEvent {
	t.Helper()
	event, err := repository.GetEvent(context.Background(), eventID)
	if err != nil {
		t.Fatalf("GetEvent(%s) error = %v", eventID, err)
	}
	if event.Sequence == nil || *event.Sequence != want {
		t.Fatalf("event %s sequence = %v; want %d", eventID, event.Sequence, want)
	}
	return event
}

func TestIngestEventAssignsTransactionalSequencePerSourceStream(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	occurredAt := "2026-08-21T11:00:00Z"

	first := streamIngest(t, repository, "stream-first", ingestParams("space-1", "chat-1", "first", occurredAt, nil))
	second := streamIngest(t, repository, "stream-second", ingestParams("space-1", "chat-1", "second", occurredAt, nil))
	other := streamIngest(t, repository, "stream-other", ingestParams("space-1", "chat-2", "other", occurredAt, nil))

	firstEvent := requireSequence(t, repository, first.EventID, 0)
	secondEvent := requireSequence(t, repository, second.EventID, 1)
	otherEvent := requireSequence(t, repository, other.EventID, 0)

	if firstEvent.SourceStreamID == nil || secondEvent.SourceStreamID == nil {
		t.Fatal("stream events are missing a source stream identity")
	}
	if *firstEvent.SourceStreamID != *secondEvent.SourceStreamID {
		t.Fatalf("same stream key produced different stream ids: %s vs %s", *firstEvent.SourceStreamID, *secondEvent.SourceStreamID)
	}
	if otherEvent.SourceStreamID == nil || *otherEvent.SourceStreamID == *firstEvent.SourceStreamID {
		t.Fatalf("distinct stream keys shared a stream id: %v", otherEvent.SourceStreamID)
	}
}

func TestIngestEventPreservesCallerSuppliedSequenceAndRejectsDuplicate(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	occurredAt := "2026-08-21T11:00:00Z"
	sequence := int64(7)

	supplied := streamIngest(t, repository, "stream-supplied", ingestParams("space-1", "chat-1", "supplied", occurredAt, &sequence))
	requireSequence(t, repository, supplied.EventID, 7)

	if _, _, err := repository.IngestEvent(context.Background(), memoryIdentity("stream-duplicate", "/v1/events", ingestParams("space-1", "chat-1", "duplicate", occurredAt, &sequence)), ingestParams("space-1", "chat-1", "duplicate", occurredAt, &sequence)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate caller sequence error = %v; want ErrConflict", err)
	}

	next := int64(8)
	contiguous := streamIngest(t, repository, "stream-contiguous", ingestParams("space-1", "chat-1", "contiguous", occurredAt, &next))
	requireSequence(t, repository, contiguous.EventID, 8)

	var count int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM raw_events WHERE event_id IN (?,?,?)`, supplied.EventID, contiguous.EventID, "duplicate").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 accepted events and no rejected event; found %d", count)
	}
}

func TestIngestEventIdempotentReplayPreservesStreamAndSequence(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	params := ingestParams("space-1", "chat-1", "replay", "2026-08-21T11:00:00Z", nil)
	identity := memoryIdentity("stream-replay", "/v1/events", params)

	first, replay, err := repository.IngestEvent(context.Background(), identity, params)
	if err != nil || replay {
		t.Fatalf("first ingest replay=%t error=%v", replay, err)
	}
	firstEvent := requireSequence(t, repository, first.EventID, 0)

	replayed, replay, err := repository.IngestEvent(context.Background(), identity, params)
	if err != nil || !replay {
		t.Fatalf("replay ingest replay=%t error=%v", replay, err)
	}
	if replayed.EventID != first.EventID {
		t.Fatalf("replay event = %s; want %s", replayed.EventID, first.EventID)
	}
	requireSequence(t, repository, replayed.EventID, 0)
	if firstEvent.SourceStreamID == nil {
		t.Fatal("stream identity was not persisted")
	}

	var events int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM raw_events WHERE source_stream_id=?`, *firstEvent.SourceStreamID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("idempotent replay created %d events; want 1", events)
	}
}

func TestIngestEventConcurrentStreamSequenceIsUniqueAndContiguous(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	const writers = 8
	occurredAt := "2026-08-21T11:00:00Z"

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			params := ingestParams("space-1", "chat-concurrent", "concurrent", occurredAt, nil)
			_, _, err := repository.IngestEvent(context.Background(), memoryIdentity("stream-concurrent-"+string(rune('a'+index)), "/v1/events", params), params)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ingest error = %v", err)
		}
	}

	var streamID string
	if err := repository.store.reader.QueryRow(`SELECT source_stream_id FROM source_streams WHERE space_id='space-1' AND source_stream_key='chat-concurrent'`).Scan(&streamID); err != nil {
		t.Fatal(err)
	}
	rows, err := repository.store.reader.Query(`SELECT sequence FROM raw_events WHERE source_stream_id=? ORDER BY sequence`, streamID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sequences []int64
	for rows.Next() {
		var sequence int64
		if err := rows.Scan(&sequence); err != nil {
			t.Fatal(err)
		}
		sequences = append(sequences, sequence)
	}
	if len(sequences) != writers {
		t.Fatalf("stream has %d sequenced events; want %d", len(sequences), writers)
	}
	for index, sequence := range sequences {
		if sequence != int64(index) {
			t.Fatalf("stream sequences = %v; want contiguous 0..%d", sequences, writers-1)
		}
	}
}

func seedStreamEvent(t *testing.T, repository *Repository, spaceID, eventID, streamID, contentID string, sequence int64, occurredAt string) {
	t.Helper()
	if _, err := repository.store.writer.Exec(`INSERT INTO raw_contents(content_id,content_kind,content_text,media_type,content_hash,size_bytes,created_at) VALUES (?,'text',?,'text/plain',?,?,?)`, contentID, eventID, contentID, len(eventID), occurredAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO raw_events(event_id,operation_id,space_id,content_id,source_type,source_name,source_stream_id,sequence,caller_id,occurred_at,received_at,created_at) VALUES (?,?,?,?,'message','chat',?,?, 'seed',?,?,?)`, eventID, "op-"+eventID, spaceID, contentID, streamID, sequence, occurredAt, occurredAt, occurredAt); err != nil {
		t.Fatal(err)
	}
}

func prepareStreamContext(t *testing.T, repository *Repository, spaceID, targetID string, maxContext int) []domain.ProcessingEvent {
	t.Helper()
	now := formatTime(repository.runtime.Now())
	if _, err := repository.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,created_at) VALUES ('stream-job','stream-op','extract_memory','raw_event',?,'test-v1','pending',3,?,?)`, targetID, now, now); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "stream-worker", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	input, err := repository.PrepareProcessing(context.Background(), claim, ProcessingPreparationParams{ProcessorIdentity: "processor/test", AllowedMemoryTypes: []string{"fact"}, MaxCandidates: 2, MaxContentChars: 100, MaxContextEvents: maxContext})
	if err != nil {
		t.Fatal(err)
	}
	return input.ContextEvents
}

func seedStreamSpace(t *testing.T, repository *Repository, spaceID, streamID string, occurredAt string) {
	t.Helper()
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_spaces(space_id,parent_space_id,space_type,external_id,display_name,created_at) VALUES (?, 'space-1', 'session', ?, ?, ?)`, spaceID, spaceID, spaceID, occurredAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO source_streams(source_stream_id,space_id,source_type,source_name,source_stream_key,created_at) VALUES (?,?,'message','chat',?,?)`, streamID, spaceID, streamID, occurredAt); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareProcessingOrdersContextBySequenceNotStableID(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	occurredAt := "2026-08-21T11:00:00Z"
	seedStreamSpace(t, repository, "stream-space-a", "stream-ctx", occurredAt)
	seedStreamEvent(t, repository, "stream-space-a", "event-z", "stream-ctx", "content-z", 0, occurredAt)
	seedStreamEvent(t, repository, "stream-space-a", "event-a", "stream-ctx", "content-a", 1, occurredAt)
	seedStreamEvent(t, repository, "stream-space-a", "event-m", "stream-ctx", "content-m", 2, occurredAt)
	seedStreamEvent(t, repository, "stream-space-a", "event-target", "stream-ctx", "content-target", 3, occurredAt)

	context := prepareStreamContext(t, repository, "stream-space-a", "event-target", 10)
	var got []string
	for _, event := range context {
		got = append(got, event.EventID)
	}
	want := []string{"event-z", "event-a", "event-m"}
	if len(got) != len(want) {
		t.Fatalf("context = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("context = %v; want %v (stable-ID order must not override sequence)", got, want)
		}
	}
}

func TestPrepareProcessingSelectsNearestSequenceWithinBoundedContext(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	occurredAt := "2026-08-21T11:00:00Z"
	seedStreamSpace(t, repository, "stream-space-b", "stream-bound", occurredAt)
	seedStreamEvent(t, repository, "stream-space-b", "event-a", "stream-bound", "content-a", 1, occurredAt)
	seedStreamEvent(t, repository, "stream-space-b", "event-b", "stream-bound", "content-b", 0, occurredAt)
	seedStreamEvent(t, repository, "stream-space-b", "event-c", "stream-bound", "content-c", 2, occurredAt)
	seedStreamEvent(t, repository, "stream-space-b", "event-target", "stream-bound", "content-target", 3, occurredAt)

	context := prepareStreamContext(t, repository, "stream-space-b", "event-target", 2)
	var got []string
	for _, event := range context {
		got = append(got, event.EventID)
	}
	want := []string{"event-a", "event-c"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("bounded context = %v; want %v", got, want)
	}
}
