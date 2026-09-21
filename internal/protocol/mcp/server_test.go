package mcp

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/pkg/client"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

func TestInitializeDeclaresToolsOnly(t *testing.T) {
	session := newSession(t, nil)
	response := session.initialize()
	result := resultOf(t, response)
	if result["protocolVersion"] != ProtocolVersion20250618 {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	capabilities, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %v", result["capabilities"])
	}
	if _, present := capabilities["tools"]; !present {
		t.Fatalf("tools capability missing: %v", capabilities)
	}
	for _, forbidden := range []string{"sampling", "prompts", "resources", "logging", "completions"} {
		if _, present := capabilities[forbidden]; present {
			t.Errorf("capabilities declared unsupported %q", forbidden)
		}
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok || info["name"] != "dmem-mcp" || info["version"] != "test" {
		t.Fatalf("serverInfo = %v", result["serverInfo"])
	}
	instructions, _ := result["instructions"].(string)
	for _, tool := range []string{toolcontract.ToolRecall, toolcontract.ToolRemember, toolcontract.ToolFeedback, toolcontract.ToolForget} {
		if !strings.Contains(instructions, tool) {
			t.Errorf("initialize instructions do not mention %s", tool)
		}
	}
}

func TestInitializeNegotiatesProtocolVersion(t *testing.T) {
	session := newSession(t, nil)
	session.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": ProtocolVersion20241105}})
	response, _ := session.receiveForID(1)
	if got := resultOf(t, response)["protocolVersion"]; got != ProtocolVersion20241105 {
		t.Fatalf("protocolVersion = %v, want %s", got, ProtocolVersion20241105)
	}

	session.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "initialize",
		"params": map[string]any{"protocolVersion": "1999-01-01"}})
	response, _ = session.receiveForID(2)
	if got := resultOf(t, response)["protocolVersion"]; got != ProtocolVersion20250618 {
		t.Fatalf("fallback protocolVersion = %v", got)
	}
}

func TestInitializeRequiresProtocolVersion(t *testing.T) {
	session := newSession(t, nil)
	session.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	response, _ := session.receiveForID(1)
	rpcErr, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("response = %v", response)
	}
	if code, _ := rpcErr["code"].(float64); int(code) != codeInvalidParams {
		t.Fatalf("error code = %v", rpcErr["code"])
	}
}

func TestRequestBeforeInitializeIsRejected(t *testing.T) {
	session := newSession(t, nil)
	session.send(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/list"})
	response, _ := session.receiveForID(5)
	rpcErr, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("response = %v", response)
	}
	if code, _ := rpcErr["code"].(float64); int(code) != codeNotInitialized {
		t.Fatalf("error code = %v", rpcErr["code"])
	}
}

func TestPingIsAllowedBeforeInitialization(t *testing.T) {
	session := newSession(t, nil)
	session.send(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "ping"})
	response, _ := session.receiveForID(5)
	if _, hasError := response["error"]; hasError {
		t.Fatalf("ping before initialize returned an error: %v", response)
	}
	result, present := response["result"].(map[string]any)
	if !present || len(result) != 0 {
		t.Fatalf("ping result = %v (present=%v)", response["result"], present)
	}
}

func TestPingIsAllowedAfterInitialization(t *testing.T) {
	session := newSession(t, nil)
	session.initialize()
	session.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "ping"})
	response, _ := session.receiveForID(2)
	if result := resultOf(t, response); len(result) != 0 {
		t.Fatalf("ping result = %v", result)
	}
}

