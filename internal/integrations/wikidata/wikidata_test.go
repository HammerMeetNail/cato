package wikidata

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"cato/internal/catalog"
	"cato/internal/db"
)

func TestCrosswalkStoresLinksAndMarksChecked(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		v, _ := url.ParseQuery(string(b))
		queries = append(queries, v.Get("query"))
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent")
		}
		fmt.Fprint(w, `{"results":{"bindings":[
			{"item":{"value":"http://www.wikidata.org/entity/Q17"},"igdb":{"value":"hades--1"},"hltb":{"value":"62941"},"pcgw":{"value":"Hades"}}
		]}}`)
	}))
	defer srv.Close()
	database, err := db.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	db.Migrate(database)
	database.Exec(`INSERT INTO users (id, email) VALUES ('u', 'u@x')`)
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'Hades', 'hades--1', 'hades'), (2, 'Unknown', 'unk"nown', 'unknown'), (3, 'Not Mine', 'nm', 'nm')`)
	database.Exec(`INSERT INTO library_items (user_id, game_id, status) VALUES ('u', 1, 'backlog'), ('u', 2, 'backlog')`)

	c := &Client{HTTP: srv.Client(), Endpoint: srv.URL}
	if err := Crosswalk(database, c, 3)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || !strings.Contains(queries[0], `"hades--1"`) || !strings.Contains(queries[0], `"unk\"nown"`) || strings.Contains(queries[0], `"nm"`) {
		t.Fatalf("queries %q", queries)
	}
	x, err := catalog.NewStore(database).GameExtras(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, l := range x.Links {
		kinds = append(kinds, l.Kind+"="+l.URL)
	}
	if strings.Join(kinds, " ") != "hltb=https://howlongtobeat.com/game/62941 pcgamingwiki=https://www.pcgamingwiki.com/wiki/Hades" {
		t.Errorf("links %v", kinds)
	}
	// Both games are marked checked, so the next run asks nothing.
	if err := Crosswalk(database, c, 3)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 {
		t.Errorf("checked games were looked up again")
	}
}
