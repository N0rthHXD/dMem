// Package retrieval defines the versioned, deterministic query-understanding,
// keyword-compilation, calibration, temporal, and feature-ranking contracts
// used by primary recall. Every behavior-changing contract is versioned; a
// change to any rule requires a new version constant and a fresh cursor
// identity so stale cursors and checkpoints cannot be replayed silently.
package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Versioned algorithm identities. These constants are the single source of
// truth for query planning, keyword compilation, semantic calibration,
// temporal features, graph scoring, and feature ranking.
const (
	QueryPlanVersion       = "query-plan-v2"
	KeywordCompilerVersion = "keyword-compiler-v2"
	SemanticCalibrationVer = "semantic-calibration-v1"
	TemporalFeatureVersion = "temporal-features-v1"
	GraphScoringVersion    = "graph-scoring-v3"
	RankingFeatureVersion  = "ranking-features-v1"
	RankingWeightsVersion  = "ranking-weights-v1"
)

// QuestionType is the deterministic classification of a recall query.
type QuestionType string

const (
	QuestionLookup       QuestionType = "lookup"
	QuestionTemporal     QuestionType = "temporal"
	QuestionEntity       QuestionType = "entity"
	QuestionRelational   QuestionType = "relational"
	QuestionMultiHop     QuestionType = "multi_hop"
	QuestionProcedural   QuestionType = "procedural"
	QuestionExploratory  QuestionType = "exploratory"
	QuestionVerification QuestionType = "verification"
)

// GraphPolicy is the query-local graph trust policy.
type GraphPolicy string

const (
	GraphOff          GraphPolicy = "off"
	GraphFeaturesOnly GraphPolicy = "features_only"
	GraphDirect       GraphPolicy = "direct"
	GraphBoundedPath  GraphPolicy = "bounded_path"
)

func (p GraphPolicy) Valid() bool {
	switch p {
	case GraphOff, GraphFeaturesOnly, GraphDirect, GraphBoundedPath:
		return true
	}
	return false
}

// QueryEnrichment is the optional, bounded provider output for query planning.
// Every field is validated before use; invalid or out-of-bounds values are
// ignored so the deterministic local plan remains authoritative.
type QueryEnrichment struct {
	QuestionType QuestionType
	Predicates   []string
	MemoryKinds  []string
	Language     string
}

// QueryEnricher is an optional policy-gated provider analysis source. It must
// never be called unless egress policy permits it; EnrichPlan enforces the
// caller-provided allowance and falls back deterministically on any error.
type QueryEnricher interface {
	EnrichQuery(ctx context.Context, query string) (QueryEnrichment, bool, error)
}

// EnrichPlan applies a bounded provider enrichment when allowed, otherwise it
// returns the local plan unchanged.
func EnrichPlan(ctx context.Context, plan QueryPlan, enricher QueryEnricher, allowed bool, maxPredicates, maxKinds int) QueryPlan {
	if !allowed || enricher == nil {
		return plan
	}
	enrichment, ok, err := enricher.EnrichQuery(ctx, plan.NormalizedQuery)
	if err != nil || !ok {
		return plan
	}
	return plan.ApplyEnrichment(enrichment, maxPredicates, maxKinds)
}

// ApplyEnrichment merges a bounded enrichment deterministically.
func (p QueryPlan) ApplyEnrichment(enrichment QueryEnrichment, maxPredicates, maxKinds int) QueryPlan {
	if enrichment.QuestionType.Valid() {
		p.QuestionType = enrichment.QuestionType
		p.GraphPolicy = graphPolicyFor("", enrichment.QuestionType, p)
	}
	if enrichment.Language != "" {
		p.Language = enrichment.Language
	}
	if predicates := boundedUnique(enrichment.Predicates, maxPredicates); len(predicates) > 0 {
		p.TargetPredicates = uniqueSorted(append(append([]string{}, p.TargetPredicates...), predicates...))
	}
	if kinds := boundedUnique(enrichment.MemoryKinds, maxKinds); len(kinds) > 0 {
		p.TargetMemoryKinds = uniqueSorted(append(append([]string{}, p.TargetMemoryKinds...), kinds...))
	}
	return p
}

