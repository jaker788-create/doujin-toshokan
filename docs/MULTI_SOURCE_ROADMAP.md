# Multi-Source Tagging — Continuation Roadmap

> Follow-on plan after `feat/multi-source-tagging` (commit `8ca95ea`), which landed
> the `source.Provider` abstraction, refactored nhentai into one implementation, added
> the MangaDex provider, and made the source selectable. This document tracks what was
> deliberately deferred, the decisions still open, and the improvements worth making —
> grounded in the code that actually shipped.

Effort key: **S** ≈ <½ day · **M** ≈ 1–2 days · **L** ≈ 3+ days.

> **Progress (feat/multi-source-tagging):** done — **1.3** (architecture docs), **3.2**
> (MangaDex 429 retry/backoff), **2.3 + 3.3** (provider-supplied `SearchResult.Language` fed
> into ranking), **2.1** (folder-id prefix registry: nhentai + mangadex peel; the
> shortcut is gated on the active provider), **3.1** (the query-struct refactor — the
> string search contract is gone), **1.2** (the Hitomi provider — the first ID-only
> source), and **2.2** (the provider chain: cross-provider id routing + ordered fallback,
> which also carried most of **2.5**). **3.5 was attempted and reverted** — not the quick
> win it looked like; see the item. Still open: **1.1** (E-Hentai, blocked on 2.4), the
> library half of **2.5**, and the rest of §3.

---

## Status — what already exists

- `internal/source` — neutral model (`SearchResult`/`GalleryDetail`, string ids,
  `tag.Typed` tags) + `Provider` interface.
- `internal/nhentai`, `internal/mangadex` — two providers.
- `providers.go` — registry (`providerPresets`), `buildProvider`, `activeProvider()`,
  and the `GetSources`/`SetSourceConfig`/`SetActiveSource` bound methods.
- `config.Config.Sources[]` + `ActiveSource`, legacy `nhentai_api_key` synth.
- migration 007 — `source_slug`/`source_ref` link columns.
- Frontend source picker (Scan page) + per-source labels.

---

## 1. Deferred deliverables

### 1.1 E-Hentai / ExHentai provider — **M**
Agreed scope: **ID + manual only** (no HTML search scraping). E-Hentai's `gmetadata`
API resolves galleries by id and returns `title`/`title_jpn`/`filecount` + namespaced
tags that map ~1:1 onto our `tag` subjects — but it has **no JSON free-text search**.

- New `internal/ehentai/client.go` implementing `source.Provider`.
  - `GalleryByID("<gid>/<token>")` → `POST /api.php` `{method:"gmetadata", gidlist:[[gid,token]]}`.
  - `Search(...)` returns an empty response (best-effort contract already allows this —
    see the `source` package doc). The title still tags via the folder-id shortcut and
    manual apply.
- Auth: session cookies (`ipb_member_id`, `ipb_pass_hash`) for ExHentai content — carry
  them in `config.SourceConfig.Secrets` (the field already exists for exactly this).
- Add to `providerPresets` with `NeedsKey:false` but a new "needs cookies" state (see
  decision 2.4).
- Folder-id prefix: generalize `doujin.sourcePrefix` (decision 2.1) so `ehentai-<gid>-<token>`
  routes here.

**Depends on:** decision 2.1 (prefix generalization) and 2.4 (cookie auth in the UI).

### 1.2 Hitomi.la provider — ✅ DONE
`internal/hitomi` implements `source.Provider` as the **first ID-only source**: `Search`
returns an empty `SearchResponse` without touching the network, and the site is reached
through the `hitomi-<id>` folder shortcut + manual apply. Metadata comes from the de-facto
endpoint every third-party client uses:

```
GET https://ltn.gold-usergeneratedcontent.net/galleries/{id}.js
```

which serves **JavaScript, not JSON** — `decodeGalleryInfo` strips the `var galleryinfo =`
assignment (leniently: any spacing, optional trailing `;`) and rejects anything else, so
hitomi's HTML 404 page can never decode into a blank success.

