package daemon

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	_ "github.com/mattn/go-sqlite3"
)

func TestSliceCNoopJobAPIAndRetry(t *testing.T) {
	cfg := testConfig(t)
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "slice-c-space", map[string]any{
		"space_type": "user", "external_id": "slice-c", "display_name": "Slice C",
	})
	response := requestJSON(t, client, http.MethodPost, "/v1/events", "slice-c-event", map[string]any{
		"space_id": space.SpaceID, "source_type": "manual", "source_name": "test", "occurred_at": "2026-08-21T00:00:00Z",
		"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "stored only"},
	})
	var accepted domain.IngestResponse
	decodeResponse(t, response, &accepted)
	job := waitForJobStatus(t, client, accepted.JobIDs[0], "succeeded")
	if job.ResultCode == nil || *job.ResultCode != "no_processor" || job.AttemptCount != 1 || job.FinishedAt == nil {
		t.Fatalf("no-op job = %+v", job)
	}

	db, err := sql.Open("sqlite3", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var memories int
	var processingStatus string
	if err := db.QueryRow(`SELECT count(*) FROM memories`).Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT processing_status FROM raw_events WHERE event_id=?`, accepted.EventID).Scan(&processingStatus); err != nil {
		t.Fatal(err)
	}
	if memories != 0 || processingStatus != "stored" {
		t.Fatalf("memories/status = %d/%q", memories, processingStatus)
	}

	response = requestJSON(t, client, http.MethodPost, "/v1/jobs/"+job.JobID+"/retry", "invalid-retry", nil)
	assertError(t, response, http.StatusConflict, "conflict")
}

func TestSliceCRunningCancelAPI(t *testing.T) {
	cfg := testConfig(t)
	cfg.WorkerCount = 1
	processor := &heldProcessor{release: make(chan struct{})}
	d, err := start(context.Background(), cfg, processor)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "cancel-space", map[string]any{
		"space_type": "user", "external_id": "cancel", "display_name": "Cancel",
	})
	response := requestJSON(t, client, http.MethodPost, "/v1/events", "cancel-event", map[string]any{
		"space_id": space.SpaceID, "source_type": "manual", "source_name": "test", "occurred_at": "2026-08-21T00:00:00Z",
		"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "cancel me"},
	})
	var accepted domain.IngestResponse
	decodeResponse(t, response, &accepted)
	waitForJobStatus(t, client, accepted.JobIDs[0], "running")
	response = requestJSON(t, client, http.MethodPost, "/v1/jobs/"+accepted.JobIDs[0]+"/cancel", "cancel-job", nil)
	if response.StatusCode != http.StatusOK || response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("cancel response = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var requested domain.Job
	decodeResponse(t, response, &requested)
	if requested.Status != "running" {
		t.Fatalf("requested job = %+v", requested)
	}
	waitForJobStatus(t, client, accepted.JobIDs[0], "cancelled")
	response = requestJSON(t, client, http.MethodPost, "/v1/jobs/"+accepted.JobIDs[0]+"/cancel", "cancel-job", nil)
	if response.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("cancel replay = %q", response.Header.Get("Idempotent-Replay"))
	}
	response.Body.Close()
}

func TestSliceCFailedRetryAPI(t *testing.T) {
	cfg := testConfig(t)
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "retry-space", map[string]any{
		"space_type": "user", "external_id": "retry", "display_name": "Retry",
	})
	response := requestJSON(t, client, http.MethodPost, "/v1/events", "retry-event", map[string]any{
		"space_id": space.SpaceID, "source_type": "manual", "source_name": "test", "occurred_at": "2026-08-21T00:00:00Z",
		"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "retry"},
	})
	var accepted domain.IngestResponse
	decodeResponse(t, response, &accepted)
	waitForJobStatus(t, client, accepted.JobIDs[0], "succeeded")
	db, err := sql.Open("sqlite3", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='failed', result_code=NULL, result_json=NULL, last_error_code='forced', last_error_message='forced failure', finished_at=? WHERE job_id=?`, time.Now().UTC().Format(time.RFC3339Nano), accepted.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	db.Close()

	response = requestJSON(t, client, http.MethodPost, "/v1/jobs/"+accepted.JobIDs[0]+"/retry", "retry-job", nil)
	if response.StatusCode != http.StatusAccepted || response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("retry response = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var retried domain.Job
	decodeResponse(t, response, &retried)
	if retried.Status != "pending" || retried.AttemptCount != 1 || retried.MaxAttempts != cfg.MaxAttempts+1 || retried.LastErrorCode != nil {
		t.Fatalf("retried job = %+v", retried)
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/jobs/"+accepted.JobIDs[0]+"/retry", "retry-job", nil)
	if response.StatusCode != http.StatusAccepted || response.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("retry replay = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	response.Body.Close()
	job := waitForJobStatus(t, client, accepted.JobIDs[0], "succeeded")
	if job.AttemptCount != 2 {
		t.Fatalf("retried completion = %+v", job)
	}
}

func TestSliceCGracefulShutdownPreservesRecoverableState(t *testing.T) {
	cfg := testConfig(t)
	cfg.WorkerCount = 1
	processor := &heldProcessor{release: make(chan struct{})}
	d, err := start(context.Background(), cfg, processor)
	if err != nil {
		t.Fatal(err)
	}
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "shutdown-space", map[string]any{
		"space_type": "user", "external_id": "shutdown", "display_name": "Shutdown",
	})
	response := requestJSON(t, client, http.MethodPost, "/v1/events", "shutdown-event", map[string]any{
		"space_id": space.SpaceID, "source_type": "manual", "source_name": "test", "occurred_at": "2026-08-21T00:00:00Z",
		"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "shutdown"},
	})
	var accepted domain.IngestResponse
	decodeResponse(t, response, &accepted)
	waitForJobStatus(t, client, accepted.JobIDs[0], "running")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := d.Close(ctx); err != nil {
		t.Fatal(err)
	}

	d, err = Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	job := waitForJobStatus(t, unixClient(cfg.Endpoint), accepted.JobIDs[0], "succeeded")
	if job.AttemptCount != 2 || job.ResultCode == nil || *job.ResultCode != "no_processor" {
		t.Fatalf("recovered shutdown job = %+v", job)
	}
}