func (q QuestionType) Valid() bool {
	switch q {
	case QuestionLookup, QuestionTemporal, QuestionEntity, QuestionRelational, QuestionMultiHop,
		QuestionProcedural, QuestionExploratory, QuestionVerification:
		return true
	default:
		return false
	}
}

func boundedUnique(values []string, limit int) []string {
	if limit < 1 {
		return nil
	}
	unique := uniqueSorted(values)
	if len(unique) > limit {
		unique = unique[:limit]
	}
	return unique
}

// QueryPlan is the immutable, deterministic output of query analysis. It is
// produced before recall and is bound into query hashes and cursors so a plan
// change cannot reuse a stale candidate membership or ranking.
type QueryPlan struct {
	Version           string       `json:"version"`
	NormalizedQuery   string       `json:"normalized_query"`
	Language          string       `json:"language"`
	ExactTerms        []string     `json:"exact_terms"`
	Identifiers       []string     `json:"identifiers"`
	QuotedPhrases     []string     `json:"quoted_phrases"`
	Dates             []string     `json:"dates"`
	Numbers           []string     `json:"numbers"`
	TimeExpression    string       `json:"time_expression"`
	ReferenceTime     string       `json:"reference_time"`
	QuestionType      QuestionType `json:"question_type"`
	TargetMemoryKinds []string     `json:"target_memory_kinds"`
	TargetPredicates  []string     `json:"target_predicates"`
	RequestedScope    []string     `json:"requested_scope"`
	GraphPolicy       GraphPolicy  `json:"graph_policy"`
	CandidateBudget   int          `json:"candidate_budget"`
	ContextBudget     int          `json:"context_budget"`
}

// PlanOptions are the deterministic, caller-supplied inputs to query planning.
type PlanOptions struct {
	ReferenceTime  time.Time
	Intent         string
	Limit          int
	MaxChars       int
	RequestedKinds []string
	RequestedScope []string
}

