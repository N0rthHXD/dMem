package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/policy"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
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

func TestScopeCapabilityEnsureIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	capability := NewScopeCapability(repository)
	call := gatewayCall("gateway-caller", "scope-ensure", "request-scope-ensure")
	command := scope.EnsureCommand{ParentSpaceID: stringPointer("space-1"), SpaceType: "project", ExternalID: "ensured", DisplayName: "Ensured"}
	identity := gatewayIdentity(t, call, scope.OperationEnsure, command)
	params := scope.EnsureParams{ParentSpaceID: command.ParentSpaceID, SpaceType: command.SpaceType, ExternalID: command.ExternalID, DisplayName: command.DisplayName}

	created, replay, err := capability.EnsureScope(ctx, identity, params)
	if err != nil || replay || created.SpaceID == "" {
		t.Fatalf("EnsureScope() = %+v replay=%t error=%v", created, replay, err)
	}
	replayed, replay, err := capability.EnsureScope(ctx, identity, params)
	if err != nil || !replay || replayed.SpaceID != created.SpaceID {
		t.Fatalf("replayed EnsureScope() = %+v replay=%t error=%v", replayed, replay, err)
	}
	// Resolving by stable external identity wins over the supplied display name:
	// a repeated ensure is a resolve, not a second create, and it never rewrites
	// the existing scope.
	renamed := params
	renamed.DisplayName = "Renamed"
	resolved, replay, err := capability.EnsureScope(ctx, gatewayIdentity(t, call, scope.OperationEnsure, scope.EnsureCommand{ParentSpaceID: command.ParentSpaceID, SpaceType: command.SpaceType, ExternalID: command.ExternalID, DisplayName: "Renamed"}), renamed)
	if err != nil || resolved.SpaceID != created.SpaceID || resolved.DisplayName != created.DisplayName {
		t.Fatalf("re-ensure with a new display name = %+v replay=%t error=%v", resolved, replay, err)
	}
}

