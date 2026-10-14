// Package notify delivers inbox notifications beyond the app (plan §8.2):
// Web Push to installed apps and browsers, and an optional weekly email
// digest. What is delivered follows each person's settings; the inbox
// always has everything.
package notify

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cato/internal/config"
	"cato/internal/db"
	"cato/internal/library"
	"cato/internal/push"
)

type Service struct {
	DB      *db.DB
	Cfg     *config.Config
	Library *library.Store
	Now     func() time.Time

	mu     sync.Mutex
	sender *push.Sender
	// SendMail is net/smtp delivery (tests replace it).
	SendMail func(to string, msg []byte) error
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Sender returns the push sender, creating and storing VAPID keys the
// first time.
func (s *Service) Sender(ctx context.Context) (*push.Sender, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sender != nil {
		return s.sender, nil
	}
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM instance_settings WHERE key = 'vapid_private'`).Scan(&raw)
	var keys push.Keys
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if keys, err = push.GenerateKeys(); err != nil {
			return nil, err
		}
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO instance_settings (key, value) VALUES ('vapid_private', ?) ON CONFLICT(key) DO NOTHING`, keys.Marshal()); err != nil {
			return nil, err
		}
		// Another process may have won the insert: use what's stored.
		if err := s.DB.QueryRowContext(ctx, `SELECT value FROM instance_settings WHERE key = 'vapid_private'`).Scan(&raw); err != nil {
			return nil, err
		}
		fallthrough
	case err == nil:
		if keys, err = push.ParseKeys(raw); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	subject := s.Cfg.PushContact
	if subject == "" {
		subject = s.Cfg.BaseURL
	}
	if subject == "" {
		subject = "mailto:admin@localhost"
	}
	s.sender = &push.Sender{Keys: keys, Subject: subject}
	return s.sender, nil
}

// PublicKey is the applicationServerKey for PushManager.subscribe.
func (s *Service) PublicKey(ctx context.Context) (string, error) {
	snd, err := s.Sender(ctx)
	if err != nil {
		return "", err
	}
	return snd.Keys.PublicKey(), nil
}

