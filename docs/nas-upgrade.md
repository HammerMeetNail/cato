# Upgrading the NAS to the cato-smooth release

There are three stages, each with a way back. Run the commands from the repo (`~/git/cato`) unless a step says it's on the NAS.

## 1. Prepare the code (5 minutes)

**a. Make the NAS build use the pinned, patched Go.** In `Makefile`, change the `deploy-build` recipe to:

```make
deploy-build:
	GOTOOLCHAIN=$(GO_TOOLCHAIN) CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o cato ./cmd/cato
```

(`GO_TOOLCHAIN` is already defined in the Makefile and reads `go1.26.9` from go.mod.) Check that it works:

```sh
make deploy-build && go version cato        # must say go1.26.9
git commit -am "build: NAS binary uses the go.mod toolchain"
```

**b. Put everything on `main`.** `main` is an ancestor of `p6-delight`, so this is a plain fast-forward:

```sh
git checkout main
git merge --ff-only p6-delight
```

## 2. Rehearse on a copy of the production database (about 20 minutes)

**a. Take a consistent copy on the NAS and bring it here.** Taking the copy with `.backup` is safe while Cato is running:

```sh
ssh nas2 'set -eu; cd /volume1/Shared/Cato; mkdir -p backup; umask 077
  sqlite3 data/cato.db ".timeout 30000" ".backup backup/rehearsal.db"
  sqlite3 backup/rehearsal.db "PRAGMA integrity_check;"'      # expect: ok

mkdir -p ~/cato-rehearsal && cd ~/cato-rehearsal
ssh nas2 'cat /volume1/Shared/Cato/backup/rehearsal.db' > prod-copy.db
cp prod-copy.db prod-copy.orig.db                              # untouched spare
```

**b. Record the "before" numbers:**

```sh
sqlite3 prod-copy.db "
  SELECT 'schema',   MAX(version) FROM schema_migrations;
  SELECT 'users',    COUNT(*) FROM users;
  SELECT 'games',    COUNT(*) FROM games;
  SELECT 'items',    COUNT(*) FROM library_items;
  SELECT 'minutes',  SUM(playtime_minutes) FROM library_items;
  SELECT 'status '||status, COUNT(*) FROM library_items GROUP BY status;" | tee before.txt
```

**c. Run the new build against it.** Leave this running in its own terminal and time it:

```sh
cd ~/git/cato && GOTOOLCHAIN=go1.26.9 go build -o ~/cato-rehearsal/cato ./cmd/cato
cd ~/cato-rehearsal
time env CATO_DB_PATH=$PWD/prod-copy.db CATO_COVER_DIR=$PWD/covers \
  CATO_STATIC_DIR=$HOME/git/cato/web/static CATO_DEV_TLS=1 CATO_DEV_TLS_DIR=$PWD/tls \
  CATO_LISTEN_ADDR=127.0.0.1:7443 ./cato
```

In the log, look for `snapshot taken before schema upgrade … to_version=23` and then `cato listening`. Note how long the gap between them was. That's your downtime on the NAS, plus a bit because the NAS is slower.

**d. Check the "after" numbers** (in another terminal; reading the database is fine):

```sh
cd ~/cato-rehearsal
sqlite3 prod-copy.db "
  PRAGMA integrity_check;
  SELECT 'schema',  MAX(version) FROM schema_migrations;              -- 23
  SELECT 'users',   COUNT(*) FROM users;                              -- same as before
  SELECT 'items',   COUNT(*) FROM library_items;                      -- same as before
  SELECT 'minutes', SUM(playtime_minutes) FROM library_items;         -- same as before
  SELECT 'journal', SUM(minutes) FROM play_sessions;                  -- equals 'minutes'
  SELECT 'status '||status, COUNT(*) FROM library_items GROUP BY status;
  SELECT 'admin', email, username FROM users WHERE role='admin';"     -- your oldest account
sqlite3 prod-copy.db "PRAGMA foreign_key_check;"                      # expect no output
```

Users, items, minutes and the status counts should match `before.txt`. "journal" must equal "minutes", because play time is now kept as a journal of sessions.

**e. Look at it in a browser.** Open https://localhost:7443 and accept the certificate warning. Sign in with your real email and password; Google sign-in won't work on this copy, so for a Google-only account rely on the numbers above. Check:

- the Library count;
- a few games you know, for play time, rating, tags and notes;
- Stats and the Diary;
- You → Admin exists for the admin account;
- log +15m and undo it.

Covers will show as placeholders here; that's expected.

**f. Clean up**, since the copy contains everyone's real data. Press Ctrl-C on the server, then:

```sh
rm -rf ~/cato-rehearsal
ssh nas2 'rm /volume1/Shared/Cato/backup/rehearsal.db'
```

**Stop here if anything didn't match**, and look into the numbers or the log before deploying.

## 3. Deploy (a minute or two of downtime)

**a. On the NAS, save a database backup and the current release for rollback:**

```sh
ssh nas2
set -eu; cd /volume1/Shared/Cato; umask 077
sqlite3 data/cato.db ".timeout 30000" ".backup backup/pre-smooth.db"
test "$(sqlite3 backup/pre-smooth.db 'PRAGMA integrity_check;')" = ok && echo backup-ok
tar czf backup/release-before-smooth.tgz cato Dockerfile docker-compose.yml web/static
exit
```

**b. Deploy from the repo, on `main`:**

```sh
make deploy-nas
make deploy-logs      # look for "snapshot taken before schema upgrade" then "cato listening"; Ctrl-C to stop
```

**c. Check it:**

```sh
curl -s http://10.0.0.42:7080/healthz      # {"database":"ok",…,"status":"ok"}
```

Then, in a browser at http://10.0.0.42:7080:

- sign in, and check the Library count matches the rehearsal;
- open a game page;
- log time and undo it;
- check Stats;
- check You → Admin → Sign-up shows the mode you expect. It follows `CATO_SIGNUP` in the NAS `.env` and defaults to closed.

Expect offline use, push notifications, passkeys and install-to-home-screen to be unavailable on plain HTTP; that's normal. Nobody gets signed out.

## If something goes wrong: roll back

The old binary can't use the upgraded database, so restore both. Anything written since the deploy is lost.

```sh
ssh nas2
set -eu; cd /volume1/Shared/Cato
/usr/local/bin/docker stop --time 30 cato
test "$(sqlite3 backup/pre-smooth.db 'PRAGMA integrity_check;')" = ok
sqlite3 data/cato.db ".timeout 30000" ".restore 'backup/pre-smooth.db'"
test "$(sqlite3 data/cato.db 'PRAGMA integrity_check;')" = ok
tar xzf backup/release-before-smooth.tgz
/usr/local/bin/docker-compose -f docker-compose.yml up -d --build
```

Cato also takes its own snapshot before migrating, in `data/backups/cato-pre-v23-…db`, as a second copy.

## Notes

- `sqlite3` is installed on nas2 (`/usr/bin/sqlite3`) and the container is named `cato`. There was no `backup/` folder there before this upgrade, which is why the steps create it.
- `CATO_ALLOW_INSECURE=1` is already in the NAS `.env`; the plain-HTTP instance needs it to start.
- Don't run `make deploy` (the tagged release path): it pushes a tag and starts CI.
