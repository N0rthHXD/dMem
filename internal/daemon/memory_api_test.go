package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	_ "github.com/mattn/go-sqlite3"
)

func TestMemoryHTTPCreateGetListReviseAndHistory(t *testing.T) {
	cfg := testConfig(t)
	d, release, client := startHeldDaemon(t, cfg)
	defer func() { close(release); _ = d.Close(context.Background()) }()

	space := createSpace(t, client, "memory-basic-space", map[string]any{
		"space_type": "user", "external_id": "memory-basic", "display_name": "Memory Basic",
	})
	event := ingestManualEvent(t, client, "memory-basic-event", space.SpaceID, "Original evidence")
	createBody := memoryCreateBody(space.SpaceID, event.EventID, "The launch is Friday")
	createBody["kind"] = "plan"
	createBody["sensitivity"] = "sensitive"
	createBody["valid_from"] = "2026-08-21T10:00:00+02:00"
	createBody["valid_until"] = "2026-08-22T10:00:00+02:00"
	createBody["evidence"] = []map[string]any{{
		"event_id": event.EventID, "role": "direct", "start_offset": 0, "end_offset": 8,
	}}

	created, response := createMemory(t, client, "memory-create", createBody)
	if response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("create replay header = %q", response.Header.Get("Idempotent-Replay"))
	}
	assertMemoryFields(t, created, space.SpaceID, "plan", "active", false, "sensitive", "The launch is Friday")
	if created.CurrentRevision.ValidFrom == nil || created.CurrentRevision.ValidFrom.Format(time.RFC3339) != "2026-08-21T08:00:00Z" ||
		created.CurrentRevision.ValidUntil == nil || created.CurrentRevision.ValidUntil.Format(time.RFC3339) != "2026-08-22T08:00:00Z" {
		t.Fatalf("validity fields = %v/%v", created.CurrentRevision.ValidFrom, created.CurrentRevision.ValidUntil)
	}
	assertEvidence(t, created.CurrentRevision.Evidence, event.EventID, 0, 8)

	response = requestJSON(t, client, http.MethodGet, "/v1/events/"+event.EventID, "", nil)
	var raw domain.RawEvent
	decodeResponse(t, response, &raw)
	if raw.SourceType != "manual" || raw.Content.Text != "Original evidence" || raw.ProcessingStatus == "" {
		t.Fatalf("manual raw event = %+v", raw)
	}

	got := getMemory(t, client, created.MemoryID)
	assertMemoryFields(t, got, space.SpaceID, "plan", "active", false, "sensitive", "The launch is Friday")
	response = requestJSON(t, client, http.MethodGet, "/v1/memories?space_id="+space.SpaceID, "", nil)
	var page domain.MemoryPage
	decodeResponse(t, response, &page)
	if len(page.Items) != 1 || page.Items[0].MemoryID != created.MemoryID || page.NextCursor != nil {
		t.Fatalf("memory list = %+v", page)
	}

	reviseBody := map[string]any{
		"expected_revision_id": created.CurrentRevision.RevisionID,
		"content":              "The launch is Saturday",
		"valid_from":           "2026-08-23T00:00:00Z",
		"evidence": []map[string]any{{
			"event_id": event.EventID, "role": "direct",
		}},
		"reason": "Schedule changed",
	}
	revised, response := reviseMemory(t, client, created.MemoryID, "memory-revise", reviseBody)
	if response.Header.Get("Idempotent-Replay") != "false" || revised.CurrentRevision.RevisionID == created.CurrentRevision.RevisionID {
		t.Fatalf("revise response = replay %q, revision %+v", response.Header.Get("Idempotent-Replay"), revised.CurrentRevision)
	}
	if revised.CurrentRevision.ParentRevisionID == nil || *revised.CurrentRevision.ParentRevisionID != created.CurrentRevision.RevisionID || revised.CurrentRevision.CreatedBy != "test-agent" {
		t.Fatalf("revised fields = %+v", revised.CurrentRevision)
	}
	assertMemoryFields(t, revised, space.SpaceID, "plan", "active", false, "sensitive", "The launch is Saturday")

	response = requestJSON(t, client, http.MethodGet, "/v1/memories/"+created.MemoryID+"/revisions", "", nil)
	var revisions []domain.MemoryRevision
	decodeResponse(t, response, &revisions)
	if len(revisions) != 2 || revisions[0].RevisionID != revised.CurrentRevision.RevisionID || revisions[1].RevisionID != created.CurrentRevision.RevisionID {
		t.Fatalf("revisions = %+v", revisions)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/memories/"+created.MemoryID+"/state-events", "", nil)
	var events []domain.MemoryStateEvent
	decodeResponse(t, response, &events)
	if len(events) != 1 || events[0].MemoryID != created.MemoryID || events[0].RevisionID != created.CurrentRevision.RevisionID || events[0].FromState != nil || events[0].ToState != "active" || events[0].FromFrozen != nil || events[0].ToFrozen || events[0].Reason != "Initial memory" || events[0].CreatedBy != "test-agent" || events[0].OperationID == "" || events[0].StateEventID == "" || events[0].CreatedAt == "" {
		t.Fatalf("initial state event = %+v", events)
	}
}

