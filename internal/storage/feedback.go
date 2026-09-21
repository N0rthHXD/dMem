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
	"sort"
	"strconv"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

// ErrFeedbackIdentityMismatch rejects feedback whose decision context was
// produced by a ranking, feature, or context identity that is no longer
// compatible with the running configuration.
var ErrFeedbackIdentityMismatch = errors.New("feedback decision identity is incompatible")

// FeedbackAlgorithmVersion is the single versioned feedback aggregation
// contract. Normal storage operations, maintenance rebuild, restore, and import
// all invoke the same AggregateFeedback implementation under this version.
const FeedbackAlgorithmVersion = "v2"

const (
	feedbackWindowDays     = 90
	feedbackMinimumSupport = 3
	feedbackHalfLifeDays   = 45
	feedbackGlobalWeight   = 0.001
	feedbackQueryWeight    = 0.0005
)

// FeedbackAggregationIdentity is a deterministic, non-secret identity for the
// feedback aggregation contract. It is bound into ranking, cursors, explain
// output, telemetry, export/import, and backup/restore.
func FeedbackAggregationIdentity() string {
	return FeedbackAlgorithmVersion + ":window=" + strconv.Itoa(feedbackWindowDays) +
		":min_support=" + strconv.Itoa(feedbackMinimumSupport) +
		":half_life=" + strconv.Itoa(feedbackHalfLifeDays)
}

// FeedbackGlobalWeight returns the maximum magnitude the global bounded prior
// may add to a candidate score.
func FeedbackGlobalWeight() float64 { return feedbackGlobalWeight }

// FeedbackQueryWeight returns the maximum magnitude the query-conditioned
// adjustment may add to a candidate score.
func FeedbackQueryWeight() float64 { return feedbackQueryWeight }

type AppendFeedbackParams struct {
	SpaceID, MemoryID, RevisionID     string
	Purpose, FeedbackType, ReasonCode string
	Note                              *string
	Decision                          *domain.FeedbackDecisionContext
	CreateProposal                    bool
	SourceEventID, ProposedContent    string
}

type FeedbackFilter struct {
	CallerID, SpaceID, MemoryID, RevisionID, Purpose, FeedbackType string
	Limit                                                          int
	Cursor                                                         string
}

