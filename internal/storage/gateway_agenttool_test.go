package storage

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/internal/provider"
)

type agentToolEmbeddingProvider struct{}

func (agentToolEmbeddingProvider) Embed(_ context.Context, request provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	vectors := make([][]float32, len(request.Inputs()))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return provider.NewEmbeddingResult(vectors, "agent-tool-test"), nil
}

func agentToolIdentity(t *testing.T, key string, command any, operation gateway.OperationName) gateway.WriteIdentity {
	t.Helper()
	call := gateway.CallContext{RequestID: "request-" + key, Caller: gateway.CallerIdentity{ClientID: "agent"}, Idempotency: &gateway.Idempotency{Key: key}}
	identity, err := gateway.NewWriteIdentity(call, operation, command)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func agentToolCommit() agenttool.CommitParams {
	value := "active"
	return agenttool.CommitParams{
		ScopeID: "space-1",
		Source:  agenttool.Source{Type: "agent_tool", Name: "assistant", ConversationID: "conversation-1", TurnID: "turn-1", ToolCallID: "call-1", OccurredAt: time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC), Metadata: map[string]any{"model": "local"}},
		Memories: []agenttool.MemorySuggestion{
			{LocalID: "authority", Kind: domain.MemoryKindPreference, Content: "PostgreSQL   is preferred", Sensitivity: agenttool.SensitivityNormal, Confidence: .9, SearchHints: []string{"database   choice"}},
			{LocalID: "sensitive", Kind: domain.MemoryKindFact, Content: "private", Sensitivity: agenttool.SensitivitySensitive, Confidence: .8, SearchHints: []string{"private hint"}},
			{LocalID: "aggregate", Kind: domain.MemoryKindScene, Content: "summary", Sensitivity: agenttool.SensitivityNormal, Confidence: .7},
		},
		Entities:  []agenttool.EntitySuggestion{{LocalID: "project", Type: agenttool.EntityProject, Name: "dMem", Confidence: .9}},
		Facts:     []agenttool.FactSuggestion{{LocalID: "status", Subject: agenttool.EntityReference{LocalID: "project"}, Predicate: "has_status", Object: agenttool.FactObject{StringValue: &value}, EvidenceMemory: "authority", Confidence: .9}},
		Relations: []agenttool.RelationSuggestion{{LocalID: "relation", Type: agenttool.RelationSupports, Source: agenttool.MemoryReference{LocalID: "authority"}, Target: agenttool.MemoryReference{LocalID: "sensitive"}, EvidenceMemory: "authority", Confidence: .8}},
	}
}

