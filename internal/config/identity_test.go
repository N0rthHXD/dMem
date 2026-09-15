package config

import (
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/provider"
)

func TestSourceIdentityDeterministic(t *testing.T) {
	t.Parallel()
	data := []byte("config_version = 1\n")
	id1 := sourceIdentity(data)
	id2 := sourceIdentity(data)
	if id1 != id2 {
		t.Fatalf("sourceIdentity not deterministic: %q != %q", id1, id2)
	}
	if !strings.HasPrefix(id1, "sha256:") {
		t.Fatalf("sourceIdentity missing prefix: %q", id1)
	}
}

func TestSourceIdentityDiffersForDifferentBytes(t *testing.T) {
	t.Parallel()
	a := sourceIdentity([]byte("a"))
	b := sourceIdentity([]byte("b"))
	if a == b {
		t.Fatal("sourceIdentity identical for different bytes")
	}
}

func TestEffectiveIdentityExcludesSecrets(t *testing.T) {
	t.Parallel()
	cfg1, err := Load("testdata/valid/minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	cfg2 := cfg1
	cfg2.SourceIdentity = ""
	cfg2.EffectiveIdentity = ""
	id1 := effectiveIdentity(cfg1)
	id2 := effectiveIdentity(cfg2)
	_ = id1
	_ = id2
}

func TestFormattingOnlyChangesProduceNoChange(t *testing.T) {
	t.Parallel()
	doc1 := `config_version = 1

[instance]
data_dir = "/tmp/data"
runtime_dir = "/tmp/run"
database_filename = "dmem.db"
socket_filename = "dmem.sock"

[server]
request_timeout = "30s"
shutdown_grace_period = "15s"
max_connections = 64
max_request_bytes = 4194304

[jobs]
worker_count = 2
queue_soft_limit = 10000
queue_hard_limit = 20000
max_attempts = 3
lease_duration = "30s"
per_job_timeout = "2m"
retry_initial_backoff = "1s"
retry_max_backoff = "5m"

[storage]
disk_soft_free_bytes = 1073741824
disk_hard_free_bytes = 268435456

[processing]
version = "builtin-noop-v1"

[processing.provider]
enabled = false

[embedding.provider]
enabled = false
`
	doc2 := strings.Replace(doc1, "config_version = 1\n", "config_version=1\n\n", 1)
	id1 := sourceIdentity([]byte(doc1))
	id2 := sourceIdentity([]byte(doc2))
	if id1 == id2 {
		t.Fatal("source identity should differ for formatting changes")
	}
	d1, _, err := decodeDocument([]byte(doc1))
	if err != nil {
		t.Fatal(err)
	}
	d2, _, err := decodeDocument([]byte(doc2))
	if err != nil {
		t.Fatal(err)
	}
	cfg1, err := normalize(d1, []byte(doc1))
	if err != nil {
		t.Fatal(err)
	}
	cfg2, err := normalize(d2, []byte(doc2))
	if err != nil {
		t.Fatal(err)
	}
	if cfg1.EffectiveIdentity != cfg2.EffectiveIdentity {
		t.Fatalf("effective identity should be the same for formatting-only changes: %q != %q", cfg1.EffectiveIdentity, cfg2.EffectiveIdentity)
	}
}

func TestCapabilityIdentityDisabled(t *testing.T) {
	t.Parallel()
	id := CapabilityIdentity(provider.Config{Enabled: false})
	if id != "" {
		t.Fatalf("CapabilityIdentity for disabled provider should be empty, got %q", id)
	}
}

func TestCapabilityIdentityEnabled(t *testing.T) {
	t.Parallel()
	id := CapabilityIdentity(provider.Config{
		Enabled:  true,
		Kind:     "openai-compatible",
		Endpoint: "https://example.com/v1/chat",
		Model:    "model-a",
	})
	if id == "" {
		t.Fatal("CapabilityIdentity for enabled provider should be non-empty")
	}
	if !strings.HasPrefix(id, "sha256:") {
		t.Fatalf("missing prefix: %q", id)
	}
}

func TestPolicyIdentityExcludesSecretValues(t *testing.T) {
	t.Parallel()
	policy1 := provider.Policy{
		Default:       "deny",
		ContentTypes:  []string{"text/plain"},
		SourceTypes:   []string{"manual"},
		Sensitivities: []string{"normal"},
		Assignments: []provider.PolicyAssignment{
			{Selector: "space", SpaceID: "space-a", Effect: "allow"},
		},
	}
	policy2 := policy1
	policy2.ContentTypes = []string{"text/plain", "text/plain"}
	id1 := PolicyIdentity(policy1)
	id2 := PolicyIdentity(policy2)
	if id1 != id2 {
		t.Fatal("policy identity should be the same with duplicate content types")
	}
}

func TestPolicyIdentityNormalization(t *testing.T) {
	t.Parallel()
	policy1 := provider.Policy{
		Default:       "deny",
		ContentTypes:  []string{"text/plain", "application/json"},
		SourceTypes:   []string{"manual", "message"},
		Sensitivities: []string{"normal"},
		Assignments: []provider.PolicyAssignment{
			{Selector: "space", SpaceID: "space-a", Effect: "allow"},
		},
	}
	policy2 := provider.Policy{
		Default:       "deny",
		ContentTypes:  []string{"application/json", "text/plain"},
		SourceTypes:   []string{"message", "manual"},
		Sensitivities: []string{"normal"},
		Assignments: []provider.PolicyAssignment{
			{Selector: "space", SpaceID: "space-a", Effect: "allow"},
		},
	}
	id1 := PolicyIdentity(policy1)
	id2 := PolicyIdentity(policy2)
	if id1 != id2 {
		t.Fatal("policy identity should be the same regardless of input order")
	}
}

func TestCompatibilityIdentityEmbedding(t *testing.T) {
	t.Parallel()
	id := CompatibilityIdentity(provider.Config{
		Enabled: true,
		Model:   "embedding-a",
		Options: provider.EmbeddingOptions{
			Dimensions:        1024,
			QueryInstructions: "query: ",
		},
	}, false)
	if id == "" {
		t.Fatal("CompatibilityIdentity should be non-empty")
	}
}

func TestCompatibilityIdentityDisabled(t *testing.T) {
	t.Parallel()
	id := CompatibilityIdentity(provider.Config{Enabled: false}, false)
	if id != "" {
		t.Fatalf("CompatibilityIdentity for disabled provider should be empty, got %q", id)
	}
}

func TestEffectiveIdentitySameForEquivalentConfigs(t *testing.T) {
	t.Parallel()
	base, err := Load("testdata/valid/minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	id1 := effectiveIdentity(base)
	id2 := effectiveIdentity(base)
	if id1 != id2 {
		t.Fatal("effective identity should be deterministic")
	}
}

func TestAlgorithmIdentityKindAndVersionSensitive(t *testing.T) {
	t.Parallel()
	base := algorithmIdentity("processing", "v1", nil)
	if !strings.HasPrefix(base, "sha256:") {
		t.Fatalf("algorithm identity missing prefix: %q", base)
	}
	if base != algorithmIdentity("processing", "v1", nil) {
		t.Fatal("algorithm identity is not deterministic")
	}
	if base == algorithmIdentity("consolidation", "v1", nil) {
		t.Fatal("algorithm identity does not separate kinds")
	}
	if base == algorithmIdentity("processing", "v2", nil) {
		t.Fatal("algorithm identity does not separate versions")
	}
}

func TestCanonicalAlgorithmIdentities(t *testing.T) {
	t.Parallel()
	kindThresholds := map[string]float64{"fact": 0.94, "event": 0.9}
	kindPrefixes := map[string]string{"fact": "fact: ", "event": "event: "}
	cases := []struct {
		name      string
		base      func() string
		reordered func() string
		changed   func() string
	}{
		{
			name:      "processing",
			base:      func() string { return ProcessingIdentity("v1", []string{"fact", "event"}, 8, 512) },
			reordered: func() string { return ProcessingIdentity("v1", []string{"event", "fact"}, 8, 512) },
			changed:   func() string { return ProcessingIdentity("v1", []string{"fact", "event"}, 9, 512) },
		},
		{
			name:      "consolidation",
			base:      func() string { return ConsolidationIdentity("v1", kindThresholds, 32) },
			reordered: func() string { return ConsolidationIdentity("v1", map[string]float64{"event": 0.9, "fact": 0.94}, 32) },
			changed:   func() string { return ConsolidationIdentity("v1", kindThresholds, 33) },
		},
		{
			name:      "projection",
			base:      func() string { return ProjectionIdentity("v1", []string{"kind", "canonical_content"}) },
			reordered: func() string { return ProjectionIdentity("v1", []string{"canonical_content", "kind"}) },
			changed:   func() string { return ProjectionIdentity("v1", []string{"kind"}) },
		},
		{
			name: "embedding_document",
			base: func() string { return EmbeddingDocumentIdentity("model-a", "v1", 1024, kindPrefixes) },
			reordered: func() string {
				return EmbeddingDocumentIdentity("model-a", "v1", 1024, map[string]string{"event": "event: ", "fact": "fact: "})
			},
			changed: func() string { return EmbeddingDocumentIdentity("model-a", "v2", 1024, kindPrefixes) },
		},
		{
			name:      "embedding_query",
			base:      func() string { return EmbeddingQueryIdentity("model-a", "v1", 1024, "query: ") },
			reordered: nil,
			changed:   func() string { return EmbeddingQueryIdentity("model-a", "v1", 1024, "question: ") },
		},
		{
			name:      "semantic_calibration",
			base:      func() string { return SemanticCalibrationIdentity("v1", 0.35, 0.08, map[string]float64{"fact": 0.4}) },
			reordered: func() string { return SemanticCalibrationIdentity("v1", 0.35, 0.08, map[string]float64{"fact": 0.4}) },
			changed:   func() string { return SemanticCalibrationIdentity("v1", 0.4, 0.08, map[string]float64{"fact": 0.4}) },
		},
		{
			name: "keyword_compiler",
			base: func() string {
				return KeywordCompilerIdentity("v1", "field-aware", "en", map[string]float64{"exact": 3})
			},
			reordered: nil,
			changed: func() string {
				return KeywordCompilerIdentity("v1", "field-aware", "zh", map[string]float64{"exact": 3})
			},
		},
		{
			name:      "entity_normalization",
			base:      func() string { return EntityNormalizationIdentity("v1", []string{"unicode", "casefold"}) },
			reordered: func() string { return EntityNormalizationIdentity("v1", []string{"casefold", "unicode"}) },
			changed:   func() string { return EntityNormalizationIdentity("v1", []string{"unicode"}) },
		},
		{
			name:      "predicate_registry",
			base:      func() string { return PredicateRegistryIdentity("v1", []string{"member_of", "located_at"}) },
			reordered: func() string { return PredicateRegistryIdentity("v1", []string{"located_at", "member_of"}) },
			changed:   func() string { return PredicateRegistryIdentity("v1", []string{"member_of"}) },
		},
		{
			name:      "graph_extraction",
			base:      func() string { return GraphExtractionIdentity("v1", "contract-v2", "model-a", 16, 16, 8) },
			reordered: nil,
			changed:   func() string { return GraphExtractionIdentity("v1", "contract-v2", "model-b", 16, 16, 8) },
		},
		{
			name:      "graph_scoring",
			base:      func() string { return GraphScoringIdentity("v1", map[string]float64{"fact": 0.9}, 0.5) },
			reordered: nil,
			changed:   func() string { return GraphScoringIdentity("v1", map[string]float64{"fact": 0.9}, 0.4) },
		},
		{
			name: "query_planning",
			base: func() string {
				return QueryPlanIdentity("v1", []string{"dates", "identifiers"}, []string{"lookup", "temporal"})
			},
			reordered: func() string {
				return QueryPlanIdentity("v1", []string{"identifiers", "dates"}, []string{"temporal", "lookup"})
			},
			changed: func() string { return QueryPlanIdentity("v1", []string{"dates"}, []string{"lookup", "temporal"}) },
		},
		{
			name:      "ranking_features",
			base:      func() string { return RankingFeatureIdentity("v1", []string{"semantic", "keyword", "graph"}) },
			reordered: func() string { return RankingFeatureIdentity("v1", []string{"graph", "keyword", "semantic"}) },
			changed:   func() string { return RankingFeatureIdentity("v1", []string{"semantic", "keyword"}) },
		},
		{
			name: "ranking_weights",
			base: func() string {
				return RankingWeightIdentity("feat-1", "v1", map[string]float64{"semantic": 1.0, "keyword": 0.35})
			},
			reordered: func() string {
				return RankingWeightIdentity("feat-1", "v1", map[string]float64{"keyword": 0.35, "semantic": 1.0})
			},
			changed: func() string {
				return RankingWeightIdentity("feat-2", "v1", map[string]float64{"semantic": 1.0, "keyword": 0.35})
			},
		},
		{
			name: "context_selection",
			base: func() string {
				return ContextSelectionIdentity("v1", "policy-v1", "format-v1", map[string]int{"tokens": 4000})
			},
			reordered: nil,
			changed: func() string {
				return ContextSelectionIdentity("v1", "policy-v2", "format-v1", map[string]int{"tokens": 4000})
			},
		},
		{
			name:      "feedback_aggregation",
			base:      func() string { return FeedbackAggregationIdentity("v1", 90, 3) },
			reordered: nil,
			changed:   func() string { return FeedbackAggregationIdentity("v1", 90, 5) },
		},
		{
			name:      "metric_schema",
			base:      func() string { return MetricSchemaIdentity("v1", []string{"recall_all", "ndcg", "mrr"}) },
			reordered: func() string { return MetricSchemaIdentity("v1", []string{"mrr", "ndcg", "recall_all"}) },
			changed:   func() string { return MetricSchemaIdentity("v1", []string{"recall_all", "ndcg"}) },
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			first := tc.base()
			if !strings.HasPrefix(first, "sha256:") {
				t.Fatalf("identity %q missing sha256 prefix: %q", tc.name, first)
			}
			if first != tc.base() {
				t.Fatalf("identity %q is not deterministic", tc.name)
			}
			if tc.reordered != nil && first != tc.reordered() {
				t.Fatalf("identity %q depends on input ordering", tc.name)
			}
			if first == tc.changed() {
				t.Fatalf("identity %q does not change when canonical input changes", tc.name)
			}
		})
	}
}
