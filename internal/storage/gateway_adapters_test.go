package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/consolidation"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/feedback"
	"github.com/N0rthHXD/dMem/internal/gateway/knowledge"
	memgw "github.com/N0rthHXD/dMem/internal/gateway/memory"
	"github.com/N0rthHXD/dMem/internal/gateway/policy"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

func gatewayCall(caller, key, requestID string) gateway.CallContext {
	return gateway.CallContext{
		RequestID:   requestID,
		Caller:      gateway.CallerIdentity{SubjectID: caller},
		Idempotency: &gateway.Idempotency{Key: key},
	}
}

func gatewayIdentity(t *testing.T, call gateway.CallContext, operation gateway.OperationName, command any) gateway.WriteIdentity {
	t.Helper()
	identity, err := gateway.NewWriteIdentity(call, operation, command)
	if err != nil {
		t.Fatalf("NewWriteIdentity() error = %v", err)
	}
	return identity
}

func TestMemoryCapabilityRememberIsAtomicIdempotentAndAudited(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	capability := NewMemoryCapability(repository)
	call := gatewayCall("gateway-caller", "remember-1", "request-remember-1")
	command := memgw.RememberCommand{SpaceID: "space-1", Content: "gateway memory", Kind: "fact", Sensitivity: "normal", SourceName: "test", OccurredAt: "2026-08-21T12:00:00Z", Reason: "gateway"}
	params := memgw.RememberParams{SpaceID: "space-1", Content: "gateway memory", Kind: "fact", Sensitivity: "normal", SourceName: "test", OccurredAt: "2026-08-21T12:00:00Z", Reason: "gateway"}
	identity := gatewayIdentity(t, call, memgw.OperationRemember, command)

	record, replay, err := capability.Remember(ctx, identity, params)
	if err != nil || replay {
		t.Fatalf("Remember() replay=%t error=%v", replay, err)
	}
	if record.Memory.MemoryID == "" || record.EventID == "" || record.Memory.CurrentRevision.RevisionID == "" {
		t.Fatalf("record = %+v", record)
	}
	var audits, idempotency, events int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM audit_events WHERE action='remember' AND target_id=?), (SELECT count(*) FROM idempotency_records WHERE normalized_route='memory.remember' AND idempotency_key='remember-1'), (SELECT count(*) FROM raw_events WHERE event_id=?)`, record.Memory.MemoryID, record.EventID).Scan(&audits, &idempotency, &events); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || idempotency != 1 || events != 1 {
		t.Fatalf("audits=%d idempotency=%d events=%d", audits, idempotency, events)
	}

	replayRecord, replay, err := capability.Remember(ctx, identity, params)
	if err != nil || !replay || replayRecord.Memory.MemoryID != record.Memory.MemoryID {
		t.Fatalf("replay record = %+v replay=%t error=%v", replayRecord, replay, err)
	}

	conflictCall := gatewayCall("gateway-caller", "remember-1", "request-remember-1")
	conflictCommand := command
	conflictCommand.Content = "different gateway memory"
	conflictIdentity := gatewayIdentity(t, conflictCall, memgw.OperationRemember, conflictCommand)
	conflictParams := params
	conflictParams.Content = conflictCommand.Content
	if _, _, err := capability.Remember(ctx, conflictIdentity, conflictParams); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
}

func TestMemoryCapabilityForgetIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "gateway-forget")
	capability := NewMemoryCapability(repository)
	call := gatewayCall("gateway-caller", "forget-1", "request-forget-1")
	command := memgw.ForgetCommand{MemoryID: memory.MemoryID, Reason: "user request"}
	identity := gatewayIdentity(t, call, memgw.OperationForget, command)

	forgotten, replay, err := capability.ForgetMemory(ctx, identity, memory.MemoryID, command.Reason)
	if err != nil || replay || forgotten.CurrentState != "deleted" {
		t.Fatalf("ForgetMemory() = %+v replay=%t error=%v", forgotten, replay, err)
	}
	replayed, replay, err := capability.ForgetMemory(ctx, identity, memory.MemoryID, command.Reason)
	if err != nil || !replay || replayed.CurrentState != "deleted" {
		t.Fatalf("replay = %+v replay=%t error=%v", replayed, replay, err)
	}
}

func TestScopeCapabilityCreateAndListCursor(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	capability := NewScopeCapability(repository)
	for _, external := range []string{"project-a", "project-b", "project-c"} {
		call := gatewayCall("gateway-caller", "scope-"+external, "request-scope-"+external)
		command := scope.CreateCommand{ParentSpaceID: stringPointer("space-1"), SpaceType: "project", ExternalID: external, DisplayName: external}
		identity := gatewayIdentity(t, call, scope.OperationCreate, command)
		if _, replay, err := capability.CreateScope(ctx, identity, scope.CreateParams{ParentSpaceID: command.ParentSpaceID, SpaceType: command.SpaceType, ExternalID: command.ExternalID, DisplayName: command.DisplayName}); err != nil || replay {
			t.Fatalf("CreateScope(%s) replay=%t error=%v", external, replay, err)
		}
	}
	first, err := capability.ListScopes(ctx, scope.ListParams{ParentSpaceID: stringPointer("space-1"), Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first page = %+v error=%v", first, err)
	}
	second, err := capability.ListScopes(ctx, scope.ListParams{ParentSpaceID: stringPointer("space-1"), Limit: 2, Cursor: *first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.NextCursor != nil {
		t.Fatalf("second page = %+v error=%v", second, err)
	}
}

func TestScopeCapabilityResolveByExternalIdentity(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	capability := NewScopeCapability(repository)
	call := gatewayCall("gateway-caller", "scope-resolve", "request-scope-resolve")
	command := scope.CreateCommand{ParentSpaceID: stringPointer("space-1"), SpaceType: "project", ExternalID: "resolvable", DisplayName: "Resolvable"}
	identity := gatewayIdentity(t, call, scope.OperationCreate, command)
	created, _, err := capability.CreateScope(ctx, identity, scope.CreateParams{ParentSpaceID: command.ParentSpaceID, SpaceType: command.SpaceType, ExternalID: command.ExternalID, DisplayName: command.DisplayName})
	if err != nil {
		t.Fatalf("CreateScope() error = %v", err)
	}

	resolved, err := capability.ResolveScope(ctx, scope.ResolveParams{ParentSpaceID: stringPointer("space-1"), SpaceType: "project", ExternalID: "resolvable"})
	if err != nil || resolved.SpaceID != created.SpaceID {
		t.Fatalf("ResolveScope() = %+v error=%v", resolved, err)
	}
	if _, err := capability.ResolveScope(ctx, scope.ResolveParams{ParentSpaceID: stringPointer("space-1"), SpaceType: "project", ExternalID: "missing"}); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("missing ResolveScope() error = %v, want not_found", err)
	}
}

func TestMemoryCapabilityClassifiesStableErrors(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	capability := NewMemoryCapability(repository)
	call := gatewayCall("gateway-caller", "remember-conflict", "request-remember-conflict")
	command := memgw.RememberCommand{SpaceID: "space-1", Content: "first", Kind: "fact", Sensitivity: "normal", SourceName: "test", OccurredAt: "2026-08-21T12:00:00Z", Reason: "gateway"}
	params := memgw.RememberParams{SpaceID: "space-1", Content: "first", Kind: "fact", Sensitivity: "normal", SourceName: "test", OccurredAt: "2026-08-21T12:00:00Z", Reason: "gateway"}
	identity := gatewayIdentity(t, call, memgw.OperationRemember, command)
	if _, _, err := capability.Remember(ctx, identity, params); err != nil {
		t.Fatalf("Remember() error = %v", err)
	}
	conflict := command
	conflict.Content = "second"
	conflictParams := params
	conflictParams.Content = "second"
	conflictIdentity := gatewayIdentity(t, call, memgw.OperationRemember, conflict)
	_, _, err := capability.Remember(ctx, conflictIdentity, conflictParams)
	if !gateway.IsCode(err, gateway.ErrorIdempotencyConflict) {
		t.Fatalf("error = %v, want idempotency_conflict", err)
	}
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want wrapped storage sentinel", err)
	}
}

func TestFeedbackCapabilityAppendSchedulesAggregate(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "gateway-feedback")
	capability := NewFeedbackCapability(repository)
	call := gatewayCall("gateway-caller", "feedback-1", "request-feedback-1")
	command := feedback.AppendCommand{SpaceID: "space-1", MemoryID: memory.MemoryID, Purpose: domain.FeedbackPurposeMemoryQuality, FeedbackType: "confirm", ReasonCode: "other"}
	identity := gatewayIdentity(t, call, feedback.OperationAppend, command)

	response, replay, err := capability.AppendFeedback(ctx, identity, feedback.AppendParams{SpaceID: command.SpaceID, MemoryID: command.MemoryID, Purpose: command.Purpose, FeedbackType: command.FeedbackType, ReasonCode: command.ReasonCode})
	if err != nil || replay || response.Feedback.RevisionID != memory.CurrentRevision.RevisionID {
		t.Fatalf("AppendFeedback() = %+v replay=%t error=%v", response, replay, err)
	}
	var jobs int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='aggregate_feedback' AND target_id=?`, memory.CurrentRevision.RevisionID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("aggregate jobs = %d error=%v", jobs, err)
	}
	if _, err := capability.FeedbackAggregateHealth(ctx); err != nil {
		t.Fatalf("FeedbackAggregateHealth() error = %v", err)
	}
}