func TestMemoryValidationIsAtomicAndErrorsAreSafe(t *testing.T) {
	cfg := testConfig(t)
	d, release, client := startHeldDaemon(t, cfg)
	defer func() { close(release); _ = d.Close(context.Background()) }()
	space := createSpace(t, client, "memory-validation-space", map[string]any{
		"space_type": "user", "external_id": "memory-validation", "display_name": "Memory Validation",
	})
	event := ingestManualEvent(t, client, "memory-validation-event", space.SpaceID, "Evidence")

	for name, body := range map[string]map[string]any{
		"empty": {
			"space_id": space.SpaceID, "kind": "fact", "content": "No evidence", "evidence": []any{}, "reason": "Invalid",
		},
		"missing": {
			"space_id": space.SpaceID, "kind": "fact", "content": "Missing evidence", "reason": "Invalid",
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := requestJSON(t, client, http.MethodPost, "/v1/memories", "invalid-evidence-"+name, body)
			assertSafeError(t, response, http.StatusUnprocessableEntity, "invalid_request")
		})
	}
	missingID := "11111111-1111-4111-8111-111111111111"
	body := memoryCreateBody(space.SpaceID, missingID, "Unknown evidence")
	response := requestJSON(t, client, http.MethodPost, "/v1/memories", "invalid-evidence-missing-row", body)
	assertSafeError(t, response, http.StatusUnprocessableEntity, "invalid_request")
	body = memoryCreateBody(space.SpaceID, event.EventID, "Null sensitivity")
	body["sensitivity"] = nil
	response = requestJSON(t, client, http.MethodPost, "/v1/memories", "invalid-null-sensitivity", body)
	assertSafeError(t, response, http.StatusUnprocessableEntity, "invalid_request")
	body = memoryCreateBody(space.SpaceID, event.EventID, "Null offset")
	body["evidence"] = []map[string]any{{"event_id": event.EventID, "role": "direct", "start_offset": nil, "end_offset": nil}}
	response = requestJSON(t, client, http.MethodPost, "/v1/memories", "invalid-null-offset", body)
	assertSafeError(t, response, http.StatusUnprocessableEntity, "invalid_request")

	db, err := sql.Open("sqlite3", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"memories", "memory_revisions", "memory_evidence", "memory_state_events", "memory_projections"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("%s count after rejected creates = %d; want 0", table, count)
		}
	}

	created, _ := createMemory(t, client, "valid-after-invalid", memoryCreateBody(space.SpaceID, event.EventID, "Valid memory"))
	badRevise := map[string]any{
		"expected_revision_id": created.CurrentRevision.RevisionID, "content": "Bad revision",
		"evidence": []map[string]any{{"event_id": missingID, "role": "direct"}}, "reason": "Invalid",
	}
	response = requestJSON(t, client, http.MethodPatch, "/v1/memories/"+created.MemoryID, "bad-revise-evidence", badRevise)
	assertSafeError(t, response, http.StatusUnprocessableEntity, "invalid_request")
	var revisionCount, evidenceCount int
	if err := db.QueryRow("SELECT count(*) FROM memory_revisions WHERE memory_id=?", created.MemoryID).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM memory_evidence me JOIN memory_revisions mr ON mr.revision_id=me.revision_id WHERE mr.memory_id=?", created.MemoryID).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if revisionCount != 1 || evidenceCount != 1 || getMemory(t, client, created.MemoryID).CurrentRevision.RevisionID != created.CurrentRevision.RevisionID {
		t.Fatalf("partial revise rows/current = %d/%d/%+v", revisionCount, evidenceCount, getMemory(t, client, created.MemoryID))
	}
}

