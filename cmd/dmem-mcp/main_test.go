package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func emptyEnv(string) string { return "" }

func TestRunUsageErrors(t *testing.T) {
	cases := map[string]struct {
		args []string
		env  map[string]string
	}{
		"missing scope":         {args: []string{"--caller-client-id", "dmem-mcp"}},
		"missing scope type":    {args: []string{"--scope-external-id", "project-1"}},
		"bad integration mode":  {args: []string{"--scope-type", "project", "--scope-external-id", "p", "--integration-mode", "sometimes"}},
		"bad log level":         {args: []string{"--scope-type", "project", "--scope-external-id", "p", "--log-level", "loud"}},
		"bad timeout":           {args: []string{"--scope-type", "project", "--scope-external-id", "p", "--request-timeout", "0s"}},
		"bad endpoint":          {args: []string{"--scope-type", "project", "--scope-external-id", "p", "--endpoint", "ftp://host"}},
		"unexpected positional": {args: []string{"--scope-type", "project", "--scope-external-id", "p", "extra"}},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			getenv := emptyEnv
			if testCase.env != nil {
				getenv = func(key string) string { return testCase.env[key] }
			}
			err := run(context.Background(), testCase.args, getenv, strings.NewReader(""), io.Discard, io.Discard)
			if !errors.Is(err, errUsage) {
				t.Fatalf("run() error = %v, want usage error", err)
			}
		})
	}
}

func TestRunReadsConfigurationFromEnvironment(t *testing.T) {
	env := map[string]string{
		"DMEM_SCOPE_TYPE":        "project",
		"DMEM_SCOPE_EXTERNAL_ID": "project-1",
		"DMEM_ENDPOINT":          "http://127.0.0.1:1",
	}
	err := run(context.Background(), []string{"--version"}, func(key string) string { return env[key] }, strings.NewReader(""), io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunPrintsVersion(t *testing.T) {
	var output strings.Builder
	err := run(context.Background(), []string{"--version", "--scope-type", "project", "--scope-external-id", "p"},
		emptyEnv, strings.NewReader(""), &output, io.Discard)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if strings.TrimSpace(output.String()) != version {
		t.Fatalf("version output = %q", output.String())
	}
}

func TestRunServesMCPUntilInputEnds(t *testing.T) {
	input, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--scope-type", "project", "--scope-external-id", "project-1",
			"--endpoint", "http://127.0.0.1:1"}, emptyEnv, input, outputWriter, io.Discard)
	}()

	responses := make(chan map[string]any, 8)
	go func() {
		reader := bufio.NewReader(outputReader)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				var value map[string]any
				if json.Unmarshal(line, &value) == nil {
					responses <- value
				}
			}
			if err != nil {
				return
			}
		}
	}()

	initialize, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-06-18"}})
	if _, err := inputWriter.Write(append(initialize, '\n')); err != nil {
		t.Fatal(err)
	}
	if response := waitForID(t, responses, 1); response["result"] == nil {
		t.Fatalf("initialize response = %v", response)
	}
	list, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if _, err := inputWriter.Write(append(list, '\n')); err != nil {
		t.Fatal(err)
	}
	result, ok := waitForID(t, responses, 2)["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list result = %v", result)
	}
	if tools, _ := result["tools"].([]any); len(tools) != 4 {
		t.Fatalf("tools = %v", result["tools"])
	}

	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after stdin closed")
	}
	_ = outputWriter.Close()
}

func waitForID(t *testing.T, responses <-chan map[string]any, id int) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case response, ok := <-responses:
			if !ok {
				t.Fatalf("output closed while waiting for id %d", id)
			}
			if value, _ := response["id"].(float64); int(value) == id {
				return response
			}
		case <-deadline:
			t.Fatalf("timed out waiting for id %d", id)
		}
	}
}
