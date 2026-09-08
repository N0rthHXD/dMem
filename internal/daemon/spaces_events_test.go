package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	_ "github.com/mattn/go-sqlite3"
)

func TestSpaceAndEventAPI(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	processor := &heldProcessor{release: make(chan struct{})}
	d, err := start(context.Background(), cfg, processor)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)

	root := createSpace(t, client, "root-key", map[string]any{
		"space_type": "user", "external_id": "owner", "display_name": "Owner",
	})
	project := createSpace(t, client, "project-key", map[string]any{
		"parent_space_id": root.SpaceID, "space_type": "project", "external_id": "p1", "display_name": "Project One",
	})

	response := requestJSON(t, client, http.MethodPost, "/v1/spaces", "bad-parent", map[string]any{
		"parent_space_id": root.SpaceID, "space_type": "task", "external_id": "bad", "display_name": "Bad",
	})
	assertError(t, response, http.StatusUnprocessableEntity, "invalid_request")

	replay := createSpace(t, client, "project-key", map[string]any{
		"parent_space_id": root.SpaceID, "space_type": "project", "external_id": "p1", "display_name": "Project One",
	})
	if replay.SpaceID != project.SpaceID {
		t.Fatalf("space replay ID = %q; want %q", replay.SpaceID, project.SpaceID)
	}

	response = requestJSON(t, client, http.MethodPost, "/v1/spaces", "project-key", map[string]any{
		"parent_space_id": root.SpaceID, "space_type": "project", "external_id": "conflict", "display_name": "Different",
	})
	assertError(t, response, http.StatusConflict, "idempotency_conflict")

	ingestBody := map[string]any{
		"space_id": project.SpaceID, "source_type": "message", "source_name": "test-agent",
		"occurred_at": "2026-08-21T10:00:00+02:00",
		"content":     map[string]any{"kind": "text", "media_type": "text/plain", "text": "remember this"},
		"metadata":    map[string]any{"turn": 1},
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/events", "event-key", ingestBody)
	if response.StatusCode != http.StatusAccepted || response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("ingest status/replay = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var accepted domain.IngestResponse
	decodeResponse(t, response, &accepted)
	if accepted.Status != "accepted" || len(accepted.JobIDs) != 1 {
		t.Fatalf("ingest response = %+v", accepted)
	}

	response = requestJSON(t, client, http.MethodPost, "/v1/events", "event-key", ingestBody)
	if response.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("event replay header = %q", response.Header.Get("Idempotent-Replay"))
	}
	var replayed domain.IngestResponse
	decodeResponse(t, response, &replayed)
	if replayed.EventID != accepted.EventID || replayed.JobIDs[0] != accepted.JobIDs[0] {
		t.Fatalf("ingest replay = %+v; want %+v", replayed, accepted)
	}

	response = requestJSON(t, client, http.MethodGet, "/v1/events/"+accepted.EventID, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get event status = %d", response.StatusCode)
	}
	var event domain.RawEvent
	decodeResponse(t, response, &event)
	if event.Content.Text != "remember this" || event.OccurredAt != "2026-08-21T08:00:00Z" || fmt.Sprint(event.Metadata["turn"]) != "1" {
		t.Fatalf("event = %+v", event)
	}

	response = requestJSON(t, client, http.MethodGet, "/v1/events?space_id="+project.SpaceID+"&limit=1", "", nil)
	var page domain.EventPage
	decodeResponse(t, response, &page)
	if len(page.Items) != 1 || page.Items[0].EventID != accepted.EventID {
		t.Fatalf("event page = %+v", page)
	}

	response = requestJSON(t, client, http.MethodGet, "/v1/health", "", nil)
	var health api.Health
	decodeResponse(t, response, &health)
	if health.JobQueueDepth != 1 {
		t.Fatalf("job queue depth = %d; want 1", health.JobQueueDepth)
	}

	response = requestJSON(t, client, http.MethodPost, "/v1/events/"+accepted.EventID+"/replay", "replay-active", nil)
	if response.StatusCode != http.StatusAccepted || response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("active replay status/header = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var activeReplay domain.JobReference
	decodeResponse(t, response, &activeReplay)
	if activeReplay.JobID != accepted.JobIDs[0] {
		t.Fatalf("active replay = %+v", activeReplay)
	}

	close(processor.release)
	waitForJobStatus(t, client, accepted.JobIDs[0], "succeeded")
	response = requestJSON(t, client, http.MethodPost, "/v1/events/"+accepted.EventID+"/replay", "replay-terminal", nil)
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("terminal replay status = %d: %s", response.StatusCode, body)
	}
	var job domain.JobReference
	decodeResponse(t, response, &job)
	if job.JobID != accepted.JobIDs[0] || job.OperationID == "" {
		t.Fatalf("replay job = %+v", job)
	}
}

