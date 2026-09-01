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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"category\":\"candidates_extracted\",\"candidates\":[{\"content\":\"remember this\",\"memory_type\":\"fact\",\"space_id\":\"space-a\",\"confidence\":0.8,\"sensitivity\":\"sensitive\",\"evidence\":[{\"event_id\":\"event-a\",\"evidence_type\":\"manual_reference\",\"start_offset\":null,\"end_offset\":null}],\"suggestion_kind\":\"new\",\"related_memory_ids\":[]}]}"}}],"usage":{"prompt_tokens":12,"completion_tokens":5}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions", Limits: Limits{MaxContextItems: 1}})
	target := NewEvent("event-a", "space-a", "file_reference", "source", "normal", time.Unix(1, 0), NewReferenceContent("text/plain", 42, "sha256:abc"))
	contextEvents := []Event{
		NewEvent("context-a", "space-a", "message", "source", "normal", time.Unix(2, 0), NewTextContent("text/plain", "allowed-context")),
		NewEvent("context-b", "space-a", "message", "source", "normal", time.Unix(3, 0), NewTextContent("text/plain", "context-over-limit")),
	}
	result, err := client.Process(context.Background(), NewProcessRequest("processor-a", "v2", "job-a", 1, target, contextEvents, nil, []string{"fact"}, 2, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if result.Category() != "candidates_extracted" || len(result.Candidates()) != 1 || result.ProviderRequestID() != "request-123" {
		t.Fatalf("result = %+v", result)
	}
	candidate := result.Candidates()[0]
	if candidate.Sensitivity() != "sensitive" || len(candidate.Evidence()) != 1 || candidate.Evidence()[0].EvidenceType() != "manual_reference" {
		t.Fatalf("candidate contract = %+v", candidate)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestProcessPreservesContextMemoryBudgetSensitivityAndTypedEvidence(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	validFrom := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	validUntil := validFrom.Add(24 * time.Hour)
	start, end := int64(1), int64(4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var prompt processPrompt
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &prompt); err != nil {
			t.Fatal(err)
		}
		if prompt.MaxContentChars != 80 || len(prompt.ContextMemories) != 1 {
			t.Fatalf("provider prompt = %+v", prompt)
		}
		memory := prompt.ContextMemories[0]
		if memory.MemoryID != "memory-a" || memory.RevisionID != "revision-a" || memory.Content != "bounded memory" || memory.State != "contested" || memory.Sensitivity != "normal" || memory.ValidFrom == nil || memory.ValidUntil == nil {
			t.Fatalf("context memory = %+v", memory)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"candidates_extracted\",\"candidates\":[{\"content\":\"typed result\",\"memory_type\":\"fact\",\"space_id\":\"space-a\",\"confidence\":0.9,\"sensitivity\":\"sensitive\",\"evidence\":[{\"event_id\":\"event-a\",\"evidence_type\":\"tool_result\",\"start_offset\":1,\"end_offset\":4}],\"suggestion_kind\":\"supplement\",\"related_memory_ids\":[\"memory-a\"]}]}"}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
	target := NewEvent("event-a", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "source text"))
	memory := NewMemory("memory-a", "revision-a", "space-a", "bounded memory", "contested", "normal", &validFrom, &validUntil)
	result, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, nil, []Memory{memory}, []string{"fact"}, 1, 80))
	if err != nil {
		t.Fatal(err)
	}
	candidate := result.Candidates()[0]
	evidence := candidate.Evidence()[0]
	if candidate.Sensitivity() != "sensitive" || evidence.EvidenceType() != "tool_result" || evidence.StartOffset() == nil || *evidence.StartOffset() != start || evidence.EndOffset() == nil || *evidence.EndOffset() != end {
		t.Fatalf("candidate = %+v evidence = %+v", candidate, evidence)
	}
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
		var prompt processPrompt
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
	result, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, events, memories, []string{"fact"}, 1, 100))
	if err != nil {
		t.Fatal(err)
	}
	if result.OmissionSummary()["context_limit"] != 1 {
		t.Fatalf("omission summary = %#v", result.OmissionSummary())
	}
}

