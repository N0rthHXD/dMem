package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/N0rthHXD/dMem/internal/domain"
)

type createFactProposalParams struct {
	EventID, SpaceID, SubjectEntityID, Predicate     string
	ObjectEntityID, ObjectLiteral, ObjectLiteralType *string
	SourceMemoryID, SourceRevisionID                 string
	Confidence                                       *float64
	ValidFrom, ValidUntil                            *string
	Evidence                                         []domain.EntityFactEvidence
	ProcessorIdentity                                string
	ProviderIdentity, ModelIdentity                  *string
	ProcessingVersion                                string
}

func factProposalSpecHash(p createFactProposalParams) string {
	value := struct {
		Subject, Predicate, ObjectEntity, ObjectLiteral, ObjectLiteralType string
		SourceMemory, SourceRevision                                       string
		Confidence                                                         *float64
		ValidFrom, ValidUntil                                              *string
	}{p.SubjectEntityID, p.Predicate, deref(p.ObjectEntityID), deref(p.ObjectLiteral), deref(p.ObjectLiteralType), p.SourceMemoryID, p.SourceRevisionID, p.Confidence, p.ValidFrom, p.ValidUntil}
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validFactPredicate(ctx context.Context, q candidateQueryer, predicate string) bool {
	var n int
	return q.QueryRowContext(ctx, `SELECT 1 FROM predicates WHERE predicate=?`, predicate).Scan(&n) == nil
}

func validLiteralType(value string) bool {
	return value == "string" || value == "number" || value == "boolean"
}

func (r *Repository) insertFactProposal(ctx context.Context, tx *sql.Tx, p createFactProposalParams, now string) (string, error) {
	hash := factProposalSpecHash(p)
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT proposal_id FROM entity_fact_proposals WHERE event_id=? AND processing_version=? AND specification_hash=?`, p.EventID, p.ProcessingVersion, hash).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id, err := r.runtime.ID()
	if err != nil {
		return "", err
	}
	evidence, _ := json.Marshal(p.Evidence)
	if _, err := tx.ExecContext(ctx, `INSERT INTO entity_fact_proposals(proposal_id, event_id, space_id, subject_entity_id, predicate, object_entity_id, object_literal, object_literal_type, source_memory_id, source_revision_id, confidence, valid_from, valid_until, evidence_json, processor_identity, provider_identity, model_identity, processing_version, specification_hash, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, p.EventID, p.SpaceID, p.SubjectEntityID, p.Predicate, p.ObjectEntityID, p.ObjectLiteral, p.ObjectLiteralType, p.SourceMemoryID, p.SourceRevisionID, p.Confidence, p.ValidFrom, p.ValidUntil, string(evidence), p.ProcessorIdentity, p.ProviderIdentity, p.ModelIdentity, p.ProcessingVersion, hash, now); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidFactProposal, err)
	}
	return id, nil
}

