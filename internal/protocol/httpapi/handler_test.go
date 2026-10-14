package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/internal/gateway/conflict"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

type fakeTools struct {
	call    gateway.CallContext
	command any
}

func (f *fakeTools) Recall(_ context.Context, call gateway.CallContext, command agenttool.RecallCommand) (agenttool.RecallResult, error) {
	f.call, f.command = call, command
	return agenttool.RecallResult{OperationID: "recall-op", Memories: []agenttool.RecalledMemory{{MemoryID: "m", RevisionID: "r", Kind: domain.MemoryKindFact,
		Content: "answer", Confidence: 1, Evidence: []agenttool.EvidenceReference{}}}, DegradedCapabilities: []agenttool.Capability{}, NextCursor: nil}, nil
}
func (f *fakeTools) MemoryCommit(_ context.Context, call gateway.CallContext, command agenttool.MemoryCommitCommand) (agenttool.MemoryCommitResult, error) {
	f.call, f.command = call, command
	return agenttool.MemoryCommitResult{WriteResult: gateway.WriteResult{OperationID: "op", Replay: true}, SourceEventID: "event",
		Memories: []agenttool.CommitOutcome{{LocalID: "local", Status: agenttool.CommitAuthority, MemoryID: "m", RevisionID: "r"}},
		Entities: []agenttool.CommitOutcome{}, Facts: []agenttool.CommitOutcome{}, Relations: []agenttool.CommitOutcome{}}, nil
}
func (f *fakeTools) Feedback(_ context.Context, call gateway.CallContext, command agenttool.FeedbackCommand) (agenttool.FeedbackResult, error) {
	f.call, f.command = call, command
	return agenttool.FeedbackResult{WriteResult: gateway.WriteResult{OperationID: "op"}, FeedbackID: "fb", MemoryID: command.MemoryID, RevisionID: command.RevisionID, Kind: command.Kind}, nil
}
func (f *fakeTools) Forget(_ context.Context, call gateway.CallContext, command agenttool.ForgetCommand) (agenttool.ForgetResult, error) {
	f.call, f.command = call, command
	return agenttool.ForgetResult{WriteResult: gateway.WriteResult{OperationID: "op"}, MemoryID: command.MemoryID, RevisionID: command.ExpectedRevisionID, State: "deleted"}, nil
}

type fakeScopes struct{ calls []string }

func (f *fakeScopes) Ensure(_ context.Context, _ gateway.CallContext, command scope.EnsureCommand) (scope.EnsureResult, error) {
	f.calls = append(f.calls, "ensure")
	return scope.EnsureResult{WriteResult: gateway.WriteResult{OperationID: "op"}, Scope: testScope(command)}, nil
}
func (f *fakeScopes) Get(_ context.Context, _ gateway.CallContext, query scope.GetQuery) (domain.Space, error) {
	f.calls = append(f.calls, "get")
	return domain.Space{SpaceID: query.SpaceID, SpaceType: "project", ExternalID: "external", DisplayName: "Project", CreatedAt: "2026-09-20T10:00:00Z"}, nil
}
func testScope(command scope.EnsureCommand) domain.Space {
	return domain.Space{SpaceID: "scope", ParentSpaceID: command.ParentSpaceID, SpaceType: command.SpaceType, ExternalID: command.ExternalID,
		DisplayName: command.DisplayName, CreatedAt: "2026-09-20T10:00:00Z"}
}

type fakeEvents struct {
	call    gateway.CallContext
	command any
}

func (f *fakeEvents) Ingest(_ context.Context, call gateway.CallContext, command extraction.IngestCommand) (extraction.IngestResult, error) {
	f.call, f.command = call, command
	return extraction.IngestResult{WriteResult: gateway.WriteResult{OperationID: "op", Replay: true}, EventID: "event",
		Status: "accepted", JobIDs: []string{"job"}, AcceptedAt: "2026-09-20T10:00:00Z"}, nil
}
func (f *fakeEvents) GetEvent(_ context.Context, _ gateway.CallContext, query extraction.GetEventQuery) (domain.RawEvent, error) {
	return domain.RawEvent{EventID: query.EventID, OperationID: "op", SpaceID: "scope", SourceType: "manual", SourceName: "collector",
		OccurredAt: "2026-09-20T10:00:00Z", ReceivedAt: "2026-09-20T10:00:01Z", ProcessingStatus: "stored",
		Content: domain.Content{Kind: "text", MediaType: "text/plain", Text: "body"}}, nil
}

