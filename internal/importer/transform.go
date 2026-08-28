package importer

import (
	"encoding/json"
	"strconv"
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

func pgArrayToJSONText(input string) string {
	if input == "" || input == `\N` || input == "{}" {
		return "[]"
	}

	trimmed := strings.Trim(input, "{}")
	if strings.TrimSpace(trimmed) == "" {
		return "[]"
	}

	parts := strings.Split(trimmed, ",")
	out := make([]int64, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil {
			out = append(out, n)
		}
	}

	if len(out) == 0 {
		return "[]"
	}

	b, _ := json.Marshal(out)
	return string(b)
}

func normalizeName(input string) string {
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

func parseNullableInt(input string) int64 {
	if input == `\N` || input == "" {
		return 0
	}
	n, err := strconv.ParseInt(input, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func parseNullableString(input string) string {
	if input == `\N` {
		return ""
	}
	return input
}
