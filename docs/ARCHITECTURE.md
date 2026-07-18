# Doujin Toshokan — Architecture

> A map of how Doujin Toshokan is put together and, more importantly, the **load-bearing
> invariants** that future changes must not break. If you're about to add a
> feature, skim "Invariants" first — most bugs in a project like this come from
> quietly violating one of them.

Doujin Toshokan is a single-user, offline, **native desktop app** that indexes a manga
collection **in place** and presents it as a searchable library and a scrollable
reader. It never moves, renames, or deletes your files.

**Stack:** Go 1.25+, Wails v2 (renders an embedded TypeScript frontend in the OS
WebView2 window), SQLite via `modernc.org/sqlite` (pure Go, no cgo),
`disintegration/imaging` for thumbnails, TypeScript + Vite frontend (no framework).

---

## How the pieces fit

```
build/bin/doujin.exe  (single static binary)
├── main.go        Wails window + //go:embed frontend/dist + Bind(app) + AssetServer.Handler
├── app.go         App struct — exported methods = the typed frontend API
├── assets.go      http.Handler for /image and /thumb (path-guarded binary serving)
└── internal/      config · store(db) · scanner · ingest · search · tag · thumbs · paths
        │ WebView2
        ▼
   frontend/src/main.ts  ── bound methods (typed JSON) ──▶  app.go
                         ── <img src="/image|/thumb?path="> ─▶  assets.go
```

The frontend reaches the backend two ways, and **only** these two: **bound
methods** (Wails turns each exported `App` method into a typed TS function and
marshals JSON both directions) and the **asset handler** for binary image bytes
that `<img>` needs by URL. Don't invent a third channel.

---

## Load-bearing invariants

These are the rules the whole design leans on. Each one has a "why" so you can
tell when it's safe to bend.

### 1. Index in place — files are read-only

The app **only ever reads** the library on disk. It never writes, moves, renames,
or deletes a file under a library root. All organization (which titles exist,
their authors, tags, cover, page count) lives in SQLite; the files are untouched.
*Why:* a database problem can never cost you data — at worst you re-scan. Any
feature that needs to modify files breaks this guarantee and should be discussed
before building.

### 2. Two sources of truth, cleanly split

- **Filesystem = source of truth for content.** The list of pages in a title is
  **not stored** — it is derived at view time by listing image files in the folder
  and natural-sorting them (`scanner.ListPages` + `naturalLess`, so `2.png` sorts
  before `10.png`). `page_count` in the DB is only a *cached* number, refreshed on
  rescan.
- **DB = source of truth for organization.** Authors, titles, tags, tag links,
  `cover_rel_path`, and the cached `page_count` live in SQLite.

*Why:* you can drop, add, or reorder image files on disk and a rescan picks it up.

### 3. `search.SearchManga` is the single read chokepoint

Every browse / filter / search / paginate path goes through `search.SearchManga`
(`internal/search/search.go`). It composes optional query, author id, tag ids,
sort, limit, and offset into one parameterized query. *Why:* pagination, sorting,
and filtering were built once; every caller inherits them. When adding a new way
to slice the library, **extend this function** rather than writing a parallel
query.

Three details inside it matter:
- **Sort is allow-listed, never interpolated.** The `sorts` map keys a handful of
  known sorts to SQL fragments; an unknown sort falls back to `m.title`. User
  input is never concatenated into the ORDER BY.
- **Multi-tag filter is AND, not OR.** `GROUP BY m.id HAVING
  COUNT(DISTINCT mt.tag_id) = len(tagIDs)` means a title must carry *all* requested
  tags to match.
- **`SourceSlug` carries a sentinel.** It filters on the `manga.source_slug`
  provenance column, where `""` means any source, a slug matches exactly, and
  `search.SourceNone` (`"none"`) means *never auto-tagged* — NULL **or** empty
  string, because migration 007 can leave either. The sentinel shares a field with
  real provider slugs, so `providers_test.go` pins that no preset ever registers it;
  `internal/search` is a leaf that cannot import the registry to check itself.

### 4. File-serving must pass the path-traversal guard

`/image` and `/thumb` (in `assets.go`) take an OS filesystem path as a query
param. Both call `paths.IsWithinRoots` (`internal/paths/paths.go`), which
canonicalizes the path (abs + clean, following symlinks when present) and
confirms it lives under a configured library root
**before any file access**. *Why:* without it, `?path=../../secrets` would serve
arbitrary files. **Any new code that serves a file by path must call this guard
first.** (Windows note in the source: the comparison is case-sensitive; in
practice paths come from the stored `folder_path` so casing matches.)

