"""E2E harness identity regression; uses an ephemeral loopback listener only."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import shutil
import signal
import ssl
import subprocess
import tempfile
import threading
import unittest

SCRIPT = Path(__file__).with_name("e2e-server.sh").resolve()
REPO = SCRIPT.parent.parent


class E2EServerTests(unittest.TestCase):
    def test_occupied_healthy_port_cannot_authorize_seeding_or_readiness(self):
        with tempfile.TemporaryDirectory(prefix="cato-e2e-harness-test-") as directory:
            root = Path(directory)
            repo = root / "repo"
            (repo / "scripts").mkdir(parents=True)
            (repo / "web/static").mkdir(parents=True)
            (repo / "web/static/index.html").write_text("disposable fixture")
            shutil.copy2(SCRIPT, repo / "scripts/e2e-server.sh")
            binaries = root / "bin"
            binaries.mkdir()
            scratch = root / "scratch"
            scratch.mkdir()
            child_log = root / "child.log"
            seeded = root / "seeded"

            # The harness is HTTPS-only, so the unrelated listener speaks TLS
            # too (a real dev certificate, generated the way the harness does).
            tls_dir = root / "tls"
            subprocess.run(["go", "run", "./cmd/cato", "dev-cert", "--out", str(tls_dir)],
                           cwd=REPO, check=True, capture_output=True)

            # A live child is deliberately insufficient: it does not own the
            # occupied listener. Signals provide the shutdown observation.
            app = root / "fake-app"
            app.write_text("""#!/usr/bin/env python3
import os, signal, ssl, sys, urllib.request
from pathlib import Path
if sys.argv[1:2] == ['dev-cert']:
    raise SystemExit(0)
log = Path(os.environ['E2E_TEST_CHILD_LOG'])
def stop(signum, frame):
    with log.open('a') as out:
        out.write('terminated\\n')
    raise SystemExit(0)
signal.signal(signal.SIGTERM, stop)
log.write_text(str(os.getpid()) + '\\n' + os.environ['CATO_STATIC_DIR'] + '\\n')
assert os.environ['CATO_TLS_CERT_FILE'].endswith('/cert.pem'), 'harness did not configure TLS'
urllib.request.urlopen('https://127.0.0.1' + os.environ['E2E_ADDR'] + '/fixture-started',
                       context=ssl._create_unverified_context()).read()
while True:
    signal.pause()
""")
            app.chmod(0o755)
            # Simulate build success without building or binding another app.
            (binaries / "go").write_text("""#!/usr/bin/env python3
import os, shutil, sys
shutil.copy2(os.environ['E2E_TEST_APP'], sys.argv[sys.argv.index('-o') + 1])
""")
            (binaries / "sqlite3").write_text("#!/bin/sh\n: > \"$E2E_TEST_SEEDED\"\n")
            # The readiness bound is iteration-based. Avoid waiting 20 seconds
            # when every real HTTP response deterministically rejects identity.
            (binaries / "sleep").write_text("#!/bin/sh\nexit 0\n")
            for binary in binaries.iterdir():
                binary.chmod(0o755)

            requests = []
            child_started = threading.Event()

            class UnrelatedApp(BaseHTTPRequestHandler):
                def do_GET(self):
                    requests.append(self.path)
                    if self.path == '/fixture-started':
                        child_started.set()
                    elif self.path == '/healthz':
                        child_started.wait(timeout=5)
                    body = b'{"status":"ok","database":"ok"}' if self.path == '/healthz' else b'unrelated app'
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(body)

                def log_message(self, *args):
                    pass

            server = ThreadingHTTPServer(("127.0.0.1", 0), UnrelatedApp)
            tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            tls.load_cert_chain(tls_dir / "cert.pem", tls_dir / "key.pem")
            server.socket = tls.wrap_socket(server.socket, server_side=True)
            server_thread = threading.Thread(target=server.serve_forever, daemon=True)
            server_thread.start()
            env = dict(os.environ, PATH=str(binaries) + os.pathsep + os.environ["PATH"],
                       TMPDIR=str(scratch), E2E_ADDR=f":{server.server_port}", E2E_TLS_DIR=str(tls_dir),
                       E2E_TEST_APP=str(app), E2E_TEST_CHILD_LOG=str(child_log), E2E_TEST_SEEDED=str(seeded))
            try:
                process = subprocess.Popen(["bash", "scripts/e2e-server.sh"], cwd=repo,
                                           env=env, text=True, stdout=subprocess.PIPE,
                                           stderr=subprocess.PIPE, start_new_session=True)
                try:
                    stdout, stderr = process.communicate(timeout=15)
                finally:
                    # Also clean up if a regressed harness falsely emits ready
                    # and waits forever on the intentionally live fake child.
                    if process.poll() is None:
                        os.killpg(process.pid, signal.SIGTERM)
                        try:
                            process.communicate(timeout=5)
                        except subprocess.TimeoutExpired:
                            os.killpg(process.pid, signal.SIGKILL)
                            process.communicate()
                result = subprocess.CompletedProcess(process.args, process.returncode, stdout, stderr)
            finally:
                server.shutdown()
                server.server_close()
                server_thread.join(timeout=5)
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertNotIn("CATO_E2E_READY", result.stdout)
            self.assertIn("failed to establish owned readiness", result.stderr)
            self.assertFalse(seeded.exists(), "unrelated healthy listener authorized SQL seeding")
            self.assertIn('/healthz', requests)
            self.assertTrue(any(path.startswith('/e2e-ready-') for path in requests))
            child_lines = child_log.read_text().splitlines()
            self.assertEqual(child_lines[-1], 'terminated', "harness did not terminate its child")
            self.assertTrue(child_lines[1].startswith(str(scratch)), "static assets were not isolated")
            self.assertEqual(list(scratch.iterdir()), [], "harness temporary files survived cleanup")
            with self.assertRaises(ProcessLookupError):
                os.kill(int(child_lines[0]), 0)


if __name__ == "__main__":
    unittest.main()
