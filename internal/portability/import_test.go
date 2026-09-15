package portability

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

func exportedFixture(t *testing.T) (string, testDatabase) {
	t.Helper()
	database := newTestDatabase(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "package")
	if _, err := NewExporter(database.repo).Export(context.Background(), request(), directory, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	return directory, database
}

type interruptAfterBatchRepository struct {
	ImportRepository
	cancel context.CancelFunc
	count  int
}

func (r *interruptAfterBatchRepository) CommitImportBatch(ctx context.Context, operationID string, generation int64, batch int, objects []storage.ImportObject, lastMember, lastObjectID, importedBy string) error {
	err := r.ImportRepository.CommitImportBatch(ctx, operationID, generation, batch, objects, lastMember, lastObjectID, importedBy)
	if err == nil {
		r.count++
		if r.count == 1 {
			r.cancel()
		}
	}
	return err
}

func TestImportRejectsMaliciousPackageBeforeAuthorityWrite(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string) string
		limits ImportLimits
		want   error
	}{
		{"unknown authority field", func(t *testing.T, directory string) string {
			data, err := os.ReadFile(filepath.Join("testdata", "malicious-record.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "memories.jsonl"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			resealPackage(t, directory)
			return directory
		}, DefaultImportLimits(), ErrInvalidPackage},
		{"symlink member", func(t *testing.T, directory string) string {
			if err := os.Remove(filepath.Join(directory, "memories.jsonl")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("revisions.jsonl", filepath.Join(directory, "memories.jsonl")); err != nil {
				t.Fatal(err)
			}
			return directory
		}, DefaultImportLimits(), ErrInvalidPackage},
		{"symlink package path", func(t *testing.T, directory string) string {
			link := filepath.Join(filepath.Dir(directory), "package-link")
			if err := os.Symlink(directory, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, DefaultImportLimits(), ErrInvalidPackage},
		{"extra member", func(t *testing.T, directory string) string {
			if err := os.WriteFile(filepath.Join(directory, "../escape.jsonl"), []byte("attack\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "extra.jsonl"), []byte("attack\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return directory
		}, DefaultImportLimits(), ErrInvalidPackage},
		{"record limit", func(_ *testing.T, directory string) string { return directory }, ImportLimits{MaxFiles: 13, MaxRecords: 1, MaxFileBytes: 1 << 20, MaxTotalBytes: 10 << 20, BatchSize: 1}, ErrImportResourceExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, database := exportedFixture(t)
			before := databaseHash(t, database.path)
			directory = test.mutate(t, directory)
			_, err := NewImporter(database.repo, test.limits).Import(context.Background(), directory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"})
			if !errors.Is(err, test.want) {
				t.Fatalf("malicious package Import() error = %v, want %v", err, test.want)
			}
			if after := databaseHash(t, database.path); after != before {
				t.Fatal("malicious package wrote authority before validation")
			}
		})
	}
}

func TestImportRejectsUnknownFormatBeforeAuthorityWrite(t *testing.T) {
	directory, database := exportedFixture(t)
	before := databaseHash(t, database.path)
	manifestData, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.FormatVersion = FormatVersion + 1
	manifestData, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), append(manifestData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = NewImporter(database.repo, DefaultImportLimits()).Import(context.Background(), directory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"})
	if !errors.Is(err, ErrIncompatibleVersion) {
		t.Fatalf("unknown format Import() error = %v, want unsupported format", err)
	}
	if after := databaseHash(t, database.path); after != before {
		t.Fatal("unknown format wrote authority before validation")
	}
}

func TestImportDryRunDeterministicSpaceAndIDConflictPlan(t *testing.T) {
	directory, database := exportedFixture(t)
	before := databaseHash(t, database.path)
	importer := NewImporter(database.repo, DefaultImportLimits())
	input := ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"}
	first, err := importer.DryRun(context.Background(), directory, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := importer.DryRun(context.Background(), directory, input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("dry-run plans differ")
	}
	for _, local := range first.SpaceMappings {
		if local != rootSpace {
			t.Fatalf("space mapped to %q", local)
		}
	}
	for _, item := range first.Items {
		want := deterministicLocalID(first.SourceIdentity, first.ExportID, item.ObjectType, item.SourceID)
		if item.ObjectType != "space" && item.LocalID != want {
			t.Fatalf("%s %s local ID = %q, want %q", item.ObjectType, item.SourceID, item.LocalID, want)
		}
	}
	if after := databaseHash(t, database.path); after != before {
		t.Fatal("dry-run wrote authority")
	}
}

func TestImportInterruptedBatchResumesWithMappingsAndProvenance(t *testing.T) {
	directory, database := exportedFixture(t)
	limits := ImportLimits{MaxFiles: 13, MaxRecords: 1000, MaxFileBytes: 1 << 20, MaxTotalBytes: 10 << 20, BatchSize: 1}
	ctx, cancel := context.WithCancel(context.Background())
	interrupted := &interruptAfterBatchRepository{ImportRepository: database.repo, cancel: cancel}
	partial, err := NewImporter(interrupted, limits).Import(ctx, directory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if partial.State != "waiting" || partial.CommittedObjects == 0 || partial.CommittedObjects >= partial.TotalObjects {
		t.Fatalf("interrupted status = %+v", partial)
	}
	var partialMappings, partialProvenance, partialCheckpoints int
	if err := database.writer.QueryRow(`SELECT (SELECT count(*) FROM import_object_mappings),(SELECT count(*) FROM imported_provenance),(SELECT count(*) FROM import_checkpoints)`).Scan(&partialMappings, &partialProvenance, &partialCheckpoints); err != nil {
		t.Fatal(err)
	}
	if partialMappings != partial.CommittedObjects || partialProvenance != partialMappings || partialCheckpoints != 1 {
		t.Fatalf("partial mappings/provenance/checkpoints = %d/%d/%d", partialMappings, partialProvenance, partialCheckpoints)
	}
	status, err := NewImporter(database.repo, limits).Import(context.Background(), directory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "succeeded" || status.CommittedObjects != status.TotalObjects {
		t.Fatalf("status = %+v", status)
	}
	var mappings, provenance, checkpoints int
	if err := database.writer.QueryRow(`SELECT (SELECT count(*) FROM import_object_mappings),(SELECT count(*) FROM imported_provenance),(SELECT count(*) FROM import_checkpoints)`).Scan(&mappings, &provenance, &checkpoints); err != nil {
		t.Fatal(err)
	}
	if mappings != status.TotalObjects || provenance != mappings || checkpoints < 2 {
		t.Fatalf("mappings/provenance/checkpoints = %d/%d/%d", mappings, provenance, checkpoints)
	}
}

func TestRepeatedImportIsMappingIdempotent(t *testing.T) {
	directory, database := exportedFixture(t)
	importer := NewImporter(database.repo, DefaultImportLimits())
	first, err := importer.Import(context.Background(), directory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var authorityBefore, mappingsBefore, provenanceBefore int
	if err := database.writer.QueryRow(`SELECT (SELECT count(*) FROM raw_events)+(SELECT count(*) FROM memories)+(SELECT count(*) FROM memory_revisions)+(SELECT count(*) FROM memory_evidence),(SELECT count(*) FROM import_object_mappings),(SELECT count(*) FROM imported_provenance)`).Scan(&authorityBefore, &mappingsBefore, &provenanceBefore); err != nil {
		t.Fatal(err)
	}
	second, err := importer.Import(context.Background(), directory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"})
	if err != nil || second.ImportOperationID != first.ImportOperationID {
		t.Fatalf("repeated import status = %+v, error = %v", second, err)
	}
	var authorityAfter, mappingsAfter, provenanceAfter int
	if err := database.writer.QueryRow(`SELECT (SELECT count(*) FROM raw_events)+(SELECT count(*) FROM memories)+(SELECT count(*) FROM memory_revisions)+(SELECT count(*) FROM memory_evidence),(SELECT count(*) FROM import_object_mappings),(SELECT count(*) FROM imported_provenance)`).Scan(&authorityAfter, &mappingsAfter, &provenanceAfter); err != nil {
		t.Fatal(err)
	}
	if authorityAfter != authorityBefore || mappingsAfter != mappingsBefore || provenanceAfter != provenanceBefore {
		t.Fatalf("repeated import changed counts: authority=%d/%d mappings=%d/%d provenance=%d/%d", authorityBefore, authorityAfter, mappingsBefore, mappingsAfter, provenanceBefore, provenanceAfter)
	}
}

func TestImportBatchFailureRollsBackMappingProvenanceAndCheckpoint(t *testing.T) {
	directory, database := exportedFixture(t)
	plan, err := NewImporter(database.repo, DefaultImportLimits()).DryRun(context.Background(), directory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	id := deterministicLocalID(plan.SourceIdentity, plan.ExportID, "import_operation", plan.TargetSpaceID)
	spec := storage.ImportOperationSpec{ImportOperationID: id, OperationID: id, SourceID: deterministicLocalID(plan.SourceIdentity, "source"), SourceIdentity: plan.SourceIdentity, ExportID: plan.ExportID, TargetSpaceID: rootSpace, PackageHash: plan.PackageHash, PlanHash: plan.PlanHash, TotalObjects: 2, CreatedBy: "test", SpaceMappings: plan.SpaceMappings}
	if _, err := database.repo.StartImport(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	generation, err := database.repo.ClaimImport(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	objects := []storage.ImportObject{{SourceType: "space", SourceID: "source-space", LocalID: rootSpace, ObjectHash: "hash-a", Action: "duplicate"}, {SourceType: "malicious", SourceID: "source-object", LocalID: "local-object", ObjectHash: "hash-b", Action: "duplicate"}}
	if err := database.repo.CommitImportBatch(context.Background(), id, generation, 0, objects, "spaces.jsonl", "source-object", "test"); err == nil {
		t.Fatal("invalid second mapping committed")
	}
	var mappings, provenance, checkpoints, committed int
	if err := database.writer.QueryRow(`SELECT (SELECT count(*) FROM import_object_mappings),(SELECT count(*) FROM imported_provenance),(SELECT count(*) FROM import_checkpoints),committed_objects FROM import_operations WHERE import_operation_id=?`, id).Scan(&mappings, &provenance, &checkpoints, &committed); err != nil {
		t.Fatal(err)
	}
	if mappings != 0 || provenance != 0 || checkpoints != 0 || committed != 0 {
		t.Fatalf("failed batch persisted %d/%d/%d/%d", mappings, provenance, checkpoints, committed)
	}
}

func TestImportCancelPreservesCommittedBatch(t *testing.T) {
	directory, database := exportedFixture(t)
	limits := ImportLimits{MaxFiles: 13, MaxRecords: 1000, MaxFileBytes: 1 << 20, MaxTotalBytes: 10 << 20, BatchSize: 1}
	ctx, cancel := context.WithCancel(context.Background())
	partial, err := NewImporter(&interruptAfterBatchRepository{ImportRepository: database.repo, cancel: cancel}, limits).Import(ctx, directory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.repo.CancelImport(context.Background(), partial.ImportOperationID); err != nil {
		t.Fatal(err)
	}
	status, err := database.repo.ImportStatus(context.Background(), partial.ImportOperationID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "cancelled" || status.CommittedObjects != partial.CommittedObjects {
		t.Fatalf("cancelled status = %+v", status)
	}
	var mappings, provenance int
	if err := database.writer.QueryRow(`SELECT (SELECT count(*) FROM import_object_mappings),(SELECT count(*) FROM imported_provenance)`).Scan(&mappings, &provenance); err != nil {
		t.Fatal(err)
	}
	if mappings != partial.CommittedObjects || provenance != mappings {
		t.Fatalf("cancel split committed batch: %d/%d", mappings, provenance)
	}
}

func TestImportLocalCurrentConflictCreatesEvolutionProposal(t *testing.T) {
	directory, database := exportedFixture(t)
	input := ImportRequest{TargetSpaceID: taskSpace, CreatedBy: "test", ObjectMappings: map[string]string{"memory\x00memory-a": "memory-external"}}
	importer := NewImporter(database.repo, DefaultImportLimits())
	plan, err := importer.DryRun(context.Background(), directory, input)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range plan.Items {
		found = found || item.ObjectType == "memory" && item.Action == "proposal" && item.ExpectedRevisionID == "revision-external"
	}
	if !found {
		t.Fatal("conflict proposal was not planned")
	}
	if _, err := importer.Import(context.Background(), directory, input); err != nil {
		t.Fatal(err)
	}
	var current string
	var proposals int
	if err := database.writer.QueryRow(`SELECT current_revision_id,(SELECT count(*) FROM evolution_proposals WHERE action='conflict' AND review_state='pending') FROM memories WHERE memory_id='memory-external'`).Scan(&current, &proposals); err != nil {
		t.Fatal(err)
	}
	if current != "revision-external" || proposals != 1 {
		t.Fatalf("current/proposals = %q/%d", current, proposals)
	}
}

func TestImportedFeedbackIdentityIsPreservedWithoutAggregateScore(t *testing.T) {
	directory, database := exportedFixture(t)
	data, _ := json.Marshal(record{ObjectID: "feedback-a", ObjectType: "feedback_event", Data: map[string]any{"feedback_id": "feedback-a", "operation_id": "feedback-op", "caller_id": "source-user", "memory_id": "memory-a", "revision_id": "revision-a", "feedback_type": "useful", "reason_code": "answered_query", "note": nil, "search_operation_id": nil, "query_hash": nil, "result_rank": nil, "ranking_version": nil, "matched_channels_json": nil, "created_at": "2026-08-25T10:00:00Z"}})
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(directory, "feedback.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	resealPackage(t, directory)
	status, err := NewImporter(database.repo, DefaultImportLimits()).Import(context.Background(), directory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var caller string
	var aggregates, rebuilds int
	if err := database.writer.QueryRow(`SELECT caller_id,(SELECT count(*) FROM feedback_aggregates),(SELECT count(DISTINCT job_type) FROM jobs WHERE operation_id=? AND job_type IN ('rebuild_fts','rebuild_semantic','rebuild_entity_index','rebuild_relation_index','rebuild_feedback_aggregates')) FROM feedback_events WHERE operation_id=?`, status.ImportOperationID, status.ImportOperationID).Scan(&caller, &aggregates, &rebuilds); err != nil {
		t.Fatal(err)
	}
	if caller != "source-user" || aggregates != 0 || rebuilds != 5 {
		t.Fatalf("caller/aggregates/rebuilds = %q/%d/%d", caller, aggregates, rebuilds)
	}
}

func TestImportGenerationFenceRejectsStaleBatchRace(t *testing.T) {
	directory, database := exportedFixture(t)
	importer := NewImporter(database.repo, DefaultImportLimits())
	plan, err := importer.DryRun(context.Background(), directory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	id := deterministicLocalID(plan.SourceIdentity, plan.ExportID, "import_operation", plan.TargetSpaceID)
	if _, err := database.repo.StartImport(context.Background(), storage.ImportOperationSpec{ImportOperationID: id, OperationID: id, SourceID: deterministicLocalID(plan.SourceIdentity, "source"), SourceIdentity: plan.SourceIdentity, ExportID: plan.ExportID, TargetSpaceID: rootSpace, PackageHash: plan.PackageHash, PlanHash: plan.PlanHash, TotalObjects: len(plan.Items), CreatedBy: "test", SpaceMappings: plan.SpaceMappings}); err != nil {
		t.Fatal(err)
	}
	generation, err := database.repo.ClaimImport(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.repo.CancelImport(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	err = database.repo.CommitImportBatch(context.Background(), id, generation, 0, nil, "spaces.jsonl", "space", "test")
	if !errors.Is(err, storage.ErrImportFenceLost) {
		t.Fatalf("stale batch error = %v", err)
	}
}

func TestExportFreshImportRebuildsEquivalentAuthorityAndRetrieval(t *testing.T) {
	source := newTestDatabase(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	packageDirectory := filepath.Join(root, "package")
	plan, err := NewExporter(source.repo).Export(context.Background(), request(), packageDirectory, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fresh := newFreshImportDatabase(t)
	status, err := NewImporter(fresh.repo, DefaultImportLimits()).Import(context.Background(), packageDirectory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "portability-drill"})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "succeeded" || status.CommittedObjects != status.TotalObjects {
		t.Fatalf("fresh import status = %+v, error = %v", status, err)
	}
	if err := fresh.repo.RebuildFTS(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := fresh.repo.SearchMemories(context.Background(), storage.SearchParams{Query: "alpha", SpaceIDs: []string{rootSpace}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 10, MaxChars: 1000})
	if err != nil || len(result.Results) != 1 || result.Results[0].Memory.CurrentRevision.DisplayContent != "alpha" {
		t.Fatalf("imported memory retrieval = %+v, error = %v", result, err)
	}
	var mappings, provenance, importedMemories int
	if err := fresh.writer.QueryRow(`SELECT (SELECT count(*) FROM import_object_mappings),(SELECT count(*) FROM imported_provenance),(SELECT count(*) FROM memories)`).Scan(&mappings, &provenance, &importedMemories); err != nil {
		t.Fatal(err)
	}
	if mappings != status.TotalObjects || provenance != mappings || importedMemories != 1 {
		t.Fatalf("imported authority/mapping counts = %d/%d/%d, import objects=%d package objects=%d", mappings, provenance, importedMemories, status.TotalObjects, planObjectCount(plan))
	}
}

func TestPortabilityPerformanceAtTargetScale(t *testing.T) {
	gates := loadPortabilityGates(t)
	source := newTestDatabase(t)
	tx, err := source.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < gates.PortabilityObjectCount; n++ {
		memoryID := fmt.Sprintf("perf-memory-%04d", n)
		revisionID := fmt.Sprintf("perf-revision-%04d", n)
		if _, err = tx.Exec(`INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,sensitivity,created_at,updated_at) VALUES (?,'`+taskSpace+`',?,'active','normal','2026-08-25T10:00:00Z','2026-08-25T10:00:00Z'); INSERT INTO memory_revisions(revision_id,memory_id,kind,display_content,canonical_content,canonical_hash,confidence,specificity,created_by,processing_version,created_at) VALUES (?,?,'fact',?,?,?,0.9,0.5,'test','v1','2026-08-25T10:00:00Z')`, memoryID, revisionID, revisionID, memoryID, "portable benchmark "+memoryID, "portable benchmark "+memoryID, "hash-"+memoryID); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	packageDirectory := filepath.Join(root, "package")
	runtime.GC()
	started := time.Now()
	plan, err := NewExporter(source.repo).Export(context.Background(), request(), packageDirectory, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fresh := newFreshImportDatabase(t)
	limits := DefaultImportLimits()
	limits.BatchSize = 25
	status, err := NewImporter(fresh.repo, limits).Import(context.Background(), packageDirectory, ImportRequest{TargetSpaceID: rootSpace, CreatedBy: "performance-test"})
	if err != nil {
		t.Fatal(err)
	}
	duration := time.Since(started)
	objects := planObjectCount(plan)
	throughput := float64(objects) / duration.Seconds()
	var checkpoints int
	if err := fresh.writer.QueryRow(`SELECT count(*) FROM import_checkpoints WHERE import_operation_id=?`, status.ImportOperationID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	packageBytes := directorySize(t, packageDirectory)
	temporaryBytes := int64(0)
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".dmem-logical-") {
			temporaryBytes += directorySize(t, filepath.Join(root, entry.Name()))
		}
	}
	tempAmplification := 0.0
	if packageBytes > 0 {
		tempAmplification = float64(temporaryBytes) / float64(packageBytes)
	}
	t.Logf("portability performance: objects=%d duration=%s throughput=%.1f/s checkpoints=%d temp=%.2fx", objects, duration, throughput, checkpoints, tempAmplification)
	if throughput < gates.MinimumPortabilityObjectsPerSecond || tempAmplification > gates.MaximumPortabilityTempAmplification || checkpoints > gates.MaximumPortabilityCheckpoints {
		t.Fatal("portability performance thresholds exceeded")
	}
}

type portabilityPerformanceGates struct {
	PortabilityObjectCount              int     `json:"portability_object_count"`
	MinimumPortabilityObjectsPerSecond  float64 `json:"minimum_portability_objects_per_second"`
	MaximumPortabilityTempAmplification float64 `json:"maximum_portability_temp_amplification"`
	MaximumPortabilityCheckpoints       int     `json:"maximum_portability_checkpoints"`
}

func loadPortabilityGates(t *testing.T) portabilityPerformanceGates {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "storage", "testdata", "search-quality-gates-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		ConfigVersion string                      `json:"config_version"`
		Performance   portabilityPerformanceGates `json:"performance"`
	}
	if err := json.Unmarshal(data, &config); err != nil || config.ConfigVersion != "search-quality-gates-v1" || config.Performance.PortabilityObjectCount < 100 {
		t.Fatalf("invalid portability performance gates: %v", err)
	}
	return config.Performance
}

func newFreshImportDatabase(t *testing.T) testDatabase {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fresh.db")
	store, err := storage.Open(context.Background(), path, storage.Options{ReadConnections: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	counter := 0
	repo := storage.NewRepository(store, domain.Runtime{Now: time.Now, ID: func() (string, error) {
		counter++
		return fmt.Sprintf("fresh-id-%d", counter), nil
	}}, 10, 20, 3, "test-v1")
	writer, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { writer.Close() })
	if _, err := writer.Exec(`INSERT INTO memory_spaces VALUES ('` + rootSpace + `',NULL,'user','fresh','Fresh','2026-08-25T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return testDatabase{path: path, store: store, repo: repo, writer: writer}
}

func planObjectCount(plan Plan) int {
	total := 0
	for _, count := range plan.ObjectCounts {
		total += count
	}
	return total
}

func directorySize(t *testing.T, path string) int64 {
	t.Helper()
	var total int64
	if err := filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			total += info.Size()
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return total
}

func resealPackage(t *testing.T, directory string) {
	t.Helper()
	manifestData, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	checksums := map[string]string{}
	var total int64
	for _, member := range dataMembers {
		data, err := os.ReadFile(filepath.Join(directory, member))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		checksums[member] = hex.EncodeToString(sum[:])
		total += int64(len(data))
		lines := 0
		for _, b := range data {
			if b == '\n' {
				lines++
			}
		}
		manifest.ObjectCounts[member] = lines
	}
	manifest.FileChecksums, manifest.TotalSize = checksums, total
	manifestData, _ = json.Marshal(manifest)
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestSum := sha256.Sum256(manifestData)
	all := make(map[string]string, len(checksums)+1)
	for name, sum := range checksums {
		all[name] = sum
	}
	all["manifest.json"] = hex.EncodeToString(manifestSum[:])
	checksumData, _ := json.Marshal(Checksums{Algorithm: "sha256", Files: all})
	if err := os.WriteFile(filepath.Join(directory, "checksums.json"), append(checksumData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
