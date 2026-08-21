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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/mattn/go-sqlite3"
)

type SearchParams struct {
	Query            string
	SpaceIDs         []string
	IncludeAncestors bool
	MemoryTypes      []string
	MemoryTypesSet   bool
	States           []string
	ReferenceTime    string
	ReferenceTimeSet bool
	Mode             string
	Limit            int
	MaxChars         int
	IncludeEvidence  bool
	Cursor           string
}

type searchCursor struct {
	Version       int    `json:"v"`
	QueryHash     string `json:"q"`
	ScoreBits     string `json:"s"`
	UpdatedAt     string `json:"u"`
	MemoryID      string `json:"m"`
	ReferenceTime string `json:"rt"`
}

type searchCandidate struct {
	memoryID, revisionID, updatedAt string
	ftsContent, projectionContent   string
	score                           float64
}

func (r *Repository) SearchMemories(ctx context.Context, params SearchParams) (domain.SearchResponse, error) {
	if params.ReferenceTime == "" {
		params.ReferenceTime = formatTime(r.runtime.Now())
	}
	tx, err := r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.SearchResponse{}, err
	}
	defer tx.Rollback()

	var indexState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM index_metadata WHERE index_name='memory_fts'`).Scan(&indexState); err != nil {
		return domain.SearchResponse{}, err
	}
	spaces, err := searchScope(ctx, tx, params.SpaceIDs, params.IncludeAncestors)
	if err != nil {
		return domain.SearchResponse{}, err
	}
	var after *searchCursor
	if params.Cursor != "" {
		cursor, err := decodeSearchCursor(params.Cursor)
		if err != nil {
			return domain.SearchResponse{}, ErrInvalidCursor
		}
		if !params.ReferenceTimeSet {
			params.ReferenceTime = cursor.ReferenceTime
		}
		after = &cursor
	}
	hash := searchQueryHash(params)
	if after != nil && (after.QueryHash != hash || after.ReferenceTime != params.ReferenceTime) {
		return domain.SearchResponse{}, ErrInvalidCursor
	}
	response := domain.SearchResponse{Results: []domain.SearchResult{}, IndexState: indexState}
	if len(params.States) == 0 || params.MemoryTypesSet && len(params.MemoryTypes) == 0 {
		if err := tx.Commit(); err != nil {
			return domain.SearchResponse{}, err
		}
		return response, nil
	}

	query := `WITH ranked AS (
		SELECT f.memory_id, f.revision_id, f.content, p.display_content, p.updated_at, bm25(memory_fts) AS score
		FROM memory_fts f
		JOIN memory_projections p ON p.memory_id=f.memory_id AND p.revision_id=f.revision_id
		JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state
		WHERE memory_fts MATCH ? AND p.searchable=1`
	args := []any{params.Query}
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
	query += `) SELECT memory_id, revision_id, content, display_content, updated_at, score FROM ranked`
	if after != nil {
		score := math.Float64frombits(parseScoreBits(after.ScoreBits))
		query += ` WHERE score>? OR (score=? AND updated_at<?) OR (score=? AND updated_at=? AND memory_id>?)`
		args = append(args, score, score, after.UpdatedAt, score, after.UpdatedAt, after.MemoryID)
	}
	query += ` ORDER BY score ASC, updated_at DESC, memory_id ASC`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrError {
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "fts5: syntax error") || strings.Contains(message, "unterminated string") || strings.Contains(message, "malformed match") {
				return domain.SearchResponse{}, ErrInvalidSearchQuery
			}
			r.markFTSDegraded()
			return domain.SearchResponse{}, ErrIndexDegraded
		}
		return domain.SearchResponse{}, err
	}
	var candidates []searchCandidate
	for rows.Next() {
		var item searchCandidate
		if err := rows.Scan(&item.memoryID, &item.revisionID, &item.ftsContent, &item.projectionContent, &item.updatedAt, &item.score); err != nil {
			rows.Close()
			return domain.SearchResponse{}, err
		}
		if math.IsNaN(item.score) || math.IsInf(item.score, 0) {
			rows.Close()
			return domain.SearchResponse{}, ErrInvalidSearchQuery
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return domain.SearchResponse{}, err
	}

	var lastInspected *searchCandidate
	hasMore := false
	returned := make(map[string]struct{})
	for i := range candidates {
		candidate := candidates[i]
		if _, duplicate := returned[candidate.memoryID]; duplicate {
			continue
		}
		if len(response.Results) == params.Limit {
			memory, err := getMemory(ctx, tx, candidate.memoryID, false)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return domain.SearchResponse{}, err
			}
			if err == nil && searchCandidateCurrent(memory, candidate, spaces, params) {
				hasMore = true
				response.Truncated = true
				break
			}
			continue
		}
		memory, err := getMemory(ctx, tx, candidate.memoryID, false)
		if errors.Is(err, ErrNotFound) {
			lastInspected = &candidate
			continue
		}
		if err != nil {
			return domain.SearchResponse{}, err
		}
		if !searchCandidateCurrent(memory, candidate, spaces, params) {
			lastInspected = &candidate
			continue
		}
		chars := utf8.RuneCountInString(memory.CurrentRevision.Content)
		if response.CharsUsed+chars > params.MaxChars {
			response.Truncated = true
			if len(response.Results) > 0 && chars <= params.MaxChars {
				hasMore = true
				break
			}
			lastInspected = &candidate
			continue
		}
		lastInspected = &candidate
		if !params.IncludeEvidence {
			memory.CurrentRevision.Evidence = nil
		}
		response.Results = append(response.Results, domain.SearchResult{Memory: memory, Rank: len(response.Results) + 1, KeywordScore: candidate.score, SelectionReason: "keyword_match"})
		returned[memory.MemoryID] = struct{}{}
		response.CharsUsed += chars
	}
	if hasMore && lastInspected != nil {
		next := encodeSearchCursor(hash, params.ReferenceTime, *lastInspected)
		response.NextCursor = &next
	}
	if err := tx.Commit(); err != nil {
		return domain.SearchResponse{}, err
	}
	return response, nil
}

func searchCandidateCurrent(memory domain.Memory, candidate searchCandidate, spaces []string, params SearchParams) bool {
	return memory.CurrentRevision.RevisionID == candidate.revisionID &&
		memory.CurrentRevision.Content == candidate.ftsContent &&
		memory.CurrentRevision.Content == candidate.projectionContent &&
		searchMemoryCurrent(memory, spaces, params)
}

func searchScope(ctx context.Context, tx *sql.Tx, starts []string, ancestors bool) ([]string, error) {
	set := make(map[string]struct{})
	for _, start := range starts {
		ids, err := memoryScope(ctx, tx, start, ancestors)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			set[id] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func searchMemoryCurrent(memory domain.Memory, spaces []string, params SearchParams) bool {
	if !contains(spaces, memory.SpaceID) || !contains(params.States, memory.CurrentState) {
		return false
	}
	if memory.CurrentState != "active" && memory.CurrentState != "contested" {
		return false
	}
	if params.MemoryTypesSet && !contains(params.MemoryTypes, memory.MemoryType) {
		return false
	}
	reference, err := time.Parse(time.RFC3339Nano, params.ReferenceTime)
	if err != nil {
		return false
	}
	if memory.CurrentRevision.ValidFrom != nil {
		from, err := time.Parse(time.RFC3339Nano, *memory.CurrentRevision.ValidFrom)
		if err != nil || reference.Before(from) {
			return false
		}
	}
	if memory.CurrentRevision.ValidUntil != nil {
		until, err := time.Parse(time.RFC3339Nano, *memory.CurrentRevision.ValidUntil)
		if err != nil || !reference.Before(until) {
			return false
		}
	}
	return true
}

func searchQueryHash(params SearchParams) string {
	spaces, types, states := append([]string(nil), params.SpaceIDs...), append([]string(nil), params.MemoryTypes...), append([]string(nil), params.States...)
	sort.Strings(spaces)
	sort.Strings(types)
	sort.Strings(states)
	canonical := struct {
		Version   int      `json:"v"`
		Query     string   `json:"query"`
		Spaces    []string `json:"space_ids"`
		Ancestors bool     `json:"include_ancestors"`
		Types     []string `json:"memory_types"`
		TypesSet  bool     `json:"memory_types_set"`
		States    []string `json:"states"`
		Reference string   `json:"reference_time"`
		Mode      string   `json:"mode"`
		Limit     int      `json:"limit"`
		MaxChars  int      `json:"max_chars"`
		Evidence  bool     `json:"include_evidence"`
	}{1, params.Query, spaces, params.IncludeAncestors, types, params.MemoryTypesSet, states, params.ReferenceTime, params.Mode, params.Limit, params.MaxChars, params.IncludeEvidence}
	encoded, _ := json.Marshal(canonical)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func encodeSearchCursor(hash, reference string, candidate searchCandidate) string {
	value := searchCursor{Version: 1, QueryHash: hash, ScoreBits: strconv.FormatUint(math.Float64bits(candidate.score), 16), UpdatedAt: candidate.updatedAt, MemoryID: candidate.memoryID, ReferenceTime: reference}
	encoded, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeSearchCursor(value string) (searchCursor, error) {
	var cursor searchCursor
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 1 || cursor.QueryHash == "" || cursor.UpdatedAt == "" || cursor.MemoryID == "" || cursor.ReferenceTime == "" {
		return searchCursor{}, ErrInvalidCursor
	}
	bits, err := strconv.ParseUint(cursor.ScoreBits, 16, 64)
	if err != nil || math.IsNaN(math.Float64frombits(bits)) || math.IsInf(math.Float64frombits(bits), 0) {
		return searchCursor{}, ErrInvalidCursor
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.UpdatedAt); err != nil {
		return searchCursor{}, ErrInvalidCursor
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.ReferenceTime); err != nil {
		return searchCursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

func parseScoreBits(value string) uint64 {
	bits, _ := strconv.ParseUint(value, 16, 64)
	return bits
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
