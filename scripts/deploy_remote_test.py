"""Exercise the remote rollout locally: SSH, Docker and HTTP are always stubs."""
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("deploy-remote.sh").resolve()
REPOSITORY = SCRIPT.parent.parent

# Symlinks select a stub by argv[0]; Docker inspects backups using the real SQLite CLI.
STUB = r'''#!/usr/bin/env python3
import json, os, pathlib, shlex, stat, subprocess, sys
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
def record(**extra):
    with open(os.environ['TEST_EVENTS'], 'a') as log:
        log.write(json.dumps(dict(command=name, args=args, **extra)) + '\n')
if name == 'ssh':
    record()
    assert args[:1] == ['-F'] and args[2] == 'cato-deploy'
    config = pathlib.Path(args[1]).read_text()
    assert 'StrictHostKeyChecking yes' in config
    identity = next(line.split()[1] for line in config.splitlines() if line.strip().startswith('IdentityFile '))
    assert stat.S_IMODE(pathlib.Path(identity).stat().st_mode) == 0o600
    remote = shlex.split(args[3])
    assert remote[:3] == ['bash', '-s', '--']
    assert pathlib.Path(remote[3]).resolve() == pathlib.Path(os.environ['TEST_SERVER']).resolve()
    # Execute only the captured shell program against our temporary server directory.
    sys.exit(subprocess.run(remote, input=sys.stdin.read(), text=True).returncode)
elif name == 'docker':
    snapshots = list(pathlib.Path('backups').glob('cato-*.db'))
    values = []
    if 'up' in args:
        for snapshot in snapshots:
            values.append(subprocess.check_output([os.environ['TEST_SQLITE'], str(snapshot),
                          'PRAGMA integrity_check; PRAGMA foreign_key_check; PRAGMA journal_mode; SELECT value FROM marker;'], text=True))
    record(backups=[str(p) for p in snapshots], verified_values=values)
    assert args[0] in ('compose', 'pull', 'run')
    if 'up' in args:
        assert '--wait' in args
        assert json.loads(pathlib.Path('.release-state.json').read_text())['status'] == 'attempted'
        sys.exit(int(os.environ.get('TEST_UP_EXIT', '0')))
    sys.exit(0)
elif name == 'curl':
    record()
    assert args[-1] == 'http://127.0.0.1:7080/healthz'
    previous = [json.loads(line) for line in pathlib.Path(os.environ['TEST_EVENTS']).read_text().splitlines()]
    calls = sum(e['command'] == 'curl' for e in previous)
    sys.exit(int(os.environ.get('TEST_HEALTH_EXIT' if calls == 1 else 'TEST_FINAL_HEALTH_EXIT', '0')))
else:
    raise AssertionError('Unexpected stub: ' + name)
'''


