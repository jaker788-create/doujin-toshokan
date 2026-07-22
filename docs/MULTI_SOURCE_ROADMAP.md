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
> source), **2.2** (the provider chain: cross-provider id routing + ordered fallback,
> which also carried most of **2.5**), **1.1** (the E-Hentai provider — keyless, so
> **2.4 was never actually the gate** it was recorded as), and **2.5** (the library half —
> detail-page provenance chip + a faceted source filter through `SearchManga`).
> **3.5 was attempted and reverted** — not the quick win it looked like; see the item.
>
> **Progress (this branch):** **3.4** (the `nhSearcher` → `providerSearcher` rename +
> `nhentai.go` → `tagging.go`), **3.6** (the legacy `Settings.HasNhentaiKey`/
> `NhentaiUserAgent` fields retired — per-source key state + User-Agent now ride on
> `SourceState`/`GetSources`; the `config` legacy synth stays for old files), and the
> **2.3** page-count decision — `confidentMatch` may now stop early on an artist-confirmed
> strong title when a provider reports no page count. **2.4 is decided: no cookie auth** —
> the keyless `gdata` API is enough and cookies buy only ExHentai-exclusive galleries
> nobody has needed; `SourceConfig.Secrets` stays as the seam if that ever changes.
> **3.7** landed too (hide the `#id` for non-numeric ids in the match picker). Still open:
> **3.5** and **3.8** — the two remaining **M** items. **3.9** (per-source rate-limit config)
> also landed: `SourceConfig.RateLimitMs` overrides a client's request spacing.

---

## Status — what already exists

- `internal/source` — neutral model (`SearchResult`/`GalleryDetail`, string ids,
  `tag.Typed` tags) + `Provider` interface.
- `internal/nhentai`, `internal/mangadex`, `internal/hitomi`, `internal/ehentai` — four
  providers; the last two are ID-only (empty `Search` by contract).
- `providers.go` — registry (`providerPresets`, carrying `NeedsKey`/`IDOnly`/`RefHint`),
  `buildProvider`, `activeProvider()`, `chainProviders()`/`providerBySlug()`, and the
  `GetSources`/`SetSourceConfig`/`SetActiveSource` bound methods.
- `config.Config.Sources[]` + `ActiveSource`, legacy `nhentai_api_key` synth.
- migration 007 — `source_slug`/`source_ref` link columns.
- Frontend source picker (Scan page) + per-source labels.

---

## 1. Deferred deliverables

### 1.1 E-Hentai provider — ✅ DONE
`internal/ehentai` implements `source.Provider` as the **second ID-only source**, scoped
as agreed to **ID + manual only** (no HTML search scraping). Metadata comes from
E-Hentai's one public API:

```
POST https://api.e-hentai.org/api.php
{"method":"gdata","gidlist":[[618395,"0439fa3666"]],"namespace":1}
```

Mapping is close to 1:1 — the `artist`/`group`/`parody`/`character`/`language` namespaces
land on the matching subjects, the content namespaces (`male`/`female`/`mixed`/`other`)
flatten to the generic Tag subject the way hitomi's gender namespace does, `filecount`
gives the page count, and `category` ("Doujinshi", "Manga", …) becomes a Category.

**The 2.4 gate turned out not to apply.** This item was blocked on cookie auth; the API
answers unauthenticated. Cookies would only buy **ExHentai-exclusive** galleries, which is
a much narrower case than "E-Hentai needs auth" — so the provider ships keyless
(`NeedsKey:false`) and `SourceConfig.Secrets` stays unused until a genuinely missing
gallery justifies the UI work. See 2.4.

**Four things the live API does that a spec-reading implementation gets wrong** — all
found by probing it, none visible against a fake server built from the field names:

1. **The method is `gdata`.** `gmetadata` — what this document said, and the natural guess
   since it is the key the response object uses — answers
   `{"error":"Unsupported method provided"}`.
