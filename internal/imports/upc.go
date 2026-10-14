package imports

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"cato/internal/db"
)

// Barcode resolves UPC/EAN codes on physical games (plan §8.3) through
// UPCitemdb, caching every lookup in upc_games so each code costs one
// request, ever. A confirmed match is remembered for everyone.
type Barcode struct {
	DB       *db.DB
	Matcher  *Matcher
	Key      string // UPCitemdb paid tier; empty uses the free trial (100/day)
	HTTP     *http.Client
	Endpoint string
}

var upcRe = regexp.MustCompile(`^\d{8,14}$`)

// ErrBadCode means the scan isn't a UPC/EAN.
var ErrBadCode = errors.New("that doesn't look like a UPC or EAN barcode")

// BarcodeResult is what a scan found.
type BarcodeResult struct {
	Code      string     `json:"code"`
	Title     string     `json:"title"`
	Confirmed *Game      `json:"confirmed,omitempty"`
	Candidate *Candidate `json:"candidate,omitempty"`
}

func (b *Barcode) Lookup(ctx context.Context, userID, code string) (*BarcodeResult, error) {
	code = strings.TrimSpace(code)
	if !upcRe.MatchString(code) {
		return nil, ErrBadCode
	}
	res := &BarcodeResult{Code: code}
	var gameID sql.NullInt64
	err := b.DB.QueryRowContext(ctx, `SELECT title, game_id FROM upc_games WHERE upc = ?`, code).Scan(&res.Title, &gameID)
	if errors.Is(err, sql.ErrNoRows) {
		if res.Title, err = b.fetch(ctx, code); err != nil {
			return nil, err
		}
		b.DB.ExecContext(ctx, `INSERT OR IGNORE INTO upc_games (upc, title) VALUES (?, ?)`, code, res.Title)
	} else if err != nil {
		return nil, err
	}
	if gameID.Valid {
		games, err := b.Matcher.games(ctx, []int64{gameID.Int64})
		if err == nil {
			if g, ok := games[gameID.Int64]; ok {
				res.Confirmed = &g
				return res, nil
			}
		}
	}
	if res.Title == "" {
		return res, nil
	}
	cands, err := b.Matcher.Match(ctx, userID, []Row{{Title: CleanProductTitle(res.Title), Status: "backlog"}})
	if err != nil {
		return nil, err
	}
	res.Candidate = &cands[0]
	return res, nil
}

// Confirm remembers which game a code is.
func (b *Barcode) Confirm(ctx context.Context, code string, gameID int64) error {
	if !upcRe.MatchString(code) {
		return ErrBadCode
	}
	_, err := b.DB.ExecContext(ctx, `INSERT INTO upc_games (upc, game_id) VALUES (?, ?)
		ON CONFLICT(upc) DO UPDATE SET game_id = excluded.game_id`, code, gameID)
	return err
}

func (b *Barcode) fetch(ctx context.Context, code string) (string, error) {
	base := b.Endpoint
	if base == "" {
		base = "https://api.upcitemdb.com"
	}
	path := "/prod/trial/lookup"
	if b.Key != "" {
		path = "/prod/v1/lookup"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+url.Values{"upc": {code}}.Encode(), nil)
	if err != nil {
		return "", err
	}
	if b.Key != "" {
		req.Header.Set("user_key", b.Key)
		req.Header.Set("key_type", "3scale")
	}
	client := b.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("barcode lookup: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusTooManyRequests {
		return "", errors.New("the barcode service's daily limit is used up; try again tomorrow or search by name")
	}
	if res.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("barcode lookup: HTTP %d", res.StatusCode)
	}
	var out struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if len(out.Items) == 0 {
		return "", nil
	}
	return out.Items[0].Title, nil
}

var productNoise = regexp.MustCompile(`(?i)\s*[-–(\[]?\s*\b(nintendo switch( 2)?|switch|playstation ?[2345]|ps[2345]|xbox( one| series [xs]( ?\| ?[xs])?| 360)?|pc( dvd)?|wii u?|3ds|standard edition|video ?game|game|new|sealed|region free|(us|eu|uk|pal|ntsc) version|physical)\b\s*[)\]]?`)

// CleanProductTitle strips retail noise ("- Nintendo Switch", "Standard
// Edition", "[PS5]") from a product title so it matches the catalog name.
func CleanProductTitle(t string) string {
	t = productNoise.ReplaceAllString(t, " ")
	t = strings.Trim(strings.Join(strings.Fields(t), " "), " -–:,")
	return t
}
