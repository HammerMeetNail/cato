// icons.js — Lucide icons as lit templates (data in icons-data.js).
import { svg, nothing } from './lit.js';
import { iconNodes } from './icons-data.js';

function node([tag, a]) {
  switch (tag) {
    case 'path': return svg`<path d=${a.d}></path>`;
    case 'circle': return svg`<circle cx=${a.cx} cy=${a.cy} r=${a.r}></circle>`;
    case 'rect': return svg`<rect x=${a.x ?? 0} y=${a.y ?? 0} width=${a.width} height=${a.height} rx=${a.rx ?? 0} ry=${a.ry ?? a.rx ?? 0}></rect>`;
    case 'line': return svg`<line x1=${a.x1} y1=${a.y1} x2=${a.x2} y2=${a.y2}></line>`;
    case 'polyline': return svg`<polyline points=${a.points}></polyline>`;
    case 'polygon': return svg`<polygon points=${a.points}></polygon>`;
    case 'ellipse': return svg`<ellipse cx=${a.cx} cy=${a.cy} rx=${a.rx} ry=${a.ry}></ellipse>`;
  }
  return nothing;
}

// icon renders a decorative icon (aria-hidden); give the control a label.
export function icon(name, cls = '') {
  const nodes = iconNodes[name];
  if (!nodes) return nothing;
  return svg`<svg class="icon ${cls}" viewBox="0 0 24 24" aria-hidden="true" focusable="false">${nodes.map(node)}</svg>`;
}

// starIcon is a filled star clipped to fraction (0, 0.5 or 1).
const STAR = 'M11.525 2.295a.53.53 0 0 1 .95 0l2.31 4.679a2.123 2.123 0 0 0 1.595 1.16l5.166.756a.53.53 0 0 1 .294.904l-3.736 3.638a2.123 2.123 0 0 0-.611 1.878l.882 5.14a.53.53 0 0 1-.771.56l-4.618-2.428a2.122 2.122 0 0 0-1.973 0L6.396 21.01a.53.53 0 0 1-.77-.56l.881-5.139a2.122 2.122 0 0 0-.611-1.879L2.16 9.795a.53.53 0 0 1 .294-.906l5.165-.755a2.122 2.122 0 0 0 1.597-1.16z';
let clipSeq = 0;
export function starIcon(fraction) {
  const id = `sc${++clipSeq}`;
  return svg`<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
    <defs><clipPath id=${id}><rect x="0" y="0" width=${24 * fraction} height="24"></rect></clipPath></defs>
    <path class="bg" d=${STAR}></path>
    <path class="fg" d=${STAR} clip-path=${`url(#${id})`}></path>
  </svg>`;
}
