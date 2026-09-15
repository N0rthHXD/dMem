package retrieval

import (
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func selCandidate(memoryID, revisionID string, relevance float64, mutate func(*domain.ContextCandidate)) domain.ContextCandidate {
	candidate := domain.ContextCandidate{
		MemoryID: memoryID, RevisionID: revisionID, SpaceID: "space-1",
		Kind: domain.MemoryKindEvent, Relevance: relevance,
		EstimatedTokens: 10, EstimatedChars: 10, LifecycleState: "active",
	}
	if mutate != nil {
		mutate(&candidate)
	}
	return candidate
}

func selectionBudget() domain.ContextBudget {
	return domain.ContextBudget{MaxTokens: 100000, MaxChars: 100000, MaxResults: 100, MaxSources: 100, MaxSensitive: 100}
}

func dayAt(day int) *time.Time {
	value := time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC)
	return &value
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	set := make(map[string]struct{}, len(got))
	for _, value := range got {
		set[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func selectedMemoryIDs(items []domain.SelectedContextItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.MemoryID
	}
	return ids
}

func TestSelectCollapsesSameMemoryToRepresentative(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: selectionBudget(), Candidates: []domain.ContextCandidate{
		selCandidate("m1", "r1", 0.8, func(c *domain.ContextCandidate) { c.PrimaryEvidence = []string{"e1"}; c.Rank = 1 }),
		selCandidate("m1", "r2", 0.9, func(c *domain.ContextCandidate) { c.PrimaryEvidence = []string{"e1", "e2"}; c.Rank = 2 }),
	}})
	if len(output.Items) != 1 {
		t.Fatalf("expected one representative, got %d: %+v", len(output.Items), output.Items)
	}
	item := output.Items[0]
	if item.RevisionID != "r2" || item.MemoryID != "m1" {
		t.Fatalf("representative = %s/%s, want m1/r2", item.MemoryID, item.RevisionID)
	}
	if !sameStringSet(item.Evidence, []string{"e1", "e2"}) {
		t.Fatalf("collapsed evidence = %v, want [e1 e2]", item.Evidence)
	}
	if item.Explanation.CollapsedMembers != 1 {
		t.Fatalf("collapsed members = %d, want 1", item.Explanation.CollapsedMembers)
	}
	if len(output.Rejected) != 1 || output.Rejected[0].Reason != StopReasonDuplicateCluster {
		t.Fatalf("rejected = %+v, want one duplicate_cluster", output.Rejected)
	}
}

func TestSelectClustersSharedPrimaryEvidenceAndCorroborates(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: selectionBudget(), Candidates: []domain.ContextCandidate{
		selCandidate("m1", "r1", 0.70, func(c *domain.ContextCandidate) { c.PrimaryEvidence = []string{"e1"}; c.Rank = 1 }),
		selCandidate("m2", "r2", 0.75, func(c *domain.ContextCandidate) { c.PrimaryEvidence = []string{"e1"}; c.Rank = 2 }),
	}})
	if len(output.Items) != 1 || output.Items[0].MemoryID != "m2" {
		t.Fatalf("shared evidence cluster = %+v", output.Items)
	}
	if !sameStringSet(output.Items[0].CorroboratingMemoryIDs, []string{"m1"}) {
		t.Fatalf("corroborating = %v, want [m1]", output.Items[0].CorroboratingMemoryIDs)
	}
}

func TestSelectClustersEquivalentCanonicalTuple(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: selectionBudget(), Candidates: []domain.ContextCandidate{
		selCandidate("m1", "r1", 0.7, func(c *domain.ContextCandidate) { c.CanonicalHash = "h1"; c.Rank = 1 }),
		selCandidate("m2", "r2", 0.8, func(c *domain.ContextCandidate) { c.CanonicalHash = "h1"; c.Rank = 2 }),
	}})
	if len(output.Items) != 1 || output.Items[0].MemoryID != "m2" {
		t.Fatalf("canonical tuple cluster = %+v", output.Items)
	}
}

func TestSelectClustersSemanticTimeCompatible(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionMultiHop, Budget: selectionBudget(), Candidates: []domain.ContextCandidate{
		selCandidate("m1", "r1", 0.7, func(c *domain.ContextCandidate) {
			c.SemanticVector = []float64{1, 0}
			c.ValidFrom, c.ValidUntil = dayAt(1), dayAt(3)
			c.Rank = 1
		}),
		selCandidate("m2", "r2", 0.8, func(c *domain.ContextCandidate) {
			c.SemanticVector = []float64{1, 0}
			c.ValidFrom, c.ValidUntil = dayAt(2), dayAt(4)
			c.Rank = 2
		}),
	}})
	if len(output.Items) != 1 || output.Items[0].MemoryID != "m2" {
		t.Fatalf("semantic/time cluster = %+v", output.Items)
	}
}

