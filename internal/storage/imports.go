package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

var ErrImportFenceLost = errors.New("import generation fence lost")

type ImportOperationSpec struct {
	ImportOperationID string
	OperationID       string
	SourceID          string
	SourceIdentity    string
	ExportID          string
	TargetSpaceID     string
	PackageHash       string
	PlanHash          string
	TotalObjects      int
	CreatedBy         string
	SpaceMappings     map[string]string
}

type ImportObject struct {
	SourceType string
	SourceID   string
	LocalID    string
	ObjectHash string
	Data       map[string]any
	Action     string
}

type ImportOperationStatus struct {
	ImportOperationID string `json:"import_operation_id"`
	State             string `json:"state"`
	Generation        int64  `json:"generation"`
	TotalObjects      int    `json:"total_objects"`
	CommittedObjects  int    `json:"committed_objects"`
	LastErrorCode     string `json:"last_error_code,omitempty"`
	LastErrorMessage  string `json:"last_error_message,omitempty"`
}

func (r *Repository) ExistingImportMappings(ctx context.Context, sourceIdentity, exportID string) (map[string]struct{ LocalID, Hash string }, error) {
	rows, err := r.store.reader.QueryContext(ctx, `SELECT source_object_type,source_object_id,local_object_id,object_hash FROM import_object_mappings m JOIN import_sources s ON s.source_id=m.source_id WHERE s.source_identity=? AND m.export_id=?`, sourceIdentity, exportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]struct{ LocalID, Hash string }{}
	for rows.Next() {
		var typ, id, local, hash string
		if err := rows.Scan(&typ, &id, &local, &hash); err != nil {
			return nil, err
		}
		result[typ+"\x00"+id] = struct{ LocalID, Hash string }{local, hash}
	}
	return result, rows.Err()
}

