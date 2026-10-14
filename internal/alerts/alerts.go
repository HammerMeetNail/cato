// Package alerts watches wishlists (plan §8.2): release-day notices for
// your platforms, an optional week-ahead heads-up, and price drops from
// IsThereAnyDeal. Each alert is sent once (alerts_sent).
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cato/internal/db"
	"cato/internal/library"
	"cato/internal/social"
)

type Service struct {
	DB      *db.DB
	Library *library.Store
	Social  *social.Service
	ITAD    *ITAD
	Now     func() time.Time
	// OnSent runs after alerts were added (wakes push delivery).
	OnSent func()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// once records an alert; false when it was already sent.
func (s *Service) once(ctx context.Context, userID string, gameID int64, kind, key string) bool {
	res, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO alerts_sent (user_id, game_id, kind, key) VALUES (?, ?, ?, ?)`, userID, gameID, kind, key)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// Releases notifies wishlist owners on release day (and a week before when
// they asked). A game counts as released for you when a release date for
// one of your platforms (your default platforms, or the platforms you own
// games on) falls on that day; with no platforms known, any platform counts.
func (s *Service) Releases(ctx context.Context) error {
	now := s.now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	rows, err := s.DB.QueryContext(ctx, `SELECT li.user_id, li.game_id, rd.date, COALESCE(p.name, ''), rd.platform_id
		FROM library_items li
		JOIN game_release_dates rd ON rd.game_id = li.game_id
		LEFT JOIN platforms p ON p.id = rd.platform_id
		JOIN users u ON u.id = li.user_id AND u.disabled = 0
		WHERE li.status = 'wishlist' AND rd.date >= ? AND rd.date < ?
		ORDER BY li.user_id, rd.date`, today.Unix(), today.Add(8*24*time.Hour).Unix())
	if err != nil {
		return err
	}
	type hit struct {
		user     string
		game     int64
		date     time.Time
		platform string
	}
	var hits []hit
	for rows.Next() {
		var h hit
		var unix, pid int64
		if err := rows.Scan(&h.user, &h.game, &unix, &h.platform, &pid); err != nil {
			rows.Close()
			return err
		}
		h.date = time.Unix(unix, 0).UTC()
		hits = append(hits, h)
	}
	rows.Close()
	plats := map[string]map[string]bool{}
	prefs := map[string]library.Prefs{}
	sent := 0
	for _, h := range hits {
		if _, ok := prefs[h.user]; !ok {
			p, err := s.Library.GetPrefs(ctx, h.user)
			if err != nil {
				return err
			}
			prefs[h.user] = p
			plats[h.user] = s.platformsOf(ctx, h.user, p)
		}
		p := prefs[h.user]
		mine := plats[h.user]
		if len(mine) > 0 && h.platform != "" && !mine[strings.ToLower(h.platform)] {
			continue
		}
		day := h.date.Format("2006-01-02")
		switch {
		case h.date.Before(today.Add(24*time.Hour)) && p.ReleaseAlerts:
			if s.once(ctx, h.user, h.game, "release", day) {
				s.Social.Notify(ctx, h.user, "release", h.game, map[string]any{"platform": h.platform, "date": day})
				sent++
			}
		case !h.date.Before(today.Add(6*24*time.Hour)) && p.ReleaseWeek:
			if s.once(ctx, h.user, h.game, "release_soon", day) {
				s.Social.Notify(ctx, h.user, "release_soon", h.game, map[string]any{"platform": h.platform, "date": day, "when": "next " + h.date.Weekday().String()})
				sent++
			}
		}
	}
	if sent > 0 && s.OnSent != nil {
		s.OnSent()
	}
	return nil
}

func (s *Service) platformsOf(ctx context.Context, userID string, p library.Prefs) map[string]bool {
	out := map[string]bool{}
	for _, name := range p.DefaultPlatforms {
		out[strings.ToLower(name)] = true
	}
	if owned, err := s.Library.OwnedPlatforms(ctx, userID); err == nil {
		for _, name := range owned {
			out[strings.ToLower(name)] = true
		}
	}
	return out
}

// ---- IsThereAnyDeal ---------------------------------------------------------

// ITAD is the IsThereAnyDeal API (api.isthereanydeal.com). Its data is
// shown as given, with the deal link unchanged (their terms).
type ITAD struct {
	Key      string
	HTTP     *http.Client
	Endpoint string
}

func (c *ITAD) post(ctx context.Context, path string, q url.Values, body, into any) error {
	base := c.Endpoint
	if base == "" {
		base = "https://api.isthereanydeal.com"
	}
	q.Set("key", c.Key)
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path+"?"+q.Encode(), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("isthereanydeal %s: HTTP %d", path, res.StatusCode)
	}
	return json.Unmarshal(raw, into)
}

// steamShop is ITAD's shop ID for Steam.
const steamShop = 61

// LookupSteam maps Steam app IDs to ITAD game IDs.
func (c *ITAD) LookupSteam(ctx context.Context, appids []string) (map[string]string, error) {
	keys := make([]string, len(appids))
	for i, a := range appids {
		keys[i] = "app/" + a
	}
	var out map[string]*string
	if err := c.post(ctx, "/lookup/id/shop/"+strconv.Itoa(steamShop)+"/v1", url.Values{}, keys, &out); err != nil {
		return nil, err
	}
	m := map[string]string{}
	for k, v := range out {
		if v != nil && *v != "" {
			m[strings.TrimPrefix(k, "app/")] = *v
		}
	}
	return m, nil
}

// Deal is the best current price and the historical low.
type Deal struct {
	ID       string
	Shop     string
	Price    float64
	Currency string
	Cut      int
	URL      string
	Low      float64
}

// Overview fetches current best prices and historical lows (≤ 200 IDs).
func (c *ITAD) Overview(ctx context.Context, ids []string, country string) ([]Deal, error) {
	type money struct {
		Amount   float64 `json:"amount"`
		Currency string  `json:"currency"`
	}
	var out struct {
		Prices []struct {
			ID      string `json:"id"`
			Current *struct {
				Shop  struct{ Name string } `json:"shop"`
				Price money                 `json:"price"`
				Cut   int                   `json:"cut"`
				URL   string                `json:"url"`
			} `json:"current"`
			Lowest *struct {
				Price money `json:"price"`
			} `json:"lowest"`
		} `json:"prices"`
	}
	if err := c.post(ctx, "/games/overview/v2", url.Values{"country": {country}}, ids, &out); err != nil {
		return nil, err
	}
	var deals []Deal
	for _, p := range out.Prices {
		if p.Current == nil {
			continue
		}
		d := Deal{ID: p.ID, Shop: p.Current.Shop.Name, Price: p.Current.Price.Amount, Currency: p.Current.Price.Currency, Cut: p.Current.Cut, URL: p.Current.URL}
		if p.Lowest != nil {
			d.Low = p.Lowest.Price.Amount
		}
		deals = append(deals, d)
	}
	return deals, nil
}

// Prices checks the wishlists of everyone with price alerts on (games
// with a Steam app ID) and notifies on a big enough cut or a historical low.
func (s *Service) Prices(ctx context.Context) error {
	if s.ITAD == nil || s.ITAD.Key == "" {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT li.user_id, li.game_id, MIN(x.uid) FROM library_items li
		JOIN game_external_ids x ON x.game_id = li.game_id AND x.source = 1
		JOIN users u ON u.id = li.user_id AND u.disabled = 0
		WHERE li.status = 'wishlist' GROUP BY li.user_id, li.game_id`)
	if err != nil {
		return err
	}
	type want struct {
		user  string
		game  int64
		appid string
	}
	var wants []want
	for rows.Next() {
		var w want
		rows.Scan(&w.user, &w.game, &w.appid)
		wants = append(wants, w)
	}
	rows.Close()
	prefs := map[string]library.Prefs{}
	var active []want
	for _, w := range wants {
		p, ok := prefs[w.user]
		if !ok {
			if p, err = s.Library.GetPrefs(ctx, w.user); err != nil {
				return err
			}
			prefs[w.user] = p
		}
		if p.PriceAlerts.Enabled {
			active = append(active, w)
		}
	}
	if len(active) == 0 {
		return nil
	}
	// ITAD IDs: cached per game, looked up for the rest.
	itad := map[int64]string{}
	var lookup []string
	appGame := map[string]int64{}
	for _, w := range active {
		var id string
		err := s.DB.QueryRowContext(ctx, `SELECT itad_id FROM game_itad WHERE game_id = ? AND checked_at > ?`, w.game,
			s.now().Add(-30*24*time.Hour).UTC().Format(time.RFC3339)).Scan(&id)
		if err == nil {
			itad[w.game] = id
		} else if _, seen := appGame[w.appid]; !seen {
			lookup = append(lookup, w.appid)
			appGame[w.appid] = w.game
		}
	}
	for start := 0; start < len(lookup); start += 200 {
		found, err := s.ITAD.LookupSteam(ctx, lookup[start:min(start+200, len(lookup))])
		if err != nil {
			return err
		}
		for _, app := range lookup[start:min(start+200, len(lookup))] {
			g := appGame[app]
			itad[g] = found[app]
			s.DB.ExecContext(ctx, `INSERT INTO game_itad (game_id, itad_id, checked_at) VALUES (?, ?, ?)
				ON CONFLICT(game_id) DO UPDATE SET itad_id = excluded.itad_id, checked_at = excluded.checked_at`, g, found[app], s.now().UTC().Format(time.RFC3339))
		}
	}
	// Prices per country.
	byCountry := map[string][]want{}
	for _, w := range active {
		if itad[w.game] != "" {
			c := prefs[w.user].PriceAlerts.Country
			byCountry[c] = append(byCountry[c], w)
		}
	}
	sent := 0
	for country, ws := range byCountry {
		var ids []string
		seen := map[string]bool{}
		for _, w := range ws {
			if id := itad[w.game]; !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		deals := map[string]Deal{}
		for start := 0; start < len(ids); start += 200 {
			ds, err := s.ITAD.Overview(ctx, ids[start:min(start+200, len(ids))], country)
			if err != nil {
				return err
			}
			for _, d := range ds {
				deals[d.ID] = d
			}
		}
		for _, w := range ws {
			d, ok := deals[itad[w.game]]
			if !ok || d.Cut <= 0 {
				continue
			}
			pa := prefs[w.user].PriceAlerts
			low := d.Low > 0 && d.Price <= d.Low+0.001
			if pa.LowOnly && !low || !pa.LowOnly && d.Cut < pa.MinCut && !low {
				continue
			}
			key := fmt.Sprintf("%s:%.2f", d.Shop, d.Price)
			if s.once(ctx, w.user, w.game, "price", key) {
				s.Social.Notify(ctx, w.user, "price", w.game, map[string]any{
					"cut": d.Cut, "shop": d.Shop, "price": formatPrice(d.Price, d.Currency), "low": low, "url": d.URL, "source": "IsThereAnyDeal",
				})
				sent++
			}
		}
	}
	if sent > 0 && s.OnSent != nil {
		s.OnSent()
	}
	return nil
}

func formatPrice(v float64, currency string) string {
	sym := map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥", "CAD": "CA$", "AUD": "A$"}[currency]
	if sym == "" {
		return fmt.Sprintf("%.2f %s", v, currency)
	}
	return fmt.Sprintf("%s%.2f", sym, v)
}
