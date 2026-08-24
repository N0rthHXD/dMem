package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/mattn/go-sqlite3"
)

var (
	ErrRevisionConflict       = errors.New("revision conflict")
	ErrInvalidStateTransition = errors.New("invalid state transition")
	ErrInvalidEvidence        = errors.New("invalid evidence")
)

type EvidenceParams struct {
	EventID      string
	EvidenceType string
	StartOffset  *int64
	EndOffset    *int64
}

type CreateMemoryParams struct {
	SpaceID     string
	MemoryType  string
	Content     string
	ValidFrom   *string
	ValidUntil  *string
	Sensitivity string
	Evidence    []EvidenceParams
	Reason      string
}

type RememberParams struct {
	SpaceID       string
	Content       string
	MemoryType    string
	Sensitivity   string
	ValidFrom     *string
	ValidUntil    *string
	SourceName    string
	SourceEventID *string
	OccurredAt    string
	Metadata      map[string]any
	Reason        string
}

type RememberResult struct {
	Memory  domain.Memory `json:"memory"`
	EventID string        `json:"event_id"`
}

type ReviseMemoryParams struct {
	ExpectedRevisionID string
	Content            string
	ValidFrom          *string
	ValidUntil         *string
	Evidence           []EvidenceParams
	Reason             string
}

type MemoryFilter struct {
	SpaceID          string
	IncludeAncestors bool
	State            string
	Limit            int
	Cursor           string
}

type ChangeMemoryStateParams struct {
	ExpectedRevisionID string
	Action             string
	RestoreRevisionID  *string
	Reason             string
}

func (r *Repository) CreateMemory(ctx context.Context, identity WriteIdentity, params CreateMemoryParams) (domain.Memory, bool, error) {
	if params.MemoryType == "aggregate" {
		return domain.Memory{}, false, ErrInvalidAggregateProposal
	}
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, false, err
	}
	defer tx.Rollback()
	if result, replay, err := replayMemory(ctx, tx, identity, 201); err != nil || replay {
		return result, replay, err
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.Memory{}, false, err
	}
	if err := requireSpaceAndEvidence(ctx, tx, params.SpaceID, params.Evidence); err != nil {
		return domain.Memory{}, false, err
	}
	ids, err := r.newIDs(5 + len(params.Evidence))
	if err != nil {
		return domain.Memory{}, false, err
	}
	memoryID, revisionID, stateEventID, operationID, auditID := ids[0], ids[1], ids[2], ids[3], ids[4]
	now := formatTime(r.runtime.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO memories(memory_id, space_id, memory_type, current_revision_id, current_state, frozen, sensitivity, created_at, updated_at) VALUES (?, ?, ?, ?, 'active', 0, ?, ?, ?)`, memoryID, params.SpaceID, params.MemoryType, revisionID, params.Sensitivity, now, now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := insertRevision(ctx, tx, revisionID, memoryID, nil, nil, params.Content, params.ValidFrom, params.ValidUntil, nil, identity.CallerID, r.processingVersion, nil, now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := insertEvidence(ctx, tx, revisionID, params.Evidence, ids[5:], now); err != nil {
		return domain.Memory{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id, memory_id, revision_id, operation_id, from_state, to_state, from_frozen, to_frozen, reason, created_by, created_at) VALUES (?, ?, ?, ?, NULL, 'active', NULL, 0, ?, ?, ?)`, stateEventID, memoryID, revisionID, operationID, params.Reason, identity.CallerID, now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, params.SpaceID, params.MemoryType, "active", params.Content, params.ValidFrom, params.ValidUntil, now); err != nil {
		return domain.Memory{}, false, err
	}
	result, err := getMemory(ctx, tx, memoryID, false)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if err := finishMemoryWrite(ctx, tx, identity, operationID, auditID, "create_memory", memoryID, params.Reason, 201, result, now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, false, err
	}
	return result, false, nil
}

