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

	"github.com/N0rthHXD/dMem/internal/domain"
)

const graphIndexName = "graph_recall"

const autoReviewVersion = "auto-review-v1"
const autoReviewIdentity = "system:auto-review-v1"

const (
	autoConfirmEntityConfidence   = 0.9
	autoConfirmFactConfidence     = 0.8
	autoConfirmRelationConfidence = 0.9
)

var (
	ErrInvalidFactProposal  = errors.New("invalid entity fact proposal")
	ErrFactProposalReviewed = errors.New("entity fact proposal has already been reviewed")
)

// scheduleGraphExtraction enqueues an idempotent graph extraction job for a
// newly confirmed memory revision. It runs inside the same write transaction
// that materializes the revision so the job is never lost or duplicated.
func (r *Repository) scheduleGraphExtraction(ctx context.Context, tx *sql.Tx, revisionID string, now string) error {
	if !r.graphExtractionOn() {
		return nil
	}
	ids, err := r.newIDs(2)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES (?, ?, 'extract_graph', 'memory_revision', ?, ?, 'pending', ?, ?, ?) ON CONFLICT DO NOTHING`, ids[0], ids[1], revisionID, r.processingVersion, r.maxAttempts, now, now)
	return err
}

func (r *Repository) PrepareGraphExtraction(ctx context.Context, claim ClaimedJob, params ProcessingPreparationParams) (domain.GraphExtractionInput, error) {
	if claim.Job.JobType != "extract_graph" || claim.Job.TargetType != "memory_revision" {
		return domain.GraphExtractionInput{}, ErrInvalidJobState
	}
	var status, owner, revisionID string
	var generation int
	err := r.store.reader.QueryRowContext(ctx, `SELECT status, lease_owner, lease_generation, target_id FROM jobs WHERE job_id=? AND lease_until>?`, claim.Job.JobID, formatTime(r.runtime.Now())).Scan(&status, &owner, &generation, &revisionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.GraphExtractionInput{}, ErrLeaseLost
		}
		return domain.GraphExtractionInput{}, err
	}
	if status != "running" || owner != claim.LeaseOwner || generation != claim.LeaseGeneration {
		return domain.GraphExtractionInput{}, ErrLeaseLost
	}
	var memoryID, spaceID, content, sensitivity string
	var currentRevision string
	var validFrom, validUntil sql.NullString
	err = r.store.reader.QueryRowContext(ctx, `SELECT r.memory_id, m.space_id, r.content, m.sensitivity, m.current_revision_id, r.valid_from, r.valid_until FROM memory_revisions r JOIN memories m ON m.memory_id=r.memory_id WHERE r.revision_id=?`, revisionID).Scan(&memoryID, &spaceID, &content, &sensitivity, &currentRevision, &validFrom, &validUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GraphExtractionInput{}, ErrNotFound
	}
	if err != nil {
		return domain.GraphExtractionInput{}, err
	}
	if currentRevision != revisionID {
		return domain.GraphExtractionInput{}, ErrRevisionConflict
	}
	if err := r.RequireEffectivePolicy(ctx, "processing", spaceID); err != nil {
		return domain.GraphExtractionInput{}, err
	}
	input := domain.GraphExtractionInput{
		ProcessorIdentity: params.ProcessorIdentity, ProcessingVersion: claim.ProcessingVersion, JobID: claim.Job.JobID,
		Attempt: claim.Job.AttemptCount, SpaceID: spaceID, MemoryID: memoryID, RevisionID: revisionID,
		Content: content, Sensitivity: sensitivity, CurrentState: "active",
		Policy: domain.GraphExtractionPolicy{MaxEntities: max(1, params.MaxCandidates), MaxFacts: max(1, params.MaxCandidates), MaxRelations: max(1, params.MaxCandidates), MaxAliasesPerEntity: 8, MaxContentChars: params.MaxContentChars},
	}
	if validFrom.Valid {
		value, err := time.Parse(time.RFC3339Nano, validFrom.String)
		if err != nil {
			return domain.GraphExtractionInput{}, err
		}
		input.ValidFrom = &value
	}
	if validUntil.Valid {
		value, err := time.Parse(time.RFC3339Nano, validUntil.String)
		if err != nil {
			return domain.GraphExtractionInput{}, err
		}
		input.ValidUntil = &value
	}
	if params.MaxContextMemories > 0 {
		scope, err := memoryScope(ctx, r.store.reader, spaceID, true)
		if err != nil {
			return domain.GraphExtractionInput{}, err
		}
		rows, err := r.store.reader.QueryContext(ctx, `SELECT m.memory_id, r.revision_id, m.space_id, r.content, m.current_state, m.sensitivity, r.valid_from, r.valid_until FROM memories m JOIN memory_revisions r ON r.revision_id=m.current_revision_id WHERE m.space_id IN (`+placeholders(len(scope))+`) AND m.current_state<>'deleted' AND m.memory_id<>? ORDER BY m.updated_at DESC, m.memory_id DESC LIMIT ?`, appendStrings(append(scope, memoryID), params.MaxContextMemories)...)
		if err != nil {
			return domain.GraphExtractionInput{}, err
		}
		for rows.Next() {
			var item domain.ProcessingMemory
			var from, until sql.NullString
			if err := rows.Scan(&item.MemoryID, &item.RevisionID, &item.SpaceID, &item.Content, &item.CurrentState, &item.Sensitivity, &from, &until); err != nil {
				rows.Close()
				return domain.GraphExtractionInput{}, err
			}
			if from.Valid {
				value, err := time.Parse(time.RFC3339Nano, from.String)
				if err != nil {
					rows.Close()
					return domain.GraphExtractionInput{}, err
				}
				item.ValidFrom = &value
			}
			if until.Valid {
				value, err := time.Parse(time.RFC3339Nano, until.String)
				if err != nil {
					rows.Close()
					return domain.GraphExtractionInput{}, err
				}
				item.ValidUntil = &value
			}
			item.ProposalCapable = item.SpaceID == spaceID
			input.ContextMemories = append(input.ContextMemories, item)
		}
		if err := rows.Close(); err != nil {
			return domain.GraphExtractionInput{}, err
		}
	}
	return input, nil
}

type GraphExtractionCommit struct {
	Result domain.GraphExtractionResult
}

func (r *Repository) CommitGraphExtraction(ctx context.Context, claim ClaimedJob, input domain.GraphExtractionInput, commit GraphExtractionCommit) error {
	result := commit.Result
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.requireGraphFence(ctx, tx, claim, input); err != nil {
		return err
	}
	now := formatTime(r.runtime.Now())
	if result.Code == domain.GraphExtractionNoProcessor || result.Code == domain.GraphExtractionEmpty {
		return r.finishGraphJob(ctx, tx, claim, input, now, string(result.Code), "empty")
	}
	eventID, err := graphSourceEventID(ctx, tx, input.RevisionID)
	if err != nil {
		return err
	}
	entityMap, err := r.resolveGraphEntities(ctx, tx, input, eventID, result.Entities, now)
	if err != nil {
		return err
	}
	if err := r.commitGraphMentions(ctx, tx, input, result.Mentions, entityMap, now); err != nil {
		return err
	}
	if err := r.commitGraphFacts(ctx, tx, input, eventID, result.Facts, entityMap, now); err != nil {
		return err
	}
	if err := r.commitGraphRelations(ctx, tx, input, eventID, result.Relations, now); err != nil {
		return err
	}
	return r.finishGraphJob(ctx, tx, claim, input, now, string(result.Code), "extracted")
}

// graphSourceEventID returns a stable raw event for graph proposal provenance,
// derived from the source revision's own evidence chain.
func graphSourceEventID(ctx context.Context, tx *sql.Tx, revisionID string) (string, error) {
	var eventID string
	err := tx.QueryRowContext(ctx, `SELECT event_id FROM memory_evidence WHERE revision_id=? ORDER BY evidence_id LIMIT 1`, revisionID).Scan(&eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidEvidence
	}
	return eventID, err
}

func (r *Repository) finishGraphJob(ctx context.Context, tx *sql.Tx, claim ClaimedJob, input domain.GraphExtractionInput, now, resultCode, summary string) error {
	value := map[string]any{"result_code": resultCode, "summary": summary, "provider_identity": input.ProcessorIdentity}
	encoded, _ := json.Marshal(value)
	updated, err := tx.ExecContext(ctx, `UPDATE jobs SET status='succeeded', processor_identity=?, lease_owner=NULL, lease_until=NULL, result_code=?, result_json=?, last_error_code=NULL, last_error_message=NULL, finished_at=? WHERE job_id=? AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, input.ProcessorIdentity, resultCode, string(encoded), now, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, now)
	if err != nil {
		return err
	}
	if !oneRow(updated) {
		return ErrLeaseLost
	}
	if err := closeAttempt(ctx, tx, claim, now, "succeeded", resultCode, "", ""); err != nil {
		return err
	}
	auditID, err := r.runtime.ID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, reason, created_at) VALUES (?,?,?,?, 'extract_graph','memory_revision',?,'succeeded',?,?)`, auditID, claim.Job.OperationID, claim.AttemptID, input.ProcessorIdentity, input.RevisionID, summary, now); err != nil {
		return err
	}
	if err := r.markGraphIndexStale(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) requireGraphFence(ctx context.Context, tx *sql.Tx, claim ClaimedJob, input domain.GraphExtractionInput) error {
	var targetID string
	var cfgGen, polGen sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT target_id, configuration_generation, policy_generation FROM jobs WHERE job_id=? AND job_type='extract_graph' AND target_type='memory_revision' AND status='running' AND lease_owner=? AND lease_generation=? AND lease_until>?`, claim.Job.JobID, claim.LeaseOwner, claim.LeaseGeneration, formatTime(r.runtime.Now())).Scan(&targetID, &cfgGen, &polGen)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if targetID != input.RevisionID || input.JobID != claim.Job.JobID {
		return ErrLeaseLost
	}
	if r.currentProcessingGeneration() > 0 || cfgGen.Valid || polGen.Valid {
		if int(cfgGen.Int64) != r.currentProcessingGeneration() || int(polGen.Int64) != r.currentProcessingPolicyGeneration() {
			return ErrStaleGeneration
		}
	}
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT current_revision_id FROM memories WHERE memory_id=?`, input.MemoryID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRevisionConflict
		}
		return err
	}
	if current != input.RevisionID {
		return ErrRevisionConflict
	}
	return nil
}

func (r *Repository) markGraphIndexStale(ctx context.Context, tx *sql.Tx, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE graph_index_metadata SET state='stale', generation=generation+1, updated_at=? WHERE singleton=1`, now); err != nil {
		return err
	}
	ids, err := r.newIDs(2)
	if err != nil {
		return err
	}
	_, err = enqueueGraphIndexJob(ctx, tx, ids[0], ids[1], r.processingVersion, r.maxAttempts, now)
	return err
}

