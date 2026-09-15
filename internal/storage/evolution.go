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
	ErrInvalidEvolutionProposal = errors.New("invalid evolution proposal")
	ErrEvolutionReviewed        = errors.New("evolution proposal has already been reviewed")
	ErrUnsupportedAction        = errors.New("unsupported action")
	ErrConflictResolved         = errors.New("conflict case has already been resolved")
)

type CreateEvolutionProposalParams struct {
	EventID, SpaceID, Action               string
	ProposedContent, ValidFrom, ValidUntil *string
	MemoryType                             *string
	Sensitivity                            string
	RelationSpecs, MentionSpecs            json.RawMessage
	ProcessorIdentity, ProcessingVersion   string
	ProviderIdentity, ModelIdentity        *string
	Targets                                []domain.EvolutionTarget
	Evidence                               []domain.RelationEvidence
}

type EvolutionProposalFilter struct {
	SpaceID, ReviewState, Action, Cursor string
	Limit                                int
}

type ReviewEvolutionProposalParams struct {
	ExpectedTargets  []domain.EvolutionTarget
	ReplacementMode  *string
	SupersedeTargets bool
	ReasonCode       string
	Note             *string
}

func evolutionSpecificationHash(p CreateEvolutionProposalParams) string {
	v := struct {
		Action                         string
		Content, ValidFrom, ValidUntil *string
		MemoryType                     *string
		Sensitivity                    string
		RelationSpecs, MentionSpecs    json.RawMessage
		Targets                        []domain.EvolutionTarget
		Evidence                       []domain.RelationEvidence
	}{p.Action, p.ProposedContent, p.ValidFrom, p.ValidUntil, p.MemoryType, p.Sensitivity, p.RelationSpecs, p.MentionSpecs, p.Targets, p.Evidence}
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func normalizeSpecs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
}

func (r *Repository) CreateEvolutionProposal(ctx context.Context, identity WriteIdentity, p CreateEvolutionProposalParams) (domain.EvolutionProposal, bool, error) {
	return r.createEvolutionProposal(ctx, identity, nil, p)
}

func (r *Repository) CreateEvolutionProposalClaimed(ctx context.Context, claim ClaimedJob, p CreateEvolutionProposalParams) (domain.EvolutionProposal, bool, error) {
	return r.createEvolutionProposal(ctx, WriteIdentity{}, &claim, p)
}

