package config

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/N0rthHXD/dMem/internal/gateway/review"
)

func strPtr(v string) *string     { return &v }
func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }
func boolPtr(v bool) *bool        { return &v }

func TestNewSectionsDefaultWhenAbsent(t *testing.T) {
	t.Parallel()
	cfg, err := Load("testdata/valid/minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Consolidation != defaultConsolidationConfig() {
		t.Fatalf("consolidation default = %+v", cfg.Consolidation)
	}
	if cfg.Semantic != defaultSemanticConfig() {
		t.Fatalf("semantic default = %+v", cfg.Semantic)
	}
	if cfg.Graph != defaultGraphConfig() {
		t.Fatalf("graph default = %+v", cfg.Graph)
	}
	if cfg.Ranking != defaultRankingConfig() {
		t.Fatalf("ranking default = %+v", cfg.Ranking)
	}
	if cfg.Context != defaultContextConfig() {
		t.Fatalf("context default = %+v", cfg.Context)
	}
}

func TestConsolidationConfigBounds(t *testing.T) {
	t.Parallel()
	base := defaultConsolidationConfig()
	for _, tc := range []struct {
		name string
		edit func(*consolidationTable)
		want bool
	}{
		{"defaults ok", func(*consolidationTable) {}, false},
		{"zero candidates", func(c *consolidationTable) { c.MaxCandidates = intPtr(0) }, true},
		{"too many candidates", func(c *consolidationTable) { c.MaxCandidates = intPtr(1024) }, true},
		{"cosine out of range", func(c *consolidationTable) { c.SameSubjectKindCosine = floatPtr(1.5) }, true},
		{"cosine order inverted", func(c *consolidationTable) {
			c.SameSourceEventCosine = floatPtr(0.99)
			c.SameSubjectKindCosine = floatPtr(0.90)
		}, true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			table := consolidationTable{
				Version:               strPtr(base.Version),
				MaxCandidates:         intPtr(base.MaxCandidates),
				SameSourceEventCosine: floatPtr(base.SameSourceEventCosine),
				SameSubjectKindCosine: floatPtr(base.SameSubjectKindCosine),
			}
			tc.edit(&table)
			_, err := buildConsolidationConfig(table)
			if tc.want && err == nil {
				t.Fatal("expected rejection")
			}
			if !tc.want && err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
		})
	}
}

func TestSemanticConfigBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*semanticTable)
		want bool
	}{
		{"defaults ok", func(*semanticTable) {}, false},
		{"floor high", func(c *semanticTable) { c.AbsoluteFloor = floatPtr(1.1) }, true},
		{"margin negative", func(c *semanticTable) { c.TopMargin = floatPtr(-0.1) }, true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			table := semanticTable{CalibrationVersion: strPtr("v1"), AbsoluteFloor: floatPtr(0.35), TopMargin: floatPtr(0.08)}
			tc.edit(&table)
			_, err := buildSemanticConfig(table)
			if (err != nil) != tc.want {
				t.Fatalf("err = %v; want rejected=%v", err, tc.want)
			}
		})
	}
}

func TestGraphConfigPolicyAndBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*graphTable)
		want bool
	}{
		{"defaults ok", func(*graphTable) {}, false},
		{"unknown policy", func(c *graphTable) { c.Policy = strPtr("always") }, true},
		{"entity confidence out of range", func(c *graphTable) { c.MinEntityConfidence = floatPtr(-0.1) }, true},
		{"boost cap zero", func(c *graphTable) { c.BoostCap = floatPtr(0) }, true},
		{"bounded path ok", func(c *graphTable) { c.Policy = strPtr("bounded_path") }, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			table := graphTable{
				Policy:              strPtr("features_only"),
				MinEntityConfidence: floatPtr(0.6),
				MinFactConfidence:   floatPtr(0.7),
				BoostCap:            floatPtr(0.08),
			}
			tc.edit(&table)
			_, err := buildGraphConfig(table)
			if (err != nil) != tc.want {
				t.Fatalf("err = %v; want rejected=%v", err, tc.want)
			}
		})
	}
}

func TestRankingConfigBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*rankingTable)
		want bool
	}{
		{"defaults ok", func(*rankingTable) {}, false},
		{"semantic weight zero", func(c *rankingTable) { c.SemanticWeight = floatPtr(0) }, true},
		{"weight too large", func(c *rankingTable) { c.KeywordWeight = floatPtr(20) }, true},
		{"custom weights ok", func(c *rankingTable) { c.KeywordWeight = floatPtr(0.5) }, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			table := rankingTable{Version: strPtr("v1"), SemanticWeight: floatPtr(1.0)}
			tc.edit(&table)
			_, err := buildRankingConfig(table)
			if (err != nil) != tc.want {
				t.Fatalf("err = %v; want rejected=%v", err, tc.want)
			}
		})
	}
}

func TestContextConfigBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*contextTable)
		want bool
	}{
		{"defaults ok", func(*contextTable) {}, false},
		{"zero tokens", func(c *contextTable) { c.MaxContextTokens = intPtr(0) }, true},
		{"chars below tokens", func(c *contextTable) { c.MaxContextChars = intPtr(10) }, true},
		{"too many results", func(c *contextTable) { c.MaxResults = intPtr(10000) }, true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			table := contextTable{
				MaxContextTokens: intPtr(4000),
				MaxContextChars:  intPtr(16000),
				MaxResults:       intPtr(20),
			}
			tc.edit(&table)
			_, err := buildContextConfig(table)
			if (err != nil) != tc.want {
				t.Fatalf("err = %v; want rejected=%v", err, tc.want)
			}
		})
	}
}