// Subscribe stores a browser's push subscription for the user.
func (s *Service) Subscribe(ctx context.Context, userID string, sub push.Subscription, userAgent string) error {
	if !strings.HasPrefix(sub.Endpoint, "https://") || len(sub.Endpoint) > 2000 || sub.P256dh == "" || sub.Auth == "" {
		return errors.New("invalid push subscription")
	}
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, user_agent) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET user_id = excluded.user_id, p256dh = excluded.p256dh, auth = excluded.auth,
		  user_agent = excluded.user_agent, failures = 0`, userID, sub.Endpoint, sub.P256dh, sub.Auth, userAgent)
	return err
}

func (s *Service) Unsubscribe(ctx context.Context, userID, endpoint string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint)
	return err
}

// Devices lists the user's push-enabled browsers.
func (s *Service) Devices(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT endpoint, user_agent, created_at, COALESCE(last_ok_at, '') FROM push_subscriptions WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var ep, ua, created, ok string
		rows.Scan(&ep, &ua, &created, &ok)
		out = append(out, map[string]any{"endpoint": ep, "user_agent": ua, "created_at": created, "last_ok_at": ok})
	}
	return out, rows.Err()
}

// Message is what the service worker shows.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
	Badge int    `json:"badge"`
	Icon  string `json:"icon,omitempty"`
}

type pending struct {
	id                 int64
	user, kind         string
	actor, game, cover string
	gameID             int64
	data               map[string]any
}

// Describe writes a notification as text (push and email).
func describe(p pending) (title, body, url string) {
	who := p.actor
	if who == "" {
		who = "Someone"
	}
	url = "/#you/inbox"
	if p.gameID != 0 {
		url = "/#game/" + strconv.FormatInt(p.gameID, 10)
	}
	str := func(k string) string { v, _ := p.data[k].(string); return v }
	num := func(k string) float64 { v, _ := p.data[k].(float64); return v }
	switch p.kind {
	case "follow":
		return "New follower", who + " followed you", "/#you/people"
	case "follow_request":
		return "Follow request", who + " asked to follow you", "/#you/people"
	case "follow_accepted":
		return "Request accepted", who + " accepted your follow request", url
	case "like":
		return "Your review got a like", who + " liked your review of " + p.game, url
	case "invite_used":
		return "Invite used", who + " joined with your invite", "/#you/people"
	case "release":
		b := p.game + " is out today"
		if pl := str("platform"); pl != "" {
			b += " on " + pl
		}
		return "Out today", b, url
	case "release_soon":
		return "Coming up", p.game + " comes out " + str("when"), url
	case "price":
		b := p.game
		if c := num("cut"); c > 0 {
			b += fmt.Sprintf(" is %d%% off", int(c))
		} else {
			b += " is on sale"
		}
		if shop := str("shop"); shop != "" {
			b += " at " + shop
		}
		if pr := str("price"); pr != "" {
			b += " (" + pr + ")"
		}
		if low, _ := p.data["low"].(bool); low {
			b += " — historical low"
		}
		return "Price drop", b, url
	}
	return "Cato", strings.ReplaceAll(p.kind, "_", " "), url
}

// Deliver pushes notifications created in the last six hours that haven't
// been pushed yet, to everyone whose settings ask for that kind.
func (s *Service) Deliver(ctx context.Context) error {
	since := s.now().Add(-6 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	rows, err := s.DB.QueryContext(ctx, `SELECT n.id, n.user_id, n.kind, COALESCE(n.game_id, 0), n.data_json,
			COALESCE(NULLIF(u.display_name, ''), u.username, ''), COALESCE(g.name, ''), COALESCE(g.local_cover_path, '')
		FROM notifications n
		LEFT JOIN users u ON u.id = n.actor_id
		LEFT JOIN games g ON g.id = n.game_id
		WHERE n.pushed_at IS NULL AND n.read_at IS NULL AND n.created_at >= ?
		  AND EXISTS (SELECT 1 FROM push_subscriptions ps WHERE ps.user_id = n.user_id)
		ORDER BY n.id LIMIT 500`, since)
	if err != nil {
		return err
	}
	var ps []pending
	for rows.Next() {
		var p pending
		var data string
		if err := rows.Scan(&p.id, &p.user, &p.kind, &p.gameID, &data, &p.actor, &p.game, &p.cover); err != nil {
			rows.Close()
			return err
		}
		json.Unmarshal([]byte(data), &p.data)
		ps = append(ps, p)
	}
	rows.Close()
	if len(ps) == 0 {
		return nil
	}
	snd, err := s.Sender(ctx)
	if err != nil {
		return err
	}
	prefs := map[string]library.Prefs{}
	for _, p := range ps {
		pr, ok := prefs[p.user]
		if !ok {
			if pr, err = s.Library.GetPrefs(ctx, p.user); err != nil {
				return err
			}
			prefs[p.user] = pr
		}
		s.DB.ExecContext(ctx, `UPDATE notifications SET pushed_at = ? WHERE id = ?`, s.now().UTC().Format(time.RFC3339), p.id)
		if !pr.Notify[p.kind].Push {
			continue
		}
		var unread int
		s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL`, p.user).Scan(&unread)
		title, body, url := describe(p)
		msg, _ := json.Marshal(Message{Title: title, Body: body, URL: url, Tag: p.kind + "-" + strconv.FormatInt(p.gameID, 10), Badge: unread, Icon: p.cover})
		s.sendAll(ctx, snd, p.user, msg)
	}
	return nil
}

func (s *Service) sendAll(ctx context.Context, snd *push.Sender, userID string, msg []byte) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, endpoint, p256dh, auth FROM push_subscriptions WHERE user_id = ?`, userID)
	if err != nil {
		return
	}
	type sub struct {
		id int64
		push.Subscription
	}
	var subs []sub
	for rows.Next() {
		var x sub
		rows.Scan(&x.id, &x.Endpoint, &x.P256dh, &x.Auth)
		subs = append(subs, x)
	}
	rows.Close()
	for _, x := range subs {
		err := snd.Send(ctx, x.Subscription, msg, 24*time.Hour)
		switch {
		case errors.Is(err, push.ErrGone):
			s.DB.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id = ?`, x.id)
		case err != nil:
			slog.WarnContext(ctx, "push", "err", err)
			// Five failures in a row and the subscription is dropped.
			s.DB.ExecContext(ctx, `UPDATE push_subscriptions SET failures = failures + 1 WHERE id = ?`, x.id)
			s.DB.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id = ? AND failures >= 5`, x.id)
		default:
			s.DB.ExecContext(ctx, `UPDATE push_subscriptions SET last_ok_at = ?, failures = 0 WHERE id = ?`, s.now().UTC().Format(time.RFC3339), x.id)
		}
	}
}

// Test sends a test push to the user's devices right away.
func (s *Service) Test(ctx context.Context, userID string) error {
	snd, err := s.Sender(ctx)
	if err != nil {
		return err
	}
	msg, _ := json.Marshal(Message{Title: "Cato", Body: "Notifications work on this device.", URL: "/#you/inbox", Tag: "test"})
	s.sendAll(ctx, snd, userID, msg)
	return nil
}

// EmailEnabled reports whether the server can send email.
func (s *Service) EmailEnabled() bool { return s.Cfg.SMTPHost != "" && s.Cfg.SMTPFrom != "" }

// Digest emails each person who asked for it their unread notifications
// from the past week (weekly job).
func (s *Service) Digest(ctx context.Context) error {
	if !s.EmailEnabled() {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT n.user_id, u.email FROM notifications n JOIN users u ON u.id = n.user_id
		WHERE n.emailed_at IS NULL AND n.read_at IS NULL AND u.disabled = 0 AND n.created_at >= ?`,
		s.now().Add(-8*24*time.Hour).UTC().Format("2006-01-02T15:04:05Z"))
	if err != nil {
		return err
	}
	type person struct{ id, email string }
	var people []person
	for rows.Next() {
		var p person
		rows.Scan(&p.id, &p.email)
		people = append(people, p)
	}
	rows.Close()
	for _, p := range people {
		pr, err := s.Library.GetPrefs(ctx, p.id)
		if err != nil || !pr.EmailDigest {
			continue
		}
		if err := s.digestOne(ctx, p.id, p.email); err != nil {
			slog.WarnContext(ctx, "email digest", "err", err)
		}
	}
	return nil
}

