package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
)

type AgentToolCapability struct {
	repo *Repository
}

func NewAgentToolCapability(repo *Repository) *AgentToolCapability {
	return &AgentToolCapability{repo: repo}
}

func (c *AgentToolCapability) CommitMemory(ctx context.Context, identity gateway.WriteIdentity, params agenttool.CommitParams) (agenttool.CommitRecord, bool, error) {
	command := agenttool.MemoryCommitCommand{
		ScopeID: params.ScopeID, Source: params.Source, Memories: params.Memories,
		Entities: params.Entities, Facts: params.Facts, Relations: params.Relations,
	}
	result, replay, err := c.repo.commitAgentToolMemory(ctx, gatewayWriteIdentity(identity), command)
	if err != nil {
		return agenttool.CommitRecord{}, replay, gatewayError(err)
	}
	return agenttool.CommitRecord{
		OperationID: result.OperationID, SourceEventID: result.SourceEventID, Memories: result.Memories, Entities: result.Entities,
		Facts: result.Facts, Relations: result.Relations,
	}, replay, nil
}

func (c *AgentToolCapability) AppendFeedback(ctx context.Context, identity gateway.WriteIdentity, params agenttool.FeedbackParams) (agenttool.FeedbackRecord, bool, error) {
	command := agenttool.FeedbackCommand(params)
	result, replay, err := c.repo.appendAgentToolFeedback(ctx, gatewayWriteIdentity(identity), command)
	if err != nil {
		return agenttool.FeedbackRecord{}, replay, gatewayError(err)
	}
	return agenttool.FeedbackRecord{OperationID: result.OperationID, FeedbackID: result.FeedbackID, MemoryID: result.MemoryID, RevisionID: result.RevisionID, Kind: result.Kind}, replay, nil
}

func (c *AgentToolCapability) ForgetMemory(ctx context.Context, identity gateway.WriteIdentity, params agenttool.ForgetParams) (agenttool.ForgetRecord, bool, error) {
	command := agenttool.ForgetCommand(params)
	result, replay, err := c.repo.forgetAgentToolMemory(ctx, gatewayWriteIdentity(identity), command)
	if err != nil {
		return agenttool.ForgetRecord{}, replay, gatewayError(err)
	}
	return agenttool.ForgetRecord{OperationID: result.OperationID, MemoryID: result.MemoryID, RevisionID: result.RevisionID, State: result.State}, replay, nil
}