func TestAPIValidationAndReferenceContent(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	d, err := start(context.Background(), cfg, &heldProcessor{release: make(chan struct{})})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)

	request, _ := http.NewRequest(http.MethodGet, "http://localhost/v1/spaces", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertError(t, response, http.StatusUnprocessableEntity, "invalid_request")

	response = requestJSON(t, client, http.MethodPost, "/v1/spaces", "unknown-field", map[string]any{
		"space_type": "user", "external_id": "owner", "display_name": "Owner", "unknown": true,
	})
	assertError(t, response, http.StatusUnprocessableEntity, "invalid_request")

	root := createSpace(t, client, "root", map[string]any{"space_type": "user", "external_id": "owner", "display_name": "Owner"})
	response = requestJSON(t, client, http.MethodPost, "/v1/events", "binary", map[string]any{
		"space_id": root.SpaceID, "source_type": "manual", "source_name": "test", "occurred_at": "2026-08-21T00:00:00Z",
		"content": map[string]any{"kind": "binary", "media_type": "application/octet-stream"},
	})
	assertError(t, response, http.StatusUnprocessableEntity, "unsupported_content_type")

	response = requestJSON(t, client, http.MethodPost, "/v1/events", "reference", map[string]any{
		"space_id": root.SpaceID, "source_type": "file_reference", "source_name": "test", "occurred_at": "2026-08-21T00:00:00Z",
		"content": map[string]any{"kind": "reference", "media_type": "text/plain", "reference_uri": "file:///not/read", "declared_size": 42},
	})
	var accepted domain.IngestResponse
	decodeResponse(t, response, &accepted)
	response = requestJSON(t, client, http.MethodGet, "/v1/events/"+accepted.EventID, "", nil)
	var event domain.RawEvent
	decodeResponse(t, response, &event)
	if event.Content.ReferenceURI != "file:///not/read" || event.Content.DeclaredSize == nil || *event.Content.DeclaredSize != 42 {
		t.Fatalf("reference event = %+v", event)
	}

	response = requestJSON(t, client, http.MethodGet, "/v1/does-not-exist", "", nil)
	assertError(t, response, http.StatusNotFound, "not_found")
}

func TestManagedDaemonCreatesPolicyForNewSpace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	cfg := reloadConfig{dataDir: filepath.Join(root, "data"), runtimeDir: filepath.Join(root, "runtime"), embedding: true}
	writeReloadConfig(t, path, cfg)
	t.Setenv("DMEM_RELOAD_EMBEDDING_SECRET", "embedding-secret")

	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(d.endpoint)
	owner := createSpace(t, client, "policy-owner", map[string]any{"space_type": "user", "external_id": "owner", "display_name": "Owner"})
	project := createSpace(t, client, "policy-project", map[string]any{"parent_space_id": owner.SpaceID, "space_type": "project", "external_id": "project-a", "display_name": "Project"})

	policy, err := d.repository.EffectivePolicy(context.Background(), "embedding", project.SpaceID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Decision != "allow" || policy.PolicyGeneration != 1 || policy.ReasonCode != "attribute_assignment" {
		t.Fatalf("project policy = %+v", policy)
	}
}

type heldProcessor struct {
	release chan struct{}
}

func (*heldProcessor) Enabled() bool    { return true }
func (*heldProcessor) Identity() string { return "processor/in-process" }

func (p *heldProcessor) Process(ctx context.Context, _ domain.ProcessingInput) domain.ProcessingResult {
	select {
	case <-p.release:
		return domain.ProcessingResult{Code: domain.ProcessingNoProcessor}
	case <-ctx.Done():
		return domain.ProcessingResult{Code: domain.ProcessingTransientFailure, Message: ctx.Err().Error()}
	}
}

func (p *heldProcessor) ProcessGraph(ctx context.Context, _ domain.GraphExtractionInput) domain.GraphExtractionResult {
	select {
	case <-p.release:
		return domain.GraphExtractionResult{Code: domain.GraphExtractionNoProcessor}
	case <-ctx.Done():
		return domain.GraphExtractionResult{Code: domain.GraphExtractionTransientFailure, Message: ctx.Err().Error()}
	}
}

func waitForJobStatus(t *testing.T, client *http.Client, jobID, status string) domain.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response := requestJSON(t, client, http.MethodGet, "/v1/jobs/"+jobID, "", nil)
		var job domain.Job
		decodeResponse(t, response, &job)
		if job.Status == status {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", jobID, status)
	return domain.Job{}
}