type fakeReviews struct {
	call    gateway.CallContext
	command any
	err     error
}

func (f *fakeReviews) ListCandidates(_ context.Context, call gateway.CallContext, query review.ListCandidatesQuery) (domain.CandidatePage, error) {
	f.call, f.command = call, query
	if f.err != nil {
		return domain.CandidatePage{}, f.err
	}
	return domain.CandidatePage{Items: []domain.Candidate{testCandidate("candidate-1")}}, nil
}
func (f *fakeReviews) GetCandidate(_ context.Context, call gateway.CallContext, query review.GetCandidateQuery) (domain.Candidate, error) {
	f.call, f.command = call, query
	if f.err != nil {
		return domain.Candidate{}, f.err
	}
	return testCandidate(query.CandidateID), nil
}
func (f *fakeReviews) ConfirmCandidate(_ context.Context, call gateway.CallContext, command review.ReviewCandidateCommand) (review.CandidateReviewResult, error) {
	f.call, f.command = call, command
	if f.err != nil {
		return review.CandidateReviewResult{}, f.err
	}
	return review.CandidateReviewResult{
		WriteResult: gateway.WriteResult{OperationID: "op", Replay: true},
		Candidate:   testCandidate(command.CandidateID),
		Memory:      &domain.Memory{MemoryID: "memory-1"},
	}, nil
}
func (f *fakeReviews) RejectCandidate(_ context.Context, call gateway.CallContext, command review.ReviewCandidateCommand) (review.CandidateReviewResult, error) {
	f.call, f.command = call, command
	if f.err != nil {
		return review.CandidateReviewResult{}, f.err
	}
	candidate := testCandidate(command.CandidateID)
	candidate.ReviewState = "rejected"
	return review.CandidateReviewResult{WriteResult: gateway.WriteResult{OperationID: "op"}, Candidate: candidate}, nil
}

func testCandidate(id string) domain.Candidate {
	return domain.Candidate{
		CandidateID: id, EventID: "event", SpaceID: "scope", ReviewState: "pending",
		ProcessorIdentity: "processor/test", ProcessingVersion: "candidate-v1", SpecificationHash: "hash",
		Revision: domain.MemoryRevision{Kind: domain.MemoryKindFact, DisplayContent: "content", CanonicalContent: "content", Sensitivity: "normal"},
		Evidence: []domain.RevisionEvidence{}, CreatedAt: "2026-09-20T10:00:00Z",
	}
}

type fakeMemories struct {
	call    gateway.CallContext
	command any
	err     error
}

