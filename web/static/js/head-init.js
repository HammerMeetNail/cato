// head-init.js — loaded render-blocking in <head>, runs before first paint.
//
// Sizes the app frame via --app-h, consumed by html/body { height: var(--app-h) }.
// The bottom tab bar is a static flex item at the end of the body column, so
// its position derives entirely from this value (see #bottom-tabs in app.css).
//
// In iOS standalone (home-screen) mode, WebKit's viewport geometry is stale
// at cold open: 100dvh, window.innerHeight, and the layout viewport's bottom
// edge all exclude the bottom safe-area until the first scroll gesture
// reconciles them. No viewport measurement can be trusted at first paint.
// The app is fullscreen (viewport-fit=cover, black-translucent status bar)
// and portrait-locked, so the true app height is exactly the physical screen
// height — a static device property immune to that bug.
//
// In browser mode the URL bar occupies part of the screen, so innerHeight
// (not screen height) is correct there.
(function () {
  var docEl = document.documentElement;
  var standalone = navigator.standalone === true ||
    (window.matchMedia && window.matchMedia('(display-mode: standalone)').matches);
  var lastH = 0;

  function zoomed() {
    return !!(window.visualViewport &&
      Math.abs(window.visualViewport.scale - 1) > 0.001);
  }

  function measure() {
    if (standalone && window.screen) {
      // Fullscreen app: physical screen height is ground truth. iOS keeps
      // screen.width/height portrait-fixed while other platforms swap them,
      // so pick the orientation-appropriate dimension via min/max in case
      // the manifest's portrait lock is ever ignored (e.g. iPad).
      var landscape = window.matchMedia &&
        window.matchMedia('(orientation: landscape)').matches;
      return landscape
        ? Math.min(window.screen.width, window.screen.height)
        : Math.max(window.screen.width, window.screen.height);
    }
    return window.innerHeight;
  }

  function setAppH() {
    // Ignore measurements while pinch-zoomed: innerHeight shrinks to the
    // zoomed visual viewport, and writing that into --app-h collapses the
    // body and pulls the tab bar up into the content area. A resize event
    // fires again when scale returns to 1, re-measuring correctly.
    if (zoomed()) return;
    var h = measure();
    if (!h || h === lastH) return;
    lastH = h;
    docEl.style.setProperty('--app-h', h + 'px');
  }

  // Run synchronously before first paint.
  setAppH();
  // Browser-mode URL bar settles after load; in standalone measure() is
  // static so these re-runs are no-ops.
  requestAnimationFrame(setAppH);
  setTimeout(setAppH, 150);
  window.addEventListener('resize', setAppH);
  window.addEventListener('orientationchange', function () {
    // Screen/viewport values lag the event on iOS; re-measure after a beat.
    setTimeout(setAppH, 50);
  });
  if (window.visualViewport) {
    window.visualViewport.addEventListener('resize', setAppH);
  }
}());