func TestMemoryIdempotencyAndRevisionConflict(t *testing.T) {
	cfg := testConfig(t)
	d, release, client := startHeldDaemon(t, cfg)
	defer func() { close(release); _ = d.Close(context.Background()) }()
	space := createSpace(t, client, "memory-idempotency-space", map[string]any{
		"space_type": "user", "external_id": "memory-idempotency", "display_name": "Memory Idempotency",
	})
	event := ingestManualEvent(t, client, "memory-idempotency-event", space.SpaceID, "Evidence")
	createBody := memoryCreateBody(space.SpaceID, event.EventID, "Version one")
	created, _ := createMemory(t, client, "memory-create-replay", createBody)
	replayed, response := createMemory(t, client, "memory-create-replay", createBody)
	if response.Header.Get("Idempotent-Replay") != "true" || replayed.MemoryID != created.MemoryID || replayed.CurrentRevision.RevisionID != created.CurrentRevision.RevisionID {
		t.Fatalf("create replay = header %q, %+v", response.Header.Get("Idempotent-Replay"), replayed)
	}
	conflictingCreate := memoryCreateBody(space.SpaceID, event.EventID, "Different content")
	response = requestJSON(t, client, http.MethodPost, "/v1/memories", "memory-create-replay", conflictingCreate)
	assertSafeError(t, response, http.StatusConflict, "idempotency_conflict")

	reviseBody := memoryReviseBody(created.CurrentRevision.RevisionID, event.EventID, "Version two")
	revised, _ := reviseMemory(t, client, created.MemoryID, "memory-revise-replay", reviseBody)
	revisedReplay, response := reviseMemory(t, client, created.MemoryID, "memory-revise-replay", reviseBody)
	if response.Header.Get("Idempotent-Replay") != "true" || revisedReplay.CurrentRevision.RevisionID != revised.CurrentRevision.RevisionID {
		t.Fatalf("revise replay = header %q, %+v", response.Header.Get("Idempotent-Replay"), revisedReplay)
	}
	conflictingRevise := memoryReviseBody(created.CurrentRevision.RevisionID, event.EventID, "Different revision")
	response = requestJSON(t, client, http.MethodPatch, "/v1/memories/"+created.MemoryID, "memory-revise-replay", conflictingRevise)
	assertSafeError(t, response, http.StatusConflict, "idempotency_conflict")
	response = requestJSON(t, client, http.MethodPatch, "/v1/memories/"+created.MemoryID, "memory-stale-revision", reviseBody)
	assertSafeError(t, response, http.StatusConflict, "revision_conflict")
	if getMemory(t, client, created.MemoryID).CurrentRevision.RevisionID != revised.CurrentRevision.RevisionID {
		t.Fatal("stale revision request changed current revision")
	}
}