func (f *fakeMemories) List(_ context.Context, call gateway.CallContext, query memory.ListQuery) (domain.MemoryPage, error) {
	f.call, f.command = call, query
	if f.err != nil {
		return domain.MemoryPage{}, f.err
	}
	return domain.MemoryPage{Items: []domain.Memory{testMemory("memory-1")}}, nil
}
func (f *fakeMemories) Get(_ context.Context, call gateway.CallContext, query memory.GetQuery) (domain.Memory, error) {
	f.call, f.command = call, query
	if f.err != nil {
		return domain.Memory{}, f.err
	}
	return testMemory(query.MemoryID), nil
}
func (f *fakeMemories) ListRevisions(_ context.Context, call gateway.CallContext, query memory.MemoryQuery) ([]domain.MemoryRevision, error) {
	f.call, f.command = call, query
	if f.err != nil {
		return nil, f.err
	}
	return []domain.MemoryRevision{testMemory(query.MemoryID).CurrentRevision}, nil
}
func (f *fakeMemories) ListStateEvents(_ context.Context, call gateway.CallContext, query memory.MemoryQuery) ([]domain.MemoryStateEvent, error) {
	f.call, f.command = call, query
	if f.err != nil {
		return nil, f.err
	}
	return []domain.MemoryStateEvent{{StateEventID: "state-event-1", MemoryID: query.MemoryID, RevisionID: "revision-1", OperationID: "op", ToState: "active", Reason: "created", CreatedBy: "operator", CreatedAt: "2026-09-20T10:00:00Z"}}, nil
}
func (f *fakeMemories) Revise(_ context.Context, call gateway.CallContext, command memory.ReviseCommand) (memory.WriteResult, error) {
	f.call, f.command = call, command
	if f.err != nil {
		return memory.WriteResult{}, f.err
	}
	return memory.WriteResult{WriteResult: gateway.WriteResult{OperationID: "op", Replay: true}, Memory: testMemory(command.MemoryID)}, nil
}
func (f *fakeMemories) ChangeState(_ context.Context, call gateway.CallContext, command memory.StateChangeCommand) (memory.WriteResult, error) {
	f.call, f.command = call, command
	if f.err != nil {
		return memory.WriteResult{}, f.err
	}
	value := testMemory(command.MemoryID)
	value.CurrentState = "archived"
	return memory.WriteResult{WriteResult: gateway.WriteResult{OperationID: "op"}, Memory: value}, nil
}

func testMemory(id string) domain.Memory {
	return domain.Memory{
		MemoryID: id, SpaceID: "scope", Kind: domain.MemoryKindFact, CurrentState: "active", Sensitivity: "normal",
		CurrentRevision: domain.MemoryRevision{RevisionID: "revision-1", MemoryID: id, Kind: domain.MemoryKindFact,
			DisplayContent: "content", CanonicalContent: "content", Sensitivity: "normal", Evidence: []domain.RevisionEvidence{}},
		CreatedAt: "2026-09-20T10:00:00Z", UpdatedAt: "2026-09-20T10:00:00Z",
	}
}

type fakeConflicts struct {
	call    gateway.CallContext
	command any
	err     error
}

func (f *fakeConflicts) List(_ context.Context, call gateway.CallContext, query conflict.ListQuery) (domain.ConflictCasePage, error) {
	f.call, f.command = call, query
	if f.err != nil {
		return domain.ConflictCasePage{}, f.err
	}
	return domain.ConflictCasePage{Items: []domain.ConflictCase{testConflictCase("case-1")}}, nil
}
func (f *fakeConflicts) Get(_ context.Context, call gateway.CallContext, query conflict.GetQuery) (domain.ConflictCase, error) {
	f.call, f.command = call, query
	if f.err != nil {
		return domain.ConflictCase{}, f.err
	}
	return testConflictCase(query.CaseID), nil
}
func (f *fakeConflicts) Resolve(_ context.Context, call gateway.CallContext, command conflict.ResolveCommand) (conflict.ResolveResult, error) {
	f.call, f.command = call, command
	if f.err != nil {
		return conflict.ResolveResult{}, f.err
	}
	value := testConflictCase(command.CaseID)
	value.State = "resolved"
	return conflict.ResolveResult{WriteResult: gateway.WriteResult{OperationID: "op", Replay: true}, Case: value}, nil
}

func testConflictCase(id string) domain.ConflictCase {
	return domain.ConflictCase{
		CaseID: id, SpaceID: "scope", State: "open", OpenedBy: "agent", ProcessingVersion: "test-v1",
		CreatedAt:    "2026-09-20T10:00:00Z",
		Participants: []domain.ConflictParticipant{{MemoryID: "memory-1", RevisionID: "revision-1"}, {MemoryID: "memory-2", RevisionID: "revision-2"}},
		Relations:    []domain.MemoryRelation{},
	}
}

type fakeProposals struct {
	call    gateway.CallContext
	command any
	err     error
}

