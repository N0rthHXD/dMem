package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
)

var (
	ErrInvalidRelationProposal  = errors.New("invalid relation proposal")
	ErrRelationProposalReviewed = errors.New("relation proposal has already been reviewed")
)

const relationIndexName = "relation_recall"

type CreateRelationProposalParams struct {
	EventID, SpaceID, RelationType       string
	SourceMemoryID, SourceRevisionID     string
	TargetMemoryID, TargetRevisionID     string
	Confidence                           *float64
	Evidence                             []domain.RelationEvidence
	ProcessorIdentity, ProcessingVersion string
	ProviderIdentity, ModelIdentity      *string
}

type RelationProposalFilter struct {
	SpaceID, ReviewState, RelationType, Cursor string
	Limit                                      int
}

type ReviewRelationProposalParams struct {
	ReasonCode string
	Note       *string
}

type RelationFilter struct {
	SpaceID, State, RelationType, Cursor string
	MemoryID, RevisionID, Direction      string
	Limit                                int
}

func relationSpecificationHash(p CreateRelationProposalParams) string {
	value := struct {
		Type, SourceMemory, SourceRevision, TargetMemory, TargetRevision string
		Confidence                                                       *float64
		Evidence                                                         []domain.RelationEvidence
	}{p.RelationType, p.SourceMemoryID, p.SourceRevisionID, p.TargetMemoryID, p.TargetRevisionID, p.Confidence, p.Evidence}
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func validRelationType(v string) bool {
	return contains([]string{"supports", "contradicts", "supersedes", "derived_from"}, v)
}

func (r *Repository) CreateRelationProposal(ctx context.Context, identity WriteIdentity, p CreateRelationProposalParams) (domain.RelationProposal, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.RelationProposal{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.RelationProposal{}, false, err
	} else if found {
		var result domain.RelationProposal
		if status != 201 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.RelationProposal{}, false, err
	}
	if !validRelationType(p.RelationType) || p.ProcessorIdentity == "" || p.ProcessingVersion == "" || len(p.Evidence) == 0 || p.SourceMemoryID == p.TargetMemoryID && p.SourceRevisionID == p.TargetRevisionID || p.Confidence != nil && (*p.Confidence < 0 || *p.Confidence > 1) {
		return domain.RelationProposal{}, false, ErrInvalidRelationProposal
	}
	if err := validateRelationContext(ctx, tx, p, false); err != nil {
		return domain.RelationProposal{}, false, err
	}
	hash := relationSpecificationHash(p)
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT proposal_id FROM relation_proposals WHERE event_id=? AND processing_version=? AND relation_type=? AND source_memory_id=? AND source_revision_id=? AND target_memory_id=? AND target_revision_id=? AND specification_hash=?`, p.EventID, p.ProcessingVersion, p.RelationType, p.SourceMemoryID, p.SourceRevisionID, p.TargetMemoryID, p.TargetRevisionID, hash).Scan(&existing)
	if err == nil {
		result, e := getRelationProposal(ctx, tx, existing)
		return result, true, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.RelationProposal{}, false, err
	}
	ids, err := r.newIDs(3)
	if err != nil {
		return domain.RelationProposal{}, false, err
	}
	now := formatTime(r.runtime.Now())
	evidence, _ := json.Marshal(p.Evidence)
	_, err = tx.ExecContext(ctx, `INSERT INTO relation_proposals(proposal_id,event_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,confidence,evidence_json,processor_identity,provider_identity,model_identity,processing_version,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ids[0], p.EventID, p.SpaceID, p.RelationType, p.SourceMemoryID, p.SourceRevisionID, p.TargetMemoryID, p.TargetRevisionID, p.Confidence, string(evidence), p.ProcessorIdentity, p.ProviderIdentity, p.ModelIdentity, p.ProcessingVersion, hash, now)
	if err != nil {
		return domain.RelationProposal{}, false, fmt.Errorf("%w: %v", ErrInvalidRelationProposal, err)
	}
	result, err := getRelationProposal(ctx, tx, ids[0])
	if err != nil {
		return result, false, err
	}
	body, _ := json.Marshal(result)
	if err = writeIdempotency(ctx, tx, identity, ids[1], 201, body, now); err != nil {
		return result, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,created_at) VALUES (?,?,?,?,'create_relation_proposal','relation_proposal',?,'succeeded',?)`, ids[2], ids[1], identity.RequestID, identity.CallerID, ids[0], now)
	if err != nil {
		return result, false, err
	}
	if err = tx.Commit(); err != nil {
		return result, false, err
	}
	return result, false, nil
}

func validateRelationContext(ctx context.Context, q candidateQueryer, p CreateRelationProposalParams, current bool) error {
	var eventSpace string
	if err := q.QueryRowContext(ctx, `SELECT space_id FROM raw_events WHERE event_id=?`, p.EventID).Scan(&eventSpace); err != nil || eventSpace != p.SpaceID {
		return ErrInvalidRelationProposal
	}
	for _, endpoint := range [][2]string{{p.SourceMemoryID, p.SourceRevisionID}, {p.TargetMemoryID, p.TargetRevisionID}} {
		var space, currentRevision string
		var frozen bool
		if err := q.QueryRowContext(ctx, `SELECT m.space_id,m.current_revision_id,m.frozen FROM memories m JOIN memory_revisions r ON r.memory_id=m.memory_id WHERE m.memory_id=? AND r.revision_id=?`, endpoint[0], endpoint[1]).Scan(&space, &currentRevision, &frozen); err != nil || space != p.SpaceID {
			return ErrInvalidRelationProposal
		}
		if current && currentRevision != endpoint[1] {
			return ErrRevisionConflict
		}
		if current && frozen {
			return fmt.Errorf("%w: %w", ErrRevisionConflict, ErrFrozenRevision)
		}
	}
	for _, evidence := range p.Evidence {
		if err := validateRelationEvidence(ctx, q, p.SpaceID, evidence); err != nil {
			return err
		}
	}
	return nil
}

func validateRelationEvidence(ctx context.Context, q candidateQueryer, space string, e domain.RelationEvidence) error {
	if e.EvidenceType == "human_confirmation" {
		if e.EventID != nil || e.MemoryID != nil || e.RevisionID != nil || e.StartOffset != nil || e.EndOffset != nil {
			return ErrInvalidRelationProposal
		}
		return nil
	}
	if e.StartOffset == nil || e.EndOffset == nil || *e.StartOffset < 0 || *e.EndOffset <= *e.StartOffset {
		return ErrInvalidRelationProposal
	}
	var text, sourceSpace string
	switch e.EvidenceType {
	case "raw_event_range":
		if e.EventID == nil || e.MemoryID != nil || e.RevisionID != nil {
			return ErrInvalidRelationProposal
		}
		if err := q.QueryRowContext(ctx, `SELECT c.content_text,e.space_id FROM raw_events e JOIN raw_contents c ON c.content_id=e.content_id WHERE e.event_id=? AND c.content_kind IN ('text','json')`, *e.EventID).Scan(&text, &sourceSpace); err != nil {
			return ErrInvalidRelationProposal
		}
	case "revision_range":
		if e.EventID != nil || e.MemoryID == nil || e.RevisionID == nil {
			return ErrInvalidRelationProposal
		}
		if err := q.QueryRowContext(ctx, `SELECT r.display_content,m.space_id FROM memory_revisions r JOIN memories m ON m.memory_id=r.memory_id WHERE r.memory_id=? AND r.revision_id=?`, *e.MemoryID, *e.RevisionID).Scan(&text, &sourceSpace); err != nil {
			return ErrInvalidRelationProposal
		}
	default:
		return ErrInvalidRelationProposal
	}
	if sourceSpace != space || *e.EndOffset > utf8.RuneCountInString(text) {
		return ErrInvalidRelationProposal
	}
	return nil
}

func (r *Repository) GetRelationProposal(ctx context.Context, id string) (domain.RelationProposal, error) {
	return getRelationProposal(ctx, r.store.reader, id)
}

func getRelationProposal(ctx context.Context, q candidateQueryer, id string) (domain.RelationProposal, error) {
	var p domain.RelationProposal
	var evidence string
	err := q.QueryRowContext(ctx, `SELECT proposal_id,event_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,confidence,evidence_json,processor_identity,provider_identity,model_identity,processing_version,specification_hash,review_state,result_relation_id,created_at,reviewed_at FROM relation_proposals WHERE proposal_id=?`, id).Scan(&p.ProposalID, &p.EventID, &p.SpaceID, &p.RelationType, &p.SourceMemoryID, &p.SourceRevisionID, &p.TargetMemoryID, &p.TargetRevisionID, &p.Confidence, &evidence, &p.ProcessorIdentity, &p.ProviderIdentity, &p.ModelIdentity, &p.ProcessingVersion, &p.SpecificationHash, &p.ReviewState, &p.ResultRelationID, &p.CreatedAt, &p.ReviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Evidence, p.Reviews = []domain.RelationEvidence{}, []domain.RelationProposalReview{}
	if err = json.Unmarshal([]byte(evidence), &p.Evidence); err != nil {
		return p, err
	}
	rows, err := q.QueryContext(ctx, `SELECT review_id,from_state,to_state,reason_code,note,reviewer_id,relation_id,created_at FROM relation_proposal_reviews WHERE proposal_id=? ORDER BY created_at DESC,review_id DESC`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var v domain.RelationProposalReview
		if err = rows.Scan(&v.ReviewID, &v.FromState, &v.ToState, &v.ReasonCode, &v.Note, &v.ReviewerID, &v.RelationID, &v.CreatedAt); err != nil {
			return p, err
		}
		p.Reviews = append(p.Reviews, v)
	}
	return p, rows.Err()
}

func (r *Repository) ListRelationProposals(ctx context.Context, f RelationProposalFilter) (domain.RelationProposalPage, error) {
	query, args := `SELECT proposal_id FROM relation_proposals WHERE space_id=?`, []any{f.SpaceID}
	if f.ReviewState != "" {
		query += ` AND review_state=?`
		args = append(args, f.ReviewState)
	}
	if f.RelationType != "" {
		query += ` AND relation_type=?`
		args = append(args, f.RelationType)
	}
	if f.Cursor != "" {
		query += ` AND proposal_id<?`
		args = append(args, f.Cursor)
	}
	query += ` ORDER BY proposal_id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.RelationProposalPage{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return domain.RelationProposalPage{}, err
	}
	page := domain.RelationProposalPage{Items: []domain.RelationProposal{}}
	for _, id := range ids {
		p, e := r.GetRelationProposal(ctx, id)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, p)
	}
	if len(page.Items) > f.Limit {
		next := page.Items[f.Limit-1].ProposalID
		page.NextCursor = &next
		page.Items = page.Items[:f.Limit]
	}
	return page, nil
}