func TestProcessRejectsEvidenceFromOmittedContext(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"candidates_extracted\",\"candidates\":[{\"content\":\"result\",\"memory_type\":\"fact\",\"space_id\":\"space-a\",\"confidence\":0.8,\"sensitivity\":\"normal\",\"evidence\":[{\"event_id\":\"denied\",\"evidence_type\":\"direct_statement\",\"start_offset\":null,\"end_offset\":null}],\"suggestion_kind\":\"new\",\"related_memory_ids\":[]}] }"}}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
	target := NewEvent("target", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "target"))
	denied := NewEvent("denied", "space-a", "message", "source", "sensitive", time.Now(), NewTextContent("text/plain", "private"))
	_, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, []Event{denied}, nil, []string{"fact"}, 1, 100))
	assertProviderError(t, err, ErrorInvalidOutput, "invalid_evidence_reference")
}

func TestProcessRejectsJobLayerOnlyCandidateViolations(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	for _, test := range []struct {
		name     string
		output   string
		wantCode string
	}{
		{
			name:     "non-new whole-event evidence",
			output:   `{"category":"candidates_extracted","candidates":[{"content":"result","memory_type":"fact","space_id":"space-a","confidence":0.8,"sensitivity":"normal","evidence":[{"event_id":"event-a","evidence_type":"direct_statement","start_offset":null,"end_offset":null}],"suggestion_kind":"supplement","related_memory_ids":["memory-a"]}]}`,
			wantCode: "invalid_evidence_reference",
		},
		{
			name:     "missing supplement target",
			output:   `{"category":"candidates_extracted","candidates":[{"content":"result","memory_type":"fact","space_id":"space-a","confidence":0.8,"sensitivity":"normal","evidence":[{"event_id":"event-a","evidence_type":"direct_statement","start_offset":0,"end_offset":6}],"suggestion_kind":"supplement","related_memory_ids":[]}]}`,
			wantCode: "invalid_related_memory_reference",
		},
		{
			name:     "duplicate memory target",
			output:   `{"category":"candidates_extracted","candidates":[{"content":"result","memory_type":"fact","space_id":"space-a","confidence":0.8,"sensitivity":"normal","evidence":[{"event_id":"event-a","evidence_type":"direct_statement","start_offset":0,"end_offset":6}],"suggestion_kind":"duplicate","related_memory_ids":["memory-a","memory-a"]}]}`,
			wantCode: "invalid_related_memory_reference",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + strconv.Quote(test.output) + `}}]}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
			target := NewEvent("event-a", "space-a", "message", "source", "normal", time.Now(), NewTextContent("text/plain", "source text"))
			memory := NewMemory("memory-a", "revision-a", "space-a", "context", "active", "normal", nil, nil)
			_, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, nil, []Memory{memory}, []string{"fact"}, 1, 100))
			assertProviderError(t, err, ErrorInvalidOutput, test.wantCode)
		})
	}
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
			_, err := client.Process(context.Background(), NewProcessRequest("p", "v", "j", 1, test.target, nil, nil, []string{"fact"}, 1, 1000))
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
		var prompt processPrompt
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
	if _, err := client.Process(context.Background(), NewProcessRequest("p", "v", "j", 1, target, contextEvents, nil, []string{"fact"}, 1, 10)); err != nil {
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
	_, err := client.Process(context.Background(), NewProcessRequest("p", "v", "j", 1, target, nil, nil, []string{"fact"}, 1, 1000))
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
	result, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, []Event{denied}, nil, []string{"fact"}, 1, 100, 100))
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
	if _, err := client.Process(context.Background(), NewProcessRequest("processor", "v1", "job", 1, target, nil, nil, []string{"fact"}, 1, 100)); err != nil {
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