// resolveGraphEntities maps provider local entity ids to authoritative entity
// ids, creating (and possibly auto-confirming) entity proposals as needed.
func (r *Repository) resolveGraphEntities(ctx context.Context, tx *sql.Tx, input domain.GraphExtractionInput, eventID string, entities []domain.GraphEntity, now string) (map[string]string, error) {
	entityMap := make(map[string]string, len(entities))
	for _, entity := range entities {
		resolved := resolveEntityForBuild(ctx, tx, input.SpaceID, entity.CanonicalName, entity.EntityType)
		switch {
		case resolved.found:
			entityMap[entity.LocalID] = resolved.entityID
			for _, alias := range entity.Aliases {
				if err := r.autoConfirmAlias(ctx, tx, input, resolved.entityID, alias, now); err != nil {
					return nil, err
				}
			}
		case resolved.ambiguous:
			if _, err := r.createPendingEntityProposal(ctx, tx, input, eventID, entity, now); err != nil {
				return nil, err
			}
		default:
			if entity.Confidence >= autoConfirmEntityConfidence {
				entityID, err := r.autoConfirmEntity(ctx, tx, input, entity, now)
				if err != nil {
					return nil, err
				}
				entityMap[entity.LocalID] = entityID
			} else {
				if _, err := r.createPendingEntityProposal(ctx, tx, input, eventID, entity, now); err != nil {
					return nil, err
				}
			}
		}
	}
	return entityMap, nil
}

