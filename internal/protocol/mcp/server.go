package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/N0rthHXD/dMem/pkg/client"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

const (
	// DefaultRequestTimeout bounds one tool call, including application retries.
	DefaultRequestTimeout = 30 * time.Second
	// DefaultMaxMessageBytes bounds one inbound JSON-RPC message.
	DefaultMaxMessageBytes = 2 << 20
	// DefaultSourceName is the provenance name used when none is configured.
	DefaultSourceName = "dmem-mcp"
)

// Client is the consumer-owned application client port. It is intentionally the
// public pkg/client method set so the MCP adapter cannot reach storage, jobs, or
// provider implementations.
type Client interface {
	Recall(context.Context, client.Call, toolcontract.RecallInput) (toolcontract.RecallOutput, error)
	Remember(context.Context, client.Call, toolcontract.MemoryCommitInput) (toolcontract.MemoryCommitOutput, error)
	Feedback(context.Context, client.Call, toolcontract.FeedbackInput) (toolcontract.FeedbackOutput, error)
	Forget(context.Context, client.Call, toolcontract.ForgetInput) (toolcontract.ForgetOutput, error)
	EnsureScope(context.Context, client.Call, toolcontract.ScopeEnsureInput) (toolcontract.ScopeEnsureOutput, error)
}

// Config configures one stdio MCP server.
type Config struct {
	// Name is the MCP serverInfo name.
	Name string
	// Title is the optional serverInfo display title.
	Title string
	// Version is the MCP serverInfo version.
	Version string
	// Client is the application client. It is required.
	Client Client
	// Scope resolves the configured default scope. It is required.
	Scope ScopeResolver
	// Tools overrides the advertised tool definitions. The default derives from
	// the versioned v1 contract with host-provided scope and source identity.
	Tools []toolcontract.ToolDefinition
	// SourceName is the provenance name recorded on Agent Tool memory commits.
	SourceName string
	// ConversationID is the stable provenance conversation identity for this MCP
	// session. It is generated when empty.
	ConversationID string
	// IntegrationMode selects the Agent memory policy prompt.
	IntegrationMode toolcontract.IntegrationMode
	// Instructions overrides the initialize response instructions.
	Instructions string
	// RequestTimeout bounds one tool call. Zero uses DefaultRequestTimeout.
	RequestTimeout time.Duration
	// MaxMessageBytes bounds one inbound message. Zero uses
	// DefaultMaxMessageBytes.
	MaxMessageBytes int
	// Logger receives lifecycle metadata only; tool arguments, query text, and
	// memory content are never logged. A nil logger disables logging.
	Logger *slog.Logger
	// Now overrides the clock in tests.
	Now func() time.Time
	// NewID overrides identity generation in tests.
	NewID func() string
}

// Server is a stdio MCP adapter over the dMem Agent Tool application client.
type Server struct {
	name            string
	title           string
	version         string
	client          Client
	scope           ScopeResolver
	tools           []toolcontract.ToolDefinition
	toolIndex       map[string]toolcontract.ToolDefinition
	sourceName      string
	conversationID  string
	instructions    string
	requestTimeout  time.Duration
	maxMessageBytes int
	logger          *slog.Logger
	now             func() time.Time
	newID           func() string

	stateMutex  sync.Mutex
	initialized bool

	inFlight *inFlightRegistry
}