// confirmFactProposalTx confirms a fact proposal atomically. It reuses an
// existing identical fact if one exists and supersedes older active facts with
// the same subject and predicate.
func (r *Repository) confirmFactProposalTx(ctx context.Context, tx *sql.Tx, proposalID, reviewerID, reasonCode, autoReviewVersion, now string) error {
	p, err := getFactProposal(ctx, tx, proposalID)
	if err != nil {
		return err
	}
	if p.ReviewState != "pending" {
		return ErrFactProposalReviewed
	}
	params := createFactProposalParams{EventID: p.EventID, SpaceID: p.SpaceID, SubjectEntityID: p.SubjectEntityID, Predicate: p.Predicate, ObjectEntityID: p.ObjectEntityID, ObjectLiteral: p.ObjectLiteral, ObjectLiteralType: p.ObjectLiteralType, SourceMemoryID: p.SourceMemoryID, SourceRevisionID: p.SourceRevisionID, Confidence: p.Confidence, ValidFrom: p.ValidFrom, ValidUntil: p.ValidUntil, Evidence: p.Evidence, ProcessorIdentity: p.ProcessorIdentity, ProcessingVersion: p.ProcessingVersion}
	if err := validateFactProposalContext(ctx, tx, params, true); err != nil {
		return err
	}
	var factID string
	err = tx.QueryRowContext(ctx, `SELECT fact_id FROM entity_facts WHERE subject_entity_id=? AND predicate=? AND object_entity_id IS ? AND object_literal IS ? AND source_revision_id=? AND specification_hash=?`, p.SubjectEntityID, p.Predicate, p.ObjectEntityID, p.ObjectLiteral, p.SourceRevisionID, p.SpecificationHash).Scan(&factID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		ids, err := r.newIDs(2 + len(p.Evidence))
		if err != nil {
			return err
		}
		factID = ids[0]
		if _, err := tx.ExecContext(ctx, `INSERT INTO entity_facts(fact_id, space_id, subject_entity_id, predicate, object_entity_id, object_literal, object_literal_type, source_memory_id, source_revision_id, confidence, state, source, specification_hash, created_by, processing_version, valid_from, valid_until, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?, 'active', 'auto_review', ?,?,?,?,?,?,?)`, factID, p.SpaceID, p.SubjectEntityID, p.Predicate, p.ObjectEntityID, p.ObjectLiteral, p.ObjectLiteralType, p.SourceMemoryID, p.SourceRevisionID, p.Confidence, p.SpecificationHash, reviewerID, p.ProcessingVersion, p.ValidFrom, p.ValidUntil, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entity_fact_state_events(state_event_id, fact_id, operation_id, from_state, to_state, reason, created_by, created_at) VALUES (?,?,?,NULL,'active',?,?,?)`, ids[1], factID, p.EventID, reasonCode, reviewerID, now); err != nil {
			return err
		}
		for i, evidence := range p.Evidence {
			if _, err := tx.ExecContext(ctx, `INSERT INTO entity_fact_evidence(evidence_id, fact_id, evidence_type, event_id, memory_id, revision_id, start_offset, end_offset, note, created_by, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, ids[2+i], factID, evidence.EvidenceType, evidence.EventID, evidence.MemoryID, evidence.RevisionID, evidence.StartOffset, evidence.EndOffset, evidence.Note, reviewerID, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entity_facts SET state='superseded', updated_at=? WHERE space_id=? AND subject_entity_id=? AND predicate=? AND state='active' AND fact_id<>?`, now, p.SpaceID, p.SubjectEntityID, p.Predicate, factID); err != nil {
			return err
		}
		if err := r.markGraphIndexStale(ctx, tx, now); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE entity_fact_proposals SET review_state='confirmed', result_fact_id=?, reviewed_at=?, auto_review_version=? WHERE proposal_id=? AND review_state='pending'`, factID, now, nullableString(autoReviewVersion), proposalID)
	if err != nil {
		return err
	}
	if !oneRow(res) {
		return ErrFactProposalReviewed
	}
	reviewID, err := r.runtime.ID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO entity_fact_proposal_reviews(review_id, proposal_id, operation_id, from_state, to_state, reason_code, reviewer_id, fact_id, created_at) VALUES (?,?,?,'pending','confirmed',?,?,?,?)`, reviewID, proposalID, p.EventID, reasonCode, reviewerID, factID, now); err != nil {
		return err
	}
	return nil
}

func validateFactProposalContext(ctx context.Context, tx *sql.Tx, p createFactProposalParams, current bool) error {
	if !validFactPredicate(ctx, tx, p.Predicate) {
		return ErrInvalidFactProposal
	}
	if !entityInSpace(ctx, tx, p.SubjectEntityID, p.SpaceID) {
		return ErrInvalidFactProposal
	}
	if p.ObjectEntityID != nil {
		if !entityInSpace(ctx, tx, *p.ObjectEntityID, p.SpaceID) {
			return ErrInvalidFactProposal
		}
		if *p.ObjectEntityID == p.SubjectEntityID {
			return ErrInvalidFactProposal
		}
	}
	if p.ObjectLiteral != nil {
		if p.ObjectLiteralType == nil || !validLiteralType(*p.ObjectLiteralType) || *p.ObjectLiteral == "" {
			return ErrInvalidFactProposal
		}
	}
	var currentRevision string
	if err := tx.QueryRowContext(ctx, `SELECT current_revision_id FROM memories WHERE memory_id=?`, p.SourceMemoryID).Scan(&currentRevision); err != nil {
		return ErrInvalidFactProposal
	}
	if current && currentRevision != p.SourceRevisionID {
		return ErrRevisionConflict
	}
	for _, evidence := range p.Evidence {
		if err := validateEntityFactEvidence(ctx, tx, p.SpaceID, evidence); err != nil {
			return err
		}
	}
	return nil
}

func validateEntityFactEvidence(ctx context.Context, tx *sql.Tx, space string, e domain.EntityFactEvidence) error {
	if e.EvidenceType == "human_confirmation" {
		if e.EventID != nil || e.MemoryID != nil || e.RevisionID != nil || e.StartOffset != nil || e.EndOffset != nil {
			return ErrInvalidFactProposal
		}
		return nil
	}
	if e.StartOffset == nil || e.EndOffset == nil || *e.StartOffset < 0 || *e.EndOffset <= *e.StartOffset {
		return ErrInvalidFactProposal
	}
	var text, sourceSpace string
	switch e.EvidenceType {
	case "raw_event_range":
		if e.EventID == nil || e.MemoryID != nil || e.RevisionID != nil {
			return ErrInvalidFactProposal
		}
		if err := tx.QueryRowContext(ctx, `SELECT c.content_text, e.space_id FROM raw_events e JOIN raw_contents c ON c.content_id=e.content_id WHERE e.event_id=? AND c.content_kind IN ('text','json')`, *e.EventID).Scan(&text, &sourceSpace); err != nil {
			return ErrInvalidFactProposal
		}
	case "revision_range":
		if e.EventID != nil || e.MemoryID == nil || e.RevisionID == nil {
			return ErrInvalidFactProposal
		}
		if err := tx.QueryRowContext(ctx, `SELECT r.display_content, m.space_id FROM memory_revisions r JOIN memories m ON m.memory_id=r.memory_id WHERE r.memory_id=? AND r.revision_id=?`, *e.MemoryID, *e.RevisionID).Scan(&text, &sourceSpace); err != nil {
			return ErrInvalidFactProposal
		}
	default:
		return ErrInvalidFactProposal
	}
	if sourceSpace != space || *e.EndOffset > len([]rune(text)) {
		return ErrInvalidFactProposal
	}
	return nil
}

// ---- internal relation proposal helpers used by graph extraction ----

func (r *Repository) insertRelationProposal(ctx context.Context, tx *sql.Tx, p CreateRelationProposalParams, now string) (string, error) {
	hash := relationSpecificationHash(p)
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT proposal_id FROM relation_proposals WHERE event_id=? AND processing_version=? AND relation_type=? AND source_memory_id=? AND source_revision_id=? AND target_memory_id=? AND target_revision_id=? AND specification_hash=?`, p.EventID, p.ProcessingVersion, p.RelationType, p.SourceMemoryID, p.SourceRevisionID, p.TargetMemoryID, p.TargetRevisionID, hash).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id, err := r.runtime.ID()
	if err != nil {
		return "", err
	}
	evidence, _ := json.Marshal(p.Evidence)
	if _, err := tx.ExecContext(ctx, `INSERT INTO relation_proposals(proposal_id, event_id, space_id, relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, confidence, evidence_json, processor_identity, provider_identity, model_identity, processing_version, specification_hash, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, p.EventID, p.SpaceID, p.RelationType, p.SourceMemoryID, p.SourceRevisionID, p.TargetMemoryID, p.TargetRevisionID, p.Confidence, string(evidence), p.ProcessorIdentity, p.ProviderIdentity, p.ModelIdentity, p.ProcessingVersion, hash, now); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRelationProposal, err)
	}
	return id, nil
}

func (r *Repository) confirmRelationProposalTx(ctx context.Context, tx *sql.Tx, proposalID, reviewerID, reasonCode, now string) error {
	p, err := getRelationProposal(ctx, tx, proposalID)
	if err != nil {
		return err
	}
	if p.ReviewState != "pending" {
		return ErrRelationProposalReviewed
	}
	params := CreateRelationProposalParams{EventID: p.EventID, SpaceID: p.SpaceID, RelationType: p.RelationType, SourceMemoryID: p.SourceMemoryID, SourceRevisionID: p.SourceRevisionID, TargetMemoryID: p.TargetMemoryID, TargetRevisionID: p.TargetRevisionID, Confidence: p.Confidence, Evidence: p.Evidence, ProcessorIdentity: p.ProcessorIdentity, ProcessingVersion: p.ProcessingVersion}
	if err := validateRelationContext(ctx, tx, params, true); err != nil {
		return err
	}
	var relationID string
	err = tx.QueryRowContext(ctx, `SELECT relation_id FROM memory_relations WHERE relation_type=? AND source_memory_id=? AND source_revision_id=? AND target_memory_id=? AND target_revision_id=? AND specification_hash=?`, p.RelationType, p.SourceMemoryID, p.SourceRevisionID, p.TargetMemoryID, p.TargetRevisionID, p.SpecificationHash).Scan(&relationID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		ids, err := r.newIDs(3 + len(p.Evidence))
		if err != nil {
			return err
		}
		relationID = ids[0]
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_relations(relation_id, space_id, relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, state, source, confidence, specification_hash, created_by, processing_version, created_at, updated_at) VALUES (?,?,?,?,?,?,?,'active','processor_review',?,?,?,?,?,?)`, relationID, p.SpaceID, p.RelationType, p.SourceMemoryID, p.SourceRevisionID, p.TargetMemoryID, p.TargetRevisionID, p.Confidence, p.SpecificationHash, reviewerID, p.ProcessingVersion, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO relation_state_events(state_event_id, relation_id, operation_id, from_state, to_state, reason, created_by, created_at) VALUES (?,?,?,NULL,'active',?,?,?)`, ids[1], relationID, p.EventID, reasonCode, reviewerID, now); err != nil {
			return err
		}
		for i, e := range p.Evidence {
			if _, err := tx.ExecContext(ctx, `INSERT INTO relation_evidence(evidence_id, relation_id, evidence_type, event_id, memory_id, revision_id, start_offset, end_offset, note, created_by, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, ids[2+i], relationID, e.EvidenceType, e.EventID, e.MemoryID, e.RevisionID, e.StartOffset, e.EndOffset, e.Note, reviewerID, now); err != nil {
				return err
			}
		}
		if err := r.markRelationIndexStale(ctx, tx, now); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE relation_proposals SET review_state='confirmed', result_relation_id=?, reviewed_at=? WHERE proposal_id=? AND review_state='pending'`, relationID, now, proposalID)
	if err != nil {
		return err
	}
	if !oneRow(res) {
		return ErrRelationProposalReviewed
	}
	reviewID, err := r.runtime.ID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO relation_proposal_reviews(review_id, proposal_id, operation_id, from_state, to_state, reason_code, reviewer_id, relation_id, created_at) VALUES (?,?,?,'pending','confirmed',?,?,?,?)`, reviewID, proposalID, p.EventID, reasonCode, reviewerID, relationID, now); err != nil {
		return err
	}
	return nil
}

