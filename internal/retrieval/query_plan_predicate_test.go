package retrieval

import (
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// TestQueryPlanPredicatePhrasesReferenceRegisteredPredicates keeps the query
// planner aligned with the closed typed predicate registry: a phrase mapping
// can never target a predicate the graph may not contain.
func TestQueryPlanPredicatePhrasesReferenceRegisteredPredicates(t *testing.T) {
	if len(predicatePhraseMap) == 0 {
		t.Fatal("predicate phrase map is empty")
	}
	for _, entry := range predicatePhraseMap {
		if _, ok := domain.LookupPredicate(entry.predicate); !ok {
			t.Fatalf("query-plan phrase %q maps to unregistered predicate %q", entry.phrase, entry.predicate)
		}
	}
}
