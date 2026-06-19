package importer

import (
	"testing"
)

func TestPgArrayToJSONText(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "[]"},
		{`\N`, "[]"},
		{"{}", "[]"},
		{"{169,6,167}", "[169,6,167]"},
		{"{1,2,3}", "[1,2,3]"},
		{"{  5 , 10  }", "[5,10]"},
		{"{invalid}", "[]"},
	}

	for _, tt := range tests {
		got := pgArrayToJSONText(tt.input)
		if got != tt.want {
			t.Errorf("pgArrayToJSONText(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"The Legend of Zelda", "the legend of zelda"},
		{"Zelda's Adventure", "zeldas adventure"},
		{"Spider-Man: Miles Morales", "spider man miles morales"},
		{"  Double   Spaces  ", "double spaces"},
		{"Mario-Kart-Deluxe", "mario kart deluxe"},
	}

	for _, tt := range tests {
		got := normalizeName(tt.input)
		if got != tt.want {
			t.Errorf("normalizeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseNullableInt(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{`\N`, 0},
		{"", 0},
		{"0", 0},
		{"12345", 12345},
		{"-1", -1},
		{"invalid", 0},
	}

	for _, tt := range tests {
		got := parseNullableInt(tt.input)
		if got != tt.want {
			t.Errorf("parseNullableInt(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseNullableString(t *testing.T) {
	if got := parseNullableString(`\N`); got != "" {
		t.Errorf("expected empty string for \\N, got %q", got)
	}
	if got := parseNullableString("hello"); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestGetField(t *testing.T) {
	colMap := map[string]int{"id": 0, "name": 1, "slug": 2}
	fields := []string{"1", "Zelda", "zelda"}

	if got := getField(fields, colMap, "id"); got != "1" {
		t.Errorf("expected '1', got %q", got)
	}
	if got := getField(fields, colMap, "name"); got != "Zelda" {
		t.Errorf("expected 'Zelda', got %q", got)
	}
	if got := getField(fields, colMap, "missing"); got != "" {
		t.Errorf("expected '', got %q", got)
	}
	if got := getField(fields, colMap, "out_of_range"); got != "" {
		// out_of_range has index that doesn't exist in colMap, but defaults to ""
	}
	// Test index out of bounds safety
	colMap2 := map[string]int{"bad": 99}
	if got := getField(fields, colMap2, "bad"); got != "" {
		t.Errorf("expected '' for out-of-bounds index, got %q", got)
	}
}
