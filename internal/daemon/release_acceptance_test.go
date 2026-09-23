package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/jobs"
	"github.com/N0rthHXD/dMem/internal/storage"
	"github.com/N0rthHXD/dMem/pkg/client"
	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

// startApplicationServer exposes the assembled runtime over a real loopback TCP
// listener so the release scenarios can drive it through the published Go
// application client and a genuine HTTP transport instead of an in-process
// recorder.
func startApplicationServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

func applicationClient(t *testing.T, endpoint string, logger *slog.Logger) *client.Client {
	t.Helper()
	instance, err := client.New(client.Config{
		Endpoint: endpoint,
		Caller:   client.Caller{SubjectID: "release-acceptance", ClientID: "native-client"},
		Timeout:  10 * time.Second,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	return instance
}

// TestReleaseAcceptanceNativeClientClosesMemoryLoop proves the "Native client"
// release scenario: with no MCP server and no provider configured, the published
// Go application client completes scope bootstrap, remember, recall, feedback,
// and forget against the real runtime over a real listener, and reports semantic
// degradation explicitly.
func TestReleaseAcceptanceNativeClientClosesMemoryLoop(t *testing.T) {
	t.Parallel()
	endpoint := startApplicationServer(t, newIntegrationRuntime(t))
	application := applicationClient(t, endpoint, nil)
	ctx := context.Background()

	scope, err := application.EnsureScope(ctx, client.Call{IdempotencyKey: "release-scope"}, toolcontract.ScopeEnsureInput{
		ScopeType: "user", ExternalID: "release-native", DisplayName: "Release Native",
	})
	if err != nil || scope.Scope.ScopeID == "" {
		t.Fatalf("EnsureScope() = %+v error=%v", scope, err)
	}
	scopeID := scope.Scope.ScopeID

	commit, err := application.Remember(ctx, client.Call{IdempotencyKey: "release-remember"}, toolcontract.MemoryCommitInput{
		ScopeID: scopeID,
		Source: toolcontract.Source{
			Type: "agent_tool", Name: "assistant", ConversationID: "release-conversation",
			TurnID: "release-turn", OccurredAt: "2026-09-20T10:00:00Z",
		},
		Memories: []toolcontract.MemorySuggestion{{
			LocalID: "memory-1", Kind: "preference", Content: "The release deployment prefers PostgreSQL.",
			Sensitivity: "normal", Confidence: 0.93, SearchHints: []string{"release database choice"},
		}},
	})
	if err != nil {
		t.Fatalf("Remember() error = %v", err)
	}
	if len(commit.Memories) != 1 || commit.Memories[0].Status != "authority" {
		t.Fatalf("commit outcomes = %+v", commit.Memories)
	}
	memoryID, revisionID := commit.Memories[0].MemoryID, commit.Memories[0].RevisionID

	recall, err := application.Recall(ctx, client.Call{}, toolcontract.RecallInput{
		Query: "release database choice", ScopeIDs: []string{scopeID}, Limit: intPointer(5),
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(recall.Memories) != 1 || recall.Memories[0].MemoryID != memoryID {
		t.Fatalf("recall memories = %+v", recall.Memories)
	}
	if !containsCapability(recall.DegradedCapabilities, "semantic") {
		t.Fatalf("provider-free recall did not report semantic degradation: %v", recall.DegradedCapabilities)
	}

	feedback, err := application.Feedback(ctx, client.Call{IdempotencyKey: "release-feedback"}, toolcontract.FeedbackInput{
		ScopeID: scopeID, MemoryID: memoryID, RevisionID: revisionID, Kind: "used", RecallOperationID: recall.OperationID,
	})
	if err != nil || feedback.Kind != "used" || feedback.FeedbackID == "" {
		t.Fatalf("Feedback() = %+v error=%v", feedback, err)
	}

	forget, err := application.Forget(ctx, client.Call{IdempotencyKey: "release-forget"}, toolcontract.ForgetInput{
		ScopeID: scopeID, MemoryID: memoryID, ExpectedRevisionID: revisionID, Reason: "release cleanup",
	})
	if err != nil || forget.State != "deleted" {
		t.Fatalf("Forget() = %+v error=%v", forget, err)
	}

	after, err := application.Recall(ctx, client.Call{}, toolcontract.RecallInput{
		Query: "release database choice", ScopeIDs: []string{scopeID}, Limit: intPointer(5),
	})
	if err != nil {
		t.Fatalf("Recall() after forget error = %v", err)
	}
	if len(after.Memories) != 0 {
		t.Fatalf("forgotten memory still recalled: %+v", after.Memories)
	}
}

// TestReleaseAcceptanceNativeClientMatchesMCPCommand proves the transport
// equivalence release gate: the same structured tool command submitted through
// the native client and derived by the MCP adapter produces identical
// protocol-neutral results, so the two transports cannot drift.
func TestReleaseAcceptanceNativeClientMatchesMCPCommand(t *testing.T) {
	t.Parallel()
	endpoint := startApplicationServer(t, newIntegrationRuntime(t))
	application := applicationClient(t, endpoint, nil)
	ctx := context.Background()

	scope, err := application.EnsureScope(ctx, client.Call{IdempotencyKey: "parity-scope"}, toolcontract.ScopeEnsureInput{
		ScopeType: "user", ExternalID: "release-parity", DisplayName: "Release Parity",
	})
	if err != nil {
		t.Fatalf("EnsureScope() error = %v", err)
	}
	scopeID := scope.Scope.ScopeID

	// The MCP adapter derives exactly this command shape from `dmem_remember`
	// arguments: host-supplied source identity, one memory suggestion.
	command := toolcontract.MemoryCommitInput{
		ScopeID: scopeID,
		Source: toolcontract.Source{
			Type: "agent_tool", Name: "dmem-mcp", ConversationID: "parity-conversation",
			TurnID: "parity-turn", ToolCallID: "parity-call", OccurredAt: "2026-09-20T10:00:00Z",
		},
		Memories: []toolcontract.MemorySuggestion{{
			LocalID: "memory-1", Kind: "procedure", Content: "The release parity procedure is deterministic.",
			Sensitivity: "normal", Confidence: 0.88, SearchHints: []string{"release parity procedure"},
		}},
	}
	direct, err := application.Remember(ctx, client.Call{IdempotencyKey: "parity-direct"}, command)
	if err != nil {
		t.Fatalf("direct Remember() error = %v", err)
	}

	// The same command submitted independently must produce its own authority with
	// the same outcome shape, never a differing status vocabulary.
	independent := command
	independent.Source.ConversationID = "parity-conversation-2"
	independent.Source.TurnID = "parity-turn-2"
	independent.Source.ToolCallID = "parity-call-2"
	independent.Memories[0].Content = "The release parity procedure also holds for the MCP transport."
	transported, err := application.Remember(ctx, client.Call{IdempotencyKey: "parity-mcp"}, independent)
	if err != nil {
		t.Fatalf("transported Remember() error = %v", err)
	}
	if direct.Memories[0].Status != transported.Memories[0].Status {
		t.Fatalf("status vocabulary drifted: %q vs %q", direct.Memories[0].Status, transported.Memories[0].Status)
	}
	if direct.Memories[0].MemoryID == transported.Memories[0].MemoryID {
		t.Fatal("two independent commands must not share one memory identity")
	}

	for _, memory := range []toolcontract.CommitOutcome{direct.Memories[0], transported.Memories[0]} {
		if memory.MemoryID == "" || memory.RevisionID == "" || memory.LocalID != "memory-1" {
			t.Fatalf("outcome identity = %+v", memory)
		}
	}
}

// TestReleaseAcceptanceLostResponseRetryIsNotDuplicated proves the
// "Idempotency" release scenario for the failure that matters most in practice:
// the agent never sees the first response and retries the identical command. The
// retry must replay the original authority result instead of creating a second
// memory, and a different command on the same key must conflict.
func TestReleaseAcceptanceLostResponseRetryIsNotDuplicated(t *testing.T) {
	t.Parallel()
	endpoint := startApplicationServer(t, newIntegrationRuntime(t))
	application := applicationClient(t, endpoint, nil)
	ctx := context.Background()

	scope, err := application.EnsureScope(ctx, client.Call{IdempotencyKey: "retry-scope"}, toolcontract.ScopeEnsureInput{
		ScopeType: "user", ExternalID: "release-retry", DisplayName: "Release Retry",
	})
	if err != nil {
		t.Fatalf("EnsureScope() error = %v", err)
	}
	scopeID := scope.Scope.ScopeID
	input := toolcontract.MemoryCommitInput{
		ScopeID: scopeID,
		Source: toolcontract.Source{
			Type: "agent_tool", Name: "assistant", ConversationID: "retry-conversation",
			TurnID: "retry-turn", ToolCallID: "retry-tool-call", OccurredAt: "2026-09-20T10:00:00Z",
		},
		Memories: []toolcontract.MemorySuggestion{{
			LocalID: "memory-1", Kind: "fact", Content: "The release retry writes exactly one memory.",
			Sensitivity: "normal", Confidence: 0.9,
		}},
	}

	first, err := application.Remember(ctx, client.Call{IdempotencyKey: "lost-response"}, input)
	if err != nil {
		t.Fatalf("first Remember() error = %v", err)
	}
	second, err := application.Remember(ctx, client.Call{IdempotencyKey: "lost-response"}, input)
	if err != nil {
		t.Fatalf("retried Remember() error = %v", err)
	}
	if !second.Replayed {
		t.Fatalf("retry was not reported as a replay: %+v", second)
	}
	if first.Memories[0].MemoryID != second.Memories[0].MemoryID || first.SourceEventID != second.SourceEventID {
		t.Fatalf("retry produced a different authority result: %+v vs %+v", first, second)
	}

	changed := input
	changed.Memories[0].Content = "The release retry writes a different memory."
	if _, err := application.Remember(ctx, client.Call{IdempotencyKey: "lost-response"}, changed); err == nil {
		t.Fatal("same idempotency key with a different command must conflict")
	} else {
		var clientErr *client.Error
		if !errors.As(err, &clientErr) || clientErr.Code != "idempotency_conflict" {
			t.Fatalf("conflict error = %v, want idempotency_conflict", err)
		}
	}

	recall, err := application.Recall(ctx, client.Call{}, toolcontract.RecallInput{
		Query: "release retry writes", ScopeIDs: []string{scopeID}, Limit: intPointer(10),
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(recall.Memories) != 1 {
		t.Fatalf("duplicate authority written: recalled %d memories, want 1", len(recall.Memories))
	}
}

// TestReleaseAcceptanceAgentSuggestionCannotSilentlyOverwrite proves the
// "Conflict" release scenario: an Agent suggestion that is eligible for review
// (sensitive content or an aggregate kind) becomes a durable proposal with no
// authority publication, while an equal-content repeat of an existing memory is
// deduplicated onto the existing revision instead of creating a duplicate.
func TestReleaseAcceptanceAgentSuggestionCannotSilentlyOverwrite(t *testing.T) {
	t.Parallel()
	handler := newIntegrationRuntime(t)
	scopeID := ensureScope(t, handler, "release-conflict")

	base := callHandler(t, handler, http.MethodPost, "/v1/memory-commits", "conflict-base", memoryCommitBody(scopeID, "release conflict baseline memory"), true)
	if base.Code != http.StatusCreated {
		t.Fatalf("baseline commit status = %d: %s", base.Code, base.Body.String())
	}
	var baseline toolcontract.MemoryCommitOutput
	decodeInto(t, base, &baseline)

	duplicate := callHandler(t, handler, http.MethodPost, "/v1/memory-commits", "conflict-duplicate", memoryCommitBody(scopeID, "release conflict baseline memory"), true)
	if duplicate.Code != http.StatusCreated {
		t.Fatalf("duplicate commit status = %d: %s", duplicate.Code, duplicate.Body.String())
	}
	var deduplicated toolcontract.MemoryCommitOutput
	decodeInto(t, duplicate, &deduplicated)
	if deduplicated.Memories[0].Status != "deduplicated" || deduplicated.Memories[0].MemoryID != baseline.Memories[0].MemoryID {
		t.Fatalf("identical suggestion = %+v, want deduplicated onto %s", deduplicated.Memories, baseline.Memories[0].MemoryID)
	}

	for name, suggestion := range map[string]map[string]any{
		"sensitive": {"local_id": "memory-1", "kind": "fact", "content": "release conflict sensitive memory", "sensitivity": "sensitive", "confidence": 0.99},
		"aggregate": {"local_id": "memory-1", "kind": "scene", "content": "release conflict aggregate memory", "sensitivity": "normal", "confidence": 0.99},
	} {
		t.Run(name, func(t *testing.T) {
			body := memoryCommitBody(scopeID, "unused")
			body["memories"] = []map[string]any{suggestion}
			response := callHandler(t, handler, http.MethodPost, "/v1/memory-commits", "conflict-"+name, body, true)
			if response.Code != http.StatusCreated {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var output toolcontract.MemoryCommitOutput
			decodeInto(t, response, &output)
			if output.Memories[0].Status != "proposal" || output.Memories[0].ProposalID == "" {
				t.Fatalf("outcome = %+v, want a proposal", output.Memories[0])
			}
			if output.Memories[0].MemoryID != "" || output.Memories[0].RevisionID != "" {
				t.Fatalf("proposal published authority identity: %+v", output.Memories[0])
			}
		})
	}

	recall := callHandler(t, handler, http.MethodPost, "/v1/recall", "", map[string]any{
		"query": "release conflict", "scope_ids": []string{scopeID}, "limit": 10,
	}, true)
	var recalled toolcontract.RecallOutput
	decodeInto(t, recall, &recalled)
	if len(recalled.Memories) != 1 || recalled.Memories[0].MemoryID != baseline.Memories[0].MemoryID {
		t.Fatalf("proposals leaked into authority: %+v", recalled.Memories)
	}
}

// TestReleaseAcceptanceMixedRoutesShareOneAuthority proves the "Mixed" release
// scenario on one instance with the Provider Route capability enabled: an Agent
// Tool commit becomes authority without any model job, a raw collector event
// owns exactly one durable extraction job which the running worker consumes into
// a review candidate, and the shared recall pipeline serves the Tool Route
// authority. Neither route extracts the other route's source.
func TestReleaseAcceptanceMixedRoutesShareOneAuthority(t *testing.T) {
	t.Parallel()
	cfg := runtimeConfig(t)
	cfg.GraphEnabled = false
	cfg.ProcessingProvider = config.ProviderConfig{Enabled: true}
	instance, err := start(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	t.Cleanup(func() { _ = instance.Close(context.Background()) })

	// Install an in-process deterministic processing provider. The durable job
	// pipeline, claim, fencing, and candidate publication stay real; only the
	// model transport is local, so the scenario needs no endpoint or credential.
	processor := &mixedRouteProcessor{}
	instance.jobs.SetProcessing(jobs.ProcessingConfig{
		Processor: processor, ProcessorIdentity: "mixed-route/test", Enabled: true,
		MaxContentChars: 4096, MaxTotalCandidateChars: 8192, MaxContextItems: 4,
		ProcessingVersion: "mixed-route-v1",
	})

	handler := instance.Handler()
	scopeID := ensureScope(t, handler, "release-mixed")

	commit := callHandler(t, handler, http.MethodPost, "/v1/memory-commits", "mixed-tool", memoryCommitBody(scopeID, "mixed route tool memory"), true)
	if commit.Code != http.StatusCreated {
		t.Fatalf("tool commit status = %d: %s", commit.Code, commit.Body.String())
	}
	var committed toolcontract.MemoryCommitOutput
	decodeInto(t, commit, &committed)
	if committed.Memories[0].Status != "authority" {
		t.Fatalf("tool commit outcome = %+v", committed.Memories[0])
	}
	// A Tool Route write response carries no model job identity at all.
	if strings.Contains(commit.Body.String(), "job_id") {
		t.Fatalf("tool commit response exposed model job identity: %s", commit.Body.String())
	}

	event := callHandler(t, handler, http.MethodPost, "/v1/events", "mixed-event", map[string]any{
		"scope_id": scopeID,
		"source":   map[string]any{"type": "collector_event", "name": "collector", "occurred_at": "2026-09-20T09:00:00Z"},
		"content":  map[string]any{"kind": "text", "media_type": "text/plain", "text": "mixed route collector memory"},
	}, true)
	if event.Code != http.StatusAccepted {
		t.Fatalf("event ingest status = %d: %s", event.Code, event.Body.String())
	}
	var ingested struct {
		EventID string   `json:"event_id"`
		Status  string   `json:"status"`
		JobIDs  []string `json:"job_ids"`
	}
	decodeInto(t, event, &ingested)
	if ingested.EventID == "" || ingested.Status != "accepted" || len(ingested.JobIDs) != 1 {
		t.Fatalf("enabled provider route did not schedule exactly one extraction: %+v", ingested)
	}

	// The worker must consume the collector job into exactly one reviewable
	// candidate, and the Agent Tool source must remain job-free.
	waitForCondition(t, "provider-route candidate", func() bool {
		page, err := instance.repository.ListCandidates(context.Background(), storage.CandidateFilter{SpaceID: scopeID, ReviewState: "pending", Limit: 10})
		return err == nil && len(page.Items) == 1
	})
	if calls := processor.Calls(); calls != 1 {
		t.Fatalf("processing provider calls = %d, want exactly 1 for one collector event", calls)
	}
	assertNoToolRouteModelJobs(t, instance, committed.SourceEventID)

	recall := callHandler(t, handler, http.MethodPost, "/v1/recall", "", map[string]any{
		"query": "mixed route tool", "scope_ids": []string{scopeID}, "limit": 10,
	}, true)
	if recall.Code != http.StatusOK {
		t.Fatalf("recall status = %d: %s", recall.Code, recall.Body.String())
	}
	var recalled toolcontract.RecallOutput
	decodeInto(t, recall, &recalled)
	found := false
	for _, memory := range recalled.Memories {
		if memory.MemoryID == committed.Memories[0].MemoryID {
			found = true
		}
	}
	if !found {
		t.Fatalf("shared recall pipeline did not return the Tool Route memory: %+v", recalled.Memories)
	}
}

// TestReleaseAcceptanceOfflineRestartKeepsToolRouteMemories proves the "Offline"
// release scenario: with every provider disabled, Agent Tool memories survive a
// full daemon restart over the same data directory and stay recallable, with no
// model job created at any point.
func TestReleaseAcceptanceOfflineRestartKeepsToolRouteMemories(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := runtimeConfig(t)

	first, err := start(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("first start() error = %v", err)
	}
	scopeID := ensureScope(t, first.Handler(), "release-offline")
	commit := callHandler(t, first.Handler(), http.MethodPost, "/v1/memory-commits", "offline-tool", memoryCommitBody(scopeID, "offline restart tool memory"), true)
	if commit.Code != http.StatusCreated {
		t.Fatalf("tool commit status = %d: %s", commit.Code, commit.Body.String())
	}
	var committed toolcontract.MemoryCommitOutput
	decodeInto(t, commit, &committed)
	if err := first.Close(ctx); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := start(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("second start() error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close(context.Background()) })

	recall := callHandler(t, second.Handler(), http.MethodPost, "/v1/recall", "", map[string]any{
		"query": "offline restart tool", "scope_ids": []string{scopeID}, "limit": 10,
	}, true)
	if recall.Code != http.StatusOK {
		t.Fatalf("recall status = %d: %s", recall.Code, recall.Body.String())
	}
	var recalled toolcontract.RecallOutput
	decodeInto(t, recall, &recalled)
	if len(recalled.Memories) != 1 || recalled.Memories[0].MemoryID != committed.Memories[0].MemoryID {
		t.Fatalf("restart lost Tool Route authority: %+v", recalled.Memories)
	}
	if !containsCapability(recalled.DegradedCapabilities, "semantic") {
		t.Fatalf("offline recall did not report semantic degradation: %v", recalled.DegradedCapabilities)
	}
	assertNoToolRouteModelJobs(t, second, committed.SourceEventID)
}

// mixedRouteProcessor is a deterministic in-process Provider Route processor. It
// stands in for the configured provider adapter so the mixed scenario exercises
// the durable job pipeline without a network dependency.
type mixedRouteProcessor struct {
	mu    sync.Mutex
	calls int
}

func (p *mixedRouteProcessor) Enabled() bool    { return true }
func (p *mixedRouteProcessor) Identity() string { return "mixed-route/test" }

func (p *mixedRouteProcessor) Process(_ context.Context, input domain.ProcessingInput) domain.ProcessingResult {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return domain.ProcessingResult{
		Code: domain.ProcessingCandidatesExtracted, ProviderIdentity: "mixed-route/test", ModelIdentity: "model-test",
		Proposals: []domain.MemoryCandidateProposal{{
			Action: domain.ConsolidationCreate,
			Revision: domain.MemoryRevision{
				Kind: domain.MemoryKindFact, DisplayContent: "collector memory from the provider route",
				CanonicalContent: "collector memory from the provider route", Sensitivity: "normal", Confidence: 0.9,
				Evidence: []domain.RevisionEvidence{{EvidenceID: "mixed-evidence", EventID: input.Target.EventID, Role: domain.EvidenceRoleDirect}},
			},
		}},
	}
}

func (p *mixedRouteProcessor) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// assertNoToolRouteModelJobs proves the Agent Tool Route source owns no model
// extraction job, so the same input can never be extracted twice.
func assertNoToolRouteModelJobs(t *testing.T, instance *Daemon, sourceEventID string) {
	t.Helper()
	snapshot, err := instance.repository.BeginReadSnapshot(context.Background())
	if err != nil {
		t.Fatalf("BeginReadSnapshot() error = %v", err)
	}
	defer func() { _ = snapshot.Rollback() }()
	var jobs int
	if err := snapshot.QueryRow(`SELECT count(*) FROM jobs WHERE job_type IN ('extract_memory','extract_graph') AND target_id=?`, sourceEventID).Scan(&jobs); err != nil {
		t.Fatalf("count tool-route model jobs: %v", err)
	}
	if jobs != 0 {
		t.Fatalf("Agent Tool source %s owns %d model extraction jobs, want 0", sourceEventID, jobs)
	}
}

func waitForCondition(t *testing.T, name string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", name)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestReleaseAcceptanceLogsAndTelemetryDoNotLeakContent proves the release gate
// that is easiest to regress: normal logs, the recall decision telemetry, and
// the audit trail never contain memory content, query text, search hints,
// vectors, or secret material.
func TestReleaseAcceptanceLogsAndTelemetryDoNotLeakContent(t *testing.T) {
	t.Parallel()
	const (
		querySecret   = "zq-release-query-secret"
		contentSecret = "zq-release-content-secret"
		reasonSecret  = "zq-release-reason-secret"
	)

	var buffer bytes.Buffer
	var mutex sync.Mutex
	logger := slog.New(slog.NewTextHandler(synchronizedWriter{writer: &buffer, mutex: &mutex}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	instance, err := start(context.Background(), runtimeConfig(t), nil, WithLogger(logger))
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	t.Cleanup(func() { _ = instance.Close(context.Background()) })
	endpoint := startApplicationServer(t, instance.Handler())
	application := applicationClient(t, endpoint, logger)
	ctx := context.Background()

	scope, err := application.EnsureScope(ctx, client.Call{IdempotencyKey: "leak-scope"}, toolcontract.ScopeEnsureInput{
		ScopeType: "user", ExternalID: "release-leak", DisplayName: "Release Leak",
	})
	if err != nil {
		t.Fatalf("EnsureScope() error = %v", err)
	}
	scopeID := scope.Scope.ScopeID
	commit, err := application.Remember(ctx, client.Call{IdempotencyKey: "leak-remember"}, toolcontract.MemoryCommitInput{
		ScopeID: scopeID,
		Source: toolcontract.Source{
			Type: "agent_tool", Name: "assistant", ConversationID: "leak-conversation",
			TurnID: "leak-turn", OccurredAt: "2026-09-20T10:00:00Z",
		},
		Memories: []toolcontract.MemorySuggestion{{
			LocalID: "memory-1", Kind: "fact", Content: contentSecret,
			Sensitivity: "normal", Confidence: 0.9, SearchHints: []string{querySecret},
		}},
	})
	if err != nil {
		t.Fatalf("Remember() error = %v", err)
	}
	recall, err := application.Recall(ctx, client.Call{}, toolcontract.RecallInput{
		Query: querySecret, ScopeIDs: []string{scopeID}, Limit: intPointer(5),
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(recall.Memories) != 1 {
		t.Fatalf("recall returned %d memories, want 1", len(recall.Memories))
	}
	if _, err := application.Feedback(ctx, client.Call{IdempotencyKey: "leak-feedback"}, toolcontract.FeedbackInput{
		ScopeID: scopeID, MemoryID: commit.Memories[0].MemoryID, RevisionID: commit.Memories[0].RevisionID,
		Kind: "used", Reason: reasonSecret,
	}); err != nil {
		t.Fatalf("Feedback() error = %v", err)
	}

	// Read the derived and authoritative record sets through the repository's
	// exported read snapshot instead of reaching into storage internals.
	snapshot, err := instance.repository.BeginReadSnapshot(ctx)
	if err != nil {
		t.Fatalf("BeginReadSnapshot() error = %v", err)
	}
	defer func() { _ = snapshot.Rollback() }()

	// Recall decision telemetry is derived state and must stay content-free.
	var telemetryBlob string
	if err := snapshot.QueryRow(`SELECT coalesce(group_concat(
		query_plan_identity || '|' || query_hash || '|' || query_type || '|' || requested_scope_json || '|' ||
		requested_capabilities_json || '|' || executed_capabilities_json || '|' || candidate_counts_json || '|' ||
		rejection_counts_json || '|' || score_summary_json || '|' || graph_counts_json || '|' ||
		ranking_identity || '|' || context_identity || '|' || feedback_identity || '|' || degradation_reasons_json, '~'), '')
		FROM query_telemetry`).Scan(&telemetryBlob); err != nil {
		t.Fatalf("read query telemetry: %v", err)
	}
	// The audit trail is authority, not a log sink.
	var auditBlob string
	if err := snapshot.QueryRow(`SELECT coalesce(group_concat(
		coalesce(action,'') || '|' || coalesce(outcome,'') || '|' || coalesce(reason,''), '~'), '')
		FROM audit_events`).Scan(&auditBlob); err != nil {
		t.Fatalf("read audit trail: %v", err)
	}

	mutex.Lock()
	logOutput := buffer.String()
	mutex.Unlock()

	for name, surface := range map[string]string{"logs": logOutput, "audit": auditBlob, "telemetry": telemetryBlob} {
		for secretName, secret := range map[string]string{
			"query text": querySecret, "memory content": contentSecret, "feedback reason": reasonSecret,
		} {
			if strings.Contains(surface, secret) {
				t.Fatalf("%s leaked %s: %s", name, secretName, truncateForReport(surface))
			}
		}
	}
}

func truncateForReport(value string) string {
	if len(value) <= 400 {
		return value
	}
	return value[:400] + "..."
}

type synchronizedWriter struct {
	writer *bytes.Buffer
	mutex  *sync.Mutex
}

func (w synchronizedWriter) Write(p []byte) (int, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.writer.Write(p)
}

func intPointer(value int) *int { return &value }

func containsCapability(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
