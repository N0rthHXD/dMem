package agenttool

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeAgentStore struct {
	commitRecord CommitRecord
	commitReplay bool
	commitErr    error
	commitID     gateway.WriteIdentity
	commitParams CommitParams
	commitCalls  int

	feedbackRecord FeedbackRecord
	feedbackReplay bool
	feedbackErr    error
	feedbackID     gateway.WriteIdentity
	feedbackParams FeedbackParams
	feedbackCalls  int

	forgetRecord ForgetRecord
	forgetReplay bool
	forgetErr    error
	forgetID     gateway.WriteIdentity
	forgetParams ForgetParams
	forgetCalls  int
}

func (f *fakeAgentStore) CommitMemory(_ context.Context, identity gateway.WriteIdentity, params CommitParams) (CommitRecord, bool, error) {
	f.commitCalls++
	f.commitID, f.commitParams = identity, params
	return f.commitRecord, f.commitReplay, f.commitErr
}

func (f *fakeAgentStore) AppendFeedback(_ context.Context, identity gateway.WriteIdentity, params FeedbackParams) (FeedbackRecord, bool, error) {
	f.feedbackCalls++
	f.feedbackID, f.feedbackParams = identity, params
	return f.feedbackRecord, f.feedbackReplay, f.feedbackErr
}

func (f *fakeAgentStore) ForgetMemory(_ context.Context, identity gateway.WriteIdentity, params ForgetParams) (ForgetRecord, bool, error) {
	f.forgetCalls++
	f.forgetID, f.forgetParams = identity, params
	return f.forgetRecord, f.forgetReplay, f.forgetErr
}

type fakeRecall struct {
	record RecallRecord
	err    error
	params RecallParams
	calls  int
}

func (f *fakeRecall) Recall(_ context.Context, _ gateway.CallContext, params RecallParams) (RecallRecord, error) {
	f.calls++
	f.params = params
	return f.record, f.err
}

type fakeAuthorizer struct {
	decision AuthorizationDecision
	err      error
	request  AuthorizationRequest
	calls    int
}

func (f *fakeAuthorizer) Authorize(_ context.Context, _ gateway.CallContext, request AuthorizationRequest) (AuthorizationDecision, error) {
	f.calls++
	f.request = request
	return f.decision, f.err
}

func agentCall() gateway.CallContext {
	return gateway.CallContext{
		RequestID: "request-1", Caller: gateway.CallerIdentity{ClientID: "agent-1"},
		Idempotency: &gateway.Idempotency{Key: "tool-call-1"},
	}
}

