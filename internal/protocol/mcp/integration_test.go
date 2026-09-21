package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/N0rthHXD/dMem/pkg/client"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

type httpExchange struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// TestMCPClientIntegrationAgainstApplicationHTTP proves the MCP handler and the
// public application client compose into one Agent Tool command path: the same
// tool call reaches the application HTTP API with caller identity, the injected
// scope, and transport provenance, and its structured result travels back
// unchanged.
func TestMCPClientIntegrationAgainstApplicationHTTP(t *testing.T) {
	var mutex sync.Mutex
	var exchanges []httpExchange
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mutex.Lock()
		exchanges = append(exchanges, httpExchange{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
		mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/scopes/ensure":
			respondJSON(w, http.StatusOK, `{"operation_id":"op","replayed":false,"scope":{"scope_id":"scope-http","parent_scope_id":null,"scope_type":"project","external_id":"external-1","display_name":"external-1","created_at":"2026-09-20T10:00:00Z"}}`)
		case "/v1/memory-commits":
			var input toolcontract.MemoryCommitInput
			if err := toolcontract.DecodeStrict(toolcontract.SchemaMemoryCommitInput, body, &input); err != nil {
				t.Errorf("application received an invalid commit: %v", err)
			}
			respondJSON(w, http.StatusCreated, `{"operation_id":"op","replayed":false,"source_event_id":"event-1","memories":[{"local_id":"m","status":"authority","memory_id":"mem-1","revision_id":"rev-1"}],"entities":[],"facts":[],"relations":[]}`)
		case "/v1/recall":
			var input toolcontract.RecallInput
			if err := toolcontract.DecodeStrict(toolcontract.SchemaRecallInput, body, &input); err != nil {
				t.Errorf("application received an invalid recall: %v", err)
			}
			if len(input.ScopeIDs) != 1 || input.ScopeIDs[0] != "scope-http" {
				t.Errorf("recall scope ids = %v", input.ScopeIDs)
			}
			respondJSON(w, http.StatusOK, `{"operation_id":"recall-op","memories":[{"memory_id":"mem-1","revision_id":"rev-1","kind":"fact","content":"stored answer","confidence":0.9,"evidence":[]}],"degraded_capabilities":["semantic"],"next_cursor":null}`)
		default:
			respondJSON(w, http.StatusNotFound, `{"error":{"code":"not_found","message":"route not found","request_id":"","retryable":false}}`)
		}
	}))
	defer httpServer.Close()

	applicationClient, err := client.New(client.Config{
		Endpoint: httpServer.URL,
		Caller:   client.Caller{SubjectID: "agent", ClientID: "dmem-mcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := startSession(t, Config{
		Name: "dmem-mcp", Version: "test",
		Client: applicationClient,
		Scope:  &ConfiguredScope{Client: applicationClient, ScopeType: "project", ExternalID: "external-1"},
	})
	session.initialize()

	remember := session.callTool(2, toolcontract.ToolRemember, map[string]any{
		"memories": []map[string]any{{"local_id": "m", "kind": "fact", "content": "stored answer", "sensitivity": "normal", "confidence": 0.9}},
	})
	rememberResult := resultOf(t, remember)
	if isErrorOf(t, rememberResult) {
		t.Fatalf("remember failed: %v", rememberResult)
	}
	structured := structuredOf(t, rememberResult)
	outcomes, _ := structured["memories"].([]any)
	if len(outcomes) != 1 {
		t.Fatalf("remember outcomes = %v", structured["memories"])
	}

	recall := session.callTool(3, toolcontract.ToolRecall, map[string]any{"query": "stored answer"})
	recallResult := resultOf(t, recall)
	if isErrorOf(t, recallResult) {
		t.Fatalf("recall failed: %v", recallResult)
	}
	recallStructured := structuredOf(t, recallResult)
	if recallStructured["operation_id"] != "recall-op" {
		t.Fatalf("recall = %v", recallStructured)
	}
	degraded, _ := recallStructured["degraded_capabilities"].([]any)
	if len(degraded) != 1 || degraded[0] != "semantic" {
		t.Fatalf("degraded = %v", degraded)
	}

	mutex.Lock()
	defer mutex.Unlock()
	if len(exchanges) != 3 {
		t.Fatalf("application exchanges = %d, want 3", len(exchanges))
	}
	for _, exchange := range exchanges {
		if exchange.header.Get("X-Caller-Subject-ID") != "agent" || exchange.header.Get("X-Caller-Client-ID") != "dmem-mcp" {
			t.Errorf("%s caller headers = %v", exchange.path, exchange.header)
		}
		if exchange.header.Get("X-Request-ID") == "" {
			t.Errorf("%s has no request id", exchange.path)
		}
	}
	if exchanges[1].path != "/v1/memory-commits" || exchanges[1].header.Get("Idempotency-Key") == "" {
		t.Fatalf("commit exchange = %+v", exchanges[1])
	}
	var commit toolcontract.MemoryCommitInput
	if err := toolcontract.DecodeStrict(toolcontract.SchemaMemoryCommitInput, exchanges[1].body, &commit); err != nil {
		t.Fatal(err)
	}
	if commit.ScopeID != "scope-http" {
		t.Fatalf("scope = %q", commit.ScopeID)
	}
	if commit.Source.Type != "agent_tool" || commit.Source.Name == "" || commit.Source.ConversationID == "" || commit.Source.OccurredAt == "" {
		t.Fatalf("source = %+v", commit.Source)
	}
	if exchanges[2].header.Get("Idempotency-Key") != "" {
		t.Fatal("recall sent an idempotency key")
	}
}

// respondJSON writes one application JSON response.
func respondJSON(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