func TestMalformedInputAndUnknownMethods(t *testing.T) {
	session := newSession(t, nil)
	session.initialize()

	session.sendRaw("{not json")
	response, _ := session.receiveForID(nil)
	rpcErr, _ := response["error"].(map[string]any)
	if code, _ := rpcErr["code"].(float64); int(code) != codeParseError {
		t.Fatalf("parse error = %v", rpcErr)
	}

	session.sendRaw(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`)
	response, _ = session.receiveForID(nil)
	rpcErr, _ = response["error"].(map[string]any)
	if code, _ := rpcErr["code"].(float64); int(code) != codeInvalidRequest {
		t.Fatalf("batch error = %v", rpcErr)
	}

	session.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "resources/list"})
	response, _ = session.receiveForID(3)
	rpcErr, _ = response["error"].(map[string]any)
	if code, _ := rpcErr["code"].(float64); int(code) != codeMethodNotFound {
		t.Fatalf("unknown method error = %v", rpcErr)
	}

	// Unknown notifications are ignored, not answered.
	session.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/unknown"})
	session.send(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "ping"})
	response, _ = session.receiveForID(4)
	resultOf(t, response)
}

func TestOversizedMessageIsRejectedAndConnectionSurvives(t *testing.T) {
	session := newSession(t, func(config *Config) { config.MaxMessageBytes = 4096 })
	session.initialize()
	session.sendRaw(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"padding":"` + strings.Repeat("x", 8192) + `"}}`)
	response, _ := session.receiveForID(nil)
	rpcErr, _ := response["error"].(map[string]any)
	if code, _ := rpcErr["code"].(float64); int(code) != codeParseError {
		t.Fatalf("oversize error = %v", rpcErr)
	}
	session.send(map[string]any{"jsonrpc": "2.0", "id": 10, "method": "ping"})
	resultOf(t, func() map[string]any { response, _ := session.receiveForID(10); return response }())
}

func TestCancellationStopsInFlightCallAndSuppressesResponse(t *testing.T) {
	started := make(chan struct{})
	var once bool
	session := newSession(t, func(config *Config) {
		config.Client = &blockingClient{started: started, release: make(chan struct{}), once: &once}
		config.Scope = &ConfiguredScope{Client: config.Client.(*blockingClient), ScopeType: "project", ExternalID: "external-1"}
	})
	session.initialize()
	session.send(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": toolcontract.ToolRecall, "arguments": map[string]any{"query": "query"}}})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool call did not start")
	}
	session.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled",
		"params": map[string]any{"requestId": 7, "reason": "test"}})
	session.send(map[string]any{"jsonrpc": "2.0", "id": 8, "method": "ping"})
	response, seen := session.receiveForID(8)
	resultOf(t, response)
	for _, id := range seen {
		if id == float64(7) {
			t.Fatalf("cancelled request produced a response; seen ids = %v", seen)
		}
	}
}

func TestLogsDoNotContainToolArguments(t *testing.T) {
	var logs bytes.Buffer
	session := newSession(t, func(config *Config) {
		config.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	session.initialize()
	session.callTool(2, toolcontract.ToolRemember, map[string]any{
		"memories": []map[string]any{{"local_id": "m", "kind": "fact", "content": "TOP-SECRET-CONTENT",
			"sensitivity": "normal", "confidence": 0.9}},
	})
	output := logs.String()
	if strings.Contains(output, "TOP-SECRET") {
		t.Fatalf("logs leaked tool content: %s", output)
	}
	if !strings.Contains(output, "mcp tool call") {
		t.Fatalf("logs are missing tool metadata: %s", output)
	}
}

// blockingClient blocks Recall until its context is cancelled, which lets the
// test exercise cancellation deterministically.
type blockingClient struct {
	fakeClient
	started chan struct{}
	release chan struct{}
	once    *bool
}

func (b *blockingClient) Recall(ctx context.Context, call client.Call, input toolcontract.RecallInput) (toolcontract.RecallOutput, error) {
	if !*b.once {
		*b.once = true
		close(b.started)
	}
	select {
	case <-ctx.Done():
		return toolcontract.RecallOutput{}, ctx.Err()
	case <-b.release:
		return toolcontract.RecallOutput{}, nil
	}
}

func TestServerRejectsInvalidConfiguration(t *testing.T) {
	fake := &fakeClient{}
	cases := map[string]Config{
		"missing client":        {Client: nil, Scope: &ConfiguredScope{Client: fake}},
		"missing scope":         {Client: fake, Scope: nil},
		"negative timeout":      {Client: fake, Scope: &ConfiguredScope{Client: fake}, RequestTimeout: -time.Second},
		"small message limit":   {Client: fake, Scope: &ConfiguredScope{Client: fake}, MaxMessageBytes: 10},
		"unknown mode":          {Client: fake, Scope: &ConfiguredScope{Client: fake}, IntegrationMode: "sometimes"},
		"duplicate definitions": {Client: fake, Scope: &ConfiguredScope{Client: fake}, Tools: []toolcontract.ToolDefinition{{Name: "a"}, {Name: "a"}}},
		"empty definitions":     {Client: fake, Scope: &ConfiguredScope{Client: fake}, Tools: []toolcontract.ToolDefinition{}},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(config); err == nil {
				t.Fatal("New() accepted an invalid configuration")
			}
		})
	}
}
