package library

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Event is something worth showing in friends' feeds (plan §7). Library
// writes record them with the write's undo ID, so an undo takes them back.
type Event struct {
	Kind   string         `json:"kind"`
	GameID int64          `json:"game_id"`
	Data   map[string]any `json:"data,omitempty"`
}

// Event kinds.
const (
	EventAdded      = "added"
	EventWishlisted = "wishlisted"
	EventStarted    = "started"
	EventFinished   = "finished"
	EventDropped    = "dropped"
	EventLogged     = "logged"
	EventRated      = "rated"
)

// events describes one item write for the feed.
func events(prev *Item, st itemState, ch Change, gameID int64, sess *Session) []Event {
	var out []Event
	prevStatus := ""
	if prev != nil {
		prevStatus = prev.Status
	}
	if st.status != prevStatus {
		switch st.status {
		case StatusWishlist:
			out = append(out, Event{Kind: EventWishlisted, GameID: gameID})
		case StatusPlaying:
			data := map[string]any{}
			if prevStatus == StatusCompleted || prevStatus == StatusAbandoned {
				data["replay"] = true
			}
			if prevStatus != StatusPaused {
				out = append(out, Event{Kind: EventStarted, GameID: gameID, Data: data})
			}
		case StatusCompleted:
			data := map[string]any{"rating": st.rating}
			if ch.Completion != nil {
				data["completion"] = *ch.Completion
			}
			out = append(out, Event{Kind: EventFinished, GameID: gameID, Data: data})
		case StatusAbandoned:
			out = append(out, Event{Kind: EventDropped, GameID: gameID})
		case StatusBacklog:
			if prev == nil {
				out = append(out, Event{Kind: EventAdded, GameID: gameID, Data: map[string]any{"status": st.status}})
			}
		}
	} else if ch.Rating != nil && prev != nil && st.rating != prev.Rating && st.rating > 0 {
		out = append(out, Event{Kind: EventRated, GameID: gameID, Data: map[string]any{"rating": st.rating}})
	}
	if sess != nil && sess.Minutes > 0 && (sess.Source == "manual" || sess.Source == "timer") {
		out = append(out, Event{Kind: EventLogged, GameID: gameID, Data: map[string]any{"minutes": sess.Minutes, "played_on": sess.PlayedOn}})
	}
	return out
}

func recordEvents(ctx context.Context, tx *sql.Tx, userID, undoID string, evs []Event) error {
	for _, e := range evs {
		data := "{}"
		if len(e.Data) > 0 {
			b, _ := json.Marshal(e.Data)
			data = string(b)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO activity (user_id, kind, game_id, data_json, undo_id) VALUES (?, ?, ?, ?, NULLIF(?, ''))`,
			userID, e.Kind, e.GameID, data, undoID); err != nil {
			return err
		}
	}
	return nil
}