func TestRecallCapabilityReturnsContextAndCursor(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	createTestMemory(t, repository, "gateway-recall")
	capability := NewRecallCapability(repository)

	plan := retrieval.BuildQueryPlan("searchable", retrieval.PlanOptions{ReferenceTime: repository.runtime.Now(), Limit: 10, CandidateBudget: 100, MaxChars: 1000, RequestedScope: []string{"space-1"}})
	processor := gwretrieval.NewService(gwretrieval.Ports{}, gwretrieval.Defaults{})
	response, err := capability.Recall(ctx, gwretrieval.RecallParams{Query: "searchable", SpaceIDs: []string{"space-1"}, IncludeAncestors: true, States: []string{"active"}, Channels: []string{"keyword"}, EffectiveChannels: []string{"keyword"}, Limit: 10, MaxChars: 1000, MaxContextTokens: 500, ReferenceTime: formatTime(repository.runtime.Now()), Plan: plan}, processor)
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(response.Context.Items) == 0 {
		t.Fatalf("recall returned no context: %+v", response.Context)
	}
}

func TestEventCapabilityIngestIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	capability := NewEventCapability(repository)
	call := gatewayCall("gateway-caller", "ingest-1", "request-ingest-1")
	command := extraction.IngestCommand{SpaceID: "space-1", SourceType: "manual", SourceName: "test", OccurredAt: "2026-08-21T12:00:00Z", Content: domain.Content{Kind: "text", Text: "raw event"}}
	identity := gatewayIdentity(t, call, extraction.OperationIngest, command)

	record, replay, err := capability.IngestEvent(ctx, identity, extraction.IngestParams{SpaceID: command.SpaceID, SourceType: command.SourceType, SourceName: command.SourceName, OccurredAt: command.OccurredAt, Content: command.Content})
	if err != nil || replay || record.EventID == "" || record.OperationID == "" || len(record.JobIDs) != 1 {
		t.Fatalf("IngestEvent() = %+v replay=%t error=%v", record, replay, err)
	}
	var audits, jobs int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM audit_events WHERE action='ingest_event' AND target_id=?), (SELECT count(*) FROM jobs WHERE target_type='raw_event' AND target_id=?)`, record.EventID, record.EventID).Scan(&audits, &jobs); err != nil || audits != 1 || jobs != 1 {
		t.Fatalf("audits=%d jobs=%d error=%v", audits, jobs, err)
	}
	replayed, replay, err := capability.IngestEvent(ctx, identity, extraction.IngestParams{SpaceID: command.SpaceID, SourceType: command.SourceType, SourceName: command.SourceName, OccurredAt: command.OccurredAt, Content: command.Content})
	if err != nil || !replay || replayed.EventID != record.EventID {
		t.Fatalf("replay = %+v replay=%t error=%v", replayed, replay, err)
	}
	if _, err := capability.GetEvent(ctx, record.EventID); err != nil {
		t.Fatalf("GetEvent() error = %v", err)
	}
}

func TestExtractionWorkCapabilityPrepareCommitAndFencing(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	claim, input := claimProcessingJob(t, repository, 3)
	capability := NewExtractionWorkCapability(repository)
	work := workClaim(claim)

	prepared, err := capability.Prepare(ctx, work, extraction.PrepareRequest{ProcessorIdentity: "processor/test", AllowedMemoryTypes: []string{"fact"}, MaxCandidates: 2, MaxContentChars: 100})
	if err != nil || prepared.JobID != claim.Job.JobID {
		t.Fatalf("Prepare() = %+v error=%v", prepared, err)
	}
	stale := work
	stale.LeaseGeneration++
	result := domain.ProcessingResult{Code: domain.ProcessingCandidatesExtracted, Proposals: []domain.MemoryCandidateProposal{processingCandidate("gateway candidate")}, ProviderIdentity: "provider/test", ModelIdentity: "model-test"}
	if err := capability.Commit(ctx, stale, input, extraction.CommitRequest{Result: result}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale Commit() error = %v, want ErrLeaseLost", err)
	}
	if err := capability.Commit(ctx, work, input, extraction.CommitRequest{Result: result}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	var status string
	if err := repository.store.reader.QueryRow(`SELECT status FROM jobs WHERE job_id=?`, claim.Job.JobID).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("job status = %q error=%v", status, err)
	}
}

func TestExtractionWorkCapabilityBlockAndFailureAreFenced(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	claim, input := claimProcessingJob(t, repository, 3)
	capability := NewExtractionWorkCapability(repository)
	work := workClaim(claim)

	if err := capability.Block(ctx, work, "private content\nleak"); !errors.Is(err, ErrInvalidJobState) {
		t.Fatalf("unsafe Block() error = %v, want ErrInvalidJobState", err)
	}
	stale := work
	stale.LeaseGeneration++
	if err := capability.Block(ctx, stale, "target_policy_denied"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale Block() error = %v, want ErrLeaseLost", err)
	}
	if err := capability.Block(ctx, work, "target_policy_denied"); err != nil {
		t.Fatalf("Block() error = %v", err)
	}
	var status, blockedReason, eventStatus, attemptOutcome string
	if err := repository.store.reader.QueryRow(`SELECT (SELECT status FROM jobs WHERE job_id=?), (SELECT blocked_reason_code FROM jobs WHERE job_id=?), (SELECT processing_status FROM raw_events WHERE event_id='event-1'), (SELECT outcome FROM job_attempts WHERE job_id=?)`, work.JobID, work.JobID, work.JobID).Scan(&status, &blockedReason, &eventStatus, &attemptOutcome); err != nil {
		t.Fatal(err)
	}
	if status != "blocked_policy" || blockedReason != "target_policy_denied" || eventStatus != "stored" || attemptOutcome != "blocked_policy" {
		t.Fatalf("blocked state = %s/%s/%s/%s", status, blockedReason, eventStatus, attemptOutcome)
	}

	repository, _ = newMemoryTestRepository(t)
	capability = NewExtractionWorkCapability(repository)
	claim, input = claimProcessingJob(t, repository, 3)
	work = workClaim(claim)
	if err := capability.Fail(ctx, work, input, extraction.Failure{Code: "request_timeout_or_cancelled", Message: "processor transient failure", Transient: true, Backoff: time.Second}); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	var errorCode, lastMessage string
	if err := repository.store.reader.QueryRow(`SELECT (SELECT status FROM jobs WHERE job_id=?), (SELECT last_error_code FROM jobs WHERE job_id=?), (SELECT last_error_message FROM jobs WHERE job_id=?), (SELECT processing_status FROM raw_events WHERE event_id='event-1')`, work.JobID, work.JobID, work.JobID).Scan(&status, &errorCode, &lastMessage, &eventStatus); err != nil {
		t.Fatal(err)
	}
	if status != "retrying" || errorCode != "request_timeout_or_cancelled" || eventStatus != "processing" {
		t.Fatalf("failed state = %s/%s/%s message=%q", status, errorCode, eventStatus, lastMessage)
	}
}
func TestPolicyCapabilityEffectivePolicy(t *testing.T) {
	ctx := context.Background()
	repository := newConfigTestRepository(t)
	commitPolicy(t, repository, []PolicyAssignmentRecord{{AssignmentID: "root-allow", PolicyIdentity: "policy-1", Capability: "processing", SelectorKind: "space", SpaceID: "root", Effect: "allow", IncludeDescendants: true}})
	insertPolicySpace(t, repository, "root", "", "user", "owner")
	insertPolicySpace(t, repository, "project", "root", "project", "project-a")
	capability := NewPolicyCapability(repository)

	decision, err := capability.EffectivePolicy(ctx, policy.CapabilityProcessing, "project")
	if err != nil || decision.Decision != "allow" || decision.PolicyGeneration != 1 {
		t.Fatalf("EffectivePolicy() = %+v error=%v", decision, err)
	}
	if decision.Capability != policy.CapabilityProcessing || decision.SpaceID != "project" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPolicyCapabilityReportsUnconfiguredAndServiceDecides(t *testing.T) {
	ctx := context.Background()
	repository := newConfigTestRepository(t)
	commitPolicy(t, repository, []PolicyAssignmentRecord{{AssignmentID: "project-allow", PolicyIdentity: "policy-1", Capability: "processing", SelectorKind: "space", SpaceID: "project", Effect: "allow"}})
	insertPolicySpace(t, repository, "root", "", "user", "owner")
	insertPolicySpace(t, repository, "project", "root", "project", "project-a")
	insertPolicySpace(t, repository, "other", "root", "project", "other")
	capability := NewPolicyCapability(repository)
	service := policy.NewService(capability)
	call := gatewayCall("gateway-caller", "policy-key", "request-policy")

	if _, err := capability.EffectivePolicy(ctx, policy.CapabilityRecall, "project"); !errors.Is(err, policy.ErrUnconfigured) {
		t.Fatalf("unconfigured capability error = %v, want policy.ErrUnconfigured", err)
	}
	if _, err := capability.EffectivePolicy(ctx, policy.CapabilityProcessing, "missing"); !errors.Is(err, policy.ErrUnconfigured) {
		t.Fatalf("unconfigured scope error = %v, want policy.ErrUnconfigured", err)
	}

	decision, err := service.Authorize(ctx, call, policy.Request{Capability: policy.CapabilityRecall, SpaceIDs: []string{"project"}})
	if err != nil || !decision.Allowed {
		t.Fatalf("offline default Authorize() = %+v error=%v", decision, err)
	}
	decision, err = service.Authorize(ctx, call, policy.Request{Capability: policy.CapabilityProcessing, SpaceIDs: []string{"project"}})
	if err != nil || !decision.Allowed || decision.PolicyGeneration != 1 {
		t.Fatalf("inherited Authorize() = %+v error=%v", decision, err)
	}
	decision, err = service.Authorize(ctx, call, policy.Request{Capability: policy.CapabilityProcessing, SpaceIDs: []string{"other"}})
	if err != nil || decision.Allowed || decision.ReasonCode != "default_deny" {
		t.Fatalf("default-deny Authorize() = %+v error=%v", decision, err)
	}
}

func TestConsolidationCapabilityConfirmIsAtomic(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	candidate := createTestCandidate(t, repository)
	capability := NewConsolidationCapability(repository)
	call := gatewayCall("gateway-caller", "confirm-1", "request-confirm-1")
	command := consolidation.ConfirmCommand{CandidateID: candidate.CandidateID, ReasonCode: "reviewed"}
	identity := gatewayIdentity(t, call, consolidation.OperationConfirm, command)

	memory, replay, err := capability.ConfirmCandidate(ctx, identity, candidate.CandidateID, consolidation.ReviewParams{ReasonCode: command.ReasonCode})
	if err != nil || replay || memory.MemoryID == "" {
		t.Fatalf("ConfirmCandidate() = %+v replay=%t error=%v", memory, replay, err)
	}
	var state string
	var audits, idempotency int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT review_state FROM candidates WHERE candidate_id=?), (SELECT count(*) FROM audit_events WHERE action='confirm_candidate' AND target_id=?), (SELECT count(*) FROM idempotency_records WHERE normalized_route='consolidation.confirm' AND idempotency_key='confirm-1')`, candidate.CandidateID, candidate.CandidateID).Scan(&state, &audits, &idempotency); err != nil {
		t.Fatal(err)
	}
	if state != "confirmed" || audits != 1 || idempotency != 1 {
		t.Fatalf("state=%q audits=%d idempotency=%d", state, audits, idempotency)
	}
	if _, err := capability.GetConsolidationDecision(ctx, candidate.CandidateID); err != nil {
		t.Fatalf("GetConsolidationDecision() error = %v", err)
	}
}