// ---- public entity fact API ----

type FactFilter struct {
	SpaceID, SubjectEntityID, ObjectEntityID, Predicate, State, Direction, ReferenceTime, Cursor string
	Limit                                                                                        int
}

func (r *Repository) GetEntityFact(ctx context.Context, id string) (domain.EntityFact, error) {
	return getEntityFact(ctx, r.store.reader, id)
}

func getEntityFact(ctx context.Context, q candidateQueryer, id string) (domain.EntityFact, error) {
	var f domain.EntityFact
	err := q.QueryRowContext(ctx, `SELECT fact_id, space_id, subject_entity_id, predicate, object_entity_id, object_literal, object_literal_type, source_memory_id, source_revision_id, confidence, state, source, specification_hash, created_by, processing_version, valid_from, valid_until, created_at, updated_at FROM entity_facts WHERE fact_id=?`, id).Scan(&f.FactID, &f.SpaceID, &f.SubjectEntityID, &f.Predicate, &f.ObjectEntityID, &f.ObjectLiteral, &f.ObjectLiteralType, &f.SourceMemoryID, &f.SourceRevisionID, &f.Confidence, &f.State, &f.Source, &f.SpecificationHash, &f.CreatedBy, &f.ProcessingVersion, &f.ValidFrom, &f.ValidUntil, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	if err != nil {
		return f, err
	}
	f.Evidence = []domain.EntityFactEvidence{}
	rows, err := q.QueryContext(ctx, `SELECT evidence_id, evidence_type, event_id, memory_id, revision_id, start_offset, end_offset, note, created_by, created_at FROM entity_fact_evidence WHERE fact_id=? ORDER BY evidence_id`, id)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var e domain.EntityFactEvidence
		if err := rows.Scan(&e.EvidenceID, &e.EvidenceType, &e.EventID, &e.MemoryID, &e.RevisionID, &e.StartOffset, &e.EndOffset, &e.Note, &e.CreatedBy, &e.CreatedAt); err != nil {
			rows.Close()
			return f, err
		}
		f.Evidence = append(f.Evidence, e)
	}
	rows.Close()
	f.StateEvents = []domain.EntityFactStateEvent{}
	rows, err = q.QueryContext(ctx, `SELECT state_event_id, operation_id, from_state, to_state, reason, created_by, created_at FROM entity_fact_state_events WHERE fact_id=? ORDER BY created_at DESC, state_event_id DESC`, id)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	for rows.Next() {
		var e domain.EntityFactStateEvent
		if err := rows.Scan(&e.StateEventID, &e.OperationID, &e.FromState, &e.ToState, &e.Reason, &e.CreatedBy, &e.CreatedAt); err != nil {
			return f, err
		}
		f.StateEvents = append(f.StateEvents, e)
	}
	return f, rows.Err()
}

