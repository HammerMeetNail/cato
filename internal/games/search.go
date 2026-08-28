package games

import (
	"strings"
	"unicode"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

func stripDiacritics(s string) string {
	t := transform.Chain(norm.NFD, transform.RemoveFunc(func(r rune) bool {
		return unicode.Is(unicode.Mn, r)
	}), norm.NFC)
	result, _, _ := transform.String(t, s)
	return result
}

var nonDecomposingReplacer = strings.NewReplacer(
	"æ", "ae",
	"œ", "oe",
	"ø", "o",
	"å", "a",
	"ß", "ss",
	"ł", "l",
	"đ", "d",
	"þ", "th",
	"ð", "d",
	"ı", "i",
)

func NormalizeName(input string) string {
	input = strings.ToLower(input)
	input = stripDiacritics(input)
	input = nonDecomposingReplacer.Replace(input)
	input = strings.ReplaceAll(input, "’", "")
	input = strings.ReplaceAll(input, "‘", "")
	input = strings.ReplaceAll(input, "'", "")
	input = strings.ReplaceAll(input, "–", " ")
	input = strings.ReplaceAll(input, "—", " ")
	input = strings.ReplaceAll(input, "-", " ")
	input = strings.ReplaceAll(input, ":", " ")
	fields := strings.Fields(input)
	return strings.Join(fields, " ")
}

// EditionPhrases are normalized substrings that indicate an explicit
// search for an edition/version (IGDB version_parent). When a query contains
// one of these, the default edition hiding is bypassed — e.g. searching
// "batman deluxe edition" should return the deluxe edition even though
// "batman" alone hides editions. Single-word entries (deluxe, goty, etc.)
// are matched on word boundaries to avoid false positives like "golden".
var EditionPhrases = []string{
	"deluxe edition",
	"deluxe",
	"collectors edition",
	"collector edition",
	"collectors",
	"collector",
	"complete edition",
	"definitive edition",
	"definitive",
	"game of the year edition",
	"game of the year",
	"goty edition",
	"goty",
	"ultimate edition",
	"gold edition",
	"premium edition",
	"special edition",
	"limited edition",
	"legendary edition",
	"enhanced edition",
	"anniversary edition",
	"anniversary",
	"directors cut",
}

// ContainsEditionKeyword reports whether the already-normalized query
// explicitly asks for an edition.
func ContainsEditionKeyword(normalized string) bool {
	fields := strings.Fields(normalized)
	fieldSet := make(map[string]bool, len(fields))
	for _, f := range fields {
		fieldSet[f] = true
	}
	for _, p := range EditionPhrases {
		if strings.Contains(p, " ") {
			if strings.Contains(normalized, p) {
				return true
			}
		} else {
			if fieldSet[p] {
				return true
			}
		}
	}
	return false
}

// PackPhrases are normalized substrings that indicate an explicit search
// for a pack/skin/bundle addon (IGDB game_type 13 pack, 3 bundle, etc).
// Like editions, these are hidden by default unless the query asks for them
// or the includeEditions toggle is on.
var PackPhrases = []string{
	"skin",
	"skins",
	"pack",
	"bundle",
	"costume",
	"outfit",
}

// ContainsPackKeyword reports whether the already-normalized query
// explicitly asks for a pack/skin/bundle.
func ContainsPackKeyword(normalized string) bool {
	fields := strings.Fields(normalized)
	fieldSet := make(map[string]bool, len(fields))
	for _, f := range fields {
		fieldSet[f] = true
	}
	for _, p := range PackPhrases {
		if strings.Contains(p, " ") {
			if strings.Contains(normalized, p) {
				return true
			}
		} else {
			if fieldSet[p] {
				return true
			}
		}
	}
	return false
}

// AllowedCategories are the game_type values kept when editions/packs are
// hidden: main (0), DLC (1), expansion (2), standalone_expansion (4),
// remake (8), remaster (9), expanded_game (10), port (11). Everything
// else (bundle 3, mod 5, episode 6, season 7, fork 12, pack 13, update 14)
// is hidden as misc add-on.
var AllowedCategories = map[int64]bool{
	0:  true,
	1:  true,
	2:  true,
	4:  true,
	8:  true,
	9:  true,
	10: true,
	11: true,
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

// BuildFTSTokenMatch converts a normalized user query into an unquoted FTS5
// MATCH expression of individually quoted tokens of at least 3 characters.
// Unquoted terms combine with implicit AND, so token order doesn't matter:
// "kingdom tears" matches "Tears of the Kingdom", which the strict phrase
// match in BuildFTSMatch cannot. Tokens shorter than 3 characters produce no
// trigrams and are dropped; if nothing survives there is no token query.
// Used as a recall retry when the phrase match returns zero rows — it runs on
// the same FTS index, so it costs index lookups only, never a table scan.
func BuildFTSTokenMatch(query string) (string, bool) {
	query = strings.TrimSpace(query)
	if len(query) < 3 {
		return "", false
	}
	var out []string
	seen := make(map[string]bool)
	for _, tok := range strings.Fields(query) {
		tok = sanitizeFTSToken(tok)
		if len(tok) < 3 || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, `"`+tok+`"`)
	}
	if len(out) == 0 {
		return "", false
	}
	return strings.Join(out, " "), true
}

func sanitizeFTSToken(token string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(fts5SpecialChars, r) {
			return -1
		}
		return r
	}, token)
}
