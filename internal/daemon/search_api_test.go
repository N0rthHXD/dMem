package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestKeywordSearchScopeFiltersValidityEvidenceAndLifecycle(t *testing.T) {
	cfg := testConfig(t)
	d, release, client := startHeldDaemon(t, cfg)
	defer func() { close(release); _ = d.Close(context.Background()) }()
	root := createSpace(t, client, "e-root", map[string]any{"space_type": "user", "external_id": "e-root", "display_name": "Root"})
	project := createSpace(t, client, "e-project", map[string]any{"parent_space_id": root.SpaceID, "space_type": "project", "external_id": "e-project", "display_name": "Project"})
	task := createSpace(t, client, "e-task", map[string]any{"parent_space_id": project.SpaceID, "space_type": "task", "external_id": "e-task", "display_name": "Task"})
	session := createSpace(t, client, "e-session", map[string]any{"parent_space_id": task.SpaceID, "space_type": "session", "external_id": "e-session", "display_name": "Session"})
	sibling := createSpace(t, client, "e-sibling", map[string]any{"parent_space_id": root.SpaceID, "space_type": "project", "external_id": "e-sibling", "display_name": "Sibling"})
	agent := createSpace(t, client, "e-agent", map[string]any{"parent_space_id": root.SpaceID, "space_type": "agent", "external_id": "e-agent", "display_name": "Agent"})

	spaces := []domain.Space{root, project, task, session, sibling, agent}
	memories := map[string]domain.Memory{}
	for i, space := range spaces {
		event := ingestManualEvent(t, client, "e-event-"+space.ExternalID, space.SpaceID, "private raw evidence "+space.DisplayName)
		body := memoryCreateBody(space.SpaceID, event.EventID, "zephyr "+space.DisplayName)
		if i == 2 {
			body["memory_type"] = "decision"
			body["valid_from"] = "2026-08-21T12:00:00+02:00"
			body["valid_until"] = "2026-08-21T14:00:00+02:00"
		}
		memories[space.SpaceID], _ = createMemory(t, client, "e-memory-"+space.ExternalID, body)
	}

	result := search(t, client, map[string]any{"query": "zephyr", "space_ids": []string{session.SpaceID}, "reference_time": "2026-08-21T11:00:00Z", "include_evidence": true})
	assertSearchSet(t, result, []string{memories[root.SpaceID].MemoryID, memories[project.SpaceID].MemoryID, memories[task.SpaceID].MemoryID, memories[session.SpaceID].MemoryID})
	for _, item := range result.Results {
		if item.Rank < 1 || item.SelectionReason != "keyword_match" || math.IsNaN(item.KeywordScore) {
			t.Fatalf("invalid search result metadata: %+v", item)
		}
		if len(item.Memory.CurrentRevision.Evidence) != 1 {
			t.Fatalf("evidence refs = %+v", item.Memory.CurrentRevision.Evidence)
		}
		encoded, _ := json.Marshal(item)
		if strings.Contains(string(encoded), "private raw evidence") || strings.Contains(string(encoded), "projection_id") || strings.Contains(string(encoded), "rowid") {
			t.Fatalf("search leaked internal/raw content: %s", encoded)
		}
	}
	withoutAncestors := search(t, client, map[string]any{"query": "zephyr", "space_ids": []string{task.SpaceID}, "include_ancestors": false, "reference_time": "2026-08-21T10:00:00Z", "memory_types": []string{"decision"}})
	assertSearchSet(t, withoutAncestors, []string{memories[task.SpaceID].MemoryID})
	atUntil := search(t, client, map[string]any{"query": "zephyr", "space_ids": []string{task.SpaceID}, "include_ancestors": false, "reference_time": "2026-08-21T12:00:00Z"})
	if len(atUntil.Results) != 0 {
		t.Fatalf("valid_until exclusive result = %+v", atUntil.Results)
	}
	union := search(t, client, map[string]any{"query": "zephyr", "space_ids": []string{task.SpaceID, agent.SpaceID}, "reference_time": "2026-08-21T11:00:00Z"})
	assertSearchSet(t, union, []string{memories[root.SpaceID].MemoryID, memories[project.SpaceID].MemoryID, memories[task.SpaceID].MemoryID, memories[agent.SpaceID].MemoryID})

	taskMemory := memories[task.SpaceID]
	taskMemory, _ = changeMemoryState(t, client, taskMemory, "e-contest", "contest", nil)
	if got := search(t, client, map[string]any{"query": "zephyr", "space_ids": []string{task.SpaceID}, "include_ancestors": false, "states": []string{"contested"}, "reference_time": "2026-08-21T11:00:00Z"}); len(got.Results) != 1 || got.Results[0].Memory.CurrentState != "contested" {
		t.Fatalf("explicit contested result = %+v", got)
	}
	if got := search(t, client, map[string]any{"query": "zephyr", "space_ids": []string{task.SpaceID}, "include_ancestors": false, "reference_time": "2026-08-21T11:00:00Z"}); len(got.Results) != 1 || got.Results[0].Memory.CurrentState != "contested" {
		t.Fatalf("default states omitted contested: %+v", got)
	}
	taskMemory, _ = changeMemoryState(t, client, taskMemory, "e-confirm", "confirm", nil)
	revised, _ := reviseMemory(t, client, taskMemory.MemoryID, "e-revise", memoryReviseBody(taskMemory.CurrentRevision.RevisionID, taskMemory.CurrentRevision.Evidence[0].EventID, "aurora replacement"))
	if len(search(t, client, map[string]any{"query": "zephyr", "space_ids": []string{task.SpaceID}, "include_ancestors": false}).Results) != 0 || len(search(t, client, map[string]any{"query": "aurora", "space_ids": []string{task.SpaceID}, "include_ancestors": false}).Results) != 1 {
		t.Fatal("revision did not replace FTS text")
	}
	archived, _ := changeMemoryState(t, client, revised, "e-archive", "archive", nil)
	if len(search(t, client, map[string]any{"query": "aurora", "space_ids": []string{task.SpaceID}, "include_ancestors": false}).Results) != 0 {
		t.Fatal("archived memory remained searchable")
	}
	restored, _ := changeMemoryState(t, client, archived, "e-restore", "restore", &revised.CurrentRevision.RevisionID)
	got := search(t, client, map[string]any{"query": "aurora", "space_ids": []string{task.SpaceID}, "include_ancestors": false})
	if len(got.Results) != 1 || got.Results[0].Memory.CurrentRevision.RevisionID != restored.CurrentRevision.RevisionID || restored.CurrentRevision.RevisionID == revised.CurrentRevision.RevisionID {
		t.Fatalf("restore search result = %+v", got)
	}
	db, err := sql.Open("sqlite3", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE memory_projections SET display_content='phantom mismatch' WHERE memory_id=?`, restored.MemoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM memory_fts WHERE memory_id=?`, restored.MemoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_fts(memory_id, revision_id, content) VALUES (?, ?, 'phantom mismatch')`, restored.MemoryID, restored.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	if mismatched := search(t, client, map[string]any{"query": "phantom", "space_ids": []string{task.SpaceID}, "include_ancestors": false}); len(mismatched.Results) != 0 {
		t.Fatalf("mismatched derived data returned authoritative object: %+v", mismatched)
	}
}

func TestSearchBudgetPaginationValidationDegradedAndRebuild(t *testing.T) {
	cfg := testConfig(t)
	d, release, client := startHeldDaemon(t, cfg)
	defer d.Close(context.Background())
	root := createSpace(t, client, "ep-root", map[string]any{"space_type": "user", "external_id": "ep-root", "display_name": "Root"})
	for _, content := range []string{"分页 星星", "分页 月亮", "分页 太阳"} {
		event := ingestManualEvent(t, client, "ep-event-"+content, root.SpaceID, "evidence")
		createMemory(t, client, "ep-memory-"+content, memoryCreateBody(root.SpaceID, event.EventID, content))
	}
	first := search(t, client, map[string]any{"query": "分页", "space_ids": []string{root.SpaceID}, "limit": 1, "max_chars": 5})
	if len(first.Results) != 1 || first.CharsUsed != 5 || !first.Truncated || first.NextCursor == nil {
		t.Fatalf("budget page 1 = %+v", first)
	}
	second := search(t, client, map[string]any{"query": "分页", "space_ids": []string{root.SpaceID}, "limit": 1, "max_chars": 5, "cursor": *first.NextCursor})
	if len(second.Results) != 1 || second.Results[0].Memory.MemoryID == first.Results[0].Memory.MemoryID {
		t.Fatalf("budget page 2 = %+v", second)
	}
	largeEvent := ingestManualEvent(t, client, "budget-large-event", root.SpaceID, "evidence")
	large, _ := createMemory(t, client, "budget-large-memory", memoryCreateBody(root.SpaceID, largeEvent.EventID, "budgetword 12345678"))
	smallEvent := ingestManualEvent(t, client, "budget-small-event", root.SpaceID, "evidence")
	small, _ := createMemory(t, client, "budget-small-memory", memoryCreateBody(root.SpaceID, smallEvent.EventID, "budgetword"))
	budgetFirst := search(t, client, map[string]any{"query": "budgetword", "space_ids": []string{root.SpaceID}, "limit": 2, "max_chars": 20})
	if len(budgetFirst.Results) != 1 || budgetFirst.Results[0].Memory.MemoryID != small.MemoryID || budgetFirst.NextCursor == nil || !budgetFirst.Truncated {
		t.Fatalf("remaining-budget first page = %+v", budgetFirst)
	}
	budgetSecond := search(t, client, map[string]any{"query": "budgetword", "space_ids": []string{root.SpaceID}, "limit": 2, "max_chars": 20, "cursor": *budgetFirst.NextCursor})
	if len(budgetSecond.Results) != 1 || budgetSecond.Results[0].Memory.MemoryID != large.MemoryID {
		t.Fatalf("deferred budget candidate = %+v", budgetSecond)
	}
	emptyStates := search(t, client, map[string]any{"query": "分页", "space_ids": []string{root.SpaceID}, "states": []string{}})
	emptyTypes := search(t, client, map[string]any{"query": "分页", "space_ids": []string{root.SpaceID}, "memory_types": []string{}})
	if len(emptyStates.Results) != 0 || len(emptyTypes.Results) != 0 {
		t.Fatalf("explicit empty filters returned results: states=%+v types=%+v", emptyStates, emptyTypes)
	}
	badCursor := requestJSON(t, client, http.MethodPost, "/v1/search", "", map[string]any{"query": "different", "space_ids": []string{root.SpaceID}, "limit": 1, "max_chars": 5, "cursor": *first.NextCursor})
	assertSafeError(t, badCursor, http.StatusUnprocessableEntity, "invalid_request")
	for _, body := range []map[string]any{
		{"query": "", "space_ids": []string{root.SpaceID}},
		{"query": "分页", "space_ids": []string{root.SpaceID, root.SpaceID}},
		{"query": "分页", "space_ids": []string{"11111111-1111-4111-8111-111111111111"}},
		{"query": "分页", "space_ids": []string{root.SpaceID}, "unknown": true},
	} {
		assertSafeError(t, requestJSON(t, client, http.MethodPost, "/v1/search", "", body), http.StatusUnprocessableEntity, "invalid_request")
	}
	special := requestJSON(t, client, http.MethodPost, "/v1/search", "", map[string]any{"query": `NEAR(`, "space_ids": []string{root.SpaceID}})
	if special.StatusCode == http.StatusOK {
		special.Body.Close()
	} else {
		assertSafeError(t, special, http.StatusUnprocessableEntity, "invalid_request")
	}
	semantic := requestJSON(t, client, http.MethodPost, "/v1/search", "", map[string]any{"query": "分页", "space_ids": []string{root.SpaceID}, "mode": "semantic"})
	if semantic.StatusCode != http.StatusServiceUnavailable {
		data, _ := io.ReadAll(semantic.Body)
		semantic.Body.Close()
		t.Fatalf("semantic unavailable status = %d: %s", semantic.StatusCode, data)
	}
	var semanticError api.Error
	decodeResponse(t, semantic, &semanticError)
	if semanticError.ErrorCode != "index_degraded" || !semanticError.Retryable || semanticError.Message == "" || semanticError.RequestID == "" {
		t.Fatalf("semantic unavailable error = %+v", semanticError)
	}

	db, err := sql.Open("sqlite3", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE index_metadata SET state='degraded' WHERE index_name='memory_fts'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM memory_fts WHERE memory_id=(SELECT memory_id FROM memory_projections ORDER BY memory_id LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_fts(memory_id, revision_id, content) VALUES ('stale-memory', 'stale-revision', '分页 stale')`); err != nil {
		t.Fatal(err)
	}
	degraded := search(t, client, map[string]any{"query": "分页", "space_ids": []string{root.SpaceID}})
	if degraded.IndexState != "degraded" {
		t.Fatalf("degraded search state = %q", degraded.IndexState)
	}
	response := requestJSON(t, client, http.MethodGet, "/v1/health", "", nil)
	var health api.Health
	decodeResponse(t, response, &health)
	if health.FTSState != "degraded" || health.FTSFreshness != "stale" || health.Mode != "degraded" {
		t.Fatalf("degraded health = %+v", health)
	}

	rebuildBody := map[string]any{}
	response = requestJSON(t, client, http.MethodPost, "/v1/indexes/fts/rebuild", "ep-rebuild", rebuildBody)
	if response.StatusCode != http.StatusAccepted || response.Header.Get("Idempotent-Replay") != "false" {
		t.Fatalf("rebuild status/header = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var job domain.JobReference
	decodeResponse(t, response, &job)
	replay := requestJSON(t, client, http.MethodPost, "/v1/indexes/fts/rebuild", "ep-rebuild", rebuildBody)
	if replay.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("rebuild replay header = %q", replay.Header.Get("Idempotent-Replay"))
	}
	replay.Body.Close()
	active := requestJSON(t, client, http.MethodPost, "/v1/indexes/fts/rebuild", "ep-rebuild-active", rebuildBody)
	if active.StatusCode != http.StatusConflict {
		t.Fatalf("active rebuild status = %d", active.StatusCode)
	}
	var conflict api.Error
	decodeResponse(t, active, &conflict)
	if conflict.ErrorCode != "job_already_active" || conflict.Details["job_id"] != job.JobID {
		t.Fatalf("active rebuild conflict = %+v", conflict)
	}
	close(release)
	waitForJobStatus(t, client, job.JobID, "succeeded")
	after := search(t, client, map[string]any{"query": "分页", "space_ids": []string{root.SpaceID}})
	if after.IndexState != "ready" || len(after.Results) != 3 {
		t.Fatalf("post-rebuild search = %+v", after)
	}
	var stale int
	if err := db.QueryRow(`SELECT count(*) FROM memory_fts WHERE memory_id='stale-memory'`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("stale FTS rows after rebuild = %d, %v", stale, err)
	}
}

func TestSearchDiskHardLimitAndCommittedReplay(t *testing.T) {
	normal := testConfig(t)
	d, release, client := startHeldDaemon(t, normal)
	root := createSpace(t, client, "hard-root", map[string]any{"space_type": "user", "external_id": "hard-root", "display_name": "Hard Root"})
	event := ingestManualEvent(t, client, "hard-event", root.SpaceID, "hard evidence")
	memory, _ := createMemory(t, client, "hard-memory", memoryCreateBody(root.SpaceID, event.EventID, "before hard pressure"))
	response := requestJSON(t, client, http.MethodPost, "/v1/indexes/fts/rebuild", "committed-before-hard", map[string]any{})
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("normal rebuild status = %d", response.StatusCode)
	}
	var committed domain.JobReference
	decodeResponse(t, response, &committed)
	close(release)
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t)
	cfg.DataDir = normal.DataDir
	cfg.DatabasePath = normal.DatabasePath
	cfg.DiskHardFree = ^uint64(0)
	cfg.DiskSoftFree = ^uint64(0)
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client = unixClient(cfg.Endpoint)
	response = requestJSON(t, client, http.MethodPost, "/v1/indexes/fts/rebuild", "committed-before-hard", map[string]any{})
	if response.StatusCode != http.StatusAccepted || response.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("committed hard-limit replay = %d/%q", response.StatusCode, response.Header.Get("Idempotent-Replay"))
	}
	var replayed domain.JobReference
	decodeResponse(t, response, &replayed)
	if replayed != committed {
		t.Fatalf("hard-limit replay = %+v; want %+v", replayed, committed)
	}
	response = requestJSON(t, client, http.MethodPost, "/v1/indexes/fts/rebuild", "hard-rebuild", map[string]any{})
	if response.StatusCode != http.StatusInsufficientStorage {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("hard limit status = %d: %s", response.StatusCode, data)
	}
	var exhausted api.Error
	decodeResponse(t, response, &exhausted)
	if exhausted.ErrorCode != "resource_exhausted" || exhausted.Retryable || exhausted.Details["resource"] != "disk" || exhausted.Details["pressure"] != "hard" {
		t.Fatalf("hard limit error = %+v", exhausted)
	}
	hardRevise := requestJSON(t, client, http.MethodPatch, "/v1/memories/"+memory.MemoryID, "hard-revise", memoryReviseBody(memory.CurrentRevision.RevisionID, event.EventID, "must not write"))
	if hardRevise.StatusCode != http.StatusInsufficientStorage {
		data, _ := io.ReadAll(hardRevise.Body)
		hardRevise.Body.Close()
		t.Fatalf("hard memory write status = %d: %s", hardRevise.StatusCode, data)
	}
	hardRevise.Body.Close()
	response = requestJSON(t, client, http.MethodGet, "/v1/health", "", nil)
	var health api.Health
	decodeResponse(t, response, &health)
	if health.DiskPressure != "hard" || health.AcceptingWrites || health.Mode != "read_only" {
		t.Fatalf("hard-limit health = %+v", health)
	}
}

func search(t *testing.T, client *http.Client, body map[string]any) domain.SearchResponse {
	t.Helper()
	response := requestJSON(t, client, http.MethodPost, "/v1/search", "", body)
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("search status = %d: %s", response.StatusCode, data)
	}
	var result domain.SearchResponse
	decodeResponse(t, response, &result)
	return result
}

func assertSearchSet(t *testing.T, result domain.SearchResponse, want []string) {
	t.Helper()
	got := make(map[string]bool, len(result.Results))
	for _, item := range result.Results {
		got[item.Memory.MemoryID] = true
	}
	if len(got) != len(want) {
		t.Fatalf("search IDs = %v; want %v", got, want)
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("search IDs = %v; missing %s", got, id)
		}
	}
}