func (r *Repository) Remember(ctx context.Context, identity WriteIdentity, params RememberParams) (RememberResult, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return RememberResult{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return RememberResult{}, false, err
	} else if found {
		var result RememberResult
		if status != 201 || json.Unmarshal([]byte(body), &result) != nil {
			return RememberResult{}, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return RememberResult{}, false, err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM memory_spaces WHERE space_id=?`, params.SpaceID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return RememberResult{}, false, ErrNotFound
	} else if err != nil {
		return RememberResult{}, false, err
	}
	ids, err := r.newIDs(8)
	if err != nil {
		return RememberResult{}, false, err
	}
	contentID, eventID, memoryID, revisionID := ids[0], ids[1], ids[2], ids[3]
	stateEventID, operationID, auditID, evidenceID := ids[4], ids[5], ids[6], ids[7]
	now := formatTime(r.runtime.Now())
	hash := sha256.Sum256([]byte(params.Content))
	metadata, err := json.Marshal(params.Metadata)
	if err != nil {
		return RememberResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO raw_contents(content_id,content_kind,content_text,media_type,content_hash,size_bytes,created_at) VALUES (?, 'text', ?, 'text/plain', ?, ?, ?)`, contentID, params.Content, hex.EncodeToString(hash[:]), len([]byte(params.Content)), now); err != nil {
		return RememberResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO raw_events(event_id,operation_id,space_id,content_id,source_type,source_name,source_event_id,caller_id,occurred_at,received_at,processing_status,metadata_json,created_at) VALUES (?,?,?,?,'manual',?,?,?,?,?,'processed',?,?)`, eventID, operationID, params.SpaceID, contentID, params.SourceName, params.SourceEventID, identity.CallerID, params.OccurredAt, now, string(metadata), now); err != nil {
		return RememberResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memories(memory_id,space_id,memory_type,current_revision_id,current_state,frozen,sensitivity,created_at,updated_at) VALUES (?,?,?,?,'active',0,?,?,?)`, memoryID, params.SpaceID, params.MemoryType, revisionID, params.Sensitivity, now, now); err != nil {
		return RememberResult{}, false, err
	}
	if err := insertRevision(ctx, tx, revisionID, memoryID, nil, nil, params.Content, params.ValidFrom, params.ValidUntil, nil, identity.CallerID, r.processingVersion, nil, now); err != nil {
		return RememberResult{}, false, err
	}
	if err := insertEvidence(ctx, tx, revisionID, []EvidenceParams{{EventID: eventID, EvidenceType: "direct_statement"}}, []string{evidenceID}, now); err != nil {
		return RememberResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id,memory_id,revision_id,operation_id,from_state,to_state,from_frozen,to_frozen,reason,created_by,created_at) VALUES (?,?,?,?,NULL,'active',NULL,0,?,?,?)`, stateEventID, memoryID, revisionID, operationID, params.Reason, identity.CallerID, now); err != nil {
		return RememberResult{}, false, err
	}
	if err := r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, params.SpaceID, params.MemoryType, "active", params.Content, params.ValidFrom, params.ValidUntil, now); err != nil {
		return RememberResult{}, false, err
	}
	memory, err := getMemory(ctx, tx, memoryID, false)
	if err != nil {
		return RememberResult{}, false, err
	}
	result := RememberResult{Memory: memory, EventID: eventID}
	body, err := json.Marshal(result)
	if err != nil {
		return RememberResult{}, false, err
	}
	if err := writeIdempotency(ctx, tx, identity, operationID, 201, body, now); err != nil {
		return RememberResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,reason,created_at) VALUES (?,?,?,?,'remember','memory',?,'succeeded',?,?)`, auditID, operationID, identity.RequestID, identity.CallerID, memoryID, params.Reason, now); err != nil {
		return RememberResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return RememberResult{}, false, err
	}
	return result, false, nil
}

