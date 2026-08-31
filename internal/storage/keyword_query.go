package storage

import (
	"strings"
	"unicode"
)

const maximumKeywordTerms = 24
const minimumLatinKeywordRunes = 4

var englishKeywordStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "at": {}, "did": {}, "do": {}, "does": {}, "for": {}, "from": {}, "how": {}, "i": {}, "in": {}, "is": {}, "it": {}, "my": {}, "of": {}, "on": {}, "or": {}, "the": {}, "to": {}, "was": {}, "were": {}, "what": {}, "when": {}, "where": {}, "which": {}, "who": {}, "why": {}, "with": {}, "would": {},
}

// compileKeywordQuery converts user text into a literal, disjunctive FTS5 query.
func compileKeywordQuery(input string) (string, bool) {
	terms := make([]string, 0, maximumKeywordTerms)
	seen := make(map[string]struct{})
	add := func(term string) {
		if len(terms) == maximumKeywordTerms {
			return
		}
		key := strings.ToLower(term)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		terms = append(terms, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}

	runes := []rune(input)
	for start := 0; start < len(runes); {
		if unicode.Is(unicode.Han, runes[start]) {
			end := start + 1
			for end < len(runes) && unicode.Is(unicode.Han, runes[end]) {
				end++
			}
			for i := start; i+3 <= end; i++ {
				add(string(runes[i : i+3]))
			}
			start = end
			continue
		}
		if unicode.IsLetter(runes[start]) || unicode.IsDigit(runes[start]) {
			end := start + 1
			for end < len(runes) && !unicode.Is(unicode.Han, runes[end]) && (unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end]) || runes[end] == '-') {
				end++
			}
			term := strings.Trim(string(runes[start:end]), "-")
			if utf8RuneCount(term) >= minimumLatinKeywordRunes {
				if _, stopWord := englishKeywordStopWords[strings.ToLower(term)]; !stopWord {
					add(term)
				}
			}
			start = end
			continue
		}
		start++
	}
	if len(terms) == 0 {
		return "", false
	}
	return strings.Join(terms, " OR "), true
}

func utf8RuneCount(value string) int {
	return len([]rune(value))
}