func (r *Repository) commitAgentToolMemory(ctx context.Context, identity WriteIdentity, command agenttool.MemoryCommitCommand) (agenttool.MemoryCommitResult, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	} else if found {
		var result agenttool.MemoryCommitResult
		if status != 201 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	if err := requireAgentToolSpace(ctx, tx, command.ScopeID); err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}

	now := formatTime(r.runtime.Now())
	operationID := agentToolOperationID(identity)
	contentID, err := r.runtime.ID()
	if err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	eventID, err := r.runtime.ID()
	if err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	sourceID, err := r.runtime.ID()
	if err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	auditID, err := r.runtime.ID()
	if err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	normalized, err := normalizedAgentToolCommand(command)
	if err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	metadata, err := json.Marshal(normalizeJSONMap(command.Source.Metadata))
	if err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	contentHash := sha256.Sum256(normalized)
	if _, err := tx.ExecContext(ctx, `INSERT INTO raw_contents(content_id,content_kind,content_text,media_type,content_hash,size_bytes,created_at) VALUES (?,'json',?,'application/json',?,?,?)`, contentID, string(normalized), hex.EncodeToString(contentHash[:]), len(normalized), now); err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO raw_events(event_id,operation_id,space_id,content_id,source_type,source_name,source_event_id,caller_id,occurred_at,received_at,processing_status,metadata_json,created_at) VALUES (?,?,?,?,'tool_call',?,?,?,?,?,'processed',?,?)`, eventID, operationID, command.ScopeID, contentID, command.Source.Name, nullableString(command.Source.ToolCallID), identity.CallerID, formatTime(command.Source.OccurredAt), now, string(metadata), now); err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_sources(source_id,event_id,operation_id,source_type,source_name,conversation_id,turn_id,tool_call_id,occurred_at,metadata_json,created_at) VALUES (?,?,?,'agent_tool',?,?,?,?,?,?,?)`, sourceID, eventID, operationID, command.Source.Name, command.Source.ConversationID, command.Source.TurnID, nullableString(command.Source.ToolCallID), formatTime(command.Source.OccurredAt), string(metadata), now); err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}

	result := agenttool.MemoryCommitResult{WriteResult: gateway.WriteResult{OperationID: operationID}, SourceEventID: eventID, Memories: []agenttool.CommitOutcome{}, Entities: []agenttool.CommitOutcome{}, Facts: []agenttool.CommitOutcome{}, Relations: []agenttool.CommitOutcome{}}
	for _, suggestion := range command.Memories {
		outcome, err := r.persistAgentToolMemory(ctx, tx, identity, command.ScopeID, eventID, operationID, now, suggestion)
		if err != nil {
			return agenttool.MemoryCommitResult{}, false, err
		}
		result.Memories = append(result.Memories, outcome)
	}
	for _, item := range command.Entities {
		outcome, err := r.persistAgentToolProposal(ctx, tx, command.ScopeID, eventID, now, "entity", item.LocalID, item)
		if err != nil {
			return agenttool.MemoryCommitResult{}, false, err
		}
		result.Entities = append(result.Entities, outcome)
	}
	for _, item := range command.Facts {
		outcome, err := r.persistAgentToolProposal(ctx, tx, command.ScopeID, eventID, now, "fact", item.LocalID, item)
		if err != nil {
			return agenttool.MemoryCommitResult{}, false, err
		}
		result.Facts = append(result.Facts, outcome)
	}
	for _, item := range command.Relations {
		outcome, err := r.persistAgentToolProposal(ctx, tx, command.ScopeID, eventID, now, "relation", item.LocalID, item)
		if err != nil {
			return agenttool.MemoryCommitResult{}, false, err
		}
		result.Relations = append(result.Relations, outcome)
	}
	body, err := json.Marshal(result)
	if err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	if err := writeIdempotency(ctx, tx, identity, operationID, 201, body, now); err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,created_at) VALUES (?,?,?,?,'agent_tool.memory_commit','raw_event',?,'succeeded',?)`, auditID, operationID, identity.RequestID, identity.CallerID, eventID, now); err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return agenttool.MemoryCommitResult{}, false, err
	}
	return result, false, nil
}

func (r *Repository) persistAgentToolMemory(ctx context.Context, tx *sql.Tx, identity WriteIdentity, spaceID, eventID, operationID, now string, suggestion agenttool.MemorySuggestion) (agenttool.CommitOutcome, error) {
	if suggestion.Sensitivity == agenttool.SensitivitySensitive || domain.ValidAggregateKind(string(suggestion.Kind)) {
		outcome, err := r.persistAgentToolProposal(ctx, tx, spaceID, eventID, now, "memory", suggestion.LocalID, suggestion)
		if err != nil {
			return agenttool.CommitOutcome{}, err
		}
		if err := insertAgentToolHints(ctx, tx, "", eventID, suggestion.LocalID, suggestion.SearchHints, now); err != nil {
			return agenttool.CommitOutcome{}, err
		}
		return outcome, nil
	}
	content := normalizeAgentToolText(suggestion.Content)
	memoryID, err := r.runtime.ID()
	if err != nil {
		return agenttool.CommitOutcome{}, err
	}
	revisionID, err := r.runtime.ID()
	if err != nil {
		return agenttool.CommitOutcome{}, err
	}
	revision, err := revisionFromTextInput(revisionID, memoryID, nil, nil, string(suggestion.Kind), content, timeText(suggestion.ValidFrom), timeText(suggestion.ValidUntil), nil, string(suggestion.Sensitivity), identity.CallerID, "agent-tool-v1", nil, now)
	if err != nil {
		return agenttool.CommitOutcome{}, err
	}
	revision.Confidence = suggestion.Confidence
	candidate := domain.Candidate{CandidateID: revisionID, EventID: eventID, SpaceID: spaceID, ProcessorIdentity: "agent-tool-v1", ProcessingVersion: "agent-tool-v1"}
	outcome, _, err := r.classifyCandidateForConsolidation(ctx, tx, candidate, revision)
	if err != nil {
		return agenttool.CommitOutcome{}, err
	}
	switch outcome.Action {
	case domain.ConsolidationAttachEvidence:
		if _, err := r.attachConsolidationEvidence(ctx, tx, spaceID, outcome.TargetMemoryID, outcome.TargetRevisionID, []EvidenceParams{{EventID: eventID, Role: domain.EvidenceRoleDirect}}, now); err != nil {
			return agenttool.CommitOutcome{}, err
		}
		if err := insertAgentToolHints(ctx, tx, outcome.TargetRevisionID, eventID, suggestion.LocalID, suggestion.SearchHints, now); err != nil {
			return agenttool.CommitOutcome{}, err
		}
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT current_state FROM memories WHERE memory_id=?`, outcome.TargetMemoryID).Scan(&state); err != nil {
			return agenttool.CommitOutcome{}, err
		}
		if err := r.upsertMemoryProjection(ctx, tx, outcome.TargetMemoryID, outcome.TargetRevisionID, spaceID, state, now, false); err != nil {
			return agenttool.CommitOutcome{}, err
		}
		return agenttool.CommitOutcome{LocalID: suggestion.LocalID, Status: agenttool.CommitDeduplicated, MemoryID: outcome.TargetMemoryID, RevisionID: outcome.TargetRevisionID, ReasonCode: outcome.ReasonCode}, nil
	case domain.ConsolidationReject:
		return agenttool.CommitOutcome{LocalID: suggestion.LocalID, Status: agenttool.CommitRejected, ReasonCode: outcome.ReasonCode}, nil
	case domain.ConsolidationRefine, domain.ConsolidationSupersede, domain.ConsolidationContest:
		proposal, err := r.persistAgentToolProposal(ctx, tx, spaceID, eventID, now, "memory", suggestion.LocalID, suggestion)
		if err != nil {
			return agenttool.CommitOutcome{}, err
		}
		proposal.ReasonCode = outcome.ReasonCode
		return proposal, nil
	}
	stateEventID, err := r.runtime.ID()
	if err != nil {
		return agenttool.CommitOutcome{}, err
	}
	evidenceID, err := r.runtime.ID()
	if err != nil {
		return agenttool.CommitOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memories(memory_id,space_id,current_revision_id,current_state,frozen,sensitivity,created_at,updated_at) VALUES (?,?,?,'active',0,'normal',?,?)`, memoryID, spaceID, revisionID, now, now); err != nil {
		return agenttool.CommitOutcome{}, err
	}
	if err := insertRevision(ctx, tx, revision); err != nil {
		return agenttool.CommitOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_evidence(evidence_id,revision_id,event_id,role,created_at) VALUES (?,?,?,'direct',?)`, evidenceID, revisionID, eventID, now); err != nil {
		return agenttool.CommitOutcome{}, err
	}
	if err := insertAgentToolHints(ctx, tx, revisionID, eventID, suggestion.LocalID, suggestion.SearchHints, now); err != nil {
		return agenttool.CommitOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id,memory_id,revision_id,operation_id,from_state,to_state,from_frozen,to_frozen,reason,created_by,created_at) VALUES (?,?,?,?,NULL,'active',NULL,0,'agent_tool_commit',?,?)`, stateEventID, memoryID, revisionID, operationID, identity.CallerID, now); err != nil {
		return agenttool.CommitOutcome{}, err
	}
	if err := r.upsertMemoryProjection(ctx, tx, memoryID, revisionID, spaceID, "active", now, false); err != nil {
		return agenttool.CommitOutcome{}, err
	}
	return agenttool.CommitOutcome{LocalID: suggestion.LocalID, Status: agenttool.CommitAuthority, MemoryID: memoryID, RevisionID: revisionID}, nil
}

func (r *Repository) persistAgentToolProposal(ctx context.Context, tx *sql.Tx, spaceID, eventID, now, kind, localID string, suggestion any) (agenttool.CommitOutcome, error) {
	payload, err := normalizedAgentToolSuggestion(suggestion)
	if err != nil {
		return agenttool.CommitOutcome{}, err
	}
	hash := sha256.Sum256(payload)
	proposalID, err := r.runtime.ID()
	if err != nil {
		return agenttool.CommitOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_proposals(proposal_id,event_id,space_id,suggestion_type,local_id,normalized_json,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?)`, proposalID, eventID, spaceID, kind, localID, string(payload), hex.EncodeToString(hash[:]), now); err != nil {
		return agenttool.CommitOutcome{}, err
	}
	return agenttool.CommitOutcome{LocalID: localID, Status: agenttool.CommitProposal, ProposalID: proposalID, ReasonCode: kind + "_requires_review"}, nil
}

