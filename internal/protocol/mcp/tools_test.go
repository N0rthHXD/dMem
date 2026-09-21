package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/pkg/client"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

func TestToolsListAdvertisesContractSchemas(t *testing.T) {
	session := newSession(t, nil)
	session.initialize()
	session.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	result := resultOf(t, func() map[string]any { response, _ := session.receiveForID(2); return response }())
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 4 {
		t.Fatalf("tools = %v", result["tools"])
	}
	byName := map[string]map[string]any{}
	for _, entry := range tools {
		tool, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("tool = %v", entry)
		}
		name, _ := tool["name"].(string)
		byName[name] = tool
	}
	for _, name := range []string{toolcontract.ToolRecall, toolcontract.ToolRemember, toolcontract.ToolFeedback, toolcontract.ToolForget} {
		tool, present := byName[name]
		if !present {
			t.Fatalf("tool %s is missing", name)
		}
		if tool["inputSchema"] == nil || tool["outputSchema"] == nil {
			t.Fatalf("tool %s is missing schemas: %v", name, tool)
		}
		if description, _ := tool["description"].(string); description == "" {
			t.Fatalf("tool %s has no description", name)
		}
	}
	remember := byName[toolcontract.ToolRemember]
	inputSchema, _ := remember["inputSchema"].(map[string]any)
	required := stringSlice(t, inputSchema["required"])
	for _, field := range []string{"scope_id", "source"} {
		if slices.Contains(required, field) {
			t.Errorf("remember MCP schema still requires %s", field)
		}
	}
	properties, _ := inputSchema["properties"].(map[string]any)
	if _, present := properties["source"]; present {
		t.Error("remember MCP schema still exposes source")
	}
	if !slices.Contains(required, "memories") {
		t.Error("remember MCP schema dropped the memories requirement")
	}
	recallAnnotations, _ := byName[toolcontract.ToolRecall]["annotations"].(map[string]any)
	if recallAnnotations["readOnlyHint"] != true {
		t.Errorf("recall annotations = %v", recallAnnotations)
	}
	forgetAnnotations, _ := byName[toolcontract.ToolForget]["annotations"].(map[string]any)
	if forgetAnnotations["destructiveHint"] != true {
		t.Errorf("forget annotations = %v", forgetAnnotations)
	}
}

func TestRecallInjectsConfiguredScope(t *testing.T) {
	session := newSession(t, nil)
	session.initialize()
	response := session.callTool(2, toolcontract.ToolRecall, map[string]any{"query": "database choice", "limit": 5})
	result := resultOf(t, response)
	if isErrorOf(t, result) {
		t.Fatalf("recall failed: %v", result)
	}
	structured := structuredOf(t, result)
	if structured["operation_id"] != "recall-op" {
		t.Fatalf("structured = %v", structured)
	}
	output, ok := result["content"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("content = %v", result["content"])
	}
	fake := session.client(t)
	input := fake.lastRecall(t)
	if len(input.ScopeIDs) != 1 || input.ScopeIDs[0] != "scope-1" {
		t.Fatalf("scope ids = %v", input.ScopeIDs)
	}
	if input.Query != "database choice" || input.Limit == nil || *input.Limit != 5 {
		t.Fatalf("recall input = %+v", input)
	}
	if call := fake.lastCall(t); call.RequestID != "2" || call.CorrelationID == "" {
		t.Fatalf("application call = %+v", call)
	}
}

func TestCallerScopeMismatchIsDenied(t *testing.T) {
	session := newSession(t, nil)
	session.initialize()
	response := session.callTool(2, toolcontract.ToolRecall, map[string]any{"query": "q", "scope_ids": []string{"other-scope"}})
	result := resultOf(t, response)
	if !isErrorOf(t, result) {
		t.Fatalf("mismatched scope was accepted: %v", result)
	}
	if code := errorCodeOf(t, result); code != "permission_denied" {
		t.Fatalf("error code = %s", code)
	}
	if session.client(t).recallCount() != 0 {
		t.Fatal("a denied recall reached the application client")
	}
}

func TestRememberInjectsTransportSource(t *testing.T) {
	session := newSession(t, nil)
	session.initialize()
	response := session.callTool(2, toolcontract.ToolRemember, map[string]any{
		"memories": []map[string]any{{"local_id": "m", "kind": "preference", "content": "PostgreSQL is preferred",
			"sensitivity": "normal", "confidence": 0.9, "search_hints": []string{"database choice"}}},
	})
	result := resultOf(t, response)
	if isErrorOf(t, result) {
		t.Fatalf("remember failed: %v", result)
	}
	input := session.client(t).lastCommit(t)
	if input.ScopeID != "scope-1" {
		t.Fatalf("scope = %q", input.ScopeID)
	}
	if input.Source.Type != "agent_tool" || input.Source.Name != DefaultSourceName {
		t.Fatalf("source = %+v", input.Source)
	}
	if input.Source.ConversationID == "" || input.Source.TurnID == "" || input.Source.ToolCallID != "2" {
		t.Fatalf("source identity = %+v", input.Source)
	}
	if input.Source.OccurredAt != "2026-09-20T10:00:00Z" {
		t.Fatalf("occurred_at = %q", input.Source.OccurredAt)
	}
}

