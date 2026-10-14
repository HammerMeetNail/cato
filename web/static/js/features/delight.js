// delight.js — small celebrations. Everything respects reduced motion.

export function reducedMotion() {
  return matchMedia('(prefers-reduced-motion: reduce)').matches;
}

// celebrate bursts confetti from the bottom of the screen for ~1.5 s.
export function celebrate() {
  if (reducedMotion()) return;
  const canvas = document.createElement('canvas');
  canvas.setAttribute('aria-hidden', 'true');
  Object.assign(canvas.style, { position: 'fixed', inset: '0', pointerEvents: 'none', zIndex: '90' });
  const dpr = Math.min(2, devicePixelRatio || 1);
  canvas.width = innerWidth * dpr;
  canvas.height = innerHeight * dpr;
  canvas.style.width = '100%';
  canvas.style.height = '100%';
  document.body.append(canvas);
  const ctx = canvas.getContext('2d');
  const styles = getComputedStyle(document.documentElement);
  const colors = ['--accent', '--status-playing', '--status-completed', '--star', '--status-wishlist']
    .map(v => styles.getPropertyValue(v).trim() || '#888');
  const parts = Array.from({ length: 120 }, () => ({
    x: innerWidth / 2 + (Math.random() - 0.5) * innerWidth * 0.4,
    y: innerHeight + 10,
    vx: (Math.random() - 0.5) * 9,
    vy: -(9 + Math.random() * 9),
    r: 3 + Math.random() * 4,
    a: Math.random() * Math.PI,
    va: (Math.random() - 0.5) * 0.3,
    c: colors[Math.floor(Math.random() * colors.length)],
  }));
  const start = performance.now();
  const frame = t => {
    const age = t - start;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, innerWidth, innerHeight);
    for (const p of parts) {
      p.vy += 0.32;
      p.x += p.vx;
      p.y += p.vy;
      p.a += p.va;
      ctx.save();
      ctx.globalAlpha = Math.max(0, 1 - age / 1600);
      ctx.translate(p.x, p.y);
      ctx.rotate(p.a);
      ctx.fillStyle = p.c;
      ctx.fillRect(-p.r, -p.r / 2, p.r * 2, p.r);
      ctx.restore();
    }
    if (age < 1600) requestAnimationFrame(frame);
    else canvas.remove();
  };
  requestAnimationFrame(frame);
}

// viewTransition runs fn inside a View Transition where supported.
export function viewTransition(fn) {
  if (!document.startViewTransition || reducedMotion()) return fn();
  return document.startViewTransition(fn);
}
