// HTTPS for every E2E project with no trust-store changes, mkcert or DNS
// setup (docs/cato_smooth.md §2.4):
// - `cato dev-cert` writes (or reuses) a self-signed certificate and prints
//   its SPKI SHA-256 pin. scripts/e2e-server.sh serves with the same files.
// - Chromium resolves the non-loopback name cato.test via
//   --host-resolver-rules and trusts the certificate via
//   --ignore-certificate-errors-spki-list, so pages get a secure context and
//   the service worker registers (ignoreHTTPSErrors alone does not let
//   Chromium register a service worker).
// - Playwright's Node-side request contexts (page.request, request fixture)
//   use Node's resolver, so cato.test is mapped there too.
import { execFileSync } from 'node:child_process';
import dns from 'node:dns';
import os from 'node:os';
import path from 'node:path';

export const E2E_HOST = 'cato.test';
export const E2E_PORT = 7180;
export const tlsDir = process.env.CATO_E2E_TLS_DIR || path.join(os.tmpdir(), 'cato-e2e-tls');

// Idempotent: every process that loads the config (runner and workers)
// gets the same certificate and pin.
export const spki = execFileSync(process.env.GO || 'go', ['run', './cmd/cato', 'dev-cert', '--out', tlsDir], {
  cwd: path.join(__dirname, '..'),
  encoding: 'utf8',
  stdio: ['ignore', 'pipe', 'inherit'],
}).trim();
if (!/^[A-Za-z0-9+/]{43}=$/.test(spki)) throw new Error(`cato dev-cert printed an invalid SPKI pin: ${spki}`);

type Lookup = typeof dns.promises.lookup;
const lookup: Lookup = dns.promises.lookup.bind(dns.promises);
dns.promises.lookup = (async (hostname: string, options?: any) => {
  if (hostname !== E2E_HOST) return lookup(hostname, options);
  const v4 = { address: '127.0.0.1', family: 4 };
  if (options?.all) return options.family === 6 ? [] : [v4];
  return v4;
}) as Lookup;
