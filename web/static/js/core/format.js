// format.js — the single source for how statuses, ratings, play time and
// dates read. Pure functions; no DOM (tested under node --test).

export const STATUSES = [
  { value: 'wishlist', label: 'Want', verb: 'Want to play', icon: 'bookmark' },
  { value: 'backlog', label: 'Backlog', verb: 'Backlog', icon: 'layers' },
  { value: 'playing', label: 'Playing', verb: 'Playing', icon: 'play' },
  { value: 'paused', label: 'Paused', verb: 'Paused', icon: 'pause' },
  { value: 'completed', label: 'Finished', verb: 'Finished', icon: 'circle-check' },
  { value: 'abandoned', label: 'Dropped', verb: 'Dropped', icon: 'x' },
];

const byValue = new Map(STATUSES.map(s => [s.value, s]));

export function statusLabel(status) {
  return byValue.get(status)?.label ?? 'Not in library';
}

export function isStatus(v) {
  return byValue.has(v);
}

// Shelves are the Library's filter chips: All plus every status.
export const SHELVES = [{ value: '', label: 'All' }, ...STATUSES];

export const COMPLETIONS = [
  { value: 'main', label: 'Main story' },
  { value: 'main_extras', label: 'Main + extras' },
  { value: 'completionist', label: '100%' },
  { value: 'endless', label: 'No ending' },
];

export function completionLabel(v) {
  if (v === 'dropped') return 'Dropped';
  return COMPLETIONS.find(c => c.value === v)?.label ?? '';
}

// Ratings are stored 0–100 (0 = unrated). Stars show value ÷ 20 rounded to
// the nearest half; new star ratings are stored as multiples of 10.
export function toStars(rating) {
  if (!rating || rating <= 0) return 0;
  return Math.min(5, Math.max(0.5, Math.round(rating / 10) / 2));
}

export function fromStars(stars) {
  const s = Math.min(5, Math.max(0, Math.round(stars * 2) / 2));
  return Math.round(s * 20);
}

// formatRating renders a rating for the user's display preference:
// stars ("4.5 ★"), ten ("9/10") or hundred ("90").
export function formatRating(rating, display = 'stars') {
  if (!rating) return '';
  switch (display) {
    case 'ten': {
      const v = rating / 10;
      return `${Number.isInteger(v) ? v : v.toFixed(1)}/10`;
    }
    case 'hundred':
      return String(rating);
    default:
      return `${toStars(rating)} ★`;
  }
}

export function ratingAria(rating, display = 'stars') {
  if (!rating) return 'Not rated';
  if (display === 'ten') return `Rated ${formatRating(rating, 'ten')}`;
  if (display === 'hundred') return `Rated ${rating} out of 100`;
  const s = toStars(rating);
  return `Rated ${s} out of 5 stars`;
}

// formatMinutes: "45m", "1h", "1h 30m", "12h", "1,204h".
export function formatMinutes(minutes) {
  const m = Math.max(0, Math.round(minutes || 0));
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  const rest = m % 60;
  if (h >= 10 || rest === 0) return `${h.toLocaleString('en-US')}h`;
  return `${h}h ${rest}m`;
}

export function formatHoursLong(minutes) {
  const m = Math.max(0, Math.round(minutes || 0));
  if (m < 60) return `${m} minute${m === 1 ? '' : 's'}`;
  const h = m / 60;
  const v = h >= 10 ? Math.round(h) : Math.round(h * 10) / 10;
  return `${v.toLocaleString('en-US')} hour${v === 1 ? '' : 's'}`;
}

export function plural(n, one, many = one + 's') {
  return `${n.toLocaleString('en-US')} ${n === 1 ? one : many}`;
}

// localDay is the user's calendar day (YYYY-MM-DD) in their timezone.
export function localDay(d = new Date()) {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  return `${y}-${m}-${day}`;
}

function parseDay(day) {
  const [y, m, d] = String(day).slice(0, 10).split('-').map(Number);
  return new Date(y, (m || 1) - 1, d || 1);
}

export function addDays(day, n) {
  const d = parseDay(day);
  d.setDate(d.getDate() + n);
  return localDay(d);
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const MONTHS_LONG = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];

export function monthName(index, long = false) {
  return (long ? MONTHS_LONG : MONTHS)[index] ?? '';
}

// formatDay: "Oct 14, 2026" (the year is dropped for this year).
export function formatDay(day, today = localDay()) {
  if (!day) return '';
  const d = parseDay(day);
  const sameYear = String(day).slice(0, 4) === today.slice(0, 4);
  return `${MONTHS[d.getMonth()]} ${d.getDate()}${sameYear ? '' : `, ${d.getFullYear()}`}`;
}

// relativeDay: "Today", "Yesterday", "3 days ago", else formatDay.
export function relativeDay(day, today = localDay()) {
  if (!day) return '';
  const diff = Math.round((parseDay(today) - parseDay(day)) / 86400000);
  if (diff === 0) return 'Today';
  if (diff === 1) return 'Yesterday';
  if (diff > 1 && diff < 7) return `${diff} days ago`;
  return formatDay(day, today);
}

// timestampDay converts an RFC 3339 or SQLite timestamp to a local day.
export function timestampDay(ts) {
  if (!ts) return '';
  const d = new Date(String(ts).replace(' ', 'T') + (/[zZ]|[+-]\d\d:?\d\d$/.test(ts) || ts.includes('T') ? '' : 'Z'));
  return Number.isNaN(d.getTime()) ? String(ts).slice(0, 10) : localDay(d);
}

export function releaseYear(unixSeconds) {
  if (!unixSeconds) return '';
  return String(new Date(unixSeconds * 1000).getUTCFullYear());
}

export function formatRelease(unixSeconds) {
  if (!unixSeconds) return 'TBA';
  const d = new Date(unixSeconds * 1000);
  return `${MONTHS[d.getUTCMonth()]} ${d.getUTCDate()}, ${d.getUTCFullYear()}`;
}

// formatElapsed: a running timer as "1:05:09" or "4:09".
export function formatElapsed(ms) {
  const total = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const mm = h ? String(m).padStart(2, '0') : String(m);
  return `${h ? h + ':' : ''}${mm}:${String(s).padStart(2, '0')}`;
}

export function initials(name) {
  const parts = String(name || '').trim().split(/\s+/).filter(Boolean);
  if (!parts.length) return '?';
  return (parts[0][0] + (parts[1]?.[0] ?? '')).toUpperCase();
}

// coverAlt describes a cover for screen readers.
export function coverAlt(name) {
  return name ? `Cover of ${name}` : '';
}
