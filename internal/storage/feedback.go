package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	"github.com/N0rthHXD/dMem/internal/domain"
)

const FeedbackAlgorithmVersion = "v1"

type AppendFeedbackParams struct {
	SpaceID, MemoryID, RevisionID, FeedbackType, ReasonCode string
	Note                                                    *string
	Search                                                  *domain.FeedbackSearchContext
	CreateProposal                                          bool
	SourceEventID, ProposedContent                          string
}

type FeedbackFilter struct {
	CallerID, SpaceID, MemoryID, RevisionID, FeedbackType string
	Limit                                                 int
	Cursor                                                string
}

func (r *Repository) AppendFeedback(ctx context.Context, identity WriteIdentity, p AppendFeedbackParams) (domain.FeedbackAppendResponse, bool, error) {
	return r.appendFeedback(ctx, identity, p, false)
}

func (r *Repository) AppendCurrentFeedback(ctx context.Context, identity WriteIdentity, p AppendFeedbackParams) (domain.FeedbackAppendResponse, bool, error) {
	return r.appendFeedback(ctx, identity, p, true)
}

func (r *Repository) appendFeedback(ctx context.Context, identity WriteIdentity, p AppendFeedbackParams, currentRevision bool) (domain.FeedbackAppendResponse, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	} else if found {
		var result domain.FeedbackAppendResponse
		if status != 201 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	if currentRevision {
		if err := tx.QueryRowContext(ctx, `SELECT current_revision_id FROM memories WHERE memory_id=? AND current_state<>'deleted'`, p.MemoryID).Scan(&p.RevisionID); errors.Is(err, sql.ErrNoRows) {
			return domain.FeedbackAppendResponse{}, false, ErrNotFound
		} else if err != nil {
			return domain.FeedbackAppendResponse{}, false, err
		}
	}
	if err := requireFeedbackTarget(ctx, tx, p.SpaceID, p.MemoryID, p.RevisionID); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	ids, err := r.newIDs(4)
	if err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	feedbackID, operationID, jobID, auditID := ids[0], ids[1], ids[2], ids[3]
	now := formatTime(r.runtime.Now())
	var searchOperation, queryHash, rankingVersion, channels any
	var resultRank any
	if p.Search != nil {
		searchOperation, queryHash, rankingVersion, resultRank = p.Search.SearchOperationID, p.Search.QueryHash, p.Search.RankingVersion, p.Search.ResultRank
		encoded, _ := json.Marshal(p.Search.MatchedChannels)
		channels = string(encoded)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO feedback_events(feedback_id,operation_id,caller_id,memory_id,revision_id,feedback_type,reason_code,note,search_operation_id,query_hash,result_rank,ranking_version,matched_channels_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, feedbackID, operationID, identity.CallerID, p.MemoryID, p.RevisionID, p.FeedbackType, p.ReasonCode, p.Note, searchOperation, queryHash, resultRank, rankingVersion, channels, now); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) VALUES (?,?,'aggregate_feedback','feedback_revision',?,'v1','pending',50,?,?,?) ON CONFLICT DO NOTHING`, jobID, operationID, p.RevisionID, r.maxAttempts, now, now); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	result := domain.FeedbackAppendResponse{Feedback: domain.FeedbackEvent{FeedbackID: feedbackID, OperationID: operationID, CallerID: identity.CallerID, MemoryID: p.MemoryID, RevisionID: p.RevisionID, FeedbackType: p.FeedbackType, ReasonCode: p.ReasonCode, Note: p.Note, Search: p.Search, CreatedAt: now}, Proposal: domain.ProposalOutcome{Status: "not_requested"}}
	if p.CreateProposal {
		result.Proposal = domain.ProposalOutcome{Status: "not_feasible", Reason: "source_event_id and proposed_content are required"}
	}
	body, _ := json.Marshal(result)
	if err := writeIdempotency(ctx, tx, identity, operationID, 201, body, now); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,created_at) VALUES (?,?,?,?, 'append_feedback','feedback',?,'succeeded',?)`, auditID, operationID, identity.RequestID, identity.CallerID, feedbackID, now); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}

	if p.CreateProposal && p.SourceEventID != "" && p.ProposedContent != "" {
		proposal, proposalErr := r.createFeedbackProposal(ctx, p, now)
		if proposalErr == nil {
			result.Proposal = domain.ProposalOutcome{Status: "created", ProposalID: &proposal}
		} else {
			result.Proposal = domain.ProposalOutcome{Status: "failed", Reason: "proposal creation failed"}
		}
		updated, _ := json.Marshal(result)
		_, _ = r.store.writer.ExecContext(ctx, `UPDATE idempotency_records SET response_body=? WHERE caller_id=? AND http_method=? AND normalized_route=? AND idempotency_key=?`, string(updated), identity.CallerID, identity.Method, identity.Route, identity.IdempotencyKey)
	}
	return result, false, nil
}

