package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/pkg/client"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

// fakeClient is a deterministic application client double. It records the
// protocol-neutral commands the MCP adapter produced.
type fakeClient struct {
	mutex    sync.Mutex
	recalls  []toolcontract.RecallInput
	commits  []toolcontract.MemoryCommitInput
	feedback []toolcontract.FeedbackInput
	forgets  []toolcontract.ForgetInput
	ensures  []toolcontract.ScopeEnsureInput
	calls    []client.Call

	recallFn  func(context.Context, client.Call, toolcontract.RecallInput) (toolcontract.RecallOutput, error)
	commitFn  func(context.Context, client.Call, toolcontract.MemoryCommitInput) (toolcontract.MemoryCommitOutput, error)
	feedbackF func(context.Context, client.Call, toolcontract.FeedbackInput) (toolcontract.FeedbackOutput, error)
	forgetFn  func(context.Context, client.Call, toolcontract.ForgetInput) (toolcontract.ForgetOutput, error)
	ensureFn  func(context.Context, client.Call, toolcontract.ScopeEnsureInput) (toolcontract.ScopeEnsureOutput, error)
}

func (f *fakeClient) Recall(ctx context.Context, call client.Call, input toolcontract.RecallInput) (toolcontract.RecallOutput, error) {
	f.record(call)
	f.mutex.Lock()
	f.recalls = append(f.recalls, input)
	fn := f.recallFn
	f.mutex.Unlock()
	if fn != nil {
		return fn(ctx, call, input)
	}
	return toolcontract.RecallOutput{OperationID: "recall-op", Memories: []toolcontract.RecalledMemory{}, DegradedCapabilities: []string{"semantic"}, NextCursor: nil}, nil
}

func (f *fakeClient) Remember(ctx context.Context, call client.Call, input toolcontract.MemoryCommitInput) (toolcontract.MemoryCommitOutput, error) {
	f.record(call)
	f.mutex.Lock()
	f.commits = append(f.commits, input)
	fn := f.commitFn
	f.mutex.Unlock()
	if fn != nil {
		return fn(ctx, call, input)
	}
	return toolcontract.MemoryCommitOutput{
		WriteMetadata: toolcontract.WriteMetadata{OperationID: "commit-op", Replayed: false},
		SourceEventID: "event-1",
		Memories:      []toolcontract.CommitOutcome{{LocalID: "m", Status: "authority", MemoryID: "mem-1", RevisionID: "rev-1"}},
		Entities:      []toolcontract.CommitOutcome{}, Facts: []toolcontract.CommitOutcome{}, Relations: []toolcontract.CommitOutcome{},
	}, nil
}

func (f *fakeClient) Feedback(ctx context.Context, call client.Call, input toolcontract.FeedbackInput) (toolcontract.FeedbackOutput, error) {
	f.record(call)
	f.mutex.Lock()
	f.feedback = append(f.feedback, input)
	fn := f.feedbackF
	f.mutex.Unlock()
	if fn != nil {
		return fn(ctx, call, input)
	}
	return toolcontract.FeedbackOutput{WriteMetadata: toolcontract.WriteMetadata{OperationID: "feedback-op"}, FeedbackID: "fb-1",
		MemoryID: input.MemoryID, RevisionID: input.RevisionID, Kind: input.Kind}, nil
}

func (f *fakeClient) Forget(ctx context.Context, call client.Call, input toolcontract.ForgetInput) (toolcontract.ForgetOutput, error) {
	f.record(call)
	f.mutex.Lock()
	f.forgets = append(f.forgets, input)
	fn := f.forgetFn
	f.mutex.Unlock()
	if fn != nil {
		return fn(ctx, call, input)
	}
	return toolcontract.ForgetOutput{WriteMetadata: toolcontract.WriteMetadata{OperationID: "forget-op"}, MemoryID: input.MemoryID,
		RevisionID: input.ExpectedRevisionID, State: "deleted"}, nil
}

func (f *fakeClient) EnsureScope(ctx context.Context, call client.Call, input toolcontract.ScopeEnsureInput) (toolcontract.ScopeEnsureOutput, error) {
	f.record(call)
	f.mutex.Lock()
	f.ensures = append(f.ensures, input)
	fn := f.ensureFn
	f.mutex.Unlock()
	if fn != nil {
		return fn(ctx, call, input)
	}
	return toolcontract.ScopeEnsureOutput{WriteMetadata: toolcontract.WriteMetadata{OperationID: "ensure-op"},
		Scope: toolcontract.Scope{ScopeID: "scope-1", ScopeType: input.ScopeType, ExternalID: input.ExternalID,
			DisplayName: input.DisplayName, CreatedAt: "2026-09-20T10:00:00Z"}}, nil
}

func (f *fakeClient) record(call client.Call) {
	f.mutex.Lock()
	f.calls = append(f.calls, call)
	f.mutex.Unlock()
}

func (f *fakeClient) lastCommit(t *testing.T) toolcontract.MemoryCommitInput {
	t.Helper()
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if len(f.commits) == 0 {
		t.Fatal("no memory commit was issued")
	}
	return f.commits[len(f.commits)-1]
}

