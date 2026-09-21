// Command dmem-mcp is the stdio Model Context Protocol server for the dMem
// Agent Tool Route.
//
// It needs no model or embedding configuration: it translates the four dMem
// tools onto the application client, injects the configured caller and scope
// identity, and streams results back over stdin/stdout. It never calls a model,
// never uses MCP sampling, and never runs a background job worker.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/N0rthHXD/dMem/internal/protocol/mcp"
	"github.com/N0rthHXD/dMem/pkg/client"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

var version = "dev"

var errUsage = errors.New("usage error")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// run is the testable entry point. It reads MCP messages from stdin and writes
// responses to stdout; diagnostics go to stderr only.
func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("dmem-mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("endpoint", envOr(getenv, "DMEM_ENDPOINT", "http://127.0.0.1:7373"), "dMem application base URL")
	callerSubject := flags.String("caller-subject-id", envOr(getenv, "DMEM_CALLER_SUBJECT_ID", ""), "dMem caller subject identity")
	callerClient := flags.String("caller-client-id", envOr(getenv, "DMEM_CALLER_CLIENT_ID", "dmem-mcp"), "dMem caller client identity")
	scopeType := flags.String("scope-type", envOr(getenv, "DMEM_SCOPE_TYPE", ""), "configured default scope type")
	scopeExternalID := flags.String("scope-external-id", envOr(getenv, "DMEM_SCOPE_EXTERNAL_ID", ""), "configured default scope external id")
	scopeDisplayName := flags.String("scope-display-name", envOr(getenv, "DMEM_SCOPE_DISPLAY_NAME", ""), "configured default scope display name")
	scopeParentID := flags.String("scope-parent-id", envOr(getenv, "DMEM_SCOPE_PARENT_ID", ""), "optional parent scope id")
	sourceName := flags.String("source-name", envOr(getenv, "DMEM_SOURCE_NAME", "assistant"), "provenance source name recorded on memory commits")
	integrationMode := flags.String("integration-mode", envOr(getenv, "DMEM_INTEGRATION_MODE", string(toolcontract.IntegrationOpportunistic)), "Agent memory policy: opportunistic or checkpoint")
	requestTimeout := flags.Duration("request-timeout", envDuration(getenv, "DMEM_REQUEST_TIMEOUT", 30*time.Second), "per tool call timeout")
	logLevel := flags.String("log-level", envOr(getenv, "DMEM_LOG_LEVEL", "info"), "stderr log level: debug, info, warn, or error")
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("%w: %s", errUsage, usage())
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return nil
	}
	if strings.TrimSpace(*scopeType) == "" || strings.TrimSpace(*scopeExternalID) == "" {
		return fmt.Errorf("%w: --scope-type and --scope-external-id are required", errUsage)
	}
	if *requestTimeout <= 0 || *requestTimeout > time.Hour {
		return fmt.Errorf("%w: --request-timeout must be between zero and one hour", errUsage)
	}
	mode, err := toolcontract.ParseIntegrationMode(*integrationMode)
	if err != nil {
		return fmt.Errorf("%w: %s", errUsage, err.Error())
	}
	level, err := parseLogLevel(*logLevel)
	if err != nil {
		return fmt.Errorf("%w: %s", errUsage, err.Error())
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	applicationClient, err := client.New(client.Config{
		Endpoint: *endpoint,
		Caller:   client.Caller{SubjectID: *callerSubject, ClientID: *callerClient},
		Timeout:  *requestTimeout,
		Logger:   logger,
	})
	if err != nil {
		return fmt.Errorf("%w: %s", errUsage, err.Error())
	}
	var parent *string
	if strings.TrimSpace(*scopeParentID) != "" {
		value := strings.TrimSpace(*scopeParentID)
		parent = &value
	}
	server, err := mcp.New(mcp.Config{
		Name:    "dmem-mcp",
		Title:   "dMem memory",
		Version: version,
		Client:  applicationClient,
		Scope: &mcp.ConfiguredScope{
			Client:      applicationClient,
			ScopeType:   strings.TrimSpace(*scopeType),
			ExternalID:  strings.TrimSpace(*scopeExternalID),
			DisplayName: strings.TrimSpace(*scopeDisplayName),
			ParentID:    parent,
		},
		SourceName:      *sourceName,
		IntegrationMode: mode,
		RequestTimeout:  *requestTimeout,
		Logger:          logger,
	})
	if err != nil {
		return fmt.Errorf("%w: %s", errUsage, err.Error())
	}

	logger.Info("dmem-mcp ready", "endpoint", *endpoint, "scope_external_id", strings.TrimSpace(*scopeExternalID), "integration_mode", string(mode))
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, stdin, stdout) }()
	select {
	case err := <-served:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	case <-ctx.Done():
		if closer, ok := stdin.(io.Closer); ok {
			_ = closer.Close()
		}
		return nil
	}
}

func usage() string {
	return "dmem-mcp --scope-type TYPE --scope-external-id ID [--endpoint URL] [--caller-subject-id ID] [--caller-client-id ID] [--source-name NAME] [--integration-mode opportunistic|checkpoint] [--request-timeout DURATION] [--log-level LEVEL]"
}

func envOr(getenv func(string) string, key, fallback string) string {
	if getenv == nil {
		return fallback
	}
	if value := getenv(key); value != "" {
		return value
	}
	return fallback
}

func envDuration(getenv func(string) string, key string, fallback time.Duration) time.Duration {
	value := envOr(getenv, key, "")
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseLogLevel(value string) (slog.Level, error) {
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
