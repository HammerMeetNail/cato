package notify

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cato/internal/catalog"
	"cato/internal/config"
	"cato/internal/db"
	"cato/internal/library"
	"cato/internal/push"
)

func TestDeliverFollowsSettingsAndDropsGoneSubscriptions(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	db.Migrate(database)
	database.Exec(`INSERT INTO users (id, email, username, display_name) VALUES ('u', 'u@x', 'u', 'U'), ('a', 'a@x', 'alex', 'Alex')`)
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'Hades', 'hades', 'hades')`)
	lib := library.NewStore(database, catalog.NewStore(database))
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	s := &Service{DB: database, Cfg: &config.Config{BaseURL: "https://cato.example"}, Library: lib}
	snd, err := s.Sender(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snd.HTTP = srv.Client()
	// Keys persist across restarts.
	s2 := &Service{DB: database, Cfg: s.Cfg, Library: lib}
	if k, _ := s2.PublicKey(context.Background()); k != snd.Keys.PublicKey() {
		t.Fatal("VAPID key changed between instances")
	}
	ua, _ := ecdh.P256().GenerateKey(nil)
	b64 := base64.RawURLEncoding
	for _, ep := range []string{"/ok", "/gone"} {
		if err := s.Subscribe(context.Background(), "u", push.Subscription{Endpoint: srv.URL + ep, P256dh: b64.EncodeToString(ua.PublicKey().Bytes()), Auth: b64.EncodeToString([]byte("0123456789abcdef"))}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Subscribe(context.Background(), "u", push.Subscription{Endpoint: "http://plain/x", P256dh: "a", Auth: "b"}, ""); err == nil {
		t.Fatal("plain-HTTP endpoints must be refused")
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	database.Exec(`INSERT INTO notifications (user_id, kind, actor_id, created_at) VALUES ('u', 'follow', 'a', ?)`, now)                               // social: off by default
	database.Exec(`INSERT INTO notifications (user_id, kind, game_id, data_json, created_at) VALUES ('u', 'release', 1, '{"platform":"PC"}', ?)`, now) // on
	if err := s.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits["/ok"] != 1 {
		t.Fatalf("one push (the release) expected, got %v", hits)
	}
	var subs, pending int
	database.QueryRow(`SELECT COUNT(*) FROM push_subscriptions`).Scan(&subs)
	database.QueryRow(`SELECT COUNT(*) FROM notifications WHERE pushed_at IS NULL`).Scan(&pending)
	if subs != 1 || pending != 0 {
		t.Fatalf("gone subscription should be dropped (%d) and all marked pushed (%d)", subs, pending)
	}
	// Turning follows on pushes the next one.
	lib.UpdatePrefs(context.Background(), "u", json.RawMessage(`{"notify":{"follow":{"push":true}}}`))
	database.Exec(`INSERT INTO notifications (user_id, kind, actor_id, created_at) VALUES ('u', 'follow', 'a', ?)`, now)
	s.Deliver(context.Background())
	if hits["/ok"] != 2 {
		t.Fatalf("follow push: %v", hits)
	}
}

func TestDigest(t *testing.T) {
	database, _ := db.Open(filepath.Join(t.TempDir(), "d.db"))
	defer database.Close()
	db.Migrate(database)
	database.Exec(`INSERT INTO users (id, email, username) VALUES ('u', 'u@example.com', 'u'), ('v', 'v@example.com', 'v'), ('a', 'a@x', 'alex')`)
	lib := library.NewStore(database, catalog.NewStore(database))
	lib.UpdatePrefs(context.Background(), "u", json.RawMessage(`{"email_digest":true}`))
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	database.Exec(`INSERT INTO notifications (user_id, kind, actor_id, created_at) VALUES ('u', 'follow', 'a', ?), ('v', 'follow', 'a', ?)`, now, now)
	var sent []string
	s := &Service{DB: database, Cfg: &config.Config{SMTPHost: "smtp.example", SMTPFrom: "cato@example.com", BaseURL: "https://cato.example"}, Library: lib,
		SendMail: func(to string, msg []byte) error { sent = append(sent, to+"\n"+string(msg)); return nil }}
	if err := s.Digest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "u@example.com") || !strings.Contains(sent[0], "alex followed you") || !strings.Contains(sent[0], "https://cato.example/#you/people") {
		t.Fatalf("digest %q", sent)
	}
	s.Digest(context.Background())
	if len(sent) != 1 {
		t.Fatal("each notification is emailed once")
	}
}
