package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

type memoryTestRuntime struct {
	now    time.Time
	mu     sync.Mutex
	id     int
	failAt int
}

func (r *memoryTestRuntime) runtime() domain.Runtime {
	return domain.Runtime{
		Now: func() time.Time { return r.now },
		ID: func() (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.id++
			if r.failAt == r.id {
				return "", errors.New("injected ID failure")
			}
			return fmt.Sprintf("memory-generated-%03d", r.id), nil
		},
	}
}

func newMemoryTestRepository(t *testing.T) (*Repository, *memoryTestRuntime) {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "memories.db"), Options{ReadConnections: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runtime := &memoryTestRuntime{now: time.Date(2026, 8, 21, 12, 0, 0, 123000000, time.UTC)}
	repository := NewRepository(store, runtime.runtime(), 10, 20, 3, "test-v1")
	seedMemoryEvidence(t, repository)
	return repository, runtime
}

func seedMemoryEvidence(t *testing.T, repository *Repository) {
	t.Helper()
	now := "2026-08-21T11:00:00Z"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO memory_spaces(space_id, space_type, external_id, display_name, created_at) VALUES ('space-1', 'user', 'user-1', 'User', ?)`, []any{now}},
		{`INSERT INTO raw_contents(content_id, content_kind, content_text, media_type, content_hash, size_bytes, created_at) VALUES ('content-1', 'text', 'source one', 'text/plain', 'hash-1', 10, ?)`, []any{now}},
		{`INSERT INTO raw_contents(content_id, content_kind, content_text, media_type, content_hash, size_bytes, created_at) VALUES ('content-2', 'text', 'source two', 'text/plain', 'hash-2', 10, ?)`, []any{now}},
		{`INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES ('event-1', 'seed-op-1', 'space-1', 'content-1', 'manual', 'test', 'seed', ?, ?, ?)`, []any{now, now, now}},
		{`INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES ('event-2', 'seed-op-2', 'space-1', 'content-2', 'manual', 'test', 'seed', ?, ?, ?)`, []any{now, now, now}},
	}
	for _, statement := range statements {
		if _, err := repository.store.writer.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func memoryIdentity(key, route string, request any) WriteIdentity {
	return WriteIdentity{
		CallerID:       "memory-test-caller",
		IdempotencyKey: key,
		RequestID:      "request-" + key,
		Method:         "POST",
		Route:          route,
		RequestHash:    HashRequest(request),
	}
}

func directEvidence(eventID string) []EvidenceParams {
	start, end := int64(1), int64(5)
	return []EvidenceParams{{EventID: eventID, StartOffset: &start, EndOffset: &end}}
}

func equalEvidenceValues(a, b []domain.RevisionEvidence) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].EventID != b[i].EventID || a[i].Role != b[i].Role || !reflect.DeepEqual(a[i].EventTime, b[i].EventTime) || !reflect.DeepEqual(a[i].EventSequence, b[i].EventSequence) || !reflect.DeepEqual(a[i].StartOffset, b[i].StartOffset) || !reflect.DeepEqual(a[i].EndOffset, b[i].EndOffset) {
			return false
		}
	}
	return true
}

func createTestMemory(t *testing.T, repository *Repository, key string) domain.Memory {
	t.Helper()
	params := CreateMemoryParams{
		SpaceID: "space-1", Kind: "fact", Content: "initial searchable memory",
		Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "initial extraction",
	}
	memory, replay, err := repository.CreateMemory(context.Background(), memoryIdentity(key, "/v1/memories", params), params)
	if err != nil || replay {
		t.Fatalf("CreateMemory() = %+v, replay=%t, error=%v", memory, replay, err)
	}
	return memory
}

var memoryWriteTables = []string{
	"memories", "memory_revisions", "memory_evidence", "memory_state_events",
	"memory_projections", "memory_fts", "idempotency_records", "audit_events",
}

func memoryTableCounts(t *testing.T, repository *Repository) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(memoryWriteTables))
	for _, table := range memoryWriteTables {
		var count int
		if err := repository.store.reader.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func TestMEMCreateIsAtomicProjectedSearchableAuditedAndReplaySafe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newMemoryTestRepository(t)
	validFrom, validUntil := "2026-08-20T00:00:00Z", "2026-09-20T00:00:00Z"
	start, end := int64(2), int64(9)
	params := CreateMemoryParams{
		SpaceID: "space-1", Kind: "preference", Content: "prefers deterministic storage tests",
		ValidFrom: &validFrom, ValidUntil: &validUntil, Sensitivity: "sensitive", Reason: "create reason",
		Evidence: []EvidenceParams{{EventID: "event-1", StartOffset: &start, EndOffset: &end}},
	}
	identity := memoryIdentity("create", "/v1/memories", params)
	memory, replay, err := repository.CreateMemory(ctx, identity, params)
	if err != nil || replay {
		t.Fatalf("CreateMemory() = %+v, replay=%t, error=%v", memory, replay, err)
	}
	wantTime := formatTime(clock.now)
	if memory.SpaceID != params.SpaceID || string(memory.Kind) != params.Kind || memory.CurrentState != "active" || memory.Frozen || memory.Sensitivity != "sensitive" || memory.CreatedAt != wantTime || memory.UpdatedAt != wantTime {
		t.Fatalf("created memory = %+v", memory)
	}
	if memory.CurrentRevision.DisplayContent != params.Content || memory.CurrentRevision.CanonicalContent != params.Content || memory.CurrentRevision.ParentRevisionID != nil || memory.CurrentRevision.CreatedBy != identity.CallerID || memory.CurrentRevision.CreatedAt != wantTime || len(memory.CurrentRevision.Evidence) != 1 || memory.CurrentRevision.Evidence[0].EventID != "event-1" || memory.CurrentRevision.Evidence[0].Role != domain.EvidenceRoleDirect || !reflect.DeepEqual(memory.CurrentRevision.Evidence[0].StartOffset, &start) || !reflect.DeepEqual(memory.CurrentRevision.Evidence[0].EndOffset, &end) {
		t.Fatalf("created revision = %+v", memory.CurrentRevision)
	}

	var revisionID, state, content, stateOperation, idempotencyOperation, auditOperation, auditReason string
	var searchable, ftsMatches, sourceCount, responseStatus int
	err = repository.store.reader.QueryRow(`
		SELECT p.revision_id, p.current_state, p.searchable, p.display_content,
		       (SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'deterministic'),
		       (SELECT source_revision_count FROM index_metadata WHERE index_name='memory_fts'),
		       s.operation_id, i.operation_id, i.response_status, a.operation_id, a.reason
		FROM memory_projections p
		JOIN memory_state_events s ON s.memory_id=p.memory_id
		JOIN audit_events a ON a.target_id=p.memory_id AND a.action='create_memory'
		JOIN idempotency_records i ON i.idempotency_key='create'
		WHERE p.memory_id=?`, memory.MemoryID).Scan(&revisionID, &state, &searchable, &content, &ftsMatches, &sourceCount, &stateOperation, &idempotencyOperation, &responseStatus, &auditOperation, &auditReason)
	if err != nil {
		t.Fatal(err)
	}
	if revisionID != memory.CurrentRevision.RevisionID || state != "active" || searchable != 1 || content != params.Content || ftsMatches != 1 || sourceCount != 1 || responseStatus != 201 || stateOperation != idempotencyOperation || auditOperation != idempotencyOperation || auditReason != params.Reason {
		t.Fatalf("projection/operation rows = revision %q state %q searchable %d content %q fts %d source %d status %d operations %q/%q/%q reason %q", revisionID, state, searchable, content, ftsMatches, sourceCount, responseStatus, stateOperation, idempotencyOperation, auditOperation, auditReason)
	}

	before := memoryTableCounts(t, repository)
	replayed, replay, err := repository.CreateMemory(ctx, identity, params)
	if err != nil || !replay || !reflect.DeepEqual(replayed, memory) {
		t.Fatalf("replayed CreateMemory() = %+v, replay=%t, error=%v", replayed, replay, err)
	}
	if after := memoryTableCounts(t, repository); !reflect.DeepEqual(after, before) {
		t.Fatalf("replay changed table counts: before=%v after=%v", before, after)
	}
}

func TestMEMRevisionAndStateEventRowsAreImmutable(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "immutable-create")
	if _, err := repository.store.writer.Exec(`UPDATE memory_revisions SET display_content='mutated' WHERE revision_id=?`, memory.CurrentRevision.RevisionID); err == nil {
		t.Fatal("memory revision update succeeded")
	}
	events, err := repository.ListMemoryStateEvents(context.Background(), memory.MemoryID)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListMemoryStateEvents() = %+v, %v", events, err)
	}
	if _, err := repository.store.writer.Exec(`UPDATE memory_state_events SET reason='mutated' WHERE state_event_id=?`, events[0].StateEventID); err == nil {
		t.Fatal("memory state event update succeeded")
	}
	if _, err := repository.store.writer.Exec(`DELETE FROM memory_state_events WHERE state_event_id=?`, events[0].StateEventID); err == nil {
		t.Fatal("memory state event delete succeeded")
	}
	got, err := repository.GetMemory(context.Background(), memory.MemoryID)
	if err != nil || got.CurrentRevision.DisplayContent != "initial searchable memory" {
		t.Fatalf("GetMemory() after rejected mutations = %+v, %v", got, err)
	}
}

func TestMEMRevisePreservesOldRevisionAndDoesNotAppendStateEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "revise-create")
	oldRevision := memory.CurrentRevision
	clock.now = clock.now.Add(time.Minute)
	validFrom := "2026-08-22T00:00:00Z"
	params := ReviseMemoryParams{ExpectedRevisionID: oldRevision.RevisionID, Content: "revised searchable content", ValidFrom: &validFrom, Evidence: directEvidence("event-2"), Reason: "correct fact"}
	revised, _, replay, err := repository.ReviseMemory(ctx, memoryIdentity("revise", "/v1/memories/{memory_id}/revisions", params), memory.MemoryID, params)
	if err != nil || replay {
		t.Fatalf("ReviseMemory() = %+v, replay=%t, error=%v", revised, replay, err)
	}
	if revised.CurrentRevision.RevisionID == oldRevision.RevisionID || revised.CurrentRevision.ParentRevisionID == nil || *revised.CurrentRevision.ParentRevisionID != oldRevision.RevisionID || revised.CurrentRevision.DisplayContent != params.Content || revised.CurrentRevision.CreatedAt != formatTime(clock.now) {
		t.Fatalf("revised memory = %+v", revised)
	}
	revisions, err := repository.ListMemoryRevisions(ctx, memory.MemoryID)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("ListMemoryRevisions() = %+v, %v", revisions, err)
	}
	var storedOld domain.MemoryRevision
	for _, revision := range revisions {
		if revision.RevisionID == oldRevision.RevisionID {
			storedOld = revision
		}
	}
	if !reflect.DeepEqual(storedOld, oldRevision) {
		t.Fatalf("old revision changed: got=%+v want=%+v", storedOld, oldRevision)
	}
	events, err := repository.ListMemoryStateEvents(ctx, memory.MemoryID)
	if err != nil || len(events) != 1 || events[0].RevisionID != oldRevision.RevisionID {
		t.Fatalf("state events after revise = %+v, %v", events, err)
	}
	var projectionRevision, projectionContent string
	var ftsOld, ftsNew int
	if err := repository.store.reader.QueryRow(`SELECT revision_id, display_content, (SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'initial'), (SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'revised') FROM memory_projections WHERE memory_id=?`, memory.MemoryID).Scan(&projectionRevision, &projectionContent, &ftsOld, &ftsNew); err != nil {
		t.Fatal(err)
	}
	if projectionRevision != revised.CurrentRevision.RevisionID || projectionContent != params.Content || ftsOld != 0 || ftsNew != 1 {
		t.Fatalf("revised projection = revision %q content %q old/new FTS %d/%d", projectionRevision, projectionContent, ftsOld, ftsNew)
	}
}

func TestMEMRejectedWritesLeaveNoPartialRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)

	invalidCreates := []struct {
		name     string
		evidence []EvidenceParams
	}{
		{"empty", nil},
		{"missing event", directEvidence("missing-event")},
	}
	for _, test := range invalidCreates {
		before := memoryTableCounts(t, repository)
		params := CreateMemoryParams{SpaceID: "space-1", Kind: "fact", Content: "rejected", Sensitivity: "normal", Evidence: test.evidence, Reason: test.name}
		if _, _, err := repository.CreateMemory(ctx, memoryIdentity("create-"+test.name, "/v1/memories", params), params); !errors.Is(err, ErrInvalidEvidence) {
			t.Fatalf("CreateMemory(%s) error = %v", test.name, err)
		}
		if after := memoryTableCounts(t, repository); !reflect.DeepEqual(after, before) {
			t.Fatalf("CreateMemory(%s) left rows: before=%v after=%v", test.name, before, after)
		}
	}

	memory := createTestMemory(t, repository, "rejected-revise-create")
	invalidRevisions := []struct {
		name       string
		expectedID string
		evidence   []EvidenceParams
	}{
		{"stale", "stale-revision", directEvidence("event-2")},
		{"empty", memory.CurrentRevision.RevisionID, nil},
		{"missing event", memory.CurrentRevision.RevisionID, directEvidence("missing-event")},
	}
	for _, test := range invalidRevisions {
		before := memoryTableCounts(t, repository)
		params := ReviseMemoryParams{ExpectedRevisionID: test.expectedID, Content: "rejected revision", Evidence: test.evidence, Reason: test.name}
		_, _, _, err := repository.ReviseMemory(ctx, memoryIdentity("revise-"+test.name, "/v1/memories/{memory_id}/revisions", params), memory.MemoryID, params)
		if test.name == "stale" && !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("ReviseMemory(stale) error = %v", err)
		}
		if test.name != "stale" && !errors.Is(err, ErrInvalidEvidence) {
			t.Fatalf("ReviseMemory(%s) error = %v", test.name, err)
		}
		if after := memoryTableCounts(t, repository); !reflect.DeepEqual(after, before) {
			t.Fatalf("ReviseMemory(%s) left rows: before=%v after=%v", test.name, before, after)
		}
	}
}

func TestMEMEvidenceOffsetConstraintsRollbackWholeCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, test := range []struct {
		name  string
		start *int64
		end   *int64
	}{
		{"negative start", int64Pointer(-1), int64Pointer(2)},
		{"missing end", int64Pointer(1), nil},
		{"non-increasing", int64Pointer(3), int64Pointer(3)},
		{"past rune length", int64Pointer(1), int64Pointer(11)},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, _ := newMemoryTestRepository(t)
			before := memoryTableCounts(t, repository)
			params := CreateMemoryParams{SpaceID: "space-1", Kind: "fact", Content: "bad offsets", Sensitivity: "normal", Evidence: []EvidenceParams{{EventID: "event-1", StartOffset: test.start, EndOffset: test.end}}, Reason: test.name}
			if _, _, err := repository.CreateMemory(ctx, memoryIdentity("offset-"+test.name, "/v1/memories", params), params); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("CreateMemory() error = %v; want ErrInvalidEvidence", err)
			}
			if after := memoryTableCounts(t, repository); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid offsets left rows: before=%v after=%v", before, after)
			}
		})
	}
}

func TestMEMEvidenceScopeAndContentKindValidation(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	now := "2026-08-21T11:00:00Z"
	statements := []string{
		`INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES ('space-child', 'space-1', 'project', 'child', 'Child', '` + now + `'), ('space-sibling', 'space-1', 'project', 'sibling', 'Sibling', '` + now + `')`,
		`INSERT INTO raw_contents(content_id, content_kind, content_text, reference_uri, media_type, content_hash, size_bytes, created_at) VALUES ('content-child', 'text', '你好a', NULL, 'text/plain', 'child-hash', 7, '` + now + `'), ('content-sibling', 'text', 'sibling', NULL, 'text/plain', 'sibling-hash', 7, '` + now + `'), ('content-reference', 'reference', NULL, 'https://example.test/source', 'text/plain', 'reference-hash', 0, '` + now + `')`,
		`INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES ('event-child', 'op-child', 'space-child', 'content-child', 'manual', 'test', 'seed', '` + now + `', '` + now + `', '` + now + `'), ('event-sibling', 'op-sibling', 'space-sibling', 'content-sibling', 'manual', 'test', 'seed', '` + now + `', '` + now + `', '` + now + `'), ('event-reference', 'op-reference', 'space-child', 'content-reference', 'file_reference', 'test', 'seed', '` + now + `', '` + now + `', '` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := repository.store.writer.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	create := func(key, spaceID string, evidence EvidenceParams) error {
		params := CreateMemoryParams{SpaceID: spaceID, Kind: "fact", Content: key, Sensitivity: "normal", Evidence: []EvidenceParams{evidence}, Reason: key}
		_, _, err := repository.CreateMemory(context.Background(), memoryIdentity(key, "/v1/memories", params), params)
		return err
	}
	start, runeEnd, pastEnd := int64(0), int64(3), int64(4)
	if err := create("ancestor evidence", "space-child", EvidenceParams{EventID: "event-1"}); err != nil {
		t.Fatalf("ancestor evidence rejected: %v", err)
	}
	if err := create("unicode range", "space-child", EvidenceParams{EventID: "event-child", StartOffset: &start, EndOffset: &runeEnd}); err != nil {
		t.Fatalf("rune-bounded evidence rejected: %v", err)
	}
	for _, test := range []struct {
		name     string
		evidence EvidenceParams
	}{
		{"cross-space", EvidenceParams{EventID: "event-sibling"}},
		{"out-of-range", EvidenceParams{EventID: "event-child", StartOffset: &start, EndOffset: &pastEnd}},
		{"reference-offset", EvidenceParams{EventID: "event-reference", StartOffset: &start, EndOffset: &runeEnd}},
	} {
		if err := create(test.name, "space-child", test.evidence); !errors.Is(err, ErrInvalidEvidence) {
			t.Fatalf("%s error = %v; want ErrInvalidEvidence", test.name, err)
		}
	}
}