Mapping is close to 1:1 — `artists`/`groups`/`parodys` (sic)/`characters`/`language` land
on the matching subjects, `files[]` gives the page count, and `type`
(doujinshi/manga/cg/imageset/anime) becomes a **Category**, mirroring nhentai's own
"doujinshi" category tag. Hitomi's gender namespace on tags (`female:loli`) is **flattened
to the bare name**, because the local library's tags come from sites that do not namespace.

**Three things the live site does that a spec-reading implementation gets wrong** — all
found by probing real galleries, none visible against a fake server:

1. **`id` is a JSON number on old galleries and a string on new ones.** Both are live
   today (5000 → `5000`, 4056725 → `"4056725"`). A single-typed field fails to decode half
   the site; `flexID` handles both.
2. **`tags[].male`/`female` are typed just as inconsistently** (`1` vs `"1"`, absent on
   ungendered tags). We do not read them, so the fields are deliberately *absent* from the
   DTO — declaring them as `string` broke every pre-2015 gallery until a live run caught it.
3. **Some old ids are aliases.** `/galleries/900.js` serves the gallery whose own id is
   4646. The client prefers the id the *document* reports, normalizing an alias to the
   canonical gallery so the stamped `source_ref` is the durable one.

Also landed:
- **Configurable base URL** — `config.SourceConfig.BaseURL` (empty = the provider default).
  This is not speculative: the old data domain `ltn.hitomi.la` **no longer resolves at all**
  after the 2025-03 move, so the next move should be a settings edit, not a release.
- **`providerPreset.IDOnly` → `SourceState.id_only` → a note in the Settings picker.** An
  id-only source makes a bulk sweep report "no match" on every title without an id in its
  folder name; unlabelled, that reads as a broken app rather than the documented contract.
- Folder-id prefix: one `sourceDefs` row reusing `leadingDigits`, as predicted by 2.1.

**Not done (deliberate):** no thumbnail. Cover URLs are derived from `files[].hash` through
the site's own URL-shuffling script (`gg.js`), which churns independently of this endpoint —
shipping no thumbnail beats shipping a broken one. Search stays unimplemented: hitomi's is
client-side over binary `.nozomi` index files, which is **L** and not worth it.

### 1.3 Architecture docs — **S**
`docs/ARCHITECTURE.md` still describes the nhentai-only design. Update:
- Module map: add `source` (leaf, the provider seam) and the `<provider>` packages.
- A new invariant: *"Tag fetching goes through `source.Provider`; the matcher speaks
  neutral types — never a site's schema."*
- Note the `manga.source_slug`/`source_ref` link columns alongside the legacy
  `nhentai_gallery_id`.

---

## 2. Open decisions (need your call)

### 2.1 Folder-id prefix generalization — ✅ DONE (Option A)
`doujin.sourcePrefix` is now a per-provider registry (`sourceDefs` in `internal/doujin/parse.go`):
each entry is a `{slug, leadingRef func(string) string}` matcher; `Parsed.GalleryID int64`
became `SourceSlug`/`SourceRef string`, and `matchInput.galleryID` is now a `(sourceSlug,
sourceRef)` pair. Registered: **nhentai** (`leadingDigits`), **mangadex** (`leadingUUID`,
canonical 8-4-4-4-12) and **hitomi** (`leadingDigits`).
- **Adding a source's shortcut** is a one-line `sourceDefs` row — hitomi (1.2) landed as
  exactly that. **ehentai** (`ehentai-<gid>-<token>`) still needs a new `gid-token` matcher
  (read `<digits>-<alnum>` → normalize to `gid/token`).
- The leftover this item flagged — a folder whose slug ≠ the active source falling to fuzzy
  instead of being routed to its own provider — **is closed by 2.2's cross-provider id
  routing**. The shortcut now fires for any enabled provider named in a folder name, not
  just the active one.

### 2.2 Multi-source strategy — ✅ DONE (Option B: ordered fallback)
A sweep now consults a **provider chain** rather than only the active source, in two phases.

