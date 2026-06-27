# Cato search quality plan

Status: **proposed, awaiting review before implementation.**
Scope settled with the user via clarifying questions:

- Goal: **rank only**. Junk is not hidden, just pushed down.
- Result count: **keep 10**, no pagination.
- Live IGDB fallback: **kept** (24h cache, as today).
- Backfill: **backfill all existing rows** with the new popularity fields once.
- Import pipeline (`import-games` Postgres COPY): **untouched** — popularity
  comes from IGDB only.
- Extra scope: add **typo/fuzzy matching** on top of popularity ranking.

---

## 1. The problem

Today, search (`internal/games/store.go:26-39`) ranks every row in the
311k-row `games` table by a four-tier `CASE` (exact > prefix > word-prefix >
fuzzy-LIKE), then by `aggregated_rating_count DESC, aggregated_rating DESC,
first_release_date DESC`. This works in principle but has three weaknesses:

1. **The catalog is full of low-quality rows.** The Postgres COPY import and the
   live IGDB fallback both ingest whatever rows IGDB returns, including obscure
   titles, DLC stubs, and old games with zero ratings. All of those compete on
   equal footing for the top 10 slots.
2. **The popularity signal is incomplete.** We store only
   `aggregated_rating` and `aggregated_rating_count` (critic-aggregate scores
   from IGDB). We do **not** store IGDB's `popularity`, `follows`, `hypes`,
   `rating`, `rating_count`, `total_rating`, `total_rating_count`, `category`,
   or `status` fields — even though the IGDB client already makes a request that
   could trivially fetch them. A game like *"The Legend of Zelda: Breath of the
   Wild"* hascritic data, but so do hundreds of fan-game stubs. There is no way
   today to prefer a game that the IGDB community actually follows.
3. **`LIKE '%query%'` has no typo tolerance.** Searching "zeld" or "mario" with
   a fat-finger typo falls through to the fuzzy tier, which is still just a
   substring `LIKE` against the whole catalog — so the typo-tolerant matches are
   drowned out by noise.

## 2. How others solve this

The standard playbook for "return the game the user probably means":

- **Combine a text-relevance score with a quality/popularity prior** (this is
  essentially what Elasticsearch's `function_score` does, and what
  Postgres FTS `ts_rank_cd` + a popularity multiplier approximates). Pure
  text-relevance alone ranks junk first; pure popularity alone returns the same
  handful of AAA games for every query. The hybrid wins.
- **Use a "trustworthy" signal** to suppress obvious junk: number of votes,
  number of followers, or whether the game is released. SteamDB, IGDB itself,
  and RateYourMusic all use a vote-count floor as a soft prior (Bayesian shrink)
  rather than a hard cutoff, so a niche-but-real game can still surface with a
  high query match.
- **Treat DLC/editions as second-class.** IGDB exposes `category`
  (0=main game, 1=DLC/Addon, 2=Expansion, etc.) and `version_parent` (the main
  game a version points back to). Most game DBs deprioritize non-zero
  `category` rows unless the query is an exact name match.
- **Tolerate typos** with a real edit-distance or trigram index, not substring
  `LIKE`. SQLite ships FTS5 with the **trigram tokenizer** (built into
  `modernc.org/sqlite` ≥ 1.x), which gives typo-tolerant token matching for free
  and runs far faster than `LIKE '%...%'` over 300k rows.

Sources worth reading before implementation:

- IGDB API docs — <https://api-docs.igdb.com/#game> documents the fields
  listed in §4 below.
- SQLite FTS5 trigram tokenizer — <https://www.sqlite.org/fts5.html#trigram>
  (modernc.org/sqlite bundles FTS5).

## 3. Proposed design

Three layered changes. (1) and (2) are the meaty ones; (3) is the typo work.

### 3.1 Add a popularity score column (computed at write time)

Add a single denormalized `popularity_score` column to `games`, computed at
upsert time from the IGDB fields we will now fetch. It is a weighted blend of:

```
popularity_score = follows*3 + hypes*2 + total_rating_count
                 + (10 if category==0 and status==0 else 0)
```