func TestEventPaginationAndQueueLimits(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.QueueSoftLimit = 1
	cfg.QueueHardLimit = 2
	d, err := start(context.Background(), cfg, &heldProcessor{release: make(chan struct{})})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	root := createSpace(t, client, "root", map[string]any{"space_type": "user", "external_id": "owner", "display_name": "Owner"})

	ingest := func(key, occurred string) *http.Response {
		return requestJSON(t, client, http.MethodPost, "/v1/events", key, map[string]any{
			"space_id": root.SpaceID, "source_type": "manual", "source_name": "pagination", "occurred_at": occurred,
			"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": key},
		})
	}
	firstResponse := ingest("first", "2026-08-21T01:00:00Z")
	var first domain.IngestResponse
	decodeResponse(t, firstResponse, &first)
	if first.Status != "accepted" {
		t.Fatalf("first status = %q", first.Status)
	}
	secondResponse := ingest("second", "2026-08-21T02:00:00Z")
	var second domain.IngestResponse
	decodeResponse(t, secondResponse, &second)
	if second.Status != "deferred" {
		t.Fatalf("second status = %q; want deferred", second.Status)
	}
	thirdResponse := ingest("third", "2026-08-21T03:00:00Z")
	assertError(t, thirdResponse, http.StatusTooManyRequests, "retryable_overload")

	response := requestJSON(t, client, http.MethodGet, "/v1/events?space_id="+root.SpaceID+"&limit=1", "", nil)
	var page1 domain.EventPage
	decodeResponse(t, response, &page1)
	if len(page1.Items) != 1 || page1.Items[0].EventID != second.EventID || page1.NextCursor == nil {
		t.Fatalf("page 1 = %+v", page1)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/events?space_id="+root.SpaceID+"&limit=1&cursor="+*page1.NextCursor, "", nil)
	var page2 domain.EventPage
	decodeResponse(t, response, &page2)
	if len(page2.Items) != 1 || page2.Items[0].EventID != first.EventID {
		t.Fatalf("page 2 = %+v", page2)
	}

	db, err := sql.Open("sqlite3", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for table, want := range map[string]int{"raw_events": 2, "raw_contents": 2, "jobs": 2, "idempotency_records": 3, "audit_events": 3} {
		var got int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s count = %d; want %d", table, got, want)
		}
	}
}

func TestJSONPrecisionAndTimestampIdempotency(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	root := createSpace(t, client, "root", map[string]any{"space_type": "user", "external_id": "owner", "display_name": "Owner"})

	body := fmt.Sprintf(`{"space_id":%q,"source_type":"manual","source_name":"test","occurred_at":"2026-08-21T00:00:00.0001Z","content":{"kind":"json","media_type":"application/json","value":{"large":9007199254740993}},"metadata":{"large":9007199254740993}}`, root.SpaceID)
	request, _ := http.NewRequest(http.MethodPost, "http://localhost/v1/events", bytes.NewBufferString(body))
	request.Header.Set("X-Caller-ID", "test-agent")
	request.Header.Set("Idempotency-Key", "precision")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var accepted domain.IngestResponse
	decodeResponse(t, response, &accepted)
	response = requestJSON(t, client, http.MethodGet, "/v1/events/"+accepted.EventID, "", nil)
	var event domain.RawEvent
	decodeResponse(t, response, &event)
	value := event.Content.Value.(map[string]any)["large"]
	if fmt.Sprint(value) != "9007199254740993" || fmt.Sprint(event.Metadata["large"]) != "9007199254740993" || event.OccurredAt != "2026-08-21T00:00:00.0001Z" {
		t.Fatalf("precision was not preserved: %+v", event)
	}

	changed := strings.Replace(body, ".0001Z", ".0009Z", 1)
	request, _ = http.NewRequest(http.MethodPost, "http://localhost/v1/events", bytes.NewBufferString(changed))
	request.Header.Set("X-Caller-ID", "test-agent")
	request.Header.Set("Idempotency-Key", "precision")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertError(t, response, http.StatusConflict, "idempotency_conflict")
}

func createSpace(t *testing.T, client *http.Client, key string, body any) domain.Space {
	t.Helper()
	response := requestJSON(t, client, http.MethodPost, "/v1/spaces", key, body)
	if response.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("create space status = %d: %s", response.StatusCode, data)
	}
	var result domain.Space
	decodeResponse(t, response, &result)
	return result
}

func requestJSON(t *testing.T, client *http.Client, method, path, key string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, "http://localhost"+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Caller-ID", "test-agent")
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

func decodeResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode status %d: %v", response.StatusCode, err)
	}
}

func assertError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	if response.StatusCode != status {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, status, data)
	}
	var result api.Error
	decodeResponse(t, response, &result)
	if result.ErrorCode != code || result.RequestID == "" || result.OperationID != "" {
		t.Fatalf("error = %+v; want code %q", result, code)
	}
	if result.Message == "" {
		t.Fatal(fmt.Sprintf("empty message for %s", code))
	}
}
