package retrieval

import (
	"math"
	"strings"
	"unicode"
)

const maximumKeywordTerms = 32

// TermKind classifies a compiled keyword term. The kind drives weighting and
// whether the term is required or optional.
type TermKind string

const (
	TermWord       TermKind = "word"
	TermAcronym    TermKind = "acronym"
	TermIdentifier TermKind = "identifier"
	TermNumber     TermKind = "number"
	TermDate       TermKind = "date"
	TermPhrase     TermKind = "phrase"
	TermCJK        TermKind = "cjk"
)

// KeywordTerm is one compiled, field-aware keyword clause.
type KeywordTerm struct {
	Text     string   `json:"text"`
	Kind     TermKind `json:"kind"`
	Required bool     `json:"required"`
	Exact    bool     `json:"exact"`
	Weight   float64  `json:"weight"`
	MinRunes int      `json:"min_runes"`
	Fallback bool     `json:"fallback"`
}

// KeywordQuery is the deterministic output of the keyword compiler. Match is a
// literal FTS5 expression. ShortTerms are terms too short for the trigram
// tokenizer and are served by a literal substring fallback. Segments are the
// query's CJK content spans between function words; they give the literal
// recall path a word-boundary level of precision without a segmentation
// dependency.
type KeywordQuery struct {
	Version    string        `json:"version"`
	Match      string        `json:"match"`
	Terms      []KeywordTerm `json:"terms"`
	ShortTerms []string      `json:"short_terms"`
	Segments   []string      `json:"segments"`
	Searchable bool          `json:"searchable"`
}

var keywordStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "at": {}, "be": {}, "did": {}, "do": {}, "does": {},
	"for": {}, "from": {}, "had": {}, "has": {}, "have": {}, "how": {}, "i": {}, "in": {}, "is": {},
	"it": {}, "my": {}, "of": {}, "on": {}, "or": {}, "that": {}, "the": {}, "this": {}, "to": {},
	"was": {}, "were": {}, "what": {}, "when": {}, "where": {}, "which": {}, "who": {}, "why": {},
	"with": {}, "would": {}, "you": {}, "your": {},
}

// CompileKeyword builds one versioned, field-aware keyword query. It preserves
// short proper names, acronyms, identifiers, dates, numbers, and quoted
// phrases regardless of generic token-length rules and emits CJK unigram/bigram
// fallback when trigrams cannot be formed.
func CompileKeyword(input string) KeywordQuery {
	result := KeywordQuery{Version: KeywordCompilerVersion}
	if strings.TrimSpace(input) == "" {
		return result
	}
	seen := map[string]struct{}{}
	add := func(term KeywordTerm) {
		if term.Text == "" || len(result.Terms) >= maximumKeywordTerms {
			return
		}
		key := strings.ToLower(term.Text)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		if term.Width() < 3 {
			term.Fallback = true
			result.ShortTerms = append(result.ShortTerms, term.Text)
		}
		result.Terms = append(result.Terms, term)
	}

	remaining := stripQuotedPhrases(input, add)
	runes := []rune(remaining)
	for start := 0; start < len(runes); {
		r := runes[start]
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			end := start + 1
			for end < len(runes) && (unicode.Is(unicode.Han, runes[end]) || unicode.Is(unicode.Hiragana, runes[end]) || unicode.Is(unicode.Katakana, runes[end])) {
				end++
			}
			segment := runes[start:end]
			if len(segment) >= 3 {
				for i := 0; i+3 <= len(segment); i++ {
					add(KeywordTerm{Text: string(segment[i : i+3]), Kind: TermCJK, Weight: 0.8, Required: false, MinRunes: 3})
				}
			} else {
				add(KeywordTerm{Text: string(segment), Kind: TermCJK, Weight: 0.8, Required: false, MinRunes: len(segment)})
			}
			start = end
			continue
		}
		if isTokenRune(r) {
			end := start + 1
			for end < len(runes) && isTokenRune(runes[end]) {
				end++
			}
			token := strings.Trim(string(runes[start:end]), "._-")
			add(classifyToken(token))
			start = end
			continue
		}
		start++
	}
	if len(result.Terms) == 0 {
		return result
	}
	result.Searchable = true
	result.Match = buildKeywordMatch(result.Terms)
	result.Segments = CompileQuerySegments(input)
	return result
}

// maximumQuerySegments bounds the phrase-level precision tier.
const maximumQuerySegments = 8

// cjkFunctionWords are closed-class Chinese words and question markers that
// separate content spans in a natural-language query. They carry little
// retrieval signal and splitting on them is deterministic, so the compiler
// never needs a lexicon or a segmentation model to recover word boundaries.
var cjkFunctionWords = map[rune]struct{}{
	'的': {}, '了': {}, '是': {}, '在': {}, '和': {}, '与': {}, '及': {}, '或': {},
	'把': {}, '被': {}, '对': {}, '从': {}, '到': {}, '由': {}, '向': {}, '给': {},
	'为': {}, '就': {}, '都': {}, '也': {}, '很': {}, '会': {}, '要': {}, '请': {},
	'哪': {}, '那': {}, '这': {}, '谁': {}, '啥': {}, '吗': {}, '呢': {}, '吧': {},
	'啊': {}, '呀': {}, '么': {}, '什': {}, '怎': {}, '如': {}, '何': {}, '有': {},
	'没': {}, '不': {}, '能': {}, '可': {}, '以': {}, '并': {}, '而': {}, '但': {},
}

