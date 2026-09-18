package consolidation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeStore struct {
	confirmMemory      domain.Memory
	confirmReplay      bool
	confirmErr         error
	confirmCalls       int
	confirmID          gateway.WriteIdentity
	confirmCandidateID string
	confirmParams      ReviewParams
	rejectCandidate    domain.Candidate
	rejectReplay       bool
	rejectErr          error
	rejectCalls        int
	rejectID           gateway.WriteIdentity
	rejectCandidateID  string
	rejectParams       ReviewParams
	decision           domain.ConsolidationDecision
	decisionErr        error
	respectCancel      bool
}

func (f *fakeStore) ConfirmCandidate(ctx context.Context, identity gateway.WriteIdentity, candidateID string, params ReviewParams) (domain.Memory, bool, error) {
	f.confirmCalls++
	f.confirmID = identity
	f.confirmCandidateID = candidateID
	f.confirmParams = params
	if f.respectCancel {
		if err := ctx.Err(); err != nil {
			return domain.Memory{}, false, err
		}
	}
	return f.confirmMemory, f.confirmReplay, f.confirmErr
}

func (f *fakeStore) RejectCandidate(ctx context.Context, identity gateway.WriteIdentity, candidateID string, params ReviewParams) (domain.Candidate, bool, error) {
	f.rejectCalls++
	f.rejectID = identity
	f.rejectCandidateID = candidateID
	f.rejectParams = params
	if f.respectCancel {
		if err := ctx.Err(); err != nil {
			return domain.Candidate{}, false, err
		}
	}
	return f.rejectCandidate, f.rejectReplay, f.rejectErr
}

func (f *fakeStore) GetConsolidationDecision(context.Context, string) (domain.ConsolidationDecision, error) {
	return f.decision, f.decisionErr
}

func testCall(caller, key, requestID string) gateway.CallContext {
	call := gateway.CallContext{RequestID: requestID, Caller: gateway.CallerIdentity{SubjectID: caller}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func TestConfirmValidatesAndPropagatesIdentity(t *testing.T) {
	store := &fakeStore{confirmMemory: domain.Memory{MemoryID: "memory-1"}}
	service := NewService(store)
	note := "reviewed by operator"
	command := ConfirmCommand{CandidateID: "candidate-1", ReasonCode: "accepted", Note: &note}

	result, err := service.Confirm(context.Background(), testCall("caller-1", "key-1", "request-1"), command)
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if result.Memory.MemoryID != "memory-1" || result.Replay || result.OperationID != "request-1" {
		t.Fatalf("result = %+v", result)
	}
	if store.confirmCandidateID != "candidate-1" || store.confirmParams.ReasonCode != "accepted" || store.confirmParams.Note != &note {
		t.Fatalf("store params = %+v candidate=%q", store.confirmParams, store.confirmCandidateID)
	}
	if store.confirmID.Operation != OperationConfirm || store.confirmID.IdempotencyKey != "key-1" {
		t.Fatalf("write identity = %+v", store.confirmID)
	}
	wantHash, _ := gateway.CanonicalHash(command)
	if store.confirmID.CommandHash != wantHash {
		t.Fatalf("command hash = %q, want %q", store.confirmID.CommandHash, wantHash)
	}
}

func TestConfirmRejectsInvalidInputBeforeStore(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	longNote := strings.Repeat("n", 1025)
	longReason := strings.Repeat("r", 129)
	cases := map[string]ConfirmCommand{
		"missing candidate": {ReasonCode: "accepted"},
		"blank candidate":   {CandidateID: "  ", ReasonCode: "accepted"},
		"missing reason":    {CandidateID: "candidate-1"},
		"long reason":       {CandidateID: "candidate-1", ReasonCode: longReason},
		"long note":         {CandidateID: "candidate-1", ReasonCode: "accepted", Note: &longNote},
	}
	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Confirm(context.Background(), testCall("caller-1", "key-1", "request-1"), command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("error = %v, want invalid_input", err)
			}
		})
	}
	if store.confirmCalls != 0 {
		t.Fatalf("store called %d times for invalid input", store.confirmCalls)
	}
}

func TestConfirmRequiresCallerAndIdempotency(t *testing.T) {
	service := NewService(&fakeStore{})
	valid := ConfirmCommand{CandidateID: "candidate-1", ReasonCode: "accepted"}
	if _, err := service.Confirm(context.Background(), testCall("", "key-1", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing caller error = %v", err)
	}
	if _, err := service.Confirm(context.Background(), testCall("caller-1", "", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing idempotency error = %v", err)
	}
}

func TestConfirmClassifiesPortErrors(t *testing.T) {
	store := &fakeStore{confirmErr: gateway.NewError(gateway.ErrorConflict, "already reviewed")}
	service := NewService(store)
	valid := ConfirmCommand{CandidateID: "candidate-1", ReasonCode: "accepted"}
	if _, err := service.Confirm(context.Background(), testCall("caller-1", "key-1", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorConflict) {
		t.Fatalf("gateway error passthrough = %v", err)
	}
	store.confirmErr = errors.New("disk on fire")
	if _, err := service.Confirm(context.Background(), testCall("caller-1", "key-1", "request-1"), valid); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("unknown error classification = %v", err)
	}
}

func TestConfirmPropagatesCancellation(t *testing.T) {
	store := &fakeStore{respectCancel: true}
	service := NewService(store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Confirm(ctx, testCall("caller-1", "key-1", "request-1"), ConfirmCommand{CandidateID: "candidate-1", ReasonCode: "accepted"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRejectValidatesAndPropagatesIdentity(t *testing.T) {
	store := &fakeStore{rejectCandidate: domain.Candidate{CandidateID: "candidate-1", ReviewState: "rejected"}}
	service := NewService(store)

	if _, err := service.Reject(context.Background(), testCall("caller-1", "key-1", "request-1"), RejectCommand{CandidateID: "candidate-1"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing reason error = %v", err)
	}

	result, err := service.Reject(context.Background(), testCall("caller-1", "key-1", "request-1"), RejectCommand{CandidateID: "candidate-1", ReasonCode: "duplicate"})
	if err != nil || result.Candidate.ReviewState != "rejected" || result.Replay {
		t.Fatalf("Reject() = %+v error = %v", result, err)
	}
	if store.rejectID.Operation != OperationReject || store.rejectCandidateID != "candidate-1" || store.rejectParams.ReasonCode != "duplicate" {
		t.Fatalf("reject identity = %+v params=%+v", store.rejectID, store.rejectParams)
	}
}

func TestRejectMapsStoreErrors(t *testing.T) {
	store := &fakeStore{rejectErr: gateway.NewError(gateway.ErrorNotFound, "missing")}
	service := NewService(store)
	if _, err := service.Reject(context.Background(), testCall("caller-1", "key-1", "request-1"), RejectCommand{CandidateID: "candidate-1", ReasonCode: "duplicate"}); !gateway.IsCode(err, gateway.ErrorNotFound) {
		t.Fatalf("error = %v, want not_found", err)
	}
}

func TestDecisionValidatesAndMapsErrors(t *testing.T) {
	store := &fakeStore{decision: domain.ConsolidationDecision{CandidateID: "candidate-1", Action: domain.ConsolidationCreate}}
	service := NewService(store)
	if _, err := service.Decision(context.Background(), testCall("caller-1", "", "request-1"), "  "); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing candidate error = %v", err)
	}
	decision, err := service.Decision(context.Background(), testCall("caller-1", "", "request-1"), "candidate-1")
	if err != nil || decision.CandidateID != "candidate-1" {
		t.Fatalf("Decision() = %+v error = %v", decision, err)
	}
}
