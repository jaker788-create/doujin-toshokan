# Multi-Source Tagging — Continuation Roadmap

> Follow-on plan after `feat/multi-source-tagging` (commit `8ca95ea`), which landed
> the `source.Provider` abstraction, refactored nhentai into one implementation, added
> the MangaDex provider, and made the source selectable. This document tracks what was
> deliberately deferred, the decisions still open, and the improvements worth making —
> grounded in the code that actually shipped.

Effort key: **S** ≈ <½ day · **M** ≈ 1–2 days · **L** ≈ 3+ days.

> **Progress (feat/multi-source-tagging):** done — **1.3** (architecture docs), **3.2**
> (MangaDex 429 retry/backoff), **2.3 + 3.3** (provider-supplied `SearchResult.Language` fed
> into ranking), and **2.1** (folder-id prefix registry: nhentai + mangadex peel; the
> shortcut is gated on the active provider). **3.5 was attempted and reverted** — not the
> quick win it looked like; see the item. Still open: the providers (**1.1** E-Hentai /
> **1.2** Hitomi — now a one-line `sourceDefs` add each), multi-source strategy (**2.2**),
> and the rest of §3 — notably the query-struct refactor (**3.1**).

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

### 1.2 Hitomi.la provider — **S/M**
Scope: **ID + manual only**, same contract as E-Hentai (1.1). Hitomi has no official
API, but a stable de-facto JSON endpoint every third-party client uses:

```
GET https://ltn.gold-usergeneratedcontent.net/galleries/{id}.js
```

Returns `var galleryinfo = {...}` — strip the `var galleryinfo = ` prefix, parse the
rest as JSON. Fields map ~1:1 onto our `tag` subjects: `tags[]` (with `male`/`female`
attributes → plain tags), `artists[]`, `groups[]`, `parodys[]` (sic), `characters[]`,
`language`, `type` (doujinshi/manga/cg/imageset), `title`/`japanese_title`, and
`files[]` (→ page count for `autotag.qualifies`). The gallery id is the trailing
number in every gallery URL (`...-中文-4056725.html`).

- New `internal/hitomi/client.go` implementing `source.Provider`.
  - `GalleryByID("<id>")` → the `.js` endpoint above; ids are numeric.
  - `Search(...)` returns empty (best-effort contract, as with E-Hentai). Hitomi's
    search is client-side over binary `.nozomi`/index files — reverse-engineering it
    is **L** and not worth it; folder-id shortcut + manual apply cover the use case.
- **No auth** — no token, no account system. Send a browser-ish `User-Agent` and
  `Referer: https://hitomi.la/` (required by the image CDN; harmless on metadata).
- Thumbnail: `files[].hash`-derived CDN URLs churn with the site's URL-shuffling
  scripts — acceptable to ship with **no thumbnail** first and add later if stable.
- **Churn risk:** the data domain already moved once (`ltn.hitomi.la` →
  `ltn.gold-usergeneratedcontent.net`, 2025-03) and hitomi shuffles endpoints
  periodically. Keep the base URL in `SourceConfig` (overridable) rather than a
  constant, so a domain move is a settings edit, not a release.
- Folder-id prefix: `hitomi-<id>` via the prefix registry (decision 2.1).

**Depends on:** decision 2.1 (prefix generalization). Simpler than E-Hentai — no
cookies/secrets needed, so it can land before 1.1 and exercise the ID-only provider
shape first.

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
sourceRef)` pair. Registered: **nhentai** (`leadingDigits`) and **mangadex** (`leadingUUID`,
canonical 8-4-4-4-12). The `MatchSource`/`runAutoTag` shortcut fires only when
`mi.sourceSlug == run.slug` (the active provider), so a mismatched folder falls through to
fuzzy instead of a doomed cross-provider `GalleryByID`.
- **Adding a source's shortcut** is a one-line `sourceDefs` row: **hitomi** reuses
  `leadingDigits`; **ehentai** (`ehentai-<gid>-<token>`) needs a new `gid-token` matcher (read
  `<digits>-<alnum>` → normalize to `gid/token`).
- Left for **2.2**: a folder whose slug ≠ the active source is *not* routed to its own
  provider (we only query the active one) — it falls to fuzzy. That cross-provider routing is
  the multi-source strategy question below.

### 2.2 Single active source vs. multi-source strategy
Today exactly one source is active. Real libraries mix doujin (nhentai/e-h) and
mainstream (MangaDex).
- **Option A:** keep single-active (simplest; user switches per sweep).
- **Option B (recommended):** per-sweep *ordered fallback* — try source 1, fall back to
  source 2 on "no match". `gatherCandidates` would loop providers; `source_slug` already
  records which one won.
- **Option C:** merge across sources in one match (most complex; tag provenance + dedup
  across sites gets hard). Probably not worth it.

### 2.3 MangaDex language + page-count handling
MangaDex series have no single page count, and `candLangResolver` only reads language
from *title decorations* (nhentai convention) — so MangaDex results never set
`LangMatch` and never get the page bonus.
- **Decision:** should the neutral `SearchResult` carry a provider-supplied `Language`
  field (populated by MangaDex from `originalLanguage`) that `ScoreAll` prefers over the
  title-decoration heuristic? Cleaner ranking for MangaDex; small change to `source` +
  `autotag.ScoreAll` + `candLangResolver`. **Recommended.**
- Content-rating filter (`contentRatings` in mangadex/client.go) is hardcoded to include
  adult content. Expose as a per-source setting? (Probably fine hardcoded for this app.)

### 2.4 E-Hentai cookie auth in the UI
`SourceConfig.Secrets` exists but the Settings picker only handles an API key. E-Hentai
needs two cookies.
- **Decision:** generic key/value secret fields per source, or a bespoke two-field
  E-Hentai form? A small generic "secrets" editor keyed off a provider-declared schema
  scales better as sources grow.

### 2.5 Source provenance in the library UI
`manga.source_slug` is stored but not shown. Worth a small "tagged from MangaDex" badge
on the detail page + a filter ("show untagged / tagged-by-X")? Cheap, and useful once
more than one source is in play.

---

## 3. Improvements & tech debt (ranked)

1. **Structured query instead of string syntax — M.** `gatherCandidates`/`searchRequests`
   emit nhentai-flavored `artist:"x" title:"y" language:z` strings; MangaDex's
   `parseQuery` reverse-engineers them. Replacing the `Search(query string)` contract
   with a `SearchQuery{Title, Artist, Language}` struct removes the leak and makes every
   provider's search precise. This is the single biggest design cleanup.
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
docs update (1.3) ─────────────────────────────► ship anytime, no deps
query-struct refactor (3.1) ──┬─► precise search for all providers
                              └─► unblocks cleaner E-Hentai + MangaDex
prefix registry (2.1) ────────► folder-id shortcut for e-h/mangadex/hitomi
Hitomi provider (1.2) ────────► needs 2.1 only (no auth) — first ID-only provider
E-Hentai provider (1.1) ──────► needs 2.1 + 2.4; reuses the ID-only shape from 1.2
multi-source fallback (2.2) ──► after ≥2 useful providers exist
```

Lowest-risk, highest-value first pass: **1.3 (docs) + 3.2 (MangaDex retry) + 3.3/2.3
(language ranking) + 3.5 (drop CDN reconstruction)** — all Small, all independent, and
they make the two shipped providers noticeably better before adding a third.
