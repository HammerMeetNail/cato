package importer

import (
	"encoding/json"
	"strconv"
	"strings"
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
	input = strings.ReplaceAll(input, "'", "")
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
