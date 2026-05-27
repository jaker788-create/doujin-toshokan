# Doujin Bunko — Architecture

> A map of how Doujin Bunko is put together and, more importantly, the **load-bearing
> invariants** that future changes must not break. If you're about to add a
> feature, skim "Invariants" first — most bugs in a project like this come from
> quietly violating one of them.

Doujin Bunko is a single-user, offline, local web app that indexes a manga collection
**in place** and serves it as a searchable library and a scrollable reader. It
never moves, renames, or deletes your files.

**Stack:** Python 3.11+, FastAPI + Uvicorn, SQLite (stdlib `sqlite3`), Pillow,
Jinja2 server-rendered HTML + vanilla JS (no frontend build step).

---

## Load-bearing invariants

These are the rules the whole design leans on. Each one has a "why" so you can
tell when it's safe to bend.

### 1. Index in place — files are read-only

The app **only ever reads** the library on disk. It never writes, moves,
renames, or deletes a file under a library root. All organization (which titles
exist, their authors, tags, cover, page count) lives in SQLite; the files are
untouched. *Why:* a database problem can never cost you data — at worst you
re-scan. Any feature that needs to modify files breaks this guarantee and should
be discussed before building.

### 2. Two sources of truth, cleanly split

- **Filesystem = source of truth for content.** The list of pages in a title is
  **not stored** — it is derived at view time by listing image files in the
  folder and natural-sorting them (`scanner.list_pages`, `scanner.natural_key`,
  so `2.png` sorts before `10.png`). `page_count` in the DB is only a *cached*
  number, refreshed on rescan.
- **DB = source of truth for organization.** Authors, titles, tags, tag links,
  `cover_rel_path`, and the cached `page_count` live in SQLite.

*Why:* you can drop, add, or reorder image files on disk and a rescan picks it
up — the DB doesn't try to mirror file contents, only to organize them.

### 3. `search_manga` is the single read chokepoint

Every browse / filter / search / paginate path goes through
`search.search_manga` (`search.py:13`). It composes optional `query`,
`author_id`, `tag_ids`, `sort`, `limit`, and `offset` into one parameterized
query. *Why:* pagination, sorting, and filtering were added once, to one
function, and every caller inherited them. When adding a new way to slice the
library, **extend this function** rather than writing a parallel query.

Two details inside it matter:
- **Sort is allow-listed, never interpolated.** `_SORTS` (`search.py:10`) maps a
  handful of known keys to SQL fragments; an unknown or attacker-supplied `sort`
  falls back to `m.title`. User input is never concatenated into the ORDER BY.
- **Multi-tag filter is AND, not OR.** `GROUP BY m.id HAVING
  COUNT(DISTINCT mt.tag_id) = len(tag_ids)` (`search.py:38-40`) means a title
  must carry *all* requested tags to match.

### 4. File-serving endpoints must pass the path-traversal guard

`/image` and `/thumb` take an OS filesystem path as a query param. Both call
`paths.is_within_roots` (`paths.py:7`), which canonicalizes via `Path.resolve()`
and confirms the target lives under a configured library root **before any file
access** (`app.py:47-62`). *Why:* without it, `?path=../../secrets` would serve
arbitrary files. **Any new endpoint that serves a file by path must call this
guard first.**

### 5. Thumbnails: on-demand, disk-cached, atomic, with fallback

`thumbnails.get_thumbnail` (`thumbnails.py:26`) generates a JPEG on first
request and caches it. The cache key (`cache_key`, `thumbnails.py:12`) hashes
**path + mtime + size + width**, so editing or replacing a source image
naturally invalidates its thumbnail. Writes go to a temp file then
`os.replace` (atomic), so an interrupted write can't leave a corrupt file that
later reads as a cache hit. Unreadable/corrupt sources return a placeholder
instead of raising. Images are never upscaled.

### 6. Duplicate ingest is prevented by `folder_path UNIQUE`

`manga.folder_path` is `UNIQUE` (`db.py` SCHEMA). Re-ingesting the same folder
raises `sqlite3.IntegrityError`, which the ingest routes treat as "already
imported" and skip silently (`app.py` `/ingest`, `/import-all`). *Why:* scan +
bulk-import + drop-in-a-new-folder are all the same flow and can overlap; the
constraint makes re-runs idempotent.

### 7. Missing folders are flagged, never deleted

If a title's folder disappears, `/rescan` sets `missing=1` rather than removing
the row (`app.py` `rescan`). *Why:* a temporarily unmounted drive or moved
folder shouldn't erase your tags and metadata.

### 8. Schema changes go through the migration ladder

Never edit the live schema in place and never edit an existing migration. See
"Evolving the schema" below.

