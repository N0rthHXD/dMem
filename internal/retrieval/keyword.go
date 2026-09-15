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
// tokenizer and are served by a literal substring fallback.
type KeywordQuery struct {
	Version    string        `json:"version"`
	Match      string        `json:"match"`
	Terms      []KeywordTerm `json:"terms"`
	ShortTerms []string      `json:"short_terms"`
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
	return result
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