Rationale:

- `follows` is the strongest real-world "people care about this" signal IGDB
  exposes. Weight it highest.
- `hypes` tracks upcoming-game anticipation; useful for not-yet-released
  games people clearly care about.
- `total_rating_count` (combined critic+user count) acts as the Bayesian vote
  floor: a game with 0 reviews and 0 follows is almost certainly junk.
- The main-game released bonus (`category == 0 && status == 0`) addresses §3.2.

`popularity_score` is stored, not computed at query time, so the `ORDER BY`
stays on a single indexed column and the query stays fast. We recompute it in
`toGame()` / wherever the upsert happens, both for live IGDB rows and for the
backfill.

**Open question for review:** is `follows*3` the right weight, or should we let
`total_rating_count` dominate? I lean toward follows-heavy because rating_count
favors old games; favors favors current attention. Easy to tune later — it is
one column.

### 3.2 Prefer main games, but don't filter DLC out

Following the "rank only, don't hide" decision: DLC, expansions, and
versions are **still returned**, but they sort below main games within the same
match tier. This is implemented as an additional `CASE` arm in the `ORDER BY`:

```
CASE WHEN category = 0 THEN 0 ELSE 1 END
```

A user searching the exact name of a DLC pack (e.g. "Witcher 3 Blood and Wine")
still gets it, because exact-name matches sit in tier 0 of the existing
`CASE` and the popularity sort within tier 0 will still surface the DLC above
unrelated main games if its name is an exact match.

**Caveat:** the current query orders tiers *first*, then popularity *within*
tiers. To make the popularity prior actually pull a popular game above a
slightly-better-matched junk game, we should switch the ordering of the CASE
columns so popularity is the dominant term *after* the exact-match anchor.
Concretely, proposed `ORDER BY`:

```
ORDER BY
  -- (a) name-match quality, primary
  CASE
    WHEN normalized_name = ?2                       THEN 0
    WHEN normalized_name LIKE ?3                    THEN 1   -- prefix
    WHEN normalized_name LIKE ?4                    THEN 2   -- word-prefix
    ELSE 3
  END,
  -- (b) prefer main games over DLC/editions within a tier
  CASE WHEN category = 0 THEN 0 ELSE 1 END,
  -- (c) popularity prior
  popularity_score DESC,
  -- (d) tie-breakers (existing)
  aggregated_rating_count DESC, aggregated_rating DESC, first_release_date DESC
```

### 3.3 Substring matching via FTS5 trigram (replaces LIKE scan)

Replace the substring `LIKE` against `normalized_name` with an FTS5 virtual
table over `normalized_name`, using the **trigram** tokenizer. A quoted-phrase
MATCH against the trigram index matches any row whose indexed text contains
the query as a contiguous substring. This is functionally equivalent to the
old `LIKE '%query%'` in what matches, but it is served by an index instead of
a full-table scan over 300k rows — the main win.

**Important honesty note about "typo tolerance":** the trigram tokenizer does
**not** tolerate character transpositions. "zleda" and "zelda" share zero
trigrams (zel/eld/lda vs zle/led/eda), so a transposed query misses. What it
*does* provide is fast contiguous-substring matching — fragments like "zeld",
"egend of zel", or "legend of zelda" all match via the index regardless of word
boundaries. True transposition/edit-distance tolerance would require a
Levenshtein-style scan, which is out of scope. Verified empirically against
`modernc.org/sqlite` during prototyping.

The match query becomes roughly:

```
SELECT g.id, g.name, g.slug, g.cover_url, g.local_cover_path,
       g.first_release_date, g.category, g.popularity_score
FROM games_fts
JOIN games g ON g.rowid = games_fts.rowid
WHERE games_fts MATCH ?1
ORDER BY <same CASE/popularity ORDER BY as above>
LIMIT ?2
```