### 5. Thumbnails: on-demand, disk-cached, atomic, with fallback

`thumbs.GetThumbnail` (`internal/thumbs/thumbs.go`) generates a JPEG on first
request and caches it. The cache key hashes **path + mtime + size + width**, so
editing or replacing a source image naturally invalidates its thumbnail. Writes go
to a temp file then `os.Rename` (atomic), so an interrupted write can't leave a
corrupt file that later reads as a cache hit. Unreadable/corrupt or
unsupported-format sources (e.g. AVIF, which pure-Go decoders may not handle)
return a **placeholder** instead of erroring — the WebView2 still renders the full
image via `/image`. Images are never upscaled.

### 6. Duplicate ingest is prevented by `folder_path UNIQUE`

`manga.folder_path` is `UNIQUE`. Re-ingesting the same folder returns a UNIQUE
constraint error, which `App.Ingest` / `App.ImportAll` detect via
`isUniqueViolation` and skip silently. *Why:* scan + bulk-import + drop-in-a-new-
folder are the same flow and can overlap; the constraint makes re-runs idempotent.

### 7. Missing folders are flagged, never deleted

If a title's folder disappears, `App.Rescan` sets `missing=1` rather than removing
the row. *Why:* a temporarily unmounted drive shouldn't erase your tags/metadata.

### 8. Schema changes go through the migration ladder

Never edit the live schema in place and never edit an existing migration. See
"Evolving the schema" below.

### 9. One SQLite connection — never read and write at once

`store.Open` sets `db.SetMaxOpenConns(1)` (SQLite is single-writer; one connection
keeps PRAGMAs and write ordering simple). **Consequence:** you cannot iterate an
open `*sql.Rows` and issue an `Exec` on the same DB — both need the one connection
and would deadlock. Read all rows into a slice and close the cursor *before*
writing. `App.Rescan` does exactly this; follow the pattern.

### 10. Tag fetching goes through `source.Provider`

Every path that copies tags from an online source — the interactive `MatchSource`,
the bulk `StartAutoTag` sweep, the manual apply — fetches through the
`source.Provider` interface (`internal/source`) and scores **neutral** types:
`source.SearchResult` / `GalleryDetail` and `tag.Typed`, never a site's JSON. Each
site's quirks (nhentai's int ids + search syntax, mangadex's UUIDs + relationship
authors) are mapped to the neutral model *inside* its client (`internal/nhentai`,
`internal/mangadex`); the matcher (`internal/autotag`) and the apply path stay
site-agnostic. *Why:* adding a source is implementing one interface, not editing the
matcher.

Searching is structured for the same reason: callers pass a `source.SearchQuery`
(`{Title, Artist, Language, Page}`) describing *what* they want, and each provider
renders it into its own wire format — **never a site's query syntax**. nhentai's
`artist:"x" title:"y" language:z` is built inside `internal/nhentai` and spoken
nowhere else; MangaDex maps the same struct onto real API filters (resolving the
artist to an author UUID). Before this seam existed the matcher emitted nhentai
syntax and every other provider had to reverse-engineer it, which silently reduced
MangaDex's artist searches to guaranteed zero-result queries.

Search is explicitly **best-effort**: a provider with no free-text search at all returns
an empty `SearchResponse` rather than an error, and stays fully useful through the
`<slug>-<id>` folder shortcut and manual apply. `internal/hitomi` is the reference case
(hitomi's own search is client-side over binary index files, so there is nothing to
query; `internal/ehentai` is the second — its API resolves galleries by id only). Such a
provider sets `providerPreset.IDOnly`, which reaches the Settings picker as
`SourceState.id_only` — a sweep that can only match id-bearing folders has to *say* so,
or its "no match" results read as a bug rather than the contract.

The *shape* of a gallery ref is likewise provider knowledge, carried as
`providerPreset.RefHint` → `SourceState.ref_hint`. It is not uniform — e-hentai needs a
gid **and** a token, so the UI cannot build its own `<slug>-<id>` example without
documenting a folder name that never matches. The hints are pinned to what
`internal/doujin`'s `sourceDefs` actually parses by a test, because the parser is a leaf
package that cannot import the registry and the two would otherwise drift in silence.

A sweep consults an ordered **chain** of providers, not one: a `<slug>-<id>` folder name
routes to that slug's provider even when another is active, and a title with no id walks
the enabled sources until one matches. The chain holds **one `autoTagRun` per provider**,
and that is load-bearing — a run's `searchCache` is keyed by `SearchQuery.CacheKey()` and
its `detailCache` by bare gallery id, both provider-scoped, so a shared cache would serve
one site's gallery for another's identical numeric id.

