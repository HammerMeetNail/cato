// decode.js — loads and decodes covers before a screen is drawn. A screen's
// enter() awaits it for the covers it shows first, inside the shell's hold
// (app.js onRoute), so they're in the screen's first frame instead of
// popping in one by one. Covers the browser already has settle at once.
import { coverSources, decoded } from './parts.js';

export function decodeCovers(urls, thumb = false) {
  return Promise.all(urls.map(url => {
    const { src, srcset } = coverSources(url, thumb);
    if (!src) return null;
    const img = new Image();
    if (srcset) img.srcset = srcset;
    img.src = src;
    return img.decode().then(() => { decoded.add(src); }, () => {});
  }));
}
