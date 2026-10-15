# Deployment

**Never deploy, push, or create or push a release tag unless the person explicitly asks for that specific action.** Approval for one deploy doesn't carry over to the next.

## Where production runs

- **Docker on a Synology NAS:** host alias `nas2`, directory `/volume1/Shared/Cato`, served at `http://10.0.0.42:7080`.
- **It's plain HTTP**, a legacy convenience, so it needs `CATO_ALLOW_INSECURE=1` in its `.env` to start. On plain HTTP, browsers turn off the service worker, offline use, push, passkeys and the share target. A real domain or HTTPS proxy turns them on.
- **Data:** the container `cato` bind-mounts `data/` → `/app/data` (the database, covers, avatars and `backups/`).
- **Tools on nas2:** `/usr/local/bin/docker`, `/usr/local/bin/docker-compose` and `/usr/bin/sqlite3` are available without sudo.
- **Don't read or change files on nas2** unless asked.

## Deploy paths

```bash
make deploy-nas    # build the Linux binary (go.mod's toolchain), copy binary/Dockerfile/compose/static, recreate the container
make deploy-logs   # tail the container logs
DRY_RUN=1 make deploy  # preview the next release tag (no push)
make deploy        # tagged release: pushes an annotated v* tag → CI builds, scans, signs, verifies (cosign) and deploys
make deploy-full   # disabled: data replacement follows the restore runbook
```

- **`deploy-nas`** replaces the NAS `docker-compose.yml` with the repo's, which passes every setting through from `.env`. Before it, take a verified `.backup` of the database (commands below).
  - Its tar runs with `COPYFILE_DISABLE=1` and excludes `._*` and `.DS_Store`: macOS metadata files otherwise land in `web/static`. The asset versioner ignores dotfiles as a second guard.
- **Tagged releases** must be reachable from `main`. Normal pushes to `main` and PRs never publish or deploy. Image, SSH and Cloudflare settings: `docs/production-runbook.md`.
- **Schema upgrades run automatically at startup**, after a `VACUUM INTO` snapshot in `data/backups/`. For a release with new migrations, rehearse on a copy of production first. `docs/nas-upgrade.md`, if present, is the worked procedure.
- **Assets are versioned automatically.** An open client shows "A new version of Cato is ready" with a Reload button.

## Backups and restore

```sh
ssh nas2 'cd /volume1/Shared/Cato && mkdir -p backup && sqlite3 data/cato.db ".timeout 30000" ".backup backup/pre-<what>.db"'
```

- Never copy a live WAL database file.
- The stopped-service restore, quarantine and rollback procedures are in `docs/production-runbook.md`.
- An older binary can't run on an upgraded schema: roll back by restoring the database and the release bundle together.
