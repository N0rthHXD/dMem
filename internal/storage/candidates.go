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
	"sort"
	"strings"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/mattn/go-sqlite3"
)

var ErrCandidateReviewed = errors.New("candidate has already been reviewed")

type CreateCandidateParams struct {
	EventID            string
	SpaceID            string
	MemoryType         string
	Content            string
	ValidFrom          *string
	ValidUntil         *string
	Confidence         *float64
	Sensitivity        string
	SuggestionKind     string
	ProcessorIdentity  string
	ProviderIdentity   *string
	ModelIdentity      *string
	ProcessingVersion  string
	Evidence           []EvidenceParams
	RelatedMemoryIDs   []string
	StructuredRevision *domain.MemoryRevision
}

type CandidateFilter struct {
	SpaceID           string
	ReviewState       string
	MemoryType        string
	ProcessorIdentity string
	ProcessingVersion string
	CreatedFrom       string
	CreatedUntil      string
	Limit             int
	Cursor            string
}

type ReviewCandidateParams struct {
	ReasonCode string
	Note       *string
}

func (r *Repository) CreateCandidate(ctx context.Context, params CreateCandidateParams) (domain.Candidate, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.Candidate{}, false, err
	}
	defer tx.Rollback()
	if err := r.requireDiskWritable(); err != nil {
		return domain.Candidate{}, false, err
	}
	if err := requireCandidateSources(ctx, tx, params); err != nil {
		return domain.Candidate{}, false, err
	}
	specificationHash := candidateSpecificationHash(params)
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT candidate_id FROM candidates WHERE event_id=? AND processing_version=? AND specification_hash=?`, params.EventID, params.ProcessingVersion, specificationHash).Scan(&existingID)
	if err == nil {
		result, err := getCandidate(ctx, tx, existingID)
		return result, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Candidate{}, false, err
	}
	ids, err := r.newIDs(1 + len(params.Evidence))
	if err != nil {
		return domain.Candidate{}, false, err
	}
	candidateID, now := ids[0], r.runtime.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	contentHash := sha256.Sum256([]byte(params.Content))
	structured, err := encodeStructuredRevision(params.StructuredRevision)
	if err != nil {
		return domain.Candidate{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO candidates(candidate_id, event_id, space_id, memory_type, content, content_hash, structured_revision_json, valid_from, valid_until, confidence, sensitivity, suggestion_kind, processor_identity, provider_identity, model_identity, processing_version, specification_hash, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, candidateID, params.EventID, params.SpaceID, params.MemoryType, params.Content, hex.EncodeToString(contentHash[:]), structured, params.ValidFrom, params.ValidUntil, params.Confidence, params.Sensitivity, params.SuggestionKind, params.ProcessorIdentity, params.ProviderIdentity, params.ModelIdentity, params.ProcessingVersion, specificationHash, now); err != nil {
		return domain.Candidate{}, false, err
	}
	for i, evidence := range params.Evidence {
		role := evidence.Role
		if role == "" {
			role = domain.EvidenceRoleDirect
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO candidate_evidence(evidence_id, candidate_id, event_id, evidence_type, role, start_offset, end_offset, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, ids[i+1], candidateID, evidence.EventID, evidence.EvidenceType, role, evidence.StartOffset, evidence.EndOffset, now); err != nil {
			return domain.Candidate{}, false, candidateConstraintError(err)
		}
	}
	for _, memoryID := range params.RelatedMemoryIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO candidate_related_memories(candidate_id, memory_id, created_at) VALUES (?, ?, ?)`, candidateID, memoryID, now); err != nil {
			return domain.Candidate{}, false, candidateConstraintError(err)
		}
	}
	result, err := getCandidate(ctx, tx, candidateID)
	if err != nil {
		return domain.Candidate{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Candidate{}, false, err
	}
	return result, false, nil
}