func requireFeedbackTarget(ctx context.Context, tx *sql.Tx, spaceID, memoryID, revisionID string) error {
	var memorySpace string
	err := tx.QueryRowContext(ctx, `SELECT m.space_id FROM memories m JOIN memory_revisions r ON r.memory_id=m.memory_id WHERE m.memory_id=? AND r.revision_id=?`, memoryID, revisionID).Scan(&memorySpace)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	scope, err := memoryScope(ctx, tx, spaceID, true)
	if err != nil {
		return ErrNotFound
	}
	if !contains(scope, memorySpace) {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) createFeedbackProposal(ctx context.Context, p AppendFeedbackParams, now string) (string, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var eventSpace, memoryType, sensitivity string
	if err := tx.QueryRowContext(ctx, `SELECT e.space_id,m.memory_type,m.sensitivity FROM raw_events e JOIN memories m ON m.memory_id=? WHERE e.event_id=? AND e.space_id=m.space_id`, p.MemoryID, p.SourceEventID).Scan(&eventSpace, &memoryType, &sensitivity); err != nil {
		return "", err
	}
	action := map[string]string{"correct": "replacement", "supplement": "supplement", "contest": "conflict"}[p.FeedbackType]
	if action == "" {
		return "", errors.New("feedback type cannot propose evolution")
	}
	proposalID, err := r.runtime.ID()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(action + "\x00" + p.MemoryID + "\x00" + p.RevisionID + "\x00" + p.ProposedContent))
	contentHash := sha256.Sum256([]byte(p.ProposedContent))
	if _, err := tx.ExecContext(ctx, `INSERT INTO evolution_proposals(proposal_id,event_id,space_id,action,proposed_content,proposed_content_hash,memory_type,sensitivity,processor_identity,processing_version,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, proposalID, p.SourceEventID, eventSpace, action, p.ProposedContent, hex.EncodeToString(contentHash[:]), memoryType, sensitivity, "feedback/"+p.FeedbackType, r.processingVersion, hex.EncodeToString(hash[:]), now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO evolution_proposal_targets(proposal_id,target_order,memory_id,expected_revision_id,created_at) VALUES (?,0,?,?,?)`, proposalID, p.MemoryID, p.RevisionID, now); err != nil {
		return "", err
	}
	return proposalID, tx.Commit()
}

