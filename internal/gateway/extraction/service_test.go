package extraction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeEventStore struct {
	record        IngestRecord
	replay        bool
	ingestErr     error
	ingestID      gateway.WriteIdentity
	ingestParams  IngestParams
	ingestCalls   int
	respectCancel bool

	event    domain.RawEvent
	getErr   error
	getCalls int
}

func (f *fakeEventStore) IngestEvent(ctx context.Context, identity gateway.WriteIdentity, params IngestParams) (IngestRecord, bool, error) {
	f.ingestCalls++
	f.ingestID = identity
	f.ingestParams = params
	if f.respectCancel {
		if err := ctx.Err(); err != nil {
			return IngestRecord{}, false, err
		}
	}
	return f.record, f.replay, f.ingestErr
}

func (f *fakeEventStore) GetEvent(context.Context, string) (domain.RawEvent, error) {
	f.getCalls++
	return f.event, f.getErr
}

func ingestCall(caller, key, requestID string) gateway.CallContext {
	call := gateway.CallContext{RequestID: requestID, Caller: gateway.CallerIdentity{SubjectID: caller}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func textContent(text string) domain.Content {
	return domain.Content{Kind: "text", MediaType: "text/plain", Text: text}
}

func TestIngestAppliesProtocolNeutralDefaults(t *testing.T) {
	store := &fakeEventStore{record: IngestRecord{OperationID: "operation-1", EventID: "event-1", Status: "accepted", JobIDs: []string{"job-1"}, AcceptedAt: "2026-08-21T12:00:00Z"}}
	service := NewService(store)
	service.now = func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) }

	command := IngestCommand{SpaceID: "space-1", SourceType: "manual", SourceName: "source", Content: textContent("raw event")}
	result, err := service.Ingest(context.Background(), ingestCall("caller-1", "key-1", "request-1"), command)
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if result.EventID != "event-1" || result.Status != "accepted" || len(result.JobIDs) != 1 || result.Replay {
		t.Fatalf("result = %+v", result)
	}
	if result.OperationID != "operation-1" {
		t.Fatalf("operation id = %q, want persisted operation id", result.OperationID)
	}
	if store.ingestParams.OccurredAt != "2026-08-21T12:00:00Z" || store.ingestParams.Metadata == nil {
		t.Fatalf("defaults = %+v", store.ingestParams)
	}
	if store.ingestID.Operation != OperationIngest || store.ingestID.IdempotencyKey != "key-1" {
		t.Fatalf("write identity = %+v", store.ingestID)
	}
	wantHash, _ := gateway.CanonicalHash(command)
	if store.ingestID.CommandHash != wantHash {
		t.Fatalf("command hash = %q, want raw-command hash %q", store.ingestID.CommandHash, wantHash)
	}
}

func TestIngestNormalizesOccurredAtToUTC(t *testing.T) {
	store := &fakeEventStore{}
	service := NewService(store)
	command := IngestCommand{SpaceID: "space-1", SourceType: "message", SourceName: "source", OccurredAt: "2026-08-21T10:00:00+02:00", Content: textContent("raw event")}
	if _, err := service.Ingest(context.Background(), ingestCall("caller-1", "key-1", "request-1"), command); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if store.ingestParams.OccurredAt != "2026-08-21T08:00:00Z" {
		t.Fatalf("occurred_at = %q, want normalized UTC", store.ingestParams.OccurredAt)
	}
}

