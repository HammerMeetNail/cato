package library

import (
	"context"
	"errors"
)

// UpNextPick is one suggestion for what to play next.
type UpNextPick struct {
	Item   Item   `json:"item"`
	Pinned bool   `json:"pinned"`
	Reason string `json:"reason"`
}

// UpNext returns the user's pinned queue (still in their backlog, in order)
// followed by backlog picks up to n. Scorer, when set, ranks the unpinned
// backlog (P3 adds time-to-beat and affinity); otherwise the longest-waiting
// games come first.
func (s *Store) UpNext(ctx context.Context, userID string, pinned []int64, n int) ([]UpNextPick, error) {
	out := []UpNextPick{}
	seen := map[int64]bool{}
	for _, id := range pinned {
		it, err := s.Get(ctx, userID, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if it.Status != StatusBacklog && it.Status != StatusPaused && it.Status != StatusWishlist {
			continue
		}
		seen[id] = true
		out = append(out, UpNextPick{Item: *it, Pinned: true, Reason: "In your Up Next queue"})
	}
	if len(out) >= n {
		return out, nil
	}
	if s.Scorer != nil {
		picks, err := s.Scorer(ctx, userID, n-len(out)+len(seen))
		if err != nil {
			return nil, err
		}
		for _, p := range picks {
			if len(out) >= n {
				break
			}
			if !seen[p.Item.GameID] {
				seen[p.Item.GameID] = true
				out = append(out, p)
			}
		}
		return out, nil
	}
	page, err := s.List(ctx, userID, ListQuery{Filter: Filter{Statuses: []string{StatusBacklog}}, Sort: "added_old", Limit: n + len(seen)})
	if err != nil {
		return nil, err
	}
	for _, it := range page.Items {
		if len(out) >= n {
			break
		}
		if !seen[it.GameID] {
			out = append(out, UpNextPick{Item: it, Reason: "Waiting longest in your backlog"})
		}
	}
	return out, nil
}

// Streaks reports the current and longest runs of days with play.
func (s *Store) Streaks(ctx context.Context, userID, today string) (int, int, error) {
	if _, ok := parseDay(today); !ok {
		today = s.now().UTC().Format("2006-01-02")
	}
	return s.streaks(ctx, userID, today)
}