func (r *Repository) appendAgentToolFeedback(ctx context.Context, identity WriteIdentity, command agenttool.FeedbackCommand) (agenttool.FeedbackResult, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return agenttool.FeedbackResult{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return agenttool.FeedbackResult{}, false, err
	} else if found {
		var result agenttool.FeedbackResult
		if status != 201 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := requireAgentToolMemory(ctx, tx, command.ScopeID, command.MemoryID, command.RevisionID); err != nil {
		return agenttool.FeedbackResult{}, false, err
	}
	operationID := agentToolOperationID(identity)
	feedbackID, err := r.runtime.ID()
	if err != nil {
		return agenttool.FeedbackResult{}, false, err
	}
	auditID, err := r.runtime.ID()
	if err != nil {
		return agenttool.FeedbackResult{}, false, err
	}
	now := formatTime(r.runtime.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO feedback_events(feedback_id,operation_id,caller_id,memory_id,revision_id,feedback_purpose,feedback_type,reason_code,note,search_operation_id,created_at) VALUES (?,?,?,?,?,'agent_tool_feedback',?,?,?,?,?)`, feedbackID, operationID, identity.CallerID, command.MemoryID, command.RevisionID, command.Kind, agentToolFeedbackReason(command.Kind), nullableString(command.Reason), nullableString(command.RecallOperationID), now); err != nil {
		return agenttool.FeedbackResult{}, false, err
	}
	result := agenttool.FeedbackResult{WriteResult: gateway.WriteResult{OperationID: operationID}, FeedbackID: feedbackID, MemoryID: command.MemoryID, RevisionID: command.RevisionID, Kind: command.Kind}
	if err := finishAgentToolWrite(ctx, tx, identity, operationID, auditID, "agent_tool.feedback", "feedback", feedbackID, 201, result, now); err != nil {
		return agenttool.FeedbackResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return agenttool.FeedbackResult{}, false, err
	}
	return result, false, nil
}

func (r *Repository) forgetAgentToolMemory(ctx context.Context, identity WriteIdentity, command agenttool.ForgetCommand) (agenttool.ForgetResult, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return agenttool.ForgetResult{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return agenttool.ForgetResult{}, false, err
	} else if found {
		var result agenttool.ForgetResult
		if status != 200 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	var currentRevision, state, spaceID string
	var frozen bool
	err = tx.QueryRowContext(ctx, `SELECT current_revision_id,current_state,frozen,space_id FROM memories WHERE memory_id=? AND space_id=?`, command.MemoryID, command.ScopeID).Scan(&currentRevision, &state, &frozen, &spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return agenttool.ForgetResult{}, false, ErrNotFound
	}
	if err != nil {
		return agenttool.ForgetResult{}, false, err
	}
	if currentRevision != command.ExpectedRevisionID {
		return agenttool.ForgetResult{}, false, ErrRevisionConflict
	}
	operationID := agentToolOperationID(identity)
	auditID, err := r.runtime.ID()
	if err != nil {
		return agenttool.ForgetResult{}, false, err
	}
	now := formatTime(r.runtime.Now())
	if state != "deleted" {
		stateEventID, err := r.runtime.ID()
		if err != nil {
			return agenttool.ForgetResult{}, false, err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE memories SET current_state='deleted',updated_at=? WHERE memory_id=? AND current_revision_id=?`, now, command.MemoryID, command.ExpectedRevisionID)
		if err != nil {
			return agenttool.ForgetResult{}, false, err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return agenttool.ForgetResult{}, false, ErrRevisionConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_state_events(state_event_id,memory_id,revision_id,operation_id,from_state,to_state,from_frozen,to_frozen,reason,created_by,created_at) VALUES (?,?,?,?,?,'deleted',?,?,?,?,?)`, stateEventID, command.MemoryID, currentRevision, operationID, state, frozen, frozen, command.Reason, identity.CallerID, now); err != nil {
			return agenttool.ForgetResult{}, false, err
		}
		if err := r.upsertMemoryProjection(ctx, tx, command.MemoryID, currentRevision, spaceID, "deleted", now, false); err != nil {
			return agenttool.ForgetResult{}, false, err
		}
	}
	result := agenttool.ForgetResult{WriteResult: gateway.WriteResult{OperationID: operationID}, MemoryID: command.MemoryID, RevisionID: currentRevision, State: "deleted"}
	if err := finishAgentToolWrite(ctx, tx, identity, operationID, auditID, "agent_tool.forget", "memory", command.MemoryID, 200, result, now); err != nil {
		return agenttool.ForgetResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return agenttool.ForgetResult{}, false, err
	}
	return result, false, nil
}

func requireAgentToolSpace(ctx context.Context, tx *sql.Tx, spaceID string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM memory_spaces WHERE space_id=?`, spaceID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else {
		return err
	}
}

func requireAgentToolMemory(ctx context.Context, tx *sql.Tx, spaceID, memoryID, revisionID string) error {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM memories m JOIN memory_revisions r ON r.memory_id=m.memory_id WHERE m.space_id=? AND m.memory_id=? AND r.revision_id=?`, spaceID, memoryID, revisionID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func insertAgentToolHints(ctx context.Context, tx *sql.Tx, revisionID, eventID, localID string, hints []string, now string) error {
	hints = normalizedAgentToolSet(hints)
	for i, hint := range hints {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_search_hints(event_id,local_id,revision_id,hint_order,hint,created_at) VALUES (?,?,?,?,?,?)`, eventID, localID, nullableString(revisionID), i, hint, now); err != nil {
			return err
		}
	}
	return nil
}