type buildResolution struct {
	found     bool
	ambiguous bool
	entityID  string
}

func resolveEntityForBuild(ctx context.Context, tx *sql.Tx, spaceID, canonicalName, entityType string) buildResolution {
	normalized := normalizeEntityName(canonicalName)
	rows, err := tx.QueryContext(ctx, `SELECT e.entity_id, e.entity_type FROM entities e WHERE e.space_id=? AND e.state='active' AND e.normalized_name=?`, spaceID, normalized)
	if err != nil {
		return buildResolution{}
	}
	defer rows.Close()
	var matches []struct{ id, typ string }
	for rows.Next() {
		var id, typ string
		if err := rows.Scan(&id, &typ); err != nil {
			return buildResolution{}
		}
		matches = append(matches, struct{ id, typ string }{id, typ})
	}
	if len(matches) == 1 && (matches[0].typ == entityType || entityType == "other" || matches[0].typ == "other") {
		return buildResolution{found: true, entityID: matches[0].id}
	}
	if len(matches) >= 1 {
		if len(matches) == 1 && matches[0].typ != entityType {
			return buildResolution{ambiguous: true}
		}
		return buildResolution{ambiguous: true}
	}
	rows2, err := tx.QueryContext(ctx, `SELECT a.entity_id, e.entity_type FROM entity_aliases a JOIN entities e ON e.entity_id=a.entity_id WHERE e.space_id=? AND e.state='active' AND a.state='active' AND a.normalized_alias=?`, spaceID, normalized)
	if err != nil {
		return buildResolution{}
	}
	defer rows2.Close()
	var aliasMatches []struct{ id, typ string }
	for rows2.Next() {
		var id, typ string
		if err := rows2.Scan(&id, &typ); err != nil {
			return buildResolution{}
		}
		aliasMatches = append(aliasMatches, struct{ id, typ string }{id, typ})
	}
	if len(aliasMatches) == 1 && (aliasMatches[0].typ == entityType || entityType == "other" || aliasMatches[0].typ == "other") {
		return buildResolution{found: true, entityID: aliasMatches[0].id}
	}
	if len(aliasMatches) >= 1 {
		return buildResolution{ambiguous: true}
	}
	return buildResolution{}
}

