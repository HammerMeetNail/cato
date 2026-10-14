package igdb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// endpointServer answers each IGDB endpoint with a canned payload and
// records the request bodies.
func endpointServer(t *testing.T, payloads map[string]string) (*Client, map[string]string) {
	t.Helper()
	bodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ep := strings.TrimPrefix(r.URL.Path, "/")
		bodies[ep] = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, payloads[ep])
	}))
	t.Cleanup(srv.Close)
	return preAuthedClient(t, srv.URL+"/"), bodies
}

func TestGetDetailsDecodesNewAndDeprecatedFields(t *testing.T) {
	c, bodies := endpointServer(t, map[string]string{"games": `[{
		"id": 7346, "genres": [12, 31], "themes": [1, 17], "game_modes": [1], "player_perspectives": [2],
		"involved_companies": [{"company": {"id": 70, "name": "Nintendo EPD"}, "developer": true, "publisher": false},
		                       {"company": {"id": 71, "name": "Nintendo"}, "developer": false, "publisher": true}],
		"collections": [{"id": 106, "name": "The Legend of Zelda"}], "collection": {"id": 106, "name": "The Legend of Zelda"},
		"franchise": {"id": 596, "name": "Zelda"},
		"similar_games": [1, 2], "dlcs": [3], "remasters": [], "expansions": [4],
		"release_dates": [{"date": 1488499200, "platform": 130, "release_region": 8, "human": "Mar 03, 2017"},
		                  {"date": 1488499200, "platform": 41, "region": 2, "human": "Mar 03, 2017"}],
		"screenshots": [{"image_id": "sc1"}], "artworks": [{"image_id": "ar1"}],
		"videos": [{"video_id": "zw47_q9wbBE", "name": "Trailer"}],
		"websites": [{"url": "https://store.steampowered.com/app/1", "type": 13}, {"url": "https://zelda.com", "category": 1}],
		"external_games": [{"uid": "1145360", "external_game_source": 1}, {"uid": "123", "category": 5}],
		"age_ratings": [{"organization": {"id": 2, "name": "PEGI"}, "rating_category": {"rating": "12"}}, {"category": 1, "rating": 10}],
		"language_supports": [{"language": {"id": 7, "name": "English"}}, {"language": {"id": 7, "name": "English"}}],
		"multiplayer_modes": [{"onlinecoop": true, "onlinemax": 4}, {"splitscreen": true, "offlinecoopmax": 2}]
	}]`})
	ds, err := c.GetDetails(context.Background(), []int64{7346})
	if err != nil || len(ds) != 1 {
		t.Fatalf("GetDetails: %v %d", err, len(ds))
	}
	if !strings.Contains(bodies["games"], "where id = (7346)") || !strings.Contains(bodies["games"], "involved_companies.company.name") {
		t.Errorf("request body %q", bodies["games"])
	}
	d := ds[0]
	if len(d.Companies) != 2 || !d.Companies[0].Developer || !d.Companies[1].Publisher {
		t.Errorf("companies %+v", d.Companies)
	}
	if len(d.Collections) != 1 || len(d.Franchises) != 1 || d.Franchises[0].Name != "Zelda" {
		t.Errorf("series %+v %+v", d.Collections, d.Franchises)
	}
	if len(d.Related["similar"]) != 2 || len(d.Related["dlc"]) != 1 || len(d.Related["expansion"]) != 1 {
		t.Errorf("related %+v", d.Related)
	}
	if d.ReleaseDates[0].Region != 8 || d.ReleaseDates[1].Region != 2 {
		t.Errorf("regions (release_region, then deprecated region): %+v", d.ReleaseDates)
	}
	if d.Websites[1].Type != 1 || d.External[1].Source != 5 {
		t.Errorf("deprecated category fallback: %+v %+v", d.Websites, d.External)
	}
	if strings.Join(d.AgeRatings, ",") != "PEGI 12,ESRB T" {
		t.Errorf("age ratings %v", d.AgeRatings)
	}
	if strings.Join(d.Languages, ",") != "English" {
		t.Errorf("languages %v", d.Languages)
	}
	if m := d.Multiplayer; m == nil || !m.OnlineCoop || !m.Splitscreen || m.OnlineMax != 4 || m.OfflineMax != 2 {
		t.Errorf("multiplayer %+v", d.Multiplayer)
	}
	if d.Videos[0].VideoID != "zw47_q9wbBE" || d.Screenshots[0] != "sc1" {
		t.Errorf("media %+v %+v", d.Videos, d.Screenshots)
	}
}

func TestTimeToBeatLookupsPopularityAndDelta(t *testing.T) {
	c, bodies := endpointServer(t, map[string]string{
		"game_time_to_beats":    `[{"game_id": 1, "hastily": 36000, "normally": 54000, "completely": 180000, "count": 40}]`,
		"genres":                `[{"id": 12, "name": "Role-playing (RPG)", "slug": "role-playing-rpg"}]`,
		"game_types":            `[{"id": 0, "type": "Main Game"}, {"id": 1, "type": "DLC"}]`,
		"popularity_primitives": `[{"game_id": 5, "value": 0.031, "popularity_type": 1}]`,
	})
	ctx := context.Background()
	ttb, err := c.GetTimesToBeat(ctx, []int64{1, 2})
	if err != nil || len(ttb) != 1 || ttb[0].Normally != 54000 {
		t.Fatalf("ttb %v %+v", err, ttb)
	}
	if !strings.Contains(bodies["game_time_to_beats"], "where game_id = (1,2)") {
		t.Errorf("ttb body %q", bodies["game_time_to_beats"])
	}
	g, err := c.GetLookup(ctx, "genres")
	if err != nil || g[0].Name != "Role-playing (RPG)" {
		t.Fatalf("genres %v %+v", err, g)
	}
	gt, _ := c.GetLookup(ctx, "game_types")
	if len(gt) != 2 || gt[1].Name != "DLC" {
		t.Errorf("game types %+v", gt)
	}
	if _, err := c.GetLookup(ctx, "users; fields *"); err == nil {
		t.Error("arbitrary endpoint accepted")
	}
	pop, err := c.GetPopular(ctx, 1, 50)
	if err != nil || pop[0].GameID != 5 || !strings.Contains(bodies["popularity_primitives"], "where popularity_type = 1; sort value desc; limit 50;") {
		t.Fatalf("popularity %v %+v %q", err, pop, bodies["popularity_primitives"])
	}

	c2, bodies2 := endpointServer(t, map[string]string{"games": `[{"id": 9, "name": "Updated", "updated_at": 1790000000}]`})
	games, err := c2.GetUpdatedSince(ctx, 1789000000, 500, 500)
	if err != nil || len(games) != 1 || games[0].SourceUpdatedAt != 1790000000 {
		t.Fatalf("delta %v %+v", err, games)
	}
	if !strings.Contains(bodies2["games"], "where updated_at > 1789000000; sort updated_at asc; limit 500; offset 500;") {
		t.Errorf("delta body %q", bodies2["games"])
	}
}
