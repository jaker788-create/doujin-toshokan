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

> The original Python/FastAPI build is archived under `legacy/` and reads the same
> database; it is not the active codebase. See CLAUDE.md.

---

## How the pieces fit

```
build/bin/doujin.exe  (single static binary)
├── main.go        Wails window + //go:embed frontend/dist + Bind(app) + AssetServer.Handler
├── app.go         App struct — exported methods = the typed frontend API
├── assets.go      http.Handler for /image and /thumb (path-guarded binary serving)
└── internal/      config · store(db) · scanner · ingest · search · thumbs · paths
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

Two details inside it matter:
- **Sort is allow-listed, never interpolated.** The `sorts` map keys a handful of
  known sorts to SQL fragments; an unknown sort falls back to `m.title`. User
  input is never concatenated into the ORDER BY.
- **Multi-tag filter is AND, not OR.** `GROUP BY m.id HAVING
  COUNT(DISTINCT mt.tag_id) = len(tagIDs)` means a title must carry *all* requested
  tags to match.

### 4. File-serving must pass the path-traversal guard

`/image` and `/thumb` (in `assets.go`) take an OS filesystem path as a query
param. Both call `paths.IsWithinRoots` (`internal/paths/paths.go`), which
canonicalizes the path (abs + clean, following symlinks when present, like
Python's `Path.resolve()`) and confirms it lives under a configured library root
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

---

## Module map

Backend packages under `internal/`. Each has one responsibility.

| Package | Responsibility |
|---------|----------------|
| `config` | Load/save `config.json`; resolve data dir (`%APPDATA%/doujin` via `os.UserConfigDir`), db path, thumb cache dir. |
| `store` | Connection (`modernc.org/sqlite`, FK on, single conn), schema, and the **migration ladder** (`migrations`, `Init`). Exposes a `Querier` interface satisfied by `*sql.DB` and `*sql.Tx`. |
| `scanner` | Walk library roots, detect un-imported title folders, list + natural-sort pages. Derives content from disk. |
| `thumbs` | `imaging` thumbnail generation + disk cache (atomic, placeholder fallback). |
| `ingest` | Create/link author, manga row, and tags. `NormalizeTag`, dedupe, transactional. |
| `search` | The read chokepoint: `SearchManga`, suggestions, tag/author/manga lookups, and the `Manga`/`Author`/`Tag` row types. |
| `paths` | `IsWithinRoots` path-traversal guard. |

Root `main` package: `app.go` (bound methods — the thin API layer that
validates/clamps input and calls the packages above), `assets.go` (binary file
handler), `main.go` (Wails wiring). Business logic lives in `internal/`, which
keeps it unit-testable without a running window.

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

The schema is byte-for-byte the Python build's, so the Go app opens an existing
`doujin.db` with no migration needed.

---

## Adding a feature — quick checklist

- **New way to slice the library?** Extend `search.SearchManga`, don't write a
  parallel query. Keep `sort` allow-listed.
- **New schema?** Append a migration; add a `store_test.go` test.
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
