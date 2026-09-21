package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/N0rthHXD/dMem/internal/domain"
)

type ConflictCaseFilter struct {
	SpaceID, State, Cursor string
	Limit                  int
}

type ResolveConflictParams domain.ConflictResolutionCommand

func (r *Repository) GetConflictCase(ctx context.Context, id string) (domain.ConflictCase, error) {
	return getConflictCase(ctx, r.store.reader, id)
}

func getConflictCase(ctx context.Context, q candidateQueryer, id string) (domain.ConflictCase, error) {
	var c domain.ConflictCase
	err := q.QueryRowContext(ctx, `SELECT case_id,space_id,state,opened_by,processing_version,created_at,closed_at FROM conflict_cases WHERE case_id=?`, id).Scan(&c.CaseID, &c.SpaceID, &c.State, &c.OpenedBy, &c.ProcessingVersion, &c.CreatedAt, &c.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.Participants = []domain.ConflictParticipant{}
	c.Relations = []domain.MemoryRelation{}
	rows, err := q.QueryContext(ctx, `SELECT memory_id,revision_id FROM conflict_case_participants WHERE case_id=? ORDER BY participant_order`, id)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var v domain.ConflictParticipant
		if err = rows.Scan(&v.MemoryID, &v.RevisionID); err != nil {
			rows.Close()
			return c, err
		}
		c.Participants = append(c.Participants, v)
	}
	if err = rows.Close(); err != nil {
		return c, err
	}
	rows, err = q.QueryContext(ctx, `SELECT relation_id FROM conflict_case_relations WHERE case_id=? ORDER BY relation_id`, id)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var rid string
		if err = rows.Scan(&rid); err != nil {
			rows.Close()
			return c, err
		}
		v, e := getRelation(ctx, q, rid)
		if e != nil {
			rows.Close()
			return c, e
		}
		c.Relations = append(c.Relations, v)
	}
	if err = rows.Close(); err != nil {
		return c, err
	}
	var v domain.ConflictResolutionEvent
	err = q.QueryRowContext(ctx, `SELECT resolution_event_id,resolution_type,from_state,to_state,reason,resolved_by,created_at FROM conflict_resolution_events WHERE case_id=?`, id).Scan(&v.ResolutionEventID, &v.ResolutionType, &v.FromState, &v.ToState, &v.Reason, &v.ResolvedBy, &v.CreatedAt)
	if err == nil {
		v.Results = []domain.EvolutionResultRef{}
		rows, e := q.QueryContext(ctx, `SELECT result_type,result_id FROM conflict_resolution_result_refs WHERE resolution_event_id=? ORDER BY result_ref_id`, v.ResolutionEventID)
		if e != nil {
			return c, e
		}
		for rows.Next() {
			var ref domain.EvolutionResultRef
			if e = rows.Scan(&ref.ResultType, &ref.ResultID); e != nil {
				rows.Close()
				return c, e
			}
			v.Results = append(v.Results, ref)
		}
		rows.Close()
		c.Resolution = &v
	} else if !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	return c, nil
}