func (r *Repository) ForgetMemory(ctx context.Context, identity WriteIdentity, memoryID, reason string) (domain.Memory, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, false, err
	}
	defer tx.Rollback()
	if result, replay, err := replayMemory(ctx, tx, identity, 200); err != nil || replay {
		return result, replay, err
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.Memory{}, false, err
	}
	current, err := getMemory(ctx, tx, memoryID, false)
	if err != nil {
		return domain.Memory{}, false, err
	}
	ids, err := r.newIDs(3)
	if err != nil {
		return domain.Memory{}, false, err
	}
	operationID, auditID, stateEventID := ids[0], ids[1], ids[2]
	now := formatTime(r.runtime.Now())
	if current.CurrentState != "deleted" {
		if _, err := tx.ExecContext(ctx, `UPDATE memories SET current_state='deleted',updated_at=? WHERE memory_id=?`, now, memoryID); err != nil {
			return domain.Memory{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id,memory_id,revision_id,operation_id,from_state,to_state,from_frozen,to_frozen,reason,created_by,created_at) VALUES (?,?,?,?,?,'deleted',?,?,?, ?,?)`, stateEventID, memoryID, current.CurrentRevision.RevisionID, operationID, current.CurrentState, current.Frozen, current.Frozen, reason, identity.CallerID, now); err != nil {
			return domain.Memory{}, false, err
		}
		if err := r.upsertMemoryProjection(ctx, tx, memoryID, current.CurrentRevision.RevisionID, current.SpaceID, current.MemoryType, "deleted", current.CurrentRevision.Content, current.CurrentRevision.ValidFrom, current.CurrentRevision.ValidUntil, now); err != nil {
			return domain.Memory{}, false, err
		}
		if err := r.markDependentAggregatesStale(ctx, tx, memoryID, now); err != nil {
			return domain.Memory{}, false, err
		}
	}
	result, err := getMemory(ctx, tx, memoryID, false)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if err := finishMemoryWrite(ctx, tx, identity, operationID, auditID, "forget_memory", memoryID, reason, 200, result, now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, false, err
	}
	return result, false, nil
}

func (r *Repository) ReviseMemory(ctx context.Context, identity WriteIdentity, memoryID string, params ReviseMemoryParams) (domain.Memory, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, false, err
	}
	defer tx.Rollback()
	if result, replay, err := replayMemory(ctx, tx, identity, 200); err != nil || replay {
		return result, replay, err
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.Memory{}, false, err
	}
	current, err := getMemory(ctx, tx, memoryID, false)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if current.CurrentRevision.RevisionID != params.ExpectedRevisionID {
		return domain.Memory{}, false, ErrRevisionConflict
	}
	if current.CurrentState == "deleted" {
		return domain.Memory{}, false, ErrNotFound
	}
	if current.MemoryType == "aggregate" {
		return domain.Memory{}, false, ErrInvalidAggregateProposal
	}
	if err := requireSpaceAndEvidence(ctx, tx, current.SpaceID, params.Evidence); err != nil {
		return domain.Memory{}, false, err
	}
	ids, err := r.newIDs(3 + len(params.Evidence))
	if err != nil {
		return domain.Memory{}, false, err
	}
	revisionID, operationID, auditID := ids[0], ids[1], ids[2]
	now := formatTime(r.runtime.Now())
	if err := insertRevision(ctx, tx, revisionID, memoryID, &params.ExpectedRevisionID, nil, params.Content, params.ValidFrom, params.ValidUntil, nil, identity.CallerID, r.processingVersion, nil, now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := insertEvidence(ctx, tx, revisionID, params.Evidence, ids[3:], now); err != nil {
		return domain.Memory{}, false, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE memories SET current_revision_id=?, updated_at=? WHERE memory_id=? AND current_revision_id=?`, revisionID, now, memoryID, params.ExpectedRevisionID)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return domain.Memory{}, false, err
		}
		return domain.Memory{}, false, ErrRevisionConflict
	}
	if err := r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, current.SpaceID, current.MemoryType, current.CurrentState, params.Content, params.ValidFrom, params.ValidUntil, now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := r.markDependentAggregatesStale(ctx, tx, memoryID, now); err != nil {
		return domain.Memory{}, false, err
	}
	result, err := getMemory(ctx, tx, memoryID, false)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if err := finishMemoryWrite(ctx, tx, identity, operationID, auditID, "revise_memory", memoryID, params.Reason, 200, result, now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, false, err
	}
	return result, false, nil
}

