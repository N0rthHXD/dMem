package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

const (
	capabilitiesBody = `{"contract_version":"dmem-agent-tool-v1","tools":["dmem_recall","dmem_remember","dmem_feedback","dmem_forget"],"predicate_registry_version":"predicate-registry-v2","evidence_disclosure":"references_only"}`
	recallBody       = `{"operation_id":"op","memories":[],"degraded_capabilities":["semantic"],"next_cursor":null}`
	commitBody       = `{"operation_id":"op","replayed":false,"source_event_id":"event-1","memories":[{"local_id":"m","status":"authority","memory_id":"mem-1","revision_id":"rev-1"}],"entities":[],"facts":[],"relations":[]}`
	feedbackBody     = `{"operation_id":"op","replayed":false,"feedback_id":"fb-1","memory_id":"mem-1","revision_id":"rev-1","kind":"used"}`
	forgetBody       = `{"operation_id":"op","replayed":false,"memory_id":"mem-1","revision_id":"rev-1","state":"deleted"}`
	scopeEnsureBody  = `{"operation_id":"op","replayed":false,"scope":{"scope_id":"scope-1","parent_scope_id":null,"scope_type":"project","external_id":"project-123","display_name":"Project","created_at":"2026-09-20T10:00:00Z"}}`
	scopeBody        = `{"scope":{"scope_id":"scope-1","parent_scope_id":null,"scope_type":"project","external_id":"project-123","display_name":"Project","created_at":"2026-09-20T10:00:00Z"}}`
)

type recordedRequest struct {
	method  string
	path    string
	header  http.Header
	body    []byte
	content string
}

type stubServer struct {
	mutex    sync.Mutex
	requests []recordedRequest
	attempts int
	respond  func(attempt int, w http.ResponseWriter, r *http.Request)
}

func (s *stubServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mutex.Lock()
	s.attempts++
	attempt := s.attempts
	s.requests = append(s.requests, recordedRequest{
		method: r.Method, path: r.URL.EscapedPath(), header: r.Header.Clone(),
		body: body, content: r.Header.Get("Content-Type"),
	})
	s.mutex.Unlock()
	w.Header().Set("Content-Type", "application/json")
	s.respond(attempt, w, r)
}

func (s *stubServer) recorded() []recordedRequest {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return append([]recordedRequest(nil), s.requests...)
}

func (s *stubServer) count() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.attempts
}

func newTestClient(t *testing.T, handler http.Handler, mutate ...func(*Config)) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	config := Config{
		Endpoint: server.URL,
		Caller:   Caller{SubjectID: "agent", ClientID: "test-client"},
		Retry:    RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
	}
	for _, apply := range mutate {
		apply(&config)
	}
	result, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return result
}