func TestMemoryLifecycleFrozenAndVisibility(t *testing.T) {
	cfg := testConfig(t)
	d, release, client := startHeldDaemon(t, cfg)
	defer func() { close(release); _ = d.Close(context.Background()) }()
	space := createSpace(t, client, "memory-lifecycle-space", map[string]any{
		"space_type": "user", "external_id": "memory-lifecycle", "display_name": "Memory Lifecycle",
	})
	event := ingestManualEvent(t, client, "memory-lifecycle-event", space.SpaceID, "Evidence")
	memory, _ := createMemory(t, client, "lifecycle-create", memoryCreateBody(space.SpaceID, event.EventID, "Original"))
	originalRevision := memory.CurrentRevision.RevisionID

	memory, replay := changeMemoryState(t, client, memory, "lifecycle-confirm-noop", "confirm", nil)
	if replay || stateEventCount(t, client, memory.MemoryID) != 1 {
		t.Fatalf("confirm no-op replay/events = %v/%d", replay, stateEventCount(t, client, memory.MemoryID))
	}
	memory, _ = changeMemoryState(t, client, memory, "lifecycle-contest", "contest", nil)
	if memory.CurrentState != "contested" {
		t.Fatalf("contest state = %q", memory.CurrentState)
	}
	memory, replay = changeMemoryState(t, client, memory, "lifecycle-contest-replay", "confirm", nil)
	if replay || memory.CurrentState != "active" {
		t.Fatalf("confirm transition = replay %v, state %q", replay, memory.CurrentState)
	}

	memory, _ = changeMemoryState(t, client, memory, "lifecycle-freeze", "freeze", nil)
	if !memory.Frozen {
		t.Fatal("freeze did not set frozen")
	}
	frozenRevision := memory.CurrentRevision.RevisionID
	memory, response := reviseMemory(t, client, memory.MemoryID, "frozen-manual-revise", memoryReviseBody(frozenRevision, event.EventID, "Explicit frozen revision"))
	if response.StatusCode != http.StatusOK || !memory.Frozen || memory.CurrentRevision.RevisionID == frozenRevision {
		t.Fatalf("explicit frozen revise = status %d, %+v", response.StatusCode, memory)
	}
	memory, _ = changeMemoryState(t, client, memory, "lifecycle-unfreeze", "unfreeze", nil)
	if memory.Frozen {
		t.Fatal("unfreeze left memory frozen")
	}
	memory, _ = changeMemoryState(t, client, memory, "lifecycle-archive", "archive", nil)
	if memory.CurrentState != "archived" {
		t.Fatalf("archive state = %q", memory.CurrentState)
	}
	memory, _ = changeMemoryState(t, client, memory, "lifecycle-restore", "restore", &originalRevision)
	if memory.CurrentState != "active" || memory.CurrentRevision.DisplayContent != "Original" || memory.CurrentRevision.RevisionID == originalRevision {
		t.Fatalf("restore result = %+v", memory)
	}
	memory, _ = changeMemoryState(t, client, memory, "lifecycle-retract", "retract", nil)
	if memory.CurrentState != "retracted" {
		t.Fatalf("retract state = %q", memory.CurrentState)
	}
	assertMemoryListIDs(t, client, space.SpaceID, "", nil)
	assertMemoryListIDs(t, client, space.SpaceID, "retracted", []string{memory.MemoryID})
	if got := getMemory(t, client, memory.MemoryID); got.CurrentState != "retracted" {
		t.Fatalf("direct get of retracted memory = %+v", got)
	}
	memory, _ = changeMemoryState(t, client, memory, "lifecycle-restore-retracted", "restore", &originalRevision)

	memory, replay = changeMemoryState(t, client, memory, "lifecycle-delete", "delete", nil)
	if replay || memory.CurrentState != "deleted" {
		t.Fatalf("delete result = replay %v, %+v", replay, memory)
	}
	deleteBody := stateBody(memory.CurrentRevision.RevisionID, "delete", nil, "delete memory")
	response = requestJSON(t, client, http.MethodPost, "/v1/memories/"+memory.MemoryID+"/state", "lifecycle-delete", deleteBody)
	if response.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("lifecycle replay header = %q", response.Header.Get("Idempotent-Replay"))
	}
	response.Body.Close()
	conflictBody := stateBody(memory.CurrentRevision.RevisionID, "delete", nil, "different reason")
	response = requestJSON(t, client, http.MethodPost, "/v1/memories/"+memory.MemoryID+"/state", "lifecycle-delete", conflictBody)
	assertSafeError(t, response, http.StatusConflict, "idempotency_conflict")

	beforeInvalid := stateEventCount(t, client, memory.MemoryID)
	response = requestJSON(t, client, http.MethodPost, "/v1/memories/"+memory.MemoryID+"/state", "invalid-after-delete", stateBody(memory.CurrentRevision.RevisionID, "archive", nil, "not allowed"))
	assertSafeError(t, response, http.StatusConflict, "invalid_state_transition")
	if stateEventCount(t, client, memory.MemoryID) != beforeInvalid {
		t.Fatal("invalid transition added a state event")
	}
	assertMemoryListIDs(t, client, space.SpaceID, "", nil)
	assertMemoryListIDs(t, client, space.SpaceID, "deleted", []string{memory.MemoryID})
	response = requestJSON(t, client, http.MethodGet, "/v1/memories/"+memory.MemoryID, "", nil)
	assertSafeError(t, response, http.StatusNotFound, "not_found")
	response = requestJSON(t, client, http.MethodPatch, "/v1/memories/"+memory.MemoryID, "revise-after-delete", memoryReviseBody(memory.CurrentRevision.RevisionID, event.EventID, "Must restore first"))
	assertSafeError(t, response, http.StatusNotFound, "not_found")
	response = requestJSON(t, client, http.MethodGet, "/v1/memories/"+memory.MemoryID+"/revisions", "", nil)
	var revisions []domain.MemoryRevision
	decodeResponse(t, response, &revisions)
	if len(revisions) != 4 {
		t.Fatalf("deleted revision history length = %d; want 4", len(revisions))
	}
	if stateEventCount(t, client, memory.MemoryID) != beforeInvalid {
		t.Fatal("deleted state-event history changed after reads")
	}
}