// CompileQuerySegments splits a query into CJK content spans at function-word
// boundaries. A span is at least two runes and at most maximumQuerySegments
// spans are returned. The result is empty when the split does not actually
// partition the query: requiring every span of a query that never reaches a
// function word would tighten recall without adding any word-boundary
// information.
func CompileQuerySegments(input string) []string {
	runes := []rune(input)
	segments := make([]string, 0, maximumQuerySegments)
	current := make([]rune, 0, len(runes))
	flush := func() {
		if len([]rune(current)) >= 2 && len(segments) < maximumQuerySegments {
			segments = append(segments, string(current))
		}
		current = current[:0]
	}
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) {
			if _, boundary := cjkFunctionWords[r]; boundary {
				flush()
				continue
			}
			current = append(current, r)
			continue
		}
		flush()
	}
	flush()
	if len(segments) < 2 {
		return nil
	}
	// The split must actually separate content. Every span already has at least
	// two runes, and at least one query rune must fall outside the spans, which
	// is what makes the spans a genuine word-boundary partition rather than the
	// whole query restated.
	total := 0
	for _, segment := range segments {
		total += len([]rune(segment))
	}
	if total >= len(runes) {
		return nil
	}
	return segments
}

func (t KeywordTerm) Width() int {
	return len([]rune(t.Text))
}

func isTokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.'
}

func classifyToken(token string) KeywordTerm {
	lower := strings.ToLower(token)
	switch {
	case token == "":
		return KeywordTerm{}
	case isDateToken(token):
		return KeywordTerm{Text: token, Kind: TermDate, Exact: true, Weight: 1.0}
	case isIdentifier(token):
		return KeywordTerm{Text: token, Kind: TermIdentifier, Required: true, Exact: true, Weight: 1.0}
	case isAcronym(token):
		return KeywordTerm{Text: token, Kind: TermAcronym, Required: true, Exact: true, Weight: 1.0}
	case isNumber(token):
		return KeywordTerm{Text: token, Kind: TermNumber, Exact: true, Weight: 0.9}
	}
	if _, stop := keywordStopWords[lower]; stop {
		return KeywordTerm{Text: token, Kind: TermWord, Weight: 0.1}
	}
	return KeywordTerm{Text: token, Kind: TermWord, Weight: 0.6}
}

func isDateToken(token string) bool {
	return datePattern.MatchString(token)
}

func stripQuotedPhrases(input string, add func(KeywordTerm)) string {
	var builder strings.Builder
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '"' {
			end := i + 1
			for end < len(runes) && runes[end] != '"' {
				end++
			}
			if end < len(runes) {
				phrase := strings.TrimSpace(string(runes[i+1 : end]))
				if phrase != "" {
					add(KeywordTerm{Text: phrase, Kind: TermPhrase, Required: true, Exact: true, Weight: 1.0})
				}
				i = end
				continue
			}
		}
		builder.WriteRune(runes[i])
	}
	return builder.String()
}

func buildKeywordMatch(terms []KeywordTerm) string {
	var required, optional []string
	for _, term := range terms {
		if term.Fallback {
			continue
		}
		clause := quoteFTS(term.Text)
		if term.Required {
			required = append(required, clause)
		} else {
			optional = append(optional, clause)
		}
	}
	var parts []string
	if len(required) > 0 {
		parts = append(parts, strings.Join(required, " AND "))
	}
	if len(optional) > 0 {
		optionalClause := strings.Join(optional, " OR ")
		if len(required) > 0 {
			optionalClause = "(" + optionalClause + ")"
		}
		parts = append(parts, optionalClause)
	}
	return strings.Join(parts, " AND ")
}

func quoteFTS(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// Coverage reports the fraction of compiled terms whose literal text appears in
// the content. It is query-local and deterministic.
func Coverage(content string, terms []KeywordTerm) float64 {
	if len(terms) == 0 {
		return 0
	}
	lower := strings.ToLower(content)
	matched := 0
	for _, term := range terms {
		if strings.Contains(lower, strings.ToLower(term.Text)) {
			matched++
		}
	}
	return float64(matched) / float64(len(terms))
}

// ExactMatched reports whether every required exact term appears literally in
// the content.
func ExactMatched(content string, terms []KeywordTerm) bool {
	lower := strings.ToLower(content)
	found := false
	for _, term := range terms {
		if !term.Exact {
			continue
		}
		found = true
		if !strings.Contains(lower, strings.ToLower(term.Text)) {
			return false
		}
	}
	return found
}

// TermWeight applies inverse-document-frequency scaling to a base kind weight
// so rare terms contribute more than common conversational words.
func TermWeight(base float64, documentFrequency, totalDocuments int) float64 {
	if totalDocuments <= 0 || documentFrequency < 0 {
		return base
	}
	idf := math.Log(1 + float64(totalDocuments)/float64(1+documentFrequency))
	return base * (1 + idf)
}