func (r *Repository) autoConfirmEntity(ctx context.Context, tx *sql.Tx, input domain.GraphExtractionInput, entity domain.GraphEntity, now string) (string, error) {
	ids, err := r.newIDs(3 + len(entity.Aliases))
	if err != nil {
		return "", err
	}
	entityID, canonicalAliasID := ids[0], ids[1]
	norm := normalizeEntityName(entity.CanonicalName)
	if _, err := tx.ExecContext(ctx, `INSERT INTO entities(entity_id, space_id, entity_type, canonical_name, normalized_name, normalization_version, state, sensitivity, created_by, processing_version, created_at, updated_at) VALUES (?,?,?,?,?,?,'active',?,?,?,?,?)`, entityID, input.SpaceID, entity.EntityType, entity.CanonicalName, norm, "v1", input.Sensitivity, autoReviewIdentity, input.ProcessingVersion, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO entity_events(entity_event_id, entity_id, operation_id, event_type, to_canonical_name, to_state, normalization_version, reason, created_by, created_at) VALUES (?,?,?,'created',?,'active','v1',?,?,?)`, ids[2], entityID, input.JobID, entity.CanonicalName, "auto_review", autoReviewIdentity, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO entity_aliases(alias_id, entity_id, alias, normalized_alias, normalization_version, source, state, created_by, created_at, updated_at) VALUES (?,?,?,?,?,'processor_review','active',?,?,?)`, canonicalAliasID, entityID, entity.CanonicalName, norm, "v1", autoReviewIdentity, now, now); err != nil {
		return "", err
	}
	for i, alias := range entity.Aliases {
		aliasNorm := normalizeEntityName(alias)
		if aliasNorm == norm {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entity_aliases(alias_id, entity_id, alias, normalized_alias, normalization_version, source, state, created_by, created_at, updated_at) VALUES (?,?,?,?,?,'processor_review','active',?,?,?)`, ids[3+i], entityID, alias, aliasNorm, "v1", autoReviewIdentity, now, now); err != nil {
			return "", err
		}
	}
	if err := r.markEntityIndexStale(ctx, tx, now); err != nil {
		return "", err
	}
	return entityID, nil
}

func (r *Repository) createPendingEntityProposal(ctx context.Context, tx *sql.Tx, input domain.GraphExtractionInput, eventID string, entity domain.GraphEntity, now string) (string, error) {
	id, err := r.runtime.ID()
	if err != nil {
		return "", err
	}
	alias := ""
	if len(entity.Aliases) > 0 {
		alias = entity.Aliases[0]
	}
	normVersion := ""
	if alias != "" {
		normVersion = "v1"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO entity_proposals(proposal_id, event_id, space_id, proposal_kind, entity_type, canonical_name, alias, normalization_version, sensitivity, processor_identity, provider_identity, model_identity, processing_version, specification_hash, created_at) VALUES (?,?,?,'entity',?,?,?,?,?,?,?,?,?,?,?)`, id, eventID, input.SpaceID, entity.EntityType, entity.CanonicalName, nullableString(alias), nullableString(normVersion), input.Sensitivity, input.ProcessorIdentity, stringPointer(input.ProcessorIdentity), stringPointer(""), input.ProcessingVersion, graphEntitySpecHash(entity), now)
	return id, err
}

func (r *Repository) autoConfirmAlias(ctx context.Context, tx *sql.Tx, input domain.GraphExtractionInput, entityID, alias string, now string) error {
	norm := normalizeEntityName(alias)
	if norm == "" {
		return nil
	}
	var collision string
	err := tx.QueryRowContext(ctx, `SELECT a.entity_id FROM entity_aliases a JOIN entities e ON e.entity_id=a.entity_id WHERE e.space_id=? AND a.state='active' AND a.normalized_alias=? AND a.entity_id<>? LIMIT 1`, input.SpaceID, norm, entityID).Scan(&collision)
	if err == nil {
		return r.createMergeProposal(ctx, tx, input, entityID, collision, norm, now)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT alias_id FROM entity_aliases WHERE entity_id=? AND normalized_alias=? AND state='active'`, entityID, norm).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	id, err := r.runtime.ID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO entity_aliases(alias_id, entity_id, alias, normalized_alias, normalization_version, source, state, created_by, created_at, updated_at) VALUES (?,?,?,?,?,'processor_review','active',?,?,?)`, id, entityID, alias, norm, "v1", autoReviewIdentity, now, now); err != nil {
		return err
	}
	return r.markEntityIndexStale(ctx, tx, now)
}

func (r *Repository) createMergeProposal(ctx context.Context, tx *sql.Tx, input domain.GraphExtractionInput, entityID, otherID, norm string, now string) error {
	h := sha256.Sum256([]byte(otherID + "\x00" + entityID + "\x00" + norm))
	id, err := r.runtime.ID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO entity_merge_proposals(merge_proposal_id, space_id, source_entity_id, target_entity_id, reason, processing_version, specification_hash, created_by, created_at) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, id, input.SpaceID, entityID, otherID, "alias_normalization_collision", input.ProcessingVersion, hex.EncodeToString(h[:]), autoReviewIdentity, now)
	return err
}

