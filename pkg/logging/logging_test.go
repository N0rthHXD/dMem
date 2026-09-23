package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"DEBUG":   slog.LevelDebug,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for value, want := range cases {
		got, err := ParseLevel(value)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Fatal("ParseLevel accepted an unknown level")
	}
}

func TestNewWritesTextWithComponent(t *testing.T) {
	var buffer bytes.Buffer
	logger := WithComponent(New(&buffer, slog.LevelInfo), ComponentHTTP)
	logger.Info("request", "status", 200)
	output := buffer.String()
	for _, want := range []string{"level=INFO", "msg=request", "component=http", "status=200"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output %q missing %q", output, want)
		}
	}
	if WithComponent(nil, ComponentHTTP) != nil {
		t.Fatal("WithComponent(nil) returned a logger")
	}
}