func TestKnowledgeReviewCapabilityDispatchesDecision(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	params := CreateEntityProposalParams{EventID: "event-1", SpaceID: "space-1", ProposalKind: "entity", EntityType: "person", CanonicalName: "Gateway", Sensitivity: "normal", ProcessorIdentity: "processor/test", ProcessingVersion: "entity-v1"}
	proposal := createEntityProposalTest(t, repository, params)
	capability := NewKnowledgeReviewCapability(repository)

	call := gatewayCall("gateway-caller", "entity-review", "request-entity-review")
	identity := gatewayIdentity(t, call, gateway.OperationName("knowledge.review_entity"), knowledge.ReviewProposalCommand{ProposalID: proposal.ID, Decision: knowledge.ReviewConfirm, ReasonCode: "approved"})
	reviewed, replay, err := capability.ReviewEntityProposal(ctx, identity, proposal.ID, knowledge.ReviewParams{Decision: knowledge.ReviewConfirm, ReasonCode: "approved"})
	if err != nil || replay || reviewed.ResultEntityID == nil {
		t.Fatalf("ReviewEntityProposal() = %+v replay=%t error=%v", reviewed, replay, err)
	}
	if _, _, err := capability.ReviewEntityProposal(ctx, identity, proposal.ID, knowledge.ReviewParams{ReasonCode: "approved"}); err == nil {
		t.Fatal("expected an error for an invalid review decision")
	}
	reader := NewKnowledgeReadCapability(repository)
	if _, err := reader.GetEntity(ctx, knowledge.GetEntityQuery{EntityID: *reviewed.ResultEntityID}); err != nil {
		t.Fatalf("GetEntity() error = %v", err)
	}
}

func TestUnitOfWorkCommitsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	unit := repository.UnitOfWork()
	identity := gatewayIdentity(t, gatewayCall("uow-caller", "uow-key", "request-uow"), gwretrieval.OperationRecall, struct{ Value string }{Value: "uow"})
	now := "2026-08-21T12:00:00Z"
	spaceParent := "space-1"
	space := domain.Space{SpaceID: "uow-space", ParentSpaceID: &spaceParent, SpaceType: "project", ExternalID: "uow", DisplayName: "UoW", CreatedAt: now}

	err := unit.Do(ctx, func(ctx context.Context, writers *TxWriters) error {
		if err := writers.Authority.InsertSpace(ctx, space); err != nil {
			return err
		}
		if err := writers.Jobs.Enqueue(ctx, JobSpec{JobID: "uow-job", OperationID: "uow-op", JobType: "extract_memory", TargetType: "raw_event", TargetID: "event-1", ProcessingVersion: "test-v1", MaxAttempts: 3, CreatedAt: now}); err != nil {
			return err
		}
		if err := writers.Audit.Record(ctx, AuditEvent{AuditID: "uow-audit", OperationID: "uow-op", RequestID: "request-uow", CallerID: "uow-caller", Action: "uow", TargetType: "space", TargetID: "uow-space", Outcome: "succeeded", CreatedAt: now}); err != nil {
			return err
		}
		if err := writers.Policy.PersistEffective(ctx, EffectiveSpacePolicy{Capability: "processing", SpaceID: "uow-space", PolicyGeneration: 1, Decision: "deny", ReasonCode: "default_deny"}, now); err != nil {
			return err
		}
		return writers.Idempotency.Write(ctx, identity, "uow-op", 200, []byte(`{"ok":true}`), now)
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	var spaces, jobs, audits, policies, idempotency int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memory_spaces WHERE space_id='uow-space'),(SELECT count(*) FROM jobs WHERE job_id='uow-job'),(SELECT count(*) FROM audit_events WHERE audit_id='uow-audit'),(SELECT count(*) FROM effective_space_policies WHERE space_id='uow-space'),(SELECT count(*) FROM idempotency_records WHERE idempotency_key='uow-key')`).Scan(&spaces, &jobs, &audits, &policies, &idempotency); err != nil {
		t.Fatal(err)
	}
	if spaces != 1 || jobs != 1 || audits != 1 || policies != 1 || idempotency != 1 {
		t.Fatalf("committed rows spaces=%d jobs=%d audits=%d policies=%d idempotency=%d", spaces, jobs, audits, policies, idempotency)
	}

	rollbackSpace := domain.Space{SpaceID: "uow-rollback", ParentSpaceID: &spaceParent, SpaceType: "project", ExternalID: "rollback", DisplayName: "Rollback", CreatedAt: now}
	injected := errors.New("injected failure")
	if err := unit.Do(ctx, func(ctx context.Context, writers *TxWriters) error {
		if err := writers.Authority.InsertSpace(ctx, rollbackSpace); err != nil {
			return err
		}
		return injected
	}); !errors.Is(err, injected) {
		t.Fatalf("Do() error = %v, want injected failure", err)
	}
	var rolledBack int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM memory_spaces WHERE space_id='uow-rollback'`).Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("rolled back rows = %d error=%v", rolledBack, err)
	}
}

func TestConsolidationClassifierBridgeMapsProviderProposal(t *testing.T) {
	candidate := domain.MemoryRevision{Kind: domain.MemoryKindFact, CanonicalContent: "bridge"}
	bridge := &ConsolidationClassifierBridge{Classifier: fakeClassifier{}}
	proposal, err := bridge.ClassifyConsolidation(context.Background(), candidate, []ConsolidationNeighbor{{MemoryID: "memory-1", RevisionID: "revision-1", CanonicalContent: "neighbor"}})
	if err != nil || proposal.Action != domain.ConsolidationRefine || proposal.MemoryID != "memory-1" {
		t.Fatalf("ClassifyConsolidation() = %+v error=%v", proposal, err)
	}
}

type fakeClassifier struct{}

func (fakeClassifier) Classify(_ context.Context, request consolidation.ClassifyRequest) (consolidation.Classification, error) {
	if len(request.Neighbors) != 1 || request.Neighbors[0].MemoryID != "memory-1" {
		return consolidation.Classification{}, errors.New("neighbor not mapped")
	}
	return consolidation.Classification{Action: domain.ConsolidationRefine, ReasonCode: "provider", MemoryID: "memory-1"}, nil
}