func writeBody(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func validCommitInput() toolcontract.MemoryCommitInput {
	return toolcontract.MemoryCommitInput{
		ScopeID: "scope-1",
		Source: toolcontract.Source{
			Type: "agent_tool", Name: "assistant", ConversationID: "conv-1", TurnID: "turn-1",
			OccurredAt: "2026-09-20T10:00:00Z",
		},
		Memories: []toolcontract.MemorySuggestion{
			{LocalID: "m", Kind: "fact", Content: "content", Sensitivity: "normal", Confidence: 0.9, SearchHints: []string{"hint"}},
		},
	}
}

func TestClientSendsContractRequestsWithIdentity(t *testing.T) {
	stub := &stubServer{respond: func(_ int, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/capabilities":
			writeBody(w, http.StatusOK, capabilitiesBody)
		case "/v1/recall":
			writeBody(w, http.StatusOK, recallBody)
		case "/v1/memory-commits":
			writeBody(w, http.StatusCreated, commitBody)
		case "/v1/feedback":
			writeBody(w, http.StatusCreated, feedbackBody)
		case "/v1/forget":
			writeBody(w, http.StatusOK, forgetBody)
		case "/v1/scopes/ensure":
			writeBody(w, http.StatusOK, scopeEnsureBody)
		default:
			writeBody(w, http.StatusOK, scopeBody)
		}
	}}
	client := newTestClient(t, stub)
	ctx := context.Background()

	if _, err := client.Capabilities(ctx, Call{}); err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	recall, err := client.Recall(ctx, Call{}, toolcontract.RecallInput{Query: "query", ScopeIDs: []string{"scope-1"}})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(recall.DegradedCapabilities) != 1 || recall.DegradedCapabilities[0] != "semantic" {
		t.Fatalf("Recall() degraded = %v", recall.DegradedCapabilities)
	}
	commit, err := client.Remember(ctx, Call{}, validCommitInput())
	if err != nil {
		t.Fatalf("Remember() error = %v", err)
	}
	if len(commit.Memories) != 1 || commit.Memories[0].Status != "authority" {
		t.Fatalf("Remember() outcomes = %+v", commit.Memories)
	}
	if _, err := client.Feedback(ctx, Call{}, toolcontract.FeedbackInput{ScopeID: "scope-1", MemoryID: "mem-1", RevisionID: "rev-1", Kind: "used"}); err != nil {
		t.Fatalf("Feedback() error = %v", err)
	}
	if _, err := client.Forget(ctx, Call{}, toolcontract.ForgetInput{ScopeID: "scope-1", MemoryID: "mem-1", ExpectedRevisionID: "rev-1", Reason: "cleanup"}); err != nil {
		t.Fatalf("Forget() error = %v", err)
	}
	if _, err := client.EnsureScope(ctx, Call{}, toolcontract.ScopeEnsureInput{ScopeType: "project", ExternalID: "project-123", DisplayName: "Project"}); err != nil {
		t.Fatalf("EnsureScope() error = %v", err)
	}
	if _, err := client.GetScope(ctx, Call{}, "scope-1"); err != nil {
		t.Fatalf("GetScope() error = %v", err)
	}

	requests := stub.recorded()
	if len(requests) != 7 {
		t.Fatalf("requests = %d, want 7", len(requests))
	}
	wantPaths := []string{"/v1/capabilities", "/v1/recall", "/v1/memory-commits", "/v1/feedback", "/v1/forget", "/v1/scopes/ensure", "/v1/scopes/scope-1"}
	for i, request := range requests {
		if request.path != wantPaths[i] {
			t.Errorf("request %d path = %q, want %q", i, request.path, wantPaths[i])
		}
		if request.header.Get("X-Caller-Subject-ID") != "agent" || request.header.Get("X-Caller-Client-ID") != "test-client" {
			t.Errorf("request %d caller headers = %v", i, request.header)
		}
		if request.header.Get("X-Request-ID") == "" {
			t.Errorf("request %d has no request id", i)
		}
	}
	for i, request := range requests {
		write := request.method == http.MethodPost && request.path != "/v1/recall"
		if got := request.header.Get("Idempotency-Key") != ""; got != write {
			t.Errorf("request %d idempotency = %v, want %v", i, got, write)
		}
		if request.method == http.MethodPost && request.content != "application/json" {
			t.Errorf("request %d content type = %q", i, request.content)
		}
	}
	var sent toolcontract.MemoryCommitInput
	if err := toolcontract.DecodeStrict(toolcontract.SchemaMemoryCommitInput, requests[2].body, &sent); err != nil {
		t.Fatalf("commit body: %v", err)
	}
	if sent.Source.Name != "assistant" || len(sent.Memories) != 1 || sent.Memories[0].SearchHints[0] != "hint" {
		t.Fatalf("commit body = %+v", sent)
	}
}

func TestClientReusesIdempotencyIdentityAcrossRetries(t *testing.T) {
	stub := &stubServer{respond: func(attempt int, w http.ResponseWriter, _ *http.Request) {
		if attempt < 3 {
			writeBody(w, http.StatusServiceUnavailable, `{"error":{"code":"capability_unavailable","message":"busy","request_id":"server-1","retryable":true}}`)
			return
		}
		writeBody(w, http.StatusCreated, commitBody)
	}}
	client := newTestClient(t, stub, func(config *Config) {
		config.Retry = RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	})
	if _, err := client.Remember(context.Background(), Call{}, validCommitInput()); err != nil {
		t.Fatalf("Remember() error = %v", err)
	}
	requests := stub.recorded()
	if len(requests) != 3 {
		t.Fatalf("attempts = %d, want 3", len(requests))
	}
	first := requests[0]
	for i, request := range requests[1:] {
		if request.header.Get("Idempotency-Key") != first.header.Get("Idempotency-Key") {
			t.Errorf("attempt %d regenerated the idempotency key", i+1)
		}
		if request.header.Get("X-Request-ID") != first.header.Get("X-Request-ID") {
			t.Errorf("attempt %d regenerated the request id", i+1)
		}
	}
}

