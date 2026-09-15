package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestFeedbackAppendListPrivacyHealthAndRebuildRoutes(t *testing.T) {
	cfg := testConfig(t)
	d, err := StartStaging(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "feedback-api-space", map[string]any{"space_type": "user", "external_id": "feedback-api", "display_name": "Feedback API"})
	event := ingestManualEvent(t, client, "feedback-api-event", space.SpaceID, "feedback source")
	memory, _ := createMemory(t, client, "feedback-api-memory", memoryCreateBody(space.SpaceID, event.EventID, "feedback target"))
	body := map[string]any{"space_id": space.SpaceID, "memory_id": memory.MemoryID, "revision_id": memory.CurrentRevision.RevisionID, "feedback_purpose": "memory_quality_feedback", "feedback_type": "confirm", "reason_code": "other", "note": "private note"}
	response := requestJSON(t, client, http.MethodPost, "/v1/feedback", "feedback-api-append", body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("append status = %d", response.StatusCode)
	}
	var appended domain.FeedbackAppendResponse
	decodeResponse(t, response, &appended)
	if appended.Feedback.Note == nil || *appended.Feedback.Note != "private note" || appended.Proposal.Status != "not_requested" {
		t.Fatalf("append response = %+v", appended)
	}
	replay := requestJSON(t, client, http.MethodPost, "/v1/feedback", "feedback-api-append", body)
	if replay.StatusCode != http.StatusCreated || replay.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay status/header = %d/%q", replay.StatusCode, replay.Header.Get("Idempotent-Replay"))
	}
	replay.Body.Close()

	path := "/v1/feedback?space_id=" + url.QueryEscape(space.SpaceID)
	list := requestAsCaller(t, client, http.MethodGet, path, "other-caller", "", nil)
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", list.StatusCode)
	}
	var page domain.FeedbackPage
	decodeResponse(t, list, &page)
	if len(page.Items) != 1 || page.Items[0].Note != nil {
		t.Fatalf("cross-caller note was exposed: %+v", page)
	}

	tooLong := body
	tooLong["note"] = strings.Repeat("x", 2001)
	assertSafeError(t, requestJSON(t, client, http.MethodPost, "/v1/feedback", "feedback-api-long-note", tooLong), http.StatusUnprocessableEntity, "invalid_request")

	mismatched := map[string]any{"space_id": space.SpaceID, "memory_id": memory.MemoryID, "revision_id": memory.CurrentRevision.RevisionID, "feedback_purpose": "memory_quality_feedback", "feedback_type": "useful", "reason_code": "answered_query"}
	assertSafeError(t, requestJSON(t, client, http.MethodPost, "/v1/feedback", "feedback-api-mismatch", mismatched), http.StatusUnprocessableEntity, "invalid_request")
	missingDecision := map[string]any{"space_id": space.SpaceID, "memory_id": memory.MemoryID, "revision_id": memory.CurrentRevision.RevisionID, "feedback_purpose": "retrieval_feedback", "feedback_type": "useful", "reason_code": "answered_query"}
	assertSafeError(t, requestJSON(t, client, http.MethodPost, "/v1/feedback", "feedback-api-missing-decision", missingDecision), http.StatusUnprocessableEntity, "invalid_request")
	health := requestJSON(t, client, http.MethodGet, "/v1/feedback/aggregates/health", "", nil)
	if health.StatusCode != http.StatusOK {
		t.Fatalf("aggregate health status = %d", health.StatusCode)
	}
	var aggregateHealth domain.FeedbackAggregateHealth
	decodeResponse(t, health, &aggregateHealth)
	if aggregateHealth.AlgorithmVersion != "v2" || aggregateHealth.WindowDays != 90 || aggregateHealth.MinimumSupport != 3 {
		t.Fatalf("aggregate health = %+v", aggregateHealth)
	}
	rebuild := requestJSON(t, client, http.MethodPost, "/v1/feedback/aggregates/rebuild", "feedback-api-rebuild", map[string]any{})
	if rebuild.StatusCode != http.StatusAccepted {
		t.Fatalf("rebuild status = %d", rebuild.StatusCode)
	}
	rebuild.Body.Close()
}

func requestAsCaller(t *testing.T, client *http.Client, method, path, caller, key string, body any) *http.Response {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Caller-ID", caller)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