2. **`"namespace":1` is load-bearing.** Without it tags come back bare (`"touhou project"`)
   instead of namespaced (`"parody:touhou project"`), and the entire subject mapping
   silently collapses to untyped General tags while the response still looks healthy.
3. **Titles are HTML-escaped** (`aren&#039;t`, `&quot;`). Left as-is, that is the string
   compared against the local folder name, so every entity is lost match score.
4. **Errors ride inside a 200.** An unknown gallery or a wrong token returns
   `{"gmetadata":[{"gid":…,"error":"Key missing, or incorrect key provided."}]}` — a
   status-only check decodes a blank success and applies it.

**The ref is a pair, not an id.** A gallery is `(gid, token)`; the token is a capability,
and the right gid with the wrong token is refused. The neutral id is `"<gid>/<token>"`; the
folder form joins them with a dash because a slash cannot be in a filename. `GalleryByID`
accepts either and always returns the canonical slash form, so `manga.source_ref` is stable
regardless of which spelling arrived. The `sourceDefs` row therefore needed a real matcher
(`leadingGidToken`, gid + exactly 10 hex) rather than reusing `leadingDigits`.

**This forced one UI change.** The Settings `id_only` note built its example as
`<slug>-<id>`, which is simply wrong for a two-part ref — a user following it would name
folders `ehentai-618395` and get silent no-matches. Ref shape is provider knowledge, so
`providerPreset.RefHint` → `SourceState.ref_hint` now supplies it, tied by test to what
`internal/doujin`'s `sourceDefs` actually parses.

**Not done (deliberate):** no thumbnail, though the response carries an absolute `thumb`
URL and it would be useful — `source.GalleryDetail` has no `Thumbnail` field, and adding
one is 3.5's change, not something to smuggle in here. Uploader/rating/torrents/parent-gid
are not decoded: nothing consumes them, and each is a field that could change type under us
for no benefit.

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

### 2.3 MangaDex language + page-count handling — ✅ DONE
The **language** half landed (with 3.3): `SearchResult.Language` is provider-supplied and
`candLangResolver` prefers it over the title-decoration heuristic.

The **page-count** half is now resolved too (see the open-decision box below): the effects
are wider than "ranks a bit worse". A MangaDex series has chapters, not a single page count,
so `NumPages` is always 0. Traced through the scorer, that means:

| | Affected? |
|---|---|
| The displayed **title %** (`TitleScore`) | **No** — pure title similarity; page count never touches it |
| Ranking `Score` | Loses `pageBonus` (0.5, large next to a 0–1 title score). Harmless *within* MangaDex — every candidate lacks it equally — but it is why the pooled review groups by provider instead of interleaving by score |
| `qualifies` (auto vs review) | **Loses 2 of 4 routes.** `PagesClose && title≥0.6` and `ArtistMatch && PagesExact` both need `NumPages > 0`. Only artist+decent-title and near-perfect-title survive, so MangaDex needs a stronger signal to auto-apply |
| `confidentMatch` (`tagging.go`) | **Now fires on a no-page-count candidate** when the title is strong *and* the artist is confirmed (the resolved decision below). Before, it gated on `c.PagesClose` and so never fired for MangaDex — every MangaDex title ran the full search budget plus a catalog page-through even after a perfect hit. A known-but-far page count still blocks the early stop; only a genuinely absent count defers to the artist tag |
| `pagesCloseTo` merge guard | Inert (returns true when either side ≤ 0), so the "don't merge a same-titled but differently-sized work" guard does nothing for MangaDex |

Fixed already: the UI rendered `0p`, which read as an empty gallery rather than an absent
signal; it now says "page count n/a".

**Decided (this branch): yes.** `confidentMatch` now stops early on an artist-confirmed
strong title when the candidate reports **no** page count — cutting the per-title request
cost for the most expensive titles in a sweep. The safety trade was contained rather than
swallowed: the artist tag is *required* as the stand-in gate (a strong title alone, with
neither a page count nor an artist to corroborate it, still does not end the search), and a
*known-but-far* page count still blocks the stop — that is a real size disagreement, not a
missing signal. A chapter-count or first-chapter page count from MangaDex was rejected as a
substitute: it is not the same quantity as a doujin's page count and would corroborate
nothing. `TestConfidentMatchNoPageCount` pins the three cases.

