package retrieval

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
	core "github.com/N0rthHXD/dMem/internal/retrieval"
)

type fakeRecallStore struct {
	params        RecallParams
	response      domain.SearchResponse
	err           error
	status        CapabilityStatus
	statusRequest CapabilityRequest
	errors        []error
	calls         int
	respectCancel bool
	referenceErr  error
	statusErr     error
	telemetryErr  error
	telemetry     TelemetryRecord
}

func (f *fakeRecallStore) RecordRecallTelemetry(_ context.Context, record TelemetryRecord) error {
	f.telemetry = record
	return f.telemetryErr
}

type fakeQueryEnricher struct{ calls int }

func fakePorts(store *fakeRecallStore) Ports {
	return Ports{ReferenceTime: store, Capabilities: store, Candidates: store, Telemetry: store}
}

func (f *fakeQueryEnricher) EnrichQuery(ctx context.Context, query string) (core.QueryEnrichment, bool, error) {
	f.calls++
	return core.QueryEnrichment{QuestionType: core.QuestionRelational}, true, nil
}

func TestRecallPlanningEnrichmentRequiresAllowance(t *testing.T) {
	store := &fakeRecallStore{}
	service := NewService(fakePorts(store), recallDefaults())
	enricher := &fakeQueryEnricher{}
	for _, allowed := range []bool{false, true} {
		service.SetQueryEnricher(enricher, allowed)
		_, err := service.Recall(context.Background(), gateway.CallContext{}, Query{Query: "query", SpaceIDs: []string{"space-1"}})
		if err != nil {
			t.Fatal(err)
		}
		if !allowed && enricher.calls != 0 {
			t.Fatal("provider called without allowance")
		}
	}
	if enricher.calls != 1 || store.params.Plan.QuestionType != core.QuestionRelational {
		t.Fatalf("enrichment not applied: calls=%d plan=%+v", enricher.calls, store.params.Plan)
	}
}

func (f *fakeRecallStore) RecallReferenceTime(ctx context.Context, reference string, explicit bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if f.referenceErr != nil {
		return "", f.referenceErr
	}
	if reference != "" {
		return reference, nil
	}
	return "2026-09-18T12:00:00Z", nil
}

func (f *fakeRecallStore) RecallCapabilities(ctx context.Context, request CapabilityRequest) (CapabilityStatus, error) {
	f.statusRequest = request
	if err := ctx.Err(); err != nil {
		return CapabilityStatus{}, err
	}
	if f.statusErr != nil {
		return CapabilityStatus{}, f.statusErr
	}
	if f.status == (CapabilityStatus{}) {
		return CapabilityStatus{Keyword: true, Semantic: true, SemanticVersion: 1, Graph: true}, nil
	}
	return f.status, nil
}

func TestRecallPlansBeforeCallingStore(t *testing.T) {
	store := &fakeRecallStore{}
	service := NewService(fakePorts(store), recallDefaults())
	_, err := service.Recall(context.Background(), gateway.CallContext{}, Query{Query: "what happened yesterday", SpaceIDs: []string{"space-1"}})
	if err != nil {
		t.Fatal(err)
	}
	params := store.params
	if params.Plan.Version == "" || params.Plan.ReferenceTime != params.ReferenceTime || params.Plan.CandidateBudget != 200 {
		t.Fatalf("missing or inconsistent plan: %+v", params)
	}
	if !reflect.DeepEqual(store.statusRequest.Requested, []string{"keyword", "semantic", "graph"}) {
		t.Fatalf("capability inspection did not receive planned defaults: %v", store.statusRequest.Requested)
	}
	if !params.QueryTimeSet || !params.QueryTime.Start.Before(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("relative query time not planned: %+v", params)
	}
}

