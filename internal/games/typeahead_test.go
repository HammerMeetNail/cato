package games

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func planFor(t *testing.T, s *Store, query string, o searchOptions, engine, match string) string {
	t.Helper()
	var candidates strings.Builder
	args := []interface{}{}
	prefix := EscapeLike(query) + "%"
	buildSearchCandidates(&candidates, &args, engine, match, query, prefix, "% "+EscapeLike(query)+"%", "%"+EscapeLike(query)+"%", o)
	q := candidates.String() + "SELECT g.id FROM eligible x JOIN games g ON g.id = x.id ORDER BY " + sortOrder(o.sort) + " LIMIT 10"
	rows, err := s.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, q)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		rows.Scan(&id, &parent, &notused, &detail)
		b.WriteString(detail + "\n")
	}
	return b.String()
}

// F8: two-character typeahead uses the normalized_name index (prefix range)
// instead of a LIKE '%q%' scan of the whole catalog.
func TestShortTypeaheadUsesPrefixIndex(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score) VALUES
		(1, 'Hades', 'hades', 'hades', 10), (2, 'Halo', 'halo', 'halo', 50), (3, 'Shadow of the Colossus', 'sotc', 'shadow of the colossus', 90)`)
	results, err := store.SearchLocal(context.Background(), "ha", 10)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range results {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "Halo,Hades" {
		t.Fatalf("2-char prefix results = %v (prefix matches only, popular first)", names)
	}
	plan := planFor(t, store, "ha", searchOptions{limit: 10}, "prefix", "")
	if strings.Contains(plan, "SCAN g\n") || !strings.Contains(plan, "idx_games_normalized_name") || !strings.Contains(plan, "idx_game_aliases_alias") {
		t.Fatalf("short query scans the catalog:\n%s", plan)
	}
}

// F8: typeahead bounds the candidate set (tier first, then popularity), so a
// common trigram doesn't drag every match through the ranking query, while
// exact and prefix matches still win.
func TestTypeaheadCapsCandidates(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	tx, _ := database.Begin()
	for i := 0; i < 700; i++ {
		tx.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score) VALUES (?, ?, ?, ?, ?)`,
			100+i, fmt.Sprintf("Super Zelda Clone %d", i), fmt.Sprintf("z%d", i), fmt.Sprintf("super zelda clone %d", i), i)
	}
	tx.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score) VALUES (1, 'Zelda', 'zelda', 'zelda', 0)`)
	tx.Commit()
	results, err := store.SearchLocal(context.Background(), "zelda", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 10 || results[0].Name != "Zelda" || results[1].Name != "Super Zelda Clone 699" {
		t.Fatalf("results = %v", results)
	}
	var sql strings.Builder
	args := []interface{}{}
	buildSearchCandidates(&sql, &args, "fts", `"zelda"`, "zelda", "zelda%", "% zelda%", "%zelda%", searchOptions{limit: 10})
	if !strings.Contains(sql.String(), "LIMIT 500") {
		t.Fatal("typeahead candidates are not capped")
	}
	// Paged full results keep exact totals (no cap).
	sql.Reset()
	buildSearchCandidates(&sql, &args, "fts", `"zelda"`, "zelda", "zelda%", "% zelda%", "%zelda%", searchOptions{limit: 10, withTotal: true})
	if strings.Contains(sql.String(), "LIMIT 500") {
		t.Fatal("full results must not cap candidates")
	}
	_, total, err := store.search(context.Background(), "zelda", searchOptions{limit: 10, withTotal: true})
	if err != nil || total != 701 {
		t.Fatalf("full total = %d, %v", total, err)
	}
}