func TestSelectDoesNotClusterSemanticWithIncompatibleTime(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionMultiHop, Budget: selectionBudget(), Candidates: []domain.ContextCandidate{
		selCandidate("m1", "r1", 0.90, func(c *domain.ContextCandidate) {
			c.SemanticVector = []float64{1, 0}
			c.ValidFrom, c.ValidUntil = dayAt(1), dayAt(2)
			c.Rank = 1
		}),
		selCandidate("m2", "r2", 0.88, func(c *domain.ContextCandidate) {
			c.SemanticVector = []float64{1, 0}
			c.ValidFrom, c.ValidUntil = dayAt(3), dayAt(4)
			c.Rank = 2
		}),
	}})
	if len(output.Items) != 2 {
		t.Fatalf("incompatible-time candidates must not cluster: %+v", output.Items)
	}
}

func TestSelectAggregateProvenancePrefersSpecificSource(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: selectionBudget(), Candidates: []domain.ContextCandidate{
		selCandidate("src", "rs", 0.70, func(c *domain.ContextCandidate) { c.PrimaryEvidence = []string{"e1"}; c.Rank = 1 }),
		selCandidate("agg", "ra", 0.95, func(c *domain.ContextCandidate) {
			c.Aggregate = true
			c.AggregateSources = []string{"src"}
			c.PrimaryEvidence = []string{"e1"}
			c.Rank = 2
		}),
	}})
	if len(output.Items) != 1 || output.Items[0].MemoryID != "src" {
		t.Fatalf("aggregate provenance representative = %+v", output.Items)
	}
	if output.Rejected[0].Reason != StopReasonDuplicateCluster {
		t.Fatalf("aggregate member reason = %q", output.Rejected[0].Reason)
	}
}

func TestSelectExploratoryDeduplicatesSourceCoverage(t *testing.T) {
	candidates := []domain.ContextCandidate{
		selCandidate("a", "ra", 0.90, func(c *domain.ContextCandidate) { c.SourceIDs = []string{"s0"}; c.Rank = 1 }),
		selCandidate("b", "rb", 0.85, func(c *domain.ContextCandidate) { c.SourceIDs = []string{"s0"}; c.Rank = 2 }),
		selCandidate("c", "rc", 0.82, func(c *domain.ContextCandidate) { c.SourceIDs = []string{"s1"}; c.Rank = 3 }),
	}
	output := SelectContext(SelectInput{QuestionType: QuestionExploratory, Budget: domain.ContextBudget{MaxResults: 2, MaxTokens: 100000, MaxChars: 100000, MaxSources: 100}, Candidates: candidates})
	if len(output.Items) != 2 || output.Items[1].MemoryID != "c" {
		t.Fatalf("exploratory selection = %+v", selectedMemoryIDs(output.Items))
	}
	if output.Accounting.SourcesUsed != 2 {
		t.Fatalf("sources used = %d, want 2", output.Accounting.SourcesUsed)
	}
	if output.Items[1].Explanation.SourceCoverage <= 0 {
		t.Fatalf("second item source coverage = %v, want > 0", output.Items[1].Explanation.SourceCoverage)
	}
}

func TestSelectPenalizesSemanticRedundancy(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: domain.ContextBudget{MaxResults: 2, MaxTokens: 100000, MaxChars: 100000, MaxSources: 100, MaxSensitive: 100}, Candidates: []domain.ContextCandidate{
		selCandidate("a", "ra", 0.90, func(c *domain.ContextCandidate) {
			c.SemanticVector = []float64{1, 0}
			c.ValidFrom, c.ValidUntil = dayAt(1), dayAt(2)
			c.Rank = 1
		}),
		selCandidate("b", "rb", 0.89, func(c *domain.ContextCandidate) {
			c.SemanticVector = []float64{1, 0}
			c.ValidFrom, c.ValidUntil = dayAt(3), dayAt(4)
			c.Rank = 2
		}),
		selCandidate("c", "rc", 0.85, func(c *domain.ContextCandidate) {
			c.SemanticVector = []float64{0, 1}
			c.ValidFrom, c.ValidUntil = dayAt(5), dayAt(6)
			c.Rank = 3
		}),
	}})
	if len(output.Items) != 2 {
		t.Fatalf("redundancy selection produced %d items", len(output.Items))
	}
	if !sameStringSet(selectedMemoryIDs(output.Items), []string{"a", "c"}) {
		t.Fatalf("redundant candidate displaced a diverse one: %v", selectedMemoryIDs(output.Items))
	}
}

