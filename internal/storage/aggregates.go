package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
)

var (
	ErrInvalidAggregateProposal  = errors.New("invalid aggregate proposal")
	ErrAggregateProposalReviewed = errors.New("aggregate proposal has already been reviewed")
)

type CommitAggregateProposalParams struct {
	SpaceID, AggregateKind, Content, Sensitivity       string
	PolicyVersion, ProcessorIdentity, ProviderIdentity string
	ModelIdentity, ProcessingVersion                   string
	ValidFrom, ValidUntil                              *string
	Inputs                                             []domain.AggregateInput
}

type AggregateProposalFilter struct {
	SpaceID, ReviewState, AggregateKind, Cursor string
	Limit                                       int
}

type AggregateFilter struct {
	SpaceID, AggregateKind, Freshness, Cursor string
	ExcludeStale                              bool
	Limit                                     int
}

type ReviewAggregateProposalParams struct {
	ReasonCode string
	Note       *string
}

func (r *Repository) PrepareAggregateBuild(ctx context.Context, space, kind string, revisionIDs []string, maxInputs int) ([]domain.AggregateInput, error) {
	if (kind != "scene" && kind != "topic") || len(revisionIDs) == 0 || len(revisionIDs) > maxInputs {
		return nil, ErrInvalidAggregateProposal
	}
	if err := r.RequireEffectivePolicy(ctx, "processing", space); err != nil {
		return nil, err
	}
	ids := append([]string(nil), revisionIDs...)
	sort.Strings(ids)
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			return nil, ErrInvalidAggregateProposal
		}
	}
	tx, err := r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	inputs := make([]domain.AggregateInput, 0, len(ids))
	for _, revisionID := range ids {
		var input domain.AggregateInput
		var evidence int
		err = tx.QueryRowContext(ctx, `SELECT m.memory_id,r.revision_id,m.space_id,r.display_content,m.current_state,m.sensitivity,(SELECT count(*) FROM memory_evidence e WHERE e.revision_id=r.revision_id) FROM memory_revisions r JOIN memories m ON m.memory_id=r.memory_id WHERE r.revision_id=?`, revisionID).Scan(&input.MemoryID, &input.RevisionID, &input.SpaceID, &input.Content, &input.CurrentState, &input.Sensitivity, &evidence)
		if err != nil || input.SpaceID != space || input.RevisionID == "" || input.RevisionID != revisionID || input.CurrentState != "active" && input.CurrentState != "contested" || evidence == 0 {
			return nil, ErrInvalidAggregateProposal
		}
		var current string
		if err = tx.QueryRowContext(ctx, `SELECT current_revision_id FROM memories WHERE memory_id=?`, input.MemoryID).Scan(&current); err != nil || current != revisionID {
			return nil, ErrRevisionConflict
		}
		inputs = append(inputs, input)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return inputs, nil
}

func aggregateFingerprint(inputs []domain.AggregateInput) (string, string) {
	type snapshot struct {
		MemoryID, RevisionID, SpaceID, State, Sensitivity string
	}
	values := make([]snapshot, len(inputs))
	for i, input := range inputs {
		values[i] = snapshot{input.MemoryID, input.RevisionID, input.SpaceID, input.CurrentState, input.Sensitivity}
	}
	b, _ := json.Marshal(values)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), string(b)
}

