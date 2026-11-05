"""Exercise the remote rollout locally: SSH, podman and HTTP are always stubs."""
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

# Symlinks select a stub by argv[0]; podman-compose inspects backups using the real SQLite CLI.
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
    server = str(pathlib.Path(os.environ['TEST_SERVER']).resolve())
    if args[3].startswith('umask 077 && cat > '):
        # The rendered settings arrive as data on stdin, never in the command.
        assert args[3] in ("umask 077 && cat > '" + os.environ['TEST_SERVER'] + "/.env.next'",
                           "umask 077 && cat > '" + os.environ['TEST_SERVER'] + "/backup.env.next'")
        sys.exit(subprocess.run(['sh', '-c', args[3]], input=sys.stdin.read(), text=True).returncode)
    remote = shlex.split(args[3])
    assert remote[:3] == ['bash', '-s', '--']
    assert str(pathlib.Path(remote[3]).resolve()) == server
    # Execute only the captured shell program against our temporary server directory.
    sys.exit(subprocess.run(remote, input=sys.stdin.read(), text=True).returncode)
elif name == 'podman':
    record()
    assert args[0] in ('pull', 'run', 'unshare', 'image', 'images', 'rmi')
    if args[0] == 'unshare':
        sys.exit(subprocess.run(args[1:]).returncode)
    host_images = json.loads(os.environ.get('TEST_IMAGES', '{"rows": [], "refs": {}}'))
    if args[0] == 'image':
        assert args[:3] == ['image', 'inspect', '--format']
        if args[-1] not in host_images['refs']:
            sys.exit(125)
        print('sha256:' + host_images['refs'][args[-1]].ljust(64, '0'))
    elif args[0] == 'images':
        for row in host_images['rows']:
            print(row[0], row[1])
    elif args[0] == 'rmi':
        sys.exit(int(os.environ.get('TEST_RMI_EXIT', '0')))
    sys.exit(0)
elif name == 'podman-compose':
    snapshots = list(pathlib.Path('backups').glob('cato-*.db'))
    values = []
    for snapshot in snapshots:
        values.append(subprocess.check_output([os.environ['TEST_SQLITE'], str(snapshot),
                      'PRAGMA integrity_check; PRAGMA foreign_key_check; PRAGMA journal_mode; SELECT value FROM marker;'], text=True))
    try:
        os.fstat(9)
        lock_inherited = True
    except OSError:
        lock_inherited = False
    record(backups=[str(p) for p in snapshots], verified_values=values, env=pathlib.Path('.env').read_text(), lock_inherited=lock_inherited)
    assert args == ['-f', 'compose.yaml', 'up', '-d', '--force-recreate']
    assert json.loads(pathlib.Path('.release-state.json').read_text())['status'] == 'attempted'
    sys.exit(int(os.environ.get('TEST_UP_EXIT', '0')))
elif name == 'sleep':
    sys.exit(0)
elif name == 'curl':
    record()
    assert args[-1] == 'http://127.0.0.1:8083/healthz'
    previous = [json.loads(line) for line in pathlib.Path(os.environ['TEST_EVENTS']).read_text().splitlines()]
    calls = sum(e['command'] == 'curl' for e in previous)
    sys.exit(int(os.environ.get('TEST_HEALTH_EXIT' if calls == 1 else 'TEST_FINAL_HEALTH_EXIT', '0')))