---

## Module map

All under `doujin/`. Each file has one responsibility; files that change together
live together.

| Module | Responsibility |
|--------|----------------|
| `config.py` | Load/save `config.json`; resolve data dir, db path, thumb cache dir. Data lives in `%APPDATA%/doujin/`, **outside the repo**. |
| `db.py` | Connection (`Row` factory, FK on), schema, and the **migration ladder** (`MIGRATIONS`, `init_db`). |
| `scanner.py` | Walk library roots, detect un-imported title folders, list + natural-sort pages. Derives content from disk. |
| `thumbnails.py` | Pillow thumbnail generation + disk cache (atomic, fallback). |
| `ingest.py` | Create/link author, manga row, and tags. `normalize_tag`, dedupe, atomic `with conn:`. |
| `search.py` | The read chokepoint: `search_manga`, `suggest_tags`, tag/author/manga lookups. |
| `paths.py` | `is_within_roots` path-traversal guard. |
| `app.py` | FastAPI app factory (`create_app`), routes, per-request `get_conn` dependency. `PAGE_SIZE = 60`. |
| `cli.py` / `__main__.py` | Entry points (`doujin` console script, `python -m doujin`). |
| `templates/`, `static/` | Jinja2 HTML + CSS/JS (infinite-scroll grid, tag autocomplete, lightbox). |

The web layer (`app.py`) is thin: it validates/clamps input, calls into the
modules above, and renders templates or JSON. Business logic lives in the
modules, which keeps them unit-testable without a running server.

---

## Request flows

**Browse / search.** `GET /` → `home()` renders page 0 via `search_manga` into
`library.html`, stamping the grid with the active filters and `next_offset`.
The client (`static/app.js`) then appends further pages from `GET /api/search`
as a sentinel scrolls into view (IntersectionObserver). A filter change is a
hard reset; a scroll is an append. `/api/search` clamps `limit`/`offset`
(`app.py:88`) so a negative offset can't 500 and an unbounded limit can't pull
everything.

**Read a title.** `GET /manga/{id}` → `get_manga` + `list_pages` (derived from
disk) → `title.html` renders the gallery; each page is an `<img>` pointing at
`/image`, covers use `/thumb`. Click an image for a lightbox.

**Ingest.** `GET /scan` lists folders on disk not yet in the DB
(`scanner.find_unimported`). `POST /ingest` imports one (confirm-each, with
manual author/title/tags). `GET/POST /import-all` bulk-imports everything found,
author/title prefilled from folder names, tags empty — for the initial load of a
large library where confirm-each is impractical.

---

## Evolving the schema (the migration ladder)

`db.py` versions the database with `PRAGMA user_version`. `MIGRATIONS` is an
ordered list of functions; the 1-based position of each is the schema version it
produces (`MIGRATIONS[0]` → version 1). `init_db` reads the current
`user_version`, runs every pending migration in order, and stamps the new
version. It is idempotent and safe to call on every startup.

**To add or change a table/column:**

1. **Append** a new function to `MIGRATIONS` — never edit or reorder an existing
   one (that corrupts the version history of databases already in the field).
2. Make it safe if re-applied after an interrupted run: prefer
   `CREATE ... IF NOT EXISTS`, and guard `ALTER TABLE ... ADD COLUMN` with a
   `PRAGMA table_info` existence check.
3. Add a test in `tests/test_db.py` (the existing `test_runner_*` and
   `test_legacy_*` tests show the pattern).

Existing databases created before this system sat at `user_version 0`;
migration 1 is the original schema (still `IF NOT EXISTS`), so running it against
a populated DB is a harmless no-op that simply stamps it to version 1.

---

## Adding a feature — quick checklist

- **New way to slice the library?** Extend `search_manga`, don't write a parallel
  query. Keep `sort` allow-listed.
- **New schema?** Append a migration; add a `test_db.py` test.
- **Serving a file by path?** Call `is_within_roots` first.
- **Client-rendered HTML from JSON?** Escape it — `app.js` has an `esc()` helper
  because client rendering bypasses Jinja's autoescaping.
- **Touching the filesystem?** Read only. Don't break index-in-place.

---

## Local development

```
# Run            (serves http://127.0.0.1:8765)
doujin           # or: python -m doujin

# Test
.venv/Scripts/python.exe -m pytest

# Lint + format  (config in pyproject.toml)
.venv/Scripts/python.exe -m ruff check doujin tests
.venv/Scripts/python.exe -m ruff format doujin tests
```

Config and runtime data (`config.json`, `doujin.db`, `thumbs/`) live in
`%APPDATA%/doujin/`, never in the repo. Set `library_roots` in `config.json`.