func TestIngestRejectsInvalidInputBeforePersistence(t *testing.T) {
	store := &fakeEventStore{}
	service := NewService(store)
	sequence := int64(-1)
	long := string(make([]rune, 129))
	cases := map[string]IngestCommand{
		"missing space":     {SourceType: "manual", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z", Content: textContent("raw event")},
		"bad source type":   {SpaceID: "space-1", SourceType: "unknown", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z", Content: textContent("raw event")},
		"long source name":  {SpaceID: "space-1", SourceType: "manual", SourceName: long, OccurredAt: "2026-08-21T12:00:00Z", Content: textContent("raw event")},
		"negative sequence": {SpaceID: "space-1", SourceType: "manual", SourceName: "source", Sequence: &sequence, OccurredAt: "2026-08-21T12:00:00Z", Content: textContent("raw event")},
		"bad occurred at":   {SpaceID: "space-1", SourceType: "manual", SourceName: "source", OccurredAt: "yesterday", Content: textContent("raw event")},
		"missing media type": {SpaceID: "space-1", SourceType: "manual", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z",
			Content: domain.Content{Kind: "text", Text: "raw event"}},
		"mixed text fields": {SpaceID: "space-1", SourceType: "manual", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z",
			Content: domain.Content{Kind: "text", MediaType: "text/plain", Text: "raw", ReferenceURI: "file:///x"}},
		"empty json value": {SpaceID: "space-1", SourceType: "manual", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z",
			Content: domain.Content{Kind: "json", MediaType: "application/json"}},
		"missing reference": {SpaceID: "space-1", SourceType: "manual", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z",
			Content: domain.Content{Kind: "reference", MediaType: "text/plain"}},
		"unsupported kind": {SpaceID: "space-1", SourceType: "manual", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z",
			Content: domain.Content{Kind: "binary", MediaType: "application/octet-stream"}},
	}
	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Ingest(context.Background(), ingestCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("error = %v, want invalid_input", err)
			}
		})
	}
	if store.ingestCalls != 0 {
		t.Fatalf("store called %d times for invalid input", store.ingestCalls)
	}
}

func TestIngestRequiresCallerAndIdempotency(t *testing.T) {
	service := NewService(&fakeEventStore{})
	valid := IngestCommand{SpaceID: "space-1", SourceType: "manual", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z", Content: textContent("raw event")}
	if _, err := service.Ingest(context.Background(), ingestCall("", "key-1", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing caller error = %v", err)
	}
	if _, err := service.Ingest(context.Background(), ingestCall("caller-1", "", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing idempotency error = %v", err)
	}
}

func TestIngestPropagatesCancellation(t *testing.T) {
	store := &fakeEventStore{respectCancel: true}
	service := NewService(store)
	command := IngestCommand{SpaceID: "space-1", SourceType: "manual", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z", Content: textContent("raw event")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Ingest(ctx, ingestCall("caller-1", "key-1", "request-1"), command); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestIngestClassifiesPortErrors(t *testing.T) {
	store := &fakeEventStore{ingestErr: gateway.NewError(gateway.ErrorNotFound, "missing scope")}
	service := NewService(store)
	command := IngestCommand{SpaceID: "space-1", SourceType: "manual", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z", Content: textContent("raw event")}
	if _, err := service.Ingest(context.Background(), ingestCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("gateway error passthrough = %v", err)
	}
	store.ingestErr = errors.New("disk on fire")
	if _, err := service.Ingest(context.Background(), ingestCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("unknown error classification = %v", err)
	}
}

func TestIngestPreservesReplayOutcome(t *testing.T) {
	store := &fakeEventStore{record: IngestRecord{OperationID: "operation-1", EventID: "event-1", Status: "accepted"}, replay: true}
	service := NewService(store)
	command := IngestCommand{SpaceID: "space-1", SourceType: "manual", SourceName: "source", OccurredAt: "2026-08-21T12:00:00Z", Content: textContent("raw event")}
	result, err := service.Ingest(context.Background(), ingestCall("caller-1", "key-1", "request-1"), command)
	if err != nil || !result.Replay || result.EventID != "event-1" {
		t.Fatalf("Ingest() = %+v error = %v", result, err)
	}
}

func TestGetEventValidatesAndClassifiesErrors(t *testing.T) {
	store := &fakeEventStore{event: domain.RawEvent{EventID: "event-1"}}
	service := NewService(store)
	if _, err := service.GetEvent(context.Background(), ingestCall("caller-1", "", "request-1"), GetEventQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing event id error = %v", err)
	}
	event, err := service.GetEvent(context.Background(), ingestCall("caller-1", "", "request-1"), GetEventQuery{EventID: "event-1"})
	if err != nil || event.EventID != "event-1" {
		t.Fatalf("GetEvent() = %+v error = %v", event, err)
	}
	store.getErr = gateway.NewError(gateway.ErrorNotFound, "missing event")
	if _, err := service.GetEvent(context.Background(), ingestCall("caller-1", "", "request-1"), GetEventQuery{EventID: "missing"}); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("missing event classification = %v", err)
	}
}