**Cross-provider id routing.** A `<slug>-<id>` folder name routes to that slug's provider
even when another source is active — the leftover 2.1 explicitly flagged. It is also
*cheaper* than what it replaced (one exact fetch instead of a doomed multi-query search),
and it is the only way an id-only provider can match at all.

**Ordered fuzzy fallback.** A title with no embedded id walks the enabled sources in
priority order (active first), advancing on anything short of an auto-apply. Opt-out per
sweep (`AutoTagOptions.Fallback`, checkbox default on): advancing on `review` rather than
only `none` costs a full pass per provider on every ambiguous title, so it needs an escape
hatch. An **id-only** source is skipped in the fuzzy phase — its `Search` is empty by
contract — while staying eligible for routing.

Two design points worth not re-litigating:

- **One `autoTagRun` per provider, never one run swapping clients.** A run's `searchCache`
  is keyed by `SearchQuery.CacheKey()` and its `detailCache` by bare gallery id — both
  provider-scoped. nhentai and hitomi both use numeric ids, so a shared cache would serve
  one site's gallery for another's id. `TestChainCachesDoNotCollideAcrossProviders` pins it.
- **Auto-applies never span providers; reviews pool.** A confident match ends the chain and
  is applied whole, because `gatherCandidates` dedupes by bare gallery id with no provider
  namespace and `applyTags` stamps one slug per merge set — a set drawn from two sites would
  drop colliding ids and mis-record provenance. A *review* is different: nothing is being
  applied yet, so every source that found candidates contributes to the shortlist
  (`pooledReviewCandidates`), grouped by provider in chain order and capped at
  `pooledReviewMax`. Groups are **never interleaved by score**: cross-provider scores are not
  comparable (MangaDex reports `NumPages: 0` for every series, so its candidates can never
  earn the page bonus), and a merged sort would bury them every time. Chain order is honest;
  a cross-provider ranking would be a fiction.
  Provenance therefore rides **per candidate** (`SourceCandidate.SourceSlug`), not just per
  result — a pooled list can hold the same numeric id from two sites, and applying must
  resolve it against the right one.

The active provider failing to build stays a **hard error** even with fallback on: sweeping
quietly with the others would hide the misconfiguration. A non-active source that fails to
build is skipped, so one unconfigured extra source cannot abort a sweep.

`MatchSource` walks the same chain, so the interactive path and a sweep cannot disagree
about which source wins a title.

**Deliberately not done: pipelining.** Running a fallback concurrently with the next title's
primary search is possible — the providers have independent limiters — but the payoff was
costed at ~2–3%. Wall clock is set by the tightest limiter (nhentai 3.3s/req) and that
serializes regardless of goroutine structure; fallbacks run on MangaDex (250ms/req) or
hitomi (**zero** requests). Against that: a thread-safe run cache with single-flight,
ordered progress emission, and `MaxOpenConns(1)` keeping applies serialized anyway. The loop
is structured so it stays possible if a real sweep ever proves slow.

### 2.3 MangaDex language + page-count handling — **language done, page count open**
The **language** half landed (with 3.3): `SearchResult.Language` is provider-supplied and
`candLangResolver` prefers it over the title-decoration heuristic.

The **page-count** half is still open, and its effects are wider than "ranks a bit worse".
A MangaDex series has chapters, not a single page count, so `NumPages` is always 0. Traced
through the scorer, that means:

| | Affected? |
|---|---|
| The displayed **title %** (`TitleScore`) | **No** — pure title similarity; page count never touches it |
| Ranking `Score` | Loses `pageBonus` (0.5, large next to a 0–1 title score). Harmless *within* MangaDex — every candidate lacks it equally — but it is why the pooled review groups by provider instead of interleaving by score |
| `qualifies` (auto vs review) | **Loses 2 of 4 routes.** `PagesClose && title≥0.6` and `ArtistMatch && PagesExact` both need `NumPages > 0`. Only artist+decent-title and near-perfect-title survive, so MangaDex needs a stronger signal to auto-apply |
| `confidentMatch` (`nhentai.go`) | **Never fires** — it gates on `c.PagesClose`. So every MangaDex title runs the *full* search budget plus a catalog page-through even after a perfect hit. MangaDex titles are the most expensive in a sweep |
| `pagesCloseTo` merge guard | Inert (returns true when either side ≤ 0), so the "don't merge a same-titled but differently-sized work" guard does nothing for MangaDex |

