"""Release helper tests use only temporary local Git repositories/remotes."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("deploy.sh").resolve()


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="cato-release-tests-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "work"
        self.repo.mkdir()
        self.remote = self.root / "origin.git"
        subprocess.run(["git", "init", "--bare", str(self.remote)], check=True, capture_output=True)
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Release Test")
        self.git("config", "user.email", "release@example.invalid")
        (self.repo / "scripts").mkdir()
        shutil.copy2(SCRIPT, self.repo / "scripts/deploy.sh")
        self.commit("initial")
        self.git("remote", "add", "origin", str(self.remote))
        self.git("push", "-u", "origin", "main")

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.repo, check=True, text=True, capture_output=True).stdout.strip()

    def commit(self, text):
        (self.repo / "README.md").write_text(text)
        self.git("add", ".")
        self.git("commit", "-m", text)

    def release(self, dry=True, answer=""):
        env = dict(os.environ, DRY_RUN="1" if dry else "0")
        return subprocess.run(["bash", "scripts/deploy.sh"], cwd=self.repo, env=env,
                              input=answer, text=True, capture_output=True, timeout=10)

    def test_bootstrap_dry_run_has_no_release_side_effects(self):
        result = self.release()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("v0.1.0", result.stdout)
        self.assertEqual(self.git("tag"), "")
        self.assertEqual(self.git("ls-remote", "--tags", "origin"), "")

    def test_confirmed_release_is_annotated_and_local_fixture_only(self):
        result = self.release(dry=False, answer="y\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.git("cat-file", "-t", "v0.1.0"), "tag")
        self.assertIn("refs/tags/v0.1.0", self.git("ls-remote", "--tags", "origin"))
        self.assertNotEqual(self.release().returncode, 0)  # No new commits.
        self.commit("next change")
        self.git("push", "origin", "main")
        self.assertIn("v0.1.1", self.release().stdout)

    def test_cancel_does_not_tag(self):
        self.assertNotEqual(self.release(dry=False, answer="n\n").returncode, 0)
        self.assertEqual(self.git("tag"), "")

    def test_branch_dirty_and_unsynchronized_main_fail_closed(self):
        self.git("checkout", "-b", "feature")
        self.assertNotEqual(self.release().returncode, 0)
        self.git("checkout", "main")
        (self.repo / "README.md").write_text("dirty")
        self.assertNotEqual(self.release().returncode, 0)
        self.commit("ahead")
        self.assertNotEqual(self.release().returncode, 0)
        self.assertEqual(self.git("tag"), "")

    def test_fetch_failure_rejects_stale_tracking_ref(self):
        self.git("remote", "set-url", "origin", str(self.root / "missing.git"))
        self.assertNotEqual(self.release().returncode, 0)
        self.assertEqual(self.git("tag"), "")


if __name__ == "__main__":
    unittest.main()
