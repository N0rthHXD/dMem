package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

var ErrInvalidConsolidation = errors.New("invalid consolidation proposal")

// consolidationRules are the versioned deterministic thresholds that classify a
// structured candidate against existing memory revisions (ADR 0035).
type consolidationRules struct {
	Version               string
	ExactHashEnabled      bool
	SameSourceEventCosine float64
	SameSubjectKindCosine float64
}

// consolidationNeighbor is one bounded recall hit for candidate consolidation.
type consolidationNeighbor struct {
	MemoryID         string
	RevisionID       string
	Kind             domain.MemoryKind
	CanonicalHash    string
	CanonicalContent string
	EventType        string
	Subjects         []string
	Objects          []string
	Attributes       map[string]string
	EvidenceRanges   []evidenceRange
	ValidFrom        *time.Time
	ValidUntil       *time.Time
	Specificity      float64
	Precision        domain.TimePrecision
	Lifecycle        string
	Frozen           bool
}

type evidenceRange struct {
	EventID     string
	StartOffset *int64
	EndOffset   *int64
}

// consolidationSimilarity carries optional semantic proximity. When ok is false
// no cosine was computed and semantic-only rules must not fire.
type consolidationSimilarity struct {
	Cosine float64
	OK     bool
}

type consolidationOutcome struct {
	Action           domain.ConsolidationAction
	ReasonCode       string
	TargetMemoryID   string
	TargetRevisionID string
	Similarity       *float64
	Classification   domain.ConsolidationClassification
}

// ConsolidationProviderProposal is a policy-controlled provider classification
// for a candidate the deterministic rules could not resolve. It remains a
// proposal until deterministic validation accepts it.
type ConsolidationProviderProposal struct {
	Action     domain.ConsolidationAction
	ReasonCode string
	MemoryID   string
	RevisionID string
	Similarity *float64
}

// ConsolidationClassifier is the optional provider interface for unresolved
// candidates. It must be policy-gated by the caller.
type ConsolidationClassifier interface {
	ClassifyConsolidation(ctx context.Context, candidate domain.MemoryRevision, neighbors []ConsolidationNeighbor) (ConsolidationProviderProposal, error)
}

// ConsolidationNeighbor is the exported classifier view of a recall hit.
type ConsolidationNeighbor struct {
	MemoryID         string
	RevisionID       string
	Kind             domain.MemoryKind
	CanonicalHash    string
	CanonicalContent string
	Subjects         []string
	Objects          []string
	Attributes       map[string]string
	EvidenceEventIDs []string
	Similarity       *float64
}

// SetConsolidationClassifier installs the optional, policy-controlled provider
// classifier used only for candidates the deterministic rules leave unresolved.
func (r *Repository) SetConsolidationClassifier(classifier ConsolidationClassifier) {
	r.consolidationClassifier = classifier
}

