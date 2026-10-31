"""Nightly backup and its verification, end to end on a temporary host
directory: real gzip, gpg and SQLite; podman, rclone and curl are stubs (the
"bucket" is a local directory, an alert email is a recorded request)."""
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import time
import unittest

SCRIPTS = Path(__file__).resolve().parent

STUB = r'''#!/usr/bin/env python3
import json, os, pathlib, shutil, subprocess, sys
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
bucket = pathlib.Path(os.environ['TEST_BUCKET'])
def remote(path):
    assert path.startswith('r2:cato-backups/'), path
    return bucket / path.split('/', 1)[1]
if name == 'podman':
    assert args[0] == 'unshare'
    sys.exit(subprocess.run(args[1:]).returncode)
if name == 'rclone':
    assert os.environ['RCLONE_CONFIG_R2_ENDPOINT'] == 'https://acct.r2.cloudflarestorage.com'
    if args[0] == 'copyto':
        src, dst = args[1], args[2]
        if src.startswith('r2:'):
            shutil.copyfile(remote(src), dst)
        else:
            shutil.copyfile(src, remote(dst))
    elif args[0] == 'lsf':
        assert args[1] == 'r2:cato-backups'
        for p in sorted(bucket.iterdir()):
            print(p.name)
    sys.exit(0)
if name == 'curl':
    with open(os.environ['TEST_MAIL'], 'a') as log:
        log.write(json.dumps({'args': args, 'body': json.loads(sys.stdin.read())}) + '\n')
    sys.exit(0)
raise AssertionError('unexpected stub ' + name)
'''


class BackupTests(unittest.TestCase):
    key = "backup-key-not-in-any-command"
    api_key = "resend-key-not-in-any-command"

    def setUp(self):
        for tool in ("gpg", "gzip"):
            self.assertIsNotNone(shutil.which(tool), f"{tool} is required")
        self.temp = tempfile.TemporaryDirectory(prefix="cato-backup-tests-")
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        self.host = root / "host"
        (self.host / "scripts").mkdir(parents=True)
        (self.host / "data").mkdir()
        (self.host / "backups").mkdir()
        for script in ("backup.sh", "verify-backup.sh", "notify-email.sh", "database.py"):
            shutil.copy(SCRIPTS / script, self.host / "scripts" / script)
        (self.host / "backup.env").write_text(
            f"BACKUP_ENCRYPTION_KEY='{self.key}'\nR2_ACCOUNT_ID='acct'\nR2_ACCESS_KEY_ID='id'\n"
            f"R2_SECRET_ACCESS_KEY='secret'\nR2_BUCKET='cato-backups'\nRESEND_API_KEY='{self.api_key}'\n"
            "EMAIL_FROM_ADDRESS='Cato <ops@example.test>'\nBACKUP_NOTIFY_EMAILS='a@example.test, b@example.test'\n")
        self.bucket = root / "bucket"
        self.bucket.mkdir()
        self.mail = root / "mail.jsonl"
        bin_dir = root / "bin"
        bin_dir.mkdir()
        stub = bin_dir / "stub"
        stub.write_text(STUB)
        stub.chmod(0o755)
        for name in ("podman", "rclone", "curl"):
            (bin_dir / name).symlink_to(stub)
        if not shutil.which("flock"):  # macOS: the lock is a no-op here
            (bin_dir / "flock").write_text("#!/bin/sh\nexit 0\n")
            (bin_dir / "flock").chmod(0o755)
        self.env = dict(os.environ, PATH=f"{bin_dir}:{os.environ['PATH']}", CATO_DIR=str(self.host),
                        TEST_BUCKET=str(self.bucket), TEST_MAIL=str(self.mail))
        conn = sqlite3.connect(self.host / "data/cato.db")
        conn.execute("PRAGMA journal_mode=WAL")
        for table in ("users", "sessions", "games"):
            conn.execute(f"CREATE TABLE {table} (id INTEGER PRIMARY KEY)")
        conn.execute("CREATE TABLE library_items (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users(id))")
        conn.execute("INSERT INTO users VALUES (1)")
        conn.execute("INSERT INTO library_items VALUES (1, 1)")
        conn.commit()
        conn.close()

    def run_script(self, name):
        result = subprocess.run(["bash", str(self.host / "scripts" / name)], env=self.env,
                                text=True, capture_output=True, timeout=60)
        self.assertNotIn(self.key, result.stdout + result.stderr)
        return result

    def mails(self):
        if not self.mail.exists():
            return []
        mails = [json.loads(line) for line in self.mail.read_text().splitlines()]
        for mail in mails:
            self.assertNotIn(self.api_key, json.dumps(mail["args"]))
        return mails

    def test_backup_is_encrypted_and_verifies(self):
        result = self.run_script("backup.sh")
        self.assertEqual(result.returncode, 0, result.stderr)
        [stored] = list(self.bucket.iterdir())
        self.assertRegex(stored.name, r"^cato_\d{8}_\d{6}\.db\.gz\.gpg$")
        self.assertNotIn(b"SQLite format 3", stored.read_bytes())
        result = self.run_script("verify-backup.sh")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("verified: 1 users, 1 library items", result.stdout)
        self.assertEqual(self.mails(), [])

    def test_a_corrupt_or_stale_backup_fails_and_emails(self):
        self.assertEqual(self.run_script("backup.sh").returncode, 0)
        [stored] = list(self.bucket.iterdir())
        stored.write_bytes(stored.read_bytes()[:-40])
        result = self.run_script("verify-backup.sh")
        self.assertNotEqual(result.returncode, 0)
        stored.rename(self.bucket / "cato_20200101_000000.db.gz.gpg")
        result = self.run_script("verify-backup.sh")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("stage: age", result.stderr + json.dumps(self.mails()))
        mails = self.mails()
        self.assertEqual(len(mails), 2)
        self.assertEqual(mails[0]["body"]["subject"], "Cato backup verification failed")
        self.assertEqual(mails[0]["body"]["to"], ["a@example.test", "b@example.test"])
        self.assertEqual(mails[0]["body"]["from"], "Cato <ops@example.test>")

    def test_a_failed_backup_emails(self):
        (self.host / "data/cato.db").unlink()
        result = self.run_script("backup.sh")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(list(self.bucket.iterdir()), [])
        [mail] = self.mails()
        self.assertEqual(mail["body"]["subject"], "Cato backup failed")
        self.assertIn("snapshot", mail["body"]["text"])

    def test_old_pre_deploy_snapshots_are_pruned(self):
        for i in range(5):
            snapshot = self.host / "backups" / f"cato-2026010{i}.db"
            snapshot.write_text("x")
            os.utime(snapshot, (time.time() - 1000 + i, time.time() - 1000 + i))
        (self.host / "backups" / "compose-old.yaml").write_text("kept")
        self.assertEqual(self.run_script("backup.sh").returncode, 0)
        left = sorted(p.name for p in (self.host / "backups").iterdir() if not p.name.startswith("."))
        self.assertEqual(left, ["cato-20260102.db", "cato-20260103.db", "cato-20260104.db", "compose-old.yaml"])


if __name__ == "__main__":
    unittest.main()