func finishAgentToolWrite(ctx context.Context, tx *sql.Tx, identity WriteIdentity, operationID, auditID, action, targetType, targetID string, status int, result any, now string) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := writeIdempotency(ctx, tx, identity, operationID, status, body, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,created_at) VALUES (?,?,?,?,?,?,?,'succeeded',?)`, auditID, operationID, identity.RequestID, identity.CallerID, action, targetType, targetID, now)
	return err
}

func normalizedAgentToolCommand(command agenttool.MemoryCommitCommand) ([]byte, error) {
	command.Source.OccurredAt = command.Source.OccurredAt.UTC()
	command.Source.Metadata = normalizeJSONMap(command.Source.Metadata)
	command.Memories = append([]agenttool.MemorySuggestion(nil), command.Memories...)
	for i := range command.Memories {
		command.Memories[i].Content = normalizeAgentToolText(command.Memories[i].Content)
		command.Memories[i].SearchHints = normalizedAgentToolSet(command.Memories[i].SearchHints)
		command.Memories[i].ValidFrom = utcTimePointer(command.Memories[i].ValidFrom)
		command.Memories[i].ValidUntil = utcTimePointer(command.Memories[i].ValidUntil)
	}
	sort.Slice(command.Memories, func(i, j int) bool { return command.Memories[i].LocalID < command.Memories[j].LocalID })
	command.Entities = append([]agenttool.EntitySuggestion(nil), command.Entities...)
	for i := range command.Entities {
		command.Entities[i].Aliases = normalizedAgentToolSet(command.Entities[i].Aliases)
	}
	sort.Slice(command.Entities, func(i, j int) bool { return command.Entities[i].LocalID < command.Entities[j].LocalID })
	command.Facts = append([]agenttool.FactSuggestion(nil), command.Facts...)
	for i := range command.Facts {
		command.Facts[i].ValidFrom = utcTimePointer(command.Facts[i].ValidFrom)
		command.Facts[i].ValidUntil = utcTimePointer(command.Facts[i].ValidUntil)
	}
	sort.Slice(command.Facts, func(i, j int) bool { return command.Facts[i].LocalID < command.Facts[j].LocalID })
	command.Relations = append([]agenttool.RelationSuggestion(nil), command.Relations...)
	sort.Slice(command.Relations, func(i, j int) bool { return command.Relations[i].LocalID < command.Relations[j].LocalID })
	memories := make([]any, len(command.Memories))
	for i, item := range command.Memories {
		memories[i] = agentToolMemoryJSON(item)
	}
	entities := make([]any, len(command.Entities))
	for i, item := range command.Entities {
		entities[i] = agentToolEntityJSON(item)
	}
	facts := make([]any, len(command.Facts))
	for i, item := range command.Facts {
		facts[i] = agentToolFactJSON(item)
	}
	relations := make([]any, len(command.Relations))
	for i, item := range command.Relations {
		relations[i] = agentToolRelationJSON(item)
	}
	return json.Marshal(map[string]any{
		"scope_id": command.ScopeID,
		"source": map[string]any{
			"type": command.Source.Type, "name": command.Source.Name,
			"conversation_id": command.Source.ConversationID, "turn_id": command.Source.TurnID,
			"tool_call_id": command.Source.ToolCallID, "occurred_at": command.Source.OccurredAt,
			"metadata": command.Source.Metadata,
		},
		"memories": memories, "entities": entities, "facts": facts, "relations": relations,
	})
}

func normalizedAgentToolSuggestion(value any) ([]byte, error) {
	switch item := value.(type) {
	case agenttool.MemorySuggestion:
		item.Content = normalizeAgentToolText(item.Content)
		item.SearchHints = normalizedAgentToolSet(item.SearchHints)
		item.ValidFrom = utcTimePointer(item.ValidFrom)
		item.ValidUntil = utcTimePointer(item.ValidUntil)
		return json.Marshal(agentToolMemoryJSON(item))
	case agenttool.EntitySuggestion:
		item.Aliases = normalizedAgentToolSet(item.Aliases)
		return json.Marshal(agentToolEntityJSON(item))
	case agenttool.FactSuggestion:
		item.ValidFrom = utcTimePointer(item.ValidFrom)
		item.ValidUntil = utcTimePointer(item.ValidUntil)
		return json.Marshal(agentToolFactJSON(item))
	case agenttool.RelationSuggestion:
		return json.Marshal(agentToolRelationJSON(item))
	default:
		return json.Marshal(value)
	}
}

func agentToolMemoryJSON(item agenttool.MemorySuggestion) map[string]any {
	return map[string]any{
		"local_id": item.LocalID, "kind": item.Kind, "content": item.Content,
		"sensitivity": item.Sensitivity, "confidence": item.Confidence,
		"search_hints": item.SearchHints, "valid_from": item.ValidFrom, "valid_until": item.ValidUntil,
	}
}

func agentToolEntityJSON(item agenttool.EntitySuggestion) map[string]any {
	return map[string]any{
		"local_id": item.LocalID, "type": item.Type, "name": item.Name,
		"aliases": item.Aliases, "confidence": item.Confidence,
	}
}

func agentToolFactJSON(item agenttool.FactSuggestion) map[string]any {
	return map[string]any{
		"local_id": item.LocalID, "subject": agentToolEntityReferenceJSON(item.Subject),
		"predicate": item.Predicate, "object": agentToolFactObjectJSON(item.Object),
		"evidence_memory": item.EvidenceMemory, "confidence": item.Confidence,
		"valid_from": item.ValidFrom, "valid_until": item.ValidUntil,
	}
}

func agentToolRelationJSON(item agenttool.RelationSuggestion) map[string]any {
	return map[string]any{
		"local_id": item.LocalID, "type": item.Type,
		"source": agentToolMemoryReferenceJSON(item.Source), "target": agentToolMemoryReferenceJSON(item.Target),
		"evidence_memory": item.EvidenceMemory, "confidence": item.Confidence,
	}
}

func agentToolEntityReferenceJSON(item agenttool.EntityReference) map[string]any {
	return map[string]any{"local_id": item.LocalID, "entity_id": item.EntityID}
}

func agentToolMemoryReferenceJSON(item agenttool.MemoryReference) map[string]any {
	return map[string]any{"local_id": item.LocalID, "memory_id": item.MemoryID, "revision_id": item.RevisionID}
}

func agentToolFactObjectJSON(item agenttool.FactObject) map[string]any {
	var entity any
	if item.Entity != nil {
		entity = agentToolEntityReferenceJSON(*item.Entity)
	}
	return map[string]any{
		"entity": entity, "string_value": item.StringValue, "number_value": item.NumberValue,
		"boolean_value": item.BooleanValue,
	}
}

func normalizeAgentToolText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizedAgentToolSet(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = normalizeAgentToolText(value); value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizeJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		switch typed := item.(type) {
		case map[string]any:
			result[key] = normalizeJSONMap(typed)
		case []any:
			copy := make([]any, len(typed))
			for i, child := range typed {
				if object, ok := child.(map[string]any); ok {
					copy[i] = normalizeJSONMap(object)
				} else {
					copy[i] = child
				}
			}
			result[key] = copy
		default:
			result[key] = item
		}
	}
	return result
}

func timeText(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}

func agentToolFeedbackReason(kind agenttool.FeedbackKind) string {
	switch kind {
	case agenttool.FeedbackIncorrect:
		return "fact_incorrect"
	case agenttool.FeedbackOutdated:
		return "fact_outdated"
	case agenttool.FeedbackDuplicate:
		return "duplicate"
	default:
		return "other"
	}
}

func agentToolOperationID(identity WriteIdentity) string {
	if identity.RequestID != "" {
		return identity.RequestID
	}
	hash := sha256.Sum256([]byte(identity.CallerID + "\x00" + identity.Route + "\x00" + identity.IdempotencyKey))
	return "agent-tool-" + hex.EncodeToString(hash[:])
}

var _ agenttool.Store = (*AgentToolCapability)(nil)
