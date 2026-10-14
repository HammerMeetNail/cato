// lit.js — the one place that imports the vendored lit-html, so views
// import rendering from here. unsafeHTML is deliberately not vendored.
// Only the directives the app uses are re-exported: each is a module the
// browser fetches before first paint.
export { html, svg, render, nothing, noChange } from '../vendor/lit-html/lit-html.js';
export { repeat } from '../vendor/lit-html/directives/repeat.js';
export { live } from '../vendor/lit-html/directives/live.js';
export { ref, createRef } from '../vendor/lit-html/directives/ref.js';
// The directives import these from minified code the server's preload scan
// doesn't follow; naming them here gets them modulepreloaded with the rest
// instead of fetched one round trip later.
import '../vendor/lit-html/directive.js';
import '../vendor/lit-html/directive-helpers.js';
import '../vendor/lit-html/async-directive.js';