// New validates the configuration and returns a stdio MCP server.
func New(config Config) (*Server, error) {
	if config.Client == nil {
		return nil, errors.New("mcp: application client is required")
	}
	if config.Scope == nil {
		return nil, errors.New("mcp: scope resolver is required")
	}
	tools := config.Tools
	if tools == nil {
		derived, err := toolcontract.V1ToolDefinitions(toolcontract.ToolDefinitionOptions{DefaultScope: true, HostSource: true})
		if err != nil {
			return nil, err
		}
		tools = derived
	}
	if len(tools) == 0 {
		return nil, errors.New("mcp: at least one tool definition is required")
	}
	index := make(map[string]toolcontract.ToolDefinition, len(tools))
	for _, tool := range tools {
		if tool.Name == "" {
			return nil, errors.New("mcp: tool name is required")
		}
		if _, exists := index[tool.Name]; exists {
			return nil, fmt.Errorf("mcp: duplicate tool %q", tool.Name)
		}
		index[tool.Name] = tool
	}
	name := config.Name
	if name == "" {
		name = DefaultSourceName
	}
	version := config.Version
	if version == "" {
		version = "dev"
	}
	sourceName := config.SourceName
	if sourceName == "" {
		sourceName = DefaultSourceName
	}
	timeout := config.RequestTimeout
	if timeout == 0 {
		timeout = DefaultRequestTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return nil, errors.New("mcp: request timeout must be between zero and one hour")
	}
	maxMessageBytes := config.MaxMessageBytes
	if maxMessageBytes == 0 {
		maxMessageBytes = DefaultMaxMessageBytes
	}
	if maxMessageBytes < 4096 {
		return nil, errors.New("mcp: max message bytes must be at least 4096")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	newID := config.NewID
	if newID == nil {
		newID = generateID
	}
	conversationID := config.ConversationID
	if conversationID == "" {
		conversationID = "mcp-" + newID()
	}
	instructions := config.Instructions
	if instructions == "" {
		mode := config.IntegrationMode
		if mode == "" {
			mode = toolcontract.IntegrationOpportunistic
		}
		if _, err := toolcontract.ParseIntegrationMode(string(mode)); err != nil {
			return nil, err
		}
		instructions = toolcontract.MemoryPolicyPrompt(mode)
	}
	return &Server{
		name:            name,
		title:           config.Title,
		version:         version,
		client:          config.Client,
		scope:           config.Scope,
		tools:           tools,
		toolIndex:       index,
		sourceName:      sourceName,
		conversationID:  conversationID,
		instructions:    instructions,
		requestTimeout:  timeout,
		maxMessageBytes: maxMessageBytes,
		logger:          config.Logger,
		now:             now,
		newID:           newID,
		inFlight:        newInFlightRegistry(),
	}, nil
}

// Tools returns the advertised tool definitions.
func (s *Server) Tools() []toolcontract.ToolDefinition {
	return append([]toolcontract.ToolDefinition(nil), s.tools...)
}

// Serve reads newline-delimited JSON-RPC messages from input and writes
// responses to output until input ends or ctx is canceled. Requests are handled
// concurrently so cancellation notifications can interrupt an in-flight call.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()

	reader := bufio.NewReader(input)
	writer := newLockedWriter(output)
	var workers sync.WaitGroup

	for {
		line, err := readMessage(reader, s.maxMessageBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, errMessageTooLarge) {
				writer.writeError(nullID, codeParseError, "message exceeds the configured size limit", nil)
				continue
			}
			workers.Wait()
			return err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		s.handleLine(sessionCtx, writer, &workers, line)
	}
	workers.Wait()
	return nil
}