func (r *Repository) ListFeedback(ctx context.Context, f FeedbackFilter) (domain.FeedbackPage, error) {
	tx, err := r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.FeedbackPage{}, err
	}
	defer tx.Rollback()
	scope, err := memoryScope(ctx, tx, f.SpaceID, true)
	if err != nil {
		return domain.FeedbackPage{}, ErrNotFound
	}
	query := `SELECT f.feedback_id,f.operation_id,f.caller_id,f.memory_id,f.revision_id,f.feedback_type,f.reason_code,CASE WHEN f.caller_id=? THEN f.note ELSE NULL END,f.search_operation_id,f.query_hash,f.result_rank,f.ranking_version,f.matched_channels_json,f.created_at FROM feedback_events f JOIN memories m ON m.memory_id=f.memory_id WHERE m.space_id IN (` + placeholders(len(scope)) + `)`
	args := []any{f.CallerID}
	for _, id := range scope {
		args = append(args, id)
	}
	if f.MemoryID != "" {
		query += " AND f.memory_id=?"
		args = append(args, f.MemoryID)
	}
	if f.RevisionID != "" {
		query += " AND f.revision_id=?"
		args = append(args, f.RevisionID)
	}
	if f.FeedbackType != "" {
		query += " AND f.feedback_type=?"
		args = append(args, f.FeedbackType)
	}
	if f.Cursor != "" {
		var c struct{ Time, ID string }
		raw, e := base64.RawURLEncoding.DecodeString(f.Cursor)
		if e != nil || json.Unmarshal(raw, &c) != nil || c.Time == "" || c.ID == "" {
			return domain.FeedbackPage{}, ErrInvalidCursor
		}
		query += " AND (f.created_at<? OR (f.created_at=? AND f.feedback_id<?))"
		args = append(args, c.Time, c.Time, c.ID)
	}
	query += " ORDER BY f.created_at DESC,f.feedback_id DESC LIMIT ?"
	args = append(args, f.Limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.FeedbackPage{}, err
	}
	defer rows.Close()
	page := domain.FeedbackPage{Items: []domain.FeedbackEvent{}}
	for rows.Next() {
		item, err := scanFeedback(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if len(page.Items) > f.Limit {
		last := page.Items[f.Limit-1]
		raw, _ := json.Marshal(struct{ Time, ID string }{last.CreatedAt, last.FeedbackID})
		next := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &next
		page.Items = page.Items[:f.Limit]
	}
	return page, rows.Err()
}

func scanFeedback(row rowScanner) (domain.FeedbackEvent, error) {
	var f domain.FeedbackEvent
	var note, searchOp, queryHash, ranking, channels sql.NullString
	var rank sql.NullInt64
	err := row.Scan(&f.FeedbackID, &f.OperationID, &f.CallerID, &f.MemoryID, &f.RevisionID, &f.FeedbackType, &f.ReasonCode, &note, &searchOp, &queryHash, &rank, &ranking, &channels, &f.CreatedAt)
	if err != nil {
		return f, err
	}
	if note.Valid {
		f.Note = &note.String
	}
	if searchOp.Valid {
		s := domain.FeedbackSearchContext{SearchOperationID: searchOp.String, QueryHash: queryHash.String, ResultRank: int(rank.Int64), RankingVersion: ranking.String}
		_ = json.Unmarshal([]byte(channels.String), &s.MatchedChannels)
		f.Search = &s
	}
	return f, nil
}

func (r *Repository) RebuildFeedbackAggregates(ctx context.Context) error {
	return r.rebuildFeedbackAggregates(ctx, nil)
}
func (r *Repository) RebuildFeedbackAggregatesClaimed(ctx context.Context, claim ClaimedJob) error {
	return r.rebuildFeedbackAggregates(ctx, &claim)
}

func (r *Repository) rebuildFeedbackAggregates(ctx context.Context, claim *ClaimedJob) error {
	end := r.runtime.Now().UTC()
	start := end.AddDate(0, 0, -90)
	endText, startText := formatTime(end), formatTime(start)
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if claim != nil {
		if err := r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT generation+1 FROM feedback_aggregate_metadata WHERE singleton=1`).Scan(&generation); err != nil {
		return err
	}
	// Aggregate v1 counts only relevance feedback: the latest useful or
	// irrelevant event per (revision, caller) inside the 90-day window.
	// Corrective types (correct/contest/supplement) never rank memories.
	rows, err := tx.QueryContext(ctx, `WITH latest AS (SELECT revision_id,caller_id,feedback_type,feedback_id,created_at,row_number() OVER (PARTITION BY revision_id,caller_id ORDER BY created_at DESC,feedback_id DESC) n FROM feedback_events WHERE created_at>=? AND created_at<=? AND feedback_type IN ('useful','irrelevant')) SELECT revision_id,sum(feedback_type='useful'),sum(feedback_type='irrelevant') FROM latest WHERE n=1 GROUP BY revision_id ORDER BY revision_id`, startText, endText)
	if err != nil {
		return err
	}
	type aggregate struct {
		revision           string
		positive, negative int
	}
	var values []aggregate
	for rows.Next() {
		var a aggregate
		if err = rows.Scan(&a.revision, &a.positive, &a.negative); err != nil {
			break
		}
		values = append(values, a)
	}
	closeErr := rows.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	var watermark sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT max(created_at||'/'||feedback_id) FROM feedback_events WHERE created_at<=?`, endText).Scan(&watermark); err != nil {
		return err
	}
	if claim != nil {
		if err := r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM feedback_aggregates`); err != nil {
		return err
	}
	for _, a := range values {
		score := math.Max(-1, math.Min(1, float64(a.positive-a.negative)/8))
		if _, err := tx.ExecContext(ctx, `INSERT INTO feedback_aggregates(revision_id,algorithm_version,generation,positive_count,negative_count,bounded_usage_score,window_start,window_end,source_watermark,state,updated_at) VALUES (?,'v1',?,?,?,?,?,?,?,'current',?)`, a.revision, generation, a.positive, a.negative, score, startText, endText, nullableString(watermark.String), endText); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE feedback_aggregate_metadata SET algorithm_version='v1',generation=?,state='ready',source_watermark=?,window_days=90,updated_at=? WHERE singleton=1 AND generation<?`, generation, nullableString(watermark.String), endText, generation)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return ErrLeaseLost
	}
	return tx.Commit()
}

