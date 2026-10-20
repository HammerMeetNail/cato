// head-init.js — render-blocking in <head>, before first paint: sets
// --app-h, the app frame's height (html, body in base.css). An iOS
// home-screen app's viewport leaves out the bottom safe area until the
// first scroll, so standalone uses the screen's height; a browser tab uses
// innerHeight (its toolbars take part of the screen).
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
      // iOS keeps screen.width/height portrait-fixed; other platforms swap.
      var landscape = window.matchMedia &&
        window.matchMedia('(orientation: landscape)').matches;
      return landscape
        ? Math.min(window.screen.width, window.screen.height)
        : Math.max(window.screen.width, window.screen.height);
    }
    return window.innerHeight;
  }

  function setAppH() {
    // Not while pinch-zoomed: innerHeight is the zoomed viewport's then, and
    // a resize follows when the zoom ends.
    if (zoomed()) return;
    var h = measure();
    if (!h || h === lastH) return;
    lastH = h;
    docEl.style.setProperty('--app-h', h + 'px');
  }

  setAppH();
  // A browser's toolbars settle after load.
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