func TestSelectHardTokenBudget(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: domain.ContextBudget{MaxResults: 10, MaxTokens: 15, MaxChars: 100000, MaxSources: 100}, Candidates: []domain.ContextCandidate{
		selCandidate("a", "ra", 0.9, func(c *domain.ContextCandidate) { c.EstimatedTokens = 10; c.Rank = 1 }),
		selCandidate("b", "rb", 0.88, func(c *domain.ContextCandidate) { c.EstimatedTokens = 10; c.Rank = 2 }),
	}})
	if len(output.Items) != 1 || output.Accounting.TokensUsed != 10 {
		t.Fatalf("token budget = %+v", output.Accounting)
	}
	if output.StopReason != StopReasonTokenBudget {
		t.Fatalf("stop reason = %q, want token_budget", output.StopReason)
	}
}

func TestSelectHardCharBudget(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: domain.ContextBudget{MaxResults: 10, MaxTokens: 100000, MaxChars: 15, MaxSources: 100}, Candidates: []domain.ContextCandidate{
		selCandidate("a", "ra", 0.9, func(c *domain.ContextCandidate) { c.EstimatedChars = 10; c.Rank = 1 }),
		selCandidate("b", "rb", 0.88, func(c *domain.ContextCandidate) { c.EstimatedChars = 10; c.Rank = 2 }),
	}})
	if len(output.Items) != 1 || output.StopReason != StopReasonCharBudget {
		t.Fatalf("char budget = %+v stop=%q", output.Accounting, output.StopReason)
	}
}

func TestSelectHardResultBudget(t *testing.T) {
	candidates := []domain.ContextCandidate{
		selCandidate("a", "ra", 0.9, func(c *domain.ContextCandidate) { c.Rank = 1 }),
		selCandidate("b", "rb", 0.88, func(c *domain.ContextCandidate) { c.Rank = 2 }),
		selCandidate("c", "rc", 0.86, func(c *domain.ContextCandidate) { c.Rank = 3 }),
	}
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: domain.ContextBudget{MaxResults: 1, MaxTokens: 100000, MaxChars: 100000, MaxSources: 100}, Candidates: candidates})
	if len(output.Items) != 1 || output.StopReason != StopReasonResultBudget {
		t.Fatalf("result budget = %d items stop=%q", len(output.Items), output.StopReason)
	}
	if output.Accounting.ResultsUsed != 1 {
		t.Fatalf("results used = %d", output.Accounting.ResultsUsed)
	}
}

func TestSelectHardSourceBudget(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: domain.ContextBudget{MaxResults: 10, MaxTokens: 100000, MaxChars: 100000, MaxSources: 1, MaxSensitive: 100}, Candidates: []domain.ContextCandidate{
		selCandidate("a", "ra", 0.9, func(c *domain.ContextCandidate) { c.SourceIDs = []string{"s0"}; c.Rank = 1 }),
		selCandidate("b", "rb", 0.88, func(c *domain.ContextCandidate) { c.SourceIDs = []string{"s1"}; c.Rank = 2 }),
	}})
	if len(output.Items) != 1 || output.Accounting.SourcesUsed != 1 || output.StopReason != StopReasonSourceBudget {
		t.Fatalf("source budget = %+v stop=%q", output.Accounting, output.StopReason)
	}
}

func TestSelectSensitivityBudget(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: domain.ContextBudget{MaxResults: 10, MaxTokens: 100000, MaxChars: 100000, MaxSources: 100, MaxSensitive: 1}, Candidates: []domain.ContextCandidate{
		selCandidate("a", "ra", 0.9, func(c *domain.ContextCandidate) { c.Rank = 1 }),
		selCandidate("b", "rb", 0.88, func(c *domain.ContextCandidate) { c.Sensitivity = "sensitive"; c.Rank = 2 }),
		selCandidate("c", "rc", 0.86, func(c *domain.ContextCandidate) { c.Sensitivity = "sensitive"; c.Rank = 3 }),
	}})
	if len(output.Items) != 2 || output.Accounting.SensitiveUsed != 1 {
		t.Fatalf("sensitivity budget = %+v", output.Accounting)
	}
	if output.StopReason != StopReasonSensitivityBudget {
		t.Fatalf("stop reason = %q", output.StopReason)
	}
}