func (r *Repository) ConfirmRelationProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewRelationProposalParams) (domain.RelationProposal, string, bool, error) {
	return r.reviewRelationProposal(ctx, identity, id, review, true)
}
func (r *Repository) RejectRelationProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewRelationProposalParams) (domain.RelationProposal, string, bool, error) {
	return r.reviewRelationProposal(ctx, identity, id, review, false)
}

func (r *Repository) reviewRelationProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewRelationProposalParams, confirm bool) (domain.RelationProposal, string, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.RelationProposal{}, "", false, err
	}
	defer tx.Rollback()
	status := 200
	if confirm {
		status = 201
	}
	if s, b, found, e := readIdempotency(ctx, tx, identity); e != nil {
		return domain.RelationProposal{}, "", false, e
	} else if found {
		var p domain.RelationProposal
		if s != status || json.Unmarshal([]byte(b), &p) != nil {
			return p, "", false, errors.New("invalid stored idempotency response")
		}
		operationID, e := readIdempotencyOperation(ctx, tx, identity)
		if e != nil {
			return p, "", false, e
		}
		return p, operationID, true, nil
	}
	p, err := getRelationProposal(ctx, tx, id)
	if err != nil {
		return p, "", false, err
	}
	if p.ReviewState != "pending" {
		return p, "", false, ErrRelationProposalReviewed
	}
	if err = r.requireDiskWritable(); err != nil {
		return p, "", false, err
	}
	ids, err := r.newIDs(8 + len(p.Evidence))
	if err != nil {
		return p, "", false, err
	}
	operationID, reviewID, auditID := ids[0], ids[1], ids[2]
	now := formatTime(r.runtime.Now())
	toState := "rejected"
	var relationID *string
	if confirm {
		params := CreateRelationProposalParams{EventID: p.EventID, SpaceID: p.SpaceID, RelationType: p.RelationType, SourceMemoryID: p.SourceMemoryID, SourceRevisionID: p.SourceRevisionID, TargetMemoryID: p.TargetMemoryID, TargetRevisionID: p.TargetRevisionID, Confidence: p.Confidence, Evidence: p.Evidence, ProcessorIdentity: p.ProcessorIdentity, ProcessingVersion: p.ProcessingVersion}
		if err = validateRelationContext(ctx, tx, params, true); err != nil {
			return p, "", false, err
		}
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT relation_id FROM memory_relations WHERE relation_type=? AND source_memory_id=? AND source_revision_id=? AND target_memory_id=? AND target_revision_id=? AND specification_hash=?`, p.RelationType, p.SourceMemoryID, p.SourceRevisionID, p.TargetMemoryID, p.TargetRevisionID, p.SpecificationHash).Scan(&existing)
		if err == nil {
			relationID = &existing
		} else if !errors.Is(err, sql.ErrNoRows) {
			return p, "", false, err
		} else {
			rid := ids[3]
			relationID = &rid
			_, err = tx.ExecContext(ctx, `INSERT INTO memory_relations(relation_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,state,source,confidence,specification_hash,created_by,processing_version,created_at,updated_at) VALUES (?,?,?,?,?,?,?,'active','human_confirmation',?,?,?,?,?,?)`, rid, p.SpaceID, p.RelationType, p.SourceMemoryID, p.SourceRevisionID, p.TargetMemoryID, p.TargetRevisionID, p.Confidence, p.SpecificationHash, identity.CallerID, p.ProcessingVersion, now, now)
			if err != nil {
				return p, "", false, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO relation_state_events(state_event_id,relation_id,operation_id,from_state,to_state,reason,created_by,created_at) VALUES (?,?,?,NULL,'active',?,?,?)`, ids[4], rid, operationID, review.ReasonCode, identity.CallerID, now)
			if err != nil {
				return p, "", false, err
			}
			for i, e := range p.Evidence {
				_, err = tx.ExecContext(ctx, `INSERT INTO relation_evidence(evidence_id,relation_id,evidence_type,event_id,memory_id,revision_id,start_offset,end_offset,note,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, ids[8+i], rid, e.EvidenceType, e.EventID, e.MemoryID, e.RevisionID, e.StartOffset, e.EndOffset, e.Note, identity.CallerID, now)
				if err != nil {
					return p, "", false, err
				}
			}
			if _, err = enqueueRelationIndexJob(ctx, tx, ids[5], operationID, r.processingVersion(), r.maxAttempts, now); err != nil {
				return p, "", false, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE relation_index_metadata SET state='stale',generation=generation+1,updated_at=? WHERE singleton=1`, now); err != nil {
				return p, "", false, err
			}
		}
		toState = "confirmed"
	}
	res, err := tx.ExecContext(ctx, `UPDATE relation_proposals SET review_state=?,result_relation_id=?,reviewed_at=? WHERE proposal_id=? AND review_state='pending'`, toState, relationID, now, id)
	if err != nil {
		return p, "", false, err
	}
	if !oneRow(res) {
		return p, "", false, ErrRelationProposalReviewed
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO relation_proposal_reviews(review_id,proposal_id,operation_id,from_state,to_state,reason_code,note,reviewer_id,relation_id,created_at) VALUES (?,?,?,'pending',?,?,?,?,?,?)`, reviewID, id, operationID, toState, review.ReasonCode, review.Note, identity.CallerID, relationID, now)
	if err != nil {
		return p, "", false, err
	}
	p, err = getRelationProposal(ctx, tx, id)
	if err != nil {
		return p, "", false, err
	}
	body, _ := json.Marshal(p)
	if err = writeIdempotency(ctx, tx, identity, operationID, status, body, now); err != nil {
		return p, "", false, err
	}
	action := "reject_relation_proposal"
	if confirm {
		action = "confirm_relation_proposal"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,reason,created_at) VALUES (?,?,?,?,?,'relation_proposal',?,'succeeded',?,?)`, auditID, operationID, identity.RequestID, identity.CallerID, action, id, review.ReasonCode, now)
	if err != nil {
		return p, "", false, err
	}
	if err = tx.Commit(); err != nil {
		return p, "", false, err
	}
	return p, operationID, false, nil
}