func (r *Repository) ChangeMemoryState(ctx context.Context, identity WriteIdentity, memoryID string, params ChangeMemoryStateParams) (domain.Memory, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, false, err
	}
	defer tx.Rollback()
	if result, replay, err := replayMemory(ctx, tx, identity, 200); err != nil || replay {
		return result, replay, err
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.Memory{}, false, err
	}
	current, err := getMemory(ctx, tx, memoryID, false)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if current.CurrentRevision.RevisionID != params.ExpectedRevisionID {
		return domain.Memory{}, false, ErrRevisionConflict
	}
	toState, toFrozen, noop, err := lifecycleTarget(current.CurrentState, current.Frozen, params.Action)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if params.Action == "restore" && !noop && params.RestoreRevisionID == nil {
		return domain.Memory{}, false, ErrInvalidStateTransition
	}
	if current.MemoryType == "aggregate" && params.Action == "restore" && !noop {
		return domain.Memory{}, false, ErrInvalidAggregateProposal
	}
	idCount := 2
	if !noop {
		idCount++
	}
	if params.Action == "restore" && !noop {
		idCount++
	}
	ids, err := r.newIDs(idCount)
	if err != nil {
		return domain.Memory{}, false, err
	}
	operationID, auditID := ids[0], ids[1]
	now := formatTime(r.runtime.Now())
	revisionID := current.CurrentRevision.RevisionID
	content, validFrom, validUntil := current.CurrentRevision.Content, current.CurrentRevision.ValidFrom, current.CurrentRevision.ValidUntil
	if params.Action == "restore" && !noop {
		source, err := getRevision(ctx, tx, memoryID, *params.RestoreRevisionID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return domain.Memory{}, false, ErrInvalidStateTransition
			}
			return domain.Memory{}, false, err
		}
		revisionID = ids[3]
		content, validFrom, validUntil = source.Content, source.ValidFrom, source.ValidUntil
		if err := insertRevision(ctx, tx, revisionID, memoryID, &params.ExpectedRevisionID, params.RestoreRevisionID, content, validFrom, validUntil, source.Confidence, identity.CallerID, r.processingVersion, nil, now); err != nil {
			return domain.Memory{}, false, err
		}
		evidence, err := revisionEvidence(ctx, tx, source.RevisionID)
		if err != nil {
			return domain.Memory{}, false, err
		}
		evidenceIDs, err := r.newIDs(len(evidence))
		if err != nil {
			return domain.Memory{}, false, err
		}
		if err := insertEvidence(ctx, tx, revisionID, evidenceParams(evidence), evidenceIDs, now); err != nil {
			return domain.Memory{}, false, err
		}
	}
	if !noop {
		stateEventID := ids[2]
		updated, err := tx.ExecContext(ctx, `UPDATE memories SET current_revision_id=?, current_state=?, frozen=?, updated_at=? WHERE memory_id=? AND current_revision_id=?`, revisionID, toState, toFrozen, now, memoryID, params.ExpectedRevisionID)
		if err != nil {
			return domain.Memory{}, false, err
		}
		if count, err := updated.RowsAffected(); err != nil || count != 1 {
			if err != nil {
				return domain.Memory{}, false, err
			}
			return domain.Memory{}, false, ErrRevisionConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id, memory_id, revision_id, operation_id, from_state, to_state, from_frozen, to_frozen, reason, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, stateEventID, memoryID, revisionID, operationID, current.CurrentState, toState, current.Frozen, toFrozen, params.Reason, identity.CallerID, now); err != nil {
			return domain.Memory{}, false, err
		}
		if err := r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, current.SpaceID, current.MemoryType, toState, content, validFrom, validUntil, now); err != nil {
			return domain.Memory{}, false, err
		}
		if err := r.markDependentAggregatesStale(ctx, tx, memoryID, now); err != nil {
			return domain.Memory{}, false, err
		}
	}
	result, err := getMemory(ctx, tx, memoryID, false)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if err := finishMemoryWrite(ctx, tx, identity, operationID, auditID, params.Action+"_memory", memoryID, params.Reason, 200, result, now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, false, err
	}
	return result, false, nil
}

func lifecycleTarget(state string, frozen bool, action string) (string, bool, bool, error) {
	if state == "deleted" && action != "delete" && action != "restore" {
		return "", false, false, ErrInvalidStateTransition
	}
	switch action {
	case "confirm":
		if state == "active" {
			return state, frozen, true, nil
		}
		if state != "candidate" && state != "contested" {
			return "", false, false, ErrInvalidStateTransition
		}
		return "active", frozen, false, nil
	case "contest":
		if state == "contested" {
			return state, frozen, true, nil
		}
		if state != "candidate" && state != "active" {
			return "", false, false, ErrInvalidStateTransition
		}
		return "contested", frozen, false, nil
	case "archive":
		if state == "archived" {
			return state, frozen, true, nil
		}
		if state != "active" && state != "contested" {
			return "", false, false, ErrInvalidStateTransition
		}
		return "archived", frozen, false, nil
	case "restore":
		if state == "active" {
			return state, frozen, true, nil
		}
		if state != "archived" && state != "expired" && state != "retracted" && state != "deleted" {
			return "", false, false, ErrInvalidStateTransition
		}
		return "active", frozen, false, nil
	case "freeze":
		if frozen {
			return state, frozen, true, nil
		}
		return state, true, false, nil
	case "unfreeze":
		if !frozen {
			return state, frozen, true, nil
		}
		return state, false, false, nil
	case "retract":
		if state == "retracted" {
			return state, frozen, true, nil
		}
		if state != "active" && state != "contested" && state != "archived" {
			return "", false, false, ErrInvalidStateTransition
		}
		return "retracted", frozen, false, nil
	case "delete":
		if state == "deleted" {
			return state, frozen, true, nil
		}
		return "deleted", frozen, false, nil
	default:
		return "", false, false, ErrInvalidStateTransition
	}
}