func (r *Repository) FindMemoryByContentHash(ctx context.Context, targetSpaceID, hash string) (string, string, error) {
	var memoryID, revisionID string
	err := r.store.reader.QueryRowContext(ctx, `SELECT m.memory_id,m.current_revision_id FROM memories m JOIN memory_revisions v ON v.memory_id=m.memory_id AND v.revision_id=m.current_revision_id WHERE m.space_id=? AND v.canonical_hash=? ORDER BY m.memory_id LIMIT 1`, targetSpaceID, hash).Scan(&memoryID, &revisionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return memoryID, revisionID, err
}

func (r *Repository) CurrentRevision(ctx context.Context, memoryID string) (string, string, error) {
	var revisionID, hash string
	err := r.store.reader.QueryRowContext(ctx, `SELECT m.current_revision_id,v.canonical_hash FROM memories m JOIN memory_revisions v ON v.memory_id=m.memory_id AND v.revision_id=m.current_revision_id WHERE m.memory_id=?`, memoryID).Scan(&revisionID, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return revisionID, hash, err
}

func (r *Repository) StartImport(ctx context.Context, spec ImportOperationSpec) (ImportOperationStatus, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return ImportOperationStatus{}, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM memory_spaces WHERE space_id=?`, spec.TargetSpaceID).Scan(new(int)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ImportOperationStatus{}, ErrNotFound
		}
		return ImportOperationStatus{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO import_sources(source_id,source_identity,format_family,format_version,first_export_id,created_at) VALUES (?,?, 'dmem-logical',1,?,?) ON CONFLICT(source_identity) DO NOTHING`, spec.SourceID, spec.SourceIdentity, spec.ExportID, now)
	if err != nil {
		return ImportOperationStatus{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT source_id FROM import_sources WHERE source_identity=?`, spec.SourceIdentity).Scan(&spec.SourceID); err != nil {
		return ImportOperationStatus{}, err
	}
	var existing ImportOperationStatus
	err = tx.QueryRowContext(ctx, `SELECT import_operation_id,state,generation,total_objects,committed_objects,coalesce(last_error_code,''),coalesce(last_error_message,'') FROM import_operations WHERE source_id=? AND export_id=? AND target_space_id=?`, spec.SourceID, spec.ExportID, spec.TargetSpaceID).Scan(&existing.ImportOperationID, &existing.State, &existing.Generation, &existing.TotalObjects, &existing.CommittedObjects, &existing.LastErrorCode, &existing.LastErrorMessage)
	if err == nil {
		var packageHash, planHash string
		if err := tx.QueryRowContext(ctx, `SELECT package_hash,plan_hash FROM import_operations WHERE import_operation_id=?`, existing.ImportOperationID).Scan(&packageHash, &planHash); err != nil {
			return ImportOperationStatus{}, err
		}
		if packageHash != spec.PackageHash || planHash != spec.PlanHash {
			return ImportOperationStatus{}, ErrIdempotencyConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ImportOperationStatus{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO import_operations(import_operation_id,operation_id,source_id,export_id,target_space_id,package_hash,plan_hash,state,generation,total_objects,created_by,created_at,updated_at) VALUES (?,?,?,?,?,?,?,'pending',0,?,?,?,?)`, spec.ImportOperationID, spec.OperationID, spec.SourceID, spec.ExportID, spec.TargetSpaceID, spec.PackageHash, spec.PlanHash, spec.TotalObjects, spec.CreatedBy, now, now)
	if err != nil {
		return ImportOperationStatus{}, err
	}
	for source, local := range spec.SpaceMappings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_space_mappings(import_operation_id,source_space_id,local_space_id,created_at) VALUES (?,?,?,?)`, spec.ImportOperationID, source, local, now); err != nil {
			return ImportOperationStatus{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ImportOperationStatus{}, err
	}
	return ImportOperationStatus{ImportOperationID: spec.ImportOperationID, State: "pending", TotalObjects: spec.TotalObjects}, nil
}

func (r *Repository) ClaimImport(ctx context.Context, operationID string) (int64, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `UPDATE import_operations SET state='running',generation=generation+1,started_at=coalesce(started_at,?),finished_at=NULL,updated_at=? WHERE import_operation_id=? AND state IN ('pending','waiting','partial')`, now, now, operationID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return 0, ErrImportFenceLost
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM import_operations WHERE import_operation_id=?`, operationID).Scan(&generation); err != nil {
		return 0, err
	}
	return generation, tx.Commit()
}

func (r *Repository) CommitImportBatch(ctx context.Context, operationID string, generation int64, batch int, objects []ImportObject, lastMember, lastObjectID, importedBy string) error {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var committed int
	if err := tx.QueryRowContext(ctx, `SELECT state,committed_objects FROM import_operations WHERE import_operation_id=? AND generation=?`, operationID, generation).Scan(&state, &committed); err != nil || state != "running" {
		return ErrImportFenceLost
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	semanticCorpusChanged := false
	authorityChanged := false
	var sourceID, exportID, targetSpace string
	if err := tx.QueryRowContext(ctx, `SELECT source_id,export_id,target_space_id FROM import_operations WHERE import_operation_id=?`, operationID).Scan(&sourceID, &exportID, &targetSpace); err != nil {
		return err
	}
	for _, object := range objects {
		if object.Action == "import" {
			if err := importAuthorityObject(ctx, tx, object, targetSpace, operationID, now); err != nil {
				return fmt.Errorf("import %s %s: %w", object.SourceType, object.SourceID, err)
			}
			authorityChanged = true
			semanticCorpusChanged = semanticCorpusChanged || object.SourceType == "revision"
		} else if object.Action == "proposal" {
			if err := importConflictProposal(ctx, tx, object, targetSpace, operationID, now); err != nil {
				return err
			}
		}
		mappingID := stableImportID("mapping", sourceID, exportID, object.SourceType, object.SourceID)
		_, err := tx.ExecContext(ctx, `INSERT INTO import_object_mappings(mapping_id,source_id,source_object_type,source_object_id,export_id,local_object_id,object_hash,created_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(source_id,source_object_type,source_object_id,export_id) DO NOTHING`, mappingID, sourceID, object.SourceType, object.SourceID, exportID, object.LocalID, object.ObjectHash, now)
		if err != nil {
			return err
		}
		provenanceID := stableImportID("provenance", operationID, mappingID)
		if _, err := tx.ExecContext(ctx, `INSERT INTO imported_provenance(provenance_id,import_operation_id,mapping_id,batch_number,imported_by,imported_at) VALUES (?,?,?,?,?,?) ON CONFLICT(import_operation_id,mapping_id) DO NOTHING`, provenanceID, operationID, mappingID, batch, importedBy, now); err != nil {
			return err
		}
	}
	if authorityChanged {
		if err := advanceCorpusGeneration(ctx, tx, now); err != nil {
			return err
		}
		if err := markImportedDerivedStateStale(ctx, tx, now); err != nil {
			return err
		}
	}
	if semanticCorpusChanged {
		if err := advanceSemanticCorpusGeneration(ctx, tx, now, true); err != nil {
			return err
		}
	}
	committed += len(objects)
	res, err := tx.ExecContext(ctx, `UPDATE import_operations SET committed_objects=?,updated_at=? WHERE import_operation_id=? AND generation=? AND state='running'`, committed, now, operationID, generation)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrImportFenceLost
	}
	checkpointID := stableImportID("checkpoint", operationID, fmt.Sprint(generation), fmt.Sprint(batch))
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_checkpoints(checkpoint_id,import_operation_id,generation,batch_number,last_member,last_object_id,committed_objects,created_at) VALUES (?,?,?,?,?,?,?,?)`, checkpointID, operationID, generation, batch, lastMember, lastObjectID, committed, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) FinishImport(ctx context.Context, operationID string, generation int64) error {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `UPDATE import_operations SET state=CASE WHEN committed_objects=total_objects THEN 'succeeded' ELSE 'partial' END,finished_at=?,updated_at=? WHERE import_operation_id=? AND generation=? AND state='running'`, now, now, operationID, generation)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrImportFenceLost
	}
	for _, job := range []struct{ typ, target, version, state string }{
		{"rebuild_fts", "memory_fts", r.processingVersion, "pending"},
		{"rebuild_semantic", semanticIndexName, r.embeddingIdentity(), "waiting"},
		{"rebuild_entity_index", entityIndexName, r.processingVersion, "pending"},
		{"rebuild_relation_index", relationIndexName, r.processingVersion, "pending"},
		{"rebuild_feedback_aggregates", "feedback_aggregates", FeedbackAlgorithmVersion, "pending"},
		{"rebuild_quality", "memory_quality", r.processingVersion, "pending"},
		{"rebuild_duplicates", "duplicate_clusters", r.processingVersion, "pending"},
		{"rebuild_graph_index", graphIndexName, r.processingVersion, "pending"},
	} {
		id := stableImportID("job", operationID, job.typ)
		_, err = tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,created_at) VALUES (?,?,?,'index',?,?,?,?,?,?) ON CONFLICT DO NOTHING`, id, operationID, job.typ, job.target, job.version, job.state, r.maxAttempts, now, now)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE corpus_metadata SET generation=generation+1,updated_at=? WHERE singleton=1`, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) InterruptImport(ctx context.Context, operationID string, generation int64) error {
	return r.setImportState(ctx, operationID, generation, "waiting", "interrupted")
}
func (r *Repository) FailImport(ctx context.Context, operationID string, generation int64, code, message string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := r.store.writer.ExecContext(ctx, `UPDATE import_operations SET state='failed',generation=generation+1,last_error_code=?,last_error_message=?,finished_at=?,updated_at=? WHERE import_operation_id=? AND generation=? AND state='running'`, code, message, now, now, operationID, generation)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrImportFenceLost
	}
	return nil
}
func (r *Repository) CancelImport(ctx context.Context, operationID string) error {
	_, err := r.store.writer.ExecContext(ctx, `UPDATE import_operations SET state='cancelled',generation=generation+1,finished_at=?,updated_at=? WHERE import_operation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), operationID)
	return err
}
func (r *Repository) setImportState(ctx context.Context, id string, generation int64, state, code string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := r.store.writer.ExecContext(ctx, `UPDATE import_operations SET state=?,generation=generation+1,last_error_code=?,updated_at=? WHERE import_operation_id=? AND generation=? AND state='running'`, state, code, now, id, generation)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrImportFenceLost
	}
	return nil
}
func (r *Repository) ImportStatus(ctx context.Context, id string) (ImportOperationStatus, error) {
	var s ImportOperationStatus
	err := r.store.reader.QueryRowContext(ctx, `SELECT import_operation_id,state,generation,total_objects,committed_objects,coalesce(last_error_code,''),coalesce(last_error_message,'') FROM import_operations WHERE import_operation_id=?`, id).Scan(&s.ImportOperationID, &s.State, &s.Generation, &s.TotalObjects, &s.CommittedObjects, &s.LastErrorCode, &s.LastErrorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return s, err
}

// markImportedDerivedStateStale fences every derived projection while an import
// is in progress, so a partially imported corpus is never reported as a healthy
// index and cursors bound to the previous corpus are invalidated.
func markImportedDerivedStateStale(ctx context.Context, tx *sql.Tx, now string) error {
	statements := []string{
		`UPDATE index_metadata SET state='degraded', updated_at=? WHERE index_name='` + ftsIndexName + `'`,
		`UPDATE projection_metadata SET quality_state='stale', duplicate_state='stale', updated_at=? WHERE singleton=1`,
		`UPDATE entity_index_metadata SET state='stale', generation=generation+1, updated_at=? WHERE singleton=1`,
		`UPDATE relation_index_metadata SET state='stale', generation=generation+1, updated_at=? WHERE singleton=1`,
		`UPDATE graph_index_metadata SET state='stale', generation=generation+1, updated_at=? WHERE singleton=1`,
		`UPDATE feedback_aggregate_metadata SET state='stale', updated_at=? WHERE singleton=1`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, now); err != nil {
			return err
		}
	}
	return nil
}

