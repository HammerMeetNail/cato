// Pure-module tests for the frontend core (node --test, no dependencies).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fmt from '../../web/static/js/core/format.js';
import { parse, href, tab } from '../../web/static/js/core/router.js';
import { parseSearch, formatSearch, matches, query, shelfCounts, normalize, tagSuggestions, platformSuggestions } from '../../web/static/js/core/filter.js';

test('status labels follow the new vocabulary', () => {
  assert.equal(fmt.statusLabel('wishlist'), 'Want');
  assert.equal(fmt.statusLabel('completed'), 'Finished');
  assert.equal(fmt.statusLabel('abandoned'), 'Dropped');
  assert.equal(fmt.statusLabel('paused'), 'Paused');
  assert.equal(fmt.statusLabel(''), 'Not in library');
  assert.deepEqual(fmt.SHELVES.map(s => s.value), ['', 'wishlist', 'backlog', 'playing', 'paused', 'completed', 'abandoned']);
});

test('ratings convert between 0-100 and half stars', () => {
  assert.equal(fmt.toStars(0), 0);
  assert.equal(fmt.toStars(90), 4.5);
  assert.equal(fmt.toStars(100), 5);
  assert.equal(fmt.toStars(87), 4.5);
  assert.equal(fmt.toStars(83), 4);
  assert.equal(fmt.toStars(1), 0.5, 'any rating shows at least half a star');
  assert.equal(fmt.fromStars(4.5), 90);
  assert.equal(fmt.fromStars(3), 60);
  assert.equal(fmt.fromStars(7), 100);
  assert.equal(fmt.formatRating(85, 'ten'), '8.5/10');
  assert.equal(fmt.formatRating(80, 'ten'), '8/10');
  assert.equal(fmt.formatRating(73, 'hundred'), '73');
  assert.equal(fmt.formatRating(70, 'stars'), '3.5 ★');
  assert.equal(fmt.formatRating(0, 'stars'), '');
  assert.equal(fmt.ratingAria(90), 'Rated 4.5 out of 5 stars');
});

test('play time and dates', () => {
  assert.equal(fmt.formatMinutes(0), '0m');
  assert.equal(fmt.formatMinutes(45), '45m');
  assert.equal(fmt.formatMinutes(60), '1h');
  assert.equal(fmt.formatMinutes(90), '1h 30m');
  assert.equal(fmt.formatMinutes(38 * 60 + 20), '38h');
  assert.equal(fmt.formatMinutes(72240), '1,204h');
  assert.equal(fmt.formatHoursLong(90), '1.5 hours');
  assert.equal(fmt.addDays('2026-03-01', -1), '2026-02-28');
  assert.equal(fmt.relativeDay('2026-10-14', '2026-10-14'), 'Today');
  assert.equal(fmt.relativeDay('2026-10-13', '2026-10-14'), 'Yesterday');
  assert.equal(fmt.relativeDay('2026-10-10', '2026-10-14'), '4 days ago');
  assert.equal(fmt.relativeDay('2026-09-01', '2026-10-14'), 'Sep 1');
  assert.equal(fmt.formatDay('2024-02-29', '2026-10-14'), 'Feb 29, 2024');
  assert.equal(fmt.formatElapsed(65000), '1:05');
  assert.equal(fmt.formatElapsed(3909000), '1:05:09');
  assert.equal(fmt.releaseYear(1600000000), '2020');
  assert.equal(fmt.initials('Ada Lovelace'), 'AL');
});

test('router parses routes, filters and legacy hashes', () => {
  assert.deepEqual(parse(''), { name: 'home', query: {}, redirect: null });
  assert.equal(parse('#library?shelf=backlog&q=zel').query.shelf, 'backlog');
  assert.equal(parse('#library?shelf=backlog&q=zel').query.q, 'zel');
  assert.deepEqual(parse('#game/42'), { name: 'game', id: 42, query: {}, redirect: null });
  assert.equal(parse('#game/-7').id, -7, 'custom entries use negative IDs');
  assert.equal(parse('#now').name, 'home');
  assert.equal(parse('#now').redirect, '#home');
  assert.equal(parse('#stats').name, 'you/stats');
  assert.equal(parse('#settings').redirect, '#you/settings');
  assert.deepEqual(parse('#backlog').query, { shelf: 'backlog' });
  assert.equal(parse('#search').focusSearch, true);
  assert.equal(parse('#search/100%').query.q, '100%', 'a malformed escape keeps the raw text');
  assert.equal(parse('#u/alex').id, 'alex');
  assert.equal(parse('#you/year/2025').id, 2025);
  assert.equal(parse('#nope').redirect, '#home');
  assert.equal(href('library', { shelf: 'backlog', q: '', sort: 'name' }), '#library?shelf=backlog&sort=name');
  assert.equal(href('game', {}, 5), '#game/5');
  assert.equal(tab('game'), 'library', 'a game opened directly sits under Library');
  assert.equal(tab('game', 'home'), 'home', 'a game opened from Home keeps Home lit');
  assert.equal(tab('profile', 'home'), 'home');
  assert.equal(tab('list'), 'you');
  assert.equal(tab('library', 'home'), 'library', 'a tab is always its own');
  assert.equal(tab('you/stats'), 'you');
  assert.equal(tab('discover/search'), 'discover');
});