func (r *Repository) commitGraphMentions(ctx context.Context, tx *sql.Tx, input domain.GraphExtractionInput, mentions []domain.GraphMention, entityMap map[string]string, now string) error {
	runes := []rune(input.Content)
	for _, mention := range mentions {
		entityID, ok := entityMap[mention.EntityLocalID]
		if !ok || entityID == "" {
			continue
		}
		if mention.StartRune < 0 || mention.EndRune <= mention.StartRune || mention.EndRune > len(runes) {
			return fmt.Errorf("%w: mention rune range", ErrInvalidEntityProposal)
		}
		surface := string(runes[mention.StartRune:mention.EndRune])
		if surface != mention.Text {
			return fmt.Errorf("%w: mention surface mismatch", ErrInvalidEntityProposal)
		}
		id, err := r.runtime.ID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entity_mentions(mention_id, entity_id, memory_id, revision_id, start_offset, end_offset, mention_type, source, surface_text, created_by, processing_version, created_at) VALUES (?,?,?,?,?,?,?, 'processor',?,?,?,?) ON CONFLICT DO NOTHING`, id, entityID, input.MemoryID, input.RevisionID, mention.StartRune, mention.EndRune, "explicit", surface, autoReviewIdentity, input.ProcessingVersion, now); err != nil {
			return err
		}
	}
	return r.markEntityIndexStale(ctx, tx, now)
}

func (r *Repository) commitGraphFacts(ctx context.Context, tx *sql.Tx, input domain.GraphExtractionInput, eventID string, facts []domain.GraphFact, entityMap map[string]string, now string) error {
	for _, fact := range facts {
		subjectID, ok := entityMap[fact.SubjectLocalID]
		if !ok || subjectID == "" {
			continue
		}
		var objectEntityID *string
		var objectLiteral, objectLiteralType *string
		if fact.ObjectLocalID != "" {
			id, ok := entityMap[fact.ObjectLocalID]
			if !ok || id == "" {
				continue
			}
			objectEntityID = &id
		} else {
			lit, typ := fact.ObjectLiteral, fact.ObjectLiteralType
			objectLiteral, objectLiteralType = &lit, &typ
		}
		confidence := fact.Confidence
		start, end := 0, len([]rune(input.Content))
		params := createFactProposalParams{
			EventID: eventID, SpaceID: input.SpaceID, SubjectEntityID: subjectID, Predicate: fact.Predicate,
			ObjectEntityID: objectEntityID, ObjectLiteral: objectLiteral, ObjectLiteralType: objectLiteralType,
			SourceMemoryID: input.MemoryID, SourceRevisionID: input.RevisionID, Confidence: &confidence,
			ValidFrom: timePointerText(fact.ValidFrom), ValidUntil: timePointerText(fact.ValidUntil),
			Evidence:          []domain.EntityFactEvidence{{EvidenceType: "revision_range", MemoryID: &input.MemoryID, RevisionID: &input.RevisionID, StartOffset: &start, EndOffset: &end}},
			ProcessorIdentity: input.ProcessorIdentity, ProviderIdentity: stringPointer(input.ProcessorIdentity), ModelIdentity: stringPointer(""),
			ProcessingVersion: input.ProcessingVersion,
		}
		autoConfirm := confidence >= autoConfirmFactConfidence && !r.factConflictExists(ctx, tx, params)
		proposalID, err := r.insertFactProposal(ctx, tx, params, now)
		if err != nil {
			return err
		}
		if autoConfirm {
			if err := r.confirmFactProposalTx(ctx, tx, proposalID, autoReviewIdentity, "auto_review", autoReviewVersion, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Repository) commitGraphRelations(ctx context.Context, tx *sql.Tx, input domain.GraphExtractionInput, eventID string, relations []domain.GraphMemoryRelation, now string) error {
	for _, relation := range relations {
		confidence := relation.Confidence
		evidence := make([]domain.RelationEvidence, 0, len(relation.Evidence))
		for _, ref := range relation.Evidence {
			start, end := ref.StartRune, ref.EndRune
			evidence = append(evidence, domain.RelationEvidence{EvidenceType: "revision_range", MemoryID: &input.MemoryID, RevisionID: &input.RevisionID, StartOffset: &start, EndOffset: &end})
		}
		params := CreateRelationProposalParams{
			EventID: eventID, SpaceID: input.SpaceID, RelationType: relation.RelationType,
			SourceMemoryID: input.MemoryID, SourceRevisionID: input.RevisionID,
			TargetMemoryID: relation.TargetMemoryID, TargetRevisionID: relation.TargetRevisionID,
			Confidence: &confidence, Evidence: evidence, ProcessorIdentity: input.ProcessorIdentity, ProcessingVersion: input.ProcessingVersion,
			ProviderIdentity: stringPointer(input.ProcessorIdentity), ModelIdentity: stringPointer(""),
		}
		params = canonicalizeRelation(params)
		if err := validateRelationContext(ctx, tx, params, true); err != nil {
			return err
		}
		proposalID, err := r.insertRelationProposal(ctx, tx, params, now)
		if err != nil {
			return err
		}
		autoConfirm := relationAutoConfirm(relation.RelationType, confidence)
		if autoConfirm {
			if err := r.confirmRelationProposalTx(ctx, tx, proposalID, autoReviewIdentity, "auto_review", now); err != nil {
				return err
			}
		}
	}
	return nil
}

func relationAutoConfirm(relationType string, confidence float64) bool {
	switch relationType {
	case "related_to":
		return true
	case "supports", "derived_from":
		return confidence >= autoConfirmRelationConfidence
	default:
		return false
	}
}

func graphEntitySpecHash(entity domain.GraphEntity) string {
	aliases := append([]string(nil), entity.Aliases...)
	sort.Strings(aliases)
	value := struct {
		Name    string
		Type    string
		Aliases []string
	}{entity.CanonicalName, entity.EntityType, aliases}
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (r *Repository) markEntityIndexStale(ctx context.Context, tx *sql.Tx, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE entity_index_metadata SET state='stale', generation=generation+1, updated_at=? WHERE singleton=1`, now); err != nil {
		return err
	}
	ids, err := r.newIDs(2)
	if err != nil {
		return err
	}
	_, err = enqueueEntityIndexJob(ctx, tx, ids[0], ids[1], r.processingVersion, r.maxAttempts, now)
	return err
}

func (r *Repository) markRelationIndexStale(ctx context.Context, tx *sql.Tx, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE relation_index_metadata SET state='stale', generation=generation+1, updated_at=? WHERE singleton=1`, now); err != nil {
		return err
	}
	ids, err := r.newIDs(2)
	if err != nil {
		return err
	}
	_, err = enqueueRelationIndexJob(ctx, tx, ids[0], ids[1], r.processingVersion, r.maxAttempts, now)
	return err
}

// factConflictExists reports whether an active fact already asserts the same
// subject+predicate from a different source revision.
func (r *Repository) factConflictExists(ctx context.Context, tx *sql.Tx, p createFactProposalParams) bool {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM entity_facts WHERE space_id=? AND subject_entity_id=? AND predicate=? AND state='active' AND source_revision_id<>?`, p.SpaceID, p.SubjectEntityID, p.Predicate, p.SourceRevisionID).Scan(&count)
	return err == nil && count > 0
}