// handleLine decodes one message and either answers it inline (notifications and
// malformed input) or dispatches it to a worker.
func (s *Server) handleLine(ctx context.Context, writer *lockedWriter, workers *sync.WaitGroup, line []byte) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		writer.writeError(nullID, codeInvalidRequest, "JSON-RPC batches are not supported", nil)
		return
	}
	var request message
	if err := json.Unmarshal(trimmed, &request); err != nil {
		writer.writeError(nullID, codeParseError, "invalid JSON", nil)
		return
	}
	if request.JSONRPC != "2.0" {
		writer.writeError(request.ID, codeInvalidRequest, "jsonrpc must be 2.0", nil)
		return
	}
	if request.Method == "" {
		// A JSON-RPC response from the client. dMem never issues server-to-client
		// requests, so it is ignored rather than reported as a protocol error.
		return
	}
	if strings.HasPrefix(request.Method, "notifications/") {
		s.handleNotification(ctx, request)
		return
	}
	if len(request.ID) == 0 || string(request.ID) == "null" {
		writer.writeError(nullID, codeInvalidRequest, "request id is required", nil)
		return
	}

	key := string(request.ID)
	callCtx, cancel := context.WithCancel(ctx)
	s.inFlight.add(key, cancel)

	workers.Add(1)
	go func() {
		defer workers.Done()
		defer cancel()
		result, rpcErr := s.dispatch(callCtx, request)
		if s.inFlight.finish(key) {
			// The client canceled the request; MCP sends no response.
			return
		}
		if rpcErr != nil {
			writer.writeError(request.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
			return
		}
		writer.writeSuccess(request.ID, result)
	}()
}

func (s *Server) dispatch(ctx context.Context, request message) (any, *rpcError) {
	// initialize and ping are the only requests allowed before the handshake
	// completes; everything else reports the MCP not-initialized lifecycle error.
	switch request.Method {
	case "initialize":
		return s.handleInitialize(request)
	case "ping":
		return map[string]any{}, nil
	}
	if !s.ready() {
		return nil, &rpcError{Code: codeNotInitialized, Message: "server is not initialized"}
	}
	switch request.Method {
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolsCall(ctx, request)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + request.Method}
	}
}

func (s *Server) handleNotification(ctx context.Context, request message) {
	switch request.Method {
	case "notifications/initialized":
		s.stateMutex.Lock()
		s.initialized = true
		s.stateMutex.Unlock()
	case "notifications/cancelled":
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
			Reason    string          `json:"reason"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || len(params.RequestID) == 0 {
			return
		}
		s.inFlight.cancelRequest(string(params.RequestID))
	}
}

func (s *Server) ready() bool {
	s.stateMutex.Lock()
	defer s.stateMutex.Unlock()
	return s.initialized
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

type initializeResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    initializeCapabilities `json:"capabilities"`
	ServerInfo      initializeServerInfo   `json:"serverInfo"`
	Instructions    string                 `json:"instructions,omitempty"`
}

type initializeCapabilities struct {
	Tools toolsCapability `json:"tools"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type initializeServerInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

func (s *Server) handleInitialize(request message) (any, *rpcError) {
	var params initializeParams
	if len(request.Params) > 0 {
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "initialize params are invalid"}
		}
	}
	if params.ProtocolVersion == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "protocolVersion is required",
			Data: supportedVersionsData("")}
	}
	negotiated := negotiateProtocolVersion(params.ProtocolVersion)
	if s.logger != nil {
		s.logger.DebugContext(context.Background(), "mcp initialize",
			"client_name", params.ClientInfo.Name, "client_version", params.ClientInfo.Version,
			"requested_protocol_version", params.ProtocolVersion, "protocol_version", negotiated)
	}
	s.stateMutex.Lock()
	s.initialized = true
	s.stateMutex.Unlock()
	return initializeResult{
		ProtocolVersion: negotiated,
		Capabilities:    initializeCapabilities{Tools: toolsCapability{ListChanged: false}},
		ServerInfo:      initializeServerInfo{Name: s.name, Title: s.title, Version: s.version},
		Instructions:    s.instructions,
	}, nil
}

func negotiateProtocolVersion(requested string) string {
	for _, supported := range SupportedProtocolVersions {
		if requested == supported {
			return requested
		}
	}
	return SupportedProtocolVersions[0]
}

func supportedVersionsData(requested string) json.RawMessage {
	encoded, err := json.Marshal(struct {
		Supported []string `json:"supported"`
		Requested string   `json:"requested"`
	}{Supported: SupportedProtocolVersions, Requested: requested})
	if err != nil {
		return nil
	}
	return encoded
}

func responseID(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nullID
	}
	return value
}