func aggregateSpecificationHash(p CommitAggregateProposalParams, fingerprint string) string {
	b, _ := json.Marshal(struct {
		Kind, Content, Fingerprint, Sensitivity, Policy, Processor, Provider, Model, Version string
		ValidFrom, ValidUntil                                                                *string
	}{p.AggregateKind, p.Content, fingerprint, p.Sensitivity, p.PolicyVersion, p.ProcessorIdentity, p.ProviderIdentity, p.ModelIdentity, p.ProcessingVersion, p.ValidFrom, p.ValidUntil})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func validateAggregateOutput(p CommitAggregateProposalParams) error {
	if p.SpaceID == "" || (p.AggregateKind != "scene" && p.AggregateKind != "topic") || p.Content == "" || p.PolicyVersion == "" || p.ProcessorIdentity == "" || p.ProviderIdentity == "" || p.ModelIdentity == "" || p.ProcessingVersion == "" || (p.Sensitivity != "normal" && p.Sensitivity != "sensitive") {
		return ErrInvalidAggregateProposal
	}
	parseTime := func(value *string) (*time.Time, error) {
		if value == nil {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, *value)
		if err != nil {
			return nil, err
		}
		return &parsed, nil
	}
	validFrom, err := parseTime(p.ValidFrom)
	if err != nil {
		return ErrInvalidAggregateProposal
	}
	validUntil, err := parseTime(p.ValidUntil)
	if err != nil {
		return ErrInvalidAggregateProposal
	}
	if validFrom != nil && validUntil != nil && !validFrom.Before(*validUntil) {
		return ErrInvalidAggregateProposal
	}
	for _, input := range p.Inputs {
		if input.Sensitivity == "sensitive" && p.Sensitivity != "sensitive" {
			return ErrInvalidAggregateProposal
		}
	}
	return nil
}

func (r *Repository) CommitAggregateProposal(ctx context.Context, identity WriteIdentity, p CommitAggregateProposalParams, claim *ClaimedJob) (domain.AggregateProposal, bool, error) {
	p.Inputs = append([]domain.AggregateInput(nil), p.Inputs...)
	sort.Slice(p.Inputs, func(i, j int) bool { return p.Inputs[i].RevisionID < p.Inputs[j].RevisionID })
	for i := 1; i < len(p.Inputs); i++ {
		if p.Inputs[i].RevisionID == p.Inputs[i-1].RevisionID {
			return domain.AggregateProposal{}, false, ErrInvalidAggregateProposal
		}
	}
	if err := validateAggregateOutput(p); err != nil {
		return domain.AggregateProposal{}, false, err
	}
	fingerprint, _ := aggregateFingerprint(p.Inputs)
	specification := aggregateSpecificationHash(p, fingerprint)
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.AggregateProposal{}, false, err
	}
	defer tx.Rollback()
	if claim != nil {
		if claim.Job.ConfigurationGeneration != r.currentProcessingGeneration() || claim.Job.PolicyGeneration != r.currentProcessingPolicyGeneration() {
			return domain.AggregateProposal{}, false, ErrStaleGeneration
		}
		if err = r.requireJobClaim(ctx, tx, *claim); err != nil {
			return domain.AggregateProposal{}, false, err
		}
	} else if result, replay, replayErr := replayAggregateProposal(ctx, tx, identity, 201); replayErr != nil || replay {
		return result, replay, replayErr
	}
	if err = r.requireDiskWritable(); err != nil {
		return domain.AggregateProposal{}, false, err
	}
	if err = validatePreparedAggregateInputs(ctx, tx, p.SpaceID, p.Inputs); err != nil {
		return domain.AggregateProposal{}, false, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT proposal_id FROM aggregate_proposals WHERE space_id=? AND aggregate_kind=? AND input_fingerprint=? AND processing_version=? AND specification_hash=?`, p.SpaceID, p.AggregateKind, fingerprint, p.ProcessingVersion, specification).Scan(&existing)
	if err == nil {
		result, getErr := getAggregateProposal(ctx, tx, existing)
		return result, true, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.AggregateProposal{}, false, err
	}
	ids, err := r.newIDs(3)
	if err != nil {
		return domain.AggregateProposal{}, false, err
	}
	now := formatTime(r.runtime.Now())
	contentHash := sha256.Sum256([]byte(p.Content))
	if _, err = tx.ExecContext(ctx, `INSERT INTO aggregate_proposals(proposal_id,space_id,aggregate_kind,content,content_hash,input_fingerprint,valid_from,valid_until,sensitivity,policy_version,processor_identity,provider_identity,model_identity,processing_version,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ids[0], p.SpaceID, p.AggregateKind, p.Content, hex.EncodeToString(contentHash[:]), fingerprint, p.ValidFrom, p.ValidUntil, p.Sensitivity, p.PolicyVersion, p.ProcessorIdentity, p.ProviderIdentity, p.ModelIdentity, p.ProcessingVersion, specification, now); err != nil {
		return domain.AggregateProposal{}, false, err
	}
	for i, input := range p.Inputs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO aggregate_proposal_inputs(proposal_id,input_order,memory_id,revision_id,input_space_id,input_state,input_sensitivity,created_at) VALUES (?,?,?,?,?,?,?,?)`, ids[0], i, input.MemoryID, input.RevisionID, input.SpaceID, input.CurrentState, input.Sensitivity, now); err != nil {
			return domain.AggregateProposal{}, false, err
		}
	}
	result, err := getAggregateProposal(ctx, tx, ids[0])
	if err != nil {
		return result, false, err
	}
	if claim == nil {
		body, _ := json.Marshal(result)
		if err = writeIdempotency(ctx, tx, identity, ids[1], 201, body, now); err != nil {
			return result, false, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,created_at) VALUES (?,?,?,?,'build_aggregate_proposal','aggregate_proposal',?,'succeeded',?)`, ids[2], ids[1], identity.RequestID, identity.CallerID, ids[0], now); err != nil {
			return result, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return result, false, err
	}
	return result, false, nil
}