func TestMemoryListPaginationAndScope(t *testing.T) {
	cfg := testConfig(t)
	d, release, client := startHeldDaemon(t, cfg)
	defer func() { close(release); _ = d.Close(context.Background()) }()
	root := createSpace(t, client, "scope-root", map[string]any{"space_type": "user", "external_id": "scope-root", "display_name": "Root"})
	project := createSpace(t, client, "scope-project", map[string]any{"parent_space_id": root.SpaceID, "space_type": "project", "external_id": "scope-project", "display_name": "Project"})
	task := createSpace(t, client, "scope-task", map[string]any{"parent_space_id": project.SpaceID, "space_type": "task", "external_id": "scope-task", "display_name": "Task"})
	sibling := createSpace(t, client, "scope-sibling", map[string]any{"parent_space_id": root.SpaceID, "space_type": "project", "external_id": "scope-sibling", "display_name": "Sibling"})
	descendant := createSpace(t, client, "scope-descendant", map[string]any{"parent_space_id": task.SpaceID, "space_type": "session", "external_id": "scope-descendant", "display_name": "Descendant"})
	agent := createSpace(t, client, "scope-agent", map[string]any{"parent_space_id": root.SpaceID, "space_type": "agent", "external_id": "scope-agent", "display_name": "Agent"})

	spaces := []domain.Space{root, project, task, sibling, descendant, agent}
	ids := make(map[string]string, len(spaces))
	for i, space := range spaces {
		event := ingestManualEvent(t, client, fmt.Sprintf("scope-event-%d", i), space.SpaceID, space.DisplayName)
		memory, _ := createMemory(t, client, fmt.Sprintf("scope-memory-%d", i), memoryCreateBody(space.SpaceID, event.EventID, space.DisplayName+" memory"))
		ids[space.SpaceID] = memory.MemoryID
	}
	assertMemoryListSet(t, client, "/v1/memories?space_id="+task.SpaceID, []string{ids[root.SpaceID], ids[project.SpaceID], ids[task.SpaceID]})
	assertMemoryListSet(t, client, "/v1/memories?space_id="+task.SpaceID+"&include_ancestors=false", []string{ids[task.SpaceID]})
	assertMemoryListSet(t, client, "/v1/memories?space_id="+agent.SpaceID, []string{ids[agent.SpaceID], ids[root.SpaceID]})
	assertMemoryListSet(t, client, "/v1/memories?space_id="+agent.SpaceID+"&include_ancestors=false", []string{ids[agent.SpaceID]})

	full := memoryPage(t, client, "/v1/memories?space_id="+task.SpaceID+"&limit=200")
	var paged []string
	cursor := ""
	for {
		path := "/v1/memories?space_id=" + task.SpaceID + "&limit=1"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page := memoryPage(t, client, path)
		if len(page.Items) != 1 {
			t.Fatalf("pagination page items = %d; want 1", len(page.Items))
		}
		paged = append(paged, page.Items[0].MemoryID)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(paged) != len(full.Items) {
		t.Fatalf("paged IDs = %v; full page = %+v", paged, full)
	}
	for i := range paged {
		if paged[i] != full.Items[i].MemoryID {
			t.Fatalf("pagination order = %v; full order differs at %d", paged, i)
		}
	}
}

func TestRestartIdempotentReviseDoesNotDuplicateRevisions(t *testing.T) {
	cfg := testConfig(t)
	d, release, client := startHeldDaemon(t, cfg)
	space := createSpace(t, client, "restart-space", map[string]any{"space_type": "user", "external_id": "restart", "display_name": "Restart"})
	event := ingestManualEvent(t, client, "restart-event", space.SpaceID, "Evidence")
	memory, _ := createMemory(t, client, "restart-memory", memoryCreateBody(space.SpaceID, event.EventID, "Before restart"))
	body := memoryReviseBody(memory.CurrentRevision.RevisionID, event.EventID, "After restart")
	revised, _ := reviseMemory(t, client, memory.MemoryID, "restart-revise", body)
	close(release)
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	d, release, client = startHeldDaemon(t, cfg)
	defer func() { close(release); _ = d.Close(context.Background()) }()
	replay, response := reviseMemory(t, client, memory.MemoryID, "restart-revise", body)
	if response.Header.Get("Idempotent-Replay") != "true" || replay.CurrentRevision.RevisionID != revised.CurrentRevision.RevisionID {
		t.Fatalf("restart replay = header %q, %+v", response.Header.Get("Idempotent-Replay"), replay)
	}
	response = requestJSON(t, client, http.MethodGet, "/v1/memories/"+memory.MemoryID+"/revisions", "", nil)
	var revisions []domain.MemoryRevision
	decodeResponse(t, response, &revisions)
	if len(revisions) != 2 {
		t.Fatalf("revision count after restart retry = %d; want 2", len(revisions))
	}
}

func startHeldDaemon(t *testing.T, cfg config.Config) (*Daemon, chan struct{}, *http.Client) {
	t.Helper()
	release := make(chan struct{})
	d, err := start(context.Background(), cfg, &heldProcessor{release: release})
	if err != nil {
		t.Fatal(err)
	}
	return d, release, unixClient(cfg.Endpoint)
}

func ingestManualEvent(t *testing.T, client *http.Client, key, spaceID, text string) domain.IngestResponse {
	t.Helper()
	response := requestJSON(t, client, http.MethodPost, "/v1/events", key, map[string]any{
		"space_id": spaceID, "source_type": "manual", "source_name": "memory-api-test",
		"occurred_at": "2026-08-21T00:00:00Z",
		"content":     map[string]any{"kind": "text", "media_type": "text/plain", "text": text},
		"metadata":    map[string]any{"suite": "memory-api"},
	})
	if response.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("ingest manual event status = %d: %s", response.StatusCode, data)
	}
	var result domain.IngestResponse
	decodeResponse(t, response, &result)
	if result.EventID == "" || result.OperationID == "" || len(result.JobIDs) != 1 || result.AcceptedAt == "" {
		t.Fatalf("ingest response = %+v", result)
	}
	return result
}