func (r *Repository) ListConflictCases(ctx context.Context, f ConflictCaseFilter) (domain.ConflictCasePage, error) {
	query, args := `SELECT case_id FROM conflict_cases WHERE space_id=?`, []any{f.SpaceID}
	if f.State != "" {
		query += ` AND state=?`
		args = append(args, f.State)
	}
	if f.Cursor != "" {
		query += ` AND case_id<?`
		args = append(args, f.Cursor)
	}
	query += ` ORDER BY case_id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ConflictCasePage{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return domain.ConflictCasePage{}, err
	}
	page := domain.ConflictCasePage{Items: []domain.ConflictCase{}}
	for _, id := range ids {
		v, e := r.GetConflictCase(ctx, id)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, v)
	}
	if len(page.Items) > f.Limit {
		next := page.Items[f.Limit-1].CaseID
		page.NextCursor = &next
		page.Items = page.Items[:f.Limit]
	}
	return page, nil
}

func (r *Repository) ResolveConflict(ctx context.Context, identity WriteIdentity, id string, p ResolveConflictParams) (domain.ConflictCase, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConflictCase{}, false, err
	}
	defer tx.Rollback()
	if s, b, found, e := readIdempotency(ctx, tx, identity); e != nil {
		return domain.ConflictCase{}, false, e
	} else if found {
		var v domain.ConflictCase
		if s != 200 || json.Unmarshal([]byte(b), &v) != nil {
			return v, false, errors.New("invalid stored idempotency response")
		}
		return v, true, nil
	}
	c, err := getConflictCase(ctx, tx, id)
	if err != nil {
		return c, false, err
	}
	if c.State != "open" {
		return c, false, ErrConflictResolved
	}
	jobsBefore, err := resolutionJobIDs(ctx, tx)
	if err != nil {
		return c, false, err
	}
	if !contains([]string{"keep_both", "dismiss", "revise_one", "supersede", "reconcile"}, p.ResolutionType) {
		return c, false, ErrUnsupportedAction
	}
	actual := make([]domain.EvolutionTarget, len(c.Participants))
	for i, v := range c.Participants {
		actual[i] = domain.EvolutionTarget{MemoryID: v.MemoryID, ExpectedRevisionID: v.RevisionID}
	}
	if !exactEvolutionTargets(p.ExpectedTargets, actual) {
		return c, false, ErrRevisionConflict
	}
	if err = validateConflictResolutionParams(p, actual); err != nil {
		return c, false, err
	}
	for _, target := range actual {
		var revision, space string
		var frozen bool
		if err = tx.QueryRowContext(ctx, `SELECT current_revision_id,space_id,frozen FROM memories WHERE memory_id=?`, target.MemoryID).Scan(&revision, &space, &frozen); err != nil || revision != target.ExpectedRevisionID || space != c.SpaceID {
			return c, false, ErrRevisionConflict
		}
		if frozen {
			return c, false, errors.Join(ErrRevisionConflict, ErrFrozenRevision)
		}
	}
	ids, err := r.newIDs(3)
	if err != nil {
		return c, false, err
	}
	operationID, eventID, auditID := ids[0], ids[1], ids[2]
	now := formatTime(r.runtime.Now())
	refs := []domain.EvolutionResultRef{}
	currentRevisions := map[string]string{}
	for _, target := range actual {
		currentRevisions[target.MemoryID] = target.ExpectedRevisionID
	}
	if p.ResolutionType == "revise_one" || p.ResolutionType == "supersede" {
		newMemory := p.ResolutionType == "supersede" && *p.ReplacementMode == "new_memory"
		created, memoryID, revisionID, e := r.createConflictResolutionMemory(ctx, tx, c, p, *p.SelectedTarget, newMemory, operationID, identity.CallerID, now)
		if e != nil {
			return c, false, e
		}
		refs = append(refs, created...)
		if !newMemory {
			currentRevisions[memoryID] = revisionID
		}
		if p.ResolutionType == "supersede" {
			relationRefs, e := r.createResolutionRelation(ctx, tx, c.SpaceID, memoryID, revisionID, p.SelectedTarget.MemoryID, p.SelectedTarget.ExpectedRevisionID, operationID, identity.CallerID, p.Reason, now)
			if e != nil {
				return c, false, e
			}
			refs = append(refs, relationRefs...)
		}
	}
	if p.ResolutionType == "reconcile" {
		created, memoryID, revisionID, e := r.createConflictResolutionMemory(ctx, tx, c, p, actual[0], true, operationID, identity.CallerID, now)
		if e != nil {
			return c, false, e
		}
		refs = append(refs, created...)
		for _, target := range actual {
			relationRefs, e := r.createResolutionRelation(ctx, tx, c.SpaceID, memoryID, revisionID, target.MemoryID, target.ExpectedRevisionID, operationID, identity.CallerID, p.Reason, now)
			if e != nil {
				return c, false, e
			}
			refs = append(refs, relationRefs...)
		}
	}
	toState := "resolved"
	if p.ResolutionType == "dismiss" {
		toState = "dismissed"
	}
	for _, target := range actual {
		var other int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM conflict_case_participants cp JOIN conflict_cases cc ON cc.case_id=cp.case_id WHERE cp.memory_id=? AND cp.revision_id=? AND cc.state='open' AND cc.case_id<>?`, target.MemoryID, target.ExpectedRevisionID, id).Scan(&other); err != nil {
			return c, false, err
		}
		if other > 0 {
			continue
		}
		state, ok := p.ResultStates[target.MemoryID]
		if !ok || !contains([]string{"active", "archived", "contested", "superseded"}, state) {
			return c, false, ErrInvalidStateTransition
		}
		var from string
		if err = tx.QueryRowContext(ctx, `SELECT current_state FROM memories WHERE memory_id=?`, target.MemoryID).Scan(&from); err != nil {
			return c, false, err
		}
		if from == state {
			continue
		}
		revisionID := currentRevisions[target.MemoryID]
		res, e := tx.ExecContext(ctx, `UPDATE memories SET current_state=?,updated_at=? WHERE memory_id=? AND current_revision_id=?`, state, now, target.MemoryID, revisionID)
		if e != nil || !oneRow(res) {
			if e != nil {
				return c, false, e
			}
			return c, false, ErrRevisionConflict
		}
		stateID, e := r.runtime.ID()
		if e != nil {
			return c, false, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id,memory_id,revision_id,operation_id,from_state,to_state,from_frozen,to_frozen,reason,created_by,created_at) SELECT ?,memory_id,current_revision_id,?,?,?,frozen,frozen,?,?,? FROM memories WHERE memory_id=?`, stateID, operationID, from, state, p.Reason, identity.CallerID, now, target.MemoryID); e != nil {
			return c, false, e
		}
		m, e := getMemory(ctx, tx, target.MemoryID, false)
		if e != nil {
			return c, false, e
		}
		if e = r.upsertMemoryProjection(ctx, tx, m.MemoryID, m.CurrentRevision.RevisionID, m.SpaceID, state, now); e != nil {
			return c, false, e
		}
		if e = r.markDependentAggregatesStale(ctx, tx, target.MemoryID, now); e != nil {
			return c, false, e
		}
		refs = append(refs, domain.EvolutionResultRef{ResultType: "state_event", ResultID: stateID}, domain.EvolutionResultRef{ResultType: "projection", ResultID: target.MemoryID})
	}
	if p.ResolutionType == "dismiss" {
		for _, relation := range c.Relations {
			stateID, e := r.runtime.ID()
			if e != nil {
				return c, false, e
			}
			res, e := tx.ExecContext(ctx, `UPDATE memory_relations SET state='dismissed',updated_at=? WHERE relation_id=? AND state='active'`, now, relation.RelationID)
			if e != nil {
				return c, false, e
			}
			if oneRow(res) {
				if _, e = tx.ExecContext(ctx, `INSERT INTO relation_state_events(state_event_id,relation_id,operation_id,from_state,to_state,reason,created_by,created_at) VALUES (?,?,?,'active','dismissed',?,?,?)`, stateID, relation.RelationID, operationID, p.Reason, identity.CallerID, now); e != nil {
					return c, false, e
				}
				refs = append(refs, domain.EvolutionResultRef{ResultType: "state_event", ResultID: stateID})
			}
		}
	}
	jobsAfter, err := resolutionJobIDs(ctx, tx)
	if err != nil {
		return c, false, err
	}
	for jobID := range jobsAfter {
		if !jobsBefore[jobID] {
			refs = append(refs, domain.EvolutionResultRef{ResultType: "job", ResultID: jobID})
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE conflict_cases SET state=?,closed_at=? WHERE case_id=? AND state='open'`, toState, now, id)
	if err != nil || !oneRow(res) {
		if err != nil {
			return c, false, err
		}
		return c, false, ErrConflictResolved
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO conflict_resolution_events(resolution_event_id,case_id,operation_id,resolution_type,from_state,to_state,reason,resolved_by,created_at) VALUES (?,?,?,?,'open',?,?,?,?)`, eventID, id, operationID, p.ResolutionType, toState, p.Reason, identity.CallerID, now); err != nil {
		return c, false, err
	}
	seenRefs := map[domain.EvolutionResultRef]bool{}
	for _, ref := range refs {
		if seenRefs[ref] {
			continue
		}
		seenRefs[ref] = true
		refID, e := r.runtime.ID()
		if e != nil {
			return c, false, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO conflict_resolution_result_refs(result_ref_id,resolution_event_id,result_type,result_id,created_at) VALUES (?,?,?,?,?)`, refID, eventID, ref.ResultType, ref.ResultID, now); e != nil {
			return c, false, e
		}
	}
	c, err = getConflictCase(ctx, tx, id)
	if err != nil {
		return c, false, err
	}
	body, _ := json.Marshal(c)
	if err = writeIdempotency(ctx, tx, identity, operationID, 200, body, now); err != nil {
		return c, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,reason,created_at) VALUES (?,?,?,?,'resolve_conflict','conflict_case',?,'succeeded',?,?)`, auditID, operationID, identity.RequestID, identity.CallerID, id, p.Reason, now); err != nil {
		return c, false, err
	}
	if err = tx.Commit(); err != nil {
		return c, false, err
	}
	return c, false, nil
}

func resolutionJobIDs(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT job_id FROM jobs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

func validateConflictResolutionParams(p ResolveConflictParams, participants []domain.EvolutionTarget) error {
	if p.Reason == "" {
		return ErrInvalidEvolutionProposal
	}
	destructive := p.ResolutionType == "revise_one" || p.ResolutionType == "supersede" || p.ResolutionType == "reconcile"
	if !destructive {
		if p.SelectedTarget != nil || p.ProposedContent != nil || p.ValidFrom != nil || p.ValidUntil != nil || len(p.Evidence) != 0 || p.ReplacementMode != nil {
			return ErrInvalidEvolutionProposal
		}
	} else {
		if p.ProposedContent == nil || *p.ProposedContent == "" || len(p.Evidence) == 0 || p.ValidFrom != nil && p.ValidUntil != nil && *p.ValidUntil <= *p.ValidFrom {
			return ErrInvalidEvolutionProposal
		}
		if p.ResolutionType == "reconcile" {
			if p.SelectedTarget != nil || p.ReplacementMode != nil {
				return ErrInvalidEvolutionProposal
			}
		} else {
			if p.SelectedTarget == nil || !containsTarget(participants, *p.SelectedTarget) {
				return ErrInvalidEvolutionProposal
			}
			if p.ResolutionType == "supersede" && (p.ReplacementMode == nil || !contains([]string{"same_memory_revision", "new_memory"}, *p.ReplacementMode)) || p.ResolutionType == "revise_one" && p.ReplacementMode != nil {
				return ErrInvalidEvolutionProposal
			}
		}
	}
	for _, target := range participants {
		state, ok := p.ResultStates[target.MemoryID]
		if !ok || !contains([]string{"active", "archived", "contested", "superseded"}, state) || state == "superseded" && p.ResolutionType != "supersede" && p.ResolutionType != "reconcile" {
			return ErrInvalidStateTransition
		}
	}
	if len(p.ResultStates) != len(participants) {
		return ErrInvalidStateTransition
	}
	return nil
}

func containsTarget(targets []domain.EvolutionTarget, selected domain.EvolutionTarget) bool {
	for _, target := range targets {
		if target == selected {
			return true
		}
	}
	return false
}

func resolutionEvidence(items []domain.RevisionEvidence) []EvidenceParams {
	result := make([]EvidenceParams, len(items))
	for i, item := range items {
		result[i] = EvidenceParams{EventID: item.EventID, Role: item.Role, StartOffset: item.StartOffset, EndOffset: item.EndOffset}
	}
	return result
}

func (r *Repository) createConflictResolutionMemory(ctx context.Context, tx *sql.Tx, c domain.ConflictCase, p ResolveConflictParams, target domain.EvolutionTarget, newMemory bool, operationID, caller, now string) ([]domain.EvolutionResultRef, string, string, error) {
	evidence := resolutionEvidence(p.Evidence)
	if err := requireEvidence(ctx, tx, c.SpaceID, evidence); err != nil {
		return nil, "", "", err
	}
	ids, err := r.newIDs(2 + len(evidence))
	if err != nil {
		return nil, "", "", err
	}
	memoryID, revisionID, parent := target.MemoryID, ids[0], &target.ExpectedRevisionID
	var memoryType, sensitivity string
	if err = tx.QueryRowContext(ctx, `SELECT r.kind,m.sensitivity FROM memories m JOIN memory_revisions r ON r.revision_id=m.current_revision_id WHERE m.memory_id=?`, target.MemoryID).Scan(&memoryType, &sensitivity); err != nil {
		return nil, "", "", err
	}
	refs := []domain.EvolutionResultRef{}
	evidenceIDs := ids[1:]
	if newMemory {
		memoryID, revisionID, parent = ids[0], ids[1], nil
		evidenceIDs = ids[2:]
		if p.ResolutionType == "reconcile" {
			for _, participant := range c.Participants {
				var participantSensitivity string
				if err = tx.QueryRowContext(ctx, `SELECT sensitivity FROM memories WHERE memory_id=?`, participant.MemoryID).Scan(&participantSensitivity); err != nil {
					return nil, "", "", err
				}
				if participantSensitivity == "sensitive" {
					sensitivity = "sensitive"
				}
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,frozen,sensitivity,created_at,updated_at) VALUES (?,?,?,'active',0,?,?,?)`, memoryID, c.SpaceID, revisionID, sensitivity, now, now); err != nil {
			return nil, "", "", err
		}
		refs = append(refs, domain.EvolutionResultRef{ResultType: "memory", ResultID: memoryID})
	}
	revision, err := revisionFromTextInput(revisionID, memoryID, parent, nil, memoryType, *p.ProposedContent, p.ValidFrom, p.ValidUntil, nil, sensitivity, caller, c.ProcessingVersion, nil, now)
	if err != nil {
		return nil, "", "", err
	}
	if err = insertRevision(ctx, tx, revision); err != nil {
		return nil, "", "", err
	}
	if err = insertEvidence(ctx, tx, revisionID, evidence, evidenceIDs, now); err != nil {
		return nil, "", "", err
	}
	refs = append(refs, domain.EvolutionResultRef{ResultType: "revision", ResultID: revisionID})
	for _, evidenceID := range evidenceIDs {
		refs = append(refs, domain.EvolutionResultRef{ResultType: "evidence", ResultID: evidenceID})
	}
	if !newMemory {
		res, e := tx.ExecContext(ctx, `UPDATE memories SET current_revision_id=?,updated_at=? WHERE memory_id=? AND current_revision_id=?`, revisionID, now, memoryID, target.ExpectedRevisionID)
		if e != nil || !oneRow(res) {
			if e != nil {
				return nil, "", "", e
			}
			return nil, "", "", ErrRevisionConflict
		}
	} else {
		stateID, e := r.runtime.ID()
		if e != nil {
			return nil, "", "", e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id,memory_id,revision_id,operation_id,from_state,to_state,from_frozen,to_frozen,reason,created_by,created_at) VALUES (?,?,?,?,NULL,'active',NULL,0,?,?,?)`, stateID, memoryID, revisionID, operationID, p.Reason, caller, now); e != nil {
			return nil, "", "", e
		}
		refs = append(refs, domain.EvolutionResultRef{ResultType: "state_event", ResultID: stateID})
	}
	state := "active"
	if !newMemory {
		if err = tx.QueryRowContext(ctx, `SELECT current_state FROM memories WHERE memory_id=?`, memoryID).Scan(&state); err != nil {
			return nil, "", "", err
		}
	}
	if err = r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, c.SpaceID, state, now); err != nil {
		return nil, "", "", err
	}
	refs = append(refs, domain.EvolutionResultRef{ResultType: "projection", ResultID: memoryID})
	if !newMemory {
		if err = r.markDependentAggregatesStale(ctx, tx, memoryID, now); err != nil {
			return nil, "", "", err
		}
	}
	jobID, err := r.runtime.ID()
	if err != nil {
		return nil, "", "", err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,created_at) VALUES (?,?,'rebuild_fts','index','memory_fts',?,'pending',?,?,?) ON CONFLICT DO NOTHING`, jobID, operationID, r.processingVersion(), r.maxAttempts, now, now)
	if err != nil {
		return nil, "", "", err
	}
	if oneRow(res) {
		refs = append(refs, domain.EvolutionResultRef{ResultType: "job", ResultID: jobID})
	}
	return refs, memoryID, revisionID, nil
}