func validatePreparedAggregateInputs(ctx context.Context, tx *sql.Tx, space string, inputs []domain.AggregateInput) error {
	if len(inputs) == 0 {
		return ErrInvalidAggregateProposal
	}
	for _, expected := range inputs {
		var revision, state, sensitivity, inputSpace string
		var evidence int
		err := tx.QueryRowContext(ctx, `SELECT current_revision_id,current_state,sensitivity,space_id,(SELECT count(*) FROM memory_evidence WHERE revision_id=?) FROM memories WHERE memory_id=?`, expected.RevisionID, expected.MemoryID).Scan(&revision, &state, &sensitivity, &inputSpace, &evidence)
		if err != nil || revision != expected.RevisionID || state != expected.CurrentState || sensitivity != expected.Sensitivity || inputSpace != space || evidence == 0 {
			return ErrRevisionConflict
		}
	}
	return nil
}

func replayAggregateProposal(ctx context.Context, tx *sql.Tx, identity WriteIdentity, status int) (domain.AggregateProposal, bool, error) {
	s, body, found, err := readIdempotency(ctx, tx, identity)
	if err != nil || !found {
		return domain.AggregateProposal{}, false, err
	}
	var result domain.AggregateProposal
	if s != status || json.Unmarshal([]byte(body), &result) != nil {
		return result, false, errors.New("invalid stored idempotency response")
	}
	return result, true, nil
}

func (r *Repository) GetAggregateProposal(ctx context.Context, id string) (domain.AggregateProposal, error) {
	return getAggregateProposal(ctx, r.store.reader, id)
}

func getAggregateProposal(ctx context.Context, q candidateQueryer, id string) (domain.AggregateProposal, error) {
	var p domain.AggregateProposal
	err := q.QueryRowContext(ctx, `SELECT proposal_id,space_id,aggregate_kind,content,input_fingerprint,valid_from,valid_until,sensitivity,policy_version,processor_identity,provider_identity,model_identity,processing_version,specification_hash,review_state,result_memory_id,result_revision_id,created_at,reviewed_at FROM aggregate_proposals WHERE proposal_id=?`, id).Scan(&p.ProposalID, &p.SpaceID, &p.AggregateKind, &p.Content, &p.InputFingerprint, &p.ValidFrom, &p.ValidUntil, &p.Sensitivity, &p.PolicyVersion, &p.ProcessorIdentity, &p.ProviderIdentity, &p.ModelIdentity, &p.ProcessingVersion, &p.SpecificationHash, &p.ReviewState, &p.ResultMemoryID, &p.ResultRevisionID, &p.CreatedAt, &p.ReviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Inputs, p.Reviews = []domain.AggregateInput{}, []domain.AggregateProposalReview{}
	rows, err := q.QueryContext(ctx, `SELECT i.memory_id,i.revision_id,i.input_space_id,r.display_content,i.input_state,i.input_sensitivity FROM aggregate_proposal_inputs i JOIN memory_revisions r ON r.revision_id=i.revision_id WHERE i.proposal_id=? ORDER BY i.input_order`, id)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var input domain.AggregateInput
		if err = rows.Scan(&input.MemoryID, &input.RevisionID, &input.SpaceID, &input.Content, &input.CurrentState, &input.Sensitivity); err != nil {
			rows.Close()
			return p, err
		}
		p.Inputs = append(p.Inputs, input)
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT review_id,from_state,to_state,reason_code,note,reviewer_id,memory_id,revision_id,created_at FROM aggregate_proposal_reviews WHERE proposal_id=? ORDER BY created_at DESC,review_id DESC`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var review domain.AggregateProposalReview
		if err = rows.Scan(&review.ReviewID, &review.FromState, &review.ToState, &review.ReasonCode, &review.Note, &review.ReviewerID, &review.MemoryID, &review.RevisionID, &review.CreatedAt); err != nil {
			return p, err
		}
		p.Reviews = append(p.Reviews, review)
	}
	return p, rows.Err()
}

func (r *Repository) ListAggregateProposals(ctx context.Context, f AggregateProposalFilter) (domain.AggregateProposalPage, error) {
	query, args := `SELECT proposal_id FROM aggregate_proposals WHERE space_id=?`, []any{f.SpaceID}
	if f.ReviewState != "" {
		query += ` AND review_state=?`
		args = append(args, f.ReviewState)
	}
	if f.AggregateKind != "" {
		query += ` AND aggregate_kind=?`
		args = append(args, f.AggregateKind)
	}
	if f.Cursor != "" {
		query += ` AND proposal_id<?`
		args = append(args, f.Cursor)
	}
	query += ` ORDER BY proposal_id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.AggregateProposalPage{}, err
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
	page := domain.AggregateProposalPage{Items: []domain.AggregateProposal{}}
	for _, id := range ids {
		item, e := r.GetAggregateProposal(ctx, id)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, item)
	}
	if len(page.Items) > f.Limit {
		next := page.Items[f.Limit-1].ProposalID
		page.NextCursor = &next
		page.Items = page.Items[:f.Limit]
	}
	return page, err
}