const entries = [
  { id: 1, name: 'The Legend of Zelda: Tears of the Kingdom', status: 'playing', rating: 100, minutes: 3000, updated: 50, added: 10, release: 1683849600, ttb: 3300, platforms: ['Nintendo Switch'], tags: ['RPG', 'open world'], notes: 'with Sam', gamePlatforms: ['Nintendo Switch'] },
  { id: 2, name: 'Celeste', status: 'completed', rating: 90, minutes: 900, updated: 40, added: 20, release: 1516000000, ttb: 480, platforms: ['PC (Microsoft Windows)'], tags: ['platformer'], notes: '', gamePlatforms: ['PC (Microsoft Windows)', 'PlayStation 4'] },
  { id: 3, name: 'Pokémon Legends: Z-A', status: 'wishlist', rating: 0, minutes: 0, updated: 60, added: 30, release: 0, ttb: 0, platforms: [], tags: [], notes: '', gamePlatforms: ['Nintendo Switch 2'] },
  { id: 4, name: 'Hades', status: 'backlog', rating: 0, minutes: 120, updated: 30, added: 40, release: 1600000000, ttb: 1300, platforms: ['PlayStation 5'], tags: ['roguelike', 'RPG'], notes: '', gamePlatforms: [] },
];

test('search syntax becomes visible filters and round-trips', () => {
  assert.deepEqual(parseSearch('zelda $rpg @"switch 2" $"open world"'), { text: 'zelda', tags: ['rpg', 'open world'], platforms: ['switch 2'] });
  assert.equal(formatSearch({ text: 'zelda', tags: ['open world'], platforms: ['ps5'] }), 'zelda $"open world" @ps5');
  assert.equal(normalize('Pokémon: Legends Z-A'), 'pokemon legends z a');
});

test('filtering and sorting the local index', () => {
  const ids = r => r.map(e => e.id).join(',');
  assert.equal(ids(query(entries)), '3,1,2,4');
  assert.equal(ids(query(entries, { search: parseSearch('pokemon') })), '3', 'accents fold');
  assert.equal(ids(query(entries, { search: parseSearch('$rpg') })), '1,4', 'tags are case-insensitive');
  assert.equal(ids(query(entries, { search: parseSearch('$rpg $roguelike') })), '4', 'tags combine with AND');
  assert.equal(ids(query(entries, { search: parseSearch('@ps5') })), '4', 'platform nicknames');
  assert.equal(ids(query(entries, { search: parseSearch('@sw2') })), '3');
  assert.equal(ids(query(entries, { search: parseSearch('sam') })), '1', 'notes are searchable');
  assert.equal(ids(query(entries, { shelf: 'playing' })), '1');
  assert.equal(ids(query(entries, { sort: 'name' })), '2,4,3,1');
  assert.equal(ids(query(entries, { sort: 'rating' })), '1,2,3,4');
  assert.equal(ids(query(entries, { sort: 'release_new' })), '1,4,2,3', 'unknown release dates last');
  assert.equal(ids(query(entries, { sort: 'ttb' })), '2,4,1,3');
  assert.equal(ids(query(entries, { search: parseSearch('h') })), '4,1', 'name prefix first');
  assert.deepEqual(shelfCounts(entries, parseSearch('')), { '': 4, playing: 1, completed: 1, wishlist: 1, backlog: 1 });
  assert.deepEqual(tagSuggestions(entries, 'r'), ['RPG', 'roguelike']);
  assert.ok(matches(entries[0], parseSearch('tears kingdom')));
  assert.equal(platformSuggestions(entries, 'switch')[0], 'Nintendo Switch');
});