func (r *Repository) ListEntityFacts(ctx context.Context, f FactFilter) (domain.EntityFactPage, error) {
	query, args := `SELECT f.fact_id FROM entity_facts f JOIN entities se ON se.entity_id=f.subject_entity_id WHERE f.space_id=?`, []any{f.SpaceID}
	if f.State != "" {
		query += ` AND f.state=?`
		args = append(args, f.State)
	}
	if f.Predicate != "" {
		query += ` AND f.predicate=?`
		args = append(args, f.Predicate)
	}
	if f.SubjectEntityID != "" {
		query += ` AND f.subject_entity_id=?`
		args = append(args, f.SubjectEntityID)
	}
	if f.ObjectEntityID != "" {
		query += ` AND f.object_entity_id=?`
		args = append(args, f.ObjectEntityID)
	}
	if f.ReferenceTime != "" {
		query += ` AND (f.valid_from IS NULL OR f.valid_from<=?) AND (f.valid_until IS NULL OR f.valid_until>?)`
		args = append(args, f.ReferenceTime, f.ReferenceTime)
	}
	if f.Cursor != "" {
		query += ` AND f.fact_id<?`
		args = append(args, f.Cursor)
	}
	query += ` ORDER BY f.fact_id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.EntityFactPage{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	state, _ := r.GraphIndexHealth(ctx)
	page := domain.EntityFactPage{Items: []domain.EntityFact{}, IndexState: state, Degraded: state != "ready"}
	for _, id := range ids {
		v, err := getEntityFact(ctx, r.store.reader, id)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, v)
	}
	if len(page.Items) > f.Limit {
		next := page.Items[f.Limit-1].FactID
		page.NextCursor = &next
		page.Items = page.Items[:f.Limit]
	}
	return page, nil
}

type CreateFactProposalParams createFactProposalParams

func (r *Repository) CreateFactProposal(ctx context.Context, identity WriteIdentity, p CreateFactProposalParams) (domain.FactProposal, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.FactProposal{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.FactProposal{}, false, err
	} else if found {
		var result domain.FactProposal
		if status != 201 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.FactProposal{}, false, err
	}
	params := createFactProposalParams(p)
	if err := validateFactProposalContext(ctx, tx, params, false); err != nil {
		return domain.FactProposal{}, false, err
	}
	now := formatTime(r.runtime.Now())
	proposalID, err := r.insertFactProposal(ctx, tx, params, now)
	if err != nil {
		return domain.FactProposal{}, false, err
	}
	result, err := getFactProposal(ctx, tx, proposalID)
	if err != nil {
		return domain.FactProposal{}, false, err
	}
	body, _ := json.Marshal(result)
	ids, err := r.newIDs(2)
	if err != nil {
		return domain.FactProposal{}, false, err
	}
	if err := writeIdempotency(ctx, tx, identity, ids[0], 201, body, now); err != nil {
		return domain.FactProposal{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, created_at) VALUES (?,?,?,?,'create_fact_proposal','fact_proposal',?,'succeeded',?)`, ids[1], ids[0], identity.RequestID, identity.CallerID, proposalID, now); err != nil {
		return domain.FactProposal{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.FactProposal{}, false, err
	}
	return result, false, nil
}