func (r *Repository) ConfirmAggregateProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewAggregateProposalParams) (domain.AggregateProposal, bool, error) {
	return r.reviewAggregateProposal(ctx, identity, id, review, true)
}

func (r *Repository) RejectAggregateProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewAggregateProposalParams) (domain.AggregateProposal, bool, error) {
	return r.reviewAggregateProposal(ctx, identity, id, review, false)
}

func (r *Repository) reviewAggregateProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewAggregateProposalParams, confirm bool) (domain.AggregateProposal, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.AggregateProposal{}, false, err
	}
	defer tx.Rollback()
	status := 200
	if confirm {
		status = 201
	}
	if result, replay, e := replayAggregateProposal(ctx, tx, identity, status); e != nil || replay {
		return result, replay, e
	}
	p, err := getAggregateProposal(ctx, tx, id)
	if err != nil {
		return p, false, err
	}
	if p.ReviewState != "pending" {
		return p, false, ErrAggregateProposalReviewed
	}
	if err = r.requireDiskWritable(); err != nil {
		return p, false, err
	}
	if confirm {
		if err = validatePreparedAggregateInputs(ctx, tx, p.SpaceID, p.Inputs); err != nil {
			return p, false, err
		}
		for _, input := range p.Inputs {
			if input.Sensitivity == "sensitive" && p.Sensitivity != "sensitive" {
				return p, false, ErrInvalidAggregateProposal
			}
		}
	}
	evidenceCount := 0
	if confirm {
		for _, input := range p.Inputs {
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_evidence WHERE revision_id=?`, input.RevisionID).Scan(&count); err != nil || count == 0 {
				return p, false, ErrInvalidAggregateProposal
			}
			evidenceCount += count
		}
	}
	ids, err := r.newIDs(12 + len(p.Inputs)*4 + evidenceCount)
	if err != nil {
		return p, false, err
	}
	operationID, reviewID, auditID := ids[0], ids[1], ids[2]
	now, toState := formatTime(r.runtime.Now()), "rejected"
	var memoryID, revisionID *string
	idx := 3
	if confirm {
		mid, rid, descriptorID := ids[idx], ids[idx+1], ids[idx+2]
		idx += 3
		memoryID, revisionID, toState = &mid, &rid, "confirmed"
		if _, err = tx.ExecContext(ctx, `INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,frozen,sensitivity,created_at,updated_at) VALUES (?,?,?,'active',0,?,?,?)`, mid, p.SpaceID, rid, p.Sensitivity, now, now); err != nil {
			return p, false, err
		}
		processor := p.ProcessorIdentity
		revision, revisionErr := revisionFromTextInput(rid, mid, nil, nil, p.AggregateKind, p.Content, p.ValidFrom, p.ValidUntil, nil, p.Sensitivity, identity.CallerID, p.ProcessingVersion, &processor, now)
		if revisionErr != nil {
			return p, false, revisionErr
		}
		if err = insertRevision(ctx, tx, revision); err != nil {
			return p, false, err
		}
		seenEvents := map[string]bool{}
		for _, input := range p.Inputs {
			rows, e := tx.QueryContext(ctx, `SELECT event_id,role,start_offset,end_offset FROM memory_evidence WHERE revision_id=? ORDER BY evidence_id`, input.RevisionID)
			if e != nil {
				return p, false, e
			}
			for rows.Next() {
				var evidence EvidenceParams
				if e = rows.Scan(&evidence.EventID, &evidence.Role, &evidence.StartOffset, &evidence.EndOffset); e != nil {
					rows.Close()
					return p, false, e
				}
				key := evidence.EventID + ":-"
				if evidence.StartOffset != nil {
					key = fmt.Sprintf("%s:%d:%d", evidence.EventID, *evidence.StartOffset, *evidence.EndOffset)
				}
				if !seenEvents[key] {
					if err = insertEvidence(ctx, tx, rid, []EvidenceParams{evidence}, []string{ids[idx]}, now); err != nil {
						rows.Close()
						return p, false, err
					}
					idx++
					seenEvents[key] = true
				}
			}
			rows.Close()
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id,memory_id,revision_id,operation_id,from_state,to_state,from_frozen,to_frozen,reason,created_by,created_at) VALUES (?,?,?, ?,NULL,'active',NULL,0,?,?,?)`, ids[idx], mid, rid, operationID, review.ReasonCode, identity.CallerID, now); err != nil {
			return p, false, err
		}
		idx++
		if err = r.upsertMemoryProjection(ctx, tx, mid, rid, p.SpaceID, "active", now); err != nil {
			return p, false, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO aggregate_descriptors(aggregate_id,memory_id,aggregate_kind,policy_version,processing_version,sensitivity,created_by,created_at) VALUES (?,?,?,?,?,?,?,?)`, descriptorID, mid, p.AggregateKind, p.PolicyVersion, p.ProcessingVersion, p.Sensitivity, identity.CallerID, now); err != nil {
			return p, false, err
		}
		_, snapshot := aggregateFingerprint(p.Inputs)
		if _, err = tx.ExecContext(ctx, `INSERT INTO aggregate_freshness(aggregate_id,revision_id,state,input_fingerprint,input_snapshot_json,stale_reasons_json,generation,checked_at,updated_at) VALUES (?,?,'fresh',?,?,'[]',1,?,?)`, descriptorID, rid, p.InputFingerprint, snapshot, now, now); err != nil {
			return p, false, err
		}
		for _, input := range p.Inputs {
			relationID, relationStateID, relationEvidenceID := ids[idx], ids[idx+1], ids[idx+2]
			idx += 3
			hashValue := sha256.Sum256([]byte("derived_from\x00" + rid + "\x00" + input.RevisionID))
			hash := hex.EncodeToString(hashValue[:])
			if _, err = tx.ExecContext(ctx, `INSERT INTO memory_relations(relation_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,state,source,specification_hash,created_by,processing_version,created_at,updated_at) VALUES (?,?, 'derived_from',?,?,?,?,'active','human_confirmation',?,?,?,?,?)`, relationID, p.SpaceID, mid, rid, input.MemoryID, input.RevisionID, hash, identity.CallerID, p.ProcessingVersion, now, now); err != nil {
				return p, false, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO relation_state_events(state_event_id,relation_id,operation_id,from_state,to_state,reason,created_by,created_at) VALUES (?,?,?,NULL,'active',?,?,?)`, relationStateID, relationID, operationID, review.ReasonCode, identity.CallerID, now); err != nil {
				return p, false, err
			}
			end := utf8.RuneCountInString(input.Content)
			if end < 1 {
				return p, false, ErrInvalidAggregateProposal
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO relation_evidence(evidence_id,relation_id,evidence_type,memory_id,revision_id,start_offset,end_offset,created_by,created_at) VALUES (?,?,'revision_range',?,?,0,?,?,?)`, relationEvidenceID, relationID, input.MemoryID, input.RevisionID, end, identity.CallerID, now); err != nil {
				return p, false, err
			}
		}
		if _, err = enqueueRelationIndexJob(ctx, tx, ids[idx], operationID, r.processingVersion, r.maxAttempts, now); err != nil {
			return p, false, err
		}
		idx++
		if _, err = tx.ExecContext(ctx, `UPDATE relation_index_metadata SET state='stale',updated_at=? WHERE singleton=1`, now); err != nil {
			return p, false, err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE aggregate_proposals SET review_state=?,result_memory_id=?,result_revision_id=?,reviewed_at=? WHERE proposal_id=? AND review_state='pending'`, toState, memoryID, revisionID, now, id)
	if err != nil || !oneRow(res) {
		if err != nil {
			return p, false, err
		}
		return p, false, ErrAggregateProposalReviewed
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO aggregate_proposal_reviews(review_id,proposal_id,operation_id,from_state,to_state,reason_code,note,reviewer_id,memory_id,revision_id,created_at) VALUES (?,?,?,'pending',?,?,?,?,?,?,?)`, reviewID, id, operationID, toState, review.ReasonCode, review.Note, identity.CallerID, memoryID, revisionID, now); err != nil {
		return p, false, err
	}
	p, err = getAggregateProposal(ctx, tx, id)
	if err != nil {
		return p, false, err
	}
	body, _ := json.Marshal(p)
	if err = writeIdempotency(ctx, tx, identity, operationID, status, body, now); err != nil {
		return p, false, err
	}
	action := "reject_aggregate_proposal"
	if confirm {
		action = "confirm_aggregate_proposal"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,reason,created_at) VALUES (?,?,?,?,?,'aggregate_proposal',?,'succeeded',?,?)`, auditID, operationID, identity.RequestID, identity.CallerID, action, id, review.ReasonCode, now); err != nil {
		return p, false, err
	}
	if err = tx.Commit(); err != nil {
		return p, false, err
	}
	return p, false, nil
}