func TestSelectDeterministicTies(t *testing.T) {
	candidates := []domain.ContextCandidate{
		selCandidate("b", "rb", 0.9, func(c *domain.ContextCandidate) { c.Rank = 5 }),
		selCandidate("a", "ra", 0.9, func(c *domain.ContextCandidate) { c.Rank = 5 }),
	}
	first := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: selectionBudget(), Candidates: candidates})
	second := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: selectionBudget(), Candidates: candidates})
	if len(first.Items) != 2 || first.Items[0].MemoryID != "a" || first.Items[1].MemoryID != "b" {
		t.Fatalf("tie order = %v", selectedMemoryIDs(first.Items))
	}
	if first.Items[0].MemoryID != second.Items[0].MemoryID || first.Items[1].MemoryID != second.Items[1].MemoryID {
		t.Fatalf("selection is not deterministic")
	}
}

func TestExactPrimaryIsProtected(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: domain.ContextBudget{MaxResults: 1, MaxTokens: 100000, MaxChars: 100000, MaxSources: 100}, Candidates: []domain.ContextCandidate{
		selCandidate("exact", "re", 0.4, func(c *domain.ContextCandidate) { c.ExactPrimary = true; c.Rank = 1 }),
		selCandidate("broad", "rb", 0.95, func(c *domain.ContextCandidate) { c.Rank = 2 }),
	}})
	if len(output.Items) != 1 || output.Items[0].MemoryID != "exact" {
		t.Fatalf("exact primary not protected: %v", selectedMemoryIDs(output.Items))
	}
	if output.Rejected[0].Reason != StopReasonResultBudget {
		t.Fatalf("non-exact rejection = %q", output.Rejected[0].Reason)
	}
}

func TestQuestionTypeSelectionPolicies(t *testing.T) {
	lookup := PolicyForQuestionType(QuestionLookup)
	verification := PolicyForQuestionType(QuestionVerification)
	temporal := PolicyForQuestionType(QuestionTemporal)
	multiHop := PolicyForQuestionType(QuestionMultiHop)
	procedural := PolicyForQuestionType(QuestionProcedural)
	exploratory := PolicyForQuestionType(QuestionExploratory)
	for _, policy := range []SelectionPolicy{lookup, verification, temporal, multiHop, procedural, exploratory} {
		if policy.Version != ContextSelectionVersion || policy.Identity() == "" {
			t.Fatalf("policy not versioned: %+v", policy)
		}
	}
	if !lookup.ProtectExact || !verification.ProtectExact {
		t.Fatal("lookup and verification must protect exact matches")
	}
	if multiHop.Evidence <= lookup.Evidence || multiHop.Source <= lookup.Source {
		t.Fatalf("multi-hop must favor evidence/source coverage: %+v", multiHop)
	}
	if lookup.Redundancy <= multiHop.Redundancy {
		t.Fatalf("lookup must penalize redundancy more than multi-hop")
	}
	if temporal.Evidence <= lookup.Evidence {
		t.Fatalf("temporal must favor anchor evidence: %+v", temporal)
	}
	if !procedural.PreserveOrder {
		t.Fatal("procedural policy must preserve ordered steps")
	}
	if exploratory.Source <= lookup.Source || exploratory.Entity <= lookup.Entity || exploratory.Redundancy >= lookup.Redundancy {
		t.Fatalf("exploratory must favor source/entity diversity: %+v", exploratory)
	}
	if multiHop.Identity() == lookup.Identity() {
		t.Fatal("distinct question types must have distinct policy identities")
	}
	auto := PolicyForQuestionType(QuestionType("auto"))
	if auto.Evidence != lookup.Evidence || auto.Redundancy != lookup.Redundancy || !auto.ProtectExact {
		t.Fatal("auto intent must default to the lookup policy")
	}
}

func TestLookupStopsAtDiscontinuityWhileMultiHopContinues(t *testing.T) {
	candidates := []domain.ContextCandidate{
		selCandidate("a", "ra", 0.90, func(c *domain.ContextCandidate) { c.PrimaryEvidence = []string{"e0"}; c.Rank = 1 }),
		selCandidate("b", "rb", 0.60, func(c *domain.ContextCandidate) { c.PrimaryEvidence = []string{"e1"}; c.Rank = 2 }),
		selCandidate("c", "rc", 0.58, func(c *domain.ContextCandidate) { c.PrimaryEvidence = []string{"e2"}; c.Rank = 3 }),
	}
	lookup := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: selectionBudget(), Candidates: candidates})
	multiHop := SelectContext(SelectInput{QuestionType: QuestionMultiHop, Budget: selectionBudget(), Candidates: candidates})
	if len(lookup.Items) != 1 || lookup.StopReason != StopReasonScoreDiscontinuity {
		t.Fatalf("lookup selection = %d items stop=%q", len(lookup.Items), lookup.StopReason)
	}
	if len(multiHop.Items) != 3 {
		t.Fatalf("multi-hop selection = %d items, want 3", len(multiHop.Items))
	}
}

