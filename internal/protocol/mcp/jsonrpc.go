package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// Supported MCP protocol versions. The first entry is the newest and is used
// when the client requests an unknown version.
const (
	ProtocolVersion20250618 = "2025-06-18"
	ProtocolVersion20241105 = "2024-11-05"
)

// SupportedProtocolVersions lists the accepted MCP revisions, newest first.
var SupportedProtocolVersions = []string{ProtocolVersion20250618, ProtocolVersion20241105}

// JSON-RPC 2.0 error codes, plus the MCP lifecycle code for requests received
// before initialization.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeNotInitialized = -32002
)

// errMessageTooLarge reports an input line beyond the configured bound.
var errMessageTooLarge = errors.New("mcp: message exceeds the size limit")

// message is one JSON-RPC envelope. ID is kept raw so it can be echoed
// verbatim, including string, number, and null identifiers.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// successResponse is a JSON-RPC success envelope. Result is never omitted, so an
// empty object result (for example ping) is still serialized as `"result":{}`.
type successResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

// errorResponse is a JSON-RPC error envelope. It never carries a result member,
// because JSON-RPC forbids including both.
type errorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   *rpcError       `json:"error"`
}

// nullID is the JSON-RPC identifier used when a request cannot be parsed.
var nullID = json.RawMessage("null")

// writeSuccess writes one success envelope.
func (w *lockedWriter) writeSuccess(id json.RawMessage, result any) {
	_ = w.write(successResponse{JSONRPC: "2.0", ID: responseID(id), Result: result})
}

// writeError writes one error envelope.
func (w *lockedWriter) writeError(id json.RawMessage, code int, message string, data json.RawMessage) {
	_ = w.write(errorResponse{JSONRPC: "2.0", ID: responseID(id), Error: &rpcError{Code: code, Message: message, Data: data}})
}

// readMessage reads one newline-delimited JSON-RPC message under a hard byte
// limit. An over-long line is drained so the connection stays usable.
func readMessage(reader *bufio.Reader, max int) ([]byte, error) {
	var buffer []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(buffer)+len(chunk) > max {
			if errors.Is(err, bufio.ErrBufferFull) {
				for errors.Is(err, bufio.ErrBufferFull) {
					_, err = reader.ReadSlice('\n')
				}
			}
			return nil, errMessageTooLarge
		}
		buffer = append(buffer, chunk...)
		switch {
		case err == nil:
			return trimLineEnding(buffer), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buffer) > 0:
			return trimLineEnding(buffer), nil
		default:
			return nil, err
		}
	}
}

func trimLineEnding(value []byte) []byte {
	value = bytes.TrimSuffix(value, []byte("\n"))
	return bytes.TrimSuffix(value, []byte("\r"))
}

// lockedWriter serializes concurrent message writes. Serve dispatches requests
// concurrently so cancellation can be observed while one call is in flight.
type lockedWriter struct {
	mutex  sync.Mutex
	writer io.Writer
}

func newLockedWriter(writer io.Writer) *lockedWriter {
	return &lockedWriter{writer: writer}
}

func (w *lockedWriter) write(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	w.mutex.Lock()
	defer w.mutex.Unlock()
	_, err = w.writer.Write(encoded)
	return err
}
