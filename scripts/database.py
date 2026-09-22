#!/usr/bin/env python3
"""Consistent SQLite backups and explicit, offline restores (Python 3.10+)."""

import argparse
from contextlib import closing
from datetime import datetime, timezone
from pathlib import Path
import os
import sqlite3
import sys
import tempfile
import time


def open_source(path):
    return sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True, timeout=5)


def verify(conn):
    result = conn.execute("PRAGMA integrity_check").fetchall()
    if result != [("ok",)]:
        raise ValueError("database integrity check failed")
    if conn.execute("PRAGMA foreign_key_check").fetchone() is not None:
        raise ValueError("database foreign-key check failed")
    # Prevent an unrelated, valid SQLite file from replacing Cato's database.
    tables = {row[0] for row in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    if not {"users", "sessions", "games", "library_items"}.issubset(tables):
        raise ValueError("database is not a Cato database")


def copy_database(source, destination):
    deadline = time.monotonic() + 30

    def progress(status, remaining, total):
        if time.monotonic() > deadline:
            raise TimeoutError("database copy exceeded 30 seconds; check for active writers")

    source.backup(destination, pages=256, progress=progress, sleep=0.05)


def sync_directory(directory):
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def backup(database, directory, prefix="cato"):
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    # mkstemp ensures mode 0600, unique names, and no existing file is clobbered.
    fd, name = tempfile.mkstemp(prefix=f".{prefix}-{stamp}-", suffix=".tmp", dir=directory)
    os.close(fd)
    temporary = Path(name)
    result = temporary.with_name(temporary.name[1:-4] + ".db")
    try:
        with closing(open_source(database)) as source, closing(sqlite3.connect(temporary)) as target:
            copy_database(source, target)
            # Backups are standalone files; do not inherit WAL mode from source.
            target.execute("PRAGMA journal_mode=DELETE")
            verify(target)
        with temporary.open("rb") as file:
            os.fsync(file.fileno())
        os.replace(temporary, result)
        # The recovery filename must survive a crash before restore may proceed.
        sync_directory(directory)
        return result
    finally:
        temporary.unlink(missing_ok=True)


def restore(database, source_path, directory, stopped):
    if not stopped:
        raise ValueError("stop every Cato process using this database, then pass --service-stopped")
    if database.resolve() == source_path.resolve():
        raise ValueError("backup and destination must be different files")
    if database.is_symlink():
        raise ValueError("refusing to restore through a symbolic link")
    # Freeze and validate the supplied backup before touching the destination.
    snapshot = backup(source_path, directory, "restore-source")
    try:
        if database.exists():
            previous = backup(database, directory, "pre-restore")
            print(f"Pre-restore backup: {previous}", flush=True)
        database.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        with closing(open_source(snapshot)) as source, closing(sqlite3.connect(database)) as target:
            # The backup API safely replaces database pages, including WAL state;
            # never copy over a live file or delete its -wal/-shm files.
            copy_database(source, target)
            verify(target)
        os.chmod(database, 0o600)
    finally:
        snapshot.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("backup", "restore"))
    parser.add_argument("--db", type=Path, default=Path("data/cato.db"))
    parser.add_argument("--backup-dir", type=Path, default=Path("backup"))
    parser.add_argument("--file", type=Path)
    parser.add_argument("--service-stopped", action="store_true",
                        help="acknowledge all processes using the destination database are stopped")
    args = parser.parse_args()
    os.umask(0o077)
    try:
        if args.command == "backup":
            print(backup(args.db, args.backup_dir))
        else:
            if args.file is None:
                raise ValueError("restore requires --file")
            restore(args.db, args.file, args.backup_dir, args.service_stopped)
            print(f"Restored: {args.db}")
    except (OSError, sqlite3.Error, ValueError, TimeoutError) as error:
        print(f"database: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