func (f *fakeRecallStore) Recall(ctx context.Context, params RecallParams, processor RecallProcessor) (domain.SearchResponse, error) {
	f.calls++
	f.params = params
	if f.respectCancel {
		if err := ctx.Err(); err != nil {
			return domain.SearchResponse{}, err
		}
	}
	if len(f.errors) >= f.calls {
		return domain.SearchResponse{}, f.errors[f.calls-1]
	}
	if f.response.Results == nil && f.err == nil {
		err := processor.Orchestrate(ctx, PipelineInput{
			Params: params,
			CursorBinding: CursorBinding{
				Schema: "schema", QueryPlan: "plan", Membership: "membership", Ranking: "ranking",
				Graph: "graph", Context: "context", Feedback: "feedback",
			},
			Primary: PrimaryInput{
				Candidates: []Candidate{
					{MemoryID: "future", RevisionID: "r-future", KeywordRank: 1, KeywordRaw: -2, EventStart: timePointer(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)), EventEnd: timePointer(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))},
					{MemoryID: "answer", RevisionID: "r-answer", KeywordRank: 2, KeywordRaw: -1, SemanticRank: 1, SemanticRaw: 0.9},
				},
				Calibration: core.DefaultCalibration(), Weights: core.DefaultWeights(), QuestionType: core.QuestionTemporal,
				SemanticTop: 10, KeywordTop: 10, MaxSeeds: 15,
			},
			ExpandGraph: func(primary PrimaryOutput) ([]Candidate, error) { return primary.Candidates, nil },
			RankingInput: func(candidates []Candidate) (RankInput, error) {
				return RankInput{Candidates: candidates, Params: params, Weights: core.DefaultWeights(), Terms: core.CompileKeyword(params.Query).Terms}, nil
			},
			Materialize: func(ranked RankOutput) (SelectionInput, error) {
				return SelectionInput{
					QuestionType: core.QuestionTemporal,
					Budget:       domain.ContextBudget{MaxResults: params.Limit, MaxTokens: params.MaxContextTokens, MaxChars: params.MaxChars},
					Candidates:   []domain.ContextCandidate{{MemoryID: ranked.Candidates[0].MemoryID, RevisionID: ranked.Candidates[0].RevisionID, DisplayContent: "answer", Rank: 1, Relevance: 1, EstimatedTokens: 1, EstimatedChars: 6}},
				}, nil
			},
			Render: func(selection SelectionOutput) error {
				f.response.Context = domain.ContextPackage{Items: []domain.ContextItem{{MemoryID: selection.Selection.Items[0].MemoryID}}}
				return nil
			},
		})
		if err != nil {
			return domain.SearchResponse{}, err
		}
	}
	return f.response, f.err
}

func timePointer(value time.Time) *time.Time { return &value }

