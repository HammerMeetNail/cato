package db

import (
	"database/sql"
	"fmt"
	"sort"
)

type Migration struct {
	Version int
	Up      string
}

var migrations = []Migration{
	{
		Version: 24,
		// version_parent is 0 for 99% of games, but ANALYZE samples at most
		// analysis_limit rows per index and recorded "= 0" as matching 4 of
		// 312k, so SQLite started joins by walking every base game (the game
		// page's series lookup took ~470 ms on the NAS). The index only
		// serves "editions of game X"; without the zeros, no "= 0" filter
		// can use it.
		Up: `DROP INDEX IF EXISTS idx_games_version_parent;
		     CREATE INDEX idx_games_version_parent ON games(version_parent) WHERE version_parent != 0;`,
	},
	{
		Version: 23,
		// Goals (plan §10): "Finish 24 games in 2026", "Clear 10 from the
		// backlog", "Play 5 games from before 2000", "Play 200 hours".
		Up: `CREATE TABLE goals (
		       id INTEGER PRIMARY KEY AUTOINCREMENT,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       year INTEGER NOT NULL,
		       kind TEXT NOT NULL CHECK (kind IN ('finish', 'backlog', 'retro', 'hours')),
		       target INTEGER NOT NULL CHECK (target > 0 AND target <= 100000),
		       param INTEGER NOT NULL DEFAULT 0,
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		     );
		     CREATE INDEX idx_goals_user ON goals(user_id, year);`,
	},
	{
		Version: 22,
		// Reach (plan §8): import batches (undoable as a whole), Steam and
		// RetroAchievements links, Web Push subscriptions, alert
		// de-duplication, IsThereAnyDeal IDs, the barcode cache and passkeys.
		Up: `CREATE TABLE import_batches (
		       id TEXT PRIMARY KEY,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       source TEXT NOT NULL,
		       items INTEGER NOT NULL DEFAULT 0,
		       snapshot_json TEXT NOT NULL,
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       undone_at TEXT
		     );
		     CREATE INDEX idx_import_batches_user ON import_batches(user_id, created_at DESC);

		     CREATE TABLE steam_links (
		       user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		       steam_id TEXT NOT NULL,
		       persona TEXT NOT NULL DEFAULT '',
		       sync INTEGER NOT NULL DEFAULT 0,
		       last_sync_at TEXT,
		       last_error TEXT NOT NULL DEFAULT ''
		     );
		     -- The playtime Steam last reported per app, so a sync logs the change.
		     CREATE TABLE steam_playtime (
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       appid INTEGER NOT NULL,
		       minutes INTEGER NOT NULL,
		       PRIMARY KEY (user_id, appid)
		     );

		     CREATE TABLE ra_links (
		       user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		       ra_user TEXT NOT NULL,
		       api_key TEXT NOT NULL
		     );

		     CREATE TABLE push_subscriptions (
		       id INTEGER PRIMARY KEY AUTOINCREMENT,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       endpoint TEXT NOT NULL UNIQUE,
		       p256dh TEXT NOT NULL,
		       auth TEXT NOT NULL,
		       user_agent TEXT NOT NULL DEFAULT '',
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       last_ok_at TEXT,
		       failures INTEGER NOT NULL DEFAULT 0
		     );
		     CREATE INDEX idx_push_user ON push_subscriptions(user_id);
		     ALTER TABLE notifications ADD COLUMN pushed_at TEXT;
		     ALTER TABLE notifications ADD COLUMN emailed_at TEXT;

		     -- One alert per (user, game, kind, key): a release day, a deal.
		     CREATE TABLE alerts_sent (
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       game_id INTEGER NOT NULL,
		       kind TEXT NOT NULL,
		       key TEXT NOT NULL,
		       sent_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       PRIMARY KEY (user_id, game_id, kind, key)
		     );

		     CREATE TABLE game_itad (
		       game_id INTEGER PRIMARY KEY REFERENCES games(id) ON DELETE CASCADE,
		       itad_id TEXT NOT NULL DEFAULT '',
		       checked_at TEXT NOT NULL
		     );

		     CREATE TABLE upc_games (
		       upc TEXT PRIMARY KEY,
		       title TEXT NOT NULL DEFAULT '',
		       game_id INTEGER REFERENCES games(id) ON DELETE SET NULL,
		       looked_up_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		     );

		     CREATE TABLE webauthn_credentials (
		       id TEXT PRIMARY KEY,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       public_key BLOB NOT NULL,
		       algorithm INTEGER NOT NULL,
		       sign_count INTEGER NOT NULL DEFAULT 0,
		       name TEXT NOT NULL DEFAULT '',
		       transports TEXT NOT NULL DEFAULT '',
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       last_used_at TEXT
		     );
		     CREATE INDEX idx_webauthn_user ON webauthn_credentials(user_id);`,
	},
	{
		Version: 21,
		// People (plan §7), limited to this server and private by default.
		// The oldest account becomes the admin. Usernames are generated from
		// the email's local part (people can change them); profile
		// visibility starts private.
		Up: `ALTER TABLE users ADD COLUMN username TEXT;
		     ALTER TABLE users ADD COLUMN bio TEXT NOT NULL DEFAULT '';
		     ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin'));
		     ALTER TABLE users ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'followers', 'instance'));
		     ALTER TABLE users ADD COLUMN avatar_path TEXT NOT NULL DEFAULT '';
		     ALTER TABLE users ADD COLUMN use_gravatar INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE users ADD COLUMN favourites_json TEXT NOT NULL DEFAULT '[]';
		     UPDATE users SET username = lower(substr(replace(replace(substr(email, 1, instr(email, '@') - 1), '.', '_'), '+', '_'), 1, 24))
		       || CASE WHEN (SELECT COUNT(*) FROM users u2 WHERE lower(substr(replace(replace(substr(u2.email, 1, instr(u2.email, '@') - 1), '.', '_'), '+', '_'), 1, 24))
		                       = lower(substr(replace(replace(substr(users.email, 1, instr(users.email, '@') - 1), '.', '_'), '+', '_'), 1, 24))
		                       AND u2.rowid < users.rowid) > 0 THEN '_' || rowid ELSE '' END;
		     CREATE UNIQUE INDEX idx_users_username ON users(username COLLATE NOCASE);
		     UPDATE users SET role = 'admin' WHERE rowid = (SELECT MIN(rowid) FROM users);

		     CREATE TABLE instance_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);

		     CREATE TABLE invites (
		       token_hash TEXT PRIMARY KEY,
		       created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       note TEXT NOT NULL DEFAULT '',
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       expires_at TEXT NOT NULL,
		       used_by TEXT REFERENCES users(id) ON DELETE SET NULL,
		       used_at TEXT,
		       revoked_at TEXT
		     );
		     CREATE INDEX idx_invites_creator ON invites(created_by, created_at DESC);

		     CREATE TABLE reset_tokens (
		       token_hash TEXT PRIMARY KEY,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       created_by TEXT,
		       expires_at TEXT NOT NULL,
		       used_at TEXT
		     );

		     CREATE TABLE follows (
		       follower_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       followee_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted')),
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       PRIMARY KEY (follower_id, followee_id),
		       CHECK (follower_id != followee_id)
		     );
		     CREATE INDEX idx_follows_followee ON follows(followee_id, status);

		     CREATE TABLE blocks (
		       blocker_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       blocked_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       PRIMARY KEY (blocker_id, blocked_id)
		     );
		     CREATE INDEX idx_blocks_blocked ON blocks(blocked_id);

		     CREATE TABLE reviews (
		       id INTEGER PRIMARY KEY AUTOINCREMENT,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		       playthrough_id INTEGER REFERENCES playthroughs(id) ON DELETE SET NULL,
		       body TEXT NOT NULL,
		       spoiler INTEGER NOT NULL DEFAULT 0,
		       visibility TEXT NOT NULL DEFAULT 'inherit' CHECK (visibility IN ('inherit', 'private', 'followers', 'instance')),
		       rating INTEGER NOT NULL DEFAULT 0,
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       hidden_at TEXT,
		       hidden_by TEXT
		     );
		     CREATE INDEX idx_reviews_game ON reviews(game_id, created_at DESC);
		     CREATE INDEX idx_reviews_user ON reviews(user_id, created_at DESC);

		     CREATE TABLE review_likes (
		       review_id INTEGER NOT NULL REFERENCES reviews(id) ON DELETE CASCADE,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       PRIMARY KEY (review_id, user_id)
		     );

		     CREATE TABLE reports (
		       id INTEGER PRIMARY KEY AUTOINCREMENT,
		       reporter_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       kind TEXT NOT NULL CHECK (kind IN ('review', 'list', 'user')),
		       target TEXT NOT NULL,
		       reason TEXT NOT NULL DEFAULT '',
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       resolved_at TEXT,
		       resolved_by TEXT
		     );

		     CREATE TABLE lists (
		       id INTEGER PRIMARY KEY AUTOINCREMENT,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       title TEXT NOT NULL,
		       description TEXT NOT NULL DEFAULT '',
		       ranked INTEGER NOT NULL DEFAULT 0,
		       visibility TEXT NOT NULL DEFAULT 'inherit' CHECK (visibility IN ('inherit', 'private', 'followers', 'instance')),
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       hidden_at TEXT
		     );
		     CREATE INDEX idx_lists_user ON lists(user_id, updated_at DESC);
		     CREATE TABLE list_items (
		       list_id INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
		       game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		       position INTEGER NOT NULL DEFAULT 0,
		       note TEXT NOT NULL DEFAULT '',
		       added_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       PRIMARY KEY (list_id, game_id)
		     );
		     CREATE INDEX idx_list_items_game ON list_items(game_id);

		     -- The feed: written when things happen (fan-out on read through
		     -- follows). undo_id lets an undone write take its activity back.
		     CREATE TABLE activity (
		       id INTEGER PRIMARY KEY AUTOINCREMENT,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       kind TEXT NOT NULL,
		       game_id INTEGER,
		       ref_id INTEGER,
		       data_json TEXT NOT NULL DEFAULT '{}',
		       undo_id TEXT,
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		     );
		     CREATE INDEX idx_activity_user ON activity(user_id, id DESC);
		     CREATE INDEX idx_activity_undo ON activity(undo_id) WHERE undo_id IS NOT NULL;

		     CREATE TABLE notifications (
		       id INTEGER PRIMARY KEY AUTOINCREMENT,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       kind TEXT NOT NULL,
		       actor_id TEXT,
		       game_id INTEGER,
		       ref_id INTEGER,
		       data_json TEXT NOT NULL DEFAULT '{}',
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
		       read_at TEXT
		     );
		     CREATE INDEX idx_notifications_user ON notifications(user_id, id DESC);

		     CREATE TABLE audit_log (
		       id INTEGER PRIMARY KEY AUTOINCREMENT,
		       actor_id TEXT,
		       action TEXT NOT NULL,
		       target TEXT NOT NULL DEFAULT '',
		       data_json TEXT NOT NULL DEFAULT '{}',
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		     );`,
	},
	{
		Version: 20,
		// Rich catalog (plan §6.1). Queried relations get tables; display-only
		// detail (media, links, ratings, languages, multiplayer) is one JSON
		// document per game.
		// - lookups: genre/theme/mode/perspective/game type names by IGDB ID.
		// - games: time to beat (minutes), theme/mode/perspective IDs, when
		//   details were fetched, where the row came from (igdb or custom);
		//   igdb_popularity now holds IGDB PopScore.
		// - custom entries use negative IDs, so every library feature works.
		Up: `CREATE TABLE lookups (
		       kind TEXT NOT NULL,
		       id INTEGER NOT NULL,
		       name TEXT NOT NULL,
		       slug TEXT NOT NULL DEFAULT '',
		       PRIMARY KEY (kind, id)
		     );
		     ALTER TABLE games ADD COLUMN ttb_hastily INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN ttb_normally INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN ttb_completely INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN ttb_count INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN themes_json TEXT NOT NULL DEFAULT '[]';
		     ALTER TABLE games ADD COLUMN modes_json TEXT NOT NULL DEFAULT '[]';
		     ALTER TABLE games ADD COLUMN perspectives_json TEXT NOT NULL DEFAULT '[]';
		     ALTER TABLE games ADD COLUMN details_fetched_at INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN source TEXT NOT NULL DEFAULT 'igdb';
		     ALTER TABLE games ADD COLUMN created_by TEXT;
		     CREATE INDEX idx_games_details_fetched ON games(details_fetched_at);
		     CREATE INDEX idx_games_popscore ON games(igdb_popularity DESC) WHERE igdb_popularity > 0;
		     CREATE INDEX idx_games_release ON games(first_release_date);

		     CREATE TABLE game_details (
		       game_id INTEGER PRIMARY KEY REFERENCES games(id) ON DELETE CASCADE,
		       json TEXT NOT NULL DEFAULT '{}',
		       fetched_at INTEGER NOT NULL DEFAULT 0
		     );
		     CREATE TABLE game_relations (
		       game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		       related_id INTEGER NOT NULL,
		       kind TEXT NOT NULL,
		       PRIMARY KEY (game_id, kind, related_id)
		     );
		     CREATE INDEX idx_game_relations_related ON game_relations(related_id, kind);
		     CREATE TABLE game_release_dates (
		       game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		       platform_id INTEGER NOT NULL DEFAULT 0,
		       date INTEGER NOT NULL DEFAULT 0,
		       region INTEGER NOT NULL DEFAULT 0,
		       human TEXT NOT NULL DEFAULT '',
		       PRIMARY KEY (game_id, platform_id, region)
		     );
		     CREATE INDEX idx_release_dates_date ON game_release_dates(date);
		     CREATE TABLE game_companies (
		       game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		       company_id INTEGER NOT NULL,
		       name TEXT NOT NULL,
		       developer INTEGER NOT NULL DEFAULT 0,
		       publisher INTEGER NOT NULL DEFAULT 0,
		       PRIMARY KEY (game_id, company_id)
		     );
		     CREATE INDEX idx_game_companies_company ON game_companies(company_id);
		     CREATE TABLE game_series (
		       game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		       series_id INTEGER NOT NULL,
		       kind TEXT NOT NULL,
		       name TEXT NOT NULL,
		       PRIMARY KEY (game_id, kind, series_id)
		     );
		     CREATE INDEX idx_game_series_series ON game_series(kind, series_id);
		     CREATE TABLE game_external_ids (
		       game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		       source INTEGER NOT NULL,
		       uid TEXT NOT NULL,
		       PRIMARY KEY (source, uid, game_id)
		     );
		     CREATE INDEX idx_game_external_game ON game_external_ids(game_id);
		     CREATE TABLE game_popularity (
		       game_id INTEGER NOT NULL,
		       type INTEGER NOT NULL,
		       value REAL NOT NULL,
		       updated_at INTEGER NOT NULL,
		       PRIMARY KEY (type, game_id)
		     );
		     CREATE INDEX idx_game_popularity_rank ON game_popularity(type, value DESC);
		     ALTER TABLE playthroughs ADD COLUMN edition_game_id INTEGER;
		     -- The library index now carries time to beat too.
		     DROP TRIGGER games_library_rev_au;
		     CREATE TRIGGER games_library_rev_au AFTER UPDATE OF name, cover_url, local_cover_path, first_release_date, ttb_normally ON games
		       WHEN old.name IS NOT new.name OR old.cover_url IS NOT new.cover_url
		         OR old.local_cover_path IS NOT new.local_cover_path OR old.first_release_date IS NOT new.first_release_date
		         OR old.ttb_normally IS NOT new.ttb_normally BEGIN
		       UPDATE library_items SET rev = rev WHERE game_id = new.id;
		     END;`,
	},
	{
		Version: 19,
		// Sessions and devices (You → Settings): which browser a session
		// belongs to and when it was last used, so people can review and
		// revoke their signed-in devices.
		Up: `ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
		     ALTER TABLE sessions ADD COLUMN last_seen_at TEXT;`,
	},
	{
		Version: 18,
		// Journal foundations (Cato Smooth plan §4.1):
		// - library_items is rebuilt to add the 'paused' status (a CHECK
		//   constraint can't be altered in place), current_playthrough_id and
		//   rev (the library_rev at which the row last changed).
		// - user_state holds the per-user library_rev counter that drives
		//   ETags and the ?since= delta index, plus preferences and the
		//   running session timer. library_tombstones records deletions for
		//   delta sync.
		// - playthroughs and play_sessions record replays and per-day play.
		//   Triggers keep library_items.playtime_minutes equal to the sum of
		//   the item's sessions; legacy totals become one 'legacy' session so
		//   no total changes.
		// Rebuilding library_items later needs foreign_keys=OFF: DROP TABLE
		// would cascade into playthroughs and play_sessions.
		Up: `CREATE TABLE library_items_v18 (
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		       status TEXT NOT NULL CHECK (status IN ('wishlist', 'backlog', 'playing', 'paused', 'completed', 'abandoned')),
		       rating INTEGER NOT NULL DEFAULT 0 CHECK (rating >= 0 AND rating <= 100),
		       playtime_minutes INTEGER NOT NULL DEFAULT 0 CHECK (playtime_minutes >= 0),
		       tags_json TEXT NOT NULL DEFAULT '[]',
		       notes TEXT NOT NULL DEFAULT '',
		       started_at TEXT,
		       completed_at TEXT,
		       created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		       updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		       platform TEXT NOT NULL DEFAULT '',
		       medium TEXT NOT NULL DEFAULT '',
		       owned_platforms_json TEXT NOT NULL DEFAULT '[]',
		       current_playthrough_id INTEGER,
		       rev INTEGER NOT NULL DEFAULT 0,
		       PRIMARY KEY (user_id, game_id)
		     );
		     INSERT INTO library_items_v18 (user_id, game_id, status, rating, playtime_minutes, tags_json, notes,
		         started_at, completed_at, created_at, updated_at, platform, medium, owned_platforms_json, rev)
		       SELECT user_id, game_id, status, rating, playtime_minutes, tags_json, notes,
		         started_at, completed_at, created_at, updated_at, platform, medium, owned_platforms_json, 1
		       FROM library_items;
		     DROP TABLE library_items;
		     ALTER TABLE library_items_v18 RENAME TO library_items;
		     CREATE INDEX idx_library_user_status ON library_items(user_id, status);
		     CREATE INDEX idx_library_game ON library_items(game_id);
		     CREATE INDEX idx_library_user_updated ON library_items(user_id, updated_at DESC, game_id DESC);
		     CREATE INDEX idx_library_user_rev ON library_items(user_id, rev);

		     CREATE TABLE user_state (
		       user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		       library_rev INTEGER NOT NULL DEFAULT 0,
		       prefs_json TEXT NOT NULL DEFAULT '{}',
		       timer_game_id INTEGER,
		       timer_started_at TEXT
		     );
		     INSERT INTO user_state (user_id, library_rev)
		       SELECT u.id, CASE WHEN EXISTS (SELECT 1 FROM library_items li WHERE li.user_id = u.id) THEN 1 ELSE 0 END
		       FROM users u;

		     -- No foreign key: a tombstone outlives the row it records, and
		     -- users_ad_tombstones cleans up after account deletion.
		     CREATE TABLE library_tombstones (
		       user_id TEXT NOT NULL,
		       game_id INTEGER NOT NULL,
		       rev INTEGER NOT NULL,
		       PRIMARY KEY (user_id, game_id)
		     );
		     CREATE INDEX idx_library_tombstones_rev ON library_tombstones(user_id, rev);

		     CREATE TABLE playthroughs (
		       id INTEGER PRIMARY KEY AUTOINCREMENT,
		       user_id TEXT NOT NULL,
		       game_id INTEGER NOT NULL,
		       platform TEXT NOT NULL DEFAULT '',
		       started_on TEXT,
		       finished_on TEXT,
		       completion TEXT CHECK (completion IS NULL OR completion IN ('main', 'main_extras', 'completionist', 'endless', 'dropped')),
		       minutes INTEGER NOT NULL DEFAULT 0 CHECK (minutes >= 0),
		       rating INTEGER CHECK (rating IS NULL OR (rating >= 0 AND rating <= 100)),
		       note TEXT NOT NULL DEFAULT '',
		       created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		       FOREIGN KEY (user_id, game_id) REFERENCES library_items(user_id, game_id) ON DELETE CASCADE
		     );
		     CREATE INDEX idx_pt_user_finished ON playthroughs(user_id, finished_on DESC);
		     CREATE INDEX idx_pt_item ON playthroughs(user_id, game_id);

		     CREATE TABLE play_sessions (
		       id INTEGER PRIMARY KEY AUTOINCREMENT,
		       user_id TEXT NOT NULL,
		       game_id INTEGER NOT NULL,
		       playthrough_id INTEGER REFERENCES playthroughs(id) ON DELETE CASCADE,
		       played_on TEXT NOT NULL,
		       minutes INTEGER NOT NULL CHECK (minutes != 0
		         AND (minutes > 0 OR source = 'adjust')
		         AND (minutes <= 1440 OR source IN ('legacy', 'adjust', 'steam', 'import'))),
		       note TEXT NOT NULL DEFAULT '',
		       source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'timer', 'legacy', 'adjust', 'steam', 'import')),
		       created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		       FOREIGN KEY (user_id, game_id) REFERENCES library_items(user_id, game_id) ON DELETE CASCADE
		     );
		     CREATE INDEX idx_sessions_user_day ON play_sessions(user_id, played_on DESC);
		     CREATE INDEX idx_sessions_item ON play_sessions(user_id, game_id, played_on DESC);
		     CREATE INDEX idx_sessions_playthrough ON play_sessions(playthrough_id);

		     -- Undo: the rows a write replaced, restorable for a short while.
		     -- AUTOINCREMENT above keeps restored journal IDs from colliding.
		     CREATE TABLE undo_log (
		       id TEXT PRIMARY KEY,
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       snapshot_json TEXT NOT NULL,
		       created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		     );
		     CREATE INDEX idx_undo_log_user ON undo_log(user_id, created_at);

		     -- Background job schedule (internal/jobs): when each job next
		     -- runs survives restarts, so a daily job stays daily.
		     CREATE TABLE jobs (
		       name TEXT PRIMARY KEY,
		       next_run_at TEXT NOT NULL,
		       last_started_at TEXT,
		       last_finished_at TEXT,
		       last_duration_ms INTEGER NOT NULL DEFAULT 0,
		       last_error TEXT NOT NULL DEFAULT '',
		       runs INTEGER NOT NULL DEFAULT 0,
		       failures INTEGER NOT NULL DEFAULT 0
		     );

		     -- Backfill: one playthrough per item with dates, one legacy session
		     -- per item with time, so totals are unchanged.
		     INSERT INTO playthroughs (user_id, game_id, platform, started_on, finished_on, completion, rating)
		       SELECT user_id, game_id, platform, substr(started_at, 1, 10),
		              CASE WHEN status IN ('completed', 'abandoned') THEN substr(completed_at, 1, 10) END,
		              CASE WHEN status = 'abandoned' THEN 'dropped' END,
		              NULLIF(rating, 0)
		       FROM library_items
		       WHERE started_at IS NOT NULL OR completed_at IS NOT NULL;
		     UPDATE library_items SET current_playthrough_id = (
		       SELECT p.id FROM playthroughs p WHERE p.user_id = library_items.user_id AND p.game_id = library_items.game_id);
		     INSERT INTO play_sessions (user_id, game_id, playthrough_id, played_on, minutes, source)
		       SELECT user_id, game_id, current_playthrough_id,
		              substr(COALESCE(completed_at, started_at, updated_at, created_at), 1, 10), playtime_minutes, 'legacy'
		       FROM library_items WHERE playtime_minutes > 0;
		     UPDATE playthroughs SET minutes = (
		       SELECT COALESCE(SUM(s.minutes), 0) FROM play_sessions s WHERE s.playthrough_id = playthroughs.id);

		     -- library_items triggers went with the old table.
		     CREATE TRIGGER library_tags_ai AFTER INSERT ON library_items BEGIN
		       INSERT OR IGNORE INTO library_tags (user_id, game_id, tag)
		         SELECT new.user_id, new.game_id, CAST(j.value AS TEXT)
		           FROM json_each(CASE WHEN json_valid(new.tags_json) THEN new.tags_json ELSE '[]' END) j
		          WHERE j.type != 'null';
		     END;
		     CREATE TRIGGER library_tags_au AFTER UPDATE OF user_id, game_id, tags_json ON library_items BEGIN
		       DELETE FROM library_tags WHERE user_id = old.user_id AND game_id = old.game_id;
		       INSERT OR IGNORE INTO library_tags (user_id, game_id, tag)
		         SELECT new.user_id, new.game_id, CAST(j.value AS TEXT)
		           FROM json_each(CASE WHEN json_valid(new.tags_json) THEN new.tags_json ELSE '[]' END) j
		          WHERE j.type != 'null';
		     END;
		     CREATE TRIGGER library_tags_ad AFTER DELETE ON library_items BEGIN
		       DELETE FROM library_tags WHERE user_id = old.user_id AND game_id = old.game_id;
		     END;

		     -- library_rev: every change to a library row (including the
		     -- playtime aggregate kept by the session triggers) bumps the
		     -- owner's counter and stamps the row. The WHEN guard skips the
		     -- stamping UPDATE itself. No OR IGNORE/OR REPLACE here: an outer
		     -- UPSERT's conflict handling overrides a trigger's.
		     CREATE TRIGGER library_rev_ai AFTER INSERT ON library_items BEGIN
		       INSERT INTO user_state (user_id) SELECT new.user_id
		         WHERE NOT EXISTS (SELECT 1 FROM user_state WHERE user_id = new.user_id);
		       UPDATE user_state SET library_rev = library_rev + 1 WHERE user_id = new.user_id;
		       UPDATE library_items SET rev = (SELECT library_rev FROM user_state WHERE user_id = new.user_id)
		         WHERE user_id = new.user_id AND game_id = new.game_id;
		       DELETE FROM library_tombstones WHERE user_id = new.user_id AND game_id = new.game_id;
		     END;
		     CREATE TRIGGER library_rev_au AFTER UPDATE ON library_items WHEN old.rev IS new.rev BEGIN
		       INSERT INTO user_state (user_id) SELECT new.user_id
		         WHERE NOT EXISTS (SELECT 1 FROM user_state WHERE user_id = new.user_id);
		       UPDATE user_state SET library_rev = library_rev + 1 WHERE user_id = new.user_id;
		       UPDATE library_items SET rev = (SELECT library_rev FROM user_state WHERE user_id = new.user_id)
		         WHERE user_id = new.user_id AND game_id = new.game_id;
		     END;
		     CREATE TRIGGER library_rev_ad AFTER DELETE ON library_items BEGIN
		       UPDATE user_state SET library_rev = library_rev + 1 WHERE user_id = old.user_id;
		       DELETE FROM library_tombstones WHERE user_id = old.user_id AND game_id = old.game_id;
		       INSERT INTO library_tombstones (user_id, game_id, rev)
		         SELECT old.user_id, old.game_id, library_rev FROM user_state WHERE user_id = old.user_id;
		     END;
		     CREATE TRIGGER users_ad_tombstones AFTER DELETE ON users BEGIN
		       DELETE FROM library_tombstones WHERE user_id = old.id;
		     END;
		     -- Index entries carry the game's name, cover and release date.
		     CREATE TRIGGER games_library_rev_au AFTER UPDATE OF name, cover_url, local_cover_path, first_release_date ON games
		       WHEN old.name IS NOT new.name OR old.cover_url IS NOT new.cover_url
		         OR old.local_cover_path IS NOT new.local_cover_path OR old.first_release_date IS NOT new.first_release_date BEGIN
		       UPDATE library_items SET rev = rev WHERE game_id = new.id;
		     END;

		     CREATE TRIGGER play_sessions_ai AFTER INSERT ON play_sessions BEGIN
		       UPDATE library_items SET playtime_minutes = MAX(0, playtime_minutes + new.minutes),
		              updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		        WHERE user_id = new.user_id AND game_id = new.game_id;
		       UPDATE playthroughs SET minutes = MAX(0, minutes + new.minutes) WHERE id = new.playthrough_id;
		     END;
		     CREATE TRIGGER play_sessions_ad AFTER DELETE ON play_sessions BEGIN
		       UPDATE library_items SET playtime_minutes = MAX(0, playtime_minutes - old.minutes)
		        WHERE user_id = old.user_id AND game_id = old.game_id;
		       UPDATE playthroughs SET minutes = MAX(0, minutes - old.minutes) WHERE id = old.playthrough_id;
		     END;
		     CREATE TRIGGER play_sessions_au AFTER UPDATE OF minutes, playthrough_id ON play_sessions BEGIN
		       UPDATE library_items SET playtime_minutes = MAX(0, playtime_minutes - old.minutes + new.minutes)
		        WHERE user_id = new.user_id AND game_id = new.game_id;
		       UPDATE playthroughs SET minutes = MAX(0, minutes - old.minutes) WHERE id = old.playthrough_id;
		       UPDATE playthroughs SET minutes = minutes + new.minutes WHERE id = new.playthrough_id;
		     END;`,
	},
	{
		Version: 17,
		// Performance (Cato Smooth plan §2.3):
		// - F2: library rows by game, for the cover worker's join and the
		//   ON DELETE CASCADE from games.
		// - F6: the default library order (updated, newest first) and its
		//   keyset cursor (updated_at, game_id) without a temp B-tree.
		// - F3: re-index FTS only when normalized_name changes, and rebuild
		//   game_platforms only when platforms_json actually changes (cover,
		//   popularity and refresh-marker updates used to rewrite both).
		// - F5: suggestions walk this partial index in ORDER BY order and stop
		//   at the limit instead of sorting the whole catalog.
		// - F8: two-character typeahead matches alias prefixes by index.
		Up: `CREATE INDEX IF NOT EXISTS idx_library_game ON library_items(game_id);
		     CREATE INDEX IF NOT EXISTS idx_library_user_updated
		       ON library_items(user_id, updated_at DESC, game_id DESC);
		     DROP TRIGGER IF EXISTS games_fts_au;
		     CREATE TRIGGER games_fts_au AFTER UPDATE OF normalized_name ON games
		       WHEN old.normalized_name IS NOT new.normalized_name BEGIN
		       INSERT INTO games_fts(games_fts, rowid, normalized_name) VALUES ('delete', old.rowid, old.normalized_name);
		       INSERT INTO games_fts(rowid, normalized_name) VALUES (new.rowid, new.normalized_name);
		     END;
		     DROP TRIGGER IF EXISTS game_platforms_au;
		     CREATE TRIGGER game_platforms_au AFTER UPDATE OF platforms_json ON games
		       WHEN old.platforms_json IS NOT new.platforms_json BEGIN
		       DELETE FROM game_platforms WHERE game_id = old.id;
		       INSERT OR IGNORE INTO game_platforms (game_id, platform_id, platform_value)
		         SELECT new.id,
		                CASE WHEN je.type = 'integer'
		                           OR (je.type = 'text' AND je.value != '' AND je.value NOT GLOB '*[^0-9]*')
		                     THEN CAST(je.value AS INTEGER) ELSE 0 END,
		                CASE WHEN je.type = 'integer'
		                           OR (je.type = 'text' AND je.value != '' AND je.value NOT GLOB '*[^0-9]*')
		                     THEN '' ELSE CAST(je.value AS TEXT) END
		           FROM json_each(CASE WHEN json_valid(new.platforms_json) THEN new.platforms_json ELSE '[]' END) je
		          WHERE je.type != 'null';
		     END;
		     CREATE INDEX IF NOT EXISTS idx_games_suggest
		       ON games(popularity_score DESC, rating_count DESC, name) WHERE local_cover_path != '';
		     CREATE INDEX IF NOT EXISTS idx_game_aliases_alias ON game_aliases(normalized_alias);`,
	},
	{
		Version: 16,
		// Case-insensitive tag indexes. Tag filtering and autocomplete are now
		// LOWER(tag) based (RPG = rpg). The v15 indexes on (user_id, tag)
		// can't be used for LOWER(tag) predicates (full scan). Add expression
		// indexes so WHERE LOWER(tag) IN / LIKE and GROUP BY LOWER(tag) stay
		// indexed. Also add a case-insensitive unique index so (user_id,
		// game_id, LOWER(tag)) can't have both "RPG" and "rpg" on the same
		// game — INSERT OR IGNORE in the v15 triggers will then dedupe
		// case variants automatically.
		Up: `CREATE INDEX IF NOT EXISTS idx_library_tags_user_ltag
		       ON library_tags(user_id, LOWER(tag));
		     CREATE INDEX IF NOT EXISTS idx_library_tags_user_ltag_game
		       ON library_tags(user_id, LOWER(tag), game_id);
		     CREATE INDEX IF NOT EXISTS idx_library_tags_game_user_ltag
		       ON library_tags(game_id, user_id, LOWER(tag));
		     CREATE UNIQUE INDEX IF NOT EXISTS idx_library_tags_user_game_ltag
		       ON library_tags(user_id, game_id, LOWER(tag));`,
	},
	{
		Version: 15,
		// Search/filter indexes for JSON-backed platform and tag fields. The JSON
		// columns remain the API/source-of-truth format for compatibility, while
		// these tables make repeated filter predicates indexed and keep raw SQL
		// import/test writes synchronized through triggers.
		Up: `CREATE TABLE IF NOT EXISTS game_platforms (
		     game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		     platform_id INTEGER NOT NULL DEFAULT 0,
		     platform_value TEXT NOT NULL DEFAULT '',
		     PRIMARY KEY (game_id, platform_id, platform_value)
		     );
		     CREATE INDEX IF NOT EXISTS idx_game_platforms_platform_game
		       ON game_platforms(platform_id, game_id);
			INSERT OR IGNORE INTO game_platforms (game_id, platform_id, platform_value)
			  SELECT g.id,
			         CASE WHEN je.type = 'integer'
			                    OR (je.type = 'text' AND je.value != '' AND je.value NOT GLOB '*[^0-9]*')
			              THEN CAST(je.value AS INTEGER) ELSE 0 END,
			         CASE WHEN je.type = 'integer'
			                    OR (je.type = 'text' AND je.value != '' AND je.value NOT GLOB '*[^0-9]*')
			              THEN '' ELSE CAST(je.value AS TEXT) END
		       FROM games g,
	            json_each(CASE WHEN json_valid(g.platforms_json) THEN g.platforms_json ELSE '[]' END) je
		       WHERE je.type != 'null';

		       CREATE TRIGGER IF NOT EXISTS game_platforms_ai AFTER INSERT ON games BEGIN
		       INSERT OR IGNORE INTO game_platforms (game_id, platform_id, platform_value)
		         SELECT new.id,
		                CASE WHEN je.type = 'integer'
		                           OR (je.type = 'text' AND je.value != '' AND je.value NOT GLOB '*[^0-9]*')
		                     THEN CAST(je.value AS INTEGER) ELSE 0 END,
		                CASE WHEN je.type = 'integer'
		                           OR (je.type = 'text' AND je.value != '' AND je.value NOT GLOB '*[^0-9]*')
		                     THEN '' ELSE CAST(je.value AS TEXT) END
		           FROM json_each(CASE WHEN json_valid(new.platforms_json) THEN new.platforms_json ELSE '[]' END) je
		          WHERE je.type != 'null';
		     END;
		       CREATE TRIGGER IF NOT EXISTS game_platforms_au AFTER UPDATE OF platforms_json ON games BEGIN
		       DELETE FROM game_platforms WHERE game_id = old.id;
		       INSERT OR IGNORE INTO game_platforms (game_id, platform_id, platform_value)
		         SELECT new.id,
		                CASE WHEN je.type = 'integer'
		                           OR (je.type = 'text' AND je.value != '' AND je.value NOT GLOB '*[^0-9]*')
		                     THEN CAST(je.value AS INTEGER) ELSE 0 END,
		                CASE WHEN je.type = 'integer'
		                           OR (je.type = 'text' AND je.value != '' AND je.value NOT GLOB '*[^0-9]*')
		                     THEN '' ELSE CAST(je.value AS TEXT) END
		           FROM json_each(CASE WHEN json_valid(new.platforms_json) THEN new.platforms_json ELSE '[]' END) je
		          WHERE je.type != 'null';
		     END;

		     CREATE TABLE IF NOT EXISTS library_tags (
		       user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		       game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		       tag TEXT NOT NULL,
		       PRIMARY KEY (user_id, game_id, tag)
		       );
		     CREATE INDEX IF NOT EXISTS idx_library_tags_user_tag_game
		       ON library_tags(user_id, tag, game_id);
		     CREATE INDEX IF NOT EXISTS idx_library_tags_game_user
		       ON library_tags(game_id, user_id, tag);
		     INSERT OR IGNORE INTO library_tags (user_id, game_id, tag)
		       SELECT li.user_id, li.game_id, CAST(j.value AS TEXT)
		       FROM library_items li,
	            json_each(CASE WHEN json_valid(li.tags_json) THEN li.tags_json ELSE '[]' END) j
		       WHERE j.type != 'null'
		       GROUP BY li.user_id, li.game_id, CAST(j.value AS TEXT);

		     CREATE TRIGGER IF NOT EXISTS library_tags_ai AFTER INSERT ON library_items BEGIN
		       INSERT OR IGNORE INTO library_tags (user_id, game_id, tag)
		         SELECT new.user_id, new.game_id, CAST(j.value AS TEXT)
		           FROM json_each(CASE WHEN json_valid(new.tags_json) THEN new.tags_json ELSE '[]' END) j
		          WHERE j.type != 'null';
		     END;
		     CREATE TRIGGER IF NOT EXISTS library_tags_au AFTER UPDATE OF user_id, game_id, tags_json ON library_items BEGIN
		       DELETE FROM library_tags WHERE user_id = old.user_id AND game_id = old.game_id;
		       INSERT OR IGNORE INTO library_tags (user_id, game_id, tag)
		         SELECT new.user_id, new.game_id, CAST(j.value AS TEXT)
		           FROM json_each(CASE WHEN json_valid(new.tags_json) THEN new.tags_json ELSE '[]' END) j
		          WHERE j.type != 'null';
		     END;
		     CREATE TRIGGER IF NOT EXISTS library_tags_ad AFTER DELETE ON library_items BEGIN
		       DELETE FROM library_tags WHERE user_id = old.user_id AND game_id = old.game_id;
		     END;`,
	},
	{
		Version: 14,
		// Seed curated shortname platforms so the platform filter works even
		// before the IGDB sync runs or when IGDB is not configured. The
		// shortnames (ps5, xsx, sw2, win, …) are gamer-style codes that
		// IGDB abbreviations don't cover. INSERT OR IGNORE keeps existing
		// IGDB rows intact; the UPDATE then stamps the shortnames.
		Up: `INSERT OR IGNORE INTO platforms (id, name, abbreviation) VALUES
		     (6, 'PC (Microsoft Windows)', 'PC'),
		     (7, 'PlayStation', 'PS'),
		     (8, 'PlayStation 2', 'PS2'),
		     (9, 'PlayStation 3', 'PS3'),
		     (12, 'Xbox 360', 'X360'),
		     (38, 'PlayStation Portable', 'PSP'),
		     (46, 'PlayStation Vita', 'PS Vita'),
		     (48, 'PlayStation 4', 'PS4'),
		     (49, 'Xbox One', 'XONE'),
		     (130, 'Nintendo Switch', 'Switch'),
		     (167, 'PlayStation 5', 'PS5'),
		     (169, 'Xbox Series X|S', 'XSX'),
		     (508, 'Nintendo Switch 2', 'Switch 2');
		     UPDATE platforms SET shortname = CASE id
		       WHEN 6 THEN 'win'
		       WHEN 7 THEN 'ps1 psx'
		       WHEN 8 THEN 'ps2'
		       WHEN 9 THEN 'ps3'
		       WHEN 12 THEN 'x360 360'
		       WHEN 38 THEN 'psp'
		       WHEN 46 THEN 'psvita vita psv'
		       WHEN 48 THEN 'ps4'
		       WHEN 49 THEN 'xb1 xone'
		       WHEN 130 THEN 'ns swi switch1'
		       WHEN 167 THEN 'ps5'
		       WHEN 169 THEN 'xsx xss seriesx'
		       WHEN 508 THEN 'sw2 ns2 switch2'
		       ELSE shortname END
		     WHERE id IN (6,7,8,9,12,38,46,48,49,130,167,169,508);`,
	},
	{
		Version: 13,
		// Pack/skin hiding: parent_game links DLC/packs to their main game,
		// and category (now synced from IGDB game_type) distinguishes packs
		// (13) from true DLC (1)/expansions (2). Search hides packs/mods
		// etc by default alongside version_parent editions. Backfill corrects
		// stale category=0 rows (import never set it).
		Up: `ALTER TABLE games ADD COLUMN parent_game INTEGER NOT NULL DEFAULT 0;
		     CREATE INDEX IF NOT EXISTS idx_games_parent_game ON games(parent_game);
		     CREATE INDEX IF NOT EXISTS idx_games_category ON games(category);`,
	},
	{
		Version: 12,
		// Edition hiding: version_parent marks IGDB edition/version rows
		// (IGDB "Deluxe Edition", "GOTY", etc. where version_parent != 0).
		// Search hides these by default (WHERE version_parent = 0) and the
		// backfill populates legacy rows that were imported with 0. The
		// marker column makes the backfill resumable; the index makes the
		// hide filter an index scan.
		Up: `ALTER TABLE games ADD COLUMN version_parent_fetched_at INTEGER NOT NULL DEFAULT 0;
		     CREATE INDEX IF NOT EXISTS idx_games_version_parent ON games(version_parent);
		     CREATE INDEX IF NOT EXISTS idx_games_version_parent_fetched ON games(version_parent_fetched_at) WHERE version_parent_fetched_at = 0;`,
	},
	{
		Version: 11,
		// Multi-platform ownership: a game can be owned on several consoles,
		// so ownership becomes an array (same pattern as tags_json). The old
		// singular platform column stays in sync with the FIRST entry for
		// compatibility (CSV consumers, stale clients); new reads use
		// owned_platforms_json. Existing non-empty values are folded in.
		Up: `ALTER TABLE library_items ADD COLUMN owned_platforms_json TEXT NOT NULL DEFAULT '[]';
		     UPDATE library_items SET owned_platforms_json = json_array(platform)
		       WHERE platform != '' AND owned_platforms_json = '[]';`,
	},
	{
		Version: 10,
		// Curated gamer-style platform codes ("sw2", "xsx", "ps5") that
		// IGDB's own abbreviations don't cover (upstream calls Switch 2
		// "Switch 2" and Xbox Series X|S "Series X|S"). Populated by
		// Store.ApplyPlatformShortnames at startup; matched by the @platform
		// search filter and autocomplete alongside name/abbreviation.
		Up: `ALTER TABLE platforms ADD COLUMN shortname TEXT NOT NULL DEFAULT '';`,
	},
	{
		Version: 9,
		// IGDB platform ID → name lookup. games.platforms_json stores raw
		// IGDB platform IDs ([6,130,...]); API responses resolve them to
		// human names via this table. Populated once from IGDB's /platforms
		// endpoint (~230 rows) by Service.SyncPlatforms at startup when a
		// real client is configured.
		Up: `CREATE TABLE IF NOT EXISTS platforms (
		     id INTEGER PRIMARY KEY,
		     name TEXT NOT NULL,
		     abbreviation TEXT NOT NULL DEFAULT ''
		     );`,
	},
	{
		Version: 8,
		// Completion marker for alias backfill. 0 = this row's alias set has
		// never been fetched from IGDB; non-zero (unix seconds) = fetched,
		// even when upstream had no aliases — otherwise zero-alias games would
		// be re-fetched forever. Set by UpsertIGDBGame (whose igdbFields
		// always include alternative_names.name) and by the resumable
		// backfill-aliases subcommand.
		Up: `ALTER TABLE games ADD COLUMN aliases_fetched_at INTEGER NOT NULL DEFAULT 0;`,
	},
	{
		Version: 7,
		// Game aliases for search. Populated from IGDB alternative_names by
		// the IGDB refresh paths; lets short abbreviations ("botw") and
		// localized titles find the main game. aliases_fts is a standalone
		// (content-managed) trigram table kept in step by triggers on
		// game_aliases. Alias rows always reference an existing game, so ON
		// DELETE CASCADE keeps both tables consistent.
		Up: `CREATE TABLE IF NOT EXISTS game_aliases (
		     game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
		     normalized_alias TEXT NOT NULL,
		     PRIMARY KEY (game_id, normalized_alias)
		     );
		     CREATE INDEX IF NOT EXISTS idx_game_aliases_game ON game_aliases(game_id);

		     CREATE VIRTUAL TABLE IF NOT EXISTS aliases_fts USING fts5(
		       normalized_alias,
		       tokenize='trigram'
		     );

		     CREATE TRIGGER IF NOT EXISTS game_aliases_ai AFTER INSERT ON game_aliases BEGIN
		       INSERT INTO aliases_fts(rowid, normalized_alias) VALUES (new.rowid, new.normalized_alias);
		     END;
		     CREATE TRIGGER IF NOT EXISTS game_aliases_ad AFTER DELETE ON game_aliases BEGIN
		       DELETE FROM aliases_fts WHERE rowid = old.rowid;
		     END;
		     CREATE TRIGGER IF NOT EXISTS game_aliases_au AFTER UPDATE OF normalized_alias ON game_aliases BEGIN
		       DELETE FROM aliases_fts WHERE rowid = old.rowid;
		       INSERT INTO aliases_fts(rowid, normalized_alias) VALUES (new.rowid, new.normalized_alias);
		     END;`,
	},
	{
		Version: 6,
		// Ownership modeling: which platform a game is owned on and whether
		// the copy is physical or digital. Empty string means "not set" —
		// existing rows keep working unchanged.
		Up: `ALTER TABLE library_items ADD COLUMN platform TEXT NOT NULL DEFAULT '';
		     ALTER TABLE library_items ADD COLUMN medium TEXT NOT NULL DEFAULT '';`,
	},
	{
		Version: 5,
		// FTS5 trigram virtual table over normalized_name for typo-tolerant
		// search. Trigram tokenizer matches on shared 3-character substrings,
		// so a query like "zleda" still matches "zelda". External-content
		// mode (content='games') avoids duplicating the text column, but
		// FTS5 does not auto-sync external content tables — the three
		// triggers below keep the index in step with INSERT/UPDATE/DELETE
		// on games. This covers every write path including the raw COPY
		// import in internal/importer, which bypasses the Go store layer.
		Up: `CREATE VIRTUAL TABLE IF NOT EXISTS games_fts USING fts5(
		     normalized_name,
		     tokenize='trigram',
		     content='games',
		     content_rowid='rowid'
		     );
		     INSERT INTO games_fts(rowid, normalized_name)
		       SELECT rowid, normalized_name FROM games;

		     CREATE TRIGGER IF NOT EXISTS games_fts_ai AFTER INSERT ON games BEGIN
		       INSERT INTO games_fts(rowid, normalized_name) VALUES (new.rowid, new.normalized_name);
		     END;
		     CREATE TRIGGER IF NOT EXISTS games_fts_ad AFTER DELETE ON games BEGIN
		       INSERT INTO games_fts(games_fts, rowid, normalized_name) VALUES ('delete', old.rowid, old.normalized_name);
		     END;
		     CREATE TRIGGER IF NOT EXISTS games_fts_au AFTER UPDATE ON games BEGIN
		       INSERT INTO games_fts(games_fts, rowid, normalized_name) VALUES ('delete', old.rowid, old.normalized_name);
		       INSERT INTO games_fts(rowid, normalized_name) VALUES (new.rowid, new.normalized_name);
		     END;`,
	},
	{
		Version: 4,
		// Popularity fields fetched from IGDB and a denormalized
		// popularity_score computed at upsert time from
		// follows*3 + hypes*2 + total_rating_count + main-game-released bonus.
		// The score drives search ranking (store.go SearchLocal) and is
		// indexed so the ORDER BY stays an O(limit) index scan.
		Up: `ALTER TABLE games ADD COLUMN rating REAL NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN rating_count INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN total_rating REAL NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN total_rating_count INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN follows INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN hypes INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN igdb_popularity REAL NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN category INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN status INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN version_parent INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN popularity_score INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN popularity_fetched_at INTEGER NOT NULL DEFAULT 0;
		     CREATE INDEX IF NOT EXISTS idx_games_popularity ON games(popularity_score DESC);
		     CREATE INDEX IF NOT EXISTS idx_games_pop_fetch ON games(popularity_fetched_at) WHERE popularity_fetched_at = 0;`,
	},
	{
		Version: 3,
		// GetStaleGames used a correlated subquery in its ORDER BY which forced
		// a full-table sort of the games table.  Adding an index on
		// source_updated_at lets SQLite satisfy the WHERE + ORDER BY in one
		// O(LIMIT) index scan with no sort step.
		Up: `CREATE INDEX IF NOT EXISTS idx_games_source_updated
		     ON games(source_updated_at);`,
	},
	{
		Version: 2,
		// cover_jobs had no index on next_attempt_at, causing the coordinator
		// goroutine to do a full table scan (potentially 50k+ rows) on every
		// poll iteration.  With SetMaxOpenConns(1) that single slow query
		// blocked every concurrent HTTP handler waiting for the DB connection.
		Up: `CREATE INDEX IF NOT EXISTS idx_cover_jobs_next_attempt
		     ON cover_jobs(next_attempt_at, attempts);`,
	},
	{
		Version: 1,
		Up: `PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT,
  display_name TEXT NOT NULL DEFAULT '',
  avatar_url TEXT NOT NULL DEFAULT '',
  google_subject TEXT UNIQUE,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS games (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL,
  safe_name TEXT NOT NULL DEFAULT '',
  normalized_name TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  storyline TEXT NOT NULL DEFAULT '',
  cover_id INTEGER NOT NULL DEFAULT 0,
  cover_url TEXT NOT NULL DEFAULT '',
  local_cover_path TEXT NOT NULL DEFAULT '',
  first_release_date INTEGER NOT NULL DEFAULT 0,
  aggregated_rating INTEGER NOT NULL DEFAULT 0,
  aggregated_rating_count INTEGER NOT NULL DEFAULT 0,
  platforms_json TEXT NOT NULL DEFAULT '[]',
  genres_json TEXT NOT NULL DEFAULT '[]',
  trailer TEXT NOT NULL DEFAULT '',
  igdb_url TEXT NOT NULL DEFAULT '',
  source_updated_at INTEGER NOT NULL DEFAULT 0,
  imported_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS library_items (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
  status TEXT NOT NULL CHECK (status IN ('wishlist', 'backlog', 'playing', 'completed', 'abandoned')),
  rating INTEGER NOT NULL DEFAULT 0 CHECK (rating >= 0 AND rating <= 100),
  playtime_minutes INTEGER NOT NULL DEFAULT 0 CHECK (playtime_minutes >= 0),
  tags_json TEXT NOT NULL DEFAULT '[]',
  notes TEXT NOT NULL DEFAULT '',
  started_at TEXT,
  completed_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (user_id, game_id)
);

CREATE TABLE IF NOT EXISTS igdb_query_cache (
  normalized_query TEXT PRIMARY KEY,
  response_json TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS igdb_sync_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS cover_jobs (
  game_id INTEGER PRIMARY KEY REFERENCES games(id) ON DELETE CASCADE,
  source_url TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_games_normalized_name ON games(normalized_name);
CREATE INDEX IF NOT EXISTS idx_games_rating_release ON games(aggregated_rating_count, aggregated_rating, first_release_date);
CREATE INDEX IF NOT EXISTS idx_games_slug ON games(slug);
CREATE INDEX IF NOT EXISTS idx_library_user_status ON library_items(user_id, status);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);`,
	},
}