func (r *Repository) GetFactProposal(ctx context.Context, id string) (domain.FactProposal, error) {
	return getFactProposal(ctx, r.store.reader, id)
}

func getFactProposal(ctx context.Context, q candidateQueryer, id string) (domain.FactProposal, error) {
	var p domain.FactProposal
	var evidence string
	err := q.QueryRowContext(ctx, `SELECT proposal_id, event_id, space_id, subject_entity_id, predicate, object_entity_id, object_literal, object_literal_type, source_memory_id, source_revision_id, confidence, valid_from, valid_until, evidence_json, processor_identity, provider_identity, model_identity, processing_version, specification_hash, auto_review_version, review_state, result_fact_id, created_at, reviewed_at FROM entity_fact_proposals WHERE proposal_id=?`, id).Scan(&p.ProposalID, &p.EventID, &p.SpaceID, &p.SubjectEntityID, &p.Predicate, &p.ObjectEntityID, &p.ObjectLiteral, &p.ObjectLiteralType, &p.SourceMemoryID, &p.SourceRevisionID, &p.Confidence, &p.ValidFrom, &p.ValidUntil, &evidence, &p.ProcessorIdentity, &p.ProviderIdentity, &p.ModelIdentity, &p.ProcessingVersion, &p.SpecificationHash, &p.AutoReviewVersion, &p.ReviewState, &p.ResultFactID, &p.CreatedAt, &p.ReviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Evidence, p.Reviews = []domain.EntityFactEvidence{}, []domain.FactProposalReview{}
	if err = json.Unmarshal([]byte(evidence), &p.Evidence); err != nil {
		return p, err
	}
	rows, err := q.QueryContext(ctx, `SELECT review_id, from_state, to_state, reason_code, note, reviewer_id, fact_id, created_at FROM entity_fact_proposal_reviews WHERE proposal_id=? ORDER BY created_at DESC, review_id DESC`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var v domain.FactProposalReview
		if err := rows.Scan(&v.ReviewID, &v.FromState, &v.ToState, &v.ReasonCode, &v.Note, &v.ReviewerID, &v.FactID, &v.CreatedAt); err != nil {
			return p, err
		}
		p.Reviews = append(p.Reviews, v)
	}
	return p, rows.Err()
}

type FactProposalFilter struct {
	SpaceID, ReviewState, Predicate, Cursor string
	Limit                                   int
}

func (r *Repository) ListFactProposals(ctx context.Context, f FactProposalFilter) (domain.FactProposalPage, error) {
	query, args := `SELECT proposal_id FROM entity_fact_proposals WHERE space_id=?`, []any{f.SpaceID}
	if f.ReviewState != "" {
		query += ` AND review_state=?`
		args = append(args, f.ReviewState)
	}
	if f.Predicate != "" {
		query += ` AND predicate=?`
		args = append(args, f.Predicate)
	}
	if f.Cursor != "" {
		query += ` AND proposal_id<?`
		args = append(args, f.Cursor)
	}
	query += ` ORDER BY proposal_id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.FactProposalPage{}, err
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
	page := domain.FactProposalPage{Items: []domain.FactProposal{}}
	for _, id := range ids {
		p, e := getFactProposal(ctx, r.store.reader, id)
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

type ReviewFactProposalParams struct {
	ReasonCode string
	Note       *string
}

func (r *Repository) ConfirmFactProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewFactProposalParams) (domain.FactProposal, bool, error) {
	return r.reviewFactProposal(ctx, identity, id, review, true)
}
func (r *Repository) RejectFactProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewFactProposalParams) (domain.FactProposal, bool, error) {
	return r.reviewFactProposal(ctx, identity, id, review, false)
}

func (r *Repository) reviewFactProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewFactProposalParams, confirm bool) (domain.FactProposal, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.FactProposal{}, false, err
	}
	defer tx.Rollback()
	status := 200
	if confirm {
		status = 201
	}
	if s, b, found, e := readIdempotency(ctx, tx, identity); e != nil {
		return domain.FactProposal{}, false, e
	} else if found {
		var p domain.FactProposal
		if s != status || json.Unmarshal([]byte(b), &p) != nil {
			return p, false, errors.New("invalid stored idempotency response")
		}
		return p, true, nil
	}
	p, err := getFactProposal(ctx, tx, id)
	if err != nil {
		return p, false, err
	}
	if p.ReviewState != "pending" {
		return p, false, ErrFactProposalReviewed
	}
	if err = r.requireDiskWritable(); err != nil {
		return p, false, err
	}
	now := formatTime(r.runtime.Now())
	if confirm {
		if err = r.confirmFactProposalTx(ctx, tx, id, identity.CallerID, review.ReasonCode, "", now); err != nil {
			return p, false, err
		}
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE entity_fact_proposals SET review_state='rejected', reviewed_at=? WHERE proposal_id=? AND review_state='pending'`, now, id)
		if err != nil {
			return p, false, err
		}
		if !oneRow(res) {
			return p, false, ErrFactProposalReviewed
		}
		reviewID, err := r.runtime.ID()
		if err != nil {
			return p, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entity_fact_proposal_reviews(review_id, proposal_id, operation_id, from_state, to_state, reason_code, note, reviewer_id, created_at) VALUES (?,?,?,'pending','rejected',?,?,?,?)`, reviewID, id, p.EventID, review.ReasonCode, review.Note, identity.CallerID, now); err != nil {
			return p, false, err
		}
	}
	p, err = getFactProposal(ctx, tx, id)
	if err != nil {
		return p, false, err
	}
	body, _ := json.Marshal(p)
	ids, err := r.newIDs(2)
	if err != nil {
		return p, false, err
	}
	if err = writeIdempotency(ctx, tx, identity, ids[0], status, body, now); err != nil {
		return p, false, err
	}
	action := "reject_fact_proposal"
	if confirm {
		action = "confirm_fact_proposal"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, reason, created_at) VALUES (?,?,?,?,?,'fact_proposal',?,'succeeded',?,?)`, ids[1], ids[0], identity.RequestID, identity.CallerID, action, id, review.ReasonCode, now); err != nil {
		return p, false, err
	}
	if err = tx.Commit(); err != nil {
		return p, false, err
	}
	return p, false, nil
}