func TestRecallOwnsFusionRankingTemporalFilteringAndSelection(t *testing.T) {
	store := &fakeRecallStore{}
	result, err := NewService(fakePorts(store), recallDefaults()).Recall(context.Background(), gateway.CallContext{}, Query{
		Query: "what happened on September 18", SpaceIDs: []string{"space-1"}, Intent: domain.RecallIntentTemporal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Context.Items) != 1 || result.Context.Items[0].MemoryID != "answer" {
		t.Fatalf("context = %+v", result.Context)
	}
}

func TestRecallOwnsCapabilitySelectionAndDegradation(t *testing.T) {
	store := &fakeRecallStore{status: CapabilityStatus{
		Keyword: true, SemanticVersion: 7, SemanticReason: "semantic_policy_denied", GraphReason: "graph_stale",
	}}
	result, err := NewService(fakePorts(store), recallDefaults()).Recall(context.Background(), gateway.CallContext{}, Query{
		Query: "query", SpaceIDs: []string{"space-1"}, Channels: []string{"keyword", "semantic", "graph"}, GraphPolicy: "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.params.EffectiveChannels, []string{"keyword"}) || store.params.SemanticVersion != 7 {
		t.Fatalf("selected capabilities = %v semantic=%d", store.params.EffectiveChannels, store.params.SemanticVersion)
	}
	if !result.Execution.Degraded || !reflect.DeepEqual(result.Execution.DegradationReasons, []string{"semantic_policy_denied", "graph_stale"}) {
		t.Fatalf("degradation = %+v", result.Execution)
	}
}

func TestRecallRetriesAfterSelectedCapabilityBecomesUnavailable(t *testing.T) {
	store := &fakeRecallStore{errors: []error{&CapabilityUnavailable{Capability: "semantic", Reason: "semantic_unavailable"}, nil}}
	result, err := NewService(fakePorts(store), recallDefaults()).Recall(context.Background(), gateway.CallContext{}, Query{
		Query: "query", SpaceIDs: []string{"space-1"}, Channels: []string{"keyword", "semantic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.calls != 2 || !reflect.DeepEqual(store.params.EffectiveChannels, []string{"keyword"}) {
		t.Fatalf("fallback calls=%d capabilities=%v", store.calls, store.params.EffectiveChannels)
	}
	if !result.Execution.Degraded || !reflect.DeepEqual(result.Execution.DegradationReasons, []string{"semantic_unavailable"}) {
		t.Fatalf("degradation = %+v", result.Execution)
	}
}

func recallDefaults() Defaults {
	return Defaults{MaxResults: 20, MaxContextTokens: 4000, MaxContextChars: 16000}
}

func TestRecallAppliesProtocolIndependentDefaults(t *testing.T) {
	store := &fakeRecallStore{}
	service := NewService(fakePorts(store), recallDefaults())

	_, err := service.Recall(context.Background(), gateway.CallContext{RequestID: "request-1"}, Query{
		Query:    "  project status  ",
		SpaceIDs: []string{"space-1"},
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}

	params := store.params
	if params.OperationID != "request-1" || params.Query != "project status" {
		t.Fatalf("identity/query = %+v", params)
	}
	if !params.IncludeAncestors || !params.ExcludeStaleAggregates {
		t.Fatalf("boolean defaults = %+v", params)
	}
	if params.Intent != string(domain.RecallIntentAuto) {
		t.Fatalf("intent = %q", params.Intent)
	}
	if !reflect.DeepEqual(params.States, []string{"active", "contested"}) {
		t.Fatalf("states = %v", params.States)
	}
	if params.Limit != 20 || params.MaxContextTokens != 4000 || params.MaxChars != 16000 {
		t.Fatalf("budgets = %+v", params)
	}
}

func TestRecallPreservesExplicitOverridesAndCanonicalizesSets(t *testing.T) {
	store := &fakeRecallStore{}
	service := NewService(fakePorts(store), recallDefaults())

	_, err := service.Recall(context.Background(), gateway.CallContext{}, Query{
		Query:                     "query",
		SpaceIDs:                  []string{"space-1"},
		IncludeAncestors:          false,
		IncludeAncestorsSet:       true,
		MemoryTypes:               []string{"plan", "fact", "plan"},
		MemoryTypesSet:            true,
		States:                    []string{},
		Intent:                    domain.RecallIntentLookup,
		Channels:                  []string{"semantic", "keyword", "semantic"},
		Limit:                     7,
		MaxChars:                  1200,
		MaxContextTokens:          300,
		ExcludeStaleAggregates:    false,
		ExcludeStaleAggregatesSet: true,
		GraphPolicy:               "off",
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}

	params := store.params
	if params.IncludeAncestors || params.ExcludeStaleAggregates {
		t.Fatalf("explicit booleans were defaulted: %+v", params)
	}
	if !reflect.DeepEqual(params.MemoryTypes, []string{"fact", "plan"}) {
		t.Fatalf("memory types = %v", params.MemoryTypes)
	}
	if params.States == nil || len(params.States) != 0 {
		t.Fatalf("explicit empty states = %#v", params.States)
	}
	if !reflect.DeepEqual(params.Channels, []string{"keyword", "semantic"}) {
		t.Fatalf("channels = %v", params.Channels)
	}
}

func TestRecallRejectsInvalidQueriesBeforeStore(t *testing.T) {
	cases := map[string]Query{
		"blank query":          {Query: "  ", SpaceIDs: []string{"space-1"}},
		"missing scope":        {Query: "query"},
		"blank scope":          {Query: "query", SpaceIDs: []string{""}},
		"duplicate scope":      {Query: "query", SpaceIDs: []string{"space-1", "space-1"}},
		"invalid intent":       {Query: "query", SpaceIDs: []string{"space-1"}, Intent: "other"},
		"invalid memory kind":  {Query: "query", SpaceIDs: []string{"space-1"}, MemoryTypesSet: true, MemoryTypes: []string{"other"}},
		"invalid state":        {Query: "query", SpaceIDs: []string{"space-1"}, States: []string{"forgotten"}},
		"invalid time":         {Query: "query", SpaceIDs: []string{"space-1"}, ReferenceTimeSet: true, ReferenceTime: "tomorrow"},
		"invalid channel":      {Query: "query", SpaceIDs: []string{"space-1"}, Channels: []string{"sql"}},
		"invalid graph policy": {Query: "query", SpaceIDs: []string{"space-1"}, GraphPolicy: "everything"},
		"zero result limit":    {Query: "query", SpaceIDs: []string{"space-1"}, LimitSet: true},
		"result limit":         {Query: "query", SpaceIDs: []string{"space-1"}, Limit: 1001},
		"zero token budget":    {Query: "query", SpaceIDs: []string{"space-1"}, MaxContextTokensSet: true},
		"token budget":         {Query: "query", SpaceIDs: []string{"space-1"}, MaxContextTokens: 1000001},
		"character budget":     {Query: "query", SpaceIDs: []string{"space-1"}, MaxChars: 4000001},
	}

	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeRecallStore{}
			_, err := NewService(fakePorts(store), recallDefaults()).Recall(context.Background(), gateway.CallContext{}, query)
			if !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("error = %v, want invalid_input", err)
			}
			if store.calls != 0 {
				t.Fatalf("store called %d times", store.calls)
			}
		})
	}
}

func TestRecallBuildsResultAndHidesRejectionsWithoutExplain(t *testing.T) {
	store := &fakeRecallStore{status: CapabilityStatus{Keyword: true, SemanticReason: "semantic_unavailable"}, response: domain.SearchResponse{
		Context:                  domain.ContextPackage{Rejected: []domain.RejectedContextCandidate{{MemoryID: "memory-2"}}},
		NextCursor:               stringPointer("next"),
		Truncated:                true,
		ReferenceTime:            "2026-09-18T12:00:00Z",
		RequestedChannels:        []string{"keyword", "semantic"},
		ExecutedChannels:         []string{"keyword"},
		Degraded:                 true,
		DegradationReasons:       []string{"semantic_unavailable"},
		QueryPlanIdentity:        "plan-v1",
		RankingIdentity:          "rank-v1",
		ContextIdentity:          "context-fallback",
		RankingVersion:           "weights-v1",
		FeedbackIdentity:         "feedback-v1",
		FeedbackAggregateVersion: 3,
		FTSIndexVersion:          4,
		SemanticIndexVersion:     5,
		GraphIndexVersion:        6,
		GraphIndexState:          "ready",
	}}

	result, err := NewService(fakePorts(store), recallDefaults()).Recall(context.Background(), gateway.CallContext{RequestID: "request-1"}, Query{
		Query: "query", SpaceIDs: []string{"space-1"}, Channels: []string{"keyword", "semantic"},
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if result.Context.Items == nil || result.Context.Rejected != nil || !result.Context.Truncated || result.Context.NextCursor == nil {
		t.Fatalf("context = %+v", result.Context)
	}
	if result.Execution.OperationID != "request-1" || result.Execution.Intent != domain.RecallIntentAuto || result.Execution.ContextSelectionIdentity != "context-fallback" {
		t.Fatalf("execution = %+v", result.Execution)
	}
	if !result.Execution.Degraded || !reflect.DeepEqual(result.Execution.ExecutedCapabilities, []string{"keyword"}) {
		t.Fatalf("execution = %+v", result.Execution)
	}
}

func TestRecallPropagatesCancellationAndClassifiesErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &fakeRecallStore{respectCancel: true}
	query := Query{Query: "query", SpaceIDs: []string{"space-1"}}
	if _, err := NewService(fakePorts(store), recallDefaults()).Recall(ctx, gateway.CallContext{}, query); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	store = &fakeRecallStore{err: gateway.NewError(gateway.ErrorCapabilityUnavailable, "semantic unavailable")}
	if _, err := NewService(fakePorts(store), recallDefaults()).Recall(context.Background(), gateway.CallContext{}, query); !gateway.IsCode(err, gateway.ErrorCapabilityUnavailable) {
		t.Fatalf("gateway error = %v", err)
	}

	store = &fakeRecallStore{err: errors.New("database failed")}
	if _, err := NewService(fakePorts(store), recallDefaults()).Recall(context.Background(), gateway.CallContext{}, query); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("internal error = %v", err)
	}
}

func TestRecallMapsEveryPortFailureToStableGatewayError(t *testing.T) {
	query := Query{Query: "query", SpaceIDs: []string{"space-1"}}
	for name, store := range map[string]*fakeRecallStore{
		"reference":  {referenceErr: errors.New("clock failed")},
		"capability": {statusErr: errors.New("inspection failed")},
		"recall":     {err: errors.New("recall failed")},
		"telemetry":  {telemetryErr: errors.New("telemetry failed")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewService(fakePorts(store), recallDefaults()).Recall(context.Background(), gateway.CallContext{RequestID: "request-1"}, query)
			if !gateway.IsCode(err, gateway.ErrorInternal) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRecallRecordsBoundedContentSafeTelemetry(t *testing.T) {
	store := &fakeRecallStore{}
	_, err := NewService(fakePorts(store), recallDefaults()).Recall(context.Background(), gateway.CallContext{RequestID: "operation-1"}, Query{
		Query: "private query prose", SpaceIDs: []string{"space-1"}, Explain: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record := store.telemetry
	if record.OperationID != "operation-1" || record.QueryHash == "" || record.QueryType == "" || record.QueryPlanIdentity == "" {
		t.Fatalf("telemetry identity = %+v", record)
	}
	encoded := fmt.Sprintf("%+v", record)
	for _, forbidden := range []string{"private query prose", "display content must not persist"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("telemetry leaked %q: %s", forbidden, encoded)
		}
	}
	telemetry := NewTelemetry()
	telemetry.AddGate("bounded", maxTelemetryCount+1)
	telemetry.SetGraphSeeds(maxTelemetryCount + 1)
	bounded := telemetry.Record(testCursorParams(), domain.SearchResponse{})
	if bounded.RejectionCounts["bounded"] != maxTelemetryCount || bounded.Graph.Seeds != maxTelemetryCount {
		t.Fatalf("unbounded telemetry = %+v", bounded)
	}
}

func TestBuildResultAppliesExplainPolicyAndBounds(t *testing.T) {
	rejected := make([]domain.RejectedContextCandidate, maxExplainItems+1)
	evidence := make([]domain.RevisionEvidence, maxExplainItems+1)
	signals := make([]domain.GraphSignal, maxExplainItems+1)
	response := domain.SearchResponse{Context: domain.ContextPackage{
		Rejected: rejected,
		Items:    []domain.ContextItem{{Evidence: evidence, Explanation: &domain.RecallExplanation{GraphSignals: signals}}},
	}}
	explained := buildResult(Query{Explain: true}, response)
	if len(explained.Context.Rejected) != maxExplainItems || len(explained.Context.Items[0].Evidence) != maxExplainItems || len(explained.Context.Items[0].Explanation.GraphSignals) != maxExplainItems {
		t.Fatalf("explain output not bounded: %+v", explained.Context)
	}
	hidden := buildResult(Query{}, response)
	if hidden.Context.Rejected != nil || hidden.Context.Items[0].Explanation != nil {
		t.Fatalf("default output exposed explain data: %+v", hidden.Context)
	}
}

func stringPointer(value string) *string { return &value }
