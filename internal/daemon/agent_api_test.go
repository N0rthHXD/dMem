package daemon

import (
	"context"
	"net/http"
	"testing"

	"github.com/N0rthHXD/dMem/internal/application"
	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestAgentMemoryAPI(t *testing.T) {
	cfg := testConfig(t)
	daemon, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close(context.Background())
	client := unixClient(cfg.Endpoint)
	space := createSpace(t, client, "agent-api-space", map[string]any{
		"space_type": "user", "external_id": "agent-api", "display_name": "Agent API",
	})

	rememberBody := map[string]any{
		"space_id": space.SpaceID, "content": "The user prefers concise Go examples", "kind": "preference",
	}
	response := requestJSON(t, client, http.MethodPost, "/v1/remember", "agent-remember", rememberBody)
	if response.StatusCode != http.StatusCreated || response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("remember status/header = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var remembered application.RememberResult
	decodeResponse(t, response, &remembered)
	if remembered.Memory.CurrentRevision.DisplayContent != rememberBody["content"] || remembered.EventID == "" || len(remembered.Memory.CurrentRevision.Evidence) != 1 || remembered.Memory.CurrentRevision.Evidence[0].EventID != remembered.EventID {
		t.Fatalf("remember result = %+v", remembered)
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/remember", "agent-remember", rememberBody)
	if response.StatusCode != http.StatusCreated || response.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("remember replay status/header = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var replay application.RememberResult
	decodeResponse(t, response, &replay)
	if replay.Memory.MemoryID != remembered.Memory.MemoryID || replay.EventID != remembered.EventID {
		t.Fatalf("remember replay = %+v, want %+v", replay, remembered)
	}

	response = requestJSON(t, client, http.MethodPost, "/v1/recall", "", map[string]any{
		"query": "concise Go examples", "space_ids": []string{space.SpaceID},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("recall status = %d", response.StatusCode)
	}
	var recalled domain.RecallResponse
	decodeResponse(t, response, &recalled)
	if len(recalled.Context.Items) != 1 || recalled.Context.Items[0].MemoryID != remembered.Memory.MemoryID {
		t.Fatalf("recall result = %+v", recalled)
	}

	feedbackBody := map[string]any{"space_id": space.SpaceID, "feedback_purpose": "memory_quality_feedback", "type": "confirm", "reason_code": "other"}
	response = requestJSON(t, client, http.MethodPost, "/v1/memories/"+remembered.Memory.MemoryID+"/feedback", "agent-feedback", feedbackBody)
	if response.StatusCode != http.StatusCreated || response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("feedback status/header = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var feedback domain.FeedbackAppendResponse
	decodeResponse(t, response, &feedback)
	if feedback.Feedback.RevisionID != remembered.Memory.CurrentRevision.RevisionID {
		t.Fatalf("feedback revision = %q", feedback.Feedback.RevisionID)
	}

	forgetBody := map[string]any{"reason": "user requested deletion"}
	path := "/v1/memories/" + remembered.Memory.MemoryID + "/forget"
	response = requestJSON(t, client, http.MethodPost, path, "agent-forget", forgetBody)
	if response.StatusCode != http.StatusOK || response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("forget status/header = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var forgotten application.ForgetResult
	decodeResponse(t, response, &forgotten)
	if forgotten.Memory.CurrentState != "deleted" {
		t.Fatalf("forgotten memory = %+v", forgotten.Memory)
	}
	response = requestJSON(t, client, http.MethodPost, path, "agent-forget", forgetBody)
	if response.StatusCode != http.StatusOK || response.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("forget replay status/header = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
}