func TestMEMArchiveRemovesSearchAndRestoreCopiesRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newMemoryTestRepository(t)
	created := createTestMemory(t, repository, "lifecycle-create")
	source := created.CurrentRevision
	clock.now = clock.now.Add(time.Minute)
	revise := ReviseMemoryParams{ExpectedRevisionID: source.RevisionID, Content: "replacement revision", Evidence: directEvidence("event-2"), Reason: "replacement"}
	current, _, _, err := repository.ReviseMemory(ctx, memoryIdentity("lifecycle-revise", "/v1/memories/{memory_id}/revisions", revise), created.MemoryID, revise)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Minute)
	archive := ChangeMemoryStateParams{ExpectedRevisionID: current.CurrentRevision.RevisionID, Action: "archive", Reason: "not current"}
	archived, _, replay, err := repository.ChangeMemoryState(ctx, memoryIdentity("archive", "/v1/memories/{memory_id}/state", archive), created.MemoryID, archive)
	if err != nil || replay || archived.CurrentState != "archived" {
		t.Fatalf("archive = %+v, replay=%t, error=%v", archived, replay, err)
	}
	var projectionState string
	var searchable, ftsRows, sourceCount int
	if err := repository.store.reader.QueryRow(`SELECT current_state, searchable, (SELECT count(*) FROM memory_fts WHERE memory_id=?), (SELECT source_revision_count FROM index_metadata WHERE index_name='memory_fts') FROM memory_projections WHERE memory_id=?`, created.MemoryID, created.MemoryID).Scan(&projectionState, &searchable, &ftsRows, &sourceCount); err != nil {
		t.Fatal(err)
	}
	if projectionState != "archived" || searchable != 0 || ftsRows != 0 || sourceCount != 0 {
		t.Fatalf("archived projection = state %q searchable %d fts %d source count %d", projectionState, searchable, ftsRows, sourceCount)
	}

	clock.now = clock.now.Add(time.Minute)
	restore := ChangeMemoryStateParams{ExpectedRevisionID: archived.CurrentRevision.RevisionID, Action: "restore", RestoreRevisionID: &source.RevisionID, Reason: "restore original"}
	restored, _, replay, err := repository.ChangeMemoryState(ctx, memoryIdentity("restore", "/v1/memories/{memory_id}/state", restore), created.MemoryID, restore)
	if err != nil || replay {
		t.Fatalf("restore = %+v, replay=%t, error=%v", restored, replay, err)
	}
	if restored.CurrentState != "active" || restored.CurrentRevision.RevisionID == source.RevisionID || restored.CurrentRevision.ParentRevisionID == nil || *restored.CurrentRevision.ParentRevisionID != archived.CurrentRevision.RevisionID || restored.CurrentRevision.DisplayContent != source.DisplayContent || !reflect.DeepEqual(restored.CurrentRevision.ValidFrom, source.ValidFrom) || !reflect.DeepEqual(restored.CurrentRevision.ValidUntil, source.ValidUntil) || !equalEvidenceValues(restored.CurrentRevision.Evidence, source.Evidence) {
		t.Fatalf("restored memory = %+v; source = %+v", restored, source)
	}
	var parentID, restoredFromID, content, validFrom, validUntil sql.NullString
	if err := repository.store.reader.QueryRow(`SELECT parent_revision_id, restored_from_revision_id, display_content, valid_from, valid_until FROM memory_revisions WHERE revision_id=?`, restored.CurrentRevision.RevisionID).Scan(&parentID, &restoredFromID, &content, &validFrom, &validUntil); err != nil {
		t.Fatal(err)
	}
	if parentID.String != archived.CurrentRevision.RevisionID || restoredFromID.String != source.RevisionID || content.String != source.DisplayContent || validFrom.Valid != (source.ValidFrom != nil) || validUntil.Valid != (source.ValidUntil != nil) {
		t.Fatalf("restored row = parent %v restored_from %v content %v valid_from %v valid_until %v", parentID, restoredFromID, content, validFrom, validUntil)
	}
	if err := repository.store.reader.QueryRow(`SELECT searchable, (SELECT count(*) FROM memory_fts WHERE memory_id=? AND revision_id=?) FROM memory_projections WHERE memory_id=?`, created.MemoryID, restored.CurrentRevision.RevisionID, created.MemoryID).Scan(&searchable, &ftsRows); err != nil {
		t.Fatal(err)
	}
	if searchable != 1 || ftsRows != 1 {
		t.Fatalf("restored search rows = searchable %d fts %d", searchable, ftsRows)
	}
}