func TestExploratoryFavorsSourceDiversityOverLookup(t *testing.T) {
	candidates := []domain.ContextCandidate{
		selCandidate("a", "ra", 0.90, func(c *domain.ContextCandidate) { c.SourceIDs = []string{"s0"}; c.Rank = 1 }),
		selCandidate("b", "rb", 0.85, func(c *domain.ContextCandidate) { c.SourceIDs = []string{"s0"}; c.Rank = 2 }),
		selCandidate("c", "rc", 0.82, func(c *domain.ContextCandidate) { c.SourceIDs = []string{"s1"}; c.Rank = 3 }),
	}
	budget := domain.ContextBudget{MaxResults: 2, MaxTokens: 100000, MaxChars: 100000, MaxSources: 100, MaxSensitive: 100}
	lookup := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: budget, Candidates: candidates})
	exploratory := SelectContext(SelectInput{QuestionType: QuestionExploratory, Budget: budget, Candidates: candidates})
	if lookup.Items[1].MemoryID != "b" {
		t.Fatalf("lookup second item = %v, want b", selectedMemoryIDs(lookup.Items))
	}
	if exploratory.Items[1].MemoryID != "c" {
		t.Fatalf("exploratory second item = %v, want c", selectedMemoryIDs(exploratory.Items))
	}
}

func TestMaximumResultsDoesNotForceIrrelevantContext(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: domain.ContextBudget{MaxResults: 10, MaxTokens: 100000, MaxChars: 100000, MaxSources: 100}, Candidates: []domain.ContextCandidate{
		selCandidate("a", "ra", 0.90, func(c *domain.ContextCandidate) { c.Rank = 1 }),
		selCandidate("b", "rb", 0.60, func(c *domain.ContextCandidate) { c.Rank = 2 }),
		selCandidate("c", "rc", 0.10, func(c *domain.ContextCandidate) { c.Rank = 3 }),
	}})
	if len(output.Items) >= 10 || len(output.Items) != 1 {
		t.Fatalf("max_results forced irrelevant context: %d items", len(output.Items))
	}
	if output.StopReason != StopReasonScoreDiscontinuity {
		t.Fatalf("stop reason = %q, want score_discontinuity", output.StopReason)
	}
	if len(output.Rejected) != 2 {
		t.Fatalf("rejected = %+v", output.Rejected)
	}
}

func TestLowQualityCandidatesStopSelection(t *testing.T) {
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: domain.ContextBudget{MaxResults: 10, MaxTokens: 100000, MaxChars: 100000, MaxSources: 100}, Candidates: []domain.ContextCandidate{
		selCandidate("a", "ra", 0.80, func(c *domain.ContextCandidate) { c.Rank = 1 }),
		selCandidate("b", "rb", 0.05, func(c *domain.ContextCandidate) { c.Rank = 2 }),
	}})
	if len(output.Items) != 1 || output.StopReason != StopReasonBelowMinimumUtility {
		t.Fatalf("low-quality stopping = %d items stop=%q", len(output.Items), output.StopReason)
	}
	if output.Rejected[0].Reason != StopReasonBelowMinimumUtility {
		t.Fatalf("rejection reason = %q", output.Rejected[0].Reason)
	}
}

func TestResultBudgetIsHardCapNotTarget(t *testing.T) {
	candidates := []domain.ContextCandidate{
		selCandidate("a", "ra", 0.90, func(c *domain.ContextCandidate) { c.Rank = 1 }),
		selCandidate("b", "rb", 0.88, func(c *domain.ContextCandidate) { c.Rank = 2 }),
		selCandidate("c", "rc", 0.86, func(c *domain.ContextCandidate) { c.Rank = 3 }),
		selCandidate("d", "rd", 0.84, func(c *domain.ContextCandidate) { c.Rank = 4 }),
	}
	output := SelectContext(SelectInput{QuestionType: QuestionLookup, Budget: domain.ContextBudget{MaxResults: 2, MaxTokens: 100000, MaxChars: 100000, MaxSources: 100}, Candidates: candidates})
	if len(output.Items) != 2 || output.StopReason != StopReasonResultBudget {
		t.Fatalf("result cap = %d items stop=%q", len(output.Items), output.StopReason)
	}
}