class RemoteDeployTests(unittest.TestCase):
    def setUp(self):
        self.sqlite = shutil.which("sqlite3")
        self.assertIsNotNone(self.sqlite, "sqlite3 CLI is required")
        self.temp = tempfile.TemporaryDirectory(prefix="cato-remote-tests-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.server = self.root / "server"
        (self.server / "data/covers").mkdir(parents=True)
        self.original_env = "GOOGLE_SECRET=host-managed-test-value\n"
        (self.server / ".env").write_text(self.original_env)
        (self.server / "compose.yaml").write_text("old compose\n")
        (self.server / "release.env").write_text("old digest\n")
        self.events = self.root / "events.jsonl"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        stub = self.bin / "stub"
        stub.write_text(STUB)
        stub.chmod(0o755)
        for command in ("ssh", "docker", "curl"):
            (self.bin / command).symlink_to(stub)
        self.key = "dummy-private-key-no-command-should-contain-this"
        self.env = dict(os.environ, PATH=f"{self.bin}:{os.environ['PATH']}",
                        DEPLOY_HOST="host.invalid", DEPLOY_USER="deploy",
                        DEPLOY_PATH=str(self.server), DEPLOY_PORT="22",
                        IMAGE_REPOSITORY="quay.io/nabu/cato", IMAGE_DIGEST="sha256:" + "a" * 64,
                        SSH_PRIVATE_KEY=self.key, SSH_KNOWN_HOSTS="host.invalid dummy-key",
                        DEPLOY_CLOUDFLARE_ACCESS="false", RELEASE_TAG="v0.2.0", RELEASE_COMMIT="b" * 40, TEST_EVENTS=str(self.events),
                        TEST_SERVER=str(self.server), TEST_SQLITE=self.sqlite)

    def deploy(self, **overrides):
        self.events.unlink(missing_ok=True)
        result = subprocess.run(["bash", str(SCRIPT)], env=dict(self.env, **overrides),
                                text=True, capture_output=True, timeout=20)
        events = self.events.read_text() if self.events.exists() else ""
        self.assertNotIn(self.key, result.stdout + result.stderr + events)
        return result, [json.loads(line) for line in events.splitlines()]

    def database(self):
        conn = sqlite3.connect(self.server / "data/cato.db")
        conn.execute("PRAGMA journal_mode=WAL")
        for table in ("users", "sessions", "games", "library_items"):
            conn.execute(f"CREATE TABLE {table} (id INTEGER PRIMARY KEY)")
        conn.execute("CREATE TABLE marker (value TEXT)")
        conn.execute("INSERT INTO marker VALUES ('committed WAL row')")
        conn.commit()
        self.addCleanup(conn.close)
        return conn  # Keep the connection open so committed WAL content stays live.

    def assert_original_manifests(self):
        self.assertEqual((self.server / "compose.yaml").read_text(), "old compose\n")
        self.assertEqual((self.server / "release.env").read_text(), "old digest\n")
        self.assertEqual((self.server / ".env").read_text(), self.original_env)

    def test_backup_verified_before_restart_and_host_files_preserved(self):
        self.database()
        self.assertTrue((self.server / "data/cato.db-wal").exists())
        result, events = self.deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        up = next(i for i, e in enumerate(events) if e['command'] == 'docker' and 'up' in e['args'])
        self.assertEqual(events[up]['verified_values'], ['ok\ndelete\ncommitted WAL row\n'])
        self.assertEqual(len(events[up]['backups']), 1)
        backups = list((self.server / "backups").glob("cato-*.db"))
        self.assertEqual(len(backups), 1)
        with sqlite3.connect(backups[0]) as conn:
            self.assertEqual(conn.execute("SELECT value FROM marker").fetchall(), [('committed WAL row',)])
        self.assertEqual((self.server / ".env").read_text(), self.original_env)
        self.assertEqual(next((self.server / "backups").glob("compose-*.yaml")).read_text(), "old compose\n")
        self.assertEqual(next((self.server / "backups").glob("release-*.env")).read_text(), "old digest\n")
        self.assertEqual((self.server / "compose.yaml").read_text(), (REPOSITORY / "compose.server.yaml").read_text())
        self.assertEqual((self.server / "release.env").read_text(),
                         f"CATO_IMAGE={self.env['IMAGE_REPOSITORY']}@{self.env['IMAGE_DIGEST']}\n")
        self.assertFalse([p for p in self.server.glob(".deploy.*") if p.is_dir()])
        self.assertEqual(json.loads((self.server / ".release-state.json").read_text())["status"], "succeeded")

    def test_corrupt_database_aborts_before_restart_or_manifest_change(self):
        (self.server / "data/cato.db").write_bytes(b"not a SQLite database")
        result, events = self.deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(e['command'] == 'docker' and 'up' in e['args'] for e in events))
        self.assert_original_manifests()

    def test_unhealthy_existing_service_can_be_repaired(self):
        self.database()
        result, events = self.deploy(TEST_HEALTH_EXIT="22")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(any(e['command'] == 'docker' and 'up' in e['args'] for e in events))

    def test_foreign_key_failure_aborts_before_restart(self):
        conn = self.database()
        conn.execute("CREATE TABLE children (parent INTEGER REFERENCES users(id))")
        conn.execute("INSERT INTO children VALUES (999)")
        conn.commit()
        result, events = self.deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(e['command'] == 'docker' and 'up' in e['args'] for e in events))
        self.assert_original_manifests()
        self.assertFalse((self.server / ".release-state.json").exists())

    def test_older_or_retargeted_release_rejected_before_pull(self):
        self.database()
        self.assertEqual(self.deploy()[0].returncode, 0)
        state = (self.server / ".release-state.json").read_text()
        manifest = (self.server / "release.env").read_text()
        for overrides in ({"RELEASE_TAG": "v0.1.9"}, {"RELEASE_COMMIT": "c" * 40}):
            with self.subTest(overrides=overrides):
                result, events = self.deploy(**overrides)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(e['command'] == 'docker' and e['args'][0] == 'pull' for e in events))
                self.assertEqual((self.server / ".release-state.json").read_text(), state)
                self.assertEqual((self.server / "release.env").read_text(), manifest)

    def test_failed_rollout_retains_attempt_guard_and_allows_same_commit_retry(self):
        self.database()
        for failure in ({"TEST_UP_EXIT": "1"}, {"TEST_FINAL_HEALTH_EXIT": "22"}):
            with self.subTest(failure=failure):
                result, _ = self.deploy(**failure)
                self.assertNotEqual(result.returncode, 0)
                state = json.loads((self.server / ".release-state.json").read_text())
                self.assertEqual(state['status'], 'attempted')
                self.assertEqual(state['tag'], 'v0.2.0')
                self.assertNotEqual(self.deploy(RELEASE_TAG="v0.1.9")[0].returncode, 0)
        result, _ = self.deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.server / ".release-state.json").read_text())['status'], 'succeeded')

    def test_shell_metacharacters_rejected_before_ssh(self):
        for key in ("DEPLOY_PATH", "DEPLOY_HOST", "IMAGE_REPOSITORY", "IMAGE_DIGEST", "RELEASE_TAG", "RELEASE_COMMIT"):
            for suffix in (";echo injected", "$(echo injected)", "'", "\nInjected value"):
                with self.subTest(key=key, suffix=suffix):
                    result, events = self.deploy(**{key: self.env[key] + suffix})
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(events, [])
        self.assert_original_manifests()


if __name__ == "__main__":
    unittest.main()