// Migrate runs pending migrations on the writer connection. All DDL/writes go
// through the single writer pool to avoid contending with the read pool.
func Migrate(database *DB) error {
	return migrateTo(database, int(^uint(0)>>1))
}

// LatestVersion is the highest schema version this binary knows.
func LatestVersion() int {
	latest := 0
	for _, m := range migrations {
		latest = max(latest, m.Version)
	}
	return latest
}

// SchemaVersion reports the applied schema version (0 for a new database).
func SchemaVersion(database *DB) (int, error) {
	if err := createMigrationsTable(database.Write); err != nil {
		return 0, err
	}
	return getCurrentVersion(database.Write)
}

// MigrateTo applies pending migrations up to and including version target.
// Tests use it to build a database at an older schema.
func MigrateTo(database *DB, target int) error { return migrateTo(database, target) }

func migrateTo(database *DB, target int) error {
	w := database.Write

	if err := createMigrationsTable(w); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	currentVersion, err := getCurrentVersion(w)
	if err != nil {
		return fmt.Errorf("get current version: %w", err)
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	for _, m := range migrations {
		if m.Version <= currentVersion || m.Version > target {
			continue
		}

		if err := applyMigration(w, m); err != nil {
			return fmt.Errorf("apply migration %d: %w", m.Version, err)
		}
	}

	return nil
}

func createMigrationsTable(database *sql.DB) error {
	_, err := database.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	return err
}

func getCurrentVersion(database *sql.DB) (int, error) {
	var version int
	err := database.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	if err != nil {
		return 0, err
	}
	return version, nil
}

func applyMigration(database *sql.DB, m Migration) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(m.Up); err != nil {
		return fmt.Errorf("exec migration SQL: %w", err)
	}

	if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.Version); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	return tx.Commit()
}
