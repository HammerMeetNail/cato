// Package wikidata uses Wikidata (CC0) as an ID crosswalk: from a game's
// IGDB slug (property P5794) to its HowLongToBeat ID (P2816) and
// PCGamingWiki page (P6337), for outbound links (plan §9). Nothing is
// scraped; one SPARQL query covers up to 100 games.
package wikidata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cato/internal/catalog"
	"cato/internal/db"
)

const endpoint = "https://query.wikidata.org/sparql"

// SourceChecked marks a game as looked up (uid = Wikidata item or "none").
const SourceChecked = 1000

type Client struct {
	HTTP     *http.Client
	Endpoint string
}

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Endpoint: endpoint}
}

// Match is what Wikidata knows for one IGDB slug.
type Match struct {
	Item         string
	HLTB         string
	PCGamingWiki string
}

func sparqlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ", "\r", " ")
	return `"` + r.Replace(s) + `"`
}

// Lookup resolves IGDB slugs (≤ 100) to their Wikidata matches.
func (c *Client) Lookup(ctx context.Context, slugs []string) (map[string]Match, error) {
	if len(slugs) == 0 {
		return map[string]Match{}, nil
	}
	values := make([]string, len(slugs))
	for i, s := range slugs {
		values[i] = sparqlString(s)
	}
	q := `SELECT ?item ?igdb ?hltb ?pcgw WHERE {
  VALUES ?igdb { ` + strings.Join(values, " ") + ` }
  ?item wdt:P5794 ?igdb .
  OPTIONAL { ?item wdt:P2816 ?hltb . }
  OPTIONAL { ?item wdt:P6337 ?pcgw . }
}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, strings.NewReader(url.Values{"query": {q}}.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/sparql-results+json")
	// Wikimedia's policy asks clients to identify themselves.
	req.Header.Set("User-Agent", "Cato/1.0 (self-hosted game tracker; https://github.com/)")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wikidata returned %d", res.StatusCode)
	}
	var body struct {
		Results struct {
			Bindings []map[string]struct {
				Value string `json:"value"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	out := map[string]Match{}
	for _, b := range body.Results.Bindings {
		slug := b["igdb"].Value
		m := out[slug]
		m.Item = strings.TrimPrefix(b["item"].Value, "http://www.wikidata.org/entity/")
		if v := b["hltb"].Value; v != "" {
			m.HLTB = v
		}
		if v := b["pcgw"].Value; v != "" {
			m.PCGamingWiki = v
		}
		out[slug] = m
	}
	return out, nil
}

// Crosswalk is the weekly job: library games not yet looked up, 100 per
// query, at most batches per run.
func Crosswalk(database *db.DB, c *Client, batches int) func(context.Context) error {
	return func(ctx context.Context) error {
		for i := 0; i < batches && ctx.Err() == nil; i++ {
			rows, err := database.QueryContext(ctx, `SELECT DISTINCT g.id, g.slug FROM games g
				JOIN library_items li ON li.game_id = g.id
				WHERE g.id > 0 AND g.slug != '' AND NOT EXISTS (SELECT 1 FROM game_external_ids x WHERE x.game_id = g.id AND x.source = ?)
				LIMIT 100`, SourceChecked)
			if err != nil {
				return err
			}
			bySlug := map[string]int64{}
			var slugs []string
			for rows.Next() {
				var id int64
				var slug string
				if rows.Scan(&id, &slug) == nil {
					bySlug[slug] = id
					slugs = append(slugs, slug)
				}
			}
			rows.Close()
			if len(slugs) == 0 {
				return nil
			}
			matches, err := c.Lookup(ctx, slugs)
			if err != nil {
				return err
			}
			for slug, id := range bySlug {
				m := matches[slug]
				item := m.Item
				if item == "" {
					item = "none"
				}
				for src, uid := range map[int64]string{SourceChecked: item, catalog.WebsiteHLTB: m.HLTB, catalog.WebsitePCGamingWiki: m.PCGamingWiki} {
					if uid == "" {
						continue
					}
					if _, err := database.ExecContext(ctx, `INSERT OR IGNORE INTO game_external_ids (game_id, source, uid) VALUES (?, ?, ?)`, id, src, uid); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
}