var (
	quotedPhrasePattern = regexp.MustCompile(`"[^"]{1,128}"`)
	datePattern         = regexp.MustCompile(`\b\d{4}[-/]\d{1,2}[-/]\d{1,2}\b`)
	yearPattern         = regexp.MustCompile(`\b(?:19|20)\d{2}\b`)
	numberPattern       = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
	tokenPattern        = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._-]*`)
	relativeAgoPattern  = regexp.MustCompile(`\b\d+ (day|week|month|year)s? ago\b`)
)

var relativeTimePhrases = []string{
	"the day before yesterday", "the day after tomorrow", "last week", "this week", "next week",
	"last month", "this month", "next month", "last year", "this year", "next year",
	"yesterday", "today", "tomorrow",
}

var predicatePhraseMap = []struct {
	phrase    string
	predicate string
}{
	{"participates in", "participates_in"}, {"participate in", "participates_in"},
	{"works on", "works_on"}, {"worked on", "works_on"},
	{"uses database", "depends_on"}, {"use database", "depends_on"}, {"database is", "depends_on"},
	{"owned by", "owned_by"}, {"owns", "owned_by"},
	{"located in", "located_at"}, {"based in", "located_at"}, {"lives in", "located_at"},
	{"depends on", "depends_on"},
}

// BuildQueryPlan deterministically analyzes a query against a reference time.
// It never consults a provider and never uses randomness or map iteration order.
func BuildQueryPlan(query string, options PlanOptions) QueryPlan {
	reference := options.ReferenceTime
	if reference.IsZero() {
		reference = time.Unix(0, 0).UTC()
	}
	normalized := normalizeQuery(query)
	plan := QueryPlan{
		Version:         QueryPlanVersion,
		NormalizedQuery: normalized,
		Language:        detectLanguage(normalized),
		ReferenceTime:   reference.UTC().Format(time.RFC3339Nano),
		GraphPolicy:     GraphFeaturesOnly,
		CandidateBudget: options.Limit,
		ContextBudget:   options.MaxChars,
	}
	if plan.CandidateBudget < 1 {
		plan.CandidateBudget = 1
	}
	if plan.ContextBudget < 1 {
		plan.ContextBudget = 1
	}
	plan.RequestedScope = sortedCopy(options.RequestedScope)
	plan.TargetMemoryKinds = sortedCopy(options.RequestedKinds)

	lower := strings.ToLower(normalized)
	if phrases := quotedPhrasePattern.FindAllString(normalized, -1); len(phrases) > 0 {
		for _, phrase := range phrases {
			trimmed := strings.Trim(phrase, `"`)
			if trimmed != "" {
				plan.QuotedPhrases = append(plan.QuotedPhrases, trimmed)
			}
		}
	}
	for _, match := range datePattern.FindAllString(normalized, -1) {
		plan.Dates = append(plan.Dates, match)
	}
	for _, match := range tokenPattern.FindAllString(normalized, -1) {
		switch {
		case datePattern.MatchString(match):
			plan.Dates = append(plan.Dates, match)
		case isIdentifier(match):
			plan.Identifiers = append(plan.Identifiers, match)
		case isAcronym(match):
			plan.Identifiers = append(plan.Identifiers, match)
		case isNumber(match):
			plan.Numbers = append(plan.Numbers, match)
		case isYear(match) && !containsString(plan.Dates, match):
			plan.Dates = append(plan.Dates, match)
		}
	}
	plan.ExactTerms = mergeTerms(plan.QuotedPhrases, plan.Identifiers, plan.Dates, plan.Numbers)
	plan.Dates = uniqueSorted(plan.Dates)
	plan.Numbers = uniqueSorted(plan.Numbers)
	plan.Identifiers = uniqueSorted(plan.Identifiers)
	plan.QuotedPhrases = uniqueSorted(plan.QuotedPhrases)

	if expression, ok := matchRelativeTime(lower); ok {
		plan.TimeExpression = expression
	}
	if plan.TimeExpression == "" && len(plan.Dates) > 0 {
		plan.TimeExpression = plan.Dates[0]
	}
	plan.TargetPredicates = predicateIntent(lower)
	plan.QuestionType = classifyQuestion(lower, plan)
	plan.GraphPolicy = graphPolicyFor(options.Intent, plan.QuestionType, plan)
	return plan
}

// Identity returns a deterministic content hash of the plan. It changes when
// normalization, extracted terms, classification, policy, or budgets change.
func (p QueryPlan) Identity() string {
	canonical := p
	canonical.ExactTerms = sortedCopy(p.ExactTerms)
	canonical.Identifiers = sortedCopy(p.Identifiers)
	canonical.QuotedPhrases = sortedCopy(p.QuotedPhrases)
	canonical.Dates = sortedCopy(p.Dates)
	canonical.Numbers = sortedCopy(p.Numbers)
	canonical.TargetMemoryKinds = sortedCopy(p.TargetMemoryKinds)
	canonical.TargetPredicates = sortedCopy(p.TargetPredicates)
	canonical.RequestedScope = sortedCopy(p.RequestedScope)
	encoded, _ := json.Marshal(canonical)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// normalizeQuery collapses whitespace, normalizes Unicode width, and trims. It
// preserves case so quoted phrases and acronyms remain exact.
func normalizeQuery(query string) string {
	var builder strings.Builder
	previousSpace := false
	for _, r := range strings.TrimSpace(query) {
		if unicode.IsSpace(r) {
			if !previousSpace {
				builder.WriteRune(' ')
			}
			previousSpace = true
			continue
		}
		if r >= '\uFF01' && r <= '\uFF5E' {
			r = r - 0xFEE0
		}
		builder.WriteRune(r)
		previousSpace = false
	}
	return strings.TrimSpace(builder.String())
}

func detectLanguage(query string) string {
	for _, r := range query {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return "cjk"
		}
	}
	return "en"
}