func (r *Repository) GetMemory(ctx context.Context, memoryID string) (domain.Memory, error) {
	return getMemory(ctx, r.store.reader, memoryID, true)
}

func (r *Repository) ListMemories(ctx context.Context, filter MemoryFilter) (domain.MemoryPage, error) {
	tx, err := r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.MemoryPage{}, err
	}
	defer tx.Rollback()
	spaces, err := memoryScope(ctx, tx, filter.SpaceID, filter.IncludeAncestors)
	if err != nil {
		return domain.MemoryPage{}, err
	}
	query := `SELECT memory_id FROM memories WHERE space_id IN (` + placeholders(len(spaces)) + `)`
	args := make([]any, len(spaces))
	for i := range spaces {
		args[i] = spaces[i]
	}
	if filter.State != "" {
		query += ` AND current_state=?`
		args = append(args, filter.State)
	} else {
		query += ` AND current_state NOT IN ('retracted','deleted')`
	}
	if filter.Cursor != "" {
		var cursor struct{ Time, ID string }
		decoded, err := base64.RawURLEncoding.DecodeString(filter.Cursor)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Time == "" || cursor.ID == "" {
			return domain.MemoryPage{}, ErrInvalidCursor
		}
		query += ` AND (updated_at < ? OR (updated_at = ? AND memory_id < ?))`
		args = append(args, cursor.Time, cursor.Time, cursor.ID)
	}
	query += ` ORDER BY updated_at DESC, memory_id DESC LIMIT ?`
	args = append(args, filter.Limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.MemoryPage{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return domain.MemoryPage{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.MemoryPage{}, err
	}
	if err := rows.Close(); err != nil {
		return domain.MemoryPage{}, err
	}
	page := domain.MemoryPage{Items: []domain.Memory{}}
	for _, id := range ids {
		item, err := getMemory(ctx, tx, id, false)
		if err != nil {
			return domain.MemoryPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		encoded, _ := json.Marshal(struct{ Time, ID string }{last.UpdatedAt, last.MemoryID})
		next := base64.RawURLEncoding.EncodeToString(encoded)
		page.NextCursor, page.Items = &next, page.Items[:filter.Limit]
	}
	if err := tx.Commit(); err != nil {
		return domain.MemoryPage{}, err
	}
	return page, nil
}

func (r *Repository) ListMemoryRevisions(ctx context.Context, memoryID string) ([]domain.MemoryRevision, error) {
	if err := memoryExists(ctx, r.store.reader, memoryID); err != nil {
		return nil, err
	}
	rows, err := r.store.reader.QueryContext(ctx, revisionSelect+` WHERE memory_id=? ORDER BY created_at DESC, revision_id DESC`, memoryID)
	if err != nil {
		return nil, err
	}
	var result []domain.MemoryRevision
	for rows.Next() {
		revision, err := scanRevision(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, revision)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range result {
		result[i].Evidence, err = revisionEvidence(ctx, r.store.reader, result[i].RevisionID)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (r *Repository) ListMemoryStateEvents(ctx context.Context, memoryID string) ([]domain.MemoryStateEvent, error) {
	if err := memoryExists(ctx, r.store.reader, memoryID); err != nil {
		return nil, err
	}
	rows, err := r.store.reader.QueryContext(ctx, `SELECT state_event_id, memory_id, revision_id, operation_id, from_state, to_state, from_frozen, to_frozen, reason, created_by, created_at FROM memory_state_events WHERE memory_id=? ORDER BY created_at DESC, state_event_id DESC`, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.MemoryStateEvent{}
	for rows.Next() {
		var item domain.MemoryStateEvent
		var fromState sql.NullString
		var fromFrozen sql.NullInt64
		var toFrozen int64
		if err := rows.Scan(&item.StateEventID, &item.MemoryID, &item.RevisionID, &item.OperationID, &fromState, &item.ToState, &fromFrozen, &toFrozen, &item.Reason, &item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		if fromState.Valid {
			item.FromState = &fromState.String
		}
		if fromFrozen.Valid {
			value := fromFrozen.Int64 != 0
			item.FromFrozen = &value
		}
		item.ToFrozen = toFrozen != 0
		result = append(result, item)
	}
	return result, rows.Err()
}

type memoryQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getMemory(ctx context.Context, db memoryQueryer, memoryID string, hideDeleted bool) (domain.Memory, error) {
	query := `SELECT m.memory_id, m.space_id, m.memory_type, m.current_state, m.frozen, m.sensitivity, m.created_at, m.updated_at, r.revision_id, r.memory_id, r.parent_revision_id, r.content, r.valid_from, r.valid_until, r.confidence, r.created_by, r.created_at FROM memories m JOIN memory_revisions r ON r.revision_id=m.current_revision_id WHERE m.memory_id=?`
	if hideDeleted {
		query += ` AND m.current_state <> 'deleted'`
	}
	var result domain.Memory
	var frozen int64
	err := db.QueryRowContext(ctx, query, memoryID).Scan(&result.MemoryID, &result.SpaceID, &result.MemoryType, &result.CurrentState, &frozen, &result.Sensitivity, &result.CreatedAt, &result.UpdatedAt, &result.CurrentRevision.RevisionID, &result.CurrentRevision.MemoryID, &result.CurrentRevision.ParentRevisionID, &result.CurrentRevision.Content, &result.CurrentRevision.ValidFrom, &result.CurrentRevision.ValidUntil, &result.CurrentRevision.Confidence, &result.CurrentRevision.CreatedBy, &result.CurrentRevision.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Memory{}, ErrNotFound
	}
	if err != nil {
		return domain.Memory{}, err
	}
	result.Frozen = frozen != 0
	evidence, err := revisionEvidence(ctx, db, result.CurrentRevision.RevisionID)
	if err != nil {
		return domain.Memory{}, err
	}
	result.CurrentRevision.Evidence = evidence
	result.ConflictRefs, err = conflictRefs(ctx, db, result.MemoryID, result.CurrentRevision.RevisionID)
	if err != nil {
		return domain.Memory{}, err
	}
	if result.MemoryType == "aggregate" {
		var state, reasons string
		if err = db.QueryRowContext(ctx, `SELECT f.state,f.stale_reasons_json FROM aggregate_descriptors d JOIN aggregate_freshness f ON f.aggregate_id=d.aggregate_id WHERE d.memory_id=?`, memoryID).Scan(&state, &reasons); err != nil {
			return domain.Memory{}, err
		}
		result.AggregateStale = state == "stale"
		result.AggregateStaleReasons = []string{}
		if err = json.Unmarshal([]byte(reasons), &result.AggregateStaleReasons); err != nil {
			return domain.Memory{}, err
		}
		rows, queryErr := db.QueryContext(ctx, `SELECT relation_id FROM memory_relations WHERE source_memory_id=? AND source_revision_id=? AND relation_type='derived_from' ORDER BY relation_id`, memoryID, result.CurrentRevision.RevisionID)
		if queryErr != nil {
			return domain.Memory{}, queryErr
		}
		for rows.Next() {
			var relationID string
			if queryErr = rows.Scan(&relationID); queryErr != nil {
				rows.Close()
				return domain.Memory{}, queryErr
			}
			result.AggregateProvenanceRefs = append(result.AggregateProvenanceRefs, relationID)
		}
		if queryErr = rows.Close(); queryErr != nil {
			return domain.Memory{}, queryErr
		}
	}
	return result, nil
}

const revisionSelect = `SELECT revision_id, memory_id, parent_revision_id, content, valid_from, valid_until, confidence, created_by, created_at FROM memory_revisions`

func getRevision(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, memoryID, revisionID string) (domain.MemoryRevision, error) {
	return scanRevision(db.QueryRowContext(ctx, revisionSelect+` WHERE memory_id=? AND revision_id=?`, memoryID, revisionID))
}

func scanRevision(row rowScanner) (domain.MemoryRevision, error) {
	var result domain.MemoryRevision
	err := row.Scan(&result.RevisionID, &result.MemoryID, &result.ParentRevisionID, &result.Content, &result.ValidFrom, &result.ValidUntil, &result.Confidence, &result.CreatedBy, &result.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MemoryRevision{}, ErrNotFound
	}
	return result, err
}

func revisionEvidence(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, revisionID string) ([]domain.MemoryEvidence, error) {
	rows, err := db.QueryContext(ctx, `SELECT event_id, evidence_type, start_offset, end_offset FROM memory_evidence WHERE revision_id=? ORDER BY evidence_id ASC`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.MemoryEvidence{}
	for rows.Next() {
		var item domain.MemoryEvidence
		if err := rows.Scan(&item.EventID, &item.EvidenceType, &item.StartOffset, &item.EndOffset); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func requireSpaceAndEvidence(ctx context.Context, tx *sql.Tx, spaceID string, evidence []EvidenceParams) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM memory_spaces WHERE space_id=?`, spaceID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return requireEvidence(ctx, tx, spaceID, evidence)
}

func requireEvidence(ctx context.Context, tx *sql.Tx, spaceID string, evidence []EvidenceParams) error {
	if len(evidence) == 0 {
		return ErrInvalidEvidence
	}
	allowedSpaces, err := memoryScope(ctx, tx, spaceID, true)
	if err != nil {
		return err
	}
	for _, item := range evidence {
		var eventSpace, contentKind string
		var contentText sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT e.space_id, c.content_kind, c.content_text FROM raw_events e JOIN raw_contents c ON c.content_id=e.content_id WHERE e.event_id=?`, item.EventID).Scan(&eventSpace, &contentKind, &contentText)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidEvidence
		} else if err != nil {
			return err
		}
		if !contains(allowedSpaces, eventSpace) {
			return ErrInvalidEvidence
		}
		paired := item.StartOffset != nil && item.EndOffset != nil
		if (item.StartOffset == nil) != (item.EndOffset == nil) || contentKind == "reference" && paired {
			return ErrInvalidEvidence
		}
		if paired && (*item.StartOffset < 0 || *item.EndOffset <= *item.StartOffset || *item.EndOffset > int64(utf8.RuneCountInString(contentText.String))) {
			return ErrInvalidEvidence
		}
	}
	return nil
}

func insertRevision(ctx context.Context, tx *sql.Tx, revisionID, memoryID string, parentID, restoredFrom *string, content string, validFrom, validUntil *string, confidence *float64, callerID, processingVersion string, processorIdentity *string, now string) error {
	hash := sha256.Sum256([]byte(content))
	_, err := tx.ExecContext(ctx, `INSERT INTO memory_revisions(revision_id, memory_id, parent_revision_id, restored_from_revision_id, content, content_hash, valid_from, valid_until, confidence, created_by, processing_version, processor_identity, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, revisionID, memoryID, parentID, restoredFrom, content, hex.EncodeToString(hash[:]), validFrom, validUntil, confidence, callerID, processingVersion, processorIdentity, now)
	return err
}

func insertEvidence(ctx context.Context, tx *sql.Tx, revisionID string, evidence []EvidenceParams, ids []string, now string) error {
	for i, item := range evidence {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_evidence(evidence_id, revision_id, event_id, evidence_type, start_offset, end_offset, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, ids[i], revisionID, item.EventID, item.EvidenceType, item.StartOffset, item.EndOffset, now); err != nil {
			var sqliteErr sqlite3.Error
			if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
				return fmt.Errorf("%w: evidence rejected", ErrInvalidEvidence)
			}
			return err
		}
	}
	return nil
}

func (r *Repository) upsertMemoryProjection(ctx context.Context, tx *sql.Tx, memoryID, revisionID, spaceID, memoryType, state, content string, validFrom, validUntil *string, now string) error {
	searchable := state == "active" || state == "contested"
	hash := sha256.Sum256([]byte(content))
	contentHash := hex.EncodeToString(hash[:])
	semanticChanged := true
	var previousRevision, previousSpace, previousHash string
	var previousSearchable bool
	err := tx.QueryRowContext(ctx, `SELECT revision_id, space_id, content_hash, searchable FROM memory_projections WHERE memory_id=?`, memoryID).Scan(&previousRevision, &previousSpace, &previousHash, &previousSearchable)
	if err == nil {
		semanticChanged = previousRevision != revisionID || previousSpace != spaceID || previousHash != contentHash || previousSearchable != searchable
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	projectionSQL := `INSERT INTO memory_projections(memory_id, revision_id, space_id, memory_type, current_state, searchable, display_content, content_hash, embedding_state, valid_from, valid_until, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'stale', ?, ?, ?) ON CONFLICT(memory_id) DO UPDATE SET revision_id=excluded.revision_id, space_id=excluded.space_id, memory_type=excluded.memory_type, current_state=excluded.current_state, searchable=excluded.searchable, display_content=excluded.display_content, content_hash=excluded.content_hash, valid_from=excluded.valid_from, valid_until=excluded.valid_until, updated_at=excluded.updated_at`
	if semanticChanged {
		projectionSQL += `, embedding_state='stale', embedding_provider=NULL, embedding_model=NULL, embedding_dimension=NULL, embedding_content_hash=NULL`
	}
	if _, err := tx.ExecContext(ctx, projectionSQL, memoryID, revisionID, spaceID, memoryType, state, searchable, content, contentHash, validFrom, validUntil, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE memory_id=?`, memoryID); err != nil {
		return err
	}
	if searchable {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_fts(memory_id, revision_id, content) VALUES (?, ?, ?)`, memoryID, revisionID, content); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE index_metadata SET source_revision_count=(SELECT count(*) FROM memory_projections WHERE searchable=1), updated_at=? WHERE index_name='memory_fts'`, now); err != nil {
		return err
	}
	if err := advanceCorpusGeneration(ctx, tx, now); err != nil {
		return err
	}
	if !semanticChanged {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE semantic_index_metadata SET state=CASE WHEN active_version IS NULL THEN 'absent' ELSE 'stale' END, updated_at=? WHERE singleton=1`, now); err != nil {
		return err
	}
	if r.embeddingProvider == nil {
		return nil
	}
	if searchable {
		ids, err := r.newIDs(2)
		if err != nil {
			return err
		}
		status := "pending"
		var depth int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE status IN ('pending','retrying','running')`).Scan(&depth); err != nil {
			return err
		}
		if depth >= r.queueSoftLimit {
			status = "waiting"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES (?, ?, 'embed_memory', 'memory_revision', ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, ids[0], ids[1], revisionID, r.embeddingIdentity(), status, r.maxAttempts, now, now); err != nil {
			return err
		}
	}
	if r.vectorIndex == nil {
		return nil
	}
	_, err = r.enqueueSemanticRebuild(ctx, tx, now)
	return err
}

func replayMemory(ctx context.Context, tx *sql.Tx, identity WriteIdentity, expectedStatus int) (domain.Memory, bool, error) {
	status, body, found, err := readIdempotency(ctx, tx, identity)
	if err != nil || !found {
		return domain.Memory{}, false, err
	}
	var result domain.Memory
	if status != expectedStatus || json.Unmarshal([]byte(body), &result) != nil {
		return domain.Memory{}, false, errors.New("invalid stored idempotency response")
	}
	return result, true, nil
}

func finishMemoryWrite(ctx context.Context, tx *sql.Tx, identity WriteIdentity, operationID, auditID, action, memoryID, reason string, status int, result domain.Memory, now string) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := writeIdempotency(ctx, tx, identity, operationID, status, body, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, reason, created_at) VALUES (?, ?, ?, ?, ?, 'memory', ?, 'succeeded', ?, ?)`, auditID, operationID, identity.RequestID, identity.CallerID, action, memoryID, reason, now)
	return err
}

func memoryExists(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, memoryID string) error {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT 1 FROM memories WHERE memory_id=?`, memoryID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else {
		return err
	}
}

func memoryScope(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, spaceID string, ancestors bool) ([]string, error) {
	var parent sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT parent_space_id FROM memory_spaces WHERE space_id=?`, spaceID).Scan(&parent); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	result := []string{spaceID}
	if !ancestors {
		return result, nil
	}
	for parent.Valid {
		result = append(result, parent.String)
		if err := db.QueryRowContext(ctx, `SELECT parent_space_id FROM memory_spaces WHERE space_id=?`, parent.String).Scan(&parent); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func evidenceParams(items []domain.MemoryEvidence) []EvidenceParams {
	result := make([]EvidenceParams, len(items))
	for i, item := range items {
		result[i] = EvidenceParams{EventID: item.EventID, EvidenceType: item.EvidenceType, StartOffset: item.StartOffset, EndOffset: item.EndOffset}
	}
	return result
}

func (r *Repository) newIDs(count int) ([]string, error) {
	result := make([]string, count)
	for i := range result {
		id, err := r.runtime.ID()
		if err != nil {
			return nil, err
		}
		result[i] = id
	}
	return result, nil
}

func placeholders(count int) string {
	result := "?"
	for i := 1; i < count; i++ {
		result += ",?"
	}
	return result
}