Fixed already: the UI rendered `0p`, which read as an empty gallery rather than an absent
signal; it now says "page count n/a".

**Open decision:** should `confidentMatch` be allowed to stop early on artist-match + strong
title when no page count exists? It would cut the request cost noticeably, at the price of
the page gate that currently stops the ladder from ending on a plausible-but-wrong title.
A chapter-count or first-chapter page count from MangaDex is *not* a substitute — it is not
the same quantity as a doujin's page count and would corroborate nothing.

- Content-rating filter (`contentRatings` in mangadex/client.go) is hardcoded to include
  adult content. Expose as a per-source setting? (Probably fine hardcoded for this app.)

### 2.4 E-Hentai cookie auth in the UI
`SourceConfig.Secrets` exists but the Settings picker only handles an API key. E-Hentai
needs two cookies.
- **Decision:** generic key/value secret fields per source, or a bespoke two-field
  E-Hentai form? A small generic "secrets" editor keyed off a provider-declared schema
  scales better as sources grow.

### 2.5 Source provenance in the library UI — **partly done**
Provenance now rides through the *matching* path, because 2.2 made it a correctness
requirement rather than a nicety: `MatchResult.SourceSlug`/`SourceLabel` record which
provider produced the candidates, the apply methods take that slug (a ref only means
something to the site that issued it — see the `fix:` that landed with 2.2), the match
picker names its source, and sweep progress lines are tagged with it.

**Still open:** the *library* side. `manga.source_slug` is stored but not shown on the
detail page, and there is no "show untagged / tagged-by-X" filter. Cheap, and more useful
now that one sweep really can produce a mixed library.

---

## 3. Improvements & tech debt (ranked)

1. **Structured query instead of string syntax — ✅ DONE.** `Provider.Search` now takes a
   `source.SearchQuery{Title, Artist, Language, Page}`; each provider renders its own wire
   format. nhentai's `buildQuery` is the only code that speaks its syntax, and MangaDex's
   `parseQuery` is deleted. nhentai's outbound queries are byte-for-byte unchanged.
   - **MangaDex matching genuinely improved**, and this was the leak's real cost: MangaDex
     filters by author with a **UUID** and 400s on a name, so the string contract's only
     option was folding the artist into `title=` — which returns **0 results** against the
     live API, since MangaDex titles never contain the author's name. `Search` now resolves
     the artist via a memoized `GET /author?name=` and filters with `authorOrArtist`.
   - Two silent-failure invariants are test-locked: `CacheKey` lowercases (two spellings of
     one title = one search, one budget slot), and `PageCacheKey`'s `#<page>` suffix is
     unconditional (so a one-page fetch can never be served back as a *complete* catalog and
     truncate a prolific artist to 25 works with no warning).
   - Also fixed en route: the language filter was sent as `availableTranslatedLanguages[]`
     (plural), which MangaDex rejects with a 400 — **every** language-narrowed MangaDex
     search had been failing outright. Landed as its own `fix:` commit.
   - **Known inconsistency, deliberately not fixed here:** `SearchResult.Language` comes from
     `originalLanguage` while the filter is `availableTranslatedLanguage[]`. Those are
     different notions of "language" — a Japanese work with an English scanlation matches the
     filter but ranks as `japanese`. Pre-existing; worth its own item.
2. **MangaDex retry/backoff — S.** nhentai's `do` honors 429 + `Retry-After`; MangaDex's
   `do` does not. Add the same retry loop (MangaDex returns 429 under load).
3. **Provider-supplied language into ranking — S.** See 2.3.
4. **Rename `nhSearcher` → `providerSearcher` — S.** The interface in `nhentai.go` is
   still nhentai-named though it's provider-generic; likewise rename the file
   `nhentai.go` → `tagging.go`.
