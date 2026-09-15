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

func TestGraphExtractAcceptsValidTypedOutput(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + strconv.Quote(validTypedGraphOutput) + `}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, Config{Endpoint: server.URL + "/chat/completions"})
	result, err := client.GraphExtract(context.Background(), graphExtractRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entities()) != 4 || len(result.Mentions()) != 1 || len(result.Events()) != 1 || len(result.Facts()) != 3 || len(result.Relations()) != 1 {
		t.Fatalf("graph result = %+v", result)
	}
	if result.Mentions()[0].Text != "Alice" || result.Mentions()[0].EndRune != 5 {
		t.Fatalf("mention = %+v", result.Mentions()[0])
	}
	if !containsString(PredicateNames(), "member_of") || !containsString(PredicateNames(), "depends_on") {
		t.Fatalf("predicate registry = %v", PredicateNames())
	}
}

func TestGraphExtractRejectsInvalidTypedOutput(t *testing.T) {
	t.Setenv("DMEM_TEST_SECRET", "secret")
	tests := []struct {
		name string
		body string
		code string
	}{
		{"unknown field", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":[],"confidence":0.9,"extra":true}]}`, "malformed_graph_extract_output"},
		{"empty name", `{"entities":[{"local_id":"e0","canonical_name":"","entity_type":"person","aliases":[],"confidence":0.9}]}`, "malformed_graph_extract_output"},
		{"bad type", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"rocket","aliases":[],"confidence":0.9}]}`, "invalid_graph_entity"},
		{"mention rune mismatch", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":[],"confidence":0.9}],"mentions":[{"entity_local_id":"e0","text":"Bob","start_rune":0,"end_rune":5,"confidence":0.9}]}`, "invalid_graph_mention"},
		{"unknown mention entity", `{"mentions":[{"entity_local_id":"missing","text":"Alice","start_rune":0,"end_rune":5,"confidence":0.9}]}`, "malformed_graph_extract_output"},
		{"self fact", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":[],"confidence":0.9}],"facts":[{"subject_local_id":"e0","predicate":"participates_in","object_local_id":"e0","confidence":0.9}]}`, "malformed_graph_extract_output"},
		{"bad predicate", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":[],"confidence":0.9}],"facts":[{"subject_local_id":"e0","predicate":"hates","object_local_id":"e0","confidence":0.9}]}`, "invalid_fact_predicate"},
		{"NaN confidence", `{"entities":[{"local_id":"e0","canonical_name":"Alice","entity_type":"person","aliases":[],"confidence":1.5}]}`, "malformed_graph_extract_output"},
		{"unknown relation target", `{"relations":[{"relation_type":"supports","target_memory_id":"missing","target_revision_id":"revision-a","confidence":0.8}]}`, "unknown_relation_target"},
		{"broad relation", `{"relations":[{"relation_type":"related_to","target_memory_id":"memory-a","target_revision_id":"revision-a","confidence":0.8}]}`, "malformed_graph_extract_output"},
		{"unknown event type", `{"events":[{"local_id":"x0","event_type":"party","participant_local_ids":[],"place_local_id":"","time":null,"confidence":0.8}]}`, "invalid_event_type"},
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
	bad := NewGraphExtractRequest("p", "v", "j", 1, "space-a", "m", "r", "content", "sensitive", nil, 20, 20, 20, 1000)
	_, err = configured.GraphExtract(context.Background(), bad)
	assertProviderError(t, err, ErrorPolicy, "sensitivity_not_allowed")
	_ = time.Now
}
