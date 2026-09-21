package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/retrieval"
	"github.com/mattn/go-sqlite3"
)

// keywordHintColumnWeight discounts the Agent Tool search-hint FTS column
// relative to the canonical content column. Content is the canonical statement;
// hints are an Agent-generated retrieval aid. The lower weight, together with
// the deterministic hint-only ordering rule, keeps hint recall from displacing a
// canonical content match.
const keywordHintColumnWeight = 0.25

// keywordHintBM25 renders the column-weighted BM25 expression over the
// memory_fts columns (memory_id, revision_id, content, hints).
func keywordHintBM25() string {
	return "bm25(memory_fts, 0.0, 0.0, 1.0, " + strconv.FormatFloat(keywordHintColumnWeight, 'f', -1, 64) + ")"
}

func finiteOrReject(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// compileKeywordQuery is the single versioned keyword compiler used by every
// recall path. It delegates to the field-aware retrieval compiler.
func compileKeywordQuery(input string) (string, bool) {
	query := retrieval.CompileKeyword(input)
	if !query.Searchable {
		return "", false
	}
	return query.Match, true
}

func compileKeyword(input string) retrieval.KeywordQuery {
	return retrieval.CompileKeyword(input)
}

// keywordRecall performs bounded FTS recall with all eligibility filters applied
// before the top-k cut. It returns raw BM25, matched content, and the revision
// fields needed for temporal and quality features.
func (r *Repository) keywordRecall(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, limit int) ([]searchCandidate, error) {
	compiled := compileKeyword(params.Query)
	if !compiled.Searchable {
		return nil, nil
	}
	if compiled.Match == "" {
		return r.keywordSubstringRecall(ctx, tx, params, spaces, compiled.ShortTerms, limit)
	}
	query := `WITH ranked AS (
		SELECT f.memory_id, f.revision_id, f.content, coalesce(f.hints,'') AS hints, p.display_content, p.updated_at, p.content_hash, p.memory_type, rv.confidence,
			rv.event_time_start, rv.event_time_end, rv.time_precision, rv.time_source, rv.source_speaker, rv.language,
			rv.valid_from, rv.valid_until, ` + keywordHintBM25() + ` AS score
		FROM memory_fts f
		JOIN memory_projections p ON p.memory_id=f.memory_id AND p.revision_id=f.revision_id
		JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state
		JOIN memory_revisions rv ON rv.memory_id=f.memory_id AND rv.revision_id=f.revision_id
		WHERE memory_fts MATCH ? AND p.searchable=1`
	args := []any{compiled.Match}
	query += ` AND p.space_id IN (` + placeholders(len(spaces)) + `)`
	for _, id := range spaces {
		args = append(args, id)
	}
	query += ` AND p.current_state IN (` + placeholders(len(params.States)) + `)`
	for _, state := range params.States {
		args = append(args, state)
	}
	if params.MemoryTypesSet {
		query += ` AND p.memory_type IN (` + placeholders(len(params.MemoryTypes)) + `)`
		for _, memoryType := range params.MemoryTypes {
			args = append(args, memoryType)
		}
	}
	query += ` AND (p.valid_from IS NULL OR p.valid_from<=?) AND (p.valid_until IS NULL OR p.valid_until>?)`
	args = append(args, params.ReferenceTime, params.ReferenceTime)
	temporalSQL, temporalArgs := temporalEligibilitySQL(params)
	query += temporalSQL
	args = append(args, temporalArgs...)
	query += `) SELECT memory_id, revision_id, content, hints, display_content, updated_at, content_hash, memory_type, confidence, event_time_start, event_time_end, time_precision, time_source, source_speaker, language, valid_from, valid_until, score FROM ranked`
	query += ` ORDER BY score ASC, updated_at DESC, memory_id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrError {
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "fts5: syntax error") || strings.Contains(message, "unterminated string") || strings.Contains(message, "malformed match") {
				return nil, ErrInvalidSearchQuery
			}
		}
		return nil, err
	}
	defer rows.Close()
	var candidates []searchCandidate
	for rows.Next() {
		item, err := scanKeywordCandidate(rows)
		if err != nil {
			return nil, err
		}
		if !finiteOrReject(item.keywordRaw) {
			return nil, ErrInvalidSearchQuery
		}
		applyKeywordMatchFields(&item, compiled.Terms)
		candidates = append(candidates, item)
	}
	return candidates, rows.Err()
}

// keywordSubstringRecall serves terms too short for the trigram tokenizer (CJK
// unigram/bigram and short latin tokens) with a literal substring match.
func (r *Repository) keywordSubstringRecall(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, terms []string, limit int) ([]searchCandidate, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	clauses := make([]string, 0, len(terms))
	args := make([]any, 0, 2*len(terms)+len(spaces)+len(params.States)+4)
	for _, term := range terms {
		clauses = append(clauses, `(lower(p.display_content) LIKE '%'||?||'%' OR EXISTS (SELECT 1 FROM agent_tool_search_hints h WHERE h.revision_id=p.revision_id AND lower(h.hint) LIKE '%'||?||'%'))`)
		args = append(args, strings.ToLower(term), strings.ToLower(term))
	}
	query := `SELECT p.memory_id, p.revision_id, p.display_content, p.updated_at, p.content_hash, p.memory_type, rv.confidence,
		rv.event_time_start, rv.event_time_end, rv.time_precision, rv.time_source, rv.source_speaker, rv.language,
		rv.valid_from, rv.valid_until,
		coalesce((SELECT group_concat(h.hint, char(10)) FROM agent_tool_search_hints h WHERE h.revision_id=p.revision_id), '')
		FROM memory_projections p
		JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state
		JOIN memory_revisions rv ON rv.memory_id=p.memory_id AND rv.revision_id=p.revision_id
		WHERE p.searchable=1 AND (` + strings.Join(clauses, " OR ") + `)`
	query += ` AND p.space_id IN (` + placeholders(len(spaces)) + `)`
	args = append(args, appendStrings2(nil, spaces)...)
	query += ` AND p.current_state IN (` + placeholders(len(params.States)) + `)`
	args = append(args, appendStrings2(nil, params.States)...)
	if params.MemoryTypesSet {
		query += ` AND p.memory_type IN (` + placeholders(len(params.MemoryTypes)) + `)`
		args = append(args, appendStrings2(nil, params.MemoryTypes)...)
	}
	query += ` AND (p.valid_from IS NULL OR p.valid_from<=?) AND (p.valid_until IS NULL OR p.valid_until>?)`
	args = append(args, params.ReferenceTime, params.ReferenceTime)
	temporalSQL, temporalArgs := temporalEligibilitySQL(params)
	query += temporalSQL
	args = append(args, temporalArgs...)
	query += ` ORDER BY p.updated_at DESC, p.memory_id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []searchCandidate
	position := 0
	matchedTerms := compileKeyword(params.Query).Terms
	for rows.Next() {
		item, err := scanSubstringCandidate(rows, position)
		if err != nil {
			return nil, err
		}
		applyKeywordMatchFields(&item, matchedTerms)
		candidates = append(candidates, item)
		position++
	}
	return candidates, rows.Err()
}

func scanKeywordCandidate(rows *sql.Rows) (searchCandidate, error) {
	var item searchCandidate
	var confidence sql.NullFloat64
	var eventStart, eventEnd, timePrecision, timeSource, sourceSpeaker, language, validFrom, validUntil sql.NullString
	if err := rows.Scan(&item.memoryID, &item.revisionID, &item.ftsContent, &item.hintsContent, &item.projectionContent, &item.updatedAt, &item.contentHash, &item.kind, &confidence, &eventStart, &eventEnd, &timePrecision, &timeSource, &sourceSpeaker, &language, &validFrom, &validUntil, &item.keywordRaw); err != nil {
		return item, err
	}
	applyCandidateRevision(&item, confidence, eventStart, eventEnd, timePrecision, timeSource, sourceSpeaker, language, validFrom, validUntil)
	return item, nil
}

func scanSubstringCandidate(rows *sql.Rows, position int) (searchCandidate, error) {
	var item searchCandidate
	var confidence sql.NullFloat64
	var eventStart, eventEnd, timePrecision, timeSource, sourceSpeaker, language, validFrom, validUntil sql.NullString
	if err := rows.Scan(&item.memoryID, &item.revisionID, &item.projectionContent, &item.updatedAt, &item.contentHash, &item.kind, &confidence, &eventStart, &eventEnd, &timePrecision, &timeSource, &sourceSpeaker, &language, &validFrom, &validUntil, &item.hintsContent); err != nil {
		return item, err
	}
	item.ftsContent = item.projectionContent
	item.keywordRaw = -float64(position) - 1
	applyCandidateRevision(&item, confidence, eventStart, eventEnd, timePrecision, timeSource, sourceSpeaker, language, validFrom, validUntil)
	return item, nil
}

// applyKeywordMatchFields records which FTS field sources produced a keyword
// candidate. Hints are auxiliary, so a candidate that matched only hints is
// marked hint-only and cannot outrank a canonical content match.
func applyKeywordMatchFields(item *searchCandidate, terms []retrieval.KeywordTerm) {
	item.contentMatched = documentMatchesTerms(item.ftsContent, terms)
	item.hintMatched = documentMatchesTerms(item.hintsContent, terms)
	item.hintOnly = item.hintMatched && !item.contentMatched
}

func documentMatchesTerms(document string, terms []retrieval.KeywordTerm) bool {
	if document == "" || len(terms) == 0 {
		return false
	}
	lower := strings.ToLower(document)
	for _, term := range terms {
		if term.Text == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(term.Text)) {
			return true
		}
	}
	return false
}