- Content-rating filter (`contentRatings` in mangadex/client.go) is hardcoded to include
  adult content. Expose as a per-source setting? (Probably fine hardcoded for this app.)

### 2.4 E-Hentai cookie auth in the UI — ❌ WON'T DO (decided this branch)
This was recorded as blocking 1.1 on the premise that E-Hentai needs auth. It does not:
`api.e-hentai.org`'s `gdata` method answers unauthenticated, and 1.1 shipped keyless.

What cookies (`ipb_member_id`, `ipb_pass_hash`) would actually buy is **ExHentai-exclusive
and expunged galleries** — a real but narrow gap, and one no user has hit. **Decision: don't
build it.** The keyless API covers the case this app has, and standing up either UI shape (a
generic provider-declared secrets editor or a bespoke two-field E-Hentai form) is real work
for a hypothetical user. `SourceConfig.Secrets` stays as the wiring seam so this is a
UI-only revival if it ever earns its place.

**Trigger to revisit:** a folder whose `ehentai-<gid>-<token>` ref returns
"Gallery not found" while the gallery plainly exists on ExHentai. Until then this stays
closed; when it reopens, the generic secrets editor is the better shape — `providerPreset`
has since grown `NeedsKey`, `IDOnly` and `RefHint`, so a provider-declared field schema is
what that registry is already trending toward.

### 2.5 Source provenance in the library UI — ✅ DONE
Provenance rides through the *matching* path, because 2.2 made it a correctness
requirement rather than a nicety: `MatchResult.SourceSlug`/`SourceLabel` record which
provider produced the candidates, the apply methods take that slug (a ref only means
something to the site that issued it — see the `fix:` that landed with 2.2), the match
picker names its source, and sweep progress lines are tagged with it.

The **library half** now landed too, as a display + filter change with no new plumbing:

- **Detail page chip.** The byline reads `by <author> · <n> pages · [nhentai]`, and the
  chip links to `#/?source=<slug>` — provenance is a way *in*, not just a label. The ref
  rides in the tooltip rather than inline, which sidesteps 3.7 entirely: a UUID or
  e-hentai's `618395/0439fa3666` reads badly next to a page count, and its shape is
  per-provider knowledge.
- **Library filter.** `SearchParams.SourceSlug` extends the `SearchManga` chokepoint
  (invariant 3) — no parallel query — and reaches the UI as a picker beside "Sort by",
  populated by the new `GetSourceFacets` bound method with counts.

Three decisions worth not re-litigating:

- **The facet list comes from the library, not the registry.** A title keeps its
  `source_slug` after that source is disabled or removed, so options built from the
  enabled sources would silently offer no way to find those titles. An unregistered slug
  therefore labels as *itself* rather than being dropped. The picker is omitted entirely
  when there is only one bucket and no active filter — on a never-swept library the
  control could only ever say "Untagged".
- **"Untagged" is a sentinel in the same field as a real slug** (`search.SourceNone`,
  `"none"`), because it has to survive a URL round-trip as one value. That is a
  collision risk by construction, so `providers_test.go` pins that no preset registers
  it. Untagged matches NULL **or** empty string: migration 007 can leave either, and
  splitting the two spellings would hide those rows from every filter value at once.
- **Labels resolve backend-side** into `MangaDetail.SourceLabel`. `providerLabel` is
  `providers.go`'s and the reader view has no source list of its own; sending the raw
  slug would have meant a second registry in TypeScript.

