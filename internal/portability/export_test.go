package portability

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
	_ "github.com/mattn/go-sqlite3"
)

const (
	rootSpace  = "00000000-0000-4000-8000-000000000001"
	taskSpace  = "00000000-0000-4000-8000-000000000002"
	otherSpace = "00000000-0000-4000-8000-000000000003"
)

type testDatabase struct {
	path   string
	store  *storage.Store
	repo   *storage.Repository
	writer *sql.DB
}

func newTestDatabase(t *testing.T) testDatabase {
	t.Helper()
	path := filepath.Join(t.TempDir(), "portability.db")
	store, err := storage.Open(context.Background(), path, storage.Options{ReadConnections: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	repo := storage.NewRepository(store, domain.Runtime{}, 10, 20, 3, "test-v1")
	writer, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { writer.Close() })
	seedAuthority(t, writer)
	return testDatabase{path, store, repo, writer}
}

func seedAuthority(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO memory_spaces VALUES ('` + rootSpace + `',NULL,'user','root','Root','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_spaces VALUES ('` + taskSpace + `','` + rootSpace + `','task','task','Task','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_spaces VALUES ('` + otherSpace + `','` + rootSpace + `','project','other','Other','2026-08-25T10:00:00Z')`,
		`INSERT INTO raw_contents(content_id,content_kind,content_text,media_type,content_hash,size_bytes,created_at) VALUES ('content-a','text','source','text/plain','hash',6,'2026-08-25T10:00:00Z')`,
		`INSERT INTO raw_events(event_id,operation_id,space_id,content_id,source_type,source_name,caller_id,occurred_at,received_at,created_at) VALUES ('event-a','op-a','` + taskSpace + `','content-a','manual','test','caller','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,sensitivity,created_at,updated_at) VALUES ('memory-a','` + taskSpace + `','revision-a','active','normal','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,event_type,event_time_start,event_time_end,time_precision,time_source,source_speaker,language,confidence,specificity,created_by,processing_version,created_at) VALUES ('revision-a','memory-a','event','alpha','alpha','hash-a','meeting','2026-08-25T09:00:00Z','2026-08-25T10:00:00Z','exact','statement','alice','en',0.9,0.5,'test','v1','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_state_events VALUES ('state-a','memory-a','revision-a','op-a',NULL,'active',NULL,0,'created','test','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_evidence(evidence_id,revision_id,event_id,role,created_at) VALUES ('evidence-a','revision-a','event-a','direct','2026-08-25T10:00:00Z')`,
		`INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,sensitivity,created_at,updated_at) VALUES ('memory-sensitive','` + taskSpace + `','revision-sensitive','active','sensitive','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,confidence,specificity,created_by,processing_version,created_at) VALUES ('revision-sensitive','memory-sensitive','fact','secret','secret','hash-s',0.9,0.5,'test','v1','2026-08-25T10:00:00Z')`,
		`INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,sensitivity,created_at,updated_at) VALUES ('memory-external','` + otherSpace + `','revision-external','active','normal','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,confidence,specificity,created_by,processing_version,created_at) VALUES ('revision-external','memory-external','fact','outside','outside','hash-o',0.9,0.5,'test','v1','2026-08-25T10:00:00Z')`,
		`INSERT INTO memory_relations(relation_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,source,specification_hash,created_by,processing_version,created_at,updated_at) VALUES ('relation-a','` + taskSpace + `','supports','memory-a','revision-a','memory-external','revision-external','human_confirmation','spec','test','v1','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO entities(entity_id,space_id,entity_type,canonical_name,normalized_name,normalization_version,state,created_by,processing_version,created_at,updated_at) VALUES ('entity-alice','` + taskSpace + `','person','Alice','alice','v1','active','test','v1','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO entities(entity_id,space_id,entity_type,canonical_name,normalized_name,normalization_version,state,created_by,processing_version,created_at,updated_at) VALUES ('entity-project','` + taskSpace + `','project','Project Alpha','project alpha','v1','active','test','v1','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO entity_aliases(alias_id,entity_id,alias,normalized_alias,normalization_version,source,state,created_by,created_at,updated_at) VALUES ('z-alias-alice','entity-alice','Alice','alice','v1','human_confirmation','active','test','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO revision_subject_entities(revision_id,entity_order,entity_id) VALUES ('revision-a',0,'entity-alice')`,
		`INSERT INTO revision_object_entities(revision_id,entity_order,entity_id) VALUES ('revision-a',0,'entity-project')`,
		`INSERT INTO entity_facts(fact_id,space_id,subject_entity_id,predicate,object_entity_id,source_memory_id,source_revision_id,confidence,state,source,specification_hash,created_by,processing_version,created_at,updated_at) VALUES ('fact-a','` + taskSpace + `','entity-alice','participates_in','entity-project','memory-a','revision-a',0.9,'active','human_confirmation','fact-spec','test','v1','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z')`,
		`INSERT INTO entity_fact_evidence(evidence_id,fact_id,evidence_type,created_by,created_at) VALUES ('fact-evidence-a','fact-a','human_confirmation','test','2026-08-25T10:00:00Z')`,
		`INSERT INTO entity_fact_state_events(state_event_id,fact_id,operation_id,from_state,to_state,reason,created_by,created_at) VALUES ('fact-state-a','fact-a','op-a',NULL,'active','confirmed','test','2026-08-25T10:00:00Z')`,
		`INSERT INTO agent_tool_sources(source_id,event_id,operation_id,source_type,source_name,conversation_id,turn_id,tool_call_id,occurred_at,metadata_json,created_at) VALUES ('source-a','event-a','op-a','agent_tool','assistant','conv-a','turn-a','call-a','2026-08-25T10:00:00Z','{}','2026-08-25T10:00:00Z')`,
		`INSERT INTO agent_tool_search_hints(event_id,local_id,revision_id,hint_order,hint,created_at) VALUES ('event-a','local-a','revision-a',0,'alpha hint','2026-08-25T10:00:00Z')`,
		`INSERT INTO agent_tool_proposals(proposal_id,event_id,space_id,suggestion_type,local_id,normalized_json,specification_hash,created_at) VALUES ('proposal-a','event-a','` + taskSpace + `','entity','entity-local','{}','hash-p','2026-08-25T10:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}
}

func request() Request {
	return Request{SourceIdentity: "source:test", SpaceIDs: []string{taskSpace}, IncludeAncestors: true, ClosurePolicy: "strict", SensitivePolicy: "exclude", ReferenceTime: "2026-08-25T12:00:00Z"}
}

func TestExportDryRunClosureCountsSensitivityAndExternalRefs(t *testing.T) {
	database := newTestDatabase(t)
	before := databaseHash(t, database.path)
	plan, err := NewExporter(database.repo).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.SpaceIDs, []string{rootSpace, taskSpace}) {
		t.Fatalf("spaces = %v", plan.SpaceIDs)
	}
	for member, want := range map[string]int{"spaces.jsonl": 2, "raw_events.jsonl": 1, "memories.jsonl": 2, "revisions.jsonl": 3, "evidence.jsonl": 2, "entities.jsonl": 3, "relations.jsonl": 4} {
		if got := plan.ObjectCounts[member]; got != want {
			t.Errorf("%s count = %d, want %d", member, got, want)
		}
	}
	if plan.Sensitivity != (SensitivitySummary{Omitted: 1}) {
		t.Errorf("sensitivity = %+v", plan.Sensitivity)
	}
	wantRef := []ExternalReference{{ObjectType: "revision", ObjectID: "revision-external", ReferencedBy: "relation-a"}}
	if !reflect.DeepEqual(plan.ExternalRefs, wantRef) {
		t.Errorf("external refs = %+v", plan.ExternalRefs)
	}
	if after := databaseHash(t, database.path); after != before {
		t.Fatal("dry-run modified authority database")
	}
}

func TestExportProducesDeterministicStrictPackage(t *testing.T) {
	database := newTestDatabase(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	exporter := NewExporter(database.repo)
	planA, err := exporter.Export(context.Background(), request(), first, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	planB, err := exporter.Export(context.Background(), request(), second, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if planA.ExportID != planB.ExportID {
		t.Fatalf("export IDs differ: %s %s", planA.ExportID, planB.ExportID)
	}
	if !reflect.DeepEqual(readPackage(t, first), readPackage(t, second)) {
		t.Fatal("deterministic exports differ")
	}
	entries, err := os.ReadDir(first)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := append(append([]string{}, dataMembers...), "checksums.json", "manifest.json")
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("members = %v, want %v", names, want)
	}
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o", entry.Name(), info.Mode().Perm())
		}
	}
}

type pinnedSource struct {
	db      *sql.DB
	pinned  chan struct{}
	proceed chan struct{}
}

func (source pinnedSource) BeginReadSnapshot(ctx context.Context) (*sql.Tx, error) {
	tx, err := source.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM corpus_metadata WHERE singleton=1`).Scan(&generation); err != nil {
		tx.Rollback()
		return nil, err
	}
	close(source.pinned)
	select {
	case <-source.proceed:
		return tx, nil
	case <-ctx.Done():
		tx.Rollback()
		return nil, ctx.Err()
	}
}

func TestExportConcurrentWritesUseOneSnapshot(t *testing.T) {
	database := newTestDatabase(t)
	reader, err := sql.Open("sqlite3", database.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	pinned, proceed := make(chan struct{}), make(chan struct{})
	result := make(chan Plan, 1)
	failures := make(chan error, 1)
	go func() {
		plan, err := NewExporter(pinnedSource{reader, pinned, proceed}).DryRun(context.Background(), request())
		result <- plan
		failures <- err
	}()
	<-pinned
	tx, err := database.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO memory_revisions(revision_id,memory_id,parent_revision_id,kind,display_content,canonical_content,canonical_hash,confidence,specificity,created_by,processing_version,created_at) VALUES ('revision-b','memory-a','revision-a','fact','beta','beta','hash-b',0.9,0.5,'test','v1','2026-08-25T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE memories SET current_revision_id='revision-b',updated_at='2026-08-25T12:00:00Z' WHERE memory_id='memory-a'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	plan := <-result
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	if plan.ObjectCounts["revisions.jsonl"] != 3 {
		t.Fatalf("snapshot revision authority = %d, want 3", plan.ObjectCounts["revisions.jsonl"])
	}
	after, err := NewExporter(database.repo).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if after.ObjectCounts["revisions.jsonl"] != 4 {
		t.Fatalf("post-write revision authority = %d, want 4", after.ObjectCounts["revisions.jsonl"])
	}
}

func TestExportChecksumFailureDoesNotPublish(t *testing.T) {
	database := newTestDatabase(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "package")
	_, err := NewExporter(database.repo).Export(context.Background(), request(), destination, ExportOptions{BeforePublish: func(temp string) error {
		return os.WriteFile(filepath.Join(temp, "memories.jsonl"), []byte("corrupt\n"), 0o600)
	}})
	if err == nil {
		t.Fatal("Export() succeeded after corruption")
	}
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination was published: %v", statErr)
	}
	entries, readErr := os.ReadDir(filepath.Dir(destination))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= len(".dmem-logical-") && entry.Name()[:len(".dmem-logical-")] == ".dmem-logical-" {
			t.Fatalf("temporary package leaked: %s", entry.Name())
		}
	}
}

func databaseHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func readPackage(t *testing.T, directory string) map[string]string {
	t.Helper()
	result := map[string]string{}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = string(data)
	}
	return result
}
