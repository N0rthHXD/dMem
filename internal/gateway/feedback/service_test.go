package feedback

import (
	"context"
	"errors"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeStore struct {
	response    domain.FeedbackAppendResponse
	replay      bool
	appendErr   error
	appendID    gateway.WriteIdentity
	appendParam AppendParams
	appendCalls int

	health    domain.FeedbackAggregateHealth
	healthErr error
}

func (f *fakeStore) AppendFeedback(_ context.Context, identity gateway.WriteIdentity, params AppendParams) (domain.FeedbackAppendResponse, bool, error) {
	f.appendCalls++
	f.appendID = identity
	f.appendParam = params
	return f.response, f.replay, f.appendErr
}

func (f *fakeStore) FeedbackAggregateHealth(context.Context) (domain.FeedbackAggregateHealth, error) {
	return f.health, f.healthErr
}

func feedbackCall(caller, key, requestID string) gateway.CallContext {
	call := gateway.CallContext{RequestID: requestID, Caller: gateway.CallerIdentity{SubjectID: caller}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func validDecision() *domain.FeedbackDecisionContext {
	return &domain.FeedbackDecisionContext{
		SearchOperationID: "op-1",
		QueryHash:         string(make([]byte, 64)),
		QueryType:         "keyword",
		RankingIdentity:   "ranking-v1",
		FeatureIdentity:   "features-v1",
		ContextIdentity:   "context-v1",
		ResultRank:        1,
		SelectionState:    "selected",
		MatchedSignals:    []string{"lexical"},
	}
}

func TestFeedbackAppendValidatesCombination(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)

	cases := map[string]AppendCommand{
		"bad combination":     {SpaceID: "space-1", MemoryID: "memory-1", Purpose: domain.FeedbackPurposeMemoryQuality, FeedbackType: "useful", ReasonCode: "answered_query"},
		"long note":           {SpaceID: "space-1", MemoryID: "memory-1", Purpose: domain.FeedbackPurposeMemoryQuality, FeedbackType: "confirm", ReasonCode: "other", Note: stringPointer(string(make([]rune, 2001)))},
		"missing decision":    {SpaceID: "space-1", MemoryID: "memory-1", Purpose: domain.FeedbackPurposeRetrieval, FeedbackType: "useful", ReasonCode: "answered_query"},
		"decision on quality": {SpaceID: "space-1", MemoryID: "memory-1", Purpose: domain.FeedbackPurposeMemoryQuality, FeedbackType: "confirm", ReasonCode: "other", Decision: validDecision()},
		"infeasible proposal": {SpaceID: "space-1", MemoryID: "memory-1", Purpose: domain.FeedbackPurposeMemoryQuality, FeedbackType: "confirm", ReasonCode: "other", CreateProposal: true},
	}
	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Append(context.Background(), feedbackCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("error = %v, want invalid_input", err)
			}
		})
	}
	if store.appendCalls != 0 {
		t.Fatalf("store called %d times for invalid input", store.appendCalls)
	}
}

func TestFeedbackAppendPropagatesIdentityAndReplay(t *testing.T) {
	store := &fakeStore{response: domain.FeedbackAppendResponse{Feedback: domain.FeedbackEvent{FeedbackID: "feedback-1"}}, replay: true}
	service := NewService(store)
	command := AppendCommand{SpaceID: "space-1", MemoryID: "memory-1", Purpose: domain.FeedbackPurposeMemoryQuality, FeedbackType: "confirm", ReasonCode: "other"}

	result, err := service.Append(context.Background(), feedbackCall("caller-1", "key-1", "request-1"), command)
	if err != nil || !result.Replay || result.Response.Feedback.FeedbackID != "feedback-1" {
		t.Fatalf("Append() = %+v error = %v", result, err)
	}
	if store.appendID.Operation != OperationAppend || store.appendID.IdempotencyKey != "key-1" {
		t.Fatalf("write identity = %+v", store.appendID)
	}
	wantHash, _ := gateway.CanonicalHash(command)
	if store.appendID.CommandHash != wantHash {
		t.Fatalf("command hash = %q, want %q", store.appendID.CommandHash, wantHash)
	}
}

func TestFeedbackAppendAcceptsRetrievalDecision(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	command := AppendCommand{SpaceID: "space-1", MemoryID: "memory-1", Purpose: domain.FeedbackPurposeRetrieval, FeedbackType: "useful", ReasonCode: "answered_query", Decision: validDecision()}
	if _, err := service.Append(context.Background(), feedbackCall("caller-1", "key-1", "request-1"), command); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if store.appendParam.Decision == nil {
		t.Fatal("decision context was not forwarded")
	}
}

func TestFeedbackAppendClassifiesPortError(t *testing.T) {
	service := NewService(&fakeStore{appendErr: gateway.NewError(gateway.ErrorNotFound, "no target")})
	command := AppendCommand{SpaceID: "space-1", MemoryID: "memory-1", Purpose: domain.FeedbackPurposeMemoryQuality, FeedbackType: "confirm", ReasonCode: "other"}
	if _, err := service.Append(context.Background(), feedbackCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("error = %v, want not_found", err)
	}

	service = NewService(&fakeStore{appendErr: errors.New("boom")})
	if _, err := service.Append(context.Background(), feedbackCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("error = %v, want internal", err)
	}
}

func TestFeedbackAggregateReturnsHealth(t *testing.T) {
	service := NewService(&fakeStore{health: domain.FeedbackAggregateHealth{AlgorithmVersion: "v2"}})
	result, err := service.Aggregate(context.Background(), feedbackCall("caller-1", "", "request-1"), AggregateQuery{})
	if err != nil || result.Health.AlgorithmVersion != "v2" {
		t.Fatalf("Aggregate() = %+v error = %v", result, err)
	}
}

func stringPointer(value string) *string { return &value }
