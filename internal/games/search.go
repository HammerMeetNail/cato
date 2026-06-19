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