func enqueueRelationIndexJob(ctx context.Context, tx *sql.Tx, jobID, operationID, version string, maxAttempts int, now string) (bool, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,created_at) VALUES (?,?,'rebuild_relation_index','index',?,?,'pending',?,?,?) ON CONFLICT DO NOTHING`, jobID, operationID, relationIndexName, version, maxAttempts, now, now)
	return oneRow(res), err
}

func (r *Repository) GetRelation(ctx context.Context, id string) (domain.MemoryRelation, error) {
	return getRelation(ctx, r.store.reader, id)
}
func (r *Repository) GetRelationInSpace(ctx context.Context, id, space string) (domain.MemoryRelation, error) {
	v, err := getRelation(ctx, r.store.reader, id)
	if err == nil && v.SpaceID != space {
		return domain.MemoryRelation{}, ErrNotFound
	}
	return v, err
}
func getRelation(ctx context.Context, q candidateQueryer, id string) (domain.MemoryRelation, error) {
	var v domain.MemoryRelation
	err := q.QueryRowContext(ctx, `SELECT relation_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,state,source,confidence,specification_hash,created_by,processing_version,created_at,updated_at FROM memory_relations WHERE relation_id=?`, id).Scan(&v.RelationID, &v.SpaceID, &v.RelationType, &v.SourceMemoryID, &v.SourceRevisionID, &v.TargetMemoryID, &v.TargetRevisionID, &v.State, &v.Source, &v.Confidence, &v.SpecificationHash, &v.CreatedBy, &v.ProcessingVersion, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.Evidence = []domain.RelationEvidence{}
	rows, err := q.QueryContext(ctx, `SELECT evidence_id,evidence_type,event_id,memory_id,revision_id,start_offset,end_offset,note,created_by,created_at FROM relation_evidence WHERE relation_id=? ORDER BY evidence_id`, id)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var e domain.RelationEvidence
		if err = rows.Scan(&e.EvidenceID, &e.EvidenceType, &e.EventID, &e.MemoryID, &e.RevisionID, &e.StartOffset, &e.EndOffset, &e.Note, &e.CreatedBy, &e.CreatedAt); err != nil {
			rows.Close()
			return v, err
		}
		v.Evidence = append(v.Evidence, e)
	}
	rows.Close()
	v.StateEvents = []domain.RelationStateEvent{}
	rows, err = q.QueryContext(ctx, `SELECT state_event_id,operation_id,from_state,to_state,reason,created_by,created_at FROM relation_state_events WHERE relation_id=? ORDER BY created_at DESC,state_event_id DESC`, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var e domain.RelationStateEvent
		if err = rows.Scan(&e.StateEventID, &e.OperationID, &e.FromState, &e.ToState, &e.Reason, &e.CreatedBy, &e.CreatedAt); err != nil {
			return v, err
		}
		v.StateEvents = append(v.StateEvents, e)
	}
	return v, rows.Err()
}

func (r *Repository) ListRelations(ctx context.Context, f RelationFilter) (domain.RelationPage, error) {
	query, args := `SELECT r.relation_id FROM memory_relations r JOIN memories sm ON sm.memory_id=r.source_memory_id AND sm.current_revision_id=r.source_revision_id JOIN memories tm ON tm.memory_id=r.target_memory_id AND tm.current_revision_id=r.target_revision_id WHERE r.space_id=?`, []any{f.SpaceID}
	if f.State != "" {
		query += ` AND r.state=?`
		args = append(args, f.State)
	}
	if f.RelationType != "" {
		query += ` AND r.relation_type=?`
		args = append(args, f.RelationType)
	}
	if f.MemoryID != "" {
		switch f.Direction {
		case "outgoing":
			query += ` AND r.source_memory_id=?`
			args = append(args, f.MemoryID)
		case "incoming":
			query += ` AND r.target_memory_id=?`
			args = append(args, f.MemoryID)
		default:
			query += ` AND (r.source_memory_id=? OR r.target_memory_id=?)`
			args = append(args, f.MemoryID, f.MemoryID)
		}
		if f.RevisionID != "" {
			query += ` AND ((r.source_memory_id=? AND r.source_revision_id=?) OR (r.target_memory_id=? AND r.target_revision_id=?))`
			args = append(args, f.MemoryID, f.RevisionID, f.MemoryID, f.RevisionID)
		}
	}
	if f.Cursor != "" {
		query += ` AND r.relation_id<?`
		args = append(args, f.Cursor)
	}
	query += ` ORDER BY r.relation_id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.RelationPage{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return domain.RelationPage{}, err
	}
	state, _ := r.RelationIndexHealth(ctx)
	page := domain.RelationPage{Items: []domain.MemoryRelation{}, IndexState: state, Degraded: state != "ready"}
	for _, id := range ids {
		v, e := r.GetRelation(ctx, id)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, v)
	}
	if len(page.Items) > f.Limit {
		next := page.Items[f.Limit-1].RelationID
		page.NextCursor = &next
		page.Items = page.Items[:f.Limit]
	}
	return page, nil
}

