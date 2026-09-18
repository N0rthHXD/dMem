package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/consolidation"
)

var ErrInvalidConsolidation = errors.New("invalid consolidation proposal")

// The deterministic consolidation classifier is protocol-independent and lives
// in the gateway module. Storage keeps only the bounded recall query, the atomic
// decision write, and the authority mutation; these aliases keep the SQL path
// readable without duplicating classification logic.
type (
	consolidationRules      = consolidation.Rules
	consolidationNeighbor   = consolidation.Neighbor
	consolidationSimilarity = consolidation.Similarity
	consolidationOutcome    = consolidation.Outcome
	evidenceRange           = consolidation.EvidenceRange
)

// SetConsolidationClassifier installs the optional, policy-controlled provider
// classifier used only for candidates the deterministic rules leave unresolved.
func (r *Repository) SetConsolidationClassifier(classifier consolidation.Classifier) {
	r.consolidationClassifier = classifier
}

func canonicalContentHash(content string) string {
	return consolidation.CanonicalContentHash(content)
}

// intervalsOverlap reports whether two optional valid-time intervals overlap. It
// backs duplicate detection, which is unrelated to candidate consolidation.
func intervalsOverlap(aStart, aEnd, bStart, bEnd *time.Time) bool {
	return consolidation.IntervalsOverlap(aStart, aEnd, bStart, bEnd)
}