func TestRememberRejectsCallerSuppliedSource(t *testing.T) {
	session := newSession(t, nil)
	session.initialize()
	response := session.callTool(2, toolcontract.ToolRemember, map[string]any{
		"source":   map[string]any{"type": "agent_tool", "name": "forged", "conversation_id": "c", "turn_id": "t", "occurred_at": "2026-09-20T10:00:00Z"},
		"memories": []map[string]any{{"local_id": "m", "kind": "fact", "content": "c", "sensitivity": "normal", "confidence": 0.5}},
	})
	result := resultOf(t, response)
	if !isErrorOf(t, result) {
		t.Fatalf("caller source was accepted: %v", result)
	}
	if code := errorCodeOf(t, result); code != "invalid_json" {
		t.Fatalf("error code = %s", code)
	}
	if session.client(t).commitCount() != 0 {
		t.Fatal("a forged source reached the application client")
	}
}

func TestUnknownFieldAndArgumentShapeAreRejected(t *testing.T) {
	session := newSession(t, nil)
	session.initialize()
	unknown := session.callTool(2, toolcontract.ToolRecall, map[string]any{"query": "q", "unexpected": true})
	if !isErrorOf(t, resultOf(t, unknown)) {
		t.Fatalf("unknown field was accepted: %v", unknown)
	}
	if code := errorCodeOf(t, resultOf(t, unknown)); code != "invalid_json" {
		t.Fatalf("unknown field code = %s", code)
	}

	session.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": toolcontract.ToolRecall, "arguments": "not-an-object"}})
	response, _ := session.receiveForID(3)
	result := resultOf(t, response)
	if !isErrorOf(t, result) || errorCodeOf(t, result) != string(client.CodeInvalidInput) {
		t.Fatalf("non-object arguments = %v", result)
	}
}

func TestUnknownToolIsProtocolError(t *testing.T) {
	session := newSession(t, nil)
	session.initialize()
	session.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "dmem_unknown"}})
	response, _ := session.receiveForID(2)
	rpcErr, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("response = %v", response)
	}
	if code, _ := rpcErr["code"].(float64); int(code) != codeInvalidParams {
		t.Fatalf("error = %v", rpcErr)
	}
}

func TestStructuredClientErrorIsPropagated(t *testing.T) {
	session := newSession(t, func(config *Config) {
		fake := config.Client.(*fakeClient)
		fake.commitFn = func(context.Context, client.Call, toolcontract.MemoryCommitInput) (toolcontract.MemoryCommitOutput, error) {
			return toolcontract.MemoryCommitOutput{}, &client.Error{Code: "conflict", Message: "stale revision", Retryable: true, Details: map[string]string{"memory_id": "mem-1"}}
		}
	})
	session.initialize()
	response := session.callTool(2, toolcontract.ToolRemember, map[string]any{
		"memories": []map[string]any{{"local_id": "m", "kind": "fact", "content": "c", "sensitivity": "normal", "confidence": 0.5}},
	})
	result := resultOf(t, response)
	if !isErrorOf(t, result) {
		t.Fatalf("client error was not reported: %v", result)
	}
	if code := errorCodeOf(t, result); code != "conflict" {
		t.Fatalf("error code = %s", code)
	}
	content, _ := result["content"].([]any)
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	if !strings.Contains(text, `"retryable":true`) || !strings.Contains(text, `"memory_id":"mem-1"`) {
		t.Fatalf("structured error lost fields: %s", text)
	}
}

func TestScopeResolutionFailureIsStructuredError(t *testing.T) {
	session := newSession(t, func(config *Config) {
		fake := config.Client.(*fakeClient)
		fake.ensureFn = func(context.Context, client.Call, toolcontract.ScopeEnsureInput) (toolcontract.ScopeEnsureOutput, error) {
			return toolcontract.ScopeEnsureOutput{}, &client.Error{Code: client.CodeCapabilityUnavailable, Message: "dMem endpoint is unavailable", Retryable: true}
		}
	})
	session.initialize()
	response := session.callTool(2, toolcontract.ToolRecall, map[string]any{"query": "q"})
	result := resultOf(t, response)
	if !isErrorOf(t, result) {
		t.Fatalf("scope failure was not reported: %v", result)
	}
	if code := errorCodeOf(t, result); code != string(client.CodeCapabilityUnavailable) {
		t.Fatalf("error code = %s", code)
	}
}

func TestFeedbackAndForgetMapToClient(t *testing.T) {
	session := newSession(t, nil)
	session.initialize()
	feedback := session.callTool(2, toolcontract.ToolFeedback, map[string]any{
		"memory_id": "mem-1", "revision_id": "rev-1", "kind": "used", "recall_operation_id": "op-1"})
	if isErrorOf(t, resultOf(t, feedback)) {
		t.Fatalf("feedback failed: %v", feedback)
	}
	fake := session.client(t)
	fake.mutex.Lock()
	if len(fake.feedback) != 1 || fake.feedback[0].Kind != "used" || fake.feedback[0].ScopeID != "scope-1" {
		t.Fatalf("feedback inputs = %+v", fake.feedback)
	}
	fake.mutex.Unlock()

	forget := session.callTool(3, toolcontract.ToolForget, map[string]any{
		"memory_id": "mem-1", "expected_revision_id": "rev-1", "reason": "user asked"})
	if isErrorOf(t, resultOf(t, forget)) {
		t.Fatalf("forget failed: %v", forget)
	}
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if len(fake.forgets) != 1 || fake.forgets[0].ExpectedRevisionID != "rev-1" || fake.forgets[0].ScopeID != "scope-1" {
		t.Fatalf("forget inputs = %+v", fake.forgets)
	}
}

func stringSlice(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, _ := item.(string)
		result = append(result, text)
	}
	return result
}
