package storage

import (
	"math"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func scoreFact(signal domain.GraphSignal) float64 {
	if signal.Kind == "" {
		signal.Kind = "exact_predicate_fact"
	}
	signal.FactID = "fact-score"
	return graphAggregateScore([]domain.GraphSignal{signal}, nil)
}

func near(t *testing.T, left, right float64) {
	t.Helper()
	if math.Abs(left-right) > 1e-9 {
		t.Fatalf("scores differ: %v vs %v", left, right)
	}
}

func TestGraphScorePredicateIntentRaisesScore(t *testing.T) {
	intended := scoreFact(domain.GraphSignal{FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 1.0, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1})
	unintended := scoreFact(domain.GraphSignal{FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 0.6, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1})
	if intended <= unintended {
		t.Fatalf("predicate intent did not raise the score: intended=%v unintended=%v", intended, unintended)
	}
}

func TestGraphScoreConfidencesMultiply(t *testing.T) {
	strong := scoreFact(domain.GraphSignal{FactConfidence: 0.95, EntityConfidence: 0.95, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1})
	weakFact := scoreFact(domain.GraphSignal{FactConfidence: 0.5, EntityConfidence: 0.95, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1})
	weakEntity := scoreFact(domain.GraphSignal{FactConfidence: 0.95, EntityConfidence: 0.5, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1})
	if !(strong > weakFact && strong > weakEntity) {
		t.Fatalf("confidence product did not dominate: strong=%v weakFact=%v weakEntity=%v", strong, weakFact, weakEntity)
	}
}

func TestGraphScoreEvidenceAndTemporalComponents(t *testing.T) {
	strong := scoreFact(domain.GraphSignal{FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 1, EvidenceStrength: 1.0, HubPenalty: 1, TemporalCompatibility: 1})
	weakEvidence := scoreFact(domain.GraphSignal{FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 1, EvidenceStrength: 0.5, HubPenalty: 1, TemporalCompatibility: 1})
	weakTemporal := scoreFact(domain.GraphSignal{FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 0.4})
	if !(strong > weakEvidence && strong > weakTemporal) {
		t.Fatalf("evidence/temporal components did not affect score: strong=%v evidence=%v temporal=%v", strong, weakEvidence, weakTemporal)
	}
}

func TestGraphScoreHubPenaltyDownWeightsGenericHubs(t *testing.T) {
	rare := scoreFact(domain.GraphSignal{FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1.0, TemporalCompatibility: 1})
	hub := scoreFact(domain.GraphSignal{FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 0.2, TemporalCompatibility: 1})
	if hub >= rare {
		t.Fatalf("hub penalty did not down-weight the hub: rare=%v hub=%v", rare, hub)
	}
}

func TestGraphScoreHopDecayOnPaths(t *testing.T) {
	oneHop := graphAggregateScore([]domain.GraphSignal{{Kind: "exact_predicate_fact", FactID: "fact-one", FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1}}, nil)
	path := graphAggregateScore(nil, []domain.GraphPath{{StartEntityID: "a", EndEntityID: "c", Hop: 2, Edges: []domain.GraphPathEdge{
		{FactID: "f1", Confidence: 0.9, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1},
		{FactID: "f2", Confidence: 0.9, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1},
	}}})
	if path >= oneHop {
		t.Fatalf("two-hop path was not decayed below an equivalent one-hop fact: oneHop=%v path=%v", oneHop, path)
	}
}

func TestGraphScorePathMultipliesEdgeConfidence(t *testing.T) {
	high := graphAggregateScore(nil, []domain.GraphPath{{StartEntityID: "a", EndEntityID: "c", Hop: 2, Edges: []domain.GraphPathEdge{
		{FactID: "f1", Confidence: 0.95, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1},
		{FactID: "f2", Confidence: 0.95, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1},
	}}})
	low := graphAggregateScore(nil, []domain.GraphPath{{StartEntityID: "a", EndEntityID: "c", Hop: 2, Edges: []domain.GraphPathEdge{
		{FactID: "f1", Confidence: 0.95, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1},
		{FactID: "f2", Confidence: 0.30, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1},
	}}})
	if high <= low {
		t.Fatalf("path confidence product did not matter: high=%v low=%v", high, low)
	}
}

func TestGraphScoreCorroborationIsCapped(t *testing.T) {
	makeSignals := func(count int) []domain.GraphSignal {
		signals := make([]domain.GraphSignal, count)
		for i := range signals {
			signals[i] = domain.GraphSignal{Kind: "exact_predicate_fact", FactID: "fact-" + strconvItoa2(i), FactConfidence: 0.9, EntityConfidence: 0.9, PredicateIntent: 1, EvidenceStrength: 1, HubPenalty: 1, TemporalCompatibility: 1}
		}
		return signals
	}
	single := graphAggregateScore(makeSignals(1), nil)
	few := graphAggregateScore(makeSignals(3), nil)
	many := graphAggregateScore(makeSignals(10), nil)
	if single >= few {
		t.Fatalf("corroboration did not add a bounded bonus: single=%v few=%v", single, few)
	}
	near(t, few, many)
}
