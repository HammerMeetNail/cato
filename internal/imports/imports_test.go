package imports

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cato/internal/catalog"
	"cato/internal/db"
	"cato/internal/library"
	"cato/internal/social"
)

type fixture struct {
	t   *testing.T
	db  *db.DB
	lib *library.Store
	svc *Service
	ctx context.Context
}

type noIGDB struct{}

func (noIGDB) SearchGames(context.Context, string, int, bool) ([]catalog.Game, error) {
	return nil, nil
}
func (noIGDB) GetGame(context.Context, int64) (*catalog.Game, error)          { return nil, nil }
func (noIGDB) GetGamesBatch(context.Context, []int64) ([]catalog.Game, error) { return nil, nil }
func (noIGDB) GetPlatforms(context.Context) ([]catalog.Platform, error)       { return nil, nil }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, email, username) VALUES ('u', 'u@example.com', 'u'), ('v', 'v@example.com', 'v')`)
	exec(`INSERT INTO games (id, name, slug, normalized_name, first_release_date) VALUES
		(1, 'Hades', 'hades', 'hades', 1600300800),
		(2, 'Hades II', 'hades-ii', 'hades ii', 1715000000),
		(3, 'Celeste', 'celeste', 'celeste', 1516665600),
		(4, 'Portal 2', 'portal-2', 'portal 2', 1303171200),
		(5, 'Doom', 'doom', 'doom', 1463702400),
		(6, 'Doom', 'doom-1993', 'doom', 755827200)`)
	exec(`INSERT INTO game_external_ids (game_id, source, uid) VALUES (4, 1, '620')`)
	games := catalog.NewStore(database)
	lib := library.NewStore(database, games)
	svc := &Service{DB: database, Library: lib, Social: social.New(database, t.TempDir()),
		Matcher: &Matcher{DB: database, Catalog: catalog.NewService(games, noIGDB{}, database)},
		Now:     func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }}
	return &fixture{t: t, db: database, lib: lib, svc: svc, ctx: context.Background()}
}

func TestCSVPresetsAndMapping(t *testing.T) {
	grouvee := "id,name,shelves,platforms,rating,review,dates,statuses,genres,franchises,series,developers,publishers,release_date,url,giantbomb_id\n" +
		`1,Hades,"{""Played"": {""date_added"": ""2024-01-02""}}",PC,4,"Loved it","[{""date_finished"": ""2024-02-03""}]",,,,,,,2020-09-17,https://grouvee.com/1,123` + "\n" +
		`2,Celeste,"{""Wish List"": {}}",Switch,,,,,,,,,,2018-01-25,https://grouvee.com/2,124` + "\n"
	tab, err := ParseCSV([]byte("\xef\xbb\xbf" + grouvee))
	if err != nil {
		t.Fatal(err)
	}
	preset, m := Detect(tab.Headers)
	if preset.ID != "grouvee" {
		t.Fatalf("preset %v", preset)
	}
	rows := tab.Apply(m)
	if len(rows) != 2 || rows[0].Status != "completed" || rows[0].Rating != 80 || rows[0].Year != 2020 || rows[1].Status != "wishlist" {
		t.Fatalf("grouvee rows %+v", rows)
	}

	// A generic sheet with semicolons, 10-point ratings and "12h 30m".
	tab, _ = ParseCSV([]byte("Game;Platform;Status;Score;Time played;Tags;Date completed\nHades;PC;Beaten;9;12h 30m;roguelike, co-op;14/02/2024\nPortal 2;PC;Want to play;;;;\n"))
	preset, m = Detect(tab.Headers)
	rows = tab.Apply(m)
	if preset.ID != "generic" || rows[0].Rating != 90 || rows[0].Minutes != 750 || rows[0].Status != "completed" || len(rows[0].Tags) != 2 || rows[1].Status != "wishlist" {
		t.Fatalf("generic rows %+v (%v)", rows, m)
	}

	// Cato's own export round-trips (formula-safe apostrophes removed).
	tab, _ = ParseCSV([]byte("game,igdb_id,status,platform,medium,rating,playtime_hours,started_at,completed_at,added_at,tags,notes\n" +
		"Hades,1,completed,PC,digital,90,12.50,2024-01-01,2024-02-01,2024-01-01,roguelike,'=not a formula\n"))
	preset, m = Detect(tab.Headers)
	rows = tab.Apply(m)
	if preset.ID != "cato" || rows[0].IGDBID != 1 || rows[0].Rating != 90 || rows[0].Minutes != 750 || rows[0].Notes != "=not a formula" {
		t.Fatalf("cato rows %+v", rows)
	}

	if _, err := ParseCSV([]byte("only,a,header\n")); err != ErrEmpty {
		t.Fatalf("empty file: %v", err)
	}
}

