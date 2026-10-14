package igdb

import (
	"encoding/json"
	"testing"
)

// TestDecodeNestedCoverAndURL verifies the §1.1 fix: the cover URL must be
// built from the covers-table image_id (requested as a nested field), not
// guessed from the numeric cover ID.
func TestDecodeNestedCoverAndURL(t *testing.T) {
	payload := `{
		"id": 194464,
		"name": "Escape Academy",
		"cover": {"id": 213263, "image_id": "co213269"}
	}`
	var g igdbGame
	if err := json.Unmarshal([]byte(payload), &g); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if g.Cover == nil {
		t.Fatal("expected nested cover to decode")
	}
	if g.Cover.ID != 213263 || g.Cover.ImageID != "co213269" {
		t.Errorf("unexpected cover: %+v", g.Cover)
	}

	game := (&Client{}).toGame(g)
	want := "https://images.igdb.com/igdb/image/upload/t_cover_big/co213269.jpg"
	if game.CoverURL != want {
		t.Errorf("CoverURL = %q, want %q", game.CoverURL, want)
	}
	if game.CoverID != 213263 {
		t.Errorf("CoverID = %d, want 213263", game.CoverID)
	}
}

func TestToGameWithoutCover(t *testing.T) {
	var g igdbGame
	json.Unmarshal([]byte(`{"id":1,"name":"No Cover Game"}`), &g)
	game := (&Client{}).toGame(g)
	if game.CoverURL != "" || game.CoverID != 0 {
		t.Errorf("expected empty cover fields, got %q/%d", game.CoverURL, game.CoverID)
	}
}

func TestIgdbCoverURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"co213263", "https://images.igdb.com/igdb/image/upload/t_cover_big/co213263.jpg"},
	}
	for _, tc := range cases {
		if got := igdbCoverURL(tc.in); got != tc.want {
			t.Errorf("igdbCoverURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestIgdbFieldsRequestImageID guards against regressing to `cover` (which
// returns only the numeric ID and forces URL guessing).
func TestIgdbFieldsRequestImageID(t *testing.T) {
	if !contains(igdbFields, "cover.image_id") {
		t.Error("igdbFields must request cover.image_id")
	}
}

// TestDecodePlatforms covers the /platforms reference-list decode used to
// populate the platform ID→name lookup table.
func TestDecodePlatforms(t *testing.T) {
	raw := []byte(`[
		{"id":6,"name":"PC (Microsoft Windows)","abbreviation":"win"},
		{"id":169,"name":"Nintendo Switch 2","abbreviation":"swi2"}
	]`)
	plats, err := decodePlatforms(raw)
	if err != nil {
		t.Fatalf("decodePlatforms: %v", err)
	}
	if len(plats) != 2 {
		t.Fatalf("got %d platforms, want 2", len(plats))
	}
	if plats[0].ID != 6 || plats[0].Name != "PC (Microsoft Windows)" || plats[0].Abbreviation != "win" {
		t.Errorf("plats[0] = %+v", plats[0])
	}
	if plats[1].ID != 169 || plats[1].Name != "Nintendo Switch 2" {
		t.Errorf("plats[1] = %+v", plats[1])
	}

	if _, err := decodePlatforms([]byte("not json")); err == nil {
		t.Error("expected error decoding invalid payload")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