func (r *Repository) markDependentAggregatesStale(ctx context.Context, tx *sql.Tx, inputMemoryID, now string) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT d.aggregate_id FROM aggregate_descriptors d JOIN memories a ON a.memory_id=d.memory_id JOIN memory_relations rel ON rel.source_memory_id=d.memory_id AND rel.source_revision_id=a.current_revision_id AND rel.relation_type='derived_from' AND rel.state='active' WHERE rel.target_memory_id=?`, inputMemoryID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, aggregateID := range ids {
		if err = r.refreshAggregateFreshness(ctx, tx, aggregateID, now); err != nil {
			return err
		}
		var state string
		if err = tx.QueryRowContext(ctx, `SELECT state FROM aggregate_freshness WHERE aggregate_id=?`, aggregateID).Scan(&state); err != nil {
			return err
		}
		if state == "stale" {
			if err = r.enqueueAggregateRebuild(ctx, tx, aggregateID, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Repository) enqueueAggregateRebuild(ctx context.Context, tx *sql.Tx, aggregateID, now string) error {
	ids, err := r.newIDs(2)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) VALUES (?,?,'rebuild_aggregate','aggregate',?,?,'pending',50,?,?,?) ON CONFLICT DO NOTHING`, ids[0], ids[1], aggregateID, r.processingVersion, r.maxAttempts, now, now)
	return err
}