func (f *fakeClient) lastRecall(t *testing.T) toolcontract.RecallInput {
	t.Helper()
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if len(f.recalls) == 0 {
		t.Fatal("no recall was issued")
	}
	return f.recalls[len(f.recalls)-1]
}

func (f *fakeClient) ensureCount() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return len(f.ensures)
}

func (f *fakeClient) recallCount() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return len(f.recalls)
}

func (f *fakeClient) commitCount() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return len(f.commits)
}

func (f *fakeClient) lastCall(t *testing.T) client.Call {
	t.Helper()
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no application call was recorded")
	}
	return f.calls[len(f.calls)-1]
}

// session drives one stdio MCP connection in-process.
type session struct {
	t         *testing.T
	fake      *fakeClient
	input     *io.PipeWriter
	output    *io.PipeWriter
	responses chan map[string]any
	done      chan error
	cancel    context.CancelFunc
}

func (s *session) client(t *testing.T) *fakeClient {
	t.Helper()
	if s.fake == nil {
		t.Fatal("session has no fake client")
	}
	return s.fake
}

func newSession(t *testing.T, mutate func(*Config)) *session {
	t.Helper()
	fake := &fakeClient{}
	config := Config{
		Name:    "dmem-mcp",
		Title:   "dMem memory",
		Version: "test",
		Client:  fake,
		Scope:   &ConfiguredScope{Client: fake, ScopeType: "project", ExternalID: "external-1"},
		Now:     func() time.Time { return time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC) },
		NewID:   sequenceIDs(),
	}
	if mutate != nil {
		mutate(&config)
	}
	instance := startSession(t, config)
	instance.fake = fake
	return instance
}

// startSession drives one stdio MCP connection over in-process pipes.
func startSession(t *testing.T, config Config) *session {
	t.Helper()
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, inReader, outWriter) }()
	responses := make(chan map[string]any, 32)
	go func() {
		reader := bufio.NewReader(outReader)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				var value map[string]any
				if json.Unmarshal(line, &value) == nil {
					responses <- value
				}
			}
			if err != nil {
				close(responses)
				return
			}
		}
	}()
	instance := &session{t: t, input: inWriter, output: outWriter, responses: responses, done: done, cancel: cancel}
	t.Cleanup(func() {
		_ = inWriter.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			cancel()
			<-done
		}
		_ = outWriter.Close()
	})
	return instance
}

func sequenceIDs() func() string {
	var mutex sync.Mutex
	next := 0
	return func() string {
		mutex.Lock()
		defer mutex.Unlock()
		next++
		return fmt.Sprintf("generated-%d", next)
	}
}

func (s *session) sendRaw(value string) {
	s.t.Helper()
	if _, err := io.WriteString(s.input, value+"\n"); err != nil {
		s.t.Fatalf("write message: %v", err)
	}
}

func (s *session) send(value any) {
	s.t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		s.t.Fatalf("encode message: %v", err)
	}
	s.sendRaw(string(encoded))
}

func (s *session) receive() map[string]any {
	s.t.Helper()
	select {
	case response, ok := <-s.responses:
		if !ok {
			s.t.Fatal("MCP server closed the output stream")
		}
		return response
	case <-time.After(3 * time.Second):
		s.t.Fatal("timed out waiting for an MCP response")
		return nil
	}
}

// receiveForID reads responses until the one with the given JSON-RPC id
// arrives, returning every id seen on the way so tests can prove that a
// cancelled request produced nothing.
func (s *session) receiveForID(id any) (map[string]any, []any) {
	s.t.Helper()
	var seen []any
	deadline := time.After(3 * time.Second)
	for {
		select {
		case response, ok := <-s.responses:
			if !ok {
				s.t.Fatalf("MCP server closed the output stream while waiting for id %v", id)
			}
			seen = append(seen, response["id"])
			if fmt.Sprint(response["id"]) == fmt.Sprint(id) {
				return response, seen
			}
		case <-deadline:
			s.t.Fatalf("timed out waiting for id %v (saw %v)", id, seen)
		}
	}
}

func (s *session) initialize() map[string]any {
	s.t.Helper()
	s.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": ProtocolVersion20250618, "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "test-host", "version": "1"}}})
	response, _ := s.receiveForID(1)
	s.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return response
}

func (s *session) callTool(id int, name string, arguments any) map[string]any {
	s.t.Helper()
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments}})
	response, _ := s.receiveForID(id)
	return response
}

func resultOf(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no result: %v", response)
	}
	return result
}

func structuredOf(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("result has no structured content: %v", result)
	}
	return structured
}

func isErrorOf(t *testing.T, result map[string]any) bool {
	t.Helper()
	value, ok := result["isError"].(bool)
	if !ok {
		t.Fatalf("result has no isError flag: %v", result)
	}
	return value
}

func errorCodeOf(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("result has no content: %v", result)
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content block = %v", content[0])
	}
	text, _ := block["text"].(string)
	var payload struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("error content is not a contract error: %q", text)
	}
	return payload.Error.Code
}