func (r *Repository) createResolutionRelation(ctx context.Context, tx *sql.Tx, spaceID, sourceMemoryID, sourceRevisionID, targetMemoryID, targetRevisionID, operationID, caller, reason, now string) ([]domain.EvolutionResultRef, error) {
	ids, err := r.newIDs(4)
	if err != nil {
		return nil, err
	}
	spec := sha256.Sum256([]byte(sourceMemoryID + sourceRevisionID + targetMemoryID + targetRevisionID + "supersedes"))
	if _, err = tx.ExecContext(ctx, `INSERT INTO memory_relations(relation_id,space_id,relation_type,source_memory_id,source_revision_id,target_memory_id,target_revision_id,state,source,specification_hash,created_by,processing_version,created_at,updated_at) VALUES (?,?,?,?,?,?,?,'active','conflict_resolution',?,?,?,?,?)`, ids[0], spaceID, "supersedes", sourceMemoryID, sourceRevisionID, targetMemoryID, targetRevisionID, hex.EncodeToString(spec[:]), caller, r.processingVersion(), now, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO relation_state_events(state_event_id,relation_id,operation_id,from_state,to_state,reason,created_by,created_at) VALUES (?,?,?,NULL,'active',?,?,?)`, ids[1], ids[0], operationID, reason, caller, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO relation_evidence(evidence_id,relation_id,evidence_type,created_by,created_at) VALUES (?,?,'human_confirmation',?,?)`, ids[2], ids[0], caller, now); err != nil {
		return nil, err
	}
	createdJob, err := enqueueRelationIndexJob(ctx, tx, ids[3], operationID, r.processingVersion(), r.maxAttempts, now)
	if err != nil {
		return nil, err
	}
	refs := []domain.EvolutionResultRef{{ResultType: "relation", ResultID: ids[0]}, {ResultType: "state_event", ResultID: ids[1]}, {ResultType: "evidence", ResultID: ids[2]}}
	if createdJob {
		refs = append(refs, domain.EvolutionResultRef{ResultType: "job", ResultID: ids[3]})
	}
	if _, err = tx.ExecContext(ctx, `UPDATE relation_index_metadata SET state='stale',updated_at=? WHERE singleton=1`, now); err != nil {
		return nil, err
	}
	return refs, nil
}

func conflictRefs(ctx context.Context, q candidateQueryer, memoryID, revisionID string) ([]domain.ConflictRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT cc.case_id,r.relation_id,CASE WHEN r.source_memory_id=? THEN r.target_memory_id ELSE r.source_memory_id END,CASE WHEN r.source_memory_id=? THEN r.target_revision_id ELSE r.source_revision_id END FROM conflict_case_participants cp JOIN conflict_cases cc ON cc.case_id=cp.case_id AND cc.state='open' JOIN conflict_case_relations cr ON cr.case_id=cc.case_id JOIN memory_relations r ON r.relation_id=cr.relation_id AND r.state='active' WHERE cp.memory_id=? AND cp.revision_id=? ORDER BY cc.case_id,r.relation_id`, memoryID, memoryID, memoryID, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ConflictRef
	for rows.Next() {
		var v domain.ConflictRef
		if err = rows.Scan(&v.CaseID, &v.RelationID, &v.OtherMemoryID, &v.OtherRevisionID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