5. **Drop the frontend's nhentai CDN reconstruction — ~~S~~ M.** `coverCandidates`/`wireCover`
   in `main.ts` rebuild `t.nhentai.net/...` from `media_id`. It *looks* like every provider
   supplies an absolute `thumbnail` (nhentai search + MangaDex do), so the fallback reads as
   nhentai-specific dead weight.
   - **⚠ NOT dead weight — attempted 2026-07-17, reverted.** `source.GalleryDetail` has no
     `Thumbnail` field, so **detail-fetched** candidates carry only `media_id`: the
     `nhentai-<id>` folder-id shortcut (`galleryIDCandidate`) and the detail-fetched top
     few build their cover *solely* from the reconstruction. Removing it blanked every
     nhentai preview cover. A real fix must supply the cover **server-side**: add
     `Thumbnail` to `GalleryDetail` and have nhentai's `GalleryByID` build the URL —
     parsing the cover *extension* from the v2 detail API's `images` object (`t = j/p/g/w`),
     which is the whole reason the frontend cascades over four extensions. Only then can the
     frontend fallback go. So this is **M**, not S, and gated on a `source` type change.
6. **Retire legacy `Settings.HasNhentaiKey`/`NhentaiUserAgent` — S.** Once the UI fully
   uses `GetSources`, these can go (keep the `config` legacy synth for old files).
7. **MangaDex id display — S (cosmetic).** The picker shows `#<gallery_id>`; a UUID reads
   badly. Hide the `#id` for non-numeric ids.
8. **End-to-end `MatchSource` test with a MangaDex fake — M.** Current tests cover the
   MangaDex client and the matcher separately; an integration test through
   `activeProvider()` → `gatherCandidates` → `Decide` would lock the wiring.
9. **Per-source rate-limit config — S.** Intervals are hardcoded constants per client;
   fine for now, but a `SourceConfig.RateLimitMs` would let power users tune.

---

## 4. Explicitly out of scope

- **Booru sites (Danbooru/Gelbooru/etc.)** — single-image model, not galleries; breaks the
  page-count matching half of `autotag.qualifies`. Would need a separate matching mode.
- **Write-back to sources** (favoriting, uploading) — violates the read-only/index-in-place
  invariant.

---

## Suggested ordering

```
docs update (1.3) ────────────► ✅ done
query-struct refactor (3.1) ──► ✅ done — providers now own their wire format
prefix registry (2.1) ────────► ✅ done
Hitomi provider (1.2) ────────► ✅ done — first ID-only provider; id_only surfaced in the UI
multi-source fallback (2.2) ──► ✅ done — provider chain; id routing + ordered fallback
E-Hentai provider (1.1) ──────► NEXT: needs 2.4 (cookie auth in the UI); reuses 1.2's shape
```

**Next up: 1.1 (E-Hentai) — but decide 2.4 first.** 1.2 proved the ID-only shape end to end
and 2.2 built the chain that makes a fourth provider worth having, so E-Hentai is now mostly
the same client with a different DTO: `providerPreset.IDOnly` and the id-routing phase both
apply to it unchanged, and `SourceConfig.Secrets` already exists for its session cookies.
What is genuinely new is **cookie auth in the Settings UI (2.4)** — a decision, not a
mechanical port, and worth settling before the client is written.

**The cheapest remaining win is the library half of 2.5.** A sweep can now genuinely produce
a mixed library, and `manga.source_slug` is still invisible on the detail page. The matching
path already carries provenance, so this is a display + filter change, not plumbing.

Remaining Small items, all independent: **3.4** (rename `nhSearcher` → `providerSearcher`,
`nhentai.go` → `tagging.go` — note `internal/mangadex/client.go`'s package doc already
refers to "the root tagging.go", and the file now holds the chain as well as the matcher, so
the name has drifted further), **3.6** (retire the legacy `Settings.HasNhentaiKey`),
**3.7** (hide `#id` for non-numeric ids).