func TestAgentToolCommitClassifiesPersistsAndSuppressesExtractionJobs(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	repository.ConfigureSemantic(agentToolEmbeddingProvider{}, "provider", "model")
	capability := NewAgentToolCapability(repository)
	params := agentToolCommit()
	identity := agentToolIdentity(t, "commit", params, agenttool.OperationMemoryCommit)

	record, replay, err := capability.CommitMemory(context.Background(), identity, params)
	if err != nil || replay {
		t.Fatalf("CommitMemory() replay=%t error=%v", replay, err)
	}
	if record.SourceEventID == "" || len(record.Memories) != 3 || record.Memories[0].Status != agenttool.CommitAuthority || record.Memories[1].Status != agenttool.CommitProposal || record.Memories[2].Status != agenttool.CommitProposal {
		t.Fatalf("record = %+v", record)
	}
	for _, outcomes := range [][]agenttool.CommitOutcome{record.Entities, record.Facts, record.Relations} {
		if len(outcomes) != 1 || outcomes[0].Status != agenttool.CommitProposal || outcomes[0].ProposalID == "" {
			t.Fatalf("proposal outcome = %+v", outcomes)
		}
	}

	var sources, hints, proposals, extractionJobs, embeddingJobs int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM agent_tool_sources),(SELECT count(*) FROM agent_tool_search_hints),(SELECT count(*) FROM agent_tool_proposals),(SELECT count(*) FROM jobs WHERE job_type IN ('extract_memory','extract_graph')),(SELECT count(*) FROM jobs WHERE job_type='embed_memory')`).Scan(&sources, &hints, &proposals, &extractionJobs, &embeddingJobs); err != nil {
		t.Fatal(err)
	}
	if sources != 1 || hints != 2 || proposals != 5 || extractionJobs != 0 || embeddingJobs != 1 {
		t.Fatalf("sources=%d hints=%d proposals=%d extraction_jobs=%d embedding_jobs=%d", sources, hints, proposals, extractionJobs, embeddingJobs)
	}
	var normalized string
	if err := repository.store.reader.QueryRow(`SELECT normalized_json FROM agent_tool_proposals WHERE suggestion_type='fact'`).Scan(&normalized); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(normalized, `"local_id":"project"`) || !strings.Contains(normalized, `"evidence_memory":"authority"`) {
		t.Fatalf("fact local references not preserved: %s", normalized)
	}
	var operationID, responseBody string
	if err := repository.store.reader.QueryRow(`SELECT operation_id,response_body FROM idempotency_records WHERE idempotency_key='commit'`).Scan(&operationID, &responseBody); err != nil {
		t.Fatal(err)
	}
	if operationID == "" || !strings.Contains(responseBody, record.SourceEventID) || !strings.Contains(responseBody, record.Entities[0].ProposalID) {
		t.Fatalf("incomplete idempotency response: operation=%q body=%s", operationID, responseBody)
	}
}

func TestAgentToolCommitReplayConflictDedupAndRollback(t *testing.T) {
	repository, runtime := newMemoryTestRepository(t)
	capability := NewAgentToolCapability(repository)
	params := agentToolCommit()
	identity := agentToolIdentity(t, "replay", params, agenttool.OperationMemoryCommit)
	first, replay, err := capability.CommitMemory(context.Background(), identity, params)
	if err != nil || replay {
		t.Fatalf("first commit replay=%t error=%v", replay, err)
	}
	second, replay, err := capability.CommitMemory(context.Background(), identity, params)
	if err != nil || !replay || !reflect.DeepEqual(first, second) {
		t.Fatalf("replay=%t error=%v first=%+v second=%+v", replay, err, first, second)
	}
	changed := identity
	changed.CommandHash = "different"
	if _, _, err := capability.CommitMemory(context.Background(), changed, params); !gateway.IsCode(err, gateway.ErrorIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}

	duplicate := params
	duplicate.Memories = []agenttool.MemorySuggestion{{LocalID: "duplicate", Kind: domain.MemoryKindPreference, Content: "PostgreSQL is preferred", Sensitivity: agenttool.SensitivityNormal, Confidence: .9}}
	duplicate.Entities, duplicate.Facts, duplicate.Relations = nil, nil, nil
	record, replay, err := capability.CommitMemory(context.Background(), agentToolIdentity(t, "duplicate", duplicate, agenttool.OperationMemoryCommit), duplicate)
	if err != nil || replay || record.Memories[0].Status != agenttool.CommitDeduplicated || record.Memories[0].MemoryID != first.Memories[0].MemoryID {
		t.Fatalf("duplicate record=%+v replay=%t error=%v", record, replay, err)
	}
	var evidence int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM memory_evidence WHERE revision_id=?`, first.Memories[0].RevisionID).Scan(&evidence); err != nil || evidence != 2 {
		t.Fatalf("evidence=%d error=%v", evidence, err)
	}

	before := runtime.id
	runtime.failAt = before + 6
	failed := duplicate
	failed.Memories[0].Content = "rollback memory"
	if _, _, err := capability.CommitMemory(context.Background(), agentToolIdentity(t, "rollback", failed, agenttool.OperationMemoryCommit), failed); err == nil {
		t.Fatal("CommitMemory() unexpectedly succeeded")
	}
	var events, idempotency int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM raw_events WHERE source_name='assistant' AND event_id NOT IN (?,?)),(SELECT count(*) FROM idempotency_records WHERE idempotency_key='rollback')`, first.SourceEventID, record.SourceEventID).Scan(&events, &idempotency); err != nil {
		t.Fatal(err)
	}
	if events != 0 || idempotency != 0 {
		t.Fatalf("rollback leaked events=%d idempotency=%d", events, idempotency)
	}
}

func TestAgentToolCommitSuppressesExtractionJobsWhenProviderEnabled(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	repository.SetGraphExtractionEnabled(true)
	capability := NewAgentToolCapability(repository)
	params := agentToolCommit()
	if _, _, err := capability.CommitMemory(context.Background(), agentToolIdentity(t, "provider-enabled", params, agenttool.OperationMemoryCommit), params); err != nil {
		t.Fatal(err)
	}
	var extractionJobs int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type IN ('extract_memory','extract_graph')`).Scan(&extractionJobs); err != nil {
		t.Fatal(err)
	}
	if extractionJobs != 0 {
		t.Fatalf("tool commit created %d extraction jobs", extractionJobs)
	}
}