elif name == 'flock':
    # macOS has no flock(1); the deployment lock is a no-op in the stub.
    record()
    sys.exit(0)
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
        self.original_env = "GOOGLE_SECRET='previous-value'\n"
        (self.server / ".env").write_text(self.original_env)
        (self.server / "compose.yaml").write_text("old compose\n")
        (self.server / "release.env").write_text("old digest\n")
        self.events = self.root / "events.jsonl"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        stub = self.bin / "stub"
        stub.write_text(STUB)
        stub.chmod(0o755)
        for command in ("ssh", "podman", "podman-compose", "curl", "flock", "sleep"):
            (self.bin / command).symlink_to(stub)
        self.key = "dummy-private-key-no-command-should-contain-this"
        self.secret = "google-secret-$HOME-no-command-should-contain-this"
        self.backup_key = "backup-key-no-command-should-contain-this"
        self.env = dict(os.environ, PATH=f"{self.bin}:{os.environ['PATH']}",
                        DEPLOY_HOST="host.invalid", DEPLOY_USER="deploy",
                        DEPLOY_PATH=str(self.server), DEPLOY_PORT="22",
                        IMAGE_REPOSITORY="quay.io/localbyte/cato", IMAGE_DIGEST="sha256:" + "a" * 64,
                        SSH_PRIVATE_KEY=self.key, SSH_KNOWN_HOSTS="host.invalid dummy-key",
                        DEPLOY_CLOUDFLARE_ACCESS="false", RELEASE_TAG="v0.2.0", RELEASE_COMMIT="b" * 40, TEST_EVENTS=str(self.events),
                        CATO_BASE_URL="https://cato.example.test", CATO_TRUSTED_PROXIES="127.0.0.1/8",
                        GOOGLE_SECRET=self.secret, STEAM_WEB_API_KEY="",
                        BACKUP_ENCRYPTION_KEY=self.backup_key, R2_ACCOUNT_ID="acct", R2_BUCKET="cato-backups",
                        TEST_SERVER=str(self.server), TEST_SQLITE=self.sqlite)

    def deploy(self, **overrides):
        self.events.unlink(missing_ok=True)
        result = subprocess.run(["bash", str(SCRIPT)], env=dict(self.env, **overrides),
                                text=True, capture_output=True, timeout=20)
        events = self.events.read_text() if self.events.exists() else ""
        self.assertNotIn(self.key, result.stdout + result.stderr + events)
        for secret in (self.secret, self.backup_key):
            self.assertNotIn(secret, result.stdout + result.stderr + "".join(
                json.dumps(json.loads(line)["args"]) for line in events.splitlines()))
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

    def test_backup_verified_before_restart_and_settings_installed(self):
        self.database()
        self.assertTrue((self.server / "data/cato.db-wal").exists())
        result, events = self.deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        up = next(i for i, e in enumerate(events) if e['command'] == 'podman-compose')
        self.assertEqual(events[up]['verified_values'], ['ok\ndelete\ncommitted WAL row\n'])
        self.assertEqual(len(events[up]['backups']), 1)
        backups = list((self.server / "backups").glob("cato-*.db"))
        self.assertEqual(len(backups), 1)
        with sqlite3.connect(backups[0]) as conn:
            self.assertEqual(conn.execute("SELECT value FROM marker").fetchall(), [('committed WAL row',)])
        rendered = ("CATO_BASE_URL='https://cato.example.test'\nCATO_TRUSTED_PROXIES='127.0.0.1/8'\n"
                    f"GOOGLE_SECRET='{self.secret}'\n")
        self.assertEqual(events[up]['env'], rendered)
        self.assertEqual((self.server / ".env").read_text(), rendered)
        self.assertEqual(next((self.server / "backups").glob("env-*")).read_text(), self.original_env)
        # The backup's settings stay out of the app's .env, in their own file.
        self.assertEqual((self.server / "backup.env").read_text(),
                         f"BACKUP_ENCRYPTION_KEY='{self.backup_key}'\nR2_ACCOUNT_ID='acct'\nR2_BUCKET='cato-backups'\n")
        self.assertNotIn(self.backup_key, rendered)
        for script in ("backup.sh", "verify-backup.sh", "notify-email.sh", "database.py", "prune.py"):
            self.assertEqual((self.server / "scripts" / script).read_bytes(), (REPOSITORY / "scripts" / script).read_bytes())
        self.assertEqual(next((self.server / "backups").glob("compose-*.yaml")).read_text(), "old compose\n")
        self.assertEqual(next((self.server / "backups").glob("release-*.env")).read_text(), "old digest\n")
        image = f"{self.env['IMAGE_REPOSITORY']}@{self.env['IMAGE_DIGEST']}"
        compose = (self.server / "compose.yaml").read_text()
        self.assertIn(f"    image: {image}\n", compose)
        self.assertNotIn("${CATO_IMAGE", compose)
        self.assertEqual((self.server / "release.env").read_text(),
                         f"CATO_IMAGE={self.env['IMAGE_REPOSITORY']}@{self.env['IMAGE_DIGEST']}\n")
        self.assertFalse([p for p in self.server.glob(".deploy.*") if p.is_dir()])
        self.assertEqual(json.loads((self.server / ".release-state.json").read_text())["status"], "succeeded")

    def test_started_container_does_not_inherit_the_deploy_lock(self):
        # conmon and rootlessport outlive the deploy: holding fd 9 they kept the
        # lock for as long as the container ran, so the next deploy always failed.
        result, events = self.deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        compose = next(e for e in events if e['command'] == 'podman-compose')
        self.assertFalse(compose['lock_inherited'])

    def seed_old_copies(self):
        backups = self.server / "backups"
        backups.mkdir()
        (backups / "empty-before-import").mkdir()
        for i in range(5):
            with sqlite3.connect(backups / f"cato-old{i}.db") as conn:
                conn.execute("CREATE TABLE marker (value TEXT)")
            conn.close()
            for name in (f"compose-old{i}.yaml", f"release-old{i}.env", f"env-old{i}"):
                (backups / name).write_text("old\n")
            for name in (f"cato-old{i}.db", f"compose-old{i}.yaml", f"release-old{i}.env", f"env-old{i}"):
                os.utime(backups / name, (1000 + i, 1000 + i))
        return backups

    def test_deploy_keeps_the_newest_three_snapshots_and_config_copies(self):
        self.database()
        backups = self.seed_old_copies()
        result, _ = self.deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        for pattern, old in (("cato-*.db", ["cato-old3.db", "cato-old4.db"]),
                             ("compose-*.yaml", ["compose-old3.yaml", "compose-old4.yaml"]),
                             ("release-*.env", ["release-old3.env", "release-old4.env"]),
                             ("env-*", ["env-old3", "env-old4"])):
            names = sorted(p.name for p in backups.glob(pattern))
            self.assertEqual(len(names), 3, names)
            self.assertTrue(set(old) <= set(names), names)
        self.assertTrue((backups / "empty-before-import").is_dir())

    def test_a_failed_rollout_still_prunes_and_keeps_its_snapshot(self):
        # Retried failures mustn't fill the disk, and the new snapshot is the rollback.
        self.database()
        backups = self.seed_old_copies()
        result, _ = self.deploy(TEST_FINAL_HEALTH_EXIT="1", TEST_HEALTH_EXIT="1")
        self.assertNotEqual(result.returncode, 0)
        names = sorted(p.name for p in backups.glob("cato-*.db"))
        self.assertEqual(len(names), 3, names)
        self.assertEqual(len([n for n in names if "old" not in n]), 1, names)

    def images_on_host(self, previous, before_previous, removable, **refs):
        repo = self.env["IMAGE_REPOSITORY"]
        current = f"{repo}@{self.env['IMAGE_DIGEST']}"
        backups = self.server / "backups"
        backups.mkdir(exist_ok=True)
        # The older deployment first: mtimes order the copies. (The newest copy is
        # the deploy's own, of release.env, so that file holds the previous image.)
        (self.server / "release.env").write_text(f"CATO_IMAGE={repo}@sha256:{previous}\n")
        (backups / "release-older.env").write_text(f"CATO_IMAGE={repo}@sha256:{before_previous}\n")
        os.utime(backups / "release-older.env", (1000, 1000))
        return json.dumps({
            # Newest build date first, as podman lists them: a cached build keeps an
            # old date, so the image before last sorts below one that's removable.
            "rows": [[repo, "cccccccccccc"], [repo, removable], [repo, "bbbbbbbbbbbb"], [repo, "aaaaaaaaaaaa"],
                     ["quay.io/nabu/nabu", "dddddddddddd"], ["quay.io/localbyte/cato-other", "eeeeeeeeeeee"]],
            "refs": {current: "cccccccccccc", f"{repo}@sha256:{previous}": "bbbbbbbbbbbb",
                     f"{repo}@sha256:{before_previous}": "aaaaaaaaaaaa", **refs}})

    def removed(self, events):
        return [e["args"] for e in events if e["command"] == "podman" and e["args"][0] == "rmi"]

    def test_deploy_removes_only_this_repositorys_images_outside_its_history(self):
        images = self.images_on_host("prev", "older", "ffffffffffff")
        result, events = self.deploy(TEST_IMAGES=images)
        self.assertEqual(result.returncode, 0, result.stderr)
        # Never --force: podman refuses an image a container is using.
        self.assertEqual(self.removed(events), [["rmi", "ffffffffffff"]])

    def test_images_are_left_alone_when_the_history_cant_be_read(self):
        images = json.loads(self.images_on_host("prev", "older", "ffffffffffff"))
        del images["refs"][f"{self.env['IMAGE_REPOSITORY']}@sha256:older"]
        result, events = self.deploy(TEST_IMAGES=json.dumps(images))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.removed(events), [])
        self.assertIn("left alone", result.stderr)

    def test_a_failed_cleanup_does_not_fail_a_healthy_deploy(self):
        images = self.images_on_host("prev", "older", "ffffffffffff")
        result, events = self.deploy(TEST_IMAGES=images, TEST_RMI_EXIT="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.removed(events), [["rmi", "ffffffffffff"]])
        self.assertEqual(json.loads((self.server / ".release-state.json").read_text())["status"], "succeeded")

    def test_corrupt_database_aborts_before_restart_or_manifest_change(self):
        (self.server / "data/cato.db").write_bytes(b"not a SQLite database")
        result, events = self.deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(e['command'] == 'podman-compose' for e in events))
        self.assert_original_manifests()

    def test_unhealthy_existing_service_can_be_repaired(self):
        self.database()
        result, events = self.deploy(TEST_HEALTH_EXIT="22")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(any(e['command'] == 'podman-compose' for e in events))

    def test_foreign_key_failure_aborts_before_restart(self):
        conn = self.database()
        conn.execute("CREATE TABLE children (parent INTEGER REFERENCES users(id))")
        conn.execute("INSERT INTO children VALUES (999)")
        conn.commit()
        result, events = self.deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(e['command'] == 'podman-compose' for e in events))
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
                self.assertFalse(any(e['command'] == 'podman' and e['args'][0] == 'pull' for e in events))
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

    def test_settings_that_would_break_the_env_file_rejected_before_ssh(self):
        for value in ("it's", "line\nINJECTED=1", "carriage\r"):
            with self.subTest(value=value):
                result, events = self.deploy(GOOGLE_SECRET=value)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(events, [])
        for value in ("http://cato.example.test", "https://cato.example.test/path", ""):
            with self.subTest(base_url=value):
                result, events = self.deploy(CATO_BASE_URL=value)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(events, [])
        self.assert_original_manifests()

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