// boundedConsolidationRecall retrieves a bounded, deterministically ordered set
// of current memory revisions in the exact target space that share a canonical
// hash, subject/kind tuple, or evidence event with the candidate.
func (r *Repository) boundedConsolidationRecall(ctx context.Context, tx *sql.Tx, spaceID string, revision domain.MemoryRevision) ([]consolidationNeighbor, error) {
	limit := r.consolidationMaxCandidates
	if limit <= 0 {
		limit = 32
	}
	hash := revision.CanonicalHash
	if hash == "" {
		hash = canonicalContentHash(revision.CanonicalContent)
	}
	var clauses []string
	args := []any{spaceID}
	if hash != "" {
		clauses = append(clauses, `rv.canonical_hash = ?`)
		args = append(args, hash)
	}
	if len(revision.SubjectEntityIDs) > 0 {
		clauses = append(clauses, `(p.memory_type = ? AND EXISTS (SELECT 1 FROM revision_subject_entities s WHERE s.revision_id = rv.revision_id AND s.entity_id IN (`+placeholders(len(revision.SubjectEntityIDs))+`)))`)
		args = append(args, string(revision.Kind))
		args = append(args, stringsToAny(revision.SubjectEntityIDs)...)
	}
	events := make([]string, 0, len(revision.Evidence))
	for _, item := range revision.Evidence {
		if item.EventID != "" {
			events = append(events, item.EventID)
		}
	}
	if len(events) > 0 {
		clauses = append(clauses, `EXISTS (SELECT 1 FROM memory_evidence me WHERE me.revision_id = rv.revision_id AND me.event_id IN (`+placeholders(len(events))+`))`)
		args = append(args, stringsToAny(events)...)
	}
	query := `SELECT p.memory_id, p.revision_id, p.memory_type, p.current_state, m.frozen,
		rv.canonical_hash, rv.canonical_content, coalesce(rv.event_type,''), rv.valid_from, rv.valid_until, rv.specificity, coalesce(rv.time_precision,'')
		FROM memory_projections p
		JOIN memories m ON m.memory_id = p.memory_id
		JOIN memory_revisions rv ON rv.memory_id = p.memory_id AND rv.revision_id = p.revision_id
		WHERE p.space_id = ? AND p.searchable = 1`
	if len(clauses) > 0 {
		query += ` AND (` + strings.Join(clauses, " OR ") + `)`
	}
	query += ` ORDER BY (rv.canonical_hash = ?) DESC, p.memory_id ASC LIMIT ?`
	args = append(args, hash, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var neighbors []consolidationNeighbor
	for rows.Next() {
		var item consolidationNeighbor
		var validFrom, validUntil sql.NullString
		var precision, lifecycle, eventType string
		var frozen bool
		if err := rows.Scan(&item.MemoryID, &item.RevisionID, &item.Kind, &lifecycle, &frozen, &item.CanonicalHash, &item.CanonicalContent, &eventType, &validFrom, &validUntil, &item.Specificity, &precision); err != nil {
			return nil, err
		}
		item.Precision = domain.TimePrecision(precision)
		if item.ValidFrom, err = parseTimePointer(validFrom); err != nil {
			return nil, err
		}
		if item.ValidUntil, err = parseTimePointer(validUntil); err != nil {
			return nil, err
		}
		if err := loadConsolidationDetails(ctx, tx, &item); err != nil {
			return nil, err
		}
		neighbors = append(neighbors, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return neighbors, nil
}

func loadConsolidationDetails(ctx context.Context, tx *sql.Tx, neighbor *consolidationNeighbor) error {
	neighbor.Subjects = []string{}
	rows, err := tx.QueryContext(ctx, `SELECT entity_id FROM revision_subject_entities WHERE revision_id=? ORDER BY entity_order`, neighbor.RevisionID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		neighbor.Subjects = append(neighbor.Subjects, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	neighbor.Objects = []string{}
	rows, err = tx.QueryContext(ctx, `SELECT entity_id FROM revision_object_entities WHERE revision_id=? ORDER BY entity_order`, neighbor.RevisionID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		neighbor.Objects = append(neighbor.Objects, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	neighbor.Attributes = map[string]string{}
	rows, err = tx.QueryContext(ctx, `SELECT attribute_key, attribute_value FROM revision_attributes WHERE revision_id=? ORDER BY attribute_key`, neighbor.RevisionID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return err
		}
		neighbor.Attributes[key] = value
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT event_id, start_offset, end_offset FROM memory_evidence WHERE revision_id=? ORDER BY event_id, start_offset`, neighbor.RevisionID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var item evidenceRange
		if err := rows.Scan(&item.EventID, &item.StartOffset, &item.EndOffset); err != nil {
			rows.Close()
			return err
		}
		neighbor.EvidenceRanges = append(neighbor.EvidenceRanges, item)
		neighbor.EvidenceEventIDs = append(neighbor.EvidenceEventIDs, item.EventID)
	}
	return rows.Close()
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

// classifyCandidateForConsolidation performs bounded consolidation recall and
// delegates deterministic classification and provider-proposal validation to the
// protocol-independent classifier. The provider classifier is consulted only
// when the deterministic rules leave the candidate unresolved, and its result
// remains a proposal validated against the cosine-only prohibition.
func (r *Repository) classifyCandidateForConsolidation(ctx context.Context, tx *sql.Tx, candidate domain.Candidate, revision domain.MemoryRevision) (consolidationOutcome, bool, error) {
	neighbors, err := r.boundedConsolidationRecall(ctx, tx, candidate.SpaceID, revision)
	if err != nil {
		return consolidationOutcome{}, false, err
	}
	rules := r.consolidationRules()
	outcome := consolidation.NovelOutcome()
	matched := false
	for i := range neighbors {
		action, reason := consolidation.ClassifyDeterministic(rules, revision, &neighbors[i], consolidationSimilarity{})
		if action == domain.ConsolidationCreate {
			continue
		}
		outcome = consolidationOutcome{
			Action: action, ReasonCode: reason,
			TargetMemoryID: neighbors[i].MemoryID, TargetRevisionID: neighbors[i].RevisionID,
			Classification: domain.ConsolidationDeterministic,
		}
		matched = true
		break
	}
	if !matched && r.consolidationClassifier != nil && len(neighbors) > 0 {
		conflict := consolidation.ConflictDetected(revision, neighbors)
		providerNeighbors := append([]consolidation.Neighbor(nil), neighbors...)
		proposal, err := r.consolidationClassifier.Classify(ctx, consolidation.ClassifyRequest{Candidate: revision, Neighbors: providerNeighbors})
		if err != nil {
			return consolidationOutcome{}, false, err
		}
		if classified, ok := consolidation.ValidateProviderProposal(outcome, proposal, conflict); ok {
			outcome = classified
		}
	}
	if !outcome.Action.Valid() {
		outcome = consolidation.NovelOutcome()
	}
	return outcome, matched, nil
}

// recordConsolidationDecision persists one immutable decision row for a
// candidate. It fails if a decision already exists, keeping one terminal record
// per candidate.
func (r *Repository) recordConsolidationDecision(ctx context.Context, tx *sql.Tx, decisionID string, candidate domain.Candidate, outcome consolidationOutcome) error {
	now := formatTime(r.runtime.Now())
	classification := outcome.Classification
	if classification == "" {
		classification = domain.ConsolidationNone
	}
	var targetMemory, targetRevision any
	if outcome.TargetMemoryID != "" {
		targetMemory, targetRevision = outcome.TargetMemoryID, outcome.TargetRevisionID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO consolidation_decisions(decision_id,candidate_id,space_id,action,reason_code,target_memory_id,target_revision_id,similarity,classification_source,algorithm_version,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		decisionID, candidate.CandidateID, candidate.SpaceID, outcome.Action, outcome.ReasonCode, targetMemory, targetRevision, outcome.Similarity, classification, r.consolidationRules().Version, now)
	return err
}

// GetConsolidationDecision returns the recorded decision for a candidate.
func (r *Repository) GetConsolidationDecision(ctx context.Context, candidateID string) (domain.ConsolidationDecision, error) {
	var result domain.ConsolidationDecision
	var targetMemory, targetRevision sql.NullString
	err := r.store.reader.QueryRowContext(ctx, `SELECT decision_id,candidate_id,space_id,action,reason_code,target_memory_id,target_revision_id,similarity,classification_source,algorithm_version,created_at FROM consolidation_decisions WHERE candidate_id=?`, candidateID).Scan(
		&result.DecisionID, &result.CandidateID, &result.SpaceID, &result.Action, &result.ReasonCode, &targetMemory, &targetRevision, &result.Similarity, &result.Classification, &result.AlgorithmVersion, &result.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result.TargetMemoryID = memoryNullStringPointer(targetMemory)
	result.TargetRevisionID = memoryNullStringPointer(targetRevision)
	return result, nil
}

// attachConsolidationEvidence appends non-duplicate evidence to an existing
// current revision atomically, preserving immutable evidence history.
func (r *Repository) attachConsolidationEvidence(ctx context.Context, tx *sql.Tx, spaceID, memoryID, expectedRevisionID string, evidence []EvidenceParams, now string) (int, error) {
	var currentRevision, space string
	var frozen bool
	if err := tx.QueryRowContext(ctx, `SELECT current_revision_id, space_id, frozen FROM memories WHERE memory_id=?`, memoryID).Scan(&currentRevision, &space, &frozen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if space != spaceID {
		return 0, ErrInvalidEvidence
	}
	if currentRevision != expectedRevisionID {
		return 0, ErrRevisionConflict
	}
	if frozen {
		return 0, ErrFrozenRevision
	}
	inserted := 0
	for _, item := range evidence {
		id, err := r.runtime.ID()
		if err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_evidence(evidence_id, revision_id, event_id, role, start_offset, end_offset, created_at) VALUES (?,?,?,?,?,?,?)`, id, expectedRevisionID, item.EventID, item.Role, item.StartOffset, item.EndOffset, now)
		if err != nil {
			return 0, err
		}
		if changed, err := result.RowsAffected(); err != nil {
			return 0, err
		} else {
			inserted += int(changed)
		}
	}
	return inserted, nil
}

// insertConsolidationEvolution records an explicit evolution proposal for
// refine, supersede, or contest with the target's expected revision and a review
// boundary. Conflict proposals must carry two existing targets.
func (r *Repository) insertConsolidationEvolution(ctx context.Context, tx *sql.Tx, candidate domain.Candidate, revision domain.MemoryRevision, outcome consolidationOutcome, secondTarget *domain.EvolutionTarget, now string) (string, error) {
	action := ""
	switch outcome.Action {
	case domain.ConsolidationRefine:
		action = "supplement"
	case domain.ConsolidationSupersede:
		action = "replacement"
	case domain.ConsolidationContest:
		action = "conflict"
	default:
		return "", ErrInvalidConsolidation
	}
	params := CreateEvolutionProposalParams{
		EventID: candidate.EventID, SpaceID: candidate.SpaceID, Action: action,
		ProposedContent: &revision.DisplayContent, ValidFrom: timePointerText(revision.ValidFrom), ValidUntil: timePointerText(revision.ValidUntil),
		MemoryType: stringPointer(string(revision.Kind)), Sensitivity: revision.Sensitivity,
		ProcessorIdentity: candidate.ProcessorIdentity, ProviderIdentity: candidate.ProviderIdentity, ModelIdentity: candidate.ModelIdentity,
		ProcessingVersion: candidate.ProcessingVersion,
		RelationSpecs:     []byte("[]"), MentionSpecs: []byte("[]"),
		Targets:  []domain.EvolutionTarget{{MemoryID: outcome.TargetMemoryID, ExpectedRevisionID: outcome.TargetRevisionID}},
		Evidence: consolidationEvolutionEvidence(candidate),
	}
	if action == "conflict" {
		if secondTarget == nil {
			return "", ErrInvalidConsolidation
		}
		params.Targets = append(params.Targets, *secondTarget)
	}
	if err := validateEvolutionProposal(ctx, tx, params); err != nil {
		return "", err
	}
	hash := evolutionSpecificationHash(params)
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT proposal_id FROM evolution_proposals WHERE event_id=? AND processing_version=? AND action=? AND specification_hash=?`, params.EventID, params.ProcessingVersion, params.Action, hash).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	proposalID, err := r.runtime.ID()
	if err != nil {
		return "", err
	}
	var contentHash *string
	if params.ProposedContent != nil {
		h := sha256.Sum256([]byte(*params.ProposedContent))
		value := hex.EncodeToString(h[:])
		contentHash = &value
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO evolution_proposals(proposal_id,event_id,space_id,action,proposed_content,proposed_content_hash,valid_from,valid_until,memory_type,sensitivity,relation_specs_json,mention_specs_json,processor_identity,provider_identity,model_identity,processing_version,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		proposalID, params.EventID, params.SpaceID, params.Action, params.ProposedContent, contentHash, params.ValidFrom, params.ValidUntil, params.MemoryType, params.Sensitivity, "[]", "[]", params.ProcessorIdentity, params.ProviderIdentity, params.ModelIdentity, params.ProcessingVersion, hash, now); err != nil {
		return "", err
	}
	for i, target := range params.Targets {
		if _, err := tx.ExecContext(ctx, `INSERT INTO evolution_proposal_targets(proposal_id,target_order,memory_id,expected_revision_id,created_at) VALUES (?,?,?,?,?)`, proposalID, i, target.MemoryID, target.ExpectedRevisionID, now); err != nil {
			return "", err
		}
	}
	for _, evidence := range params.Evidence {
		id, err := r.runtime.ID()
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO evolution_proposal_evidence(evidence_id,proposal_id,evidence_type,event_id,memory_id,revision_id,start_offset,end_offset,note,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, id, proposalID, evidence.EvidenceType, evidence.EventID, evidence.MemoryID, evidence.RevisionID, evidence.StartOffset, evidence.EndOffset, evidence.Note, now); err != nil {
			return "", err
		}
	}
	return proposalID, nil
}

func consolidationEvolutionEvidence(candidate domain.Candidate) []domain.RelationEvidence {
	result := []domain.RelationEvidence{}
	for _, item := range candidate.Evidence {
		if item.StartOffset == nil || item.EndOffset == nil {
			continue
		}
		start, end := int(*item.StartOffset), int(*item.EndOffset)
		eventID := item.EventID
		result = append(result, domain.RelationEvidence{EvidenceType: "raw_event_range", EventID: &eventID, StartOffset: &start, EndOffset: &end})
	}
	if len(result) == 0 {
		result = append(result, domain.RelationEvidence{EvidenceType: "human_confirmation"})
	}
	return result
}
