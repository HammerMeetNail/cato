// filter.js — searching, filtering and sorting the on-device library index.
// Pure functions over plain entry objects (see store.js entryFrom).

// normalize folds case, accents and punctuation so "Pokémon: Red" matches
// "pokemon red".
export function normalize(s) {
  return String(s || '')
    .toLowerCase()
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/['’‘]/g, '')
    .replace(/[^\p{L}\p{N}]+/gu, ' ')
    .trim();
}

// Common platform nicknames, so "@ps5" or "@switch" find canonical names.
const PLATFORM_ALIASES = {
  ps1: 'playstation', psx: 'playstation', ps2: 'playstation 2', ps3: 'playstation 3', ps4: 'playstation 4',
  ps5: 'playstation 5', psp: 'playstation portable', vita: 'playstation vita', switch: 'nintendo switch',
  ns: 'nintendo switch', sw2: 'nintendo switch 2', ns2: 'nintendo switch 2', xsx: 'xbox series', xss: 'xbox series',
  xb1: 'xbox one', xone: 'xbox one', x360: 'xbox 360', pc: 'pc', win: 'windows', mac: 'mac', '3ds': 'nintendo 3ds',
  wiiu: 'wii u', gba: 'game boy advance', gb: 'game boy', snes: 'super nintendo', n64: 'nintendo 64',
};

// parseSearch splits "zelda $rpg @switch" into text and visible filter
// chips. Quotes group words: $"couch co-op" @"switch 2".
export function parseSearch(input) {
  const tags = [];
  const platforms = [];
  const words = [];
  const re = /([$@])"([^"]*)"?|([$@])(\S+)|(\S+)/g;
  let m;
  while ((m = re.exec(String(input || '')))) {
    const sigil = m[1] || m[3];
    const value = (m[2] ?? m[4] ?? m[5] ?? '').trim();
    if (!value) continue;
    if (sigil === '$') tags.push(value);
    else if (sigil === '@') platforms.push(value);
    else words.push(value);
  }
  return { text: words.join(' '), tags, platforms };
}

function quote(v) {
  return /\s/.test(v) ? `"${v.replace(/"/g, '')}"` : v;
}

// formatSearch is parseSearch's inverse.
export function formatSearch({ text = '', tags = [], platforms = [] }) {
  return [text, ...tags.map(t => '$' + quote(t)), ...platforms.map(p => '@' + quote(p))].filter(Boolean).join(' ');
}

function platformMatches(names, wanted) {
  const w = normalize(wanted);
  const alias = PLATFORM_ALIASES[w.replace(/\s+/g, '')];
  return names.some(n => {
    const nn = normalize(n);
    return nn.includes(w) || (alias && nn.includes(alias));
  });
}

// matches reports whether entry passes the parsed search and shelf.
export function matches(entry, search, shelf = '') {
  if (shelf && entry.status !== shelf) return false;
  for (const t of search.tags || []) {
    const tl = t.toLowerCase();
    if (!(entry.tags || []).some(x => x.toLowerCase() === tl)) return false;
  }
  for (const p of search.platforms || []) {
    if (!platformMatches([...(entry.platforms || []), ...(entry.gamePlatforms || [])], p)) return false;
  }
  const text = normalize(search.text);
  if (!text) return true;
  const hay = entry._hay ??= normalize([entry.name, entry.notes, ...(entry.tags || []), ...(entry.platforms || [])].join(' '));
  return text.split(' ').every(tok => hay.includes(tok));
}

const cmpName = (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base', numeric: true });
const newest = (a, b) => (b.updated - a.updated) || (b.id - a.id);
const zeroLast = (v, dir) => (v ? v * dir : Infinity);

export const SORTS = [
  { value: 'updated', label: 'Recently updated' },
  { value: 'added', label: 'Date added' },
  { value: 'name', label: 'Title' },
  { value: 'rating', label: 'Your rating' },
  { value: 'hours', label: 'Hours played' },
  { value: 'release_new', label: 'Release date' },
  { value: 'ttb', label: 'Time to beat' },
];

const SORT_FNS = {
  updated: newest,
  added: (a, b) => (b.added - a.added) || newest(a, b),
  added_old: (a, b) => (a.added - b.added) || newest(a, b),
  name: (a, b) => cmpName(a, b) || newest(a, b),
  name_desc: (a, b) => cmpName(b, a) || newest(a, b),
  rating: (a, b) => (b.rating - a.rating) || newest(a, b),
  rating_low: (a, b) => (zeroLast(a.rating, 1) - zeroLast(b.rating, 1)) || newest(a, b),
  hours: (a, b) => (b.minutes - a.minutes) || newest(a, b),
  hours_low: (a, b) => (a.minutes - b.minutes) || newest(a, b),
  release_new: (a, b) => (zeroLast(a.release, -1) - zeroLast(b.release, -1)) || newest(a, b),
  release_old: (a, b) => (zeroLast(a.release, 1) - zeroLast(b.release, 1)) || newest(a, b),
  ttb: (a, b) => (zeroLast(a.ttb, 1) - zeroLast(b.ttb, 1)) || newest(a, b),
  ttb_long: (a, b) => (zeroLast(a.ttb, -1) - zeroLast(b.ttb, -1)) || newest(a, b),
};

// query filters and sorts entries. With search text, name-prefix matches
// come first (what you typed is most likely the title).
export function query(entries, { search = { text: '', tags: [], platforms: [] }, shelf = '', sort = 'updated' } = {}) {
  const out = entries.filter(e => matches(e, search, shelf));
  const fn = SORT_FNS[sort] || newest;
  const text = normalize(search.text);
  if (text) {
    const rank = e => (normalize(e.name).startsWith(text) ? 0 : normalize(e.name).includes(text) ? 1 : 2);
    out.sort((a, b) => (rank(a) - rank(b)) || fn(a, b));
  } else {
    out.sort(fn);
  }
  return out;
}

// shelfCounts counts entries per status (plus '' for all) under a search.
export function shelfCounts(entries, search) {
  const counts = { '': 0 };
  for (const e of entries) {
    if (!matches(e, search, '')) continue;
    counts[''] += 1;
    counts[e.status] = (counts[e.status] || 0) + 1;
  }
  return counts;
}

// tagSuggestions lists tags in use (most used first) starting with prefix.
export function tagSuggestions(entries, prefix = '', limit = 8) {
  const counts = new Map();
  for (const e of entries) {
    for (const t of e.tags || []) {
      const k = t.toLowerCase();
      const cur = counts.get(k);
      counts.set(k, { tag: cur?.tag ?? t, n: (cur?.n ?? 0) + 1 });
    }
  }
  const p = prefix.toLowerCase();
  return [...counts.values()].filter(x => x.tag.toLowerCase().startsWith(p))
    .sort((a, b) => b.n - a.n || a.tag.localeCompare(b.tag)).slice(0, limit).map(x => x.tag);
}

export function platformSuggestions(entries, prefix = '', limit = 8) {
  const counts = new Map();
  for (const e of entries) {
    for (const p of [...(e.platforms || []), ...(e.gamePlatforms || [])]) counts.set(p, (counts.get(p) || 0) + 1);
  }
  const pre = normalize(prefix);
  return [...counts.entries()].filter(([p]) => !pre || normalize(p).includes(pre) || PLATFORM_ALIASES[pre] && normalize(p).includes(PLATFORM_ALIASES[pre]))
    .sort((a, b) => b[1] - a[1]).slice(0, limit).map(([p]) => p);
}
