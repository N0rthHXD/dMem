package daemon

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	_ "github.com/mattn/go-sqlite3"
)

func TestCandidateReviewAPIConfirmRejectAndReplay(t *testing.T) {
	cfg := testConfig(t)
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "candidate-space", map[string]any{
		"space_type": "user", "external_id": "candidate", "display_name": "Candidate",
	})
	response := requestJSON(t, client, http.MethodPost, "/v1/events", "candidate-event", map[string]any{
		"space_id": space.SpaceID, "source_type": "manual", "source_name": "test", "occurred_at": "2026-08-21T00:00:00Z",
		"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "candidate evidence"},
	})
	var event domain.IngestResponse
	decodeResponse(t, response, &event)

	db, err := sql.Open("sqlite3", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	confirmedID := "11111111-1111-4111-8111-111111111111"
	rejectedID := "22222222-2222-4222-8222-222222222222"
	seedCandidateAPI(t, db, confirmedID, "33333333-3333-4333-8333-333333333333", event.EventID, space.SpaceID, "confirmable candidate", "spec-confirm")
	seedCandidateAPI(t, db, rejectedID, "44444444-4444-4444-8444-444444444444", event.EventID, space.SpaceID, "rejectable candidate", "spec-reject")

	response = requestJSON(t, client, http.MethodGet, "/v1/candidates?space_id="+url.QueryEscape(space.SpaceID)+"&review_state=pending&limit=1", "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list candidates status = %d", response.StatusCode)
	}
	var page domain.CandidatePage
	decodeResponse(t, response, &page)
	if len(page.Items) != 1 || page.NextCursor == nil || page.Items[0].ReviewState != "pending" {
		t.Fatalf("candidate page = %+v", page)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/candidates/"+confirmedID, "", nil)
	var candidate domain.Candidate
	decodeResponse(t, response, &candidate)
	if candidate.CandidateID != confirmedID || len(candidate.Evidence) != 1 || candidate.ProcessorIdentity != "processor/api" {
		t.Fatalf("candidate get = %+v", candidate)
	}

	review := map[string]any{"reason_code": "accepted", "note": "reviewed by API"}
	response = requestJSON(t, client, http.MethodPost, "/v1/candidates/"+confirmedID+"/confirm", "confirm-candidate", review)
	if response.StatusCode != http.StatusCreated || response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("confirm status/replay = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var memory domain.Memory
	decodeResponse(t, response, &memory)
	if memory.CurrentState != "active" || memory.CurrentRevision.DisplayContent != "confirmable candidate" || memory.CurrentRevision.Confidence != 0.75 {
		t.Fatalf("confirmed memory = %+v", memory)
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/candidates/"+confirmedID+"/confirm", "confirm-candidate", review)
	if response.StatusCode != http.StatusCreated || response.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("confirm replay = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	response.Body.Close()

	response = requestJSON(t, client, http.MethodPost, "/v1/candidates/"+rejectedID+"/reject", "reject-candidate", map[string]any{"reason_code": "unsupported"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reject status = %d", response.StatusCode)
	}
	decodeResponse(t, response, &candidate)
	if candidate.ReviewState != "rejected" || len(candidate.Reviews) != 1 {
		t.Fatalf("rejected candidate = %+v", candidate)
	}
	var memories, fts, reviews int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM memories), (SELECT count(*) FROM memory_fts), (SELECT count(*) FROM candidate_reviews)`).Scan(&memories, &fts, &reviews); err != nil || memories != 1 || fts != 1 || reviews != 2 {
		t.Fatalf("candidate API rows = memories %d fts %d reviews %d, %v", memories, fts, reviews, err)
	}
}

func seedCandidateAPI(t *testing.T, db *sql.DB, candidateID, evidenceID, eventID, spaceID, content, specificationHash string) {
	t.Helper()
	createdAt := "2026-08-21T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO candidates(candidate_id, event_id, space_id, memory_type, content, content_hash, confidence, sensitivity, suggestion_kind, processor_identity, processing_version, specification_hash, created_at) VALUES (?, ?, ?, 'fact', ?, 'content-hash', 0.75, 'normal', 'new', 'processor/api', 'api-v1', ?, ?)`, candidateID, eventID, spaceID, content, specificationHash, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO candidate_evidence(evidence_id, candidate_id, event_id, created_at) VALUES (?, ?, ?, ?)`, evidenceID, candidateID, eventID, createdAt); err != nil {
		t.Fatal(err)
	}
}
