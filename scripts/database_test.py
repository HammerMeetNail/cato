import contextlib
import importlib.util
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("database", Path(__file__).with_name("database.py"))
database = importlib.util.module_from_spec(spec)
spec.loader.exec_module(database)


class DatabaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.db = self.root / "live.db"
        self.backups = self.root / "backups"
        self.writer = sqlite3.connect(self.db)
        self.addCleanup(self.writer.close)
        self.writer.executescript("""
            PRAGMA journal_mode=WAL;
            CREATE TABLE users(id TEXT PRIMARY KEY);
            CREATE TABLE sessions(user_id TEXT REFERENCES users(id));
            CREATE TABLE games(id INTEGER PRIMARY KEY);
            CREATE TABLE library_items(game_id INTEGER REFERENCES games(id));
            INSERT INTO users VALUES ('saved');
        """)

    def users(self, path):
        with contextlib.closing(database.open_source(path)) as conn:
            return conn.execute("SELECT id FROM users ORDER BY id").fetchall()

    def test_live_wal_backup_and_restore_preserves_previous_data(self):
        saved = database.backup(self.db, self.backups)
        self.assertEqual(saved.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.users(saved), [("saved",)])
        self.writer.execute("INSERT INTO users VALUES ('new')")
        self.writer.commit()
        self.writer.close()
        database.restore(self.db, saved, self.backups, True)
        self.assertEqual(self.users(self.db), [("saved",)])
        previous = list(self.backups.glob("pre-restore-*.db"))
        self.assertEqual(len(previous), 1)
        self.assertEqual(self.users(previous[0]), [("new",), ("saved",)])
        self.assertFalse(list(self.backups.glob("restore-source-*")))

    def test_invalid_restore_does_not_modify_destination(self):
        bad = self.root / "corrupt.db"
        bad.write_text("not sqlite")
        with self.assertRaises(sqlite3.DatabaseError):
            database.restore(self.db, bad, self.backups, True)
        self.assertEqual(self.users(self.db), [("saved",)])
        self.assertEqual(list(self.backups.iterdir()), [])

    def test_guards_and_unique_backups(self):
        saved = database.backup(self.db, self.backups)
        self.assertNotEqual(saved, database.backup(self.db, self.backups))
        with self.assertRaisesRegex(ValueError, "service-stopped"):
            database.restore(self.db, saved, self.backups, False)
        with self.assertRaisesRegex(ValueError, "different files"):
            database.restore(self.db, self.db, self.backups, True)
        with self.assertRaises(sqlite3.OperationalError):
            database.backup(self.root / "missing.db", self.backups)
        self.assertFalse((self.root / "missing.db").exists())

    def test_rejects_unrelated_database(self):
        empty = self.root / "empty.db"
        sqlite3.connect(empty).close()
        with self.assertRaisesRegex(ValueError, "not a Cato"):
            database.restore(self.db, empty, self.backups, True)

    def test_restore_with_crash_left_committed_wal(self):
        saved = database.backup(self.db, self.backups)
        self.writer.close()
        subprocess.run([sys.executable, "-c", """
import os, sqlite3, sys
conn = sqlite3.connect(sys.argv[1])
conn.execute('PRAGMA journal_mode=WAL')
conn.execute('PRAGMA wal_autocheckpoint=0')
conn.execute("INSERT INTO users VALUES ('crash-committed')")
conn.commit()
os._exit(0)
""", str(self.db)], check=True)
        self.assertGreater(Path(str(self.db) + "-wal").stat().st_size, 0)
        database.restore(self.db, saved, self.backups, True)
        self.assertEqual(self.users(self.db), [("saved",)])
        previous = next(self.backups.glob("pre-restore-*.db"))
        self.assertEqual(self.users(previous), [("crash-committed",), ("saved",)])

    def test_failed_copy_rolls_back_destination(self):
        # Multiple pages ensure the exception occurs before the copy commits.
        self.writer.execute("CREATE TABLE padding(value BLOB)")
        self.writer.execute("INSERT INTO padding VALUES (zeroblob(100000))")
        self.writer.commit()
        saved = database.backup(self.db, self.backups)
        self.writer.execute("INSERT INTO users VALUES ('keep-me')")
        self.writer.commit()
        self.writer.close()
        original_copy = database.copy_database
        calls = 0

        def fail_restore(source, destination):
            nonlocal calls
            calls += 1
            if calls != 3:
                return original_copy(source, destination)

            def interrupt(status, remaining, total):
                self.assertGreater(remaining, 0)
                raise RuntimeError("injected mid-copy failure")

            source.backup(destination, pages=1, progress=interrupt)

        with mock.patch.object(database, "copy_database", side_effect=fail_restore):
            with self.assertRaisesRegex(RuntimeError, "mid-copy"):
                database.restore(self.db, saved, self.backups, True)
        self.assertEqual(self.users(self.db), [("keep-me",), ("saved",)])
        self.assertEqual(len(list(self.backups.glob("pre-restore-*.db"))), 1)

    def test_backup_publication_failure_prevents_restore(self):
        saved = database.backup(self.db, self.backups)
        self.writer.execute("INSERT INTO users VALUES ('keep-me')")
        self.writer.commit()
        self.writer.close()
        with mock.patch.object(database, "sync_directory", side_effect=OSError("fsync failed")):
            with self.assertRaisesRegex(OSError, "fsync failed"):
                database.restore(self.db, saved, self.backups, True)
        self.assertEqual(self.users(self.db), [("keep-me",), ("saved",)])

    def test_corrupt_destination_requires_preserved_quarantine(self):
        saved = database.backup(self.db, self.backups)
        self.writer.close()
        self.db.write_bytes(b"corrupt database")
        with self.assertRaises(sqlite3.DatabaseError):
            database.restore(self.db, saved, self.backups, True)
        # Exercise the documented stopped-service quarantine recovery procedure.
        quarantine = self.root / "quarantine"
        quarantine.mkdir(mode=0o700)
        for suffix in ("", "-wal", "-shm", "-journal"):
            original = Path(str(self.db) + suffix)
            if original.exists():
                original.rename(quarantine / original.name)
        database.restore(self.db, saved, self.backups, True)
        self.assertEqual(self.users(self.db), [("saved",)])
        self.assertEqual((quarantine / "live.db").read_bytes(), b"corrupt database")


if __name__ == "__main__":
    unittest.main()