func (r *Repository) refreshAggregateFreshness(ctx context.Context, tx *sql.Tx, aggregateID, now string) error {
	var snapshotJSON string
	if err := tx.QueryRowContext(ctx, `SELECT input_snapshot_json FROM aggregate_freshness WHERE aggregate_id=?`, aggregateID).Scan(&snapshotJSON); err != nil {
		return err
	}
	var baseline []struct{ MemoryID, RevisionID, SpaceID, State, Sensitivity string }
	if err := json.Unmarshal([]byte(snapshotJSON), &baseline); err != nil {
		return err
	}
	reasonSet := map[string]bool{}
	for _, old := range baseline {
		var revision, space, state, sensitivity string
		if err := tx.QueryRowContext(ctx, `SELECT current_revision_id,space_id,current_state,sensitivity FROM memories WHERE memory_id=?`, old.MemoryID).Scan(&revision, &space, &state, &sensitivity); err != nil {
			reasonSet["input_missing"] = true
			continue
		}
		if revision != old.RevisionID {
			reasonSet["input_revision_changed"] = true
		}
		if space != old.SpaceID {
			reasonSet["input_space_changed"] = true
		}
		if state != old.State {
			reasonSet["input_state_changed"] = true
		}
		if sensitivity != old.Sensitivity {
			reasonSet["input_sensitivity_changed"] = true
		}
	}
	reasons := make([]string, 0, len(reasonSet))
	for reason := range reasonSet {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	encoded, _ := json.Marshal(reasons)
	state := "fresh"
	if len(reasons) > 0 {
		state = "stale"
	}
	_, err := tx.ExecContext(ctx, `UPDATE aggregate_freshness SET state=?,stale_reasons_json=?,generation=generation+1,checked_at=?,updated_at=? WHERE aggregate_id=?`, state, string(encoded), now, now, aggregateID)
	return err
}

func (r *Repository) RebuildAggregateFreshness(ctx context.Context) error {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT d.aggregate_id,m.current_revision_id,af.aggregate_id IS NULL FROM aggregate_descriptors d JOIN memories m ON m.memory_id=d.memory_id LEFT JOIN aggregate_freshness af ON af.aggregate_id=d.aggregate_id ORDER BY d.aggregate_id`)
	if err != nil {
		return err
	}
	type projection struct {
		aggregateID, revisionID string
		missing                 bool
	}
	var projections []projection
	for rows.Next() {
		var item projection
		if err = rows.Scan(&item.aggregateID, &item.revisionID, &item.missing); err != nil {
			rows.Close()
			return err
		}
		projections = append(projections, item)
	}
	rows.Close()
	now := formatTime(r.runtime.Now())
	for _, item := range projections {
		if item.missing {
			var proposalID string
			if err = tx.QueryRowContext(ctx, `SELECT proposal_id FROM aggregate_proposals WHERE review_state='confirmed' AND result_revision_id=?`, item.revisionID).Scan(&proposalID); err != nil {
				return fmt.Errorf("rebuild aggregate freshness %s: %w", item.aggregateID, err)
			}
			proposal, getErr := getAggregateProposal(ctx, tx, proposalID)
			if getErr != nil {
				return getErr
			}
			fingerprint, snapshot := aggregateFingerprint(proposal.Inputs)
			if fingerprint != proposal.InputFingerprint {
				return fmt.Errorf("rebuild aggregate freshness %s: input fingerprint mismatch", item.aggregateID)
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO aggregate_freshness(aggregate_id,revision_id,state,input_fingerprint,input_snapshot_json,stale_reasons_json,generation,checked_at,updated_at) VALUES (?,?,'fresh',?,?,'[]',1,?,?)`, item.aggregateID, item.revisionID, fingerprint, snapshot, now, now); err != nil {
				return err
			}
		}
		if err = r.refreshAggregateFreshness(ctx, tx, item.aggregateID, now); err != nil {
			return err
		}
		var state string
		if err = tx.QueryRowContext(ctx, `SELECT state FROM aggregate_freshness WHERE aggregate_id=?`, item.aggregateID).Scan(&state); err != nil {
			return err
		}
		if state == "stale" {
			if err = r.enqueueAggregateRebuild(ctx, tx, item.aggregateID, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *Repository) GetAggregate(ctx context.Context, id string) (domain.Aggregate, error) {
	return getAggregate(ctx, r.store.reader, id)
}

func getAggregate(ctx context.Context, q candidateQueryer, id string) (domain.Aggregate, error) {
	var a domain.Aggregate
	var reasons string
	err := q.QueryRowContext(ctx, `SELECT d.aggregate_id,d.aggregate_kind,d.policy_version,d.processing_version,d.sensitivity,f.state,f.input_fingerprint,f.stale_reasons_json,f.generation,f.checked_at,d.memory_id FROM aggregate_descriptors d JOIN aggregate_freshness f ON f.aggregate_id=d.aggregate_id WHERE d.aggregate_id=? OR d.memory_id=?`, id, id).Scan(&a.AggregateID, &a.AggregateKind, &a.PolicyVersion, &a.ProcessingVersion, &a.Sensitivity, &a.Freshness, &a.InputFingerprint, &reasons, &a.Generation, &a.CheckedAt, &a.Memory.MemoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.Memory, err = getMemory(ctx, q, a.Memory.MemoryID, true)
	if err != nil {
		return a, err
	}
	a.AggregateStale = a.Freshness == "stale"
	a.StaleReasons = []string{}
	if err = json.Unmarshal([]byte(reasons), &a.StaleReasons); err != nil {
		return a, err
	}
	a.Provenance = []domain.AggregateProvenance{}
	rows, err := q.QueryContext(ctx, `SELECT r.relation_id,r.target_memory_id,r.target_revision_id,m.current_revision_id,m.current_state,m.sensitivity FROM memory_relations r JOIN memories m ON m.memory_id=r.target_memory_id WHERE r.relation_type='derived_from' AND r.source_memory_id=? AND r.source_revision_id=? ORDER BY r.target_revision_id`, a.Memory.MemoryID, a.Memory.CurrentRevision.RevisionID)
	if err != nil {
		return a, err
	}
	defer rows.Close()
	for rows.Next() {
		var p domain.AggregateProvenance
		if err = rows.Scan(&p.RelationID, &p.MemoryID, &p.RevisionID, &p.CurrentRevisionID, &p.CurrentState, &p.Sensitivity); err != nil {
			return a, err
		}
		a.Provenance = append(a.Provenance, p)
	}
	return a, rows.Err()
}

func (r *Repository) ListAggregates(ctx context.Context, f AggregateFilter) (domain.AggregatePage, error) {
	query, args := `SELECT d.aggregate_id FROM aggregate_descriptors d JOIN memories m ON m.memory_id=d.memory_id JOIN aggregate_freshness af ON af.aggregate_id=d.aggregate_id WHERE m.space_id=?`, []any{f.SpaceID}
	if f.AggregateKind != "" {
		query += ` AND d.aggregate_kind=?`
		args = append(args, f.AggregateKind)
	}
	if f.Freshness != "" {
		query += ` AND af.state=?`
		args = append(args, f.Freshness)
	}
	if f.ExcludeStale {
		query += ` AND af.state='fresh'`
	}
	if f.Cursor != "" {
		query += ` AND d.aggregate_id<?`
		args = append(args, f.Cursor)
	}
	query += ` ORDER BY d.aggregate_id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.AggregatePage{}, err
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
	page := domain.AggregatePage{Items: []domain.Aggregate{}}
	for _, id := range ids {
		item, e := r.GetAggregate(ctx, id)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, item)
	}
	if len(page.Items) > f.Limit {
		next := page.Items[f.Limit-1].AggregateID
		page.NextCursor = &next
		page.Items = page.Items[:f.Limit]
	}
	return page, err
}

func (r *Repository) PrepareAggregateRebuild(ctx context.Context, claim ClaimedJob, maxInputs int) (string, string, []string, error) {
	if claim.Job.JobType != "rebuild_aggregate" || claim.Job.TargetType != "aggregate" {
		return "", "", nil, ErrInvalidJobState
	}
	var space, kind string
	err := r.store.reader.QueryRowContext(ctx, `SELECT m.space_id,d.aggregate_kind FROM aggregate_descriptors d JOIN memories m ON m.memory_id=d.memory_id WHERE d.aggregate_id=?`, claim.Job.TargetID).Scan(&space, &kind)
	if err != nil {
		return "", "", nil, err
	}
	rows, err := r.store.reader.QueryContext(ctx, `SELECT m.current_revision_id FROM memory_relations rel JOIN memories m ON m.memory_id=rel.target_memory_id JOIN aggregate_descriptors d ON d.memory_id=rel.source_memory_id WHERE d.aggregate_id=? AND rel.relation_type='derived_from' ORDER BY m.current_revision_id`, claim.Job.TargetID)
	if err != nil {
		return "", "", nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return "", "", nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 || len(ids) > maxInputs {
		return "", "", nil, ErrInvalidAggregateProposal
	}
	return space, kind, ids, nil
}

func (r *Repository) AggregateHealth(ctx context.Context) (domain.AggregateHealth, error) {
	var h domain.AggregateHealth
	var oldest sql.NullString
	err := r.store.reader.QueryRowContext(ctx, `SELECT coalesce(sum(CASE WHEN state='fresh' THEN 1 ELSE 0 END),0),coalesce(sum(CASE WHEN state='stale' THEN 1 ELSE 0 END),0),min(CASE WHEN state='stale' THEN updated_at END) FROM aggregate_freshness`).Scan(&h.FreshCount, &h.StaleCount, &oldest)
	if err != nil {
		return h, err
	}
	if err = r.store.reader.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE job_type='rebuild_aggregate' AND status IN ('pending','waiting','running','retrying')`).Scan(&h.PendingRebuilds); err != nil {
		return h, err
	}
	h.State = "ready"
	if h.StaleCount > 0 || h.PendingRebuilds > 0 {
		h.State = "stale"
	}
	if oldest.Valid {
		h.OldestStaleAt = &oldest.String
	}
	return h, nil
}