func FuzzParseCSV(f *testing.F) {
	f.Add([]byte("name,rating,hours,status\nHades,4.5,12:30,beaten\n"))
	f.Add([]byte("\xef\xbb\xbfGame;Score\n\"a\"\"b\";10/10\n"))
	f.Add([]byte("title\tshelves\nX\t{\"Played\":{}}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		tab, err := ParseCSV(data)
		if err != nil {
			return
		}
		_, m := Detect(tab.Headers)
		for _, r := range tab.Apply(m) {
			if r.Rating < 0 || r.Rating > 100 || r.Minutes < 0 || r.Title == "" {
				t.Fatalf("bad row %+v", r)
			}
			if r.Status != "" && rank[r.Status] == 0 {
				t.Fatalf("bad status %q", r.Status)
			}
		}
	})
}

func TestMatchingAndReview(t *testing.T) {
	f := newFixture(t)
	f.lib.Upsert(f.ctx, "u", 3, library.Change{Status: ptr("playing")})
	cands, err := f.svc.Matcher.Match(f.ctx, "u", []Row{
		{Title: "Hades"}, {Title: "HADES"}, {Title: "Doom", Year: 1993}, {Title: "Doom"}, {Title: "Celeste"},
		{Title: "Whatever", SteamAppID: 620}, {Title: "Nothing like it at all"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		id    int64
		state string
	}{{1, "matched"}, {1, "matched"}, {6, "matched"}, {0, "review"}, {3, "matched"}, {4, "matched"}, {0, "none"}}
	for i, w := range want {
		c := cands[i]
		if c.State != w.state || (w.id != 0 && (c.Match == nil || c.Match.ID != w.id)) {
			t.Errorf("row %d (%s): state %s match %+v, want %d %s", i, c.Row.Title, c.State, c.Match, w.id, w.state)
		}
	}
	if cands[4].InLibrary != "playing" {
		t.Errorf("in library: %q", cands[4].InLibrary)
	}
	if len(cands[3].Alternatives) == 0 {
		t.Error("an ambiguous name should offer alternatives")
	}
}

func ptr[T any](v T) *T { return &v }

func TestImportIsIdempotentAndUndoable(t *testing.T) {
	f := newFixture(t)
	// Already in the library with a rating: an import doesn't overwrite it.
	f.lib.Upsert(f.ctx, "u", 3, library.Change{Status: ptr("playing"), Rating: ptr(int64(60))})
	entries := []CommitEntry{
		{GameID: 1, Row: Row{Title: "Hades", Status: "completed", Rating: 90, Minutes: 750, Tags: []string{"roguelike"}, CompletedAt: "2024-02-14"}},
		{GameID: 3, Row: Row{Title: "Celeste", Status: "completed", Rating: 100, Minutes: 300, Notes: "Summit!"}},
		{GameID: 4, Row: Row{Title: "Portal 2", Status: "backlog", SteamAppID: 620, Minutes: 30, Played: true}},
	}
	res, err := f.svc.Commit(f.ctx, "u", "csv", entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 || res.Updated != 1 || res.BatchID == "" {
		t.Fatalf("first import %+v", res)
	}
	hades, _ := f.lib.Get(f.ctx, "u", 1)
	celeste, _ := f.lib.Get(f.ctx, "u", 3)
	portal, _ := f.lib.Get(f.ctx, "u", 4)
	if hades.Status != "completed" || hades.Minutes != 750 || hades.CompletedAt == nil || !strings.HasPrefix(*hades.CompletedAt, "2024-02-14") {
		t.Fatalf("hades %+v", hades)
	}
	if celeste.Status != "playing" || celeste.Rating != 60 || celeste.Minutes != 300 || celeste.Notes != "Summit!" {
		t.Fatalf("existing item should only gain what it lacked: %+v", celeste)
	}
	if strings.Join(portal.Tags, ",") != "played" {
		t.Fatalf("steam-played backlog tag: %v", portal.Tags)
	}
	// Imports don't fill friends' feeds.
	var n int
	f.db.QueryRow(`SELECT COUNT(*) FROM activity WHERE user_id = 'u'`).Scan(&n)
	if n != 1 { // only the manual "started" for Celeste above
		t.Fatalf("activity rows %d", n)
	}
	again, err := f.svc.Commit(f.ctx, "u", "csv", entries, nil)
	if err != nil || again.Added+again.Updated != 0 || again.BatchID != "" {
		t.Fatalf("second import should change nothing: %+v %v", again, err)
	}
	if _, err := f.lib.UndoImport(f.ctx, "u", res.BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.lib.Get(f.ctx, "u", 1); err != library.ErrNotFound {
		t.Fatalf("undo should remove added games: %v", err)
	}
	celeste, _ = f.lib.Get(f.ctx, "u", 3)
	if celeste.Minutes != 0 || celeste.Notes != "" || celeste.Rating != 60 {
		t.Fatalf("undo should restore the old item: %+v", celeste)
	}
	if _, err := f.lib.UndoImport(f.ctx, "u", res.BatchID); err != library.ErrNotFound {
		t.Fatal("a batch undoes once")
	}
}

func TestSteamPreviewAndDailySync(t *testing.T) {
	f := newFixture(t)
	minutes := int64(30)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "k" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/ISteamUser/ResolveVanityURL/v1/":
			w.Write([]byte(`{"response":{"steamid":"76561197960287930","success":1}}`))
		case "/ISteamUser/GetPlayerSummaries/v2/":
			w.Write([]byte(`{"response":{"players":[{"personaname":"Gabe"}]}}`))
		case "/IPlayerService/GetOwnedGames/v1/":
			if r.URL.Query().Get("steamid") == "76561197960287931" {
				w.Write([]byte(`{"response":{}}`)) // private profile
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"game_count": 2, "games": []map[string]any{
				{"appid": 620, "name": "Portal 2", "playtime_forever": minutes, "rtime_last_played": time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC).Unix()},
				{"appid": 999999, "name": "Hades", "playtime_forever": 0},
			}}})
		}
	}))
	defer srv.Close()
	f.svc.Steam = &Steam{Key: "k", Endpoint: srv.URL}
	link, cands, err := f.svc.SteamPreview(f.ctx, "u", "https://steamcommunity.com/id/gaben/")
	if err != nil {
		t.Fatal(err)
	}
	if link.SteamID != "76561197960287930" || link.Persona != "Gabe" || len(cands) != 2 {
		t.Fatalf("preview %+v %d", link, len(cands))
	}
	if cands[0].Match.ID != 4 || cands[0].Row.Status != "playing" || cands[1].Match.ID != 1 || cands[1].Row.Status != "backlog" {
		t.Fatalf("steam rows %+v / %+v", cands[0], cands[1])
	}
	if _, _, err := f.svc.SteamPreview(f.ctx, "u", "76561197960287931"); err != ErrSteamPrivate {
		t.Fatalf("private profile: %v", err)
	}
	var entries []CommitEntry
	for _, c := range cands {
		entries = append(entries, CommitEntry{GameID: c.Match.ID, Row: c.Row})
	}
	if _, err := f.svc.Commit(f.ctx, "u", "steam", entries, nil); err != nil {
		t.Fatal(err)
	}
	f.svc.SetSteamSync(f.ctx, "u", true)
	minutes = 120
	if err := f.svc.SyncSteam(f.ctx); err != nil {
		t.Fatal(err)
	}
	ss, _ := f.lib.Sessions(f.ctx, "u", library.SessionRange{GameID: 4, Limit: 10})
	if len(ss) != 1 || ss[0].Minutes != 90 || ss[0].Source != "steam" {
		t.Fatalf("sync should log the 90 new minutes: %+v", ss)
	}
	f.svc.SyncSteam(f.ctx)
	ss, _ = f.lib.Sessions(f.ctx, "u", library.SessionRange{GameID: 4, Limit: 10})
	if len(ss) != 1 {
		t.Fatal("a second sync with no new play logs nothing")
	}
}

func TestCatoJSONRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.lib.Upsert(f.ctx, "u", 1, library.Change{Status: ptr("completed"), Rating: ptr(int64(90))})
	f.lib.LogSession(f.ctx, "u", 1, 60, "2026-10-01", "first")
	f.lib.LogSession(f.ctx, "u", 1, 45, "2026-10-02", "")
	title := "Faves"
	l, _ := f.svc.Social.CreateList(f.ctx, "u", social.ListInput{Title: &title})
	f.svc.Social.AddToList(f.ctx, "u", l.ID, 1, "best")
	f.svc.Social.WriteReview(f.ctx, "u", social.ReviewInput{GameID: 1, Body: "Great"})

	items, _ := f.lib.ExportAll(f.ctx, "u")
	people, _ := f.svc.Social.Export(f.ctx, "u")
	doc := map[string]any{"format": "cato-export", "library": items}
	for k, v := range people {
		doc[k] = v
	}
	raw, _ := json.Marshal(doc)
	ex, rows, err := ParseCatoJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	cands, _ := f.svc.Matcher.Match(f.ctx, "v", rows)
	var entries []CommitEntry
	for _, c := range cands {
		entries = append(entries, CommitEntry{GameID: c.Match.ID, Row: c.Row})
	}
	res, err := f.svc.Commit(f.ctx, "v", "cato-json", entries, &Extras{Reviews: ex.Reviews, Lists: ex.Lists})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Reviews != 1 || res.Lists != 1 {
		t.Fatalf("round trip %+v", res)
	}
	it, _ := f.lib.Get(f.ctx, "v", 1)
	ss, _ := f.lib.Sessions(f.ctx, "v", library.SessionRange{GameID: 1, IncludeAdjustments: true, Limit: 10})
	if it.Minutes != 105 || it.Rating != 90 || len(ss) != 2 {
		t.Fatalf("item %+v sessions %+v", it, ss)
	}
	// Again: nothing new.
	res, _ = f.svc.Commit(f.ctx, "v", "cato-json", entries, &Extras{Reviews: ex.Reviews, Lists: ex.Lists})
	if res.Added != 0 || res.Reviews != 0 || res.Lists != 0 {
		t.Fatalf("second round trip %+v", res)
	}
}

func TestRetroAchievementsRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Count":3,"Total":3,"Results":[
			{"GameID":1,"Title":"~Hack~ Super Mario World","ConsoleName":"SNES","NumAwarded":96,"MaxPossible":96,"HighestAwardKind":"mastered","HighestAwardDate":"2024-03-01T10:00:00+00:00"},
			{"GameID":2,"Title":"Sonic the Hedgehog","ConsoleName":"Genesis/Mega Drive","NumAwarded":5,"MaxPossible":30,"HighestAwardKind":null,"MostRecentAwardedDate":"2026-10-10 10:00:00"},
			{"GameID":3,"Title":"Metroid","ConsoleName":"NES","NumAwarded":0,"MaxPossible":20,"HighestAwardKind":null}]}`))
	}))
	defer srv.Close()
	rows, err := (&RetroAchievements{Endpoint: srv.URL}).Rows(context.Background(), "me", "key", time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Title != "Super Mario World" || rows[0].Status != "completed" || rows[0].CompletedAt != "2024-03-01" ||
		rows[1].Status != "playing" || rows[2].Status != "backlog" || rows[1].Platform != "Genesis/Mega Drive" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestCleanProductTitle(t *testing.T) {
	for in, want := range map[string]string{
		"The Legend of Zelda: Tears of the Kingdom - Nintendo Switch": "The Legend of Zelda: Tears of the Kingdom",
		"Elden Ring Standard Edition (PS5)":                           "Elden Ring",
		"Hades [Nintendo Switch] - NEW":                               "Hades",
	} {
		if got := CleanProductTitle(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestBarcodeLookupCachesAndRemembers(t *testing.T) {
	f := newFixture(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("upc") == "045496596439" {
			w.Write([]byte(`{"code":"OK","items":[{"title":"Celeste - Nintendo Switch"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	b := &Barcode{DB: f.db, Matcher: f.svc.Matcher, Endpoint: srv.URL}
	res, err := b.Lookup(f.ctx, "u", "045496596439")
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "Celeste - Nintendo Switch" || res.Candidate == nil || res.Candidate.Match.ID != 3 {
		t.Fatalf("lookup %+v", res)
	}
	b.Lookup(f.ctx, "u", "045496596439")
	if calls != 1 {
		t.Fatalf("each code is looked up once, got %d calls", calls)
	}
	if err := b.Confirm(f.ctx, "045496596439", 3); err != nil {
		t.Fatal(err)
	}
	res, _ = b.Lookup(f.ctx, "v", "045496596439")
	if res.Confirmed == nil || res.Confirmed.ID != 3 {
		t.Fatalf("confirmed match %+v", res)
	}
	if _, err := b.Lookup(f.ctx, "u", "12ab"); err != ErrBadCode {
		t.Fatalf("bad code: %v", err)
	}
}