func isIdentifier(token string) bool {
	hasDigit, hasLetter, hasSeparator := false, false, false
	for _, r := range token {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsLetter(r):
			hasLetter = true
		case r == '-' || r == '_' || r == '.':
			hasSeparator = true
		}
	}
	if hasLetter && (hasDigit || hasSeparator) {
		return true
	}
	return hasDigit && hasSeparator
}

func isAcronym(token string) bool {
	if len(token) < 2 || len(token) > 6 {
		return false
	}
	for _, r := range token {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func isNumber(token string) bool {
	if token == "" {
		return false
	}
	dot := false
	for _, r := range token {
		switch {
		case unicode.IsDigit(r):
		case r == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return true
}

func isYear(token string) bool {
	if len(token) != 4 {
		return false
	}
	return yearPattern.MatchString(token)
}

func matchRelativeTime(lower string) (string, bool) {
	for _, phrase := range relativeTimePhrases {
		if strings.Contains(lower, phrase) {
			return phrase, true
		}
	}
	if match := relativeAgoPattern.FindString(lower); match != "" {
		return match, true
	}
	return "", false
}

func classifyQuestion(lower string, plan QueryPlan) QuestionType {
	trimmed := strings.TrimSpace(lower)
	switch {
	case hasAnyPrefix(trimmed, []string{"is ", "are ", "was ", "were ", "did ", "does ", "do ", "has ", "have ", "can ", "will ", "should "}) && strings.Contains(lower, "?"):
		return QuestionVerification
	case hasAny(lower, []string{"how do i", "how to", "steps to", "process for", "procedure", "instructions"}):
		return QuestionProcedural
	case hasAny(lower, []string{"how are", "connected", "connection between"}) || hasAny(lower, []string{"how many", "how much"}) && hasAny(lower, []string{"then", "after", "before", "and"}):
		return QuestionMultiHop
	case hasAny(lower, []string{"when ", "what time", "what date", "which year", "how long", "how long ago", "last week", "last month", "last year", "yesterday", "today", "tomorrow"}):
		return QuestionTemporal
	case hasAny(lower, []string{"relationship", "related to", "who owns", "who is married", "who works", "who knows", "who manages", "who leads", "who reports"}) || len(plan.TargetPredicates) > 0:
		return QuestionRelational
	case hasAnyPrefix(trimmed, []string{"who is ", "who are "}) || hasAnyPrefix(trimmed, []string{"tell me about ", "what is "}) && len(plan.ExactTerms) > 0:
		return QuestionEntity
	case hasAny(lower, []string{"tell me about", "what do you know", "summary", "overview", "everything about", "explore"}):
		return QuestionExploratory
	default:
		return QuestionLookup
	}
}

func graphPolicyFor(intent string, question QuestionType, plan QueryPlan) GraphPolicy {
	switch intent {
	case "relational", "multi_hop":
		return GraphBoundedPath
	case "entity":
		return GraphDirect
	case "temporal", "lookup", "verification", "procedural", "exploratory":
		return GraphFeaturesOnly
	}
	switch question {
	case QuestionRelational, QuestionMultiHop:
		if len(plan.ExactTerms) > 0 || len(plan.TargetPredicates) > 0 {
			return GraphBoundedPath
		}
		return GraphFeaturesOnly
	case QuestionEntity:
		if len(plan.ExactTerms) > 0 {
			return GraphDirect
		}
		return GraphFeaturesOnly
	default:
		return GraphFeaturesOnly
	}
}

func predicateIntent(lower string) []string {
	var out []string
	for _, m := range predicatePhraseMap {
		if strings.Contains(lower, m.phrase) {
			out = append(out, m.predicate)
		}
	}
	return uniqueSorted(out)
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func hasAny(value string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func mergeTerms(groups ...[]string) []string {
	var merged []string
	for _, group := range groups {
		merged = append(merged, group...)
	}
	return uniqueSorted(merged)
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