func TestRecallDelegatesAndMapsBoundedReferences(t *testing.T) {
	evidence := make([]domain.RevisionEvidence, MaxEvidencePerMemory+1)
	for i := range evidence {
		evidence[i] = domain.RevisionEvidence{EvidenceID: string(rune('a' + i%26)), EventID: "event", Role: domain.EvidenceRoleDirect}
	}
	recall := &fakeRecall{record: RecallRecord{
		OperationID: "request-1", RequestedCapabilities: []Capability{CapabilityKeyword, CapabilitySemantic, CapabilityGraph},
		ExecutedCapabilities: []Capability{CapabilityKeyword, CapabilityGraph}, DegradationReasons: []string{"semantic_unavailable"},
		Memories: []RecallMemory{{MemoryID: "memory-1", RevisionID: "revision-1", Kind: domain.MemoryKindPreference, Content: "PostgreSQL", Confidence: .9, Evidence: evidence}},
	}}
	service := NewService(&fakeAgentStore{}, recall, &fakeAuthorizer{})
	command := RecallCommand{Query: "database", ScopeIDs: []string{"scope-1"}}
	result, err := service.Recall(context.Background(), agentCall(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationID != "request-1" || !reflect.DeepEqual(result.DegradedCapabilities, []Capability{CapabilitySemantic}) {
		t.Fatalf("recall result = %+v", result)
	}
	if len(result.Memories) != 1 || len(result.Memories[0].Evidence) != MaxEvidencePerMemory {
		t.Fatalf("memories = %+v", result.Memories)
	}
	if recall.params.OperationID != "request-1" || recall.params.Intent != domain.RecallIntentAuto || recall.params.Limit != DefaultRecallLimit || !recall.params.IncludeEvidence {
		t.Fatalf("recall params = %+v", recall.params)
	}
}

func TestRecallGeneratesStableOperationID(t *testing.T) {
	recall := &fakeRecall{}
	service := NewService(&fakeAgentStore{}, recall, &fakeAuthorizer{})
	call := agentCall()
	call.RequestID = ""
	command := RecallCommand{Query: "database", ScopeIDs: []string{"scope-1"}}
	first, err := service.Recall(context.Background(), call, command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Recall(context.Background(), call, command)
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID == "" || first.OperationID != second.OperationID || !strings.HasPrefix(first.OperationID, string(OperationRecall)+":") {
		t.Fatalf("operation IDs = %q, %q", first.OperationID, second.OperationID)
	}
}

func TestRecallValidatesAndMapsErrors(t *testing.T) {
	recall := &fakeRecall{err: errors.New("boom")}
	service := NewService(&fakeAgentStore{}, recall, &fakeAuthorizer{})
	if _, err := service.Recall(context.Background(), agentCall(), RecallCommand{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) || recall.calls != 0 {
		t.Fatalf("validation error = %v calls=%d", err, recall.calls)
	}
	if _, err := service.Recall(context.Background(), agentCall(), RecallCommand{Query: "query", ScopeIDs: []string{"scope"}}); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("adapter error = %v", err)
	}
}

func TestMemoryCommitAuthorizesAndUsesCanonicalIdentity(t *testing.T) {
	store := &fakeAgentStore{commitRecord: CommitRecord{OperationID: "operation-1", SourceEventID: "event-1", Memories: []CommitOutcome{{LocalID: "memory-1", Status: CommitAuthority, MemoryID: "memory-id", RevisionID: "revision-id"}}}}
	authorizer := &fakeAuthorizer{decision: AuthorizationDecision{Allowed: true}}
	service := NewService(store, &fakeRecall{}, authorizer)
	result, err := service.MemoryCommit(context.Background(), agentCall(), validCommit())
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationID != "operation-1" || result.SourceEventID != "event-1" || store.commitCalls != 1 || store.commitID.Operation != OperationMemoryCommit {
		t.Fatalf("result/store = %+v %+v", result, store)
	}
	want, _ := CanonicalMemoryCommitHash(validCommit())
	if store.commitID.CommandHash != want || store.commitParams.ScopeID != "scope-1" {
		t.Fatalf("identity/params = %+v %+v", store.commitID, store.commitParams)
	}
	if authorizer.request.Capability != CapabilityAuthorityWrite || !reflect.DeepEqual(authorizer.request.ScopeIDs, []string{"scope-1"}) {
		t.Fatalf("authorization = %+v", authorizer.request)
	}
}

func TestWritesRequireAuthorizationBeforeStorage(t *testing.T) {
	store := &fakeAgentStore{}
	authorizer := &fakeAuthorizer{decision: AuthorizationDecision{Allowed: false}}
	service := NewService(store, &fakeRecall{}, authorizer)
	feedback := FeedbackCommand{ScopeID: "scope", MemoryID: "memory", RevisionID: "revision", Kind: FeedbackUsed}
	if _, err := service.Feedback(context.Background(), agentCall(), feedback); !gateway.IsCode(err, gateway.ErrorPermissionDenied) {
		t.Fatalf("feedback error = %v", err)
	}
	forget := ForgetCommand{ScopeID: "scope", MemoryID: "memory", ExpectedRevisionID: "revision", Reason: "requested"}
	if _, err := service.Forget(context.Background(), agentCall(), forget); !gateway.IsCode(err, gateway.ErrorPermissionDenied) {
		t.Fatalf("forget error = %v", err)
	}
	if store.feedbackCalls != 0 || store.forgetCalls != 0 {
		t.Fatalf("storage calls feedback=%d forget=%d", store.feedbackCalls, store.forgetCalls)
	}
}

func TestFeedbackAndForgetMapStoreResults(t *testing.T) {
	store := &fakeAgentStore{
		feedbackRecord: FeedbackRecord{OperationID: "operation-feedback", FeedbackID: "feedback-1", MemoryID: "memory", RevisionID: "revision", Kind: FeedbackUsed}, feedbackReplay: true,
		forgetRecord: ForgetRecord{OperationID: "operation-forget", MemoryID: "memory", RevisionID: "revision", State: "deleted"},
	}
	service := NewService(store, &fakeRecall{}, &fakeAuthorizer{decision: AuthorizationDecision{Allowed: true}})
	feedback, err := service.Feedback(context.Background(), agentCall(), FeedbackCommand{ScopeID: "scope", MemoryID: "memory", RevisionID: "revision", Kind: FeedbackUsed, RecallOperationID: "recall-1"})
	if err != nil || feedback.OperationID != "operation-feedback" || feedback.FeedbackID != "feedback-1" || !feedback.Replay || store.feedbackParams.RecallOperationID != "recall-1" {
		t.Fatalf("Feedback() = %+v error=%v params=%+v", feedback, err, store.feedbackParams)
	}
	forgotten, err := service.Forget(context.Background(), agentCall(), ForgetCommand{ScopeID: "scope", MemoryID: "memory", ExpectedRevisionID: "revision", Reason: "requested"})
	if err != nil || forgotten.OperationID != "operation-forget" || forgotten.State != "deleted" || store.forgetParams.ExpectedRevisionID != "revision" {
		t.Fatalf("Forget() = %+v error=%v params=%+v", forgotten, err, store.forgetParams)
	}
}

func TestWriteValidationPrecedesAuthorization(t *testing.T) {
	authorizer := &fakeAuthorizer{decision: AuthorizationDecision{Allowed: true}}
	store := &fakeAgentStore{}
	service := NewService(store, &fakeRecall{}, authorizer)
	command := validCommit()
	command.Source.OccurredAt = time.Time{}
	if _, err := service.MemoryCommit(context.Background(), agentCall(), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("error = %v", err)
	}
	if authorizer.calls != 0 || store.commitCalls != 0 {
		t.Fatalf("calls authorizer=%d store=%d", authorizer.calls, store.commitCalls)
	}
}