func (f *fakeProposals) ListProposals(_ context.Context, call gateway.CallContext, query review.ListProposalsQuery) (review.ProposalPage, error) {
	f.call, f.command = call, query
	if f.err != nil {
		return review.ProposalPage{}, f.err
	}
	return review.ProposalPage{Items: []review.Proposal{testProposal(query.Kind)}}, nil
}
func (f *fakeProposals) GetProposal(_ context.Context, call gateway.CallContext, query review.GetProposalQuery) (review.Proposal, error) {
	f.call, f.command = call, query
	if f.err != nil {
		return review.Proposal{}, f.err
	}
	return testProposal(query.Kind), nil
}
func (f *fakeProposals) ConfirmProposal(_ context.Context, call gateway.CallContext, command review.ReviewProposalCommand) (review.ProposalReviewResult, error) {
	f.call, f.command = call, command
	if f.err != nil {
		return review.ProposalReviewResult{}, f.err
	}
	value := testProposal(command.Kind)
	if command.Kind == review.ProposalKindEntity && value.Entity != nil {
		value.Entity.ReviewState = "confirmed"
	}
	if command.Kind == review.ProposalKindEvolution && value.Evolution != nil {
		value.Evolution.ReviewState = "confirmed"
	}
	return review.ProposalReviewResult{WriteResult: gateway.WriteResult{OperationID: "op", Replay: true}, Proposal: value}, nil
}
func (f *fakeProposals) RejectProposal(_ context.Context, call gateway.CallContext, command review.ReviewProposalCommand) (review.ProposalReviewResult, error) {
	f.call, f.command = call, command
	if f.err != nil {
		return review.ProposalReviewResult{}, f.err
	}
	value := testProposal(command.Kind)
	if command.Kind == review.ProposalKindEntity && value.Entity != nil {
		value.Entity.ReviewState = "rejected"
	}
	return review.ProposalReviewResult{WriteResult: gateway.WriteResult{OperationID: "op"}, Proposal: value}, nil
}

func testProposal(kind review.ProposalKind) review.Proposal {
	switch kind {
	case review.ProposalKindFact:
		return review.NewFactProposal(domain.FactProposal{ProposalID: "proposal-1", SpaceID: "scope", Predicate: "works_for", ReviewState: "pending", Evidence: []domain.EntityFactEvidence{}, Reviews: []domain.FactProposalReview{}})
	case review.ProposalKindRelation:
		return review.NewRelationProposal(domain.RelationProposal{ProposalID: "proposal-1", SpaceID: "scope", RelationType: "supports", ReviewState: "pending", Evidence: []domain.RelationEvidence{}, Reviews: []domain.RelationProposalReview{}})
	case review.ProposalKindEvolution:
		return review.NewEvolutionProposal(domain.EvolutionProposal{ProposalID: "proposal-1", SpaceID: "scope", Action: "supplement", ReviewState: "pending", Targets: []domain.EvolutionTarget{{MemoryID: "memory-1", ExpectedRevisionID: "revision-1"}}, Evidence: []domain.RelationEvidence{}, Reviews: []domain.EvolutionReview{}})
	case review.ProposalKindAggregate:
		return review.NewAggregateProposal(domain.AggregateProposal{ProposalID: "proposal-1", SpaceID: "scope", AggregateKind: "topic", ReviewState: "pending", Inputs: []domain.AggregateInput{}, Reviews: []domain.AggregateProposalReview{}})
	default:
		return review.NewEntityProposal(domain.EntityProposal{ProposalID: "proposal-1", SpaceID: "scope", ProposalKind: "entity", Sensitivity: "normal", ReviewState: "pending", Reviews: []domain.EntityProposalReview{}})
	}
}

func testHandler(t *testing.T) (*Handler, *fakeTools, *fakeScopes) {
	t.Helper()
	tools, scopes := &fakeTools{}, &fakeScopes{}
	handler, err := New(Dependencies{AgentTools: tools, Scopes: scopes, Events: &fakeEvents{}, Reviews: &fakeReviews{}, Memories: &fakeMemories{}, Conflicts: &fakeConflicts{}, Proposals: &fakeProposals{}})
	if err != nil {
		t.Fatal(err)
	}
	return handler, tools, scopes
}