// ---- graph index ----

func (r *Repository) GraphIndexHealth(ctx context.Context) (string, int64) {
	var state string
	var version int64
	if r.store.reader.QueryRowContext(ctx, `SELECT state, active_version FROM graph_index_metadata WHERE singleton=1`).Scan(&state, &version) != nil {
		return "unavailable", 0
	}
	return state, version
}

func (r *Repository) CreateGraphIndexRebuildJob(ctx context.Context, identity WriteIdentity) (domain.JobReference, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.JobReference{}, false, err
	}
	defer tx.Rollback()
	if s, b, found, e := readIdempotency(ctx, tx, identity); e != nil {
		return domain.JobReference{}, false, e
	} else if found {
		var v domain.JobReference
		if s != 202 || json.Unmarshal([]byte(b), &v) != nil {
			return v, false, errors.New("invalid stored idempotency response")
		}
		return v, true, nil
	}
	var active string
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM jobs WHERE job_type='rebuild_graph_index' AND target_type='index' AND target_id=? AND status IN ('pending','waiting','running','retrying')`, graphIndexName).Scan(&active)
	if err == nil {
		return domain.JobReference{JobID: active}, false, ErrJobAlreadyActive
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.JobReference{}, false, err
	}
	if err = r.requireDiskWritable(); err != nil {
		return domain.JobReference{}, false, err
	}
	ids, err := r.newIDs(3)
	if err != nil {
		return domain.JobReference{}, false, err
	}
	now := formatTime(r.runtime.Now())
	if _, err = enqueueGraphIndexJob(ctx, tx, ids[0], ids[1], r.processingVersion, r.maxAttempts, now); err != nil {
		return domain.JobReference{}, false, err
	}
	v := domain.JobReference{JobID: ids[0], OperationID: ids[1]}
	body, _ := json.Marshal(v)
	if err = writeIdempotency(ctx, tx, identity, ids[1], 202, body, now); err != nil {
		return v, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, created_at) VALUES (?,?,?,?,'rebuild_graph_index','index',?,'accepted',?)`, ids[2], ids[1], identity.RequestID, identity.CallerID, graphIndexName, now); err != nil {
		return v, false, err
	}
	if err = tx.Commit(); err != nil {
		return v, false, err
	}
	return v, false, nil
}

