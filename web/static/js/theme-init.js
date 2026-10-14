// theme-init.js — loaded render-blocking in <head>, before first paint, so
// a chosen theme never flashes the other one. External (not inline) for
// the CSP. "System" stores nothing and follows prefers-color-scheme.
try {
  var t = localStorage.getItem('cato-theme');
  if (t === 'light' || t === 'dark') {
    document.documentElement.setAttribute('data-theme', t);
    var m = document.querySelector('meta[name="theme-color"]');
    if (m) m.setAttribute('content', t === 'dark' ? '#0d0f14' : '#f4f5f8');
  }
} catch (e) {}