// classifyConsolidation applies the deterministic exact rules first. Semantic
// similarity can only reinforce a deterministic tuple or shared-evidence match;
// cosine alone never supersedes, contests, deletes, or attaches.
func classifyConsolidation(rules consolidationRules, candidate domain.MemoryRevision, neighbor *consolidationNeighbor, sim consolidationSimilarity) (domain.ConsolidationAction, string) {
	if neighbor == nil {
		return domain.ConsolidationCreate, "novel"
	}
	candidateHash := candidate.CanonicalHash
	if candidateHash == "" {
		candidateHash = canonicalContentHash(candidate.CanonicalContent)
	}
	sameKind := candidate.Kind == neighbor.Kind
	sameSubjectKind := sameKind && equalStringSets(candidate.SubjectEntityIDs, neighbor.Subjects)
	sameEvent := sharesEvent(candidate.Evidence, neighbor)
	attrConflict := attributesConflict(candidate.Attributes, neighbor.Attributes)
	timeCompatible := validTimeCompatible(candidate, neighbor)
	timeOrdered := validTimeOrdered(candidate, neighbor)

	if rules.ExactHashEnabled && candidateHash != "" && candidateHash == neighbor.CanonicalHash && sameKind {
		if attributesConflict(candidate.Attributes, neighbor.Attributes) {
			return domain.ConsolidationRefine, "exact_hash_attribute_delta"
		}
		if evidenceSubset(candidate.Evidence, neighbor) {
			return domain.ConsolidationReject, "redundant_evidence"
		}
		return domain.ConsolidationAttachEvidence, "exact_canonical_hash"
	}

	if sameSubjectKind && attrConflict {
		switch {
		case timeOrdered:
			return domain.ConsolidationSupersede, "ordered_attribute_conflict"
		case timeCompatible:
			return domain.ConsolidationContest, "overlapping_attribute_conflict"
		default:
			return domain.ConsolidationCreate, "disjoint_attribute_conflict"
		}
	}

	if sameEvent && sim.OK && sim.Cosine >= rules.SameSourceEventCosine {
		if attrConflict {
			return domain.ConsolidationCreate, "cosine_conflict_no_merge"
		}
		if evidenceSubset(candidate.Evidence, neighbor) {
			return domain.ConsolidationReject, "redundant_evidence"
		}
		return domain.ConsolidationAttachEvidence, "same_source_event"
	}

	if sameSubjectKind && timeCompatible && sim.OK && sim.Cosine >= rules.SameSubjectKindCosine {
		if attrConflict {
			return domain.ConsolidationCreate, "cosine_conflict_no_merge"
		}
		if evidenceSubset(candidate.Evidence, neighbor) {
			return domain.ConsolidationReject, "redundant_evidence"
		}
		if morePrecise(candidate, neighbor) {
			return domain.ConsolidationRefine, "more_precise"
		}
		return domain.ConsolidationAttachEvidence, "subject_kind_semantic"
	}

	return domain.ConsolidationCreate, "novel"
}

// validateProviderConsolidation accepts a provider proposal only when the
// deterministic rules left the candidate unresolved. Destructive actions
// require an independently detected deterministic conflict; cosine alone cannot
// authorize them.
func validateProviderConsolidation(deterministic consolidationOutcome, proposal ConsolidationProviderProposal, conflictDetected bool) (consolidationOutcome, bool) {
	if deterministic.Action != domain.ConsolidationCreate {
		return consolidationOutcome{}, false
	}
	if !proposal.Action.Valid() || proposal.ReasonCode == "" {
		return consolidationOutcome{}, false
	}
	switch proposal.Action {
	case domain.ConsolidationSupersede, domain.ConsolidationContest:
		if !conflictDetected {
			return consolidationOutcome{}, false
		}
	}
	return consolidationOutcome{
		Action:           proposal.Action,
		ReasonCode:       "provider_" + proposal.ReasonCode,
		TargetMemoryID:   proposal.MemoryID,
		TargetRevisionID: proposal.RevisionID,
		Similarity:       proposal.Similarity,
		Classification:   domain.ConsolidationProvider,
	}, true
}

func canonicalContentHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func attributesConflict(a, b map[string]string) bool {
	for key, value := range a {
		if other, ok := b[key]; ok && other != value {
			return true
		}
	}
	return false
}

func sharesEvent(evidence []domain.RevisionEvidence, neighbor *consolidationNeighbor) bool {
	if len(evidence) == 0 || len(neighbor.EvidenceRanges) == 0 {
		return false
	}
	for _, item := range evidence {
		for _, existing := range neighbor.EvidenceRanges {
			if existing.EventID == item.EventID {
				return true
			}
		}
	}
	return false
}