func enqueueGraphIndexJob(ctx context.Context, tx *sql.Tx, jobID, operationID, version string, maxAttempts int, now string) (bool, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES (?,?,'rebuild_graph_index','index',?,?,'pending',?,?,?) ON CONFLICT DO NOTHING`, jobID, operationID, graphIndexName, version, maxAttempts, now, now)
	return oneRow(res), err
}

func (r *Repository) RebuildGraphIndexClaimed(ctx context.Context, claim ClaimedJob) error {
	return r.rebuildGraphIndex(ctx, &claim)
}
func (r *Repository) RebuildGraphIndex(ctx context.Context) error {
	return r.rebuildGraphIndex(ctx, nil)
}

func (r *Repository) rebuildGraphIndex(ctx context.Context, claim *ClaimedJob) error {
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
	if _, err = tx.ExecContext(ctx, `UPDATE graph_index_metadata SET state='rebuilding', updated_at=? WHERE singleton=1`, now); err != nil {
		return err
	}
	var facts, entities, relations int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM entity_facts WHERE state='active'), (SELECT count(*) FROM entities WHERE state='active'), (SELECT count(*) FROM memory_relations WHERE state='active')`).Scan(&facts, &entities, &relations); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE graph_index_metadata SET active_version=active_version+1, generation=generation+1, state='ready', source_fact_count=?, source_entity_count=?, source_relation_count=?, updated_at=? WHERE singleton=1`, facts, entities, relations, now); err != nil {
		return err
	}
	return tx.Commit()
}
