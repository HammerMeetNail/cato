// Timestamp helpers for library dates.
//
// The DB contains mixed formats (FINDINGS §2.2):
//   "2026-08-21 02:11:10"            — SQLite CURRENT_TIMESTAMP, UTC, no zone
//   "2024-01-04 22:13:59.054264"     — same with microseconds
//   "2026-08-20T22:11:10-04:00"      — RFC3339, server-local offset
//   "2026-08-01T00:00:00Z"           — RFC3339 UTC (new writes)
//
// new Date() alone is wrong for the space-separated forms: they're
// non-standard and Safari returns Invalid Date; and without a zone JS treats
// them as *local* time when they are actually UTC.

const SQLITE_RE = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|[+-]\d{2}:?\d{2})?$/;

// parseDBDate returns a Date or null. Space-separated values without an
// explicit zone are interpreted as UTC (that's what SQLite CURRENT_TIMESTAMP
// stores).
export function parseDBDate(s) {
  if (!s) return null;
  const m = SQLITE_RE.exec(s);
  if (m) {
    const zone = m[7];
    if (!zone || zone === 'Z') {
      const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]));
      return isNaN(d) ? null : d;
    }
    // RFC3339-style offset with a space instead of 'T' — normalize and parse.
    const d = new Date(`${m[1]}-${m[2]}-${m[3]}T${m[4]}:${m[5]}:${m[6]}${zone}`);
    return isNaN(d) ? null : d;
  }
  const d = new Date(s);
  return isNaN(d) ? null : d;
}

// formatDate renders a stored date as e.g. "Aug 21, 2026", or '' if unset.
export function formatDate(s) {
  const d = parseDBDate(s);
  if (!d) return '';
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

// formatYear renders just the release/year value from a DB timestamp.
export function formatYear(s) {
  const d = parseDBDate(s);
  return d ? String(d.getFullYear()) : '';
}

// Release date helpers — games store first_release_date as a unix timestamp
// (seconds, 0 = unknown/TBA), not a DB string.

export function formatReleaseDate(unixSeconds) {
  if (!unixSeconds) return '';
  const d = new Date(unixSeconds * 1000);
  if (isNaN(d.getTime())) return '';
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

export function isUpcomingRelease(unixSeconds) {
  if (!unixSeconds) return false;
  const d = new Date(unixSeconds * 1000);
  if (isNaN(d.getTime())) return false;
  return d.getTime() > Date.now();
}

export function formatReleaseYear(unixSeconds) {
  if (!unixSeconds) return '';
  const d = new Date(unixSeconds * 1000);
  if (isNaN(d.getTime())) return '';
  return String(d.getFullYear());
}

// releaseLabel returns a compact human label for search results: the full
// date for dated games, "TBA" for unknown, with "(upcoming)" suffix when the
// date is in the future so scheduled releases are obvious at a glance.
export function releaseLabel(unixSeconds) {
  if (!unixSeconds) return 'TBA';
  const dateStr = formatReleaseDate(unixSeconds);
  if (!dateStr) return 'TBA';
  return isUpcomingRelease(unixSeconds) ? `${dateStr} · Upcoming` : dateStr;
}

// releaseStatus returns "upcoming" / "released" / "tba" for styling hooks.
export function releaseStatus(unixSeconds) {
  if (!unixSeconds) return 'tba';
  return isUpcomingRelease(unixSeconds) ? 'upcoming' : 'released';
}

// modalReleaseLabel is the form used in the edit modal header — just the
// date (or TBA), with "· Upcoming" for future releases. "Released" is
// implied for past dates, so no prefix is needed.
export function modalReleaseLabel(unixSeconds) {
  if (!unixSeconds) return 'TBA';
  const dateStr = formatReleaseDate(unixSeconds);
  if (!dateStr) return 'TBA';
  return isUpcomingRelease(unixSeconds) ? `${dateStr} · Upcoming` : dateStr;
}
