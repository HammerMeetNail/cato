package games

import (
	"strings"
)

func NormalizeName(input string) string {
	input = strings.ToLower(input)
	input = strings.ReplaceAll(input, "'", "")
	input = strings.ReplaceAll(input, "-", " ")
	input = strings.ReplaceAll(input, ":", " ")
	fields := strings.Fields(input)
	return strings.Join(fields, " ")
}

// fts5SpecialChars are characters FTS5 treats as part of its query syntax.
// We strip them so user input can't accidentally construct a malformed or
// overly-broad MATCH expression. See https://www.sqlite.org/fts5.html#full_text_query_syntax.
const fts5SpecialChars = `:*"^()+-"`

// BuildFTSMatch converts a normalized user query into an FTS5 MATCH expression
// suitable for the trigram tokenizer. The trigram tokenizer requires the query
// string as a whole to be >= 3 chars to produce any trigrams; shorter queries
// can't be served by FTS (caller falls back to LIKE prefix). All whitespace-
// separated tokens are kept in the phrase — including 2-char ones like "of" —
// because dropping them would break the trigram sequence and fail to match
// documents that contain them (e.g. "the legend of zelda").
func BuildFTSMatch(query string) (string, bool) {
	query = strings.TrimSpace(query)
	if len(query) < 3 {
		return "", false
	}
	tokens := strings.Fields(query)
	if len(tokens) == 0 {
		return "", false
	}
	for i, tok := range tokens {
		tokens[i] = sanitizeFTSToken(tok)
	}
	// A single quoted phrase produces the strongest match for multi-word
	// queries with the trigram tokenizer.
	return `"` + strings.Join(tokens, " ") + `"`, true
}

func sanitizeFTSToken(token string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(fts5SpecialChars, r) {
			return -1
		}
		return r
	}, token)
}
