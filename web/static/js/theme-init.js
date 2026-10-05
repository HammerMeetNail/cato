// theme-init.js — loaded render-blocking in <head>, before first paint.
// Applies the saved theme immediately so pages never flash the default theme.
// External (not inline) so the site's Content-Security-Policy can use
// script-src 'self' without unsafe-inline.
try {
  var t = localStorage.getItem('cato-theme');
  if (t) document.documentElement.setAttribute('data-theme', t);
} catch (e) {}
