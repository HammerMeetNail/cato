# Feature domains: people, sign-up, imports, notifications, passkeys, delight

How the bigger features are put together and the rules that keep them safe. The design rationale is in `docs/cato_smooth.md` §§7, 8, 10, and the endpoints are in `docs/api-v1.md`.

## People (`internal/social`, migration v21)

- **One visibility rule.** Who may see someone else's content is decided only by `Service.CanSee` (Go) and `visibleSQL` (a SQL fragment):
  - you always see your own things;
  - a block in either direction, or a disabled owner, hides everything;
  - `private` is you only; `followers` means accepted follows; `instance` is everyone on this server;
  - reviews and lists can override the owner's level, and `inherit` uses the profile's.
- **Every query returning someone else's content must use that rule.** `TestAuthorizationMatrix` checks 96 combinations; extend it when adding a content type.
- **The feed is fan-out on read through `follows`.** `activity` rows are written:
  - by library writes (`library/activity.go`, carrying the write's `undo_id`);
  - by reviews and list additions.

  The feed groups a person's logging, adds and list additions per day.
- **Notifications** go into the inbox (`notifications`); delivery is described below. Social notification kinds are off for push by default.
- **Avatars** are re-encoded to 256 px JPEG (which drops EXIF), stored in `data/avatars`, and served by `/avatars/` to signed-in people only.
- **Admin** endpoints check the role on every request and write `audit_log`. The last admin can't be demoted or disabled. Admins get no read bypass beyond reported text.
- **Text limits:** bio 500, review 10,000, list title 100, list description 2,000, list note 500.

## Sign-up, invites, resets

- **The effective sign-up mode** is the admin's choice (`instance_settings.signup`), else `CATO_SIGNUP`. It defaults to `closed`; `social.EffectiveSignup` decides.
- **Account creation** is one transaction for email and Google: `AuthHandler.createUser` calls `social.CreateUser`.
  - The first account on an empty database is always allowed and becomes the admin.
  - `open` lets anyone in.
  - `invite` needs a valid invite from anyone; `closed` needs one from an admin.
- **Invites** are single-use, last 7 days, are stored as a SHA-256 hash, and make the two people follow each other. Google sign-up carries the invite in the OAuth state cookie.
- **Usernames** come from the email's local part, with `_2`, `_3`… on clashes.
- **Admin reset links** (`/login?reset=…`, 24 hours) go to `POST /api/auth/reset`, which signs the account out everywhere.

## Imports (`internal/imports`)

- **Every source previews first:**
  - CSV, with presets for Grouvee, HowLongToBeat, Infinite Backlog, Cato and generic, plus column mapping;
  - a Cato JSON export;
  - Steam;
  - RetroAchievements.

  Rows are matched in order by IGDB ID, then Steam app ID (`game_external_ids` source 1, asking IGDB `external_games` for unknown IDs), then name plus year. Uncertain matches wait for "Is this right?".
- **`POST /api/v1/import/commit`** calls `library.Store.Import` with these rules:
  - one batch, undoable at any time;
  - idempotent;
  - never overwrites what a person recorded: it only fills gaps or raises play time;
  - never posts to friends' feeds.
- **Steam's daily sync** turns increases in play time into `steam` sessions (`library.LogSynced`).
- **`FuzzParseCSV`** guards the parser.

## Notifications and Web Push

- **`internal/push` implements Web Push with no library:** VAPID (RFC 8292) and aes128gcm (RFC 8291). A test checks it against the RFC 8291 vector.
  - Never add a push library or send unencrypted payloads.
  - VAPID keys are generated on first use and kept in `instance_settings`.
- **`internal/notify` delivers.** The `push.deliver` job pushes the kinds a person enabled (`prefs.notify[kind].push`):
  - a subscription is dropped on 404 or 410, or after five failures;
  - the weekly email digest needs `CATO_SMTP_*`.
- **`internal/alerts`** sends:
  - release-day alerts for wishlist games on your platforms (by UTC day);
  - an optional week-ahead alert;
  - IsThereAnyDeal price drops.

  Each alert is sent once (`alerts_sent`).
- **The service worker** handles `push` and `notificationclick` (same-origin URLs only), and sets the app badge.

## Passkeys and sign-in

- **`internal/webauthn` is minimal and fuzzed** (`FuzzDecodeCBOR`):
  - a CBOR decoder;
  - any attestation, which isn't checked;
  - ES256, EdDSA and RS256 assertions;
  - sign-count clone detection.
- **`webauthn.RPFor`** takes the relying party from `CATO_BASE_URL` or the Host header. It refuses IP addresses, so passkeys don't work on `http://10.0.0.42`.
- **Challenges** are single-use, held in memory, and last 5 minutes.
- **Endpoints:** public sign-in is `/api/auth/passkey/*`; management is `/api/v1/passkeys`.

## Share target, barcodes

- **`GET /share`** is the manifest's `share_target`:
  - a Steam or IGDB link to a known game opens its page;
  - anything else opens the ⊕ Log sheet, searched for the title.

  Pasting a link into the ⊕ search takes the same path, which is the iOS route. Never ship a `javascript:` bookmarklet.
- **Barcodes:** UPCitemdb lookups are cached per code in `upc_games`. The camera uses `BarcodeDetector` where it exists; elsewhere, people type the number.

## Delight (migration v23)

- **Goals** live in the `goals` table, with progress computed on read (`library.Goals`).
- **Year in Review** is one read (`library.YearInReview`). Its server comparison includes only people who aren't private.
- **The share image** is drawn on a canvas in the browser (`features/year.js` `drawCard`). IGDB covers load with `crossOrigin` and fall back to text, so the canvas is never tainted.
- **Mood tags** are suggestions computed from lookups (`catalog.MoodsFor`), and stored only when the person taps one.
