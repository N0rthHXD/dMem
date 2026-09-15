package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func TestDisabledDoesNotResolveSecretOrUseHTTP(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "do-not-read")
	transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("disabled provider made an HTTP request")
		return nil, nil
	})
	client, err := New(Config{Enabled: false, Secret: SecretReference{Source: "environment", Name: "DMEM_TEST_SECRET"}}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Process(context.Background(), ProcessRequest{})
	assertProviderError(t, err, ErrorDisabled, "provider_disabled")
}

func wholeEventStructuredCandidate() string {
	return strings.Replace(validStructuredCandidate, `"role":"direct","start_offset":0,"end_offset":16`, `"role":"direct","start_offset":null,"end_offset":null`, 1)
}

func TestProcessMinimizesContentAndUsesExplicitSecret(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "test-secret-value")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret-value" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			Model    string        `json:"model"`
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "model-a" || len(body.Messages) != 2 {
			t.Errorf("request = %+v", body)
		}
		user := body.Messages[1].Content
		if strings.Contains(user, "https://private.example/file") || strings.Contains(user, "metadata-secret") {
			t.Errorf("request leaked excluded data: %s", user)
		}
		if !strings.Contains(user, `"declared_hash":"sha256:abc"`) || strings.Contains(user, "context-over-limit") {
			t.Errorf("request did not apply minimization: %s", user)
		}
		w.Header().Set("X-Request-Id", "request-123")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + strconv.Quote(structuredCandidatesOutput(wholeEventStructuredCandidate())) + `}}],"usage":{"prompt_tokens":12,"completion_tokens":5}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions", Limits: Limits{MaxContextItems: 1}})
	target := NewEvent("event-1", "space-a", "file_reference", "source", "normal", time.Unix(1, 0), NewReferenceContent("text/plain", 42, "sha256:abc"))
	contextEvents := []Event{
		NewEvent("context-a", "space-a", "message", "source", "normal", time.Unix(2, 0), NewTextContent("text/plain", "allowed-context")),
		NewEvent("context-b", "space-a", "message", "source", "normal", time.Unix(3, 0), NewTextContent("text/plain", "context-over-limit")),
	}
	result, err := client.Process(context.Background(), NewProcessRequest("processor-a", "v2", "job-a", 1, target, contextEvents, nil, []domain.MemoryKind{domain.MemoryKindFact}, 2, 1000))
	if err != nil {
		t.Fatal(err)
	}
	revisions := result.Revisions()
	if result.Category() != ProcessCandidatesExtracted || len(revisions) != 1 || result.ProviderRequestID() != "request-123" {
		t.Fatalf("result = %+v", result)
	}
	revision := revisions[0]
	if revision.Kind != domain.MemoryKindFact || revision.CanonicalContent != "alice prefers tea" || len(revision.Evidence) != 1 || revision.Evidence[0].Role != domain.EvidenceRoleDirect || revision.Evidence[0].EventID != "event-1" {
		t.Fatalf("revision = %+v", revision)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestProcessPreservesContextBudgetAndStructuredPrompt(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var prompt structuredProcessPrompt
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &prompt); err != nil {
			t.Fatal(err)
		}
		if prompt.MaxContentChars != 80 || len(prompt.ContextMemories) != 1 || len(prompt.AllowedKinds) != 1 || prompt.AllowedKinds[0] != "fact" {
			t.Fatalf("provider prompt = %+v", prompt)
		}
		memory := prompt.ContextMemories[0]
		if memory.MemoryID != "memory-a" || memory.RevisionID != "revision-a" || memory.Content != "bounded memory" || memory.State != "contested" || memory.Sensitivity != "normal" {
			t.Fatalf("context memory = %+v", memory)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"no_memory\",\"candidates\":[]}"}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
	target := NewEvent("event-1", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "source text"))
	memory := NewMemory("memory-a", "revision-a", "space-a", "bounded memory", "contested", "normal", nil, nil)
	result, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, nil, []Memory{memory}, []domain.MemoryKind{domain.MemoryKindFact}, 1, 80))
	if err != nil {
		t.Fatal(err)
	}
	if result.Category() != ProcessNoMemory || len(result.Revisions()) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestProcessRejectsStructuredEvidenceOutsideOffsets(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	outOfRange := strings.Replace(validStructuredCandidate, `"end_offset":16`, `"end_offset":4096`, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + strconv.Quote(structuredCandidatesOutput(outOfRange)) + `}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
	target := NewEvent("event-1", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "source text"))
	_, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, nil, nil, []domain.MemoryKind{domain.MemoryKindFact}, 1, 1000))
	assertProviderError(t, err, ErrorInvalidOutput, "invalid_evidence_reference")
}

