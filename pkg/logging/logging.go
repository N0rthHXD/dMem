package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

const (
	ComponentDMEMD  = "dmemd"
	ComponentDaemon = "daemon"
	ComponentHTTP   = "http"
	ComponentJobs   = "jobs"
	ComponentServer = "server"
	ComponentMCP    = "mcp"
	ComponentClient = "client"
)

const (
	FieldComponent = "component"
	FieldError     = "error"
	FieldOperation = "operation"
)

func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q", value)
	}
}

func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

func WithComponent(logger *slog.Logger, component string) *slog.Logger {
	if logger == nil {
		return nil
	}
	return logger.With(FieldComponent, component)
}