func TestMEMCurrentLifecycleMatchesLatestEventAndNoopIsRecordedWithoutEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, clock := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "state-create")
	for i, action := range []string{"freeze", "contest", "unfreeze"} {
		clock.now = clock.now.Add(time.Second)
		params := ChangeMemoryStateParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Action: action, Reason: action + " reason"}
		var err error
		memory, _, _, err = repository.ChangeMemoryState(ctx, memoryIdentity(fmt.Sprintf("state-%d", i), "/v1/memories/{memory_id}/state", params), memory.MemoryID, params)
		if err != nil {
			t.Fatalf("ChangeMemoryState(%s): %v", action, err)
		}
	}
	events, err := repository.ListMemoryStateEvents(ctx, memory.MemoryID)
	if err != nil || len(events) != 4 {
		t.Fatalf("state events = %+v, %v", events, err)
	}
	latest := events[0]
	if memory.CurrentState != latest.ToState || memory.Frozen != latest.ToFrozen || latest.RevisionID != memory.CurrentRevision.RevisionID {
		t.Fatalf("current memory %+v does not match latest event %+v", memory, latest)
	}

	clock.now = clock.now.Add(time.Second)
	params := ChangeMemoryStateParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Action: "contest", Reason: "already contested"}
	identity := memoryIdentity("contest-noop", "/v1/memories/{memory_id}/state", params)
	noop, _, replay, err := repository.ChangeMemoryState(ctx, identity, memory.MemoryID, params)
	if err != nil || replay || noop.CurrentState != "contested" {
		t.Fatalf("noop contest = %+v, replay=%t, error=%v", noop, replay, err)
	}
	afterEvents, err := repository.ListMemoryStateEvents(ctx, memory.MemoryID)
	if err != nil || len(afterEvents) != len(events) {
		t.Fatalf("events after noop = %+v, %v", afterEvents, err)
	}
	var idempotency, audits int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM idempotency_records WHERE idempotency_key='contest-noop'), (SELECT count(*) FROM audit_events WHERE action='contest_memory' AND target_id=? AND request_id='request-contest-noop')`, memory.MemoryID).Scan(&idempotency, &audits); err != nil {
		t.Fatal(err)
	}
	if idempotency != 1 || audits != 1 {
		t.Fatalf("noop idempotency/audits = %d/%d", idempotency, audits)
	}
	before := memoryTableCounts(t, repository)
	replayed, _, replay, err := repository.ChangeMemoryState(ctx, identity, memory.MemoryID, params)
	if err != nil || !replay || !reflect.DeepEqual(replayed, noop) {
		t.Fatalf("replayed noop = %+v, replay=%t, error=%v", replayed, replay, err)
	}
	if after := memoryTableCounts(t, repository); !reflect.DeepEqual(after, before) {
		t.Fatalf("noop replay changed table counts: before=%v after=%v", before, after)
	}
}

func TestCorpusGenerationAdvancesOnlyWithCommittedProjectionChanges(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	var generation int64
	if err := repository.store.reader.QueryRow(`SELECT generation FROM corpus_metadata WHERE singleton=1`).Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("initial generation = %d, %v", generation, err)
	}
	memory := createTestMemory(t, repository, "generation-create")
	if err := repository.store.reader.QueryRow(`SELECT generation FROM corpus_metadata WHERE singleton=1`).Scan(&generation); err != nil || generation != 2 {
		t.Fatalf("generation after create = %d, %v", generation, err)
	}
	invalid := ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "invalid", Evidence: directEvidence("missing-event"), Reason: "rollback"}
	if _, _, _, err := repository.ReviseMemory(context.Background(), memoryIdentity("generation-invalid", "/v1/memories/{memory_id}/revisions", invalid), memory.MemoryID, invalid); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("invalid revise error = %v", err)
	}
	if err := repository.store.reader.QueryRow(`SELECT generation FROM corpus_metadata WHERE singleton=1`).Scan(&generation); err != nil || generation != 2 {
		t.Fatalf("generation after rollback = %d, %v", generation, err)
	}
	noop := ChangeMemoryStateParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Action: "confirm", Reason: "already active"}
	if _, _, _, err := repository.ChangeMemoryState(context.Background(), memoryIdentity("generation-noop", "/v1/memories/{memory_id}/state", noop), memory.MemoryID, noop); err != nil {
		t.Fatal(err)
	}
	if err := repository.store.reader.QueryRow(`SELECT generation FROM corpus_metadata WHERE singleton=1`).Scan(&generation); err != nil || generation != 2 {
		t.Fatalf("generation after no-op = %d, %v", generation, err)
	}
}

func TestMEMConcurrentReviseHasOneWinnerAndNoPartialLoser(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "race-create")
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for i, eventID := range []string{"event-1", "event-2"} {
		workers.Add(1)
		go func(i int, eventID string) {
			defer workers.Done()
			params := ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: fmt.Sprintf("racing revision %d", i), Evidence: directEvidence(eventID), Reason: "race"}
			<-start
			_, _, _, err := repository.ReviseMemory(context.Background(), memoryIdentity(fmt.Sprintf("race-%d", i), "/v1/memories/{memory_id}/revisions", params), memory.MemoryID, params)
			results <- err
		}(i, eventID)
	}
	close(start)
	workers.Wait()
	close(results)
	winners, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrRevisionConflict):
			conflicts++
		default:
			t.Fatalf("ReviseMemory race error = %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("race results winners=%d conflicts=%d", winners, conflicts)
	}
	counts := memoryTableCounts(t, repository)
	if counts["memories"] != 1 || counts["memory_revisions"] != 2 || counts["memory_evidence"] != 2 || counts["memory_state_events"] != 1 || counts["memory_projections"] != 1 || counts["memory_fts"] != 1 || counts["idempotency_records"] != 2 || counts["audit_events"] != 2 {
		t.Fatalf("race table counts = %v", counts)
	}
}

func TestMEMInjectedFailuresRollbackCreateReviseAndRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("create", func(t *testing.T) {
		repository, runtime := newMemoryTestRepository(t)
		before := memoryTableCounts(t, repository)
		runtime.failAt = runtime.id + 3
		params := CreateMemoryParams{SpaceID: "space-1", Kind: "fact", Content: "not committed", Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "fault"}
		if _, _, err := repository.CreateMemory(ctx, memoryIdentity("fault-create", "/v1/memories", params), params); err == nil {
			t.Fatal("CreateMemory() succeeded with injected failure")
		}
		if after := memoryTableCounts(t, repository); !reflect.DeepEqual(after, before) {
			t.Fatalf("failed create changed rows: before=%v after=%v", before, after)
		}
	})

	t.Run("revise", func(t *testing.T) {
		repository, runtime := newMemoryTestRepository(t)
		memory := createTestMemory(t, repository, "fault-revise-create")
		before := memoryTableCounts(t, repository)
		runtime.failAt = runtime.id + 2
		params := ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "not committed", Evidence: directEvidence("event-2"), Reason: "fault"}
		if _, _, _, err := repository.ReviseMemory(ctx, memoryIdentity("fault-revise", "/v1/memories/{memory_id}", params), memory.MemoryID, params); err == nil {
			t.Fatal("ReviseMemory() succeeded with injected failure")
		}
		if after := memoryTableCounts(t, repository); !reflect.DeepEqual(after, before) {
			t.Fatalf("failed revise changed rows: before=%v after=%v", before, after)
		}
	})

	t.Run("restore after revision insert", func(t *testing.T) {
		repository, runtime := newMemoryTestRepository(t)
		memory := createTestMemory(t, repository, "fault-restore-create")
		archive := ChangeMemoryStateParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Action: "archive", Reason: "archive"}
		memory, _, _, err := repository.ChangeMemoryState(ctx, memoryIdentity("fault-restore-archive", "/v1/memories/{memory_id}/state", archive), memory.MemoryID, archive)
		if err != nil {
			t.Fatal(err)
		}
		before := memoryTableCounts(t, repository)
		runtime.failAt = runtime.id + 5
		restore := ChangeMemoryStateParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Action: "restore", RestoreRevisionID: &memory.CurrentRevision.RevisionID, Reason: "fault"}
		if _, _, _, err := repository.ChangeMemoryState(ctx, memoryIdentity("fault-restore", "/v1/memories/{memory_id}/state", restore), memory.MemoryID, restore); err == nil {
			t.Fatal("ChangeMemoryState(restore) succeeded with injected failure")
		}
		if after := memoryTableCounts(t, repository); !reflect.DeepEqual(after, before) {
			t.Fatalf("failed restore changed rows: before=%v after=%v", before, after)
		}
	})
}

func int64Pointer(value int64) *int64 { return &value }
