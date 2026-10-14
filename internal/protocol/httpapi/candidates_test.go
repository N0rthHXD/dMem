package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
)

func reviewHandler(t *testing.T, reviews *fakeReviews) *Handler {
	t.Helper()
	handler, err := New(Dependencies{AgentTools: &fakeTools{}, Scopes: &fakeScopes{}, Events: &fakeEvents{}, Reviews: reviews})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestCandidateReviewRoutesUseProviderRouteSurface(t *testing.T) {
	reviews := &fakeReviews{}
	handler := reviewHandler(t, reviews)

	list := request(t, handler, http.MethodGet, "/v1/candidates?scope_id=scope&review_state=pending&limit=5", "", caller())
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", list.Code, list.Body.String())
	}
	var page candidateListOutput
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].CandidateID != "candidate-1" || len(page.Items[0].Evidence) != 0 || len(page.Items[0].Reviews) != 0 {
		t.Fatalf("list output = %+v", page)
	}
	query, ok := reviews.command.(review.ListCandidatesQuery)
	if !ok || query.SpaceID != "scope" || query.ReviewState != "pending" || query.Page.Limit != 5 {
		t.Fatalf("list query = %+v", reviews.command)
	}

	get := request(t, handler, http.MethodGet, "/v1/candidates/candidate-1", "", caller())
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", get.Code, get.Body.String())
	}
	var single candidateOutput
	if err := json.Unmarshal(get.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if single.CandidateID != "candidate-1" || single.ScopeID != "scope" || single.ReviewState != "pending" {
		t.Fatalf("get output = %+v", single)
	}

	confirm := request(t, handler, http.MethodPost, "/v1/candidates/candidate-1/confirm", `{"reason_code":"reviewed","note":"ok"}`, writes())
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm status = %d: %s", confirm.Code, confirm.Body.String())
	}
	if confirm.Header().Get(replayHeader) != "true" {
		t.Fatal("confirm did not publish the replay header")
	}
	var confirmed candidateReviewOutput
	if err := json.Unmarshal(confirm.Body.Bytes(), &confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed.OperationID != "op" || !confirmed.Replayed || confirmed.Memory == nil || confirmed.Memory.MemoryID != "memory-1" {
		t.Fatalf("confirm output = %+v", confirmed)
	}
	command, ok := reviews.command.(review.ReviewCandidateCommand)
	if !ok || command.CandidateID != "candidate-1" || command.ReasonCode != "reviewed" || command.Note == nil || *command.Note != "ok" {
		t.Fatalf("confirm command = %+v", reviews.command)
	}

	reject := request(t, handler, http.MethodPost, "/v1/candidates/candidate-1/reject", `{"reason_code":"duplicate"}`, writes())
	if reject.Code != http.StatusOK {
		t.Fatalf("reject status = %d: %s", reject.Code, reject.Body.String())
	}
	var rejected candidateReviewOutput
	if err := json.Unmarshal(reject.Body.Bytes(), &rejected); err != nil {
		t.Fatal(err)
	}
	if rejected.Memory != nil || rejected.Candidate.ReviewState != "rejected" {
		t.Fatalf("reject output = %+v", rejected)
	}
}

func TestCandidateDTOReportsAutoReviewAttribution(t *testing.T) {
	t.Parallel()
	version := "auto-review-v1"
	automatic, err := json.Marshal(candidateDTO(domain.Candidate{CandidateID: "candidate-auto", ReviewState: "confirmed", AutoReviewVersion: &version}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(automatic), `"auto_review_version":"auto-review-v1"`) {
		t.Fatalf("automatic candidate JSON = %s; want the policy version", automatic)
	}
	human, err := json.Marshal(candidateDTO(domain.Candidate{CandidateID: "candidate-human", ReviewState: "confirmed"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(human), "auto_review_version") {
		t.Fatalf("human-reviewed candidate JSON = %s; want no automatic attribution", human)
	}
}

func TestCandidateReviewRequiresIdentityAndIdempotency(t *testing.T) {
	handler := reviewHandler(t, &fakeReviews{})

	if response := request(t, handler, http.MethodGet, "/v1/candidates?scope_id=scope", "", nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("list without caller status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/candidates/candidate-1/confirm", `{"reason_code":"reviewed"}`, caller()); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("confirm without idempotency status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/candidates/candidate-1/confirm", `{"reason_code":"reviewed"}`, nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("confirm without caller status = %d: %s", response.Code, response.Body.String())
	}
}

func TestCandidateReviewStrictBodyAndMethods(t *testing.T) {
	handler := reviewHandler(t, &fakeReviews{})

	if response := request(t, handler, http.MethodPost, "/v1/candidates/candidate-1/confirm", `{"reason_code":"reviewed","extra":1}`, writes()); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/candidates", `{}`, writes()); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("collection POST status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodPost, "/v1/candidates/candidate-1", `{}`, writes()); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("item POST status = %d: %s", response.Code, response.Body.String())
	}
	if response := request(t, handler, http.MethodGet, "/v1/candidates/candidate-1", "{}", caller()); response.Code != http.StatusBadRequest {
		t.Fatalf("read body status = %d: %s", response.Code, response.Body.String())
	}
}

func TestCandidateReviewClassifiesGatewayErrors(t *testing.T) {
	reviews := &fakeReviews{}
	handler := reviewHandler(t, reviews)

	reviews.err = gateway.NewError(gateway.ErrorNotFound, "candidate not found")
	if response := request(t, handler, http.MethodGet, "/v1/candidates/missing", "", caller()); response.Code != http.StatusNotFound {
		t.Fatalf("missing candidate status = %d: %s", response.Code, response.Body.String())
	}
	reviews.err = gateway.NewError(gateway.ErrorConflict, "candidate already reviewed")
	if response := request(t, handler, http.MethodPost, "/v1/candidates/candidate-1/confirm", `{"reason_code":"reviewed"}`, writes()); response.Code != http.StatusConflict {
		t.Fatalf("reviewed candidate status = %d: %s", response.Code, response.Body.String())
	}
}