func TestAgentToolFeedbackPersistsEveryClosedKind(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	capability := NewAgentToolCapability(repository)
	params := agentToolCommit()
	record, _, err := capability.CommitMemory(context.Background(), agentToolIdentity(t, "feedback-kinds", params, agenttool.OperationMemoryCommit), params)
	if err != nil {
		t.Fatal(err)
	}
	memory := record.Memories[0]
	for _, kind := range []agenttool.FeedbackKind{agenttool.FeedbackUsed, agenttool.FeedbackNotUsed, agenttool.FeedbackIncorrect, agenttool.FeedbackOutdated, agenttool.FeedbackDuplicate} {
		command := agenttool.FeedbackParams{ScopeID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.RevisionID, Kind: kind}
		feedback, replay, err := capability.AppendFeedback(context.Background(), agentToolIdentity(t, "feedback-"+string(kind), command, agenttool.OperationFeedback), command)
		if err != nil || replay {
			t.Fatalf("feedback %q replay=%t error=%v", kind, replay, err)
		}
		var purpose, stored string
		if err := repository.store.reader.QueryRow(`SELECT feedback_purpose,feedback_type FROM feedback_events WHERE feedback_id=?`, feedback.FeedbackID).Scan(&purpose, &stored); err != nil {
			t.Fatal(err)
		}
		if purpose != "agent_tool_feedback" || stored != string(kind) {
			t.Fatalf("feedback %q stored as %q/%q", kind, purpose, stored)
		}
	}
}

func TestAgentToolFeedbackAndRevisionFencedForget(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	capability := NewAgentToolCapability(repository)
	params := agentToolCommit()
	record, _, err := capability.CommitMemory(context.Background(), agentToolIdentity(t, "seed", params, agenttool.OperationMemoryCommit), params)
	if err != nil {
		t.Fatal(err)
	}
	memory := record.Memories[0]
	feedback := agenttool.FeedbackParams{ScopeID: "space-1", MemoryID: memory.MemoryID, RevisionID: memory.RevisionID, Kind: agenttool.FeedbackUsed, Reason: "used", RecallOperationID: "recall-1"}
	feedbackRecord, replay, err := capability.AppendFeedback(context.Background(), agentToolIdentity(t, "feedback", feedback, agenttool.OperationFeedback), feedback)
	if err != nil || replay || feedbackRecord.FeedbackID == "" {
		t.Fatalf("feedback=%+v replay=%t error=%v", feedbackRecord, replay, err)
	}
	var purpose, recallID string
	if err := repository.store.reader.QueryRow(`SELECT feedback_purpose,search_operation_id FROM feedback_events WHERE feedback_id=?`, feedbackRecord.FeedbackID).Scan(&purpose, &recallID); err != nil || purpose != "agent_tool_feedback" || recallID != "recall-1" {
		t.Fatalf("purpose=%q recall=%q error=%v", purpose, recallID, err)
	}

	stale := agenttool.ForgetParams{ScopeID: "space-1", MemoryID: memory.MemoryID, ExpectedRevisionID: "stale", Reason: "requested"}
	if _, _, err := capability.ForgetMemory(context.Background(), agentToolIdentity(t, "forget-stale", stale, agenttool.OperationForget), stale); !gateway.IsCode(err, gateway.ErrorConflict) {
		t.Fatalf("stale forget error = %v", err)
	}
	forget := stale
	forget.ExpectedRevisionID = memory.RevisionID
	forgotten, replay, err := capability.ForgetMemory(context.Background(), agentToolIdentity(t, "forget", forget, agenttool.OperationForget), forget)
	if err != nil || replay || forgotten.State != "deleted" || forgotten.RevisionID != memory.RevisionID {
		t.Fatalf("forget=%+v replay=%t error=%v", forgotten, replay, err)
	}
	var state string
	if err := repository.store.reader.QueryRow(`SELECT current_state FROM memories WHERE memory_id=?`, memory.MemoryID).Scan(&state); err != nil || state != "deleted" {
		t.Fatalf("state=%q error=%v", state, err)
	}
}

func TestAgentToolCommitValidatesThroughGatewayContract(t *testing.T) {
	params := agentToolCommit()
	command := agenttool.MemoryCommitCommand{ScopeID: params.ScopeID, Source: params.Source, Memories: params.Memories, Entities: params.Entities, Facts: params.Facts, Relations: params.Relations}
	if err := agenttool.ValidateMemoryCommit(command); err != nil {
		t.Fatalf("test command is invalid: %v", err)
	}
	bad := command
	bad.Source.Type = "provider"
	if err := agenttool.ValidateMemoryCommit(bad); err == nil {
		t.Fatal("invalid source accepted")
	}
}