func evidenceSubset(evidence []domain.RevisionEvidence, neighbor *consolidationNeighbor) bool {
	if len(evidence) == 0 {
		return false
	}
	for _, item := range evidence {
		found := false
		for _, existing := range neighbor.EvidenceRanges {
			if existing.EventID == item.EventID && equalOptionalInt64(existing.StartOffset, item.StartOffset) && equalOptionalInt64(existing.EndOffset, item.EndOffset) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func equalOptionalInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func validTimeCompatible(candidate domain.MemoryRevision, neighbor *consolidationNeighbor) bool {
	return intervalsOverlap(candidate.ValidFrom, candidate.ValidUntil, neighbor.ValidFrom, neighbor.ValidUntil)
}

func validTimeOrdered(candidate domain.MemoryRevision, neighbor *consolidationNeighbor) bool {
	if candidate.ValidFrom != nil && neighbor.ValidUntil != nil && candidate.ValidFrom.After(*neighbor.ValidUntil) {
		return true
	}
	if neighbor.ValidFrom != nil && candidate.ValidUntil != nil && neighbor.ValidFrom.After(*candidate.ValidUntil) {
		return true
	}
	return false
}

func intervalsOverlap(aStart, aEnd, bStart, bEnd *time.Time) bool {
	if aStart != nil && bEnd != nil && !aStart.Before(*bEnd) {
		return false
	}
	if bStart != nil && aEnd != nil && !bStart.Before(*aEnd) {
		return false
	}
	return true
}

func morePrecise(candidate domain.MemoryRevision, neighbor *consolidationNeighbor) bool {
	if len(candidate.Attributes) > len(neighbor.Attributes) {
		return true
	}
	if candidate.Specificity > neighbor.Specificity {
		return true
	}
	return timePrecisionRank(candidate.TimePrecision) > timePrecisionRank(neighbor.Precision)
}

func timePrecisionRank(precision domain.TimePrecision) int {
	switch precision {
	case domain.TimePrecisionExact:
		return 5
	case domain.TimePrecisionDay:
		return 4
	case domain.TimePrecisionMonth:
		return 3
	case domain.TimePrecisionYear:
		return 2
	case domain.TimePrecisionApproximate:
		return 1
	default:
		return 0
	}
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
		var precision string
		if err := rows.Scan(&item.MemoryID, &item.RevisionID, &item.Kind, &item.Lifecycle, &item.Frozen, &item.CanonicalHash, &item.CanonicalContent, &item.EventType, &validFrom, &validUntil, &item.Specificity, &precision); err != nil {
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
// deterministic classification. The provider classifier is consulted only when
// the deterministic rules leave the candidate unresolved, and its result remains
// a proposal validated against the cosine-only prohibition.
func (r *Repository) classifyCandidateForConsolidation(ctx context.Context, tx *sql.Tx, candidate domain.Candidate, revision domain.MemoryRevision) (consolidationOutcome, bool, error) {
	neighbors, err := r.boundedConsolidationRecall(ctx, tx, candidate.SpaceID, revision)
	if err != nil {
		return consolidationOutcome{}, false, err
	}
	rules := r.consolidationRules()
	outcome := consolidationOutcome{Action: domain.ConsolidationCreate, ReasonCode: "novel", Classification: domain.ConsolidationNone}
	matched := false
	for i := range neighbors {
		action, reason := classifyConsolidation(rules, revision, &neighbors[i], consolidationSimilarity{})
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
		proposal, err := r.consolidationClassifier.ClassifyConsolidation(ctx, revision, exportedNeighbors(neighbors))
		if err != nil {
			return consolidationOutcome{}, false, err
		}
		conflict := false
		for i := range neighbors {
			if revision.Kind == neighbors[i].Kind && equalStringSets(revision.SubjectEntityIDs, neighbors[i].Subjects) && attributesConflict(revision.Attributes, neighbors[i].Attributes) {
				conflict = true
				break
			}
		}
		if classified, ok := validateProviderConsolidation(outcome, proposal, conflict); ok {
			outcome = classified
		}
	}
	if !outcome.Action.Valid() {
		outcome = consolidationOutcome{Action: domain.ConsolidationCreate, ReasonCode: "novel", Classification: domain.ConsolidationNone}
	}
	return outcome, matched, nil
}

func exportedNeighbors(neighbors []consolidationNeighbor) []ConsolidationNeighbor {
	result := make([]ConsolidationNeighbor, len(neighbors))
	for i := range neighbors {
		result[i] = ConsolidationNeighbor{
			MemoryID: neighbors[i].MemoryID, RevisionID: neighbors[i].RevisionID, Kind: neighbors[i].Kind,
			CanonicalHash: neighbors[i].CanonicalHash, CanonicalContent: neighbors[i].CanonicalContent,
			Subjects: neighbors[i].Subjects, Objects: neighbors[i].Objects, Attributes: neighbors[i].Attributes,
		}
		for _, item := range neighbors[i].EvidenceRanges {
			result[i].EvidenceEventIDs = append(result[i].EvidenceEventIDs, item.EventID)
		}
	}
	return result
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