func (r *Repository) RelationIndexHealth(ctx context.Context) (string, int64) {
	var state string
	var version int64
	if r.store.reader.QueryRowContext(ctx, `SELECT state,active_version FROM relation_index_metadata WHERE singleton=1`).Scan(&state, &version) != nil {
		return "unavailable", 0
	}
	return state, version
}
func (r *Repository) RebuildRelationIndexClaimed(ctx context.Context, claim ClaimedJob) error {
	return r.rebuildRelationIndex(ctx, &claim)
}

func (r *Repository) RebuildRelationIndex(ctx context.Context) error {
	return r.rebuildRelationIndex(ctx, nil)
}

func (r *Repository) rebuildRelationIndex(ctx context.Context, claim *ClaimedJob) error {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if claim != nil {
		if err = r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}
	now := formatTime(r.runtime.Now())
	if _, err = tx.ExecContext(ctx, `UPDATE relation_index_metadata SET state='rebuilding',updated_at=? WHERE singleton=1`, now); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_relations`).Scan(&count); err != nil {
		return err
	}
	if claim != nil {
		if err = r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE relation_index_metadata SET active_version=active_version+1,generation=generation+1,state='ready',source_relation_count=?,updated_at=? WHERE singleton=1`, count, now); err != nil {
		return err
	}
	return tx.Commit()
}
