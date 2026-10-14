#!/usr/bin/env node
// Guardrails for the frontend (docs/cato_smooth.md §4.2): HTML must be
// rendered through lit-html templates (which escape values), never by
// assigning HTML strings. Fails on HTML-string sinks and eval-like calls
// anywhere under web/static/js except the vendored libraries and the
// legacy screens listed below (which the new shell replaces).
'use strict';
const fs = require('node:fs');
const path = require('node:path');

const root = path.join(__dirname, '..', 'web', 'static', 'js');
const LEGACY = new Set(process.env.CATO_LINT_LEGACY === '0' ? [] : [
  'api.js', 'app.js', 'library.js', 'search.js', 'playing.js', 'stats.js', 'settings.js', 'dates.js',
]);
const RULES = [
  [/\.innerHTML\b/, 'innerHTML'],
  [/\.outerHTML\b/, 'outerHTML'],
  [/\binsertAdjacentHTML\b/, 'insertAdjacentHTML'],
  [/\bdocument\.write(ln)?\b/, 'document.write'],
  [/\bunsafeHTML\b|\bunsafeSVG\b/, 'unsafeHTML'],
  [/\beval\s*\(/, 'eval'],
  [/\bnew\s+Function\s*\(/, 'new Function'],
  [/\bsetTimeout\s*\(\s*['"`]/, 'string setTimeout'],
  [/javascript:/i, 'javascript: URL'],
];

const problems = [];
function walk(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    const rel = path.relative(root, full);
    if (entry.isDirectory()) {
      if (rel === 'vendor') continue;
      walk(full);
    } else if (entry.name.endsWith('.js') && !LEGACY.has(rel)) {
      fs.readFileSync(full, 'utf8').split('\n').forEach((line, i) => {
        if (/^\s*(\/\/|\*)/.test(line)) return;
        for (const [re, name] of RULES) {
          if (re.test(line)) problems.push(`${path.join('web/static/js', rel)}:${i + 1}: ${name} is not allowed (render with lit-html)`);
        }
      });
    }
  }
}
walk(root);
if (problems.length) {
  console.error(problems.join('\n'));
  process.exit(1);
}
console.log('PASS: no HTML-string sinks outside vendor/ and the legacy screens');