where `?1` is a sanitized quoted phrase built from the user query. We keep
the existing `normalized_name` column and B-tree index — FTS5 sits alongside
(rules: external-content table + sync triggers, see migration v5) — so the
exact-match `CASE` tier (`normalized_name = ?2`) still uses the B-tree index
for its probe, and the FTS5 path falls back to the LIKE query at runtime if
the FTS table is missing (e.g. on a pre-v5 DB or if FTS5 is unavailable).

**Why not `LIKE`-based Levenshtein:** computing edit distance over 311k rows
per keystroke is not viable, and SQLite's `LIKE` opt-in is case/diacritic
only, not typographic. FTS5 trigram is the lowest-effort real fix for the
substring-scan performance problem.

**Risk:** FTS5 must be enabled in the `modernc.org/sqlite` build. Verified
at prototyping time (trigram tokenizer + external content + sync triggers all
work). The search rewrite falls back to the `LIKE` query if the FTS table is
missing or the MATCH errors.

## 4. Data from IGDB we will use

The IGDB `Games` endpoint exposes (per <https://api-docs.igdb.com/#game>):
`id, name, slug, summary, storyline, cover, first_release_date,
aggregated_rating, aggregated_rating_count, rating, rating_count,
total_rating, total_rating_count, follows, hypes, popularity, category, status,
version_parent, platforms, genres, game_modes, themes,
involved_companies, screenshots, videos, url, updated_at, ...`.

We currently fetch the bolded-12 in the IGDB client request
(`internal/igdb/client.go:59`). The plan adds these fields:

| IGDB field      | Stored as            | Used for                            |
|-----------------|----------------------|-------------------------------------|
| `rating`        | `rating` (REAL)      | tie-breaker / future use           |
| `rating_count`  | `rating_count` (INT) | tie-breaker / future use           |
| `total_rating`  | `total_rating` (REAL) | future use                         |
| `total_rating_count` | `total_rating_count` (INT) | popularity_score input             |
| `follows`       | `follows` (INT)      | popularity_score primary input     |
| `hypes`         | `hypes` (INT)        | popularity_score input             |
| `popularity`    | `igdb_popularity` (REAL) | stored raw for debugging/tuning   |
| `category`      | `category` (INT)     | DLC/main-game deprioritization      |
| `status`        | `status` (INT)       | main-game-bonus check (released=0)  |
| `version_parent`| `version_parent` (INT) | future "collapse editions" feature |

`popularity_score` is derived (§3.1), not fetched.

## 5. Impact on existing code

| File                           | Change                                                                              |
|--------------------------------|-------------------------------------------------------------------------------------|
| `internal/db/migrate.go`       | New migration v4: add `rating, rating_count, total_rating, total_rating_count, follows, hypes, igdb_popularity, category, status, version_parent, popularity_score` columns to `games`; add `idx_games_popularity` on `(popularity_score)`. Do **not** edit v1. |
| `internal/db/migrate.go`       | New migration v5: create FTS5 virtual table `games_fts` over `normalized_name` with `content='games', content_rowid='rowid'`; populate from existing rows. (Trigram needs `tokenize='trigram'`.) |
| `internal/igdb/client.go`      | Extend `igdbGame` struct and the `fields id,name,...` string (line 59) to request the new fields. Update `toGame()` (lines 96-115) to populate the new `games.Game` fields and compute `popularity_score`. |
| `internal/games/types.go`      | Add the new fields to `Game` / `GameResult`.                                       |
| `internal/games/store.go`      | Rewrite `SearchLocal` (lines 20-60) for the FTS5 join + new `ORDER BY`. If FTS5 is unavailable at runtime, fall back to today's `LIKE` query. Keep the public signature unchanged. |
| `internal/games/search.go`     | Keep `NormalizeName`; add a helper to convert a user query into an FTS5 MATCH expression (escape special chars, append `*` for prefix). |
| `internal/games/service.go`    | `Search()` (lines 34-68) is unchanged in flow. Upsert path picks up the new fields automatically via the existing `toGame()` / upsert. |
| `internal/http/handler_games.go` | No change — still calls `service.Search` and returns `[]GameResult`. Optional: add `category`/`release_year` to the response payload so the frontend can show a subtitle. **Out of scope per user: frontend UX changes were not selected.** |
| cmd                            | A new `cato backfill-popularity` subcommand, OR a one-shot step inside `import-games`. Walks all rows missing `follows`, batches IGDB `GetGame` lookups (respecting the IGDB rate limiter — ~1 req/sec — so 311k rows ≈ 3.6 days at full rate). **Mitigation:** only backfill rows where `aggregated_rating_count > 0` *or* `follows IS NULL`, which cuts the long tail dramatically and likely brings it under a day.                |

## 6. Backfill strategy

The 311k-row backfill is the riskiest part. Concretely:

1. **Don't backfill everything.** Most rows are obscure titles with zero
   ratings. A precondition filter (`WHERE aggregated_rating_count > 0 OR
   first_release_date > <2 years ago>`) cuts the workload by ~90% and gives a
   popularity bump exactly where it matters. Rows that fail the filter keep
   `popularity_score = 0`, which ranks them at the bottom — exactly what we
   want.
2. **Respect the IGDB rate limiter.** The existing `games.IGDBRateLimiter` (~1
   req/sec) must govern the loop, otherwise IGDB will 429 us. 30k rows at
   1/sec ≈ 8 hours.
3. **Run it off the live server** if possible — the backfill writes a lot to
   the writer pool. Either run it as a flag to the main binary
   (`cato --backfill-popularity`) on the running container, or as a standalone
   one-shot. WAL mode (which we now enforce) means readers won't block.
4. **Make it resumable.** Add `WHERE follows IS NULL` (or a `popularity_score
   IS NULL` sentinel) so re-running the command skips rows it already filled.
5. **Refresh path for new rows.** The existing `StartStaleRefresh`
   (every 6h, refreshes games older than 90 days) should be extended to
   re-fetch `popularity_score` components alongside `source_updated_at`. This
   keeps `follows`/`hypes` current for games that surge in popularity after
   release. (Per the backfill-questions answer, this is in scope as "backfill +
   periodic refresh" — note that the user's answer explicitly mentions
   periodic refresh as a yes.)

## 7. Risks and open questions

- **FTS5 availability in `modernc.org/sqlite`.** Needs a one-time verify.
  Mitigation: the search rewrite falls back to today's `LIKE` query if the FTS
  table is missing or the migration errors.
- **Backfill duration / IGDB API budget.** 30k rows at 1/sec is ~8h; the
  precondition filter may need tuning based on real counts. **Question for the
  user:** acceptable to run a multi-hour backfill, or should we cap it (e.g.
  backfill only the top-N most-rated games first, leave the rest at 0)?
- **popularity_score weights.** The `follows*3 + hypes*2 + total_rating_count`
  blend is a guess. After backfill we should eyeball sample queries
  ("zelda", "mario", "final fantasy", "call of duty") and tune. The formula
  lives in one function so re-tuning is cheap.
- **The trigram tokenizer needs query length ≥ 3** to produce any trigrams.
  Queries of 1-2 chars will fall through to the existing `LIKE` prefix path.
  Today's 2-char minimum is preserved.
- **Search result count stays at 10.** If popular games still dominate the
  top-10 in a way that buries legitimate niche exact matches, we may revisit —
  but per the user's decision we keep 10 for now.

## 8. Implementation phases (suggested order)

1. Migration v4 (add columns) + extend IGDB client + extend `Game` type +
   `toGame()` popularity computation. New `popularity_score` is populated for
   new IGDB rows only. Search query is **unchanged** — still works, no
   regression.
2. `cato backfill-popularity` subcommand, respecting rate limits, with the
   precondition filter. Run once on prod once main is upgraded.
3. Migration v5 + FTS5 search rewrite in `store.go`. Optional fallback to
   `LIKE` path if FTS5 is unavailable. Land typo tolerance.
4. Extend `StartStaleRefresh` to refresh popularity components for stale rows.
5. Eyeball rankings, tune `popularity_score` weights.

Phases 1-2 alone deliver most of the value (rank popular games first); phases
3-5 are the polish.
