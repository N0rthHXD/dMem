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
	"strings"
	"time"
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
	EventID     string
	Role        domain.EvidenceRole
	StartOffset *int64
	EndOffset   *int64
}

type CreateMemoryParams struct {
	SpaceID     string
	Kind        string
	Content     string
	ValidFrom   *string
	ValidUntil  *string
	Sensitivity string
	Evidence    []EvidenceParams
	Reason      string
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
	if domain.ValidAggregateKind(params.Kind) || params.Kind == "aggregate" {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO memories(memory_id, space_id, current_revision_id, current_state, frozen, sensitivity, created_at, updated_at) VALUES (?, ?, ?, 'active', 0, ?, ?, ?)`, memoryID, params.SpaceID, revisionID, params.Sensitivity, now, now); err != nil {
		return domain.Memory{}, false, err
	}
	revision, err := revisionFromTextInput(revisionID, memoryID, nil, nil, params.Kind, params.Content, params.ValidFrom, params.ValidUntil, nil, params.Sensitivity, identity.CallerID, r.processingVersion(), nil, now)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if err := validateDirectRevision(revision, params.Evidence); err != nil {
		return domain.Memory{}, false, err
	}
	if err := insertRevision(ctx, tx, revision); err != nil {
		return domain.Memory{}, false, err
	}
	if err := insertEvidence(ctx, tx, revisionID, params.Evidence, ids[5:], now); err != nil {
		return domain.Memory{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id, memory_id, revision_id, operation_id, from_state, to_state, from_frozen, to_frozen, reason, created_by, created_at) VALUES (?, ?, ?, ?, NULL, 'active', NULL, 0, ?, ?, ?)`, stateEventID, memoryID, revisionID, operationID, params.Reason, identity.CallerID, now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, params.SpaceID, "active", now); err != nil {
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
	if isAggregateMemory(ctx, tx, current.MemoryID) {
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
	revision, err := revisionFromTextInput(revisionID, memoryID, &params.ExpectedRevisionID, nil, string(current.Kind), params.Content, params.ValidFrom, params.ValidUntil, nil, current.Sensitivity, identity.CallerID, r.processingVersion(), nil, now)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if err := validateDirectRevision(revision, params.Evidence); err != nil {
		return domain.Memory{}, false, err
	}
	if err := insertRevision(ctx, tx, revision); err != nil {
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
	if err := r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, current.SpaceID, current.CurrentState, now); err != nil {
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
	if isAggregateMemory(ctx, tx, current.MemoryID) && params.Action == "restore" && !noop {
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
	revision := current.CurrentRevision
	if params.Action == "restore" && !noop {
		source, err := getRevision(ctx, tx, memoryID, *params.RestoreRevisionID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return domain.Memory{}, false, ErrInvalidStateTransition
			}
			return domain.Memory{}, false, err
		}
		revisionID = ids[3]
		revision = source
		revision.RevisionID = revisionID
		revision.ParentRevisionID = &params.ExpectedRevisionID
		revision.RestoredFromRevisionID = params.RestoreRevisionID
		revision.CreatedBy = identity.CallerID
		revision.ProcessingVersion = r.processingVersion()
		revision.ProcessorIdentity = ""
		revision.CreatedAt = now
		if err := insertRevision(ctx, tx, revision); err != nil {
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
		if err := r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, current.SpaceID, toState, now); err != nil {
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
		if state != "contested" {
			return "", false, false, ErrInvalidStateTransition
		}
		return "active", frozen, false, nil
	case "contest":
		if state == "contested" {
			return state, frozen, true, nil
		}
		if state != "active" {
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
		if err = loadRevisionDetails(ctx, r.store.reader, &result[i]); err != nil {
			return nil, err
		}
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
	query := `SELECT m.memory_id, m.space_id, m.current_state, m.frozen, m.sensitivity, m.created_at, m.updated_at, ` + revisionColumns("r") + ` FROM memories m JOIN memory_revisions r ON r.revision_id=m.current_revision_id WHERE m.memory_id=?`
	if hideDeleted {
		query += ` AND m.current_state <> 'deleted'`
	}
	var result domain.Memory
	var frozen int64
	err := scanMemoryRow(db.QueryRowContext(ctx, query, memoryID), &result, &frozen)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Memory{}, ErrNotFound
	}
	if err != nil {
		return domain.Memory{}, err
	}
	result.Frozen = frozen != 0
	if err := loadRevisionDetails(ctx, db, &result.CurrentRevision); err != nil {
		return domain.Memory{}, err
	}
	result.Kind = result.CurrentRevision.Kind
	evidence, err := revisionEvidence(ctx, db, result.CurrentRevision.RevisionID)
	if err != nil {
		return domain.Memory{}, err
	}
	result.CurrentRevision.Evidence = evidence
	result.ConflictRefs, err = conflictRefs(ctx, db, result.MemoryID, result.CurrentRevision.RevisionID)
	if err != nil {
		return domain.Memory{}, err
	}
	if isAggregateMemory(ctx, db, result.MemoryID) {
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

const revisionSelect = `SELECT ` + revisionColumnList + ` FROM memory_revisions`

const revisionColumnList = `revision_id,memory_id,parent_revision_id,restored_from_revision_id,kind,display_content,canonical_content,canonical_hash,event_type,event_time_start,event_time_end,time_precision,time_source,valid_from,valid_until,source_speaker,language,sensitivity,specificity,confidence,created_by,processing_version,processor_identity,created_at`

func revisionColumns(alias string) string {
	parts := strings.Split(revisionColumnList, ",")
	for i := range parts {
		parts[i] = alias + "." + parts[i]
	}
	return strings.Join(parts, ",")
}

func getRevision(ctx context.Context, db memoryQueryer, memoryID, revisionID string) (domain.MemoryRevision, error) {
	result, err := scanRevision(db.QueryRowContext(ctx, revisionSelect+` WHERE memory_id=? AND revision_id=?`, memoryID, revisionID))
	if err != nil {
		return result, err
	}
	err = loadRevisionDetails(ctx, db, &result)
	return result, err
}

func scanRevision(row rowScanner) (domain.MemoryRevision, error) {
	var result domain.MemoryRevision
	err := scanRevisionFields(row, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MemoryRevision{}, ErrNotFound
	}
	return result, err
}

type revisionScan struct {
	parent, restored, eventType, eventStart, eventEnd, precision, timeSource sql.NullString
	validFrom, validUntil, speaker, language, sensitivity, processor         sql.NullString
}

func revisionScanTargets(result *domain.MemoryRevision, nullable *revisionScan) []any {
	return []any{&result.RevisionID, &result.MemoryID, &nullable.parent, &nullable.restored, &result.Kind, &result.DisplayContent, &result.CanonicalContent, &result.CanonicalHash, &nullable.eventType, &nullable.eventStart, &nullable.eventEnd, &nullable.precision, &nullable.timeSource, &nullable.validFrom, &nullable.validUntil, &nullable.speaker, &nullable.language, &nullable.sensitivity, &result.Specificity, &result.Confidence, &result.CreatedBy, &result.ProcessingVersion, &nullable.processor, &result.CreatedAt}
}

func scanRevisionFields(row rowScanner, result *domain.MemoryRevision) error {
	var nullable revisionScan
	if err := row.Scan(revisionScanTargets(result, &nullable)...); err != nil {
		return err
	}
	return applyRevisionScan(result, nullable)
}

func scanMemoryRow(row rowScanner, result *domain.Memory, frozen *int64) error {
	var nullable revisionScan
	targets := []any{&result.MemoryID, &result.SpaceID, &result.CurrentState, frozen, &result.Sensitivity, &result.CreatedAt, &result.UpdatedAt}
	targets = append(targets, revisionScanTargets(&result.CurrentRevision, &nullable)...)
	if err := row.Scan(targets...); err != nil {
		return err
	}
	return applyRevisionScan(&result.CurrentRevision, nullable)
}

func applyRevisionScan(result *domain.MemoryRevision, nullable revisionScan) error {
	result.ParentRevisionID = memoryNullStringPointer(nullable.parent)
	result.RestoredFromRevisionID = memoryNullStringPointer(nullable.restored)
	result.EventType = nullable.eventType.String
	result.TimePrecision = domain.TimePrecision(nullable.precision.String)
	result.TimeSource = nullable.timeSource.String
	result.SourceSpeaker = nullable.speaker.String
	result.Language = nullable.language.String
	result.Sensitivity = nullable.sensitivity.String
	result.ProcessorIdentity = nullable.processor.String
	var err error
	if result.EventTimeStart, err = parseTimePointer(nullable.eventStart); err != nil {
		return err
	}
	if result.EventTimeEnd, err = parseTimePointer(nullable.eventEnd); err != nil {
		return err
	}
	if result.ValidFrom, err = parseTimePointer(nullable.validFrom); err != nil {
		return err
	}
	result.ValidUntil, err = parseTimePointer(nullable.validUntil)
	return err
}

func loadRevisionDetails(ctx context.Context, db memoryQueryer, revision *domain.MemoryRevision) error {
	for _, spec := range []struct {
		query string
		ids   *[]string
	}{
		{`SELECT entity_id FROM revision_subject_entities WHERE revision_id=? ORDER BY entity_order`, &revision.SubjectEntityIDs},
		{`SELECT entity_id FROM revision_object_entities WHERE revision_id=? ORDER BY entity_order`, &revision.ObjectEntityIDs},
	} {
		rows, err := db.QueryContext(ctx, spec.query, revision.RevisionID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			*spec.ids = append(*spec.ids, id)
		}
		if err = rows.Close(); err != nil {
			return err
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT attribute_key,attribute_value FROM revision_attributes WHERE revision_id=? ORDER BY attribute_key`, revision.RevisionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			return err
		}
		if revision.Attributes == nil {
			revision.Attributes = map[string]string{}
		}
		revision.Attributes[key] = value
	}
	return rows.Err()
}

func parseTimePointer(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func memoryNullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func revisionEvidence(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, revisionID string) ([]domain.RevisionEvidence, error) {
	rows, err := db.QueryContext(ctx, `SELECT e.evidence_id,e.event_id,r.occurred_at,r.sequence,e.role,e.start_offset,e.end_offset FROM memory_evidence e JOIN raw_events r ON r.event_id=e.event_id WHERE e.revision_id=? ORDER BY r.occurred_at, r.sequence IS NULL, r.sequence, e.start_offset IS NULL, e.start_offset, e.evidence_id`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.RevisionEvidence{}
	for rows.Next() {
		var item domain.RevisionEvidence
		var eventTime string
		if err := rows.Scan(&item.EvidenceID, &item.EventID, &eventTime, &item.EventSequence, &item.Role, &item.StartOffset, &item.EndOffset); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, eventTime)
		if err != nil {
			return nil, err
		}
		item.EventTime = &parsed
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

func insertRevision(ctx context.Context, tx *sql.Tx, revision domain.MemoryRevision) error {
	if revision.CanonicalHash == "" {
		hash := sha256.Sum256([]byte(revision.CanonicalContent))
		revision.CanonicalHash = hex.EncodeToString(hash[:])
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO memory_revisions(revision_id,memory_id,parent_revision_id,restored_from_revision_id,kind,display_content,canonical_content,canonical_hash,event_type,event_time_start,event_time_end,time_precision,time_source,valid_from,valid_until,source_speaker,language,sensitivity,specificity,confidence,created_by,processing_version,processor_identity,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, revision.RevisionID, revision.MemoryID, revision.ParentRevisionID, revision.RestoredFromRevisionID, revision.Kind, revision.DisplayContent, revision.CanonicalContent, revision.CanonicalHash, nullableString(revision.EventType), formatTimePointer(revision.EventTimeStart), formatTimePointer(revision.EventTimeEnd), nullableString(string(revision.TimePrecision)), nullableString(revision.TimeSource), formatTimePointer(revision.ValidFrom), formatTimePointer(revision.ValidUntil), nullableString(revision.SourceSpeaker), nullableString(revision.Language), revision.Sensitivity, revision.Specificity, revision.Confidence, revision.CreatedBy, revision.ProcessingVersion, nullableString(revision.ProcessorIdentity), revision.CreatedAt)
	if err != nil {
		return err
	}
	for i, entityID := range revision.SubjectEntityIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO revision_subject_entities(revision_id,entity_order,entity_id) VALUES (?,?,?)`, revision.RevisionID, i, entityID); err != nil {
			return err
		}
	}
	for i, entityID := range revision.ObjectEntityIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO revision_object_entities(revision_id,entity_order,entity_id) VALUES (?,?,?)`, revision.RevisionID, i, entityID); err != nil {
			return err
		}
	}
	for key, value := range revision.Attributes {
		if _, err = tx.ExecContext(ctx, `INSERT INTO revision_attributes(revision_id,attribute_key,attribute_value,created_at) VALUES (?,?,?,?)`, revision.RevisionID, key, value, revision.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func revisionFromTextInput(revisionID, memoryID string, parentID, restoredFrom *string, kind, content string, validFrom, validUntil *string, confidence *float64, sensitivity, callerID, processingVersion string, processorIdentity *string, now string) (domain.MemoryRevision, error) {
	revision := domain.MemoryRevision{
		RevisionID: revisionID, MemoryID: memoryID, ParentRevisionID: parentID, RestoredFromRevisionID: restoredFrom,
		Kind: domain.MemoryKind(kind), DisplayContent: content, CanonicalContent: content, Sensitivity: sensitivity,
		CreatedBy: callerID, ProcessingVersion: processingVersion, CreatedAt: now,
	}
	if confidence != nil {
		revision.Confidence = *confidence
	}
	if processorIdentity != nil {
		revision.ProcessorIdentity = *processorIdentity
	}
	var err error
	if revision.ValidFrom, err = parseOptionalTime(validFrom); err != nil {
		return domain.MemoryRevision{}, err
	}
	if revision.ValidUntil, err = parseOptionalTime(validUntil); err != nil {
		return domain.MemoryRevision{}, err
	}
	return revision, nil
}

func validateDirectRevision(revision domain.MemoryRevision, evidence []EvidenceParams) error {
	if domain.ValidAggregateKind(string(revision.Kind)) {
		return ErrInvalidAggregateProposal
	}
	revision.Evidence = revisionEvidenceParamsToDomain(evidence)
	return domain.ValidateMemoryRevision(revision)
}

func revisionEvidenceParamsToDomain(items []EvidenceParams) []domain.RevisionEvidence {
	result := make([]domain.RevisionEvidence, len(items))
	for i, item := range items {
		role := item.Role
		if role == "" {
			role = domain.EvidenceRoleDirect
		}
		result[i] = domain.RevisionEvidence{EvidenceID: fmt.Sprintf("pending-%d", i), EventID: item.EventID, Role: role, StartOffset: item.StartOffset, EndOffset: item.EndOffset}
	}
	return result
}

func parseOptionalTime(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func formatTimePointer(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func isAggregateMemory(ctx context.Context, db rowQuerier, memoryID string) bool {
	var exists int
	return db.QueryRowContext(ctx, `SELECT 1 FROM aggregate_descriptors WHERE memory_id=?`, memoryID).Scan(&exists) == nil
}

func insertEvidence(ctx context.Context, tx *sql.Tx, revisionID string, evidence []EvidenceParams, ids []string, now string) error {
	for i, item := range evidence {
		role := item.Role
		if role == "" {
			role = domain.EvidenceRoleDirect
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_evidence(evidence_id, revision_id, event_id, role, start_offset, end_offset, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, ids[i], revisionID, item.EventID, role, item.StartOffset, item.EndOffset, now); err != nil {
			var sqliteErr sqlite3.Error
			if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
				return fmt.Errorf("%w: evidence rejected", ErrInvalidEvidence)
			}
			return err
		}
	}
	return nil
}

func (r *Repository) upsertMemoryProjection(ctx context.Context, tx *sql.Tx, memoryID, revisionID, spaceID, state, now string, scheduleExtraction ...bool) error {
	revision, err := getRevision(ctx, tx, memoryID, revisionID)
	if err != nil {
		return err
	}
	if err := loadRevisionDetails(ctx, tx, &revision); err != nil {
		return err
	}
	memoryType, content, contentHash := string(revision.Kind), revision.DisplayContent, revision.CanonicalHash
	ftsContent := canonicalStructuredDocument(revision)
	hintDocument, err := RevisionSearchHintDocument(ctx, tx, revisionID)
	if err != nil {
		return err
	}
	validFrom, validUntil := formatTimePointer(revision.ValidFrom), formatTimePointer(revision.ValidUntil)
	searchable := state == "active" || state == "contested"
	embeddingChanged, corpusChanged := true, true
	revisionChanged := true
	var previousRevision, previousSpace, previousType, previousState, previousHash string
	var previousSearchable bool
	var previousValidFrom, previousValidUntil sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT revision_id, space_id, memory_type, current_state, content_hash, searchable, valid_from, valid_until FROM memory_projections WHERE memory_id=?`, memoryID).Scan(&previousRevision, &previousSpace, &previousType, &previousState, &previousHash, &previousSearchable, &previousValidFrom, &previousValidUntil)
	if err == nil {
		revisionChanged = previousRevision != revisionID
		embeddingChanged = revisionChanged || previousSpace != spaceID || previousHash != contentHash || previousSearchable != searchable
		corpusChanged = embeddingChanged || previousType != memoryType || previousState != state || !equalNullableString(previousValidFrom, validFrom) || !equalNullableString(previousValidUntil, validUntil)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	projectionSQL := `INSERT INTO memory_projections(memory_id, revision_id, space_id, memory_type, current_state, searchable, display_content, content_hash, embedding_state, valid_from, valid_until, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'stale', ?, ?, ?) ON CONFLICT(memory_id) DO UPDATE SET revision_id=excluded.revision_id, space_id=excluded.space_id, memory_type=excluded.memory_type, current_state=excluded.current_state, searchable=excluded.searchable, display_content=excluded.display_content, content_hash=excluded.content_hash, valid_from=excluded.valid_from, valid_until=excluded.valid_until, updated_at=excluded.updated_at`
	if embeddingChanged {
		projectionSQL += `, embedding_state='stale', embedding_provider=NULL, embedding_model=NULL, embedding_dimension=NULL, embedding_content_hash=NULL`
	}
	if _, err := tx.ExecContext(ctx, projectionSQL, memoryID, revisionID, spaceID, memoryType, state, searchable, content, contentHash, validFrom, validUntil, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE memory_id=?`, memoryID); err != nil {
		return err
	}
	if searchable {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_fts(memory_id, revision_id, content, hints) VALUES (?, ?, ?, ?)`, memoryID, revisionID, ftsContent, hintDocument); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE index_metadata SET source_revision_count=(SELECT count(*) FROM memory_projections WHERE searchable=1), updated_at=? WHERE index_name='memory_fts'`, now); err != nil {
		return err
	}
	if err := advanceProjectionGeneration(ctx, tx, now); err != nil {
		return err
	}
	if err := advanceCorpusGeneration(ctx, tx, now); err != nil {
		return err
	}
	eligible, err := r.upsertCurrentEmbeddingEligibility(ctx, tx, memoryID, revisionID, spaceID, searchable, now)
	if err != nil {
		return err
	}
	if corpusChanged {
		if err := advanceSemanticCorpusGeneration(ctx, tx, now, embeddingChanged); err != nil {
			return err
		}
	}
	if revisionChanged && searchable && (len(scheduleExtraction) == 0 || scheduleExtraction[0]) {
		if err := r.scheduleGraphExtraction(ctx, tx, revisionID, now); err != nil {
			return err
		}
	}
	if !embeddingChanged {
		return nil
	}
	if r.emb().Provider == nil {
		return nil
	}
	if searchable && eligible {
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
	_, err = r.enqueueSemanticRebuild(ctx, tx, now)
	return err
}

func equalNullableString(stored sql.NullString, value *string) bool {
	return value == nil && !stored.Valid || value != nil && stored.Valid && *value == stored.String
}

func (r *Repository) upsertCurrentEmbeddingEligibility(ctx context.Context, tx *sql.Tx, memoryID, revisionID, spaceID string, searchable bool, now string) (bool, error) {
	runtime := r.emb()
	eligible, reason := false, "not_searchable"
	if searchable {
		state, err := r.currentCapabilityState(ctx, tx, "embedding")
		if errors.Is(err, ErrNotFound) {
			eligible, reason = true, "unconfigured"
		} else if err != nil {
			return false, err
		} else {
			policy, err := r.resolveEffectivePolicy(ctx, tx, "embedding", spaceID, state.PolicyIdentity, runtime.PolicyGeneration)
			if err != nil {
				return false, err
			}
			eligible, reason = policy.Decision == "allow", policy.ReasonCode
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO embedding_eligibility(memory_id,revision_id,space_id,configuration_generation,policy_generation,eligible,reason_code,evaluated_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(memory_id,revision_id,policy_generation) DO UPDATE SET space_id=excluded.space_id,configuration_generation=excluded.configuration_generation,eligible=excluded.eligible,reason_code=excluded.reason_code,evaluated_at=excluded.evaluated_at`, memoryID, revisionID, spaceID, runtime.Generation, runtime.PolicyGeneration, boolInt(eligible), reason, now)
	return eligible, err
}

func (r *Repository) ensureCurrentEmbeddingEligibility(ctx context.Context, tx *sql.Tx, now string) error {
	runtime := r.emb()
	rows, err := tx.QueryContext(ctx, `SELECT memory_id,revision_id,space_id,searchable FROM memory_projections WHERE NOT EXISTS (SELECT 1 FROM embedding_eligibility e WHERE e.memory_id=memory_projections.memory_id AND e.revision_id=memory_projections.revision_id AND e.policy_generation=?)`, runtime.PolicyGeneration)
	if err != nil {
		return err
	}
	type projection struct {
		memoryID, revisionID, spaceID string
		searchable                    bool
	}
	var projections []projection
	for rows.Next() {
		var item projection
		if err := rows.Scan(&item.memoryID, &item.revisionID, &item.spaceID, &item.searchable); err != nil {
			rows.Close()
			return err
		}
		projections = append(projections, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range projections {
		if _, err := r.upsertCurrentEmbeddingEligibility(ctx, tx, item.memoryID, item.revisionID, item.spaceID, item.searchable, now); err != nil {
			return err
		}
	}
	if len(projections) > 0 {
		return advanceSemanticCorpusGeneration(ctx, tx, now, true)
	}
	return nil
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

func evidenceParams(items []domain.RevisionEvidence) []EvidenceParams {
	result := make([]EvidenceParams, len(items))
	for i, item := range items {
		result[i] = EvidenceParams{EventID: item.EventID, Role: item.Role, StartOffset: item.StartOffset, EndOffset: item.EndOffset}
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