var targetRoutes = []RouteInfo{
	{Operation: OperationCapabilities, Method: http.MethodGet, Path: "/v1/capabilities", Public: true, Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: agenttool.OperationMemoryCommit, Method: http.MethodPost, Path: "/v1/memory-commits", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxMemoryCommitBodyBytes},
	{Operation: agenttool.OperationRecall, Method: http.MethodPost, Path: "/v1/recall", Access: AccessRead, Idempotency: IdempotencyNotRequired, MaxBodyBytes: MaxRecallBodyBytes},
	{Operation: agenttool.OperationFeedback, Method: http.MethodPost, Path: "/v1/feedback", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxFeedbackBodyBytes},
	{Operation: agenttool.OperationForget, Method: http.MethodPost, Path: "/v1/forget", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxForgetBodyBytes},
	{Operation: scope.OperationEnsure, Method: http.MethodPost, Path: "/v1/scopes/ensure", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxScopeBodyBytes},
	{Operation: OperationScopeGet, Method: http.MethodGet, Path: "/v1/scopes/{scope_id}", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: extraction.OperationIngest, Method: http.MethodPost, Path: "/v1/events", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxEventIngestBodyBytes},
	{Operation: OperationEventGet, Method: http.MethodGet, Path: "/v1/events/{event_id}", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: OperationCandidateList, Method: http.MethodGet, Path: "/v1/candidates", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: OperationCandidateGet, Method: http.MethodGet, Path: "/v1/candidates/{candidate_id}", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: review.OperationCandidateConfirm, Method: http.MethodPost, Path: "/v1/candidates/{candidate_id}/confirm", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxCandidateReviewBodyBytes},
	{Operation: review.OperationCandidateReject, Method: http.MethodPost, Path: "/v1/candidates/{candidate_id}/reject", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxCandidateReviewBodyBytes},
	{Operation: OperationMemoryList, Method: http.MethodGet, Path: "/v1/memories", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: OperationMemoryGet, Method: http.MethodGet, Path: "/v1/memories/{memory_id}", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: OperationMemoryRevisionList, Method: http.MethodGet, Path: "/v1/memories/{memory_id}/revisions", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: OperationMemoryStateEventList, Method: http.MethodGet, Path: "/v1/memories/{memory_id}/state-events", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: memory.OperationRevise, Method: http.MethodPost, Path: "/v1/memories/{memory_id}/revisions", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxMemoryReviseBodyBytes},
	{Operation: memory.OperationStateChange, Method: http.MethodPost, Path: "/v1/memories/{memory_id}/state", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxMemoryStateBodyBytes},
	{Operation: OperationConflictList, Method: http.MethodGet, Path: "/v1/conflicts", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: OperationConflictGet, Method: http.MethodGet, Path: "/v1/conflicts/{case_id}", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: conflict.OperationResolve, Method: http.MethodPost, Path: "/v1/conflicts/{case_id}/resolve", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxConflictResolveBodyBytes},
	{Operation: OperationProposalList, Method: http.MethodGet, Path: "/v1/proposals", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: OperationProposalGet, Method: http.MethodGet, Path: "/v1/proposals/{proposal_type}/{proposal_id}", Access: AccessRead, Idempotency: IdempotencyNotRequired},
	{Operation: OperationProposalConfirm, Method: http.MethodPost, Path: "/v1/proposals/{proposal_type}/{proposal_id}/confirm", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxProposalReviewBodyBytes},
	{Operation: OperationProposalReject, Method: http.MethodPost, Path: "/v1/proposals/{proposal_type}/{proposal_id}/reject", Access: AccessWrite, Idempotency: IdempotencyRequired, MaxBodyBytes: MaxProposalReviewBodyBytes},
}