func applyCandidateRevision(item *searchCandidate, confidence sql.NullFloat64, eventStart, eventEnd, timePrecision, timeSource, sourceSpeaker, language, validFrom, validUntil sql.NullString) {
	if confidence.Valid {
		item.confidence = confidence.Float64
	}
	item.eventStart = nullableTime(eventStart)
	item.eventEnd = nullableTime(eventEnd)
	item.validFrom = nullableTime(validFrom)
	item.validUntil = nullableTime(validUntil)
	item.timePrecision = timePrecision.String
	item.timeSource = timeSource.String
	item.sourceSpeaker = sourceSpeaker.String
	item.language = language.String
}

func nullableTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func temporalEligibilitySQL(params SearchParams) (string, []any) {
	predicate := ` AND (rv.event_time_start IS NULL OR rv.event_time_end IS NULL OR rv.event_time_end>rv.event_time_start)` +
		` AND (rv.valid_from IS NULL OR rv.valid_until IS NULL OR rv.valid_until>rv.valid_from)`
	if !params.QueryTimeSet || !params.QueryTime.Set {
		return predicate, nil
	}
	startExpr := `CASE WHEN rv.valid_from IS NOT NULL OR rv.valid_until IS NOT NULL THEN rv.valid_from ELSE rv.event_time_start END`
	endExpr := `CASE WHEN rv.valid_from IS NOT NULL OR rv.valid_until IS NOT NULL THEN rv.valid_until ELSE rv.event_time_end END`
	if params.Plan.QuestionType == retrieval.QuestionTemporal {
		startExpr = `CASE WHEN rv.event_time_start IS NOT NULL OR rv.event_time_end IS NOT NULL THEN rv.event_time_start ELSE rv.valid_from END`
		endExpr = `CASE WHEN rv.event_time_start IS NOT NULL OR rv.event_time_end IS NOT NULL THEN rv.event_time_end ELSE rv.valid_until END`
	}
	windowStart := params.QueryTime.Start.Add(-366 * 24 * time.Hour)
	windowEnd := params.QueryTime.End.Add(366 * 24 * time.Hour)
	predicate += ` AND ((` + startExpr + ` IS NULL AND ` + endExpr + ` IS NULL) OR (` +
		`coalesce(` + startExpr + `,` + endExpr + `)<? AND coalesce(` + endExpr + `,` + startExpr + `)>?))`
	return predicate, []any{formatTime(windowEnd), formatTime(windowStart)}
}