func TestScopeCapabilityCreateAndListCursor(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	capability := NewScopeCapability(repository)
	for _, external := range []string{"project-a", "project-b", "project-c"} {
		call := gatewayCall("gateway-caller", "scope-"+external, "request-scope-"+external)
		command := scope.EnsureCommand{ParentSpaceID: stringPointer("space-1"), SpaceType: "project", ExternalID: external, DisplayName: external}
		identity := gatewayIdentity(t, call, scope.OperationEnsure, command)
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
	command := scope.EnsureCommand{ParentSpaceID: stringPointer("space-1"), SpaceType: "project", ExternalID: "resolvable", DisplayName: "Resolvable"}
	identity := gatewayIdentity(t, call, scope.OperationEnsure, command)
	created, _, err := capability.CreateScope(ctx, identity, scope.CreateParams{ParentSpaceID: command.ParentSpaceID, SpaceType: command.SpaceType, ExternalID: command.ExternalID, DisplayName: command.DisplayName})
	if err != nil {
		t.Fatalf("CreateScope() error = %v", err)
	}

	resolved, err := capability.ResolveScope(ctx, scope.ResolveParams{ParentSpaceID: stringPointer("space-1"), SpaceType: "project", ExternalID: "resolvable"})
	if err != nil || resolved.SpaceID != created.SpaceID {
		t.Fatalf("ResolveScope() = %+v error=%v", resolved, err)
	}
	if _, err := capability.ResolveScope(ctx, scope.ResolveParams{ParentSpaceID: stringPointer("space-1"), SpaceType: "project", ExternalID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing ResolveScope() error = %v, want ErrNotFound", err)
	}
}

func TestReviewCapabilityConfirmAndRejectIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	capability := NewReviewCapability(repository)
	candidate := createTestCandidate(t, repository)

	page, err := capability.ListCandidates(ctx, review.CandidateFilter{SpaceID: candidate.SpaceID, Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListCandidates() = %+v error=%v", page, err)
	}
	if _, err := capability.GetCandidate(ctx, candidate.CandidateID); err != nil {
		t.Fatalf("GetCandidate() error = %v", err)
	}

	call := gatewayCall("gateway-caller", "review-confirm", "request-review-confirm")
	command := review.ReviewCandidateCommand{CandidateID: candidate.CandidateID, ReasonCode: "reviewed"}
	identity := gatewayIdentity(t, call, review.OperationCandidateConfirm, command)
	memory, operationID, replay, err := capability.ConfirmCandidate(ctx, identity, review.ReviewParams{CandidateID: command.CandidateID, ReasonCode: command.ReasonCode})
	if err != nil || replay || memory.MemoryID == "" || operationID == "" {
		t.Fatalf("ConfirmCandidate() = %+v op=%q replay=%t error=%v", memory, operationID, replay, err)
	}
	replayedMemory, replayedOperationID, replay, err := capability.ConfirmCandidate(ctx, identity, review.ReviewParams{CandidateID: command.CandidateID, ReasonCode: command.ReasonCode})
	if err != nil || !replay || replayedMemory.MemoryID != memory.MemoryID || replayedOperationID != operationID {
		t.Fatalf("replayed ConfirmCandidate() = %+v op=%q replay=%t error=%v", replayedMemory, replayedOperationID, replay, err)
	}

	params := candidateParams()
	params.Revision.DisplayContent = "gateway review reject candidate"
	params.Revision.CanonicalContent = "gateway review reject candidate"
	rejectable, _, err := repository.CreateCandidate(ctx, params)
	if err != nil {
		t.Fatalf("CreateCandidate() error = %v", err)
	}
	rejectCall := gatewayCall("gateway-caller", "review-reject", "request-review-reject")
	rejectCommand := review.ReviewCandidateCommand{CandidateID: rejectable.CandidateID, ReasonCode: "irrelevant"}
	rejectIdentity := gatewayIdentity(t, rejectCall, review.OperationCandidateReject, rejectCommand)
	rejected, rejectOperation, rejectReplay, err := capability.RejectCandidate(ctx, rejectIdentity, review.ReviewParams{CandidateID: rejectCommand.CandidateID, ReasonCode: rejectCommand.ReasonCode})
	if err != nil || rejectReplay || rejected.ReviewState != "rejected" || rejectOperation == "" {
		t.Fatalf("RejectCandidate() = %+v op=%q replay=%t error=%v", rejected, rejectOperation, rejectReplay, err)
	}
	replayedCandidate, replayedRejectOperation, replay, err := capability.RejectCandidate(ctx, rejectIdentity, review.ReviewParams{CandidateID: rejectCommand.CandidateID, ReasonCode: rejectCommand.ReasonCode})
	if err != nil || !replay || replayedCandidate.ReviewState != "rejected" || replayedRejectOperation != rejectOperation {
		t.Fatalf("replayed RejectCandidate() = %+v op=%q replay=%t error=%v", replayedCandidate, replayedRejectOperation, replay, err)
	}
	secondIdentity := gatewayIdentity(t, gatewayCall("gateway-caller", "review-reject-2", "request-review-reject-2"), review.OperationCandidateReject, rejectCommand)
	if _, _, _, err := capability.RejectCandidate(ctx, secondIdentity, review.ReviewParams{CandidateID: rejectCommand.CandidateID, ReasonCode: rejectCommand.ReasonCode}); err == nil {
		t.Fatal("second reject of a reviewed candidate must conflict")
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
	repository.SetProcessingEnabled(true)
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

func TestEventCapabilityIngestWithoutProcessingStoresEventWithoutJob(t *testing.T) {
	ctx := context.Background()
	repository, _ := newMemoryTestRepository(t)
	capability := NewEventCapability(repository)
	call := gatewayCall("gateway-caller", "ingest-disabled", "request-ingest-disabled")
	command := extraction.IngestCommand{SpaceID: "space-1", SourceType: "manual", SourceName: "test", OccurredAt: "2026-08-21T12:00:00Z", Content: domain.Content{Kind: "text", Text: "stored only"}}
	identity := gatewayIdentity(t, call, extraction.OperationIngest, command)

	record, replay, err := capability.IngestEvent(ctx, identity, extraction.IngestParams{SpaceID: command.SpaceID, SourceType: command.SourceType, SourceName: command.SourceName, OccurredAt: command.OccurredAt, Content: command.Content})
	if err != nil || replay || record.Status != "stored" || len(record.JobIDs) != 0 {
		t.Fatalf("IngestEvent() = %+v replay=%t error=%v", record, replay, err)
	}
	var jobs int
	var status string
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM jobs WHERE target_type='raw_event' AND target_id=?), (SELECT processing_status FROM raw_events WHERE event_id=?)`, record.EventID, record.EventID).Scan(&jobs, &status); err != nil || jobs != 0 || status != "stored" {
		t.Fatalf("jobs=%d status=%q error=%v", jobs, status, err)
	}
	event, err := capability.GetEvent(ctx, record.EventID)
	if err != nil || event.ProcessingStatus != "stored" {
		t.Fatalf("GetEvent() = %+v error=%v", event, err)
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
