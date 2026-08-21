package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestSliceEKeywordScopeFiltersValidityEvidenceAndLifecycle(t *testing.T) {
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
	if got := search(t, client, map[string]any{"query": "zephyr", "space_ids": []string{task.SpaceID}, "include_ancestors": false, "reference_time": "2026-08-21T11:00:00Z"}); len(got.Results) != 0 {
		t.Fatalf("default states returned contested: %+v", got)
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

func TestSliceEBudgetPaginationValidationDegradedAndRebuild(t *testing.T) {
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
	assertSafeError(t, semantic, http.StatusUnprocessableEntity, "unsupported")

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

func TestSliceEDiskHardLimitAndCommittedReplay(t *testing.T) {
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

func TestPhaseOneOpenAPIGoldenContractIsLoaded(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "api", "openapi.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	spec := string(data)
	for _, declaration := range []string{"openapi: 3.1.0", "  /search:", "      operationId: searchMemories", "  /indexes/fts/rebuild:", "      operationId: rebuildFts", "required: [query, space_ids]", "required: [results, truncated, chars_used, index_state]", "required: [memory, rank, keyword_score, selection_reason]", "Idempotent-Replay", "X-Caller-ID", "Idempotency-Key", "'202':", "'507':"} {
		if !strings.Contains(spec, declaration) {
			t.Fatalf("OpenAPI contract is missing %q", declaration)
		}
	}
	for _, operation := range []struct {
		path, method, operationID, success, schema string
		headers                                    []string
	}{
		{"/health", "get", "getHealth", "200", "Health", nil},
		{"/spaces", "post", "createSpace", "201", "CreateSpaceRequest", []string{"callerId", "idempotencyKey"}},
		{"/events", "post", "ingestEvent", "202", "IngestEventRequest", []string{"callerId", "idempotencyKey"}},
		{"/memories", "post", "createMemory", "201", "CreateMemoryRequest", []string{"callerId", "idempotencyKey"}},
		{"/search", "post", "searchMemories", "200", "SearchRequest", []string{"callerId"}},
		{"/indexes/fts/rebuild", "post", "rebuildFts", "202", "JobReference", []string{"callerId", "idempotencyKey"}},
	} {
		assertOpenAPIOperation(t, spec, operation.path, operation.method, operation.operationID, operation.success, operation.schema, operation.headers)
	}
	var response domain.SearchResponse
	golden := `{"results":[],"truncated":false,"chars_used":0,"index_state":"ready","next_cursor":null}`
	decoder := json.NewDecoder(strings.NewReader(golden))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil || response.Results == nil || response.IndexState != "ready" {
		t.Fatalf("golden SearchResponse does not match executable model: %+v, %v", response, err)
	}
	assertJSONFields(t, []byte(golden), []string{"results", "truncated", "chars_used", "index_state", "next_cursor"})
	resultGolden := `{"memory":{"memory_id":"11111111-1111-4111-8111-111111111111","space_id":"22222222-2222-4222-8222-222222222222","memory_type":"fact","current_state":"active","frozen":false,"sensitivity":"normal","current_revision":{"revision_id":"33333333-3333-4333-8333-333333333333","memory_id":"11111111-1111-4111-8111-111111111111","content":"golden","created_by":"test","created_at":"2026-08-21T00:00:00Z"},"created_at":"2026-08-21T00:00:00Z","updated_at":"2026-08-21T00:00:00Z"},"rank":1,"keyword_score":-1,"selection_reason":"keyword_match"}`
	assertJSONFields(t, []byte(resultGolden), []string{"memory", "rank", "keyword_score", "selection_reason"})
	requestGolden := `{"query":"golden","space_ids":["22222222-2222-4222-8222-222222222222"],"include_ancestors":true,"memory_types":["fact"],"states":["active"],"reference_time":"2026-08-21T00:00:00Z","mode":"keyword","limit":10,"max_chars":12000,"include_evidence":false,"cursor":"opaque"}`
	var request searchRequestGolden
	requestDecoder := json.NewDecoder(strings.NewReader(requestGolden))
	requestDecoder.DisallowUnknownFields()
	if err := requestDecoder.Decode(&request); err != nil || request.Query == "" || len(request.SpaceIDs) != 1 {
		t.Fatalf("golden SearchRequest does not match executable contract: %+v, %v", request, err)
	}
	assertJSONFields(t, []byte(requestGolden), []string{"query", "space_ids", "include_ancestors", "memory_types", "states", "reference_time", "mode", "limit", "max_chars", "include_evidence", "cursor"})
}

func assertOpenAPIOperation(t *testing.T, spec, path, method, operationID, success, schema string, headers []string) {
	t.Helper()
	start := strings.Index(spec, "  "+path+":\n")
	if start < 0 {
		t.Fatalf("OpenAPI path %s is missing", path)
	}
	rest := spec[start+1:]
	end := strings.Index(rest, "\n  /")
	if end >= 0 {
		rest = rest[:end]
	}
	methodStart := strings.Index(rest, "    "+method+":\n")
	if methodStart < 0 {
		t.Fatalf("OpenAPI operation %s %s is missing", method, path)
	}
	operation := rest[methodStart:]
	for _, required := range []string{"operationId: " + operationID, "'" + success + "'", schema} {
		if !strings.Contains(operation, required) {
			t.Fatalf("OpenAPI operation %s %s is missing %q", method, path, required)
		}
	}
	for _, header := range headers {
		if !strings.Contains(operation, "#/components/parameters/"+header) {
			t.Fatalf("OpenAPI operation %s %s is missing header ref %s", method, path, header)
		}
	}
}

type searchRequestGolden struct {
	Query            string   `json:"query"`
	SpaceIDs         []string `json:"space_ids"`
	IncludeAncestors bool     `json:"include_ancestors"`
	MemoryTypes      []string `json:"memory_types"`
	States           []string `json:"states"`
	ReferenceTime    string   `json:"reference_time"`
	Mode             string   `json:"mode"`
	Limit            int      `json:"limit"`
	MaxChars         int      `json:"max_chars"`
	IncludeEvidence  bool     `json:"include_evidence"`
	Cursor           string   `json:"cursor"`
}

func assertJSONFields(t *testing.T, data []byte, want []string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != len(want) {
		t.Fatalf("JSON fields = %v; want %v", object, want)
	}
	for _, field := range want {
		if _, ok := object[field]; !ok {
			t.Fatalf("JSON is missing %q: %s", field, data)
		}
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
