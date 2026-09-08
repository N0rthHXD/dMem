package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func graphExtractRequest() GraphExtractRequest {
	memory := NewMemory("memory-a", "revision-a", "space-a", "context memory", "active", "normal", nil, nil)
	return NewGraphExtractRequest("processor", "v1", "job-a", 1, "space-a", "memory-source", "revision-source", "Alice participates in Alpha", "normal", []Memory{memory}, 20, 20, 20, 1000)
}

func TestGraphExtractAcceptsValidOutput(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"entities\":[{\"local_id\":\"e0\",\"canonical_name\":\"Alice\",\"entity_type\":\"person\",\"aliases\":[\"A\"],\"confidence\":0.9},{\"local_id\":\"e1\",\"canonical_name\":\"Alpha\",\"entity_type\":\"project\",\"aliases\":[],\"confidence\":0.8}],\"mentions\":[{\"entity_local_id\":\"e0\",\"text\":\"Alice\",\"start_rune\":0,\"end_rune\":5,\"confidence\":0.9}],\"facts\":[{\"subject_local_id\":\"e0\",\"predicate\":\"participates_in\",\"object_local_id\":\"e1\",\"confidence\":0.9}],\"relations\":[{\"relation_type\":\"supports\",\"target_memory_id\":\"memory-a\",\"target_revision_id\":\"revision-a\",\"confidence\":0.8,\"evidence\":[]}]}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
	result, err := client.GraphExtract(context.Background(), graphExtractRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entities()) != 2 || len(result.Mentions()) != 1 || len(result.Facts()) != 1 || len(result.Relations()) != 1 {
		t.Fatalf("graph result = %+v", result)
	}
}

func TestGraphExtractRejectsInvalidOutput(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	tests := []struct {
		name string
		body string
		code string
	}{
		{"unknown field", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":[],"confidence":0.9,"extra":true}]}`, "malformed_graph_extract_output"},
		{"empty name", `{"entities":[{"local_id":"e0","canonical_name":"","entity_type":"person","aliases":[],"confidence":0.9}]}`, "invalid_graph_entity"},
		{"bad type", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"rocket","aliases":[],"confidence":0.9}]}`, "invalid_graph_entity"},
		{"mention rune mismatch", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":[],"confidence":0.9}],"mentions":[{"entity_local_id":"e0","text":"Bob","start_rune":0,"end_rune":5,"confidence":0.9}]}`, "invalid_graph_mention"},
		{"unknown mention entity", `{"mentions":[{"entity_local_id":"missing","text":"Alice","start_rune":0,"end_rune":5,"confidence":0.9}]}`, "unknown_mention_entity"},
		{"self fact", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":[],"confidence":0.9}],"facts":[{"subject_local_id":"e0","predicate":"participates_in","object_local_id":"e0","confidence":0.9}]}`, "self_fact_relation"},
		{"bad predicate", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":[],"confidence":0.9}],"facts":[{"subject_local_id":"e0","predicate":"hates","object_local_id":"e0","confidence":0.9}]}`, "invalid_fact_predicate"},
		{"NaN confidence", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":[],"confidence":1.5}]}`, "invalid_graph_entity"},
		{"unknown relation target", `{"relations":[{"relation_type":"supports","target_memory_id":"missing","target_revision_id":"revision-a","confidence":0.8,"evidence":[]}]}`, "unknown_relation_target"},
		{"bad relation type", `{"relations":[{"relation_type":"friend_of","target_memory_id":"memory-a","target_revision_id":"revision-a","confidence":0.8,"evidence":[]}]}`, "invalid_relation_type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + strconv.Quote(test.body) + `}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
			_, err := client.GraphExtract(context.Background(), graphExtractRequest())
			assertProviderError(t, err, ErrorInvalidOutput, test.code)
		})
	}
}

func TestGraphExtractRejectsDisabledAndPolicy(t *testing.T) {
	client := &Client{enabled: false}
	_, err := client.GraphExtract(context.Background(), graphExtractRequest())
	assertProviderError(t, err, ErrorDisabled, "provider_disabled")

	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("policy rejection reached network") }))
	defer server.Close()
	configured := newTestClient(t, server, Config{})
	request := graphExtractRequest()
	// sensitivity not in allowlist is enforced in PreflightGraphExtract
	bad := NewGraphExtractRequest("p", "v", "j", 1, "space-a", "m", "r", "content", "sensitive", nil, 20, 20, 20, 1000)
	_, err = configured.GraphExtract(context.Background(), bad)
	assertProviderError(t, err, ErrorPolicy, "sensitivity_not_allowed")
	_ = request
	_ = time.Now
}