func (r *Repository) createEvolutionProposal(ctx context.Context, identity WriteIdentity, claim *ClaimedJob, p CreateEvolutionProposalParams) (domain.EvolutionProposal, bool, error) {
	p.RelationSpecs, p.MentionSpecs = normalizeSpecs(p.RelationSpecs), normalizeSpecs(p.MentionSpecs)
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.EvolutionProposal{}, false, err
	}
	defer tx.Rollback()
	if claim != nil {
		if err = r.requireJobClaim(ctx, tx, *claim); err != nil {
			return domain.EvolutionProposal{}, false, err
		}
		var targetID, version string
		if err = tx.QueryRowContext(ctx, `SELECT target_id,processing_version FROM jobs WHERE job_id=?`, claim.Job.JobID).Scan(&targetID, &version); err != nil || targetID != p.EventID || version != p.ProcessingVersion {
			return domain.EvolutionProposal{}, false, ErrLeaseLost
		}
	} else if status, body, found, e := readIdempotency(ctx, tx, identity); e != nil {
		return domain.EvolutionProposal{}, false, e
	} else if found {
		var result domain.EvolutionProposal
		if status != 201 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err = r.requireDiskWritable(); err != nil {
		return domain.EvolutionProposal{}, false, err
	}
	if err = validateEvolutionProposal(ctx, tx, p); err != nil {
		return domain.EvolutionProposal{}, false, err
	}
	hash := evolutionSpecificationHash(p)
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT proposal_id FROM evolution_proposals WHERE event_id=? AND processing_version=? AND action=? AND specification_hash=?`, p.EventID, p.ProcessingVersion, p.Action, hash).Scan(&existing)
	if err == nil {
		result, e := getEvolutionProposal(ctx, tx, existing)
		return result, true, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.EvolutionProposal{}, false, err
	}
	proposalID, err := r.runtime.ID()
	if err != nil {
		return domain.EvolutionProposal{}, false, err
	}
	now := formatTime(r.runtime.Now())
	var contentHash *string
	if p.ProposedContent != nil {
		h := sha256.Sum256([]byte(*p.ProposedContent))
		value := hex.EncodeToString(h[:])
		contentHash = &value
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO evolution_proposals(proposal_id,event_id,space_id,action,proposed_content,proposed_content_hash,valid_from,valid_until,memory_type,sensitivity,relation_specs_json,mention_specs_json,processor_identity,provider_identity,model_identity,processing_version,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, proposalID, p.EventID, p.SpaceID, p.Action, p.ProposedContent, contentHash, p.ValidFrom, p.ValidUntil, p.MemoryType, p.Sensitivity, string(p.RelationSpecs), string(p.MentionSpecs), p.ProcessorIdentity, p.ProviderIdentity, p.ModelIdentity, p.ProcessingVersion, hash, now)
	if err != nil {
		return domain.EvolutionProposal{}, false, fmt.Errorf("%w: proposal", ErrInvalidEvolutionProposal)
	}
	for i, target := range p.Targets {
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_proposal_targets(proposal_id,target_order,memory_id,expected_revision_id,created_at) VALUES (?,?,?,?,?)`, proposalID, i, target.MemoryID, target.ExpectedRevisionID, now); err != nil {
			return domain.EvolutionProposal{}, false, ErrInvalidEvolutionProposal
		}
	}
	for _, evidence := range p.Evidence {
		id, e := r.runtime.ID()
		if e != nil {
			return domain.EvolutionProposal{}, false, e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_proposal_evidence(evidence_id,proposal_id,evidence_type,event_id,memory_id,revision_id,start_offset,end_offset,note,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, id, proposalID, evidence.EvidenceType, evidence.EventID, evidence.MemoryID, evidence.RevisionID, evidence.StartOffset, evidence.EndOffset, evidence.Note, now); err != nil {
			return domain.EvolutionProposal{}, false, ErrInvalidEvolutionProposal
		}
	}
	result, err := getEvolutionProposal(ctx, tx, proposalID)
	if err != nil {
		return result, false, err
	}
	if claim == nil {
		operationID, auditID, e := nextTwoIDs(r)
		if e != nil {
			return result, false, e
		}
		body, _ := json.Marshal(result)
		if err = writeIdempotency(ctx, tx, identity, operationID, 201, body, now); err != nil {
			return result, false, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,created_at) VALUES (?,?,?,?,'create_evolution_proposal','evolution_proposal',?,'succeeded',?)`, auditID, operationID, identity.RequestID, identity.CallerID, proposalID, now); err != nil {
			return result, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return result, false, err
	}
	return result, false, nil
}

func validateEvolutionProposal(ctx context.Context, tx *sql.Tx, p CreateEvolutionProposalParams) error {
	if !contains([]string{"duplicate", "supplement", "replacement", "conflict"}, p.Action) || p.EventID == "" || p.SpaceID == "" || p.ProcessorIdentity == "" || p.ProcessingVersion == "" || len(p.Targets) == 0 || p.Sensitivity != "normal" && p.Sensitivity != "sensitive" {
		return ErrInvalidEvolutionProposal
	}
	if p.Action == "duplicate" && p.ProposedContent != nil || p.Action != "duplicate" && (p.ProposedContent == nil || *p.ProposedContent == "") || p.Action == "conflict" && len(p.Targets) != 2 || (p.Action == "supplement" || p.Action == "replacement") && len(p.Targets) != 1 {
		return ErrInvalidEvolutionProposal
	}
	if p.ValidFrom != nil && p.ValidUntil != nil && *p.ValidUntil <= *p.ValidFrom || !json.Valid(p.RelationSpecs) || !json.Valid(p.MentionSpecs) {
		return ErrInvalidEvolutionProposal
	}
	var eventSpace string
	if tx.QueryRowContext(ctx, `SELECT space_id FROM raw_events WHERE event_id=?`, p.EventID).Scan(&eventSpace) != nil || eventSpace != p.SpaceID {
		return ErrInvalidEvolutionProposal
	}
	seen := map[string]bool{}
	for _, target := range p.Targets {
		if seen[target.MemoryID] {
			return ErrInvalidEvolutionProposal
		}
		seen[target.MemoryID] = true
		var space string
		if tx.QueryRowContext(ctx, `SELECT m.space_id FROM memories m JOIN memory_revisions r ON r.memory_id=m.memory_id WHERE m.memory_id=? AND r.revision_id=?`, target.MemoryID, target.ExpectedRevisionID).Scan(&space) != nil || space != p.SpaceID {
			return ErrInvalidEvolutionProposal
		}
	}
	if len(p.Evidence) == 0 {
		return ErrInvalidEvolutionProposal
	}
	for _, evidence := range p.Evidence {
		if err := validateRelationEvidence(ctx, tx, p.SpaceID, evidence); err != nil {
			return ErrInvalidEvolutionProposal
		}
	}
	return nil
}

func nextTwoIDs(r *Repository) (string, string, error) {
	ids, err := r.newIDs(2)
	if err != nil {
		return "", "", err
	}
	return ids[0], ids[1], nil
}

func (r *Repository) GetEvolutionProposal(ctx context.Context, id string) (domain.EvolutionProposal, error) {
	return getEvolutionProposal(ctx, r.store.reader, id)
}

func getEvolutionProposal(ctx context.Context, q candidateQueryer, id string) (domain.EvolutionProposal, error) {
	var p domain.EvolutionProposal
	var relationSpecs, mentionSpecs string
	err := q.QueryRowContext(ctx, `SELECT proposal_id,event_id,space_id,action,proposed_content,valid_from,valid_until,memory_type,sensitivity,relation_specs_json,mention_specs_json,processor_identity,provider_identity,model_identity,processing_version,specification_hash,review_state,created_at,reviewed_at FROM evolution_proposals WHERE proposal_id=?`, id).Scan(&p.ProposalID, &p.EventID, &p.SpaceID, &p.Action, &p.ProposedContent, &p.ValidFrom, &p.ValidUntil, &p.MemoryType, &p.Sensitivity, &relationSpecs, &mentionSpecs, &p.ProcessorIdentity, &p.ProviderIdentity, &p.ModelIdentity, &p.ProcessingVersion, &p.SpecificationHash, &p.ReviewState, &p.CreatedAt, &p.ReviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.RelationSpecs, p.MentionSpecs = json.RawMessage(relationSpecs), json.RawMessage(mentionSpecs)
	p.Targets, p.Evidence, p.Reviews = []domain.EvolutionTarget{}, []domain.RelationEvidence{}, []domain.EvolutionReview{}
	rows, err := q.QueryContext(ctx, `SELECT memory_id,expected_revision_id FROM evolution_proposal_targets WHERE proposal_id=? ORDER BY target_order`, id)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var v domain.EvolutionTarget
		if err = rows.Scan(&v.MemoryID, &v.ExpectedRevisionID); err != nil {
			rows.Close()
			return p, err
		}
		p.Targets = append(p.Targets, v)
	}
	if err = rows.Close(); err != nil {
		return p, err
	}
	rows, err = q.QueryContext(ctx, `SELECT evidence_type,event_id,memory_id,revision_id,start_offset,end_offset,note FROM evolution_proposal_evidence WHERE proposal_id=? ORDER BY evidence_id`, id)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var v domain.RelationEvidence
		if err = rows.Scan(&v.EvidenceType, &v.EventID, &v.MemoryID, &v.RevisionID, &v.StartOffset, &v.EndOffset, &v.Note); err != nil {
			rows.Close()
			return p, err
		}
		p.Evidence = append(p.Evidence, v)
	}
	if err = rows.Close(); err != nil {
		return p, err
	}
	rows, err = q.QueryContext(ctx, `SELECT review_id,from_state,to_state,replacement_mode,reason_code,note,reviewer_id,created_at FROM evolution_proposal_reviews WHERE proposal_id=? ORDER BY created_at DESC,review_id DESC`, id)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var v domain.EvolutionReview
		if err = rows.Scan(&v.ReviewID, &v.FromState, &v.ToState, &v.ReplacementMode, &v.ReasonCode, &v.Note, &v.ReviewerID, &v.CreatedAt); err != nil {
			rows.Close()
			return p, err
		}
		v.Results = []domain.EvolutionResultRef{}
		refs, e := q.QueryContext(ctx, `SELECT result_type,result_id FROM evolution_result_refs WHERE review_id=? ORDER BY result_ref_id`, v.ReviewID)
		if e != nil {
			rows.Close()
			return p, e
		}
		for refs.Next() {
			var ref domain.EvolutionResultRef
			if e = refs.Scan(&ref.ResultType, &ref.ResultID); e != nil {
				refs.Close()
				rows.Close()
				return p, e
			}
			v.Results = append(v.Results, ref)
		}
		refs.Close()
		p.Reviews = append(p.Reviews, v)
	}
	return p, rows.Close()
}

func (r *Repository) ListEvolutionProposals(ctx context.Context, f EvolutionProposalFilter) (domain.EvolutionProposalPage, error) {
	query, args := `SELECT proposal_id FROM evolution_proposals WHERE space_id=?`, []any{f.SpaceID}
	if f.ReviewState != "" {
		query += ` AND review_state=?`
		args = append(args, f.ReviewState)
	}
	if f.Action != "" {
		query += ` AND action=?`
		args = append(args, f.Action)
	}
	if f.Cursor != "" {
		query += ` AND proposal_id<?`
		args = append(args, f.Cursor)
	}
	query += ` ORDER BY proposal_id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.EvolutionProposalPage{}, err
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
		return domain.EvolutionProposalPage{}, err
	}
	page := domain.EvolutionProposalPage{Items: []domain.EvolutionProposal{}}
	for _, id := range ids {
		v, e := r.GetEvolutionProposal(ctx, id)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, v)
	}
	if len(page.Items) > f.Limit {
		next := page.Items[f.Limit-1].ProposalID
		page.NextCursor = &next
		page.Items = page.Items[:f.Limit]
	}
	return page, nil
}