func TestClientDoesNotRetryNonRetryableErrors(t *testing.T) {
	stub := &stubServer{respond: func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeBody(w, http.StatusUnprocessableEntity, `{"error":{"code":"invalid_input","message":"content is required","request_id":"req-9","retryable":false,"details":{"field":"content"}}}`)
	}}
	client := newTestClient(t, stub, func(config *Config) {
		config.Retry = RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	})
	_, err := client.Remember(context.Background(), Call{}, validCommitInput())
	if err == nil {
		t.Fatal("Remember() succeeded")
	}
	if stub.count() != 1 {
		t.Fatalf("attempts = %d, want 1", stub.count())
	}
	var clientErr *Error
	if !errors.As(err, &clientErr) {
		t.Fatalf("error type = %T", err)
	}
	if clientErr.Code != "invalid_input" || clientErr.Retryable || clientErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("error = %+v", clientErr)
	}
	if clientErr.RequestID != "req-9" || clientErr.Details["field"] != "content" {
		t.Fatalf("error identity lost: %+v", clientErr)
	}
	if CodeOf(err) != "invalid_input" || IsRetryable(err) {
		t.Fatalf("helpers disagree: %s/%v", CodeOf(err), IsRetryable(err))
	}
}

func TestClientPreservesRetryAfterHint(t *testing.T) {
	stub := &stubServer{respond: func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeBody(w, http.StatusTooManyRequests, `{"error":{"code":"resource_exhausted","message":"slow down","request_id":"req-2","retryable":true,"retry_after_ms":1234}}`)
	}}
	client := newTestClient(t, stub)
	_, err := client.Feedback(context.Background(), Call{}, toolcontract.FeedbackInput{ScopeID: "scope-1", MemoryID: "mem-1", RevisionID: "rev-1", Kind: "used"})
	var clientErr *Error
	if !errors.As(err, &clientErr) {
		t.Fatalf("error type = %T", err)
	}
	if clientErr.RetryAfter != 1234*time.Millisecond || !clientErr.Retryable || clientErr.Status != http.StatusTooManyRequests {
		t.Fatalf("error = %+v", clientErr)
	}
}

func TestClientValidatesInputBeforeSending(t *testing.T) {
	stub := &stubServer{respond: func(_ int, _ http.ResponseWriter, _ *http.Request) {
		t.Error("server received a request that fails the contract")
	}}
	client := newTestClient(t, stub)
	input := validCommitInput()
	input.Memories[0].Content = ""
	_, err := client.Remember(context.Background(), Call{}, input)
	if CodeOf(err) != CodeInvalidInput {
		t.Fatalf("error = %v", err)
	}
	if stub.count() != 0 {
		t.Fatalf("server attempts = %d", stub.count())
	}
}

func TestClientRejectsResponseOutsideContract(t *testing.T) {
	stub := &stubServer{respond: func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeBody(w, http.StatusOK, `{}`)
	}}
	client := newTestClient(t, stub)
	_, err := client.Recall(context.Background(), Call{}, toolcontract.RecallInput{Query: "query", ScopeIDs: []string{"scope-1"}})
	if CodeOf(err) != CodeInternal {
		t.Fatalf("error = %v", err)
	}
}

func TestClientBoundsResponseSize(t *testing.T) {
	stub := &stubServer{respond: func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeBody(w, http.StatusOK, strings.Repeat("a", maxResponseBytes+16))
	}}
	client := newTestClient(t, stub)
	_, err := client.Recall(context.Background(), Call{}, toolcontract.RecallInput{Query: "query", ScopeIDs: []string{"scope-1"}})
	if err == nil {
		t.Fatal("oversized response was accepted")
	}
	if stub.count() != 1 {
		t.Fatalf("attempts = %d, want 1", stub.count())
	}
}

func TestClientHonorsCancellationAndDeadline(t *testing.T) {
	stub := &stubServer{respond: func(_ int, _ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}}
	client := newTestClient(t, stub)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := client.Recall(ctx, Call{}, toolcontract.RecallInput{Query: "query", ScopeIDs: []string{"scope-1"}})
	if !errors.Is(err, context.Canceled) || CodeOf(err) != CodeCanceled {
		t.Fatalf("cancellation error = %v (%s)", err, CodeOf(err))
	}

	blocking := &stubServer{respond: func(_ int, _ http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}}
	timed := newTestClient(t, blocking, func(config *Config) { config.Timeout = 20 * time.Millisecond })
	_, err = timed.Recall(context.Background(), Call{}, toolcontract.RecallInput{Query: "query", ScopeIDs: []string{"scope-1"}})
	if !errors.Is(err, context.DeadlineExceeded) || CodeOf(err) != CodeDeadlineExceeded {
		t.Fatalf("deadline error = %v (%s)", err, CodeOf(err))
	}
}

func TestClientMapsTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	client, err := New(Config{
		Endpoint: url,
		Caller:   Caller{SubjectID: "agent"},
		Retry:    RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Capabilities(context.Background(), Call{})
	if CodeOf(err) != CodeCapabilityUnavailable || !IsRetryable(err) {
		t.Fatalf("error = %v (%s retryable=%v)", err, CodeOf(err), IsRetryable(err))
	}
	if strings.Contains(err.Error(), url) {
		t.Fatalf("transport error leaked the endpoint: %v", err)
	}
}

func TestClientLogsNeverContainPayloads(t *testing.T) {
	var logs bytes.Buffer
	stub := &stubServer{respond: func(_ int, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/recall" {
			writeBody(w, http.StatusOK, recallBody)
			return
		}
		writeBody(w, http.StatusCreated, commitBody)
	}}
	client := newTestClient(t, stub, func(config *Config) {
		config.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	input := validCommitInput()
	input.Memories[0].Content = "TOP-SECRET-MEMORY-CONTENT"
	if _, err := client.Remember(context.Background(), Call{}, input); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Recall(context.Background(), Call{}, toolcontract.RecallInput{Query: "TOP-SECRET-QUERY", ScopeIDs: []string{"scope-1"}}); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	if strings.Contains(output, "TOP-SECRET") {
		t.Fatalf("logs leaked content: %s", output)
	}
	if !strings.Contains(output, "/v1/recall") || !strings.Contains(output, "/v1/memory-commits") {
		t.Fatalf("logs are missing request metadata: %s", output)
	}
}

func TestClientEscapesScopePath(t *testing.T) {
	stub := &stubServer{respond: func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeBody(w, http.StatusOK, scopeBody)
	}}
	client := newTestClient(t, stub)
	if _, err := client.GetScope(context.Background(), Call{}, "a b"); err != nil {
		t.Fatal(err)
	}
	requests := stub.recorded()
	if requests[0].path != "/v1/scopes/a%20b" {
		t.Fatalf("path = %q", requests[0].path)
	}
}

func TestClientRejectsInvalidConfiguration(t *testing.T) {
	cases := map[string]Config{
		"missing endpoint": {Caller: Caller{SubjectID: "agent"}},
		"bad scheme":       {Endpoint: "ftp://host", Caller: Caller{SubjectID: "agent"}},
		"missing host":     {Endpoint: "http://", Caller: Caller{SubjectID: "agent"}},
		"endpoint query":   {Endpoint: "http://host/?x=1", Caller: Caller{SubjectID: "agent"}},
		"missing caller":   {Endpoint: "http://host"},
		"negative timeout": {Endpoint: "http://host", Caller: Caller{SubjectID: "agent"}, Timeout: -time.Second},
		"too many retries": {Endpoint: "http://host", Caller: Caller{SubjectID: "agent"}, Retry: RetryPolicy{MaxAttempts: 11}},
		"retry delay":      {Endpoint: "http://host", Caller: Caller{SubjectID: "agent"}, Retry: RetryPolicy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: time.Millisecond}},
		"http timeout":     {Endpoint: "http://host", Caller: Caller{SubjectID: "agent"}, HTTPClient: &http.Client{Timeout: time.Second}},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(config); err == nil {
				t.Fatal("New() accepted an invalid configuration")
			}
		})
	}
}

func TestToContractErrorShape(t *testing.T) {
	millis := int64(250)
	converted := ToContractError(&Error{Code: "conflict", Message: "stale revision", Retryable: true, RetryAfter: 250 * time.Millisecond, Details: map[string]string{"k": "v"}}, "req-1")
	if converted.Code != "conflict" || converted.Message != "stale revision" || !converted.Retryable || converted.Details["k"] != "v" {
		t.Fatalf("converted = %+v", converted)
	}
	if converted.RetryAfterMS == nil || *converted.RetryAfterMS != millis {
		t.Fatalf("retry_after_ms = %v", converted.RetryAfterMS)
	}
	if got := ToContractError(errors.New("boom"), "req-2"); got.Code != CodeInternal || got.RequestID != "req-2" {
		t.Fatalf("unknown error = %+v", got)
	}
	if got := ToContractError(nil, "req-3"); got.Code != CodeInternal {
		t.Fatalf("nil error = %+v", got)
	}
	if got := ToContractError(&Error{Code: "internal"}, "req-4"); got.Message == "" {
		t.Fatal("empty message was not defaulted")
	}
}