func requireCandidateSources(ctx context.Context, tx *sql.Tx, params CreateCandidateParams) error {
	if err := requireSpaceAndEvidence(ctx, tx, params.SpaceID, params.Evidence); err != nil {
		return err
	}
	var eventSpace string
	if err := tx.QueryRowContext(ctx, `SELECT space_id FROM raw_events WHERE event_id=?`, params.EventID).Scan(&eventSpace); err != nil || eventSpace != params.SpaceID {
		return ErrInvalidEvidence
	}
	for _, memoryID := range params.RelatedMemoryIDs {
		if err := memoryExists(ctx, tx, memoryID); err != nil {
			return ErrInvalidEvidence
		}
	}
	return nil
}

func candidateSpecificationHash(params CreateCandidateParams) string {
	evidence := append([]EvidenceParams(nil), params.Evidence...)
	sort.Slice(evidence, func(i, j int) bool {
		left, _ := json.Marshal(evidence[i])
		right, _ := json.Marshal(evidence[j])
		return string(left) < string(right)
	})
	relatedMemoryIDs := append([]string(nil), params.RelatedMemoryIDs...)
	sort.Strings(relatedMemoryIDs)
	value := struct {
		EventID, SpaceID, MemoryType, Content, Sensitivity, SuggestionKind, ProcessingVersion string
		ValidFrom, ValidUntil                                                                 *string
		Confidence                                                                            *float64
		Evidence                                                                              []EvidenceParams
		RelatedMemoryIDs                                                                      []string
		StructuredRevision                                                                    *domain.MemoryRevision
	}{params.EventID, params.SpaceID, params.MemoryType, strings.TrimSpace(params.Content), params.Sensitivity, params.SuggestionKind, params.ProcessingVersion, params.ValidFrom, params.ValidUntil, params.Confidence, evidence, relatedMemoryIDs, structuredSpecification(params.StructuredRevision)}
	data, _ := json.Marshal(value)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func structuredSpecification(revision *domain.MemoryRevision) *domain.MemoryRevision {
	if revision == nil {
		return nil
	}
	normalized := *revision
	normalized.RevisionID = ""
	normalized.MemoryID = ""
	normalized.ParentRevisionID = nil
	normalized.RestoredFromRevisionID = nil
	normalized.CreatedAt = ""
	normalized.CreatedBy = ""
	normalized.ProcessorIdentity = ""
	normalized.ProcessingVersion = ""
	return &normalized
}

func encodeStructuredRevision(revision *domain.MemoryRevision) (string, error) {
	if revision == nil {
		return "{}", nil
	}
	data, err := json.Marshal(revision)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func candidateConstraintError(err error) error {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
		return fmt.Errorf("%w: candidate references", ErrInvalidEvidence)
	}
	return err
}

func (r *Repository) GetCandidate(ctx context.Context, candidateID string) (domain.Candidate, error) {
	return getCandidate(ctx, r.store.reader, candidateID)
}

func (r *Repository) ListCandidates(ctx context.Context, filter CandidateFilter) (domain.CandidatePage, error) {
	tx, err := r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.CandidatePage{}, err
	}
	defer tx.Rollback()
	query := `SELECT candidate_id FROM candidates WHERE space_id=?`
	args := []any{filter.SpaceID}
	for column, value := range map[string]string{
		"review_state": filter.ReviewState, "memory_type": filter.MemoryType,
		"processor_identity": filter.ProcessorIdentity, "processing_version": filter.ProcessingVersion,
	} {
		if value != "" {
			query += ` AND ` + column + `=?`
			args = append(args, value)
		}
	}
	if filter.CreatedFrom != "" {
		query += ` AND created_at>=?`
		args = append(args, filter.CreatedFrom)
	}
	if filter.CreatedUntil != "" {
		query += ` AND created_at<?`
		args = append(args, filter.CreatedUntil)
	}
	if filter.Cursor != "" {
		var cursor struct{ Time, ID string }
		data, err := base64.RawURLEncoding.DecodeString(filter.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Time == "" || cursor.ID == "" {
			return domain.CandidatePage{}, ErrInvalidCursor
		}
		query += ` AND (created_at<? OR (created_at=? AND candidate_id<?))`
		args = append(args, cursor.Time, cursor.Time, cursor.ID)
	}
	query += ` ORDER BY created_at DESC, candidate_id DESC LIMIT ?`
	args = append(args, filter.Limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.CandidatePage{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return domain.CandidatePage{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return domain.CandidatePage{}, err
	}
	page := domain.CandidatePage{Items: []domain.Candidate{}}
	for _, id := range ids {
		item, err := getCandidate(ctx, tx, id)
		if err != nil {
			return domain.CandidatePage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		data, _ := json.Marshal(struct{ Time, ID string }{last.CreatedAt, last.CandidateID})
		next := base64.RawURLEncoding.EncodeToString(data)
		page.NextCursor, page.Items = &next, page.Items[:filter.Limit]
	}
	if err := tx.Commit(); err != nil {
		return domain.CandidatePage{}, err
	}
	return page, nil
}

type candidateQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func getCandidate(ctx context.Context, db candidateQueryer, candidateID string) (domain.Candidate, error) {
	var result domain.Candidate
	err := db.QueryRowContext(ctx, `SELECT candidate_id, event_id, space_id, memory_type, content, valid_from, valid_until, confidence, sensitivity, suggestion_kind, processor_identity, provider_identity, model_identity, processing_version, specification_hash, review_state, confirmed_memory_id, confirmed_revision_id, created_at, reviewed_at FROM candidates WHERE candidate_id=?`, candidateID).Scan(&result.CandidateID, &result.EventID, &result.SpaceID, &result.MemoryType, &result.Content, &result.ValidFrom, &result.ValidUntil, &result.Confidence, &result.Sensitivity, &result.SuggestionKind, &result.ProcessorIdentity, &result.ProviderIdentity, &result.ModelIdentity, &result.ProcessingVersion, &result.SpecificationHash, &result.ReviewState, &result.ConfirmedMemoryID, &result.ConfirmedRevisionID, &result.CreatedAt, &result.ReviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Candidate{}, ErrNotFound
	}
	if err != nil {
		return domain.Candidate{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT evidence_id, event_id, role, start_offset, end_offset FROM candidate_evidence WHERE candidate_id=? ORDER BY evidence_id`, candidateID)
	if err != nil {
		return domain.Candidate{}, err
	}
	result.Evidence = []domain.RevisionEvidence{}
	for rows.Next() {
		var item domain.RevisionEvidence
		if err := rows.Scan(&item.EvidenceID, &item.EventID, &item.Role, &item.StartOffset, &item.EndOffset); err != nil {
			rows.Close()
			return domain.Candidate{}, err
		}
		result.Evidence = append(result.Evidence, item)
	}
	if err := rows.Close(); err != nil {
		return domain.Candidate{}, err
	}
	domain.OrderRevisionEvidence(result.Evidence)
	rows, err = db.QueryContext(ctx, `SELECT memory_id FROM candidate_related_memories WHERE candidate_id=? ORDER BY memory_id`, candidateID)
	if err != nil {
		return domain.Candidate{}, err
	}
	result.RelatedMemoryIDs = []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return domain.Candidate{}, err
		}
		result.RelatedMemoryIDs = append(result.RelatedMemoryIDs, id)
	}
	if err := rows.Close(); err != nil {
		return domain.Candidate{}, err
	}
	rows, err = db.QueryContext(ctx, `SELECT review_id, from_state, to_state, reason_code, note, reviewer_id, memory_id, revision_id, created_at FROM candidate_reviews WHERE candidate_id=? ORDER BY created_at DESC, review_id DESC`, candidateID)
	if err != nil {
		return domain.Candidate{}, err
	}
	result.Reviews = []domain.CandidateReview{}
	defer rows.Close()
	for rows.Next() {
		var item domain.CandidateReview
		if err := rows.Scan(&item.ReviewID, &item.FromState, &item.ToState, &item.ReasonCode, &item.Note, &item.ReviewerID, &item.MemoryID, &item.RevisionID, &item.CreatedAt); err != nil {
			return domain.Candidate{}, err
		}
		result.Reviews = append(result.Reviews, item)
	}
	return result, rows.Err()
}

func candidateStructuredRevision(ctx context.Context, tx *sql.Tx, candidateID string) (domain.MemoryRevision, bool, error) {
	var encoded string
	if err := tx.QueryRowContext(ctx, `SELECT structured_revision_json FROM candidates WHERE candidate_id=?`, candidateID).Scan(&encoded); err != nil {
		return domain.MemoryRevision{}, false, err
	}
	if encoded == "" || encoded == "{}" {
		return domain.MemoryRevision{}, false, nil
	}
	var revision domain.MemoryRevision
	if err := json.Unmarshal([]byte(encoded), &revision); err != nil {
		return domain.MemoryRevision{}, false, err
	}
	if strings.TrimSpace(revision.CanonicalContent) == "" || !revision.Kind.Valid() {
		return domain.MemoryRevision{}, false, nil
	}
	return revision, true, nil
}

func candidateRevision(ctx context.Context, tx *sql.Tx, candidate domain.Candidate, revisionID, memoryID, caller, now string) (domain.MemoryRevision, error) {
	structured, found, err := candidateStructuredRevision(ctx, tx, candidate.CandidateID)
	if err != nil {
		return domain.MemoryRevision{}, err
	}
	if !found {
		return revisionFromTextInput(revisionID, memoryID, nil, nil, candidate.MemoryType, candidate.Content, candidate.ValidFrom, candidate.ValidUntil, candidate.Confidence, candidate.Sensitivity, caller, candidate.ProcessingVersion, &candidate.ProcessorIdentity, now)
	}
	structured.RevisionID = revisionID
	structured.MemoryID = memoryID
	structured.ParentRevisionID = nil
	structured.RestoredFromRevisionID = nil
	structured.Kind = domain.MemoryKind(candidate.MemoryType)
	structured.DisplayContent = candidate.Content
	if structured.CanonicalContent == "" {
		structured.CanonicalContent = candidate.Content
	}
	if candidate.ValidFrom != nil {
		if structured.ValidFrom, err = parseOptionalTime(candidate.ValidFrom); err != nil {
			return domain.MemoryRevision{}, err
		}
	}
	if candidate.ValidUntil != nil {
		if structured.ValidUntil, err = parseOptionalTime(candidate.ValidUntil); err != nil {
			return domain.MemoryRevision{}, err
		}
	}
	if candidate.Confidence != nil {
		structured.Confidence = *candidate.Confidence
	}
	structured.Sensitivity = candidate.Sensitivity
	structured.CreatedBy = caller
	structured.ProcessingVersion = candidate.ProcessingVersion
	structured.ProcessorIdentity = candidate.ProcessorIdentity
	structured.CreatedAt = now
	if err := domain.ValidateMemoryRevision(structured); err != nil {
		return domain.MemoryRevision{}, err
	}
	return structured, nil
}

func (r *Repository) ConfirmCandidate(ctx context.Context, identity WriteIdentity, candidateID string, params ReviewCandidateParams) (domain.Memory, bool, error) {
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
	candidate, err := getCandidate(ctx, tx, candidateID)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if candidate.ReviewState != "pending" {
		return domain.Memory{}, false, ErrCandidateReviewed
	}
	evidence := evidenceParams(candidate.Evidence)
	if err := requireSpaceAndEvidence(ctx, tx, candidate.SpaceID, evidence); err != nil {
		return domain.Memory{}, false, err
	}
	structured, found, err := candidateStructuredRevision(ctx, tx, candidate.CandidateID)
	if err != nil {
		return domain.Memory{}, false, err
	}
	classificationRevision := consolidationClassificationRevision(candidate, structured, found)
	outcome, _, err := r.classifyCandidateForConsolidation(ctx, tx, candidate, classificationRevision)
	if err != nil {
		return domain.Memory{}, false, err
	}
	result, targetMemoryID, targetRevisionID, err := r.applyConsolidation(ctx, tx, identity, candidate, evidence, structured, found, outcome)
	if err != nil {
		return domain.Memory{}, false, err
	}
	decisionID, operationID, auditID, reviewID, err := r.fourIDs()
	if err != nil {
		return domain.Memory{}, false, err
	}
	now := formatTime(r.runtime.Now())
	if outcome.Action != domain.ConsolidationReject {
		outcome.TargetMemoryID, outcome.TargetRevisionID = targetMemoryID, targetRevisionID
	}
	if err := r.recordConsolidationDecision(ctx, tx, decisionID, candidate, outcome); err != nil {
		return domain.Memory{}, false, err
	}
	status := 201
	toState := "confirmed"
	if outcome.Action == domain.ConsolidationReject {
		status, toState = 200, "rejected"
	}
	updated, err := tx.ExecContext(ctx, `UPDATE candidates SET review_state=?, confirmed_memory_id=?, confirmed_revision_id=?, reviewed_at=? WHERE candidate_id=? AND review_state='pending'`, toState, nullableConfirmRef(targetMemoryID), nullableConfirmRef(targetRevisionID), now, candidateID)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		return domain.Memory{}, false, ErrCandidateReviewed
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO candidate_reviews(review_id, candidate_id, operation_id, from_state, to_state, reason_code, note, reviewer_id, memory_id, revision_id, created_at) VALUES (?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?)`, reviewID, candidateID, operationID, toState, params.ReasonCode, params.Note, identity.CallerID, nullableConfirmRef(targetMemoryID), nullableConfirmRef(targetRevisionID), now); err != nil {
		return domain.Memory{}, false, err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if err := writeIdempotency(ctx, tx, identity, operationID, status, body, now); err != nil {
		return domain.Memory{}, false, err
	}
	reason, _ := json.Marshal(map[string]any{
		"reason": params.ReasonCode, "consolidation_action": outcome.Action,
		"consolidation_reason": outcome.ReasonCode, "decision_id": decisionID,
		"target_memory_id": targetMemoryID, "target_revision_id": targetRevisionID,
	})
	action := "confirm_candidate"
	if outcome.Action == domain.ConsolidationReject {
		action = "reject_candidate"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, reason, created_at) VALUES (?, ?, ?, ?, ?, 'candidate', ?, 'succeeded', ?, ?)`, auditID, operationID, identity.RequestID, identity.CallerID, action, candidateID, string(reason), now); err != nil {
		return domain.Memory{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, false, err
	}
	return result, false, nil
}

func nullableConfirmRef(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (r *Repository) fourIDs() (string, string, string, string, error) {
	ids, err := r.newIDs(4)
	if err != nil {
		return "", "", "", "", err
	}
	return ids[0], ids[1], ids[2], ids[3], nil
}

// consolidationClassificationRevision builds the revision view used for
// consolidation classification, even when the candidate carries no structured
// extraction output.
func consolidationClassificationRevision(candidate domain.Candidate, structured domain.MemoryRevision, found bool) domain.MemoryRevision {
	if found {
		if structured.CanonicalHash == "" {
			structured.CanonicalHash = canonicalContentHash(structured.CanonicalContent)
		}
		return structured
	}
	revision := domain.MemoryRevision{
		Kind:              domain.MemoryKind(candidate.MemoryType),
		DisplayContent:    candidate.Content,
		CanonicalContent:  candidate.Content,
		CanonicalHash:     canonicalContentHash(candidate.Content),
		Sensitivity:       candidate.Sensitivity,
		ProcessingVersion: candidate.ProcessingVersion,
		Evidence:          candidate.Evidence,
	}
	if candidate.Confidence != nil {
		revision.Confidence = *candidate.Confidence
	}
	if candidate.ValidFrom != nil {
		revision.ValidFrom, _ = parseOptionalTime(candidate.ValidFrom)
	}
	if candidate.ValidUntil != nil {
		revision.ValidUntil, _ = parseOptionalTime(candidate.ValidUntil)
	}
	return revision
}

// applyConsolidation executes the classified action and returns the resulting
// memory, plus the target memory and revision the decision binds to.
func (r *Repository) applyConsolidation(ctx context.Context, tx *sql.Tx, identity WriteIdentity, candidate domain.Candidate, evidence []EvidenceParams, structured domain.MemoryRevision, found bool, outcome consolidationOutcome) (domain.Memory, string, string, error) {
	now := formatTime(r.runtime.Now())
	switch outcome.Action {
	case domain.ConsolidationCreate:
		memory, memoryID, revisionID, err := r.createMemoryFromCandidate(ctx, tx, identity, candidate, evidence, now)
		if err != nil {
			return domain.Memory{}, "", "", err
		}
		return memory, memoryID, revisionID, nil
	case domain.ConsolidationReject:
		return domain.Memory{}, "", "", nil
	case domain.ConsolidationAttachEvidence:
		if _, err := r.attachConsolidationEvidence(ctx, tx, candidate.SpaceID, outcome.TargetMemoryID, outcome.TargetRevisionID, evidence, now); err != nil {
			return domain.Memory{}, "", "", err
		}
		memory, err := getMemory(ctx, tx, outcome.TargetMemoryID, false)
		if err != nil {
			return domain.Memory{}, "", "", err
		}
		if err := r.refreshConsolidationProjections(ctx, tx, outcome.TargetMemoryID, outcome.TargetRevisionID, now); err != nil {
			return domain.Memory{}, "", "", err
		}
		return memory, outcome.TargetMemoryID, outcome.TargetRevisionID, nil
	case domain.ConsolidationRefine, domain.ConsolidationSupersede:
		if _, err := r.insertConsolidationEvolution(ctx, tx, candidate, structured, outcome, nil, now); err != nil {
			return domain.Memory{}, "", "", err
		}
		memory, err := getMemory(ctx, tx, outcome.TargetMemoryID, false)
		if err != nil {
			return domain.Memory{}, "", "", err
		}
		return memory, outcome.TargetMemoryID, outcome.TargetRevisionID, nil
	case domain.ConsolidationContest:
		newMemory, newMemoryID, newRevisionID, err := r.createMemoryFromCandidate(ctx, tx, identity, candidate, evidence, now)
		if err != nil {
			return domain.Memory{}, "", "", err
		}
		second := domain.EvolutionTarget{MemoryID: newMemoryID, ExpectedRevisionID: newRevisionID}
		if _, err := r.insertConsolidationEvolution(ctx, tx, candidate, structured, outcome, &second, now); err != nil {
			return domain.Memory{}, "", "", err
		}
		return newMemory, outcome.TargetMemoryID, outcome.TargetRevisionID, nil
	default:
		return domain.Memory{}, "", "", ErrInvalidConsolidation
	}
}

// createMemoryFromCandidate creates one logical memory and its first revision
// from a confirmed candidate inside the caller's transaction.
func (r *Repository) createMemoryFromCandidate(ctx context.Context, tx *sql.Tx, identity WriteIdentity, candidate domain.Candidate, evidence []EvidenceParams, now string) (domain.Memory, string, string, error) {
	ids, err := r.newIDs(4 + len(evidence))
	if err != nil {
		return domain.Memory{}, "", "", err
	}
	memoryID, revisionID, stateEventID, operationID := ids[0], ids[1], ids[2], ids[3]
	if _, err := tx.ExecContext(ctx, `INSERT INTO memories(memory_id, space_id, current_revision_id, current_state, frozen, sensitivity, created_at, updated_at) VALUES (?, ?, ?, 'active', 0, ?, ?, ?)`, memoryID, candidate.SpaceID, revisionID, candidate.Sensitivity, now, now); err != nil {
		return domain.Memory{}, "", "", err
	}
	revision, err := candidateRevision(ctx, tx, candidate, revisionID, memoryID, identity.CallerID, now)
	if err != nil {
		return domain.Memory{}, "", "", err
	}
	if err := insertRevision(ctx, tx, revision); err != nil {
		return domain.Memory{}, "", "", err
	}
	if err := insertEvidence(ctx, tx, revisionID, evidence, ids[4:], now); err != nil {
		return domain.Memory{}, "", "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id, memory_id, revision_id, operation_id, from_state, to_state, from_frozen, to_frozen, reason, created_by, created_at) VALUES (?, ?, ?, ?, NULL, 'active', NULL, 0, ?, ?, ?)`, stateEventID, memoryID, revisionID, operationID, "consolidation_create", identity.CallerID, now); err != nil {
		return domain.Memory{}, "", "", err
	}
	if err := r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, candidate.SpaceID, "active", now); err != nil {
		return domain.Memory{}, "", "", err
	}
	memory, err := getMemory(ctx, tx, memoryID, false)
	if err != nil {
		return domain.Memory{}, "", "", err
	}
	return memory, memoryID, revisionID, nil
}

// refreshConsolidationProjections schedules derived quality and duplicate
// projection updates after evidence changes without changing eligibility.
func (r *Repository) refreshConsolidationProjections(ctx context.Context, tx *sql.Tx, memoryID, revisionID, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE memory_projections SET updated_at=? WHERE memory_id=?`, now, memoryID); err != nil {
		return err
	}
	return advanceProjectionGeneration(ctx, tx, now)
}

func (r *Repository) RejectCandidate(ctx context.Context, identity WriteIdentity, candidateID string, params ReviewCandidateParams) (domain.Candidate, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.Candidate{}, false, err
	}
	defer tx.Rollback()
	status, body, found, err := readIdempotency(ctx, tx, identity)
	if err != nil {
		return domain.Candidate{}, false, err
	}
	if found {
		var result domain.Candidate
		if status != 200 || json.Unmarshal([]byte(body), &result) != nil {
			return domain.Candidate{}, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.Candidate{}, false, err
	}
	candidate, err := getCandidate(ctx, tx, candidateID)
	if err != nil {
		return domain.Candidate{}, false, err
	}
	if candidate.ReviewState != "pending" {
		return domain.Candidate{}, false, ErrCandidateReviewed
	}
	ids, err := r.newIDs(3)
	if err != nil {
		return domain.Candidate{}, false, err
	}
	operationID, auditID, reviewID := ids[0], ids[1], ids[2]
	now := formatTime(r.runtime.Now())
	updated, err := tx.ExecContext(ctx, `UPDATE candidates SET review_state='rejected', reviewed_at=? WHERE candidate_id=? AND review_state='pending'`, now, candidateID)
	if err != nil {
		return domain.Candidate{}, false, err
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		return domain.Candidate{}, false, ErrCandidateReviewed
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO candidate_reviews(review_id, candidate_id, operation_id, from_state, to_state, reason_code, note, reviewer_id, created_at) VALUES (?, ?, ?, 'pending', 'rejected', ?, ?, ?, ?)`, reviewID, candidateID, operationID, params.ReasonCode, params.Note, identity.CallerID, now); err != nil {
		return domain.Candidate{}, false, err
	}
	result, err := getCandidate(ctx, tx, candidateID)
	if err != nil {
		return domain.Candidate{}, false, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return domain.Candidate{}, false, err
	}
	if err := writeIdempotency(ctx, tx, identity, operationID, 200, encoded, now); err != nil {
		return domain.Candidate{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, reason, created_at) VALUES (?, ?, ?, ?, 'reject_candidate', 'candidate', ?, 'succeeded', ?, ?)`, auditID, operationID, identity.RequestID, identity.CallerID, candidateID, params.ReasonCode, now); err != nil {
		return domain.Candidate{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Candidate{}, false, err
	}
	return result, false, nil
}