func TestProcessUsesOneContextBudgetAndMarksReadOnlyMemories(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var prompt structuredProcessPrompt
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &prompt); err != nil {
			t.Fatal(err)
		}
		if len(prompt.ContextEvents) != 1 || len(prompt.ContextMemories) != 1 || prompt.ContextMemories[0].ProposalCapable {
			t.Fatalf("context budget/proposal capability = %+v", prompt)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"no_memory\",\"candidates\":[]}"}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions", Limits: Limits{MaxContextItems: 2}})
	target := NewEvent("target", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "target"))
	events := []Event{NewEvent("context", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "event context"))}
	memories := []Memory{
		NewMemoryWithProposalCapability("memory-a", "revision-a", "ancestor", "read only", "active", "normal", false, nil, nil),
		NewMemory("memory-b", "revision-b", "space-a", "over limit", "active", "normal", nil, nil),
	}
	result, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, events, memories, []domain.MemoryKind{domain.MemoryKindFact}, 1, 100))
	if err != nil {
		t.Fatal(err)
	}
	if result.OmissionSummary()["context_limit"] != 1 {
		t.Fatalf("omission summary = %#v", result.OmissionSummary())
	}
}

func TestProcessRejectsEvidenceFromOmittedContext(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	denied := strings.Replace(wholeEventStructuredCandidate(), `"event_id":"event-1"`, `"event_id":"denied"`, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + strconv.Quote(structuredCandidatesOutput(denied)) + `}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
	target := NewEvent("target", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "target"))
	deniedEvent := NewEvent("denied", "space-a", "message", "source", "sensitive", time.Now(), NewTextContent("text/plain", "private"))
	_, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, []Event{deniedEvent}, nil, []domain.MemoryKind{domain.MemoryKindFact}, 1, 1000))
	assertProviderError(t, err, ErrorInvalidOutput, "invalid_evidence_reference")
}

func TestPolicyRejectsBeforeSecretAndNetwork(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	for _, test := range []struct {
		name   string
		target Event
		code   string
	}{
		{"content", NewEvent("event-a", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/html", "private-content")), "content_type_not_allowed"},
		{"source", NewEvent("event-a", "space-a", "tool_result", "source", "normal", time.Now(), NewTextContent("text/plain", "private-content")), "source_type_not_allowed"},
		{"sensitivity", NewEvent("event-a", "space-a", "message", "source", "sensitive", time.Now(), NewTextContent("text/plain", "private-content")), "sensitivity_not_allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.Process(context.Background(), NewProcessRequest("p", "v", "j", 1, test.target, nil, nil, []domain.MemoryKind{domain.MemoryKindFact}, 1, 1000))
			assertProviderError(t, err, ErrorPolicy, test.code)
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestEmbeddingPolicyRejectsBeforeSecretAndNetwork(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	for _, test := range []struct {
		name        string
		sensitivity string
		sourceTypes []string
		code        string
	}{
		{"sensitivity", "sensitive", []string{"manual"}, "sensitivity_not_allowed"},
		{"source", "normal", []string{"tool_result"}, "source_type_not_allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.Embed(context.Background(), NewEmbeddingRequest("space-a", "text/plain", test.sensitivity, test.sourceTypes, []string{"private content"}))
			assertProviderError(t, err, ErrorPolicy, test.code)
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestEmbeddingRequestIsImmutable(t *testing.T) {
	sources := []string{"manual"}
	inputs := []string{"content"}
	request := NewEmbeddingRequest("space-a", "text/plain", "normal", sources, inputs)
	sources[0], inputs[0] = "tool_result", "changed"
	returnedSources, returnedInputs := request.SourceTypes(), request.Inputs()
	returnedSources[0], returnedInputs[0] = "message", "changed again"
	if got := request.SourceTypes(); len(got) != 1 || got[0] != "manual" {
		t.Fatalf("source types = %v", got)
	}
	if got := request.Inputs(); len(got) != 1 || got[0] != "content" {
		t.Fatalf("inputs = %v", got)
	}
}

func TestEmbeddingAllowedClassificationUsesHTTP(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,2]}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	result, err := client.Embed(context.Background(), NewEmbeddingRequest("space-a", "text/plain", "normal", []string{"manual"}, []string{"content"}))
	if err != nil || len(result.Vectors()) != 1 || requests.Load() != 1 {
		t.Fatalf("Embed() = %+v, requests=%d, error=%v", result.Vectors(), requests.Load(), err)
	}
}

func TestEmbeddingUsesFullURLAndOptionalConfiguration(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if r.URL.Path != "/custom/embed" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["dimensions"] != float64(2) {
			t.Errorf("dimensions = %#v", body["dimensions"])
		}
		inputs, ok := body["input"].([]any)
		expected := "stored memory"
		if requestNumber == 1 {
			expected = "query: deployment region"
		}
		if !ok || len(inputs) != 1 || inputs[0] != expected {
			t.Errorf("input = %#v", body["input"])
		}
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,2]}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{
		Endpoint: server.URL + "/custom/embed", Options: EmbeddingOptions{Dimensions: 2, QueryInstructions: "query: "},
	})
	if _, err := client.Embed(context.Background(), NewEmbeddingQueryRequest("space-a", "text/plain", "normal", []string{"manual"}, []string{"deployment region"})); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(context.Background(), NewEmbeddingRequest("space-a", "text/plain", "normal", []string{"manual"}, []string{"stored memory"})); err != nil {
		t.Fatal(err)
	}
}

func TestMultimodalEmbeddingUsesTextInputAndObjectResponse(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model      string                     `json:"model"`
			Input      []multimodalEmbeddingInput `json:"input"`
			Dimensions int                        `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "model-a" || body.Dimensions != 2 || len(body.Input) != 1 || body.Input[0].Type != "text" || body.Input[0].Text != "query: deployment region" {
			t.Errorf("request = %+v", body)
		}
		_, _ = w.Write([]byte(`{"data":{"embedding":[1,2]},"usage":{"prompt_tokens":4}}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{
		Endpoint: server.URL + "/api/v3/embeddings/multimodal", Options: EmbeddingOptions{Dimensions: 2, QueryInstructions: "query: "},
	})
	result, err := client.Embed(context.Background(), NewEmbeddingQueryRequest("space-a", "text/plain", "normal", []string{"manual"}, []string{"deployment region"}))
	if err != nil {
		t.Fatal(err)
	}
	if vectors := result.Vectors(); len(vectors) != 1 || len(vectors[0]) != 2 || vectors[0][0] != 1 || result.Usage().InputTokens() != 4 {
		t.Fatalf("result = %+v", result)
	}
}

func TestMultimodalEmbeddingRejectsBatch(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/api/v3/embeddings/multimodal"})
	_, err := client.Embed(context.Background(), NewEmbeddingRequest("space-a", "text/plain", "normal", []string{"manual"}, []string{"first", "second"}))
	assertProviderError(t, err, ErrorInvalidInput, "multimodal_embedding_requires_single_input")
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestEmbeddingOmitsEmptyOptionalConfiguration(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["dimensions"]; ok {
			t.Errorf("request includes empty dimensions: %#v", body)
		}
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,2]}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	if _, err := client.Embed(context.Background(), NewEmbeddingQueryRequest("space-a", "text/plain", "normal", []string{"manual"}, []string{"query"})); err != nil {
		t.Fatal(err)
	}
}

func TestProcessEnforcesAggregateTargetAndContextCharacterBudget(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/chat/completions" || r.URL.RawQuery != "" {
			t.Errorf("request target = %q", r.URL.String())
		}
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var prompt structuredProcessPrompt
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &prompt); err != nil {
			t.Fatal(err)
		}
		if len(prompt.ContextEvents) != 1 || prompt.Event.Content.Text != "123456" || prompt.ContextEvents[0].Content.Text != "7890" {
			t.Errorf("egress prompt = %+v", prompt)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"no_memory\",\"candidates\":[]}"}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions", Limits: Limits{MaxEgressInputChars: 10}})
	target := NewEvent("event-a", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "123456"))
	contextEvents := []Event{
		NewEvent("context-a", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "7890")),
		NewEvent("context-b", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "excluded")),
	}
	if _, err := client.Process(context.Background(), NewProcessRequest("p", "v", "j", 1, target, contextEvents, nil, []domain.MemoryKind{domain.MemoryKindFact}, 1, 10)); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestRejectsRedirectWithoutFollowing(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	var redirected atomic.Int32
	destination := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
	_, err := client.Embed(context.Background(), NewEmbeddingRequest("space-a", "text/plain", "normal", []string{"manual"}, []string{"content"}))
	assertProviderError(t, err, ErrorPermanent, "redirect_rejected")
	if redirected.Load() != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestEmbeddingValidatesMalformedResponses(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	tests := []struct {
		name string
		body string
		code string
	}{
		{"count", `{"data":[]}`, "embedding_count_mismatch"},
		{"duplicate index", `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`, "malformed_embedding_response"},
		{"dimension", `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[1,2]}]}`, "embedding_dimension_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			client := newTestClient(t, server, Config{})
			inputs := []string{"a"}
			if test.name != "count" {
				inputs = []string{"a", "b"}
			}
			_, err := client.Embed(context.Background(), NewEmbeddingRequest("space-a", "text/plain", "normal", []string{"manual"}, inputs))
			assertProviderError(t, err, ErrorInvalidOutput, test.code)
		})
	}
}

func TestProcessRejectsUnrecognizedOutputFields(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"no_memory\",\"candidates\":[],\"delete\":true}"}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	target := NewEvent("event-a", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "content"))
	_, err := client.Process(context.Background(), NewProcessRequest("p", "v", "j", 1, target, nil, nil, []domain.MemoryKind{domain.MemoryKindFact}, 1, 1000))
	assertProviderError(t, err, ErrorInvalidOutput, "malformed_processor_output")
}

func TestErrorsDoNotExposeResponseContentOrSecret(t *testing.T) {
	secret := "highly-sensitive-secret"
	raw := "private provider response body"
	t.Setenv("DMEM_TEST_SECRET", secret)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(raw))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	_, err := client.Embed(context.Background(), NewEmbeddingRequest("space-a", "text/plain", "normal", []string{"manual"}, []string{"private input"}))
	assertProviderError(t, err, ErrorPermanent, "provider_request_rejected")
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "private input") {
		t.Fatalf("unsafe error = %q", err)
	}
}

func TestDeadlineCancelsRequest(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Limits: Limits{RequestTimeout: 20 * time.Millisecond}})
	_, err := client.Embed(context.Background(), NewEmbeddingRequest("space-a", "text/plain", "normal", []string{"manual"}, []string{"content"}))
	close(release)
	assertProviderError(t, err, ErrorTransient, "request_timeout_or_cancelled")
}

func TestProcessOmitsDeniedOptionalContext(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body.Messages[1].Content, "denied context") {
			t.Fatal("denied context reached provider")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"no_memory\",\"candidates\":[]}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{})
	target := NewEvent("target", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "allowed target"))
	denied := NewEvent("context", "space-a", "message", "source", "sensitive", time.Now(), NewTextContent("text/plain", "denied context"))
	result, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, []Event{denied}, nil, []domain.MemoryKind{domain.MemoryKindFact}, 1, 100, 100))
	if err != nil {
		t.Fatal(err)
	}
	if result.OmittedContextCount() != 1 || result.OmissionSummary()["context_sensitivity_not_allowed"] != 1 {
		t.Fatalf("omissions = %d %#v", result.OmittedContextCount(), result.OmissionSummary())
	}
}

func TestProcessNormalizesMIMEPolicyKeys(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"no_memory\",\"candidates\":[]}"}}],"usage":{}}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Policy: Policy{ContentTypes: []string{" Text/Plain "}}})
	target := NewEvent("target", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "content"))
	if _, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, nil, nil, []domain.MemoryKind{domain.MemoryKindFact}, 1, 100)); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateUsesIndependentInputLimit(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"content\":\"summary\",\"sensitivity\":\"normal\"}"}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions", Limits: Limits{MaxAggregateInputChars: 20}})
	_, err := client.Build(context.Background(), domain.AggregateProviderInput{Kind: "scene", SpaceID: "space-a", PolicyVersion: "policy", ProcessingVersion: "v1", Inputs: []domain.AggregateInput{{SpaceID: "space-a", Content: "12345678901", Sensitivity: "normal"}, {SpaceID: "space-a", Content: "1234567890", Sensitivity: "normal"}}})
	assertProviderError(t, err, ErrorInvalidInput, "content_out_of_bounds")
}

func TestAggregateSupportsTimelineAndRejectsUnknownKind(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"content\":\"timeline summary\",\"sensitivity\":\"normal\"}"}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
	inputs := []domain.AggregateInput{{SpaceID: "space-a", Content: "first event", Sensitivity: "normal"}, {SpaceID: "space-a", Content: "second event", Sensitivity: "normal"}}
	result, err := client.Build(context.Background(), domain.AggregateProviderInput{Kind: "timeline", SpaceID: "space-a", PolicyVersion: "policy", ProcessingVersion: "v1", Inputs: inputs})
	if err != nil || result.Content != "timeline summary" {
		t.Fatalf("timeline build = %+v err=%v", result, err)
	}
	_, err = client.Build(context.Background(), domain.AggregateProviderInput{Kind: "summary", SpaceID: "space-a", PolicyVersion: "policy", ProcessingVersion: "v1", Inputs: inputs})
	assertProviderError(t, err, ErrorInvalidInput, "invalid_aggregate_request")
}

func newTestClient(t *testing.T, server *httptest.Server, override Config) *Client {
	t.Helper()
	cfg := Config{
		Enabled: true, Kind: KindOpenAICompatible, Endpoint: server.URL + "/embeddings", Model: "model-a",
		Secret: SecretReference{Source: "environment", Name: "DMEM_TEST_SECRET"}, TelemetryDisabled: true,
		Limits: Limits{
			MaxEgressInputChars: 1000, MaxAggregateInputChars: 1000, MaxContextItems: 2, MaxCandidateChars: 1000, MaxTotalCandidateChars: 1000, MaxResponseBytes: 64 * 1024,
			RequestTimeout: time.Second, MaxConcurrent: 2, RequestsPerMinute: 60000,
		},
		Policy: Policy{
			Default:       "deny",
			ContentTypes:  []string{"text/plain", "application/json"},
			SourceTypes:   []string{"message", "file_reference", "manual"},
			Sensitivities: []string{"normal"},
			Assignments: []PolicyAssignment{
				{Selector: "space", SpaceID: "space-a", Effect: "allow"},
			},
		},
	}
	if override.Limits.MaxContextItems != 0 {
		cfg.Limits.MaxContextItems = override.Limits.MaxContextItems
	}
	if override.Limits.MaxEgressInputChars != 0 {
		cfg.Limits.MaxEgressInputChars = override.Limits.MaxEgressInputChars
	}
	if override.Limits.MaxAggregateInputChars != 0 {
		cfg.Limits.MaxAggregateInputChars = override.Limits.MaxAggregateInputChars
	}
	if override.Limits.RequestTimeout != 0 {
		cfg.Limits.RequestTimeout = override.Limits.RequestTimeout
	}
	if override.Endpoint != "" {
		cfg.Endpoint = override.Endpoint
	}
	if override.Options.Dimensions != 0 {
		cfg.Options.Dimensions = override.Options.Dimensions
	}
	if override.Options.QueryInstructions != "" {
		cfg.Options.QueryInstructions = override.Options.QueryInstructions
	}
	if len(override.Policy.ContentTypes) > 0 {
		cfg.Policy.ContentTypes = override.Policy.ContentTypes
	}
	client, err := New(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertProviderError(t *testing.T, err error, class ErrorClass, code string) {
	t.Helper()
	var providerError *Error
	if !errors.As(err, &providerError) || providerError.Class() != class || providerError.Code() != code {
		t.Fatalf("error = %#v, want class=%q code=%q", err, class, code)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

const validStructuredCandidate = `{"kind":"fact","display_content":"Alice prefers tea","canonical_content":"alice prefers tea","subject_entity_ids":["alice"],"object_entity_ids":[],"event_type":"","attributes":{"preference":"tea"},"event_time":{"expression":"yesterday","start":"2024-03-01T09:00:00Z","end":"2024-03-01T10:00:00Z","precision":"exact","source":"relative_anchor"},"valid_time":{"expression":"","start":"2024-01-01T00:00:00Z","end":"2024-12-31T00:00:00Z","precision":"day","source":"explicit"},"source_speaker":"alice","language":"en","sensitivity":"normal","specificity":0.7,"confidence":0.9,"evidence":[{"evidence_id":"ev-1","event_id":"event-1","event_time":"2024-03-01T09:00:00Z","event_sequence":1,"role":"direct","start_offset":0,"end_offset":16}]}`

func structuredCandidatesOutput(candidate string) string {
	return `{"category":"candidates_extracted","candidates":[` + candidate + `]}`
}

func TestDecodeStructuredCandidatesNormalizesProposal(t *testing.T) {
	revisions, err := DecodeStructuredCandidates([]byte(structuredCandidatesOutput(validStructuredCandidate)), StructuredCandidateBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 {
		t.Fatalf("revisions = %d", len(revisions))
	}
	revision := revisions[0]
	if revision.Kind != domain.MemoryKindFact || revision.CanonicalContent != "alice prefers tea" || revision.DisplayContent != "Alice prefers tea" {
		t.Fatalf("revision = %+v", revision)
	}
	if revision.EventTimeStart == nil || revision.EventTimeEnd == nil || revision.TimePrecision != domain.TimePrecisionExact || revision.TimeSource != "relative_anchor" {
		t.Fatalf("event time = %+v", revision)
	}
	if revision.ValidFrom == nil || revision.ValidUntil == nil {
		t.Fatalf("valid time = %+v", revision)
	}
	if len(revision.SubjectEntityIDs) != 1 || revision.SubjectEntityIDs[0] != "alice" || revision.Attributes["preference"] != "tea" {
		t.Fatalf("structured fields = %+v", revision)
	}
	if len(revision.Evidence) != 1 || revision.Evidence[0].Role != domain.EvidenceRoleDirect || revision.Evidence[0].EventID != "event-1" {
		t.Fatalf("evidence = %+v", revision.Evidence)
	}
}

func TestDecodeStructuredCandidatesNoMemory(t *testing.T) {
	revisions, err := DecodeStructuredCandidates([]byte(`{"category":"no_memory","candidates":[]}`), StructuredCandidateBounds{})
	if err != nil || len(revisions) != 0 {
		t.Fatalf("no_memory = %v, %v", revisions, err)
	}
	_, err = DecodeStructuredCandidates([]byte(structuredCandidatesOutput(validStructuredCandidate)), StructuredCandidateBounds{MaxCandidates: 0})
	if err != nil {
		t.Fatalf("single candidate with default bounds rejected: %v", err)
	}
	_, err = DecodeStructuredCandidates([]byte(`{"category":"no_memory","candidates":[`+validStructuredCandidate+`]}`), StructuredCandidateBounds{})
	if !errors.Is(err, errInvalidStructuredOutput) {
		t.Fatalf("no_memory with candidates error = %v", err)
	}
}

func TestDecodeStructuredCandidatesStrictDecode(t *testing.T) {
	cases := map[string]string{
		"unknown field": strings.Replace(validStructuredCandidate, `"kind":"fact"`, `"kind":"fact","extra":true`, 1),
		"unknown top":   strings.Replace(validStructuredCandidate, `"kind":"fact"`, `"kind":"fact","unexpected":{"nested":1}`, 1),
		"invalid json":  `{"category":"candidates_extracted","candidates":[`,
		"invalid cat":   `{"category":"everything","candidates":[]}`,
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeStructuredCandidates([]byte(structuredCandidatesOutput(candidate)), StructuredCandidateBounds{}); !errors.Is(err, errInvalidStructuredOutput) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	// A second JSON value after the object is not a single strict document.
	_, err := DecodeStructuredCandidates([]byte(`{"category":"no_memory","candidates":[]} {"category":"no_memory","candidates":[]}`), StructuredCandidateBounds{})
	if !errors.Is(err, errInvalidStructuredOutput) {
		t.Fatalf("additional value error = %v", err)
	}
}

func TestDecodeStructuredCandidatesRejectsInvalidFields(t *testing.T) {
	cases := map[string]string{
		"aggregate kind":        strings.Replace(validStructuredCandidate, `"kind":"fact"`, `"kind":"scene"`, 1),
		"empty canonical":       strings.Replace(validStructuredCandidate, `"canonical_content":"alice prefers tea"`, `"canonical_content":"  "`, 1),
		"confidence high":       strings.Replace(validStructuredCandidate, `"confidence":0.9`, `"confidence":1.5`, 1),
		"specificity low":       strings.Replace(validStructuredCandidate, `"specificity":0.7`, `"specificity":-0.5`, 1),
		"bad sensitivity":       strings.Replace(validStructuredCandidate, `"sensitivity":"normal"`, `"sensitivity":"secret"`, 1),
		"duplicate evidence id": strings.Replace(validStructuredCandidate, `"evidence_id":"ev-1"`, `"evidence_id":""`, 1),
		"bad role":              strings.Replace(validStructuredCandidate, `"role":"direct"`, `"role":"hint"`, 1),
		"half range":            strings.Replace(validStructuredCandidate, `,"end_offset":16`, ``, 1),
		"bad range":             strings.Replace(validStructuredCandidate, `"end_offset":16`, `"end_offset":0`, 1),
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeStructuredCandidates([]byte(structuredCandidatesOutput(candidate)), StructuredCandidateBounds{}); !errors.Is(err, errInvalidStructuredOutput) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDecodeStructuredCandidatesTimeWindowsAndAuthority(t *testing.T) {
	invertedEvent := strings.Replace(validStructuredCandidate,
		`"start":"2024-03-01T09:00:00Z","end":"2024-03-01T10:00:00Z"`,
		`"start":"2024-03-01T10:00:00Z","end":"2024-03-01T09:00:00Z"`, 1)
	invertedValid := strings.Replace(validStructuredCandidate,
		`"start":"2024-01-01T00:00:00Z","end":"2024-12-31T00:00:00Z"`,
		`"start":"2024-12-31T00:00:00Z","end":"2024-01-01T00:00:00Z"`, 1)
	contextOnly := strings.Replace(validStructuredCandidate, `"role":"direct"`, `"role":"context"`, 1)
	for name, candidate := range map[string]string{
		"inverted event window": invertedEvent,
		"inverted valid window": invertedValid,
		"context-only evidence": contextOnly,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeStructuredCandidates([]byte(structuredCandidatesOutput(candidate)), StructuredCandidateBounds{}); !errors.Is(err, errInvalidStructuredOutput) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	unknownPrecision := strings.Replace(validStructuredCandidate, `"precision":"exact"`, `"precision":""`, 1)
	revisions, err := DecodeStructuredCandidates([]byte(structuredCandidatesOutput(unknownPrecision)), StructuredCandidateBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if revisions[0].TimePrecision != domain.TimePrecisionUnknown {
		t.Fatalf("empty precision did not normalize: %q", revisions[0].TimePrecision)
	}
}

func TestDecodeStructuredCandidatesBounds(t *testing.T) {
	twoCandidates := structuredCandidatesOutput(validStructuredCandidate + "," + validStructuredCandidate)
	if _, err := DecodeStructuredCandidates([]byte(twoCandidates), StructuredCandidateBounds{MaxCandidates: 1}); !errors.Is(err, errInvalidStructuredOutput) {
		t.Fatalf("candidate count error = %v", err)
	}
	if _, err := DecodeStructuredCandidates([]byte(structuredCandidatesOutput(validStructuredCandidate)), StructuredCandidateBounds{MaxContentChars: 4}); !errors.Is(err, errInvalidStructuredOutput) {
		t.Fatalf("content bounds error = %v", err)
	}
	twoEvidence := strings.Replace(validStructuredCandidate, `"evidence":[`,
		`"evidence":[{"evidence_id":"ev-0","event_id":"event-1","role":"direct","start_offset":0,"end_offset":3},`, 1)
	if _, err := DecodeStructuredCandidates([]byte(structuredCandidatesOutput(twoEvidence)), StructuredCandidateBounds{MaxEvidencePerCandidate: 1}); !errors.Is(err, errInvalidStructuredOutput) {
		t.Fatalf("evidence bounds error = %v", err)
	}
	twoRefs := strings.Replace(validStructuredCandidate, `"subject_entity_ids":["alice"]`, `"subject_entity_ids":["alice","bob"]`, 1)
	if _, err := DecodeStructuredCandidates([]byte(structuredCandidatesOutput(twoRefs)), StructuredCandidateBounds{MaxEntityRefs: 1}); !errors.Is(err, errInvalidStructuredOutput) {
		t.Fatalf("entity ref bounds error = %v", err)
	}
}

const validTypedGraphOutput = `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":["A"],"confidence":0.9},{"local_id":"e1","canonical_name":"Alpha","entity_type":"project","aliases":[],"confidence":0.8},{"local_id":"e2","canonical_name":"Paris","entity_type":"place","aliases":[],"confidence":0.9},{"local_id":"e3","canonical_name":"Beta","entity_type":"project","aliases":[],"confidence":0.8}],"mentions":[{"entity_local_id":"e0","text":"Alice","start_rune":0,"end_rune":5,"confidence":0.9}],"events":[{"local_id":"x0","event_type":"meeting","participant_local_ids":["e0"],"place_local_id":"e2","time":{"expression":"yesterday","start":"2024-03-01T09:00:00Z","end":"2024-03-01T10:00:00Z","precision":"exact","source":"relative_anchor"},"confidence":0.8}],"facts":[{"subject_local_id":"e0","predicate":"participates_in","object_local_id":"e1","confidence":0.9},{"subject_local_id":"e1","predicate":"has_status","object_literal":"active","object_literal_type":"string","confidence":0.9},{"subject_local_id":"e1","predicate":"depends_on","object_local_id":"e3","confidence":0.7}],"relations":[{"relation_type":"supports","target_memory_id":"memory-a","target_revision_id":"revision-a","confidence":0.8}]}`

func TestDecodeTypedGraphOutputAcceptsTypedKnowledge(t *testing.T) {
	result, err := DecodeTypedGraphOutput([]byte(validTypedGraphOutput), TypedGraphBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entities) != 4 || len(result.Mentions) != 1 || len(result.Events) != 1 || len(result.Facts) != 3 || len(result.Relations) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Mentions[0].EntityLocalID != "e0" || result.Mentions[0].Text != "Alice" || result.Mentions[0].EndRune != 5 {
		t.Fatalf("mention = %+v", result.Mentions[0])
	}
	event := result.Events[0]
	if event.EventType != "meeting" || len(event.ParticipantLocalIDs) != 1 || event.PlaceLocalID != "e2" || event.Time == nil {
		t.Fatalf("event = %+v", event)
	}
	if result.Facts[0].Predicate != "participates_in" || result.Facts[1].ObjectLiteral != "active" || result.Facts[2].Predicate != "depends_on" {
		t.Fatalf("facts = %+v", result.Facts)
	}
	if !containsString(PredicateNames(), "member_of") || !containsString(PredicateNames(), "depends_on") {
		t.Fatalf("predicate registry = %v", PredicateNames())
	}
}

func TestDecodeTypedGraphOutputRejectsClosedContractViolations(t *testing.T) {
	cases := map[string]struct {
		body string
		want error
	}{
		"unknown predicate": {
			body: strings.Replace(validTypedGraphOutput, `"predicate":"participates_in"`, `"predicate":"hates"`, 1),
			want: errUnknownPredicate,
		},
		"predicate type mismatch": {
			body: strings.Replace(validTypedGraphOutput, `"predicate":"participates_in"`, `"predicate":"member_of"`, 1),
			want: errPredicateTypeMismatch,
		},
		"literal type mismatch": {
			body: strings.Replace(validTypedGraphOutput, `"object_literal_type":"string"`, `"object_literal_type":"number"`, 1),
			want: errPredicateTypeMismatch,
		},
		"unknown entity type": {
			body: strings.Replace(validTypedGraphOutput, `"entity_type":"person"`, `"entity_type":"rocket"`, 1),
			want: errUnknownEntityType,
		},
		"unknown event type": {
			body: strings.Replace(validTypedGraphOutput, `"event_type":"meeting"`, `"event_type":"party"`, 1),
			want: errUnknownEventType,
		},
		"broad relation": {
			body: strings.Replace(validTypedGraphOutput, `"relation_type":"supports"`, `"relation_type":"related_to"`, 1),
			want: errInvalidGraphOutput,
		},
		"unknown participant": {
			body: strings.Replace(validTypedGraphOutput, `"participant_local_ids":["e0"]`, `"participant_local_ids":["missing"]`, 1),
			want: errInvalidGraphOutput,
		},
		"unknown mention entity": {
			body: strings.Replace(validTypedGraphOutput, `"entity_local_id":"e0","text":"Alice"`, `"entity_local_id":"missing","text":"Alice"`, 1),
			want: errInvalidGraphOutput,
		},
		"inverted mention range": {
			body: strings.Replace(validTypedGraphOutput, `"start_rune":0,"end_rune":5`, `"start_rune":5,"end_rune":2`, 1),
			want: errInvalidGraphOutput,
		},
		"self fact": {
			body: strings.Replace(validTypedGraphOutput, `"predicate":"depends_on","object_local_id":"e3"`, `"predicate":"depends_on","object_local_id":"e1"`, 1),
			want: errInvalidGraphOutput,
		},
		"duplicate entity": {
			body: strings.Replace(validTypedGraphOutput, `"local_id":"e1"`, `"local_id":"e0"`, 1),
			want: errInvalidGraphOutput,
		},
		"unknown field": {
			body: strings.Replace(validTypedGraphOutput, `"canonical_name":"Alice"`, `"canonical_name":"Alice","extra":true`, 1),
			want: errInvalidGraphOutput,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTypedGraphOutput([]byte(test.body), TypedGraphBounds{}); !errors.Is(err, test.want) {
				t.Fatalf("error = %v; want %v", err, test.want)
			}
		})
	}
}

func TestDecodeTypedGraphOutputBounds(t *testing.T) {
	if _, err := DecodeTypedGraphOutput([]byte(validTypedGraphOutput), TypedGraphBounds{MaxEntities: 1}); !errors.Is(err, errInvalidGraphOutput) {
		t.Fatalf("entity bounds error = %v", err)
	}
	if _, err := DecodeTypedGraphOutput([]byte(validTypedGraphOutput), TypedGraphBounds{MaxAliases: 0}); err != nil {
		t.Fatalf("default alias bound rejected valid output: %v", err)
	}
	if _, err := DecodeTypedGraphOutput([]byte(`{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":["A","B"],"confidence":0.9}]}`), TypedGraphBounds{MaxAliases: 1}); !errors.Is(err, errInvalidGraphOutput) {
		t.Fatalf("alias bounds error = %v", err)
	}
}