func TestRoutesAreExactProviderAndToolSurface(t *testing.T) {
	handler, _, _ := testHandler(t)
	if got := handler.Routes(); !reflect.DeepEqual(got, targetRoutes) {
		t.Fatalf("routes = %+v", got)
	}
	for _, path := range []string{"/v1/scopes", "/v1/jobs"} {
		if response := request(t, handler, http.MethodGet, path, "", nil); response.Code != http.StatusNotFound {
			t.Errorf("%s status = %d", path, response.Code)
		}
	}
}

func TestCapabilitiesIsPublic(t *testing.T) {
	handler, _, _ := testHandler(t)
	response := request(t, handler, http.MethodGet, "/v1/capabilities", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var got toolcontract.Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, toolcontract.V1Capabilities()) {
		t.Fatalf("capabilities = %+v", got)
	}
}

func TestRecallIsReadSemanticsAndReturnsOperationID(t *testing.T) {
	handler, tools, _ := testHandler(t)
	response := request(t, handler, http.MethodPost, "/v1/recall", `{"query":"what","scope_ids":["s"],"limit":5}`, caller())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if tools.call.Idempotency != nil {
		t.Fatal("recall received idempotency metadata")
	}
	command := tools.command.(agenttool.RecallCommand)
	if command.Query != "what" || command.Limit != 5 {
		t.Fatalf("command = %+v", command)
	}
	var output toolcontract.RecallOutput
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.OperationID != "recall-op" {
		t.Fatalf("operation_id = %q", output.OperationID)
	}
	if err := toolcontract.Validate(toolcontract.SchemaRecallOutput, response.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryCommitMapsContractAndPreservesMetadataNumbers(t *testing.T) {
	handler, tools, _ := testHandler(t)
	body := `{"scope_id":"s","source":{"type":"agent_tool","name":"assistant","conversation_id":"c","turn_id":"t","occurred_at":"2026-09-20T10:00:00Z","metadata":{"n":9007199254740993}},"memories":[{"local_id":"local","kind":"fact","content":"answer","sensitivity":"normal","confidence":1}]}`
	response := request(t, handler, http.MethodPost, "/v1/memory-commits", body, writes())
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	command := tools.command.(agenttool.MemoryCommitCommand)
	if command.Source.Metadata["n"] != json.Number("9007199254740993") {
		t.Fatalf("metadata = %#v", command.Source.Metadata)
	}
	if response.Header().Get(replayHeader) != "true" {
		t.Fatal("missing replay header")
	}
	if err := toolcontract.Validate(toolcontract.SchemaMemoryCommitOutput, response.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackAndForgetUseBodyIdentities(t *testing.T) {
	handler, tools, _ := testHandler(t)
	feedback := request(t, handler, http.MethodPost, "/v1/feedback", `{"scope_id":"s","memory_id":"m","revision_id":"r","kind":"used","recall_operation_id":"recall"}`, writes())
	if feedback.Code != http.StatusCreated {
		t.Fatalf("feedback status = %d: %s", feedback.Code, feedback.Body.String())
	}
	if command := tools.command.(agenttool.FeedbackCommand); command.MemoryID != "m" || command.RecallOperationID != "recall" {
		t.Fatalf("feedback = %+v", command)
	}
	forget := request(t, handler, http.MethodPost, "/v1/forget", `{"scope_id":"s","memory_id":"m","expected_revision_id":"r","reason":"delete"}`, writes())
	if forget.Code != http.StatusOK {
		t.Fatalf("forget status = %d: %s", forget.Code, forget.Body.String())
	}
	if command := tools.command.(agenttool.ForgetCommand); command.MemoryID != "m" || command.ExpectedRevisionID != "r" {
		t.Fatalf("forget = %+v", command)
	}
}

func TestScopeEnsureResolvesThenCreatesAndScopeGet(t *testing.T) {
	handler, _, scopes := testHandler(t)
	body := `{"parent_scope_id":null,"scope_type":"project","external_id":"external","display_name":"Project"}`
	response := request(t, handler, http.MethodPost, "/v1/scopes/ensure", body, writes())
	if response.Code != http.StatusOK {
		t.Fatalf("ensure status = %d: %s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(scopes.calls, []string{"ensure"}) {
		t.Fatalf("calls = %v", scopes.calls)
	}
	response = request(t, handler, http.MethodGet, "/v1/scopes/scope", "", caller())
	if response.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", response.Code, response.Body.String())
	}
	if err := toolcontract.Validate(toolcontract.SchemaScopeOutput, response.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestEventIngestAndStatusUseProviderRouteSurface(t *testing.T) {
	events := &fakeEvents{}
	handler, err := New(Dependencies{AgentTools: &fakeTools{}, Scopes: &fakeScopes{}, Events: events, Reviews: &fakeReviews{}, Memories: &fakeMemories{}, Conflicts: &fakeConflicts{}, Proposals: &fakeProposals{}})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"scope_id":"scope","source":{"type":"manual","name":"collector","occurred_at":"2026-09-20T10:00:00Z","metadata":{"n":1}},"content":{"kind":"text","media_type":"text/plain","text":"raw body"}}`
	response := request(t, handler, http.MethodPost, "/v1/events", body, writes())
	if response.Code != http.StatusAccepted {
		t.Fatalf("ingest status = %d: %s", response.Code, response.Body.String())
	}
	command, ok := events.command.(extraction.IngestCommand)
	if !ok || command.SpaceID != "scope" || command.SourceType != "manual" || command.Content.Text != "raw body" {
		t.Fatalf("ingest command = %+v", events.command)
	}
	var output eventOutput
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.EventID != "event" || output.Status != "accepted" || len(output.JobIDs) != 1 || !output.Replayed {
		t.Fatalf("ingest output = %+v", output)
	}
	status := request(t, handler, http.MethodGet, "/v1/events/event", "", caller())
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", status.Code, status.Body.String())
	}
	var event eventStatusOutput
	if err := json.Unmarshal(status.Body.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.EventID != "event" || event.ProcessingStatus != "stored" || event.Content.Text != "body" {
		t.Fatalf("event status = %+v", event)
	}
	for name, invalid := range map[string]string{
		"unknown field": `{"scope_id":"scope","source":{"type":"manual","name":"collector"},"content":{"kind":"text","media_type":"text/plain","text":"x"},"extra":1}`,
		"bad value":     `{"scope_id":"scope","source":{"type":"manual","name":"collector"},"content":{"kind":"json","media_type":"application/json","value":}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if rejected := request(t, handler, http.MethodPost, "/v1/events", invalid, writes()); rejected.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", rejected.Code, rejected.Body.String())
			}
		})
	}
}

func TestStrictBodiesLimitsAndWriteIdempotency(t *testing.T) {
	handler, tools, _ := testHandler(t)
	valid := `{"query":"q","scope_ids":["s"]}`
	for name, body := range map[string]string{"unknown": `{"query":"q","scope_ids":["s"],"unknown":true}`, "trailing": valid + ` {}`, "non-object": `[]`} {
		t.Run(name, func(t *testing.T) {
			response := request(t, handler, http.MethodPost, "/v1/recall", body, caller())
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
		})
	}
	response := request(t, handler, http.MethodPost, "/v1/feedback", `{}`, caller())
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing idempotency status = %d", response.Code)
	}
	requestValue := httptest.NewRequest(http.MethodPost, "/v1/recall", strings.NewReader(`{"query":"`+strings.Repeat("x", int(MaxRecallBodyBytes))+`","scope_ids":["s"]}`))
	requestValue.Header.Set("Content-Type", "application/json")
	requestValue.Header.Set(callerSubjectHeader, "agent")
	requestValue.ContentLength = -1
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, requestValue)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d", response.Code)
	}
	if tools.command != nil {
		t.Fatal("gateway called for rejected requests")
	}
}

func request(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	value := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		value.Header.Set("Content-Type", "application/json")
	}
	for name, header := range headers {
		value.Header.Set(name, header)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, value)
	return response
}
func caller() map[string]string { return map[string]string{callerSubjectHeader: "agent"} }
func writes() map[string]string {
	return map[string]string{callerSubjectHeader: "agent", idempotencyHeader: "key"}
}