func TestNewSectionsAffectEffectiveIdentity(t *testing.T) {
	t.Parallel()
	base, err := Load("testdata/valid/minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.Ranking.KeywordWeight += 0.25
	if effectiveIdentity(base) == effectiveIdentity(changed) {
		t.Fatal("effective identity should change when ranking weights change")
	}
	disabled := base
	disabled.Review.AutoConfirmEnabled = false
	if effectiveIdentity(base) == effectiveIdentity(disabled) {
		t.Fatal("effective identity should change when candidate auto-confirmation changes")
	}
}

func TestReviewConfigDefaultsAndBounds(t *testing.T) {
	t.Parallel()
	defaults := defaultReviewConfig()
	if !defaults.AutoConfirmEnabled {
		t.Fatal("candidate auto-confirmation must default to enabled")
	}
	if defaults.AutoConfirmVersion == "" || len(defaults.AutoConfirmKinds) == 0 || defaults.AutoConfirmMaxPerSpacePerHour < 1 {
		t.Fatalf("default review config = %+v; want every bound named", defaults)
	}
	loaded, err := Load("testdata/valid/minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Review.AutoConfirmEnabled {
		t.Fatal("a config without a review section must inherit the enabled default")
	}
	if !reflect.DeepEqual(loaded.Review, defaults) {
		t.Fatalf("review default = %+v; want %+v", loaded.Review, defaults)
	}

	for _, tc := range []struct {
		name string
		edit func(*reviewTable)
		want bool
	}{
		{"defaults ok", func(*reviewTable) {}, false},
		{"enabled with explicit kinds", func(c *reviewTable) {
			c.AutoConfirm.Enabled = boolPtr(true)
			c.AutoConfirm.Kinds = []string{"event", "fact"}
		}, false},
		{"empty version", func(c *reviewTable) { c.AutoConfirm.Version = strPtr("") }, true},
		{"confidence above one", func(c *reviewTable) { c.AutoConfirm.MinConfidence = floatPtr(1.5) }, true},
		{"confidence zero", func(c *reviewTable) { c.AutoConfirm.MinConfidence = floatPtr(0) }, true},
		{"rate limit zero", func(c *reviewTable) { c.AutoConfirm.MaxPerSpacePerHour = intPtr(0) }, true},
		{"rate limit above the bound", func(c *reviewTable) {
			c.AutoConfirm.MaxPerSpacePerHour = intPtr(maxAutoConfirmsPerSpacePerHour + 1)
		}, true},
		{"unknown kind", func(c *reviewTable) { c.AutoConfirm.Kinds = []string{"opinion"} }, true},
		{"aggregate kind", func(c *reviewTable) { c.AutoConfirm.Kinds = []string{"topic"} }, true},
		{"duplicate kind", func(c *reviewTable) { c.AutoConfirm.Kinds = []string{"fact", "fact"} }, true},
		{"enabled without kinds", func(c *reviewTable) {
			c.AutoConfirm.Enabled = boolPtr(true)
			c.AutoConfirm.Kinds = []string{}
		}, true},
		{"disabled without kinds", func(c *reviewTable) {
			c.AutoConfirm.Enabled = boolPtr(false)
			c.AutoConfirm.Kinds = []string{}
		}, false},
		{"empty processing version entry", func(c *reviewTable) { c.AutoConfirm.ProcessingVersions = []string{""} }, true},
		{"too many processing versions", func(c *reviewTable) {
			versions := make([]string, maxAutoConfirmProcessingVersions+1)
			for i := range versions {
				versions[i] = "version-" + strconv.Itoa(i)
			}
			c.AutoConfirm.ProcessingVersions = versions
		}, true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			table := reviewTable{AutoConfirm: reviewAutoConfirmTable{
				Enabled:            boolPtr(defaults.AutoConfirmEnabled),
				Version:            strPtr(defaults.AutoConfirmVersion),
				MinConfidence:      floatPtr(defaults.AutoConfirmMinConfidence),
				Kinds:              append([]string(nil), defaults.AutoConfirmKinds...),
				MaxPerSpacePerHour: intPtr(defaults.AutoConfirmMaxPerSpacePerHour),
			}}
			tc.edit(&table)
			_, err := buildReviewConfig(table)
			if tc.want && err == nil {
				t.Fatal("expected rejection")
			}
			if !tc.want && err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
		})
	}
}

func TestAutoConfirmBoundsMatchReviewModule(t *testing.T) {
	t.Parallel()
	if maxAutoConfirmsPerSpacePerHour != review.MaxAutoConfirmsPerSpacePerHour {
		t.Fatalf("rate-limit bound = %d; review module publishes %d", maxAutoConfirmsPerSpacePerHour, review.MaxAutoConfirmsPerSpacePerHour)
	}
	if maxAutoConfirmProcessingVersions != review.MaxAutoConfirmVersions {
		t.Fatalf("version bound = %d; review module publishes %d", maxAutoConfirmProcessingVersions, review.MaxAutoConfirmVersions)
	}
}