Verified against the live library (695 titles): facets came back nhentai 667 / mangadex 2
/ untagged 26, summing exactly to the total, with each filter value returning precisely
its faceted count.

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
4. **Rename `nhSearcher` → `providerSearcher` — ✅ DONE.** The interface is renamed (it was
   nhentai-named though provider-generic), and the file `nhentai.go` → `tagging.go`, which
   `internal/nhentai/client.go`'s package doc already pointed at. The companion test file
   keeps its `nhentai_test.go` name — it holds broader app tests (ingest, remove-missing,
   delete) beyond the tagging surface, so renaming it would overstate the move.
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
6. **Retire legacy `Settings.HasNhentaiKey`/`NhentaiUserAgent` — ✅ DONE.** Both fields are
   gone from the `Settings` DTO. The UI reads key presence from `SourceState.HasKey` and the
   per-source User-Agent from `SourceState.UserAgent` (both via `GetSources`) — the key-save
   handler pulls the active source's UA from there instead of the retired field. The
   `config`-level legacy synth (`ResolveSources` from `NhentaiAPIKey`/`NhentaiUserAgent`)
   stays, so old `config.json` files still work. The masking test moved to
   `TestGetSourcesMasksKey`.
7. **MangaDex id display — ✅ DONE.** The match-picker candidate card gated its `· #<id>`
   metadata segment on a purely-numeric id, so only nhentai's short gallery numbers show it;
   a MangaDex UUID or e-hentai's `gid/token` ref is dropped (it was noise next to the
   metadata and the title button already opens the gallery). The titleless-candidate
   fallback label was gated the same way — "gallery #123" for a numeric id, bare "gallery"
   otherwise — so no raw UUID leaks there either.
8. **End-to-end `MatchSource` test with a MangaDex fake — M.** Current tests cover the
   MangaDex client and the matcher separately; an integration test through
   `activeProvider()` → `gatherCandidates` → `Decide` would lock the wiring.
9. **Per-source rate-limit config — ✅ DONE.** `config.SourceConfig.RateLimitMs` (0 = the
   provider default) now overrides a client's request spacing. Each client grew a
   `SetRateLimit`/`RateLimit` pair over its existing single-limiter throttle, and
   `buildProvider` applies the override through a small `rateLimited` interface every client
   satisfies — `TestBuildProviderAppliesRateLimit` fails a future provider that forgets it
   (the setting would otherwise be silently ignored). A non-positive value is guarded so a
   stray config can't collapse the spacing to nothing. Left as config.json-only:
   power-user tuning is the framing, and `SetSourceConfig` already preserves the field
   in place across a UI key-save, so no picker knob was added.

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
E-Hentai provider (1.1) ──────► ✅ done — keyless; 2.4 was not the gate it looked like
library provenance (2.5) ─────► ✅ done — detail chip + faceted source filter
rename + retire legacy (3.4/3.6) ► ✅ done — providerSearcher/tagging.go; Settings slimmed
confident-match early stop (2.3) ► ✅ done — no-page-count titles stop on artist + strong title
e-hentai cookies (2.4) ───────► ❌ won't do — keyless API is enough
id display (3.7) ─────────────► ✅ done — non-numeric ids hidden in the match picker
rate-limit config (3.9) ──────► ✅ done — SourceConfig.RateLimitMs overrides the throttle
```

**Next up: the two remaining §3 items**, both **M** and independent: **3.5** (drop the
frontend nhentai CDN reconstruction — gated on adding `Thumbnail` to `source.GalleryDetail`)
and **3.8** (an end-to-end `MatchSource` test with a MangaDex fake). Every **S** item is now
done, and with 2.3 and 2.4 resolved, no open decisions remain.

**Verified in the GUI (2026-07-18).** Every provider was probed live end to end (folder
name → parser → API → mapped tags) *and* driven through the real UI, closing the gap this
section previously flagged: the sweep loop, the provider chain, the pooled review card and
the per-source chips all behave as intended against a real library.

Remaining: the two **M** items — **3.5** (drop the frontend CDN reconstruction, gated on a
`source.GalleryDetail.Thumbnail` field) and **3.8** (an end-to-end `MatchSource` test with a
MangaDex fake). **3.4**, **3.6**, **3.7** and **3.9** landed this branch; every **S** item
in §3 is now done.