func (r *Repository) FeedbackAggregateHealth(ctx context.Context) (domain.FeedbackAggregateHealth, error) {
	var h domain.FeedbackAggregateHealth
	var watermark sql.NullString
	err := r.store.reader.QueryRowContext(ctx, `SELECT state,algorithm_version,generation,source_watermark,window_days FROM feedback_aggregate_metadata WHERE singleton=1`).Scan(&h.State, &h.AlgorithmVersion, &h.Generation, &watermark, &h.WindowDays)
	if watermark.Valid {
		h.SourceWatermark = &watermark.String
	}
	return h, err
}

func (r *Repository) CreateFeedbackRebuildJob(ctx context.Context, identity WriteIdentity) (domain.JobReference, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.JobReference{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.JobReference{}, false, err
	} else if found {
		var result domain.JobReference
		if status != 202 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	var active string
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM jobs WHERE job_type='rebuild_feedback_aggregates' AND target_type='index' AND target_id='feedback_aggregates' AND processing_version='v1' AND status IN ('pending','waiting','running','retrying')`).Scan(&active)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) VALUES (?,?,'rebuild_feedback_aggregates','index','feedback_aggregates','v1','pending',50,?,?,?)`, ids[0], ids[1], r.maxAttempts, now, now); err != nil {
		return domain.JobReference{}, false, err
	}
	result := domain.JobReference{JobID: ids[0], OperationID: ids[1]}
	body, _ := json.Marshal(result)
	if err := writeIdempotency(ctx, tx, identity, ids[1], 202, body, now); err != nil {
		return result, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,created_at) VALUES (?,?,?,?, 'rebuild_feedback_aggregates','index','feedback_aggregates','accepted',?)`, ids[2], ids[1], identity.RequestID, identity.CallerID, now); err != nil {
		return result, false, err
	}
	return result, false, tx.Commit()
}