func (s *Service) digestOne(ctx context.Context, userID, email string) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT n.id, n.kind, COALESCE(n.game_id, 0), n.data_json,
			COALESCE(NULLIF(u.display_name, ''), u.username, ''), COALESCE(g.name, '')
		FROM notifications n LEFT JOIN users u ON u.id = n.actor_id LEFT JOIN games g ON g.id = n.game_id
		WHERE n.user_id = ? AND n.emailed_at IS NULL AND n.read_at IS NULL ORDER BY n.id DESC LIMIT 50`, userID)
	if err != nil {
		return err
	}
	var lines []string
	var ids []int64
	base := strings.TrimSuffix(s.Cfg.BaseURL, "/")
	for rows.Next() {
		var p pending
		var data string
		rows.Scan(&p.id, &p.kind, &p.gameID, &data, &p.actor, &p.game)
		json.Unmarshal([]byte(data), &p.data)
		_, body, url := describe(p)
		line := "• " + body
		if base != "" {
			line += "\n  " + base + url
		}
		lines = append(lines, line)
		ids = append(ids, p.id)
	}
	rows.Close()
	if len(lines) == 0 {
		return nil
	}
	text := "Here's what happened on Cato this week:\n\n" + strings.Join(lines, "\n") +
		"\n\nYou get this because the weekly email is on in Settings → Notifications.\n"
	msg := []byte("From: Cato <" + s.Cfg.SMTPFrom + ">\r\nTo: " + email + "\r\nSubject: Your week on Cato\r\n" +
		"Date: " + s.now().Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\nAuto-Submitted: auto-generated\r\n\r\n" + strings.ReplaceAll(text, "\n", "\r\n"))
	send := s.SendMail
	if send == nil {
		send = s.smtpSend
	}
	if err := send(email, msg); err != nil {
		return err
	}
	stamp := s.now().UTC().Format(time.RFC3339)
	for _, id := range ids {
		s.DB.ExecContext(ctx, `UPDATE notifications SET emailed_at = ? WHERE id = ?`, stamp, id)
	}
	return nil
}

// smtpSend uses implicit TLS on port 465 and STARTTLS otherwise (net/smtp
// refuses to send a password without TLS except to localhost).
func (s *Service) smtpSend(to string, msg []byte) error {
	c := s.Cfg
	addr := net.JoinHostPort(c.SMTPHost, strconv.Itoa(c.SMTPPort))
	var auth smtp.Auth
	if c.SMTPUser != "" {
		auth = smtp.PlainAuth("", c.SMTPUser, c.SMTPPassword, c.SMTPHost)
	}
	if c.SMTPPort != 465 {
		return smtp.SendMail(addr, auth, c.SMTPFrom, []string{to}, msg)
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: c.SMTPHost, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	cl, err := smtp.NewClient(conn, c.SMTPHost)
	if err != nil {
		return err
	}
	defer cl.Close()
	if auth != nil {
		if err := cl.Auth(auth); err != nil {
			return err
		}
	}
	if err := cl.Mail(c.SMTPFrom); err != nil {
		return err
	}
	if err := cl.Rcpt(to); err != nil {
		return err
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}
