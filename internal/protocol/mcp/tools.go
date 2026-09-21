package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/N0rthHXD/dMem/pkg/client"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

// contentItem is one MCP tool result content block. dMem always returns one
// text block; structured results are mirrored in structuredContent.
type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolResult is the MCP tools/call result shape.
type toolResult struct {
	Content           []contentItem   `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
}

// mcpTool is the MCP tools/list wire shape of one contract tool definition.
type mcpTool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  mcpAnnotations  `json:"annotations"`
}

type mcpAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

type toolsListResult struct {
	Tools []mcpTool `json:"tools"`
}

func (s *Server) handleToolsList() (any, *rpcError) {
	tools := make([]mcpTool, 0, len(s.tools))
	for _, definition := range s.tools {
		tools = append(tools, mcpTool{
			Name:         definition.Name,
			Title:        definition.Title,
			Description:  definition.Description,
			InputSchema:  definition.InputSchema,
			OutputSchema: definition.OutputSchema,
			Annotations: mcpAnnotations{
				ReadOnlyHint:    definition.ReadOnly,
				DestructiveHint: definition.Destructive,
				IdempotentHint:  definition.Idempotent,
				OpenWorldHint:   false,
			},
		})
	}
	return toolsListResult{Tools: tools}, nil
}

func (s *Server) handleToolsCall(ctx context.Context, request message) (any, *rpcError) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(request.Params) > 0 {
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call params are invalid"}
		}
	}
	if params.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tool name is required"}
	}
	definition, ok := s.toolIndex[params.Name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + params.Name}
	}
	callID := requestIDToken(request.ID)
	callCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	defer cancel()

	started := s.now()
	result := s.invokeTool(callCtx, definition, callID, params.Arguments)
	if s.logger != nil {
		s.logger.DebugContext(context.Background(), "mcp tool call",
			"tool", definition.Name, "is_error", result.IsError, "duration_ms", time.Since(started).Milliseconds())
	}
	return result, nil
}

// invokeTool resolves host-supplied identity, decodes the strict contract input,
// and calls the application client. Every failure is a tool execution error so
// the Agent can see the structured dMem error and correct itself.
func (s *Server) invokeTool(ctx context.Context, definition toolcontract.ToolDefinition, callID string, arguments json.RawMessage) toolResult {
	args, err := objectArguments(arguments)
	if err != nil {
		return executionError(string(client.CodeInvalidInput), "tool arguments must be a single JSON object", callID)
	}
	if definition.Name == toolcontract.ToolRemember {
		if _, present := args["source"]; present {
			return executionError("invalid_json", "source identity is supplied by the MCP server", callID)
		}
	}
	scopeID, err := s.scope.ResolveScope(ctx)
	if err != nil {
		return errorResult(client.ToContractError(err, callID))
	}
	call := client.Call{RequestID: callID, CorrelationID: s.conversationID}

	switch definition.Name {
	case toolcontract.ToolRecall:
		if contractErr := singleScope(args, "scope_ids", scopeID); contractErr != nil {
			return errorResult(*contractErr)
		}
		var input toolcontract.RecallInput
		if decodeErr := decodeArguments(toolcontract.SchemaRecallInput, args, &input); decodeErr != nil {
			return errorResult(*decodeErr)
		}
		output, err := s.client.Recall(ctx, call, input)
		if err != nil {
			return errorResult(client.ToContractError(err, callID))
		}
		return successResult(output)

	case toolcontract.ToolRemember:
		if contractErr := singleScope(args, "scope_id", scopeID); contractErr != nil {
			return errorResult(*contractErr)
		}
		occurredAt := s.now().UTC().Format(time.RFC3339)
		source, err := json.Marshal(toolcontract.Source{
			Type:           "agent_tool",
			Name:           s.sourceName,
			ConversationID: s.conversationID,
			TurnID:         s.newID(),
			ToolCallID:     callID,
			OccurredAt:     occurredAt,
		})
		if err != nil {
			return errorResult(toolcontract.ContractError{Code: string(client.CodeInternal), Message: "source identity could not be encoded", RequestID: callID})
		}
		args["source"] = source
		var input toolcontract.MemoryCommitInput
		if decodeErr := decodeArguments(toolcontract.SchemaMemoryCommitInput, args, &input); decodeErr != nil {
			return errorResult(*decodeErr)
		}
		output, err := s.client.Remember(ctx, call, input)
		if err != nil {
			return errorResult(client.ToContractError(err, callID))
		}
		return successResult(output)

	case toolcontract.ToolFeedback:
		if contractErr := singleScope(args, "scope_id", scopeID); contractErr != nil {
			return errorResult(*contractErr)
		}
		var input toolcontract.FeedbackInput
		if decodeErr := decodeArguments(toolcontract.SchemaFeedbackInput, args, &input); decodeErr != nil {
			return errorResult(*decodeErr)
		}
		output, err := s.client.Feedback(ctx, call, input)
		if err != nil {
			return errorResult(client.ToContractError(err, callID))
		}
		return successResult(output)

	case toolcontract.ToolForget:
		if contractErr := singleScope(args, "scope_id", scopeID); contractErr != nil {
			return errorResult(*contractErr)
		}
		var input toolcontract.ForgetInput
		if decodeErr := decodeArguments(toolcontract.SchemaForgetInput, args, &input); decodeErr != nil {
			return errorResult(*decodeErr)
		}
		output, err := s.client.Forget(ctx, call, input)
		if err != nil {
			return errorResult(client.ToContractError(err, callID))
		}
		return successResult(output)

	default:
		return executionError(string(client.CodeInternal), "tool is not implemented by this server", callID)
	}
}

// objectArguments normalizes absent arguments to an empty object and rejects any
// non-object payload.
func objectArguments(arguments json.RawMessage) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	if trimmed[0] != '{' {
		return nil, errNotObject
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return nil, errNotObject
	}
	return values, nil
}

// singleScope binds the configured scope: when the caller omits the scope field
// the host injects its own, and when the caller supplies one it must match.
func singleScope(args map[string]json.RawMessage, key, scopeID string) *toolcontract.ContractError {
	list := key == "scope_ids"
	raw, present := args[key]
	if !present {
		var value any = scopeID
		if list {
			value = []string{scopeID}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return &toolcontract.ContractError{Code: string(client.CodeInternal), Message: "scope identity could not be encoded"}
		}
		args[key] = encoded
		return nil
	}
	if list {
		var provided []string
		if err := json.Unmarshal(raw, &provided); err != nil {
			return &toolcontract.ContractError{Code: "invalid_json", Message: "scope_ids must be an array of strings"}
		}
		if len(provided) == 0 {
			return &toolcontract.ContractError{Code: "invalid_json", Message: "scope_ids must not be empty"}
		}
		for _, value := range provided {
			if value != scopeID {
				return scopeDenied()
			}
		}
		return nil
	}
	var provided string
	if err := json.Unmarshal(raw, &provided); err != nil {
		return &toolcontract.ContractError{Code: "invalid_json", Message: "scope_id must be a string"}
	}
	if provided != scopeID {
		return scopeDenied()
	}
	return nil
}

func scopeDenied() *toolcontract.ContractError {
	return &toolcontract.ContractError{
		Code:    "permission_denied",
		Message: "scope is not served by this MCP server",
	}
}

func decodeArguments(schema string, args map[string]json.RawMessage, target any) *toolcontract.ContractError {
	encoded, err := json.Marshal(args)
	if err != nil {
		return &toolcontract.ContractError{Code: "invalid_json", Message: "tool arguments could not be encoded"}
	}
	if err := toolcontract.DecodeStrict(schema, encoded, target); err != nil {
		return &toolcontract.ContractError{Code: "invalid_json", Message: "arguments do not match the dMem tool contract"}
	}
	return nil
}

func successResult(output any) toolResult {
	encoded, err := json.Marshal(output)
	if err != nil {
		return executionError(string(client.CodeInternal), "tool result could not be encoded", "")
	}
	return toolResult{
		Content:           []contentItem{{Type: "text", Text: string(encoded)}},
		StructuredContent: encoded,
		IsError:           false,
	}
}

func errorResult(contractErr toolcontract.ContractError) toolResult {
	encoded, err := json.Marshal(toolcontract.ErrorResponse{Error: contractErr})
	if err != nil {
		encoded = []byte(`{"error":{"code":"internal","message":"dMem request failed","request_id":"","retryable":false}}`)
	}
	return toolResult{
		Content: []contentItem{{Type: "text", Text: string(encoded)}},
		IsError: true,
	}
}

func executionError(code, message, requestID string) toolResult {
	return errorResult(toolcontract.ContractError{Code: code, Message: message, RequestID: requestID})
}

// requestIDToken derives an application request identity from a JSON-RPC id.
// Non-token identifiers are dropped so the client generates a fresh, valid one.
func requestIDToken(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(id, &text); err == nil {
		if validOpaqueToken(text) {
			return text
		}
		return ""
	}
	trimmed := string(bytes.TrimSpace(id))
	if validOpaqueToken(trimmed) {
		return trimmed
	}
	return ""
}

func validOpaqueToken(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

var errNotObject = errors.New("mcp: arguments must be a JSON object")