An **auto-apply never spans providers**: the first source to clear the bar wins outright
and its decision is applied whole. `gatherCandidates` dedupes by bare gallery id with no
provider namespace and `applyTags` stamps one slug per merge set, so a set drawn from two
sites would drop colliding ids and mis-record provenance.

A **review does pool**: every source that found candidates contributes to the shortlist,
grouped by provider in chain order — nothing is being applied yet, so hiding one site's
options would just withhold the answer. Groups are never interleaved by score, because
cross-provider scores are not comparable (MangaDex reports `NumPages: 0` for every series,
so its candidates never earn the page bonus). Provenance therefore rides **per candidate**
(`SourceCandidate.SourceSlug`), and applying resolves the ref against that provider rather
than the active source.

The `manga.source_slug` / `source_ref` link columns (migration 007) record
which provider a title's tags came from as a `(slug, string-id)` pair — the provider-
agnostic successor to the legacy `nhentai_gallery_id` they backfill from.

Provenance is **readable after the fact**, not only at match time: the title page shows
the source as a chip linking to the library filtered by it, and the library's source
picker is populated by `search.SourceCounts` — a facet over what the library actually
*holds*, deliberately not over the enabled sources. A title keeps its `source_slug`
after that source is disabled or removed from the registry, so a picker built from
config would silently offer no way to find those titles. For the same reason an
unregistered slug labels as itself (`providerLabel`'s fallback) rather than being
hidden. The slug→label mapping is resolved backend-side into `MangaDetail.SourceLabel`,
because the registry is `providers.go`'s and the frontend has no view of it.

---

## Module map

Backend packages under `internal/`. Each has one responsibility.

| Package | Responsibility |
|---------|----------------|
| `config` | Load/save `config.json`; resolve data dir (`%APPDATA%/doujin` via `os.UserConfigDir`), db path, thumb cache dir. |
| `store` | Connection (`modernc.org/sqlite`, FK on, single conn), schema, and the **migration ladder** (`migrations`, `Init`). Exposes a `Querier` interface satisfied by `*sql.DB` and `*sql.Tx`. |
| `scanner` | Walk library roots, detect un-imported title folders, list + natural-sort pages. Derives content from disk. |
| `thumbs` | `imaging` thumbnail generation + disk cache (atomic, placeholder fallback). |
| `ingest` | Create/link author, manga row, and tags. `NormalizeTag`, dedupe, transactional. `GetOrCreateTag(name, subject)` enriches a tag's subject (upgrade, **never downgrade**), so re-saving a typed tag by name keeps its subject. |
| `search` | The read chokepoint: `SearchManga`, suggestions, tag/author/manga lookups (incl. `GetMangaTagsTyped` for the subject-grouped detail view), and the `Manga`/`Author`/`Tag` row types. |
| `tag` | Leaf vocabulary package: the canonical tag **subjects** (`language`, `artist`, `group`, `parody`, `character`, `category`, `tag`, plus `General`) — the same set nhentai uses — with `Typed{Name,Type}`, `Normalize`, `Label`, `Rank`, `Sort`. Shared by the parser-mapping, ingest, search, and provider layers with no import cycle. |
| `autotag` | Pure, network-free matcher: scores a local title against a provider's neutral `SearchResult`s (cross-language title similarity, with page + language ranking) and decides auto-apply vs. review. Works identically for every provider. |
| `source` | Leaf provider-neutral **seam**: the `Provider` interface + neutral `SearchResult`/`GalleryDetail`/`SearchResponse` the matcher scores. IDs are strings; search is best-effort (a detail-only site may return nothing). See invariant 10. |
| `nhentai` / `mangadex` / `hitomi` / `ehentai` | The `source.Provider` implementations — rate-limited HTTP clients that look up galleries and map each site's JSON + tags onto the neutral types and the shared `tag.Subject` vocabulary. `hitomi` and `ehentai` are id-only (empty `Search`); `hitomi` parses a JS `var galleryinfo = {…}` document rather than JSON, and `ehentai` POSTs its `gdata` API and identifies a gallery by a `gid/token` **pair** rather than a single id. Add a source by adding a package here. |
| `paths` | `IsWithinRoots` path-traversal guard. |
| `stash` | Saved pages ("tabs"): CRUD over the `stash` table. An entry is a `hash` + `label` + `kind` (`search`\|`title`); title entries `LEFT JOIN manga`/`authors` for card display and own a `last_page` resume position (`ON DELETE CASCADE` with `manga`). Uses the `store.Querier` style. |

Root `main` package: `app.go` (bound methods — the thin API layer that
validates/clamps input and calls the packages above), `nhentai.go` (the tag-matching
bound methods that drive `internal/autotag` over a chain of `source.Provider`s — the name
predates both the chain and the multi-provider seam), `providers.go`
(the provider registry, chain construction + source-config bound methods), `assets.go` (binary file
handler), `main.go` (Wails wiring). Business logic lives in `internal/`, which keeps
it unit-testable without a running window.

---

## Request flows

**Browse / search.** The Library view calls `Search(SearchArgs)` for page 0, then
appends further pages (limit/offset) as a sentinel scrolls into view
(IntersectionObserver). `App.Search` resolves tag *names* to ids, clamps the
limit/offset, and calls `search.SearchManga`. Covers use `/thumb`.

**Read a title.** The Reader view calls `GetManga(id)`, which returns the manga
row, its on-disk page paths (derived via `scanner.ListPages`), its tags, and a
`missing` flag. Each page is an `<img>` pointing at `/image`; click for a lightbox.

**Edit tags.** The inline tag editor calls `UpdateTags(id, tags)` →
`ingest.SetMangaTags`, which *replaces* the title's tag set in one transaction
(delete its `manga_tags`, re-insert the normalized + de-duped list) and returns the
saved set so the UI re-renders its chips. DB-only; files untouched (invariant 1).
Unused tag rows are left in place on purpose — they keep autocomplete useful.

**Ingest.** The Scan view calls `GetUnimported()` (folders on disk not yet in the
DB). `Ingest(folder, tags)` imports one; `ImportAll()` bulk-imports everything
found. Library roots are managed in-app via `AddLibraryRoot` (native folder
dialog) / `RemoveLibraryRoot`.

---

## Evolving the schema (the migration ladder)

`internal/store/store.go` versions the database with `PRAGMA user_version`.
`migrations` is an ordered slice of `func(*sql.Tx) error`; the 1-based position of
each is the schema version it produces (`migrations[0]` → version 1). `Init` reads
the current `user_version`, runs every pending migration in order (each in its own
transaction), and stamps the new version. It is idempotent and safe on every
startup.

**To add or change a table/column:**

1. **Append** a new function to `migrations` — never edit or reorder an existing
   one (that corrupts the version history of databases already in the field).
2. Make it safe if re-applied after an interrupted run: prefer
   `CREATE ... IF NOT EXISTS`, and guard `ALTER TABLE ... ADD COLUMN` with a
   `PRAGMA table_info` existence check.
3. Add a test in `internal/store/store_test.go` (the existing
   `TestRunnerAppliesOnlyPendingMigrations` and `TestLegacyDBAtVersionZeroUpgrades`
   show the pattern; the latter confirms a pre-migration DB at `user_version 0`
   upgrades cleanly).

The schema is byte-for-byte compatible with any existing on-disk `doujin.db`, so
the app opens it with no migration needed.

---

## Adding a feature — quick checklist

- **New way to slice the library?** Extend `search.SearchManga`, don't write a
  parallel query. Keep `sort` allow-listed.
- **New schema?** Append a migration; add a `store_test.go` test.
- **New tag source (metadata site)?** Add an `internal/<site>` package implementing
  `source.Provider` and register it in `providers.go`; the matcher is untouched
  (invariant 10).
- **Serving a file by path?** Call `paths.IsWithinRoots` first.
- **New frontend↔backend call?** Add an exported method on `App` and rebuild
  (`wails build` / `wails generate module`) so the typed bindings regenerate. Don't
  add a second IPC mechanism.
- **Client-rendered HTML from JSON?** Escape it — `main.ts` has an `esc()` helper
  because client rendering has no template autoescaping.
- **Iterating rows then writing?** Read all rows first, close the cursor, then
  write (invariant 9).
- **Touching the filesystem?** Read only. Don't break index-in-place.

---

## Local development

```
wails dev            # run with hot reload (native window)
go test ./...        # backend tests (parity with the old pytest suite)
go vet ./...         # static checks
gofmt -w .           # format
wails build          # produce build/bin/doujin.exe (regenerates bindings + frontend)
```

Config and runtime data (`config.json`, `doujin.db`, `thumbs/`) live in
`%APPDATA%/doujin/`, never in the repo. Set `library_roots` in-app (Scan →
Add folder…) or by editing `config.json`.