func exactEvolutionTargets(expected, actual []domain.EvolutionTarget) bool {
	if len(expected) != len(actual) {
		return false
	}
	for i := range expected {
		if expected[i] != actual[i] {
			return false
		}
	}
	return true
}

func (r *Repository) RejectEvolutionProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewEvolutionProposalParams) (domain.EvolutionProposal, bool, error) {
	return r.reviewEvolutionProposal(ctx, identity, id, review, false)
}

func (r *Repository) ConfirmEvolutionProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewEvolutionProposalParams) (domain.EvolutionProposal, bool, error) {
	return r.reviewEvolutionProposal(ctx, identity, id, review, true)
}

func (r *Repository) reviewEvolutionProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewEvolutionProposalParams, confirm bool) (domain.EvolutionProposal, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.EvolutionProposal{}, false, err
	}
	defer tx.Rollback()
	status := 200
	if confirm {
		status = 201
	}
	if s, b, found, e := readIdempotency(ctx, tx, identity); e != nil {
		return domain.EvolutionProposal{}, false, e
	} else if found {
		var v domain.EvolutionProposal
		if s != status || json.Unmarshal([]byte(b), &v) != nil {
			return v, false, errors.New("invalid stored idempotency response")
		}
		return v, true, nil
	}
	p, err := getEvolutionProposal(ctx, tx, id)
	if err != nil {
		return p, false, err
	}
	if p.ReviewState != "pending" {
		return p, false, ErrEvolutionReviewed
	}
	if confirm && !exactEvolutionTargets(review.ExpectedTargets, p.Targets) {
		return p, false, ErrRevisionConflict
	}
	if confirm && p.Action == "replacement" && (review.ReplacementMode == nil || !contains([]string{"same_memory_revision", "new_memory"}, *review.ReplacementMode)) {
		return p, false, ErrInvalidEvolutionProposal
	}
	if confirm && p.Action != "replacement" && review.ReplacementMode != nil {
		return p, false, ErrInvalidEvolutionProposal
	}
	if err = r.requireDiskWritable(); err != nil {
		return p, false, err
	}
	if confirm {
		for _, target := range p.Targets {
			var revision, space, state string
			var frozen bool
			if err = tx.QueryRowContext(ctx, `SELECT current_revision_id,space_id,current_state,frozen FROM memories WHERE memory_id=?`, target.MemoryID).Scan(&revision, &space, &state, &frozen); err != nil || space != p.SpaceID || state == "deleted" || state == "retracted" {
				return p, false, ErrRevisionConflict
			}
			if revision != target.ExpectedRevisionID {
				return p, false, ErrRevisionConflict
			}
			if frozen {
				return p, false, fmt.Errorf("%w: %w", ErrRevisionConflict, ErrFrozenRevision)
			}
		}
		for _, evidence := range p.Evidence {
			if err = validateRelationEvidence(ctx, tx, p.SpaceID, evidence); err != nil {
				return p, false, ErrInvalidEvolutionProposal
			}
		}
	}
	operationID, auditID, err := nextTwoIDs(r)
	if err != nil {
		return p, false, err
	}
	reviewID, err := r.runtime.ID()
	if err != nil {
		return p, false, err
	}
	now := formatTime(r.runtime.Now())
	toState := "rejected"
	refs := []domain.EvolutionResultRef{}
	if confirm {
		toState = "confirmed"
		switch p.Action {
		case "duplicate":
		case "supplement":
			refs, err = r.confirmEvolutionRevision(ctx, tx, p, p.Targets[0], false, operationID, identity.CallerID, now)
		case "replacement":
			refs, err = r.confirmEvolutionReplacement(ctx, tx, p, review, operationID, identity.CallerID, now)
		case "conflict":
			refs, err = r.confirmEvolutionConflict(ctx, tx, p, operationID, identity.CallerID, now)
		}
		if err != nil {
			return p, false, err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE evolution_proposals SET review_state=?,reviewed_at=? WHERE proposal_id=? AND review_state='pending'`, toState, now, id)
	if err != nil || !oneRow(res) {
		if err != nil {
			return p, false, err
		}
		return p, false, ErrEvolutionReviewed
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_proposal_reviews(review_id,proposal_id,operation_id,from_state,to_state,replacement_mode,reason_code,note,reviewer_id,created_at) VALUES (?,?,?,'pending',?,?,?,?,?,?)`, reviewID, id, operationID, toState, review.ReplacementMode, review.ReasonCode, review.Note, identity.CallerID, now); err != nil {
		return p, false, err
	}
	for _, ref := range refs {
		refID, e := r.runtime.ID()
		if e != nil {
			return p, false, e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_result_refs(result_ref_id,review_id,result_type,result_id,created_at) VALUES (?,?,?,?,?)`, refID, reviewID, ref.ResultType, ref.ResultID, now); err != nil {
			return p, false, err
		}
	}
	p, err = getEvolutionProposal(ctx, tx, id)
	if err != nil {
		return p, false, err
	}
	body, _ := json.Marshal(p)
	if err = writeIdempotency(ctx, tx, identity, operationID, status, body, now); err != nil {
		return p, false, err
	}
	action := "reject_evolution_proposal"
	if confirm {
		action = "confirm_evolution_proposal"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,reason,created_at) VALUES (?,?,?,?,?,'evolution_proposal',?,'succeeded',?,?)`, auditID, operationID, identity.RequestID, identity.CallerID, action, id, review.ReasonCode, now); err != nil {
		return p, false, err
	}
	if err = tx.Commit(); err != nil {
		return p, false, err
	}
	return p, false, nil
}

func revisionEvidenceFromEvolution(items []domain.RelationEvidence) []EvidenceParams {
	out := []EvidenceParams{}
	for _, e := range items {
		if e.EvidenceType == "raw_event_range" && e.EventID != nil {
			start, end := int64(*e.StartOffset), int64(*e.EndOffset)
			out = append(out, EvidenceParams{EventID: *e.EventID, EvidenceType: "manual_reference", StartOffset: &start, EndOffset: &end})
		}
	}
	return out
}

func (r *Repository) confirmEvolutionRevision(ctx context.Context, tx *sql.Tx, p domain.EvolutionProposal, target domain.EvolutionTarget, newMemory bool, operationID, caller, now string) ([]domain.EvolutionResultRef, error) {
	evidence := revisionEvidenceFromEvolution(p.Evidence)
	if len(evidence) == 0 {
		return nil, ErrInvalidEvidence
	}
	if err := requireEvidence(ctx, tx, p.SpaceID, evidence); err != nil {
		return nil, err
	}
	ids, err := r.newIDs(3 + len(evidence))
	if err != nil {
		return nil, err
	}
	memoryID, revisionID := target.MemoryID, ids[0]
	var parent *string = &target.ExpectedRevisionID
	memoryType := "fact"
	if p.MemoryType != nil {
		memoryType = *p.MemoryType
	}
	if newMemory {
		memoryID, revisionID, parent = ids[0], ids[1], nil
	}
	if newMemory {
		if _, err = tx.ExecContext(ctx, `INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,frozen,sensitivity,created_at,updated_at) VALUES (?,?,?,'active',0,?,?,?)`, memoryID, p.SpaceID, revisionID, p.Sensitivity, now, now); err != nil {
			return nil, err
		}
	} else {
		if err = tx.QueryRowContext(ctx, `SELECT r.kind FROM memories m JOIN memory_revisions r ON r.revision_id=m.current_revision_id WHERE m.memory_id=?`, memoryID).Scan(&memoryType); err != nil {
			return nil, err
		}
	}
	revision, err := revisionFromTextInput(revisionID, memoryID, parent, nil, memoryType, *p.ProposedContent, p.ValidFrom, p.ValidUntil, nil, p.Sensitivity, caller, p.ProcessingVersion, &p.ProcessorIdentity, now)
	if err != nil {
		return nil, err
	}
	if err = insertRevision(ctx, tx, revision); err != nil {
		return nil, err
	}
	evidenceStart := 1
	if newMemory {
		evidenceStart = 2
	}
	if err = insertEvidence(ctx, tx, revisionID, evidence, ids[evidenceStart:evidenceStart+len(evidence)], now); err != nil {
		return nil, err
	}
	if !newMemory {
		res, e := tx.ExecContext(ctx, `UPDATE memories SET current_revision_id=?,sensitivity=?,updated_at=? WHERE memory_id=? AND current_revision_id=?`, revisionID, p.Sensitivity, now, memoryID, target.ExpectedRevisionID)
		if e != nil || !oneRow(res) {
			if e != nil {
				return nil, e
			}
			return nil, ErrRevisionConflict
		}
	}
	state := "active"
	if !newMemory {
		if err = tx.QueryRowContext(ctx, `SELECT current_state FROM memories WHERE memory_id=?`, memoryID).Scan(&state); err != nil {
			return nil, err
		}
	}
	if err = r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, p.SpaceID, state, now); err != nil {
		return nil, err
	}
	if !newMemory {
		if err = r.markDependentAggregatesStale(ctx, tx, memoryID, now); err != nil {
			return nil, err
		}
	}
	jobID, err := r.runtime.ID()
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,created_at) VALUES (?,?,'rebuild_fts','index','memory_fts',?,'pending',?,?,?) ON CONFLICT DO NOTHING`, jobID, operationID, r.processingVersion, r.maxAttempts, now, now); err != nil {
		return nil, err
	}
	return []domain.EvolutionResultRef{{ResultType: "memory", ResultID: memoryID}, {ResultType: "revision", ResultID: revisionID}}, nil
}

func (r *Repository) confirmEvolutionReplacement(ctx context.Context, tx *sql.Tx, p domain.EvolutionProposal, review ReviewEvolutionProposalParams, operationID, caller, now string) ([]domain.EvolutionResultRef, error) {
	newMemory := *review.ReplacementMode == "new_memory"
	refs, err := r.confirmEvolutionRevision(ctx, tx, p, p.Targets[0], newMemory, operationID, caller, now)
	if err != nil {
		return nil, err
	}
	newMemoryID, newRevisionID := refs[0].ResultID, refs[1].ResultID
	relationID, stateID, jobID, err := func() (string, string, string, error) {
		ids, e := r.newIDs(3)
		if e != nil {
			return "", "", "", e
		}
		return ids[0], ids[1], ids[2], nil
	}()
	if err != nil {
		return nil, err
	}
	spec := sha256.Sum256([]byte(newMemoryID + newRevisionID + p.Targets[0].MemoryID + p.Targets[0].ExpectedRevisionID + "supersedes"))
	if _, err = tx.ExecContext(ctx, `INSERT INTO memory_relations(relation_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,state,source,specification_hash,created_by,processing_version,created_at,updated_at) VALUES (?,?,?,?,?,?,?,'active','human_confirmation',?,?,?,?,?)`, relationID, p.SpaceID, "supersedes", newMemoryID, newRevisionID, p.Targets[0].MemoryID, p.Targets[0].ExpectedRevisionID, hex.EncodeToString(spec[:]), caller, p.ProcessingVersion, now, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO relation_state_events(state_event_id,relation_id,operation_id,from_state,to_state,reason,created_by,created_at) VALUES (?,?,?,NULL,'active','replacement confirmed',?,?)`, stateID, relationID, operationID, caller, now); err != nil {
		return nil, err
	}
	if _, err = enqueueRelationIndexJob(ctx, tx, jobID, operationID, r.processingVersion, r.maxAttempts, now); err != nil {
		return nil, err
	}
	refs = append(refs, domain.EvolutionResultRef{ResultType: "relation", ResultID: relationID})
	if newMemory && review.SupersedeTargets {
		stateEventID, e := r.runtime.ID()
		if e != nil {
			return nil, e
		}
		var oldState string
		if e = tx.QueryRowContext(ctx, `SELECT current_state FROM memories WHERE memory_id=?`, p.Targets[0].MemoryID).Scan(&oldState); e != nil {
			return nil, e
		}
		res, e := tx.ExecContext(ctx, `UPDATE memories SET current_state='archived',updated_at=? WHERE memory_id=? AND current_revision_id=?`, now, p.Targets[0].MemoryID, p.Targets[0].ExpectedRevisionID)
		if e != nil || !oneRow(res) {
			if e != nil {
				return nil, e
			}
			return nil, ErrRevisionConflict
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id,memory_id,revision_id,operation_id,from_state,to_state,from_frozen,to_frozen,reason,created_by,created_at) SELECT ?,memory_id,current_revision_id,?,?,'archived',frozen,frozen,'superseded',?,? FROM memories WHERE memory_id=?`, stateEventID, operationID, oldState, caller, now, p.Targets[0].MemoryID); e != nil {
			return nil, e
		}
		if e = r.upsertMemoryProjection(ctx, tx, p.Targets[0].MemoryID, p.Targets[0].ExpectedRevisionID, p.SpaceID, "archived", now); e != nil {
			return nil, e
		}
		if e = r.markDependentAggregatesStale(ctx, tx, p.Targets[0].MemoryID, now); e != nil {
			return nil, e
		}
	}
	return refs, nil
}

func (r *Repository) confirmEvolutionConflict(ctx context.Context, tx *sql.Tx, p domain.EvolutionProposal, operationID, caller, now string) ([]domain.EvolutionResultRef, error) {
	ids, err := r.newIDs(7 + len(p.Evidence))
	if err != nil {
		return nil, err
	}
	relationID, relationStateID, relationJobID, caseID := ids[0], ids[1], ids[2], ids[3]
	spec := sha256.Sum256([]byte(p.Targets[0].MemoryID + p.Targets[0].ExpectedRevisionID + p.Targets[1].MemoryID + p.Targets[1].ExpectedRevisionID + "contradicts"))
	if _, err = tx.ExecContext(ctx, `INSERT INTO memory_relations(relation_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,state,source,specification_hash,created_by,processing_version,created_at,updated_at) VALUES (?,?,?,?,?,?,?,'active','human_confirmation',?,?,?,?,?)`, relationID, p.SpaceID, "contradicts", p.Targets[0].MemoryID, p.Targets[0].ExpectedRevisionID, p.Targets[1].MemoryID, p.Targets[1].ExpectedRevisionID, hex.EncodeToString(spec[:]), caller, p.ProcessingVersion, now, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO relation_state_events(state_event_id,relation_id,operation_id,from_state,to_state,reason,created_by,created_at) VALUES (?,?,?,NULL,'active','conflict confirmed',?,?)`, relationStateID, relationID, operationID, caller, now); err != nil {
		return nil, err
	}
	for i, e := range p.Evidence {
		if _, err = tx.ExecContext(ctx, `INSERT INTO relation_evidence(evidence_id,relation_id,evidence_type,event_id,memory_id,revision_id,start_offset,end_offset,note,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, ids[7+i], relationID, e.EvidenceType, e.EventID, e.MemoryID, e.RevisionID, e.StartOffset, e.EndOffset, e.Note, caller, now); err != nil {
			return nil, err
		}
	}
	if _, err = enqueueRelationIndexJob(ctx, tx, relationJobID, operationID, r.processingVersion, r.maxAttempts, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO conflict_cases(case_id,space_id,state,opened_by,processing_version,created_at) VALUES (?,?,'open',?,?,?)`, caseID, p.SpaceID, caller, p.ProcessingVersion, now); err != nil {
		return nil, err
	}
	for i, t := range p.Targets {
		stateID := ids[4+i]
		var state, sensitivity string
		if err = tx.QueryRowContext(ctx, `SELECT current_state,sensitivity FROM memories WHERE memory_id=?`, t.MemoryID).Scan(&state, &sensitivity); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO conflict_case_participants(case_id,participant_order,memory_id,revision_id,input_state,input_sensitivity,created_at) VALUES (?,?,?,?,?,?,?)`, caseID, i, t.MemoryID, t.ExpectedRevisionID, state, sensitivity, now); err != nil {
			return nil, err
		}
		if state != "contested" {
			res, e := tx.ExecContext(ctx, `UPDATE memories SET current_state='contested',updated_at=? WHERE memory_id=? AND current_revision_id=?`, now, t.MemoryID, t.ExpectedRevisionID)
			if e != nil || !oneRow(res) {
				if e != nil {
					return nil, e
				}
				return nil, ErrRevisionConflict
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id,memory_id,revision_id,operation_id,from_state,to_state,from_frozen,to_frozen,reason,created_by,created_at) SELECT ?,memory_id,current_revision_id,?,?,'contested',frozen,frozen,'conflict confirmed',?,? FROM memories WHERE memory_id=?`, stateID, operationID, state, caller, now, t.MemoryID); e != nil {
				return nil, e
			}
			m, e := getMemory(ctx, tx, t.MemoryID, false)
			if e != nil {
				return nil, e
			}
			if e = r.upsertMemoryProjection(ctx, tx, m.MemoryID, m.CurrentRevision.RevisionID, m.SpaceID, "contested", now); e != nil {
				return nil, e
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO conflict_case_relations(case_id,relation_id,created_at) VALUES (?,?,?)`, caseID, relationID, now); err != nil {
		return nil, err
	}
	return []domain.EvolutionResultRef{{ResultType: "relation", ResultID: relationID}, {ResultType: "conflict_case", ResultID: caseID}}, nil
}

func validReviewText(reason string, note *string) bool {
	return utf8.RuneCountInString(reason) >= 1 && utf8.RuneCountInString(reason) <= 128 && (note == nil || utf8.RuneCountInString(*note) <= 1024)
}
