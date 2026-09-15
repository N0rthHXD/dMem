package storage

import (
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/retrieval"
)

func planFor(query string) retrieval.QueryPlan {
	reference := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	return retrieval.BuildQueryPlan(query, retrieval.PlanOptions{ReferenceTime: reference, Limit: 20, MaxChars: 4000})
}

func TestQueryPlanNormalizesAndExtractsTerms(t *testing.T) {
	plan := planFor("  When   did   Alice  join   PROJECT-ALPHA on 2026-08-21 for 3 days?  ")
	if plan.NormalizedQuery != "When did Alice join PROJECT-ALPHA on 2026-08-21 for 3 days?" {
		t.Fatalf("normalized query = %q", plan.NormalizedQuery)
	}
	if plan.Version != retrieval.QueryPlanVersion {
		t.Fatalf("plan version = %q", plan.Version)
	}
	if len(plan.Identifiers) == 0 || len(plan.Dates) == 0 || len(plan.Numbers) == 0 {
		t.Fatalf("extracted terms = identifiers %v dates %v numbers %v", plan.Identifiers, plan.Dates, plan.Numbers)
	}
	if plan.ReferenceTime == "" {
		t.Fatal("reference time not bound")
	}
	if plan.CandidateBudget != 20 || plan.ContextBudget != 4000 {
		t.Fatalf("budgets = %d/%d", plan.CandidateBudget, plan.ContextBudget)
	}
}

func TestQueryPlanLanguageAndQuestionType(t *testing.T) {
	chinese := planFor("我上次去上海旅游住的是哪家酒店？")
	if chinese.Language != "cjk" {
		t.Fatalf("language = %q", chinese.Language)
	}
	if planFor("When did Alice visit Kyoto?").QuestionType != retrieval.QuestionTemporal {
		t.Fatalf("temporal classification failed: %+v", planFor("When did Alice visit Kyoto?"))
	}
	if planFor("who owns PROJECT-ALPHA").QuestionType != retrieval.QuestionRelational {
		t.Fatalf("relational classification failed")
	}
	if planFor("Is the report ready?").QuestionType != retrieval.QuestionVerification {
		t.Fatalf("verification classification failed")
	}
	if planFor("how to deploy the service").QuestionType != retrieval.QuestionProcedural {
		t.Fatalf("procedural classification failed")
	}
}

func TestQueryPlanQuotedPhrasesAreExact(t *testing.T) {
	plan := planFor(`find "quarterly revenue report" for Alice`)
	if len(plan.QuotedPhrases) != 1 || plan.QuotedPhrases[0] != "quarterly revenue report" {
		t.Fatalf("quoted phrases = %v", plan.QuotedPhrases)
	}
	found := false
	for _, term := range plan.ExactTerms {
		if term == "quarterly revenue report" {
			found = true
		}
	}
	if !found {
		t.Fatalf("quoted phrase missing from exact terms: %v", plan.ExactTerms)
	}
}

func TestQueryPlanRequestedKindsAndScope(t *testing.T) {
	reference := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	plan := retrieval.BuildQueryPlan("anything", retrieval.PlanOptions{ReferenceTime: reference, Limit: 20, MaxChars: 4000, RequestedKinds: []string{"event", "fact"}, RequestedScope: []string{"space-b", "space-a"}})
	if len(plan.TargetMemoryKinds) != 2 || plan.TargetMemoryKinds[0] != "event" || len(plan.RequestedScope) != 2 || plan.RequestedScope[0] != "space-a" {
		t.Fatalf("requested kinds/scope = %v/%v", plan.TargetMemoryKinds, plan.RequestedScope)
	}
	if plan.Identity() == "" {
		t.Fatal("plan identity empty")
	}
	if plan.GraphPolicy != retrieval.GraphFeaturesOnly {
		t.Fatalf("default graph policy = %q", plan.GraphPolicy)
	}
}

func TestQueryPlanPredicatesAndQuestionTypes(t *testing.T) {
	if predicates := planFor("who owns PROJECT-ALPHA").TargetPredicates; len(predicates) == 0 || predicates[0] != "owned_by" {
		t.Fatalf("ownership predicates = %v", predicates)
	}
	cases := map[string]retrieval.QuestionType{
		"show the status":                 retrieval.QuestionLookup,
		"who is Alice":                    retrieval.QuestionEntity,
		"how are Alice and Bob connected": retrieval.QuestionMultiHop,
		"explore project ideas":           retrieval.QuestionExploratory,
	}
	for query, want := range cases {
		if got := planFor(query).QuestionType; got != want {
			t.Fatalf("question type for %q = %q, want %q", query, got, want)
		}
	}
}

func TestQueryPlanDeterminismIgnoresScopeOrdering(t *testing.T) {
	first := planFor("Alice project")
	first.RequestedScope = []string{"a", "b"}
	first.TargetMemoryKinds = []string{"fact", "event"}
	second := planFor("Alice project")
	second.RequestedScope = []string{"b", "a"}
	second.TargetMemoryKinds = []string{"event", "fact"}
	if first.Identity() != second.Identity() {
		t.Fatal("plan identity depends on input ordering")
	}
}

func TestQueryPlanGraphPolicyIsSafe(t *testing.T) {
	if planFor("hello world").GraphPolicy != retrieval.GraphFeaturesOnly {
		t.Fatalf("lookup plan must default to features_only")
	}
	relational := planFor("who owns PROJECT-ALPHA")
	if relational.QuestionType != retrieval.QuestionRelational {
		t.Fatalf("relational classification = %q", relational.QuestionType)
	}
	if relational.GraphPolicy != retrieval.GraphBoundedPath {
		t.Fatalf("relational graph policy = %q", relational.GraphPolicy)
	}
}