func memoryCreateBody(spaceID, eventID, content string) map[string]any {
	return map[string]any{
		"space_id": spaceID, "kind": "fact", "content": content,
		"evidence": []map[string]any{{"event_id": eventID, "role": "direct"}},
		"reason":   "Initial memory",
	}
}

func memoryReviseBody(revisionID, eventID, content string) map[string]any {
	return map[string]any{
		"expected_revision_id": revisionID, "content": content,
		"evidence": []map[string]any{{"event_id": eventID, "role": "direct"}},
		"reason":   "Manual revision",
	}
}

func createMemory(t *testing.T, client *http.Client, key string, body any) (domain.Memory, *http.Response) {
	t.Helper()
	response := requestJSON(t, client, http.MethodPost, "/v1/memories", key, body)
	if response.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("create memory status = %d: %s", response.StatusCode, data)
	}
	var result domain.Memory
	decodeResponse(t, response, &result)
	return result, response
}

func reviseMemory(t *testing.T, client *http.Client, memoryID, key string, body any) (domain.Memory, *http.Response) {
	t.Helper()
	response := requestJSON(t, client, http.MethodPatch, "/v1/memories/"+memoryID, key, body)
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("revise memory status = %d: %s", response.StatusCode, data)
	}
	var result domain.Memory
	decodeResponse(t, response, &result)
	return result, response
}

func getMemory(t *testing.T, client *http.Client, memoryID string) domain.Memory {
	t.Helper()
	response := requestJSON(t, client, http.MethodGet, "/v1/memories/"+memoryID, "", nil)
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("get memory status = %d: %s", response.StatusCode, data)
	}
	var result domain.Memory
	decodeResponse(t, response, &result)
	return result
}