func importAuthorityObject(ctx context.Context, tx *sql.Tx, o ImportObject, targetSpace, operationID, now string) error {
	d := o.Data
	s := func(k string) any { return d[k] }
	local := func(k string) any {
		if v, ok := d["local_"+k]; ok {
			return v
		}
		return d[k]
	}
	switch o.SourceType {
	case "raw_event":
		contentID := fmt.Sprint(local("content_id"))
		_, err := tx.ExecContext(ctx, `INSERT INTO raw_contents(content_id,content_kind,content_text,reference_uri,media_type,content_hash,size_bytes,declared_size,declared_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, contentID, s("content_kind"), s("content_text"), s("reference_uri"), s("media_type"), s("content_hash"), s("size_bytes"), s("declared_size"), s("declared_hash"), s("content_created_at"))
		if err != nil {
			return err
		}
		streamID, err := resolveSourceStream(ctx, tx, now, targetSpace, fmt.Sprint(s("source_type")), fmt.Sprint(s("source_name")), importText(s("source_stream_key")))
		if err != nil {
			return err
		}
		sequence, err := importedSequence(d["sequence"])
		if err != nil {
			return err
		}
		effectiveSequence, err := eventSequence(ctx, tx, streamID, sequence)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO raw_events(event_id,operation_id,space_id,content_id,source_type,source_name,source_event_id,source_stream_id,sequence,caller_id,occurred_at,received_at,processing_status,metadata_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, operationID, targetSpace, contentID, s("source_type"), s("source_name"), s("source_event_id"), streamID, effectiveSequence, "import:"+fmt.Sprint(s("caller_id")), s("occurred_at"), s("received_at"), s("processing_status"), s("metadata_json"), s("created_at"))
		return err
	case "memory":
		_, err := tx.ExecContext(ctx, `INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,frozen,sensitivity,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`, o.LocalID, targetSpace, local("current_revision_id"), s("current_state"), s("frozen"), s("sensitivity"), s("created_at"), s("updated_at"))
		return err
	case "revision":
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_revisions(revision_id,memory_id,parent_revision_id,restored_from_revision_id,valid_from,valid_until,confidence,kind,display_content,canonical_content,canonical_hash,event_type,event_time_start,event_time_end,time_precision,time_source,source_speaker,language,sensitivity,specificity,created_by,processing_version,processor_identity,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, local("memory_id"), local("parent_revision_id"), local("restored_from_revision_id"), s("valid_from"), s("valid_until"), s("confidence"), s("kind"), s("display_content"), s("canonical_content"), s("canonical_hash"), s("event_type"), s("event_time_start"), s("event_time_end"), s("time_precision"), s("time_source"), s("source_speaker"), s("language"), s("sensitivity"), s("specificity"), "import", s("processing_version"), s("processor_identity"), s("created_at")); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_projections(memory_id,revision_id,space_id,memory_type,current_state,searchable,display_content,content_hash,valid_from,valid_until,updated_at) SELECT m.memory_id,r.revision_id,m.space_id,r.kind,m.current_state,m.current_state IN ('active','contested'),r.display_content,r.canonical_hash,r.valid_from,r.valid_until,? FROM memories m JOIN memory_revisions r ON r.revision_id=m.current_revision_id WHERE m.memory_id=? AND m.current_revision_id=? ON CONFLICT(memory_id) DO UPDATE SET revision_id=excluded.revision_id,space_id=excluded.space_id,memory_type=excluded.memory_type,current_state=excluded.current_state,searchable=excluded.searchable,display_content=excluded.display_content,content_hash=excluded.content_hash,embedding_state='stale',valid_from=excluded.valid_from,valid_until=excluded.valid_until,updated_at=excluded.updated_at`, now, local("memory_id"), o.LocalID)
		return err
	case "revision_subject_entity":
		_, err := tx.ExecContext(ctx, `INSERT INTO revision_subject_entities(revision_id,entity_order,entity_id) VALUES (?,?,?)`, local("revision_id"), s("entity_order"), local("entity_id"))
		return err
	case "revision_object_entity":
		_, err := tx.ExecContext(ctx, `INSERT INTO revision_object_entities(revision_id,entity_order,entity_id) VALUES (?,?,?)`, local("revision_id"), s("entity_order"), local("entity_id"))
		return err
	case "memory_state_event":
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id,memory_id,revision_id,operation_id,from_state,to_state,from_frozen,to_frozen,reason,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, local("memory_id"), local("revision_id"), operationID, s("from_state"), s("to_state"), s("from_frozen"), s("to_frozen"), s("reason"), "import", s("created_at"))
		return err
	case "memory_evidence":
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_evidence(evidence_id,revision_id,event_id,role,start_offset,end_offset,created_at) VALUES (?,?,?,?,?,?,?)`, o.LocalID, local("revision_id"), local("event_id"), s("role"), s("start_offset"), s("end_offset"), s("created_at"))
		return err
	case "agent_tool_source":
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_sources(source_id,event_id,operation_id,source_type,source_name,conversation_id,turn_id,tool_call_id,occurred_at,metadata_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, local("event_id"), operationID, s("source_type"), s("source_name"), s("conversation_id"), s("turn_id"), s("tool_call_id"), s("occurred_at"), s("metadata_json"), s("created_at"))
		return err
	case "agent_tool_search_hint":
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_search_hints(event_id,local_id,revision_id,hint_order,hint,created_at) VALUES (?,?,?,?,?,?)`, local("event_id"), s("local_id"), local("revision_id"), s("hint_order"), s("hint"), s("created_at"))
		return err
	case "agent_tool_proposal":
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_proposals(proposal_id,event_id,space_id,suggestion_type,local_id,normalized_json,specification_hash,review_state,result_memory_id,result_revision_id,created_at,reviewed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, local("event_id"), targetSpace, s("suggestion_type"), s("local_id"), s("normalized_json"), s("specification_hash"), s("review_state"), local("result_memory_id"), local("result_revision_id"), s("created_at"), s("reviewed_at"))
		return err
	case "feedback_event":
		resultRank, rankErr := importedSequence(s("result_rank"))
		if rankErr != nil {
			return rankErr
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO feedback_events(feedback_id,operation_id,caller_id,memory_id,revision_id,feedback_purpose,feedback_type,reason_code,note,search_operation_id,query_hash,query_type,entity_ids_json,ranking_identity,feature_identity,context_identity,result_rank,selection_state,matched_signals_json,answer_identity,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, operationID, s("caller_id"), local("memory_id"), local("revision_id"), s("feedback_purpose"), s("feedback_type"), s("reason_code"), s("note"), s("search_operation_id"), s("query_hash"), s("query_type"), s("entity_ids_json"), s("ranking_identity"), s("feature_identity"), s("context_identity"), resultRank, s("selection_state"), s("matched_signals_json"), s("answer_identity"), s("created_at"))
		return err
	case "relation":
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_relations(relation_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,state,source,confidence,specification_hash,created_by,processing_version,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, targetSpace, s("relation_type"), local("source_memory_id"), local("source_revision_id"), local("target_memory_id"), local("target_revision_id"), s("state"), "import", s("confidence"), s("specification_hash"), "import", s("processing_version"), s("created_at"), s("updated_at"))
		return err
	case "entity":
		_, err := tx.ExecContext(ctx, `INSERT INTO entities(entity_id,space_id,entity_type,canonical_name,normalized_name,normalization_version,state,sensitivity,created_by,processing_version,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, targetSpace, s("entity_type"), s("canonical_name"), s("normalized_name"), s("normalization_version"), s("state"), s("sensitivity"), "import", s("processing_version"), s("created_at"), s("updated_at"))
		return err
	case "entity_event":
		_, err := tx.ExecContext(ctx, `INSERT INTO entity_events(entity_event_id,entity_id,operation_id,event_type,from_canonical_name,to_canonical_name,from_state,to_state,normalization_version,reason,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, local("entity_id"), operationID, s("event_type"), s("from_canonical_name"), s("to_canonical_name"), s("from_state"), s("to_state"), s("normalization_version"), s("reason"), "import", s("created_at"))
		return err
	case "entity_alias":
		_, err := tx.ExecContext(ctx, `INSERT INTO entity_aliases(alias_id,entity_id,alias,normalized_alias,normalization_version,source,state,created_by,created_at,updated_at) VALUES (?,?,?,?,?,'import',?,?,?,?)`, o.LocalID, local("entity_id"), s("alias"), s("normalized_alias"), s("normalization_version"), s("state"), "import", s("created_at"), s("updated_at"))
		return err
	case "entity_alias_event":
		_, err := tx.ExecContext(ctx, `INSERT INTO entity_alias_events(alias_event_id,alias_id,operation_id,from_state,to_state,reason,created_by,created_at) VALUES (?,?,?,?,?,?,?,?)`, o.LocalID, local("alias_id"), operationID, s("from_state"), s("to_state"), s("reason"), "import", s("created_at"))
		return err
	case "entity_mention":
		_, err := tx.ExecContext(ctx, `INSERT INTO entity_mentions(mention_id,entity_id,memory_id,revision_id,event_id,start_offset,end_offset,mention_type,source,surface_text,confidence,created_by,processing_version,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, local("entity_id"), local("memory_id"), local("revision_id"), local("event_id"), s("start_offset"), s("end_offset"), s("mention_type"), s("source"), s("surface_text"), s("confidence"), "import", s("processing_version"), s("created_at"))
		return err
	case "relation_evidence":
		_, err := tx.ExecContext(ctx, `INSERT INTO relation_evidence(evidence_id,relation_id,evidence_type,event_id,memory_id,revision_id,start_offset,end_offset,note,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, local("relation_id"), s("evidence_type"), local("event_id"), local("memory_id"), local("revision_id"), s("start_offset"), s("end_offset"), s("note"), "import", s("created_at"))
		return err
	case "relation_state_event":
		_, err := tx.ExecContext(ctx, `INSERT INTO relation_state_events(state_event_id,relation_id,operation_id,from_state,to_state,reason,created_by,created_at) VALUES (?,?,?,?,?,?,?,?)`, o.LocalID, local("relation_id"), operationID, s("from_state"), s("to_state"), s("reason"), "import", s("created_at"))
		return err
	case "entity_fact":
		_, err := tx.ExecContext(ctx, `INSERT INTO entity_facts(fact_id,space_id,subject_entity_id,predicate,object_entity_id,object_literal,object_literal_type,source_memory_id,source_revision_id,confidence,state,source,specification_hash,created_by,processing_version,valid_from,valid_until,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, targetSpace, local("subject_entity_id"), s("predicate"), local("object_entity_id"), s("object_literal"), s("object_literal_type"), local("source_memory_id"), local("source_revision_id"), s("confidence"), s("state"), "import", s("specification_hash"), "import", s("processing_version"), s("valid_from"), s("valid_until"), s("created_at"), s("updated_at"))
		return err
	case "entity_fact_evidence":
		_, err := tx.ExecContext(ctx, `INSERT INTO entity_fact_evidence(evidence_id,fact_id,evidence_type,event_id,memory_id,revision_id,start_offset,end_offset,note,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, o.LocalID, local("fact_id"), s("evidence_type"), local("event_id"), local("memory_id"), local("revision_id"), s("start_offset"), s("end_offset"), s("note"), "import", s("created_at"))
		return err
	case "entity_fact_state_event":
		_, err := tx.ExecContext(ctx, `INSERT INTO entity_fact_state_events(state_event_id,fact_id,operation_id,from_state,to_state,reason,created_by,created_at) VALUES (?,?,?,?,?,?,?,?)`, o.LocalID, local("fact_id"), operationID, s("from_state"), s("to_state"), s("reason"), "import", s("created_at"))
		return err
	case "aggregate_descriptor":
		_, err := tx.ExecContext(ctx, `INSERT INTO aggregate_descriptors(aggregate_id,memory_id,aggregate_kind,policy_version,processing_version,sensitivity,created_by,created_at) VALUES (?,?,?,?,?,?,?,?)`, o.LocalID, local("memory_id"), s("aggregate_kind"), s("policy_version"), s("processing_version"), s("sensitivity"), "import", s("created_at"))
		return err
	case "conflict_case":
		_, err := tx.ExecContext(ctx, `INSERT INTO conflict_cases(case_id,space_id,state,opened_by,processing_version,created_at,closed_at) VALUES (?,?,?,?,?,?,?)`, o.LocalID, targetSpace, s("state"), "import", s("processing_version"), s("created_at"), s("closed_at"))
		return err
	case "conflict_participant":
		_, err := tx.ExecContext(ctx, `INSERT INTO conflict_case_participants(case_id,participant_order,memory_id,revision_id,input_state,input_sensitivity,created_at) VALUES (?,?,?,?,?,?,?)`, local("case_id"), s("participant_order"), local("memory_id"), local("revision_id"), s("input_state"), s("input_sensitivity"), s("created_at"))
		return err
	case "conflict_relation":
		_, err := tx.ExecContext(ctx, `INSERT INTO conflict_case_relations(case_id,relation_id,created_at) VALUES (?,?,?)`, local("case_id"), local("relation_id"), s("created_at"))
		return err
	case "conflict_resolution":
		_, err := tx.ExecContext(ctx, `INSERT INTO conflict_resolution_events(resolution_event_id,case_id,operation_id,resolution_type,from_state,to_state,reason,resolved_by,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, o.LocalID, local("case_id"), operationID, s("resolution_type"), s("from_state"), s("to_state"), s("reason"), "import", s("created_at"))
		return err
	case "conflict_resolution_ref":
		_, err := tx.ExecContext(ctx, `INSERT INTO conflict_resolution_result_refs(result_ref_id,resolution_event_id,result_type,result_id,created_at) VALUES (?,?,?,?,?)`, o.LocalID, local("resolution_event_id"), s("result_type"), s("result_id"), s("created_at"))
		return err
	default:
		return nil
	}
}

func importConflictProposal(ctx context.Context, tx *sql.Tx, o ImportObject, targetSpace, operationID, now string) error {
	d := o.Data
	eventID := fmt.Sprint(d["local_event_id"])
	if eventID == "" || eventID == "<nil>" {
		return errors.New("conflicting import requires source event")
	}
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT current_revision_id FROM memories WHERE memory_id=?`, d["target_memory_id"]).Scan(&current); err != nil {
		return err
	}
	if current != fmt.Sprint(d["expected_revision_id"]) {
		return ErrConflict
	}
	proposalID := fmt.Sprint(d["local_proposal_id"])
	_, err := tx.ExecContext(ctx, `INSERT INTO evolution_proposals(proposal_id,event_id,space_id,action,proposed_content,proposed_content_hash,memory_type,sensitivity,processor_identity,processing_version,specification_hash,created_at) VALUES (?, ?,?,'conflict',?,?,?,?, 'import','logical-v1',?,?)`, proposalID, eventID, targetSpace, d["display_content"], d["canonical_hash"], d["memory_type"], d["sensitivity"], o.ObjectHash, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO evolution_proposal_targets(proposal_id,target_order,memory_id,expected_revision_id,created_at) VALUES (?,0,?,?,?)`, proposalID, d["target_memory_id"], d["expected_revision_id"], now)
	return err
}

func importText(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func importedSequence(value any) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case int64:
		if v < 0 {
			return nil, errors.New("invalid source stream sequence")
		}
		return &v, nil
	case float64:
		if v < 0 || v != math.Trunc(v) {
			return nil, errors.New("invalid source stream sequence")
		}
		sequence := int64(v)
		return &sequence, nil
	case string:
		sequence, err := strconv.ParseInt(v, 10, 64)
		if err != nil || sequence < 0 {
			return nil, errors.New("invalid source stream sequence")
		}
		return &sequence, nil
	default:
		return nil, errors.New("invalid source stream sequence")
	}
}

func stableImportID(parts ...string) string {
	h := sha256Bytes(parts...)
	return fmt.Sprintf("%s-%s-4%s-a%s-%s", h[:8], h[8:12], h[13:16], h[17:20], h[20:32])
}

func sha256Bytes(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
