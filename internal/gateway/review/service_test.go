package review

import (
	"context"
	"errors"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeStore struct {
	filter     CandidateFilter
	listPage   domain.CandidatePage
	listErr    error
	getValue   domain.Candidate
	getErr     error
	confirmMem domain.Memory
	confirmOp  string
	confirmRep bool
	confirmErr error
	rejectVal  domain.Candidate
	rejectOp   string
	rejectRep  bool
	rejectErr  error
	confirmID  gateway.WriteIdentity
	rejectID   gateway.WriteIdentity
	confirmP   ReviewParams
	rejectP    ReviewParams
}

func (f *fakeStore) ListCandidates(_ context.Context, filter CandidateFilter) (domain.CandidatePage, error) {
	f.filter = filter
	return f.listPage, f.listErr
}

func (f *fakeStore) GetCandidate(context.Context, string) (domain.Candidate, error) {
	return f.getValue, f.getErr
}

func (f *fakeStore) ConfirmCandidate(_ context.Context, identity gateway.WriteIdentity, params ReviewParams) (domain.Memory, string, bool, error) {
	f.confirmID, f.confirmP = identity, params
	return f.confirmMem, f.confirmOp, f.confirmRep, f.confirmErr
}

func (f *fakeStore) RejectCandidate(_ context.Context, identity gateway.WriteIdentity, params ReviewParams) (domain.Candidate, string, bool, error) {
	f.rejectID, f.rejectP = identity, params
	return f.rejectVal, f.rejectOp, f.rejectRep, f.rejectErr
}

func reviewCall(key string) gateway.CallContext {
	call := gateway.CallContext{RequestID: "request-1", Caller: gateway.CallerIdentity{SubjectID: "reviewer"}}
	if key != "" {
		call.Idempotency = &gateway.Idempotency{Key: key}
	}
	return call
}

func TestListCandidatesValidatesQuery(t *testing.T) {
	service := NewService(&fakeStore{listPage: domain.CandidatePage{Items: []domain.Candidate{}}})
	if _, err := service.ListCandidates(context.Background(), reviewCall(""), ListCandidatesQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing space_id error = %v", err)
	}
	if _, err := service.ListCandidates(context.Background(), reviewCall(""), ListCandidatesQuery{SpaceID: "s", ReviewState: "bogus"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("invalid review_state error = %v", err)
	}
}

func TestListCandidatesAppliesPageAndFilter(t *testing.T) {
	store := &fakeStore{listPage: domain.CandidatePage{Items: []domain.Candidate{{CandidateID: "candidate-1"}}}}
	service := NewService(store)
	query := ListCandidatesQuery{SpaceID: "space-1", ReviewState: "pending", MemoryType: "fact", Page: gateway.PageRequest{Limit: 10, Cursor: "cursor-1"}}
	page, err := service.ListCandidates(context.Background(), reviewCall(""), query)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListCandidates() = %+v error = %v", page, err)
	}
	if store.filter.SpaceID != "space-1" || store.filter.ReviewState != "pending" || store.filter.MemoryType != "fact" || store.filter.Limit != 10 || store.filter.Cursor != "cursor-1" {
		t.Fatalf("filter = %+v", store.filter)
	}
}

func TestGetCandidateValidatesIdentifier(t *testing.T) {
	service := NewService(&fakeStore{getValue: domain.Candidate{CandidateID: "candidate-1"}})
	if _, err := service.GetCandidate(context.Background(), reviewCall(""), GetCandidateQuery{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing candidate_id error = %v", err)
	}
	if _, err := service.GetCandidate(context.Background(), reviewCall(""), GetCandidateQuery{CandidateID: "candidate-1"}); err != nil {
		t.Fatalf("GetCandidate() error = %v", err)
	}
}

func TestConfirmCandidateReturnsMemoryOnlyWhenConfirmed(t *testing.T) {
	store := &fakeStore{
		confirmMem: domain.Memory{MemoryID: "memory-1"},
		confirmOp:  "op-1",
		confirmRep: true,
		getValue:   domain.Candidate{CandidateID: "candidate-1", ReviewState: "confirmed"},
	}
	service := NewService(store)
	result, err := service.ConfirmCandidate(context.Background(), reviewCall("key-1"), ReviewCandidateCommand{CandidateID: "candidate-1", ReasonCode: "reviewed"})
	if err != nil {
		t.Fatalf("ConfirmCandidate() error = %v", err)
	}
	if result.OperationID != "op-1" || !result.Replay || result.Memory == nil || result.Memory.MemoryID != "memory-1" {
		t.Fatalf("result = %+v", result)
	}
	if store.confirmID.Operation != OperationCandidateConfirm || store.confirmID.IdempotencyKey != "key-1" || store.confirmP.CandidateID != "candidate-1" {
		t.Fatalf("identity/params = %+v %+v", store.confirmID, store.confirmP)
	}
}

func TestConfirmCandidateOmitsMemoryWhenConsolidationRejects(t *testing.T) {
	store := &fakeStore{
		confirmOp: "op-1",
		getValue:  domain.Candidate{CandidateID: "candidate-1", ReviewState: "rejected"},
	}
	service := NewService(store)
	result, err := service.ConfirmCandidate(context.Background(), reviewCall("key-1"), ReviewCandidateCommand{CandidateID: "candidate-1", ReasonCode: "reviewed"})
	if err != nil || result.Memory != nil || result.Candidate.ReviewState != "rejected" {
		t.Fatalf("result = %+v error = %v", result, err)
	}
}

func TestRejectCandidateUsesRejectOperation(t *testing.T) {
	store := &fakeStore{rejectVal: domain.Candidate{CandidateID: "candidate-1", ReviewState: "rejected"}, rejectOp: "op-2"}
	service := NewService(store)
	result, err := service.RejectCandidate(context.Background(), reviewCall("key-1"), ReviewCandidateCommand{CandidateID: "candidate-1", ReasonCode: "duplicate"})
	if err != nil || result.Memory != nil || result.OperationID != "op-2" {
		t.Fatalf("result = %+v error = %v", result, err)
	}
	if store.rejectID.Operation != OperationCandidateReject {
		t.Fatalf("reject identity = %+v", store.rejectID)
	}
}

func TestReviewValidatesReasonAndNote(t *testing.T) {
	service := NewService(&fakeStore{})
	tooLong := make([]rune, MaxNoteRunes+1)
	for i := range tooLong {
		tooLong[i] = 'x'
	}
	note := string(tooLong)
	if _, err := service.ConfirmCandidate(context.Background(), reviewCall("key-1"), ReviewCandidateCommand{CandidateID: "c", ReasonCode: ""}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing reason error = %v", err)
	}
	if _, err := service.RejectCandidate(context.Background(), reviewCall("key-1"), ReviewCandidateCommand{CandidateID: "c", ReasonCode: "ok", Note: &note}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("long note error = %v", err)
	}
}

func TestReviewRequiresIdempotencyKey(t *testing.T) {
	service := NewService(&fakeStore{})
	if _, err := service.ConfirmCandidate(context.Background(), reviewCall(""), ReviewCandidateCommand{CandidateID: "c", ReasonCode: "ok"}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("missing idempotency error = %v", err)
	}
}

func TestReviewClassifiesUnknownError(t *testing.T) {
	service := NewService(&fakeStore{confirmErr: errors.New("boom")})
	if _, err := service.ConfirmCandidate(context.Background(), reviewCall("key-1"), ReviewCandidateCommand{CandidateID: "c", ReasonCode: "ok"}); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("error = %v, want internal", err)
	}
}