func (r *Repository) AppendFeedback(ctx context.Context, identity WriteIdentity, p AppendFeedbackParams) (domain.FeedbackAppendResponse, bool, error) {
	return r.appendFeedback(ctx, identity, p, false)
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
	if err := r.requireFeedbackIdentity(p.Decision); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	ids, err := r.newIDs(4)
	if err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	feedbackID, operationID, jobID, auditID := ids[0], ids[1], ids[2], ids[3]
	now := formatTime(r.runtime.Now())
	var searchOperation, queryHash, queryType, entityIDs, rankingIdentity, featureIdentity, contextIdentity, selectionState, matchedSignals, answerIdentity sql.NullString
	var resultRank sql.NullInt64
	if p.Decision != nil {
		searchOperation = sql.NullString{String: p.Decision.SearchOperationID, Valid: true}
		queryHash = sql.NullString{String: p.Decision.QueryHash, Valid: true}
		queryType = sql.NullString{String: p.Decision.QueryType, Valid: true}
		rankingIdentity = sql.NullString{String: p.Decision.RankingIdentity, Valid: true}
		featureIdentity = sql.NullString{String: p.Decision.FeatureIdentity, Valid: true}
		contextIdentity = sql.NullString{String: p.Decision.ContextIdentity, Valid: true}
		resultRank = sql.NullInt64{Int64: int64(p.Decision.ResultRank), Valid: true}
		selectionState = sql.NullString{String: p.Decision.SelectionState, Valid: true}
		answerIdentity = sql.NullString{String: p.Decision.AnswerIdentity, Valid: p.Decision.AnswerIdentity != ""}
		if len(p.Decision.EntityIDs) > 0 {
			encoded, _ := json.Marshal(p.Decision.EntityIDs)
			entityIDs = sql.NullString{String: string(encoded), Valid: true}
		}
		encoded, _ := json.Marshal(p.Decision.MatchedSignals)
		matchedSignals = sql.NullString{String: string(encoded), Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO feedback_events(feedback_id,operation_id,caller_id,memory_id,revision_id,feedback_purpose,feedback_type,reason_code,note,search_operation_id,query_hash,query_type,entity_ids_json,ranking_identity,feature_identity,context_identity,result_rank,selection_state,matched_signals_json,answer_identity,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		feedbackID, operationID, identity.CallerID, p.MemoryID, p.RevisionID, p.Purpose, p.FeedbackType, p.ReasonCode, p.Note,
		searchOperation, queryHash, queryType, entityIDs, rankingIdentity, featureIdentity, contextIdentity, resultRank, selectionState, matchedSignals, answerIdentity, now); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) VALUES (?,?,'aggregate_feedback','feedback_revision',?,?, 'pending',50,?,?,?) ON CONFLICT DO NOTHING`, jobID, operationID, p.RevisionID, FeedbackAlgorithmVersion, r.maxAttempts, now, now); err != nil {
		return domain.FeedbackAppendResponse{}, false, err
	}
	result := domain.FeedbackAppendResponse{Feedback: domain.FeedbackEvent{FeedbackID: feedbackID, OperationID: operationID, CallerID: identity.CallerID, MemoryID: p.MemoryID, RevisionID: p.RevisionID, Purpose: p.Purpose, FeedbackType: p.FeedbackType, ReasonCode: p.ReasonCode, Note: p.Note, Decision: p.Decision, CreatedAt: now}, Proposal: domain.ProposalOutcome{Status: "not_requested"}}
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

	if p.CreateProposal && domain.CanProposeEvolution(p.Purpose, p.FeedbackType) && p.SourceEventID != "" && p.ProposedContent != "" {
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

// requireFeedbackIdentity rejects decision context produced under a ranking,
// feature, or context identity that no longer matches the running
// configuration. Memory quality feedback carries no decision context.
func (r *Repository) requireFeedbackIdentity(decision *domain.FeedbackDecisionContext) error {
	if decision == nil {
		return nil
	}
	if decision.FeatureIdentity != retrieval.RankingFeatureVersion {
		return ErrFeedbackIdentityMismatch
	}
	if decision.RankingIdentity != r.rankingIdentity() {
		return ErrFeedbackIdentityMismatch
	}
	if decision.ContextIdentity == "" {
		return ErrFeedbackIdentityMismatch
	}
	return nil
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
	if err := tx.QueryRowContext(ctx, `SELECT e.space_id,r.kind,m.sensitivity FROM raw_events e JOIN memories m ON m.memory_id=? JOIN memory_revisions r ON r.revision_id=m.current_revision_id WHERE e.event_id=? AND e.space_id=m.space_id`, p.MemoryID, p.SourceEventID).Scan(&eventSpace, &memoryType, &sensitivity); err != nil {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO evolution_proposals(proposal_id,event_id,space_id,action,proposed_content,proposed_content_hash,memory_type,sensitivity,processor_identity,processing_version,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, proposalID, p.SourceEventID, eventSpace, action, p.ProposedContent, hex.EncodeToString(contentHash[:]), memoryType, sensitivity, "feedback/"+p.FeedbackType, r.processingVersion(), hex.EncodeToString(hash[:]), now); err != nil {
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
	query := feedbackSelectColumns + ` FROM feedback_events f JOIN memories m ON m.memory_id=f.memory_id WHERE m.space_id IN (` + placeholders(len(scope)) + `)`
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
	if f.Purpose != "" {
		query += " AND f.feedback_purpose=?"
		args = append(args, f.Purpose)
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

const feedbackSelectColumns = `SELECT f.feedback_id,f.operation_id,f.caller_id,f.memory_id,f.revision_id,f.feedback_purpose,f.feedback_type,f.reason_code,CASE WHEN f.caller_id=? THEN f.note ELSE NULL END,f.search_operation_id,f.query_hash,f.query_type,f.entity_ids_json,f.ranking_identity,f.feature_identity,f.context_identity,f.result_rank,f.selection_state,f.matched_signals_json,f.answer_identity,f.created_at`

func scanFeedback(row rowScanner) (domain.FeedbackEvent, error) {
	var f domain.FeedbackEvent
	var note, searchOp, queryHash, queryType, entityIDs, ranking, feature, contextID, selection, signals, answer sql.NullString
	var rank sql.NullInt64
	err := row.Scan(&f.FeedbackID, &f.OperationID, &f.CallerID, &f.MemoryID, &f.RevisionID, &f.Purpose, &f.FeedbackType, &f.ReasonCode, &note, &searchOp, &queryHash, &queryType, &entityIDs, &ranking, &feature, &contextID, &rank, &selection, &signals, &answer, &f.CreatedAt)
	if err != nil {
		return f, err
	}
	if note.Valid {
		f.Note = &note.String
	}
	if searchOp.Valid {
		decision := domain.FeedbackDecisionContext{SearchOperationID: searchOp.String, QueryHash: queryHash.String, QueryType: queryType.String, RankingIdentity: ranking.String, FeatureIdentity: feature.String, ContextIdentity: contextID.String, ResultRank: int(rank.Int64), SelectionState: selection.String, AnswerIdentity: answer.String}
		_ = json.Unmarshal([]byte(entityIDs.String), &decision.EntityIDs)
		_ = json.Unmarshal([]byte(signals.String), &decision.MatchedSignals)
		f.Decision = &decision
	}
	return f, nil
}

// FeedbackExecutor is satisfied by *sql.Tx and *sql.DB. It lets the single
// aggregation implementation run inside online storage transactions, the
// maintenance restore transaction, and import/rebuild jobs.
type FeedbackExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// FeedbackAggregationResult reports what a rebuild produced.
type FeedbackAggregationResult struct {
	AlgorithmVersion string
	Generation       int64
	SourceWatermark  string
	GlobalCount      int
	QueryCount       int
}

// AggregateFeedback is the one versioned aggregation function. It reads only
// append-only feedback events and writes the global bounded prior plus
// query-conditioned adjustments. It never mutates memory authority, eligibility,
// scope, or review state.
func AggregateFeedback(ctx context.Context, exec FeedbackExecutor, at time.Time, algorithmVersion string, windowDays, minimumSupport, halfLifeDays int) (FeedbackAggregationResult, error) {
	if algorithmVersion == "" {
		algorithmVersion = FeedbackAlgorithmVersion
	}
	if windowDays <= 0 {
		windowDays = feedbackWindowDays
	}
	if minimumSupport < 1 {
		minimumSupport = feedbackMinimumSupport
	}
	if halfLifeDays <= 0 {
		halfLifeDays = feedbackHalfLifeDays
	}
	at = at.UTC()
	startText := formatTime(at.AddDate(0, 0, -windowDays))
	endText := formatTime(at)
	var generation int64
	if err := exec.QueryRowContext(ctx, `SELECT generation+1 FROM feedback_aggregate_metadata WHERE singleton=1`).Scan(&generation); err != nil {
		return FeedbackAggregationResult{}, err
	}
	global, err := globalFeedbackAggregates(ctx, exec, startText, endText, at, halfLifeDays)
	if err != nil {
		return FeedbackAggregationResult{}, err
	}
	queryAggregates, err := queryFeedbackAggregates(ctx, exec, startText, endText, at, halfLifeDays, minimumSupport)
	if err != nil {
		return FeedbackAggregationResult{}, err
	}
	var watermark sql.NullString
	if err := exec.QueryRowContext(ctx, `SELECT max(created_at||'/'||feedback_id) FROM feedback_events WHERE created_at<=?`, endText).Scan(&watermark); err != nil {
		return FeedbackAggregationResult{}, err
	}
	if _, err := exec.ExecContext(ctx, `DELETE FROM feedback_aggregates`); err != nil {
		return FeedbackAggregationResult{}, err
	}
	if _, err := exec.ExecContext(ctx, `DELETE FROM feedback_query_aggregates`); err != nil {
		return FeedbackAggregationResult{}, err
	}
	for _, row := range global {
		if _, err := exec.ExecContext(ctx, `INSERT INTO feedback_aggregates(revision_id,algorithm_version,generation,positive_count,negative_count,bounded_usage_score,window_start,window_end,source_watermark,state,updated_at) VALUES (?,?,?,?,?,?,?,?,?,'current',?)`, row.revision, algorithmVersion, generation, row.positive, row.negative, row.score, startText, endText, nullableString(watermark.String), endText); err != nil {
			return FeedbackAggregationResult{}, err
		}
	}
	for _, row := range queryAggregates {
		if _, err := exec.ExecContext(ctx, `INSERT INTO feedback_query_aggregates(query_hash,query_type,revision_id,algorithm_version,generation,positive_count,negative_count,distinct_callers,support_count,bounded_adjustment,window_start,window_end,source_watermark,state,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,'current',?)`, row.queryHash, row.queryType, row.revision, algorithmVersion, generation, row.positive, row.negative, row.callers, row.support, row.score, startText, endText, nullableString(watermark.String), endText); err != nil {
			return FeedbackAggregationResult{}, err
		}
	}
	result, err := exec.ExecContext(ctx, `UPDATE feedback_aggregate_metadata SET algorithm_version=?,generation=?,state='ready',source_watermark=?,window_days=?,minimum_support=?,updated_at=? WHERE singleton=1 AND generation<?`, algorithmVersion, generation, nullableString(watermark.String), windowDays, minimumSupport, endText, generation)
	if err != nil {
		return FeedbackAggregationResult{}, err
	}
	if !execOneRow(result) {
		return FeedbackAggregationResult{}, ErrLeaseLost
	}
	return FeedbackAggregationResult{AlgorithmVersion: algorithmVersion, Generation: generation, SourceWatermark: watermark.String, GlobalCount: len(global), QueryCount: len(queryAggregates)}, nil
}

type globalFeedbackRow struct {
	revision           string
	positive, negative int
	score              float64
}

type queryFeedbackRow struct {
	queryHash, queryType string
	revision             string
	positive, negative   int
	callers, support     int
	score                float64
}

func globalFeedbackAggregates(ctx context.Context, exec FeedbackExecutor, start, end string, at time.Time, halfLifeDays int) ([]globalFeedbackRow, error) {
	rows, err := exec.QueryContext(ctx, `SELECT revision_id,caller_id,feedback_type,created_at FROM feedback_events WHERE feedback_purpose='retrieval_feedback' AND created_at>=? AND created_at<=? ORDER BY revision_id,caller_id,created_at DESC,feedback_id DESC`, start, end)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	order := []string{}
	index := map[string]*globalFeedbackRow{}
	defer rows.Close()
	for rows.Next() {
		var revision, caller, kind, created string
		if err := rows.Scan(&revision, &caller, &kind, &created); err != nil {
			return nil, err
		}
		key := revision + "\x00" + caller
		if seen[key] {
			continue
		}
		seen[key] = true
		row, ok := index[revision]
		if !ok {
			row = &globalFeedbackRow{revision: revision}
			index[revision] = row
			order = append(order, revision)
		}
		weight := feedbackDecay(created, at, halfLifeDays)
		if kind == "useful" {
			row.positive++
			row.score += weight
		} else {
			row.negative++
			row.score -= weight
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]globalFeedbackRow, 0, len(order))
	for _, revision := range order {
		row := index[revision]
		row.score = clampSigned(row.score/8, -1, 1)
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].revision < result[j].revision })
	return result, nil
}

func queryFeedbackAggregates(ctx context.Context, exec FeedbackExecutor, start, end string, at time.Time, halfLifeDays, minimumSupport int) ([]queryFeedbackRow, error) {
	rows, err := exec.QueryContext(ctx, `SELECT query_hash,query_type,revision_id,caller_id,feedback_type,created_at FROM feedback_events WHERE feedback_purpose='retrieval_feedback' AND query_hash IS NOT NULL AND query_type IS NOT NULL AND created_at>=? AND created_at<=? ORDER BY query_hash,revision_id,caller_id,created_at DESC,feedback_id DESC`, start, end)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	order := []string{}
	index := map[string]*queryFeedbackRow{}
	defer rows.Close()
	for rows.Next() {
		var queryHash, queryType, revision, caller, kind, created string
		if err := rows.Scan(&queryHash, &queryType, &revision, &caller, &kind, &created); err != nil {
			return nil, err
		}
		key := queryHash + "\x00" + revision + "\x00" + caller
		if seen[key] {
			continue
		}
		seen[key] = true
		group := queryHash + "\x00" + revision
		row, ok := index[group]
		if !ok {
			row = &queryFeedbackRow{queryHash: queryHash, queryType: queryType, revision: revision}
			index[group] = row
			order = append(order, group)
		}
		weight := feedbackDecay(created, at, halfLifeDays)
		row.callers++
		row.support++
		if kind == "useful" {
			row.positive++
			row.score += weight
		} else {
			row.negative++
			row.score -= weight
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]queryFeedbackRow, 0, len(order))
	for _, group := range order {
		row := index[group]
		if row.support < minimumSupport || row.callers < 2 {
			continue
		}
		row.score = clampSigned(row.score/4, -1, 1)
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].queryHash != result[j].queryHash {
			return result[i].queryHash < result[j].queryHash
		}
		return result[i].revision < result[j].revision
	})
	return result, nil
}

func feedbackDecay(createdAt string, at time.Time, halfLifeDays int) float64 {
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return 1
	}
	ageDays := at.Sub(created.UTC()).Hours() / 24
	if ageDays <= 0 {
		return 1
	}
	return math.Pow(0.5, ageDays/float64(halfLifeDays))
}

func clampSigned(value, minimum, maximum float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func execOneRow(result sql.Result) bool {
	affected, err := result.RowsAffected()
	return err == nil && affected == 1
}

func (r *Repository) aggregateFeedback(ctx context.Context, exec FeedbackExecutor) (FeedbackAggregationResult, error) {
	return AggregateFeedback(ctx, exec, r.runtime.Now().UTC(), FeedbackAlgorithmVersion, feedbackWindowDays, feedbackMinimumSupport, feedbackHalfLifeDays)
}

func (r *Repository) RebuildFeedbackAggregates(ctx context.Context) error {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := r.aggregateFeedback(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) RebuildFeedbackAggregatesClaimed(ctx context.Context, claim ClaimedJob) error {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.requireJobClaim(ctx, tx, claim); err != nil {
		return err
	}
	if _, err := r.aggregateFeedback(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) FeedbackAggregateHealth(ctx context.Context) (domain.FeedbackAggregateHealth, error) {
	var h domain.FeedbackAggregateHealth
	var watermark sql.NullString
	err := r.store.reader.QueryRowContext(ctx, `SELECT state,algorithm_version,generation,source_watermark,window_days,minimum_support FROM feedback_aggregate_metadata WHERE singleton=1`).Scan(&h.State, &h.AlgorithmVersion, &h.Generation, &watermark, &h.WindowDays, &h.MinimumSupport)
	if watermark.Valid {
		h.SourceWatermark = &watermark.String
	}
	return h, err
}

// FeedbackUsageScores loads the combined bounded global prior plus
// query-conditioned adjustment for a candidate window. Global and
// query-conditioned terms are individually bounded and are never allowed to
// establish primary relevance.
func (r *Repository) FeedbackUsageScores(ctx context.Context, tx *sql.Tx, revisions []string, generation int64, queryHash string) (map[string]float64, error) {
	if len(revisions) == 0 {
		return map[string]float64{}, nil
	}
	scores := map[string]float64{}
	const batchSize = 400
	for start := 0; start < len(revisions); start += batchSize {
		end := min(start+batchSize, len(revisions))
		batch := revisions[start:end]
		args := make([]any, 0, len(batch)+2)
		for _, revision := range batch {
			args = append(args, revision)
		}
		args = append(args, FeedbackAlgorithmVersion, generation)
		rows, err := tx.QueryContext(ctx, `SELECT revision_id,bounded_usage_score FROM feedback_aggregates WHERE revision_id IN (`+placeholders(len(batch))+`) AND algorithm_version=? AND generation=? AND state='current'`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var revision string
			var score float64
			if err := rows.Scan(&revision, &score); err != nil {
				rows.Close()
				return nil, err
			}
			scores[revision] = feedbackGlobalWeight * clampSigned(score, -1, 1)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if queryHash != "" {
			queryArgs := make([]any, 0, len(batch)+3)
			queryArgs = append(queryArgs, queryHash)
			for _, revision := range batch {
				queryArgs = append(queryArgs, revision)
			}
			queryArgs = append(queryArgs, FeedbackAlgorithmVersion, generation)
			rows, err := tx.QueryContext(ctx, `SELECT revision_id,bounded_adjustment FROM feedback_query_aggregates WHERE query_hash=? AND revision_id IN (`+placeholders(len(batch))+`) AND algorithm_version=? AND generation=? AND state='current'`, queryArgs...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var revision string
				var score float64
				if err := rows.Scan(&revision, &score); err != nil {
					rows.Close()
					return nil, err
				}
				scores[revision] += feedbackQueryWeight * clampSigned(score, -1, 1)
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
	}
	return scores, nil
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
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM jobs WHERE job_type='rebuild_feedback_aggregates' AND target_type='index' AND target_id='feedback_aggregates' AND processing_version=? AND status IN ('pending','waiting','running','retrying')`, FeedbackAlgorithmVersion).Scan(&active)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) VALUES (?,?,'rebuild_feedback_aggregates','index','feedback_aggregates',?,'pending',50,?,?,?)`, ids[0], ids[1], FeedbackAlgorithmVersion, r.maxAttempts, now, now); err != nil {
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