func assertMemoryFields(t *testing.T, memory domain.Memory, spaceID, memoryType, state string, frozen bool, sensitivity, content string) {
	t.Helper()
	if memory.MemoryID == "" || memory.SpaceID != spaceID || string(memory.Kind) != memoryType || memory.CurrentState != state || memory.Frozen != frozen || memory.Sensitivity != sensitivity || memory.CurrentRevision.MemoryID != memory.MemoryID || memory.CurrentRevision.RevisionID == "" || memory.CurrentRevision.DisplayContent != content || memory.CurrentRevision.CreatedBy != "test-agent" || memory.CurrentRevision.CreatedAt == "" || memory.CreatedAt == "" || memory.UpdatedAt == "" {
		t.Fatalf("memory fields = %+v", memory)
	}
}

func assertEvidence(t *testing.T, evidence []domain.RevisionEvidence, eventID string, start, end int64) {
	t.Helper()
	if len(evidence) != 1 || evidence[0].EventID != eventID || evidence[0].Role != domain.EvidenceRoleDirect || evidence[0].StartOffset == nil || *evidence[0].StartOffset != start || evidence[0].EndOffset == nil || *evidence[0].EndOffset != end {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func stateBody(revisionID, action string, restoreRevisionID *string, reason string) map[string]any {
	body := map[string]any{"expected_revision_id": revisionID, "action": action, "reason": reason}
	if restoreRevisionID != nil {
		body["restore_revision_id"] = *restoreRevisionID
	}
	return body
}

func changeMemoryState(t *testing.T, client *http.Client, current domain.Memory, key, action string, restoreRevisionID *string) (domain.Memory, bool) {
	t.Helper()
	response := requestJSON(t, client, http.MethodPost, "/v1/memories/"+current.MemoryID+"/state", key, stateBody(current.CurrentRevision.RevisionID, action, restoreRevisionID, action+" memory"))
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("%s memory status = %d: %s", action, response.StatusCode, data)
	}
	replay := response.Header.Get("Idempotent-Replay") == "true"
	var result domain.Memory
	decodeResponse(t, response, &result)
	return result, replay
}

func stateEventCount(t *testing.T, client *http.Client, memoryID string) int {
	t.Helper()
	response := requestJSON(t, client, http.MethodGet, "/v1/memories/"+memoryID+"/state-events", "", nil)
	var events []domain.MemoryStateEvent
	decodeResponse(t, response, &events)
	return len(events)
}

func memoryPage(t *testing.T, client *http.Client, path string) domain.MemoryPage {
	t.Helper()
	response := requestJSON(t, client, http.MethodGet, path, "", nil)
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("list memories status = %d: %s", response.StatusCode, data)
	}
	var result domain.MemoryPage
	decodeResponse(t, response, &result)
	return result
}

func assertMemoryListIDs(t *testing.T, client *http.Client, spaceID, state string, want []string) {
	t.Helper()
	path := "/v1/memories?space_id=" + spaceID
	if state != "" {
		path += "&state=" + state
	}
	page := memoryPage(t, client, path)
	if len(page.Items) != len(want) {
		t.Fatalf("list %q items = %+v; want IDs %v", state, page.Items, want)
	}
	for i := range want {
		if page.Items[i].MemoryID != want[i] {
			t.Fatalf("list %q item %d = %q; want %q", state, i, page.Items[i].MemoryID, want[i])
		}
	}
}

func assertMemoryListSet(t *testing.T, client *http.Client, path string, want []string) {
	t.Helper()
	page := memoryPage(t, client, path)
	got := make(map[string]bool, len(page.Items))
	for _, item := range page.Items {
		got[item.MemoryID] = true
	}
	if len(got) != len(want) {
		t.Fatalf("list %s IDs = %v; want %v", path, got, want)
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("list %s missing %s: %v", path, id, got)
		}
	}
}

func assertSafeError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	if response.StatusCode != status {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, status, data)
	}
	var result api.Error
	decodeResponse(t, response, &result)
	if result.ErrorCode != code || result.Message == "" || result.RequestID == "" || result.OperationID != "" || result.Retryable || result.RetryAfter != nil || len(result.Details) != 0 {
		t.Fatalf("unsafe or malformed error = %+v", result)
	}
	lower := strings.ToLower(result.Message)
	for _, leaked := range []string{"sqlite", "select ", "insert ", "update ", "delete from", "memory_revisions", "raw_events", ".db"} {
		if strings.Contains(lower, leaked) {
			t.Fatalf("error message leaks internal detail %q: %+v", leaked, result)
		}
	}
}
