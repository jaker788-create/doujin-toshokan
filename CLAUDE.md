# CLAUDE.md - Claude Code Instructions

> **Project**: Doujin Bunko
> **Created**: 2026-05-27

---

## Project

Doujin Bunko is a local, offline, single-user manga library and viewer. It indexes a
collection of `author/title/*.images` folders on disk, lets you search and
filter by title, author, and tags, and read titles as a scrollable image
gallery — without ever moving your files (index-in-place).

**Stack:** Python 3.11+, FastAPI, Uvicorn, SQLite (stdlib `sqlite3`), Pillow,
Jinja2 server-rendered HTML with vanilla JS.

**Key directories:**
- `doujin/` — application package (config, db, scanner, thumbnails, ingest,
  search, paths, app, cli)
- `doujin/templates/` — Jinja2 HTML templates (base, library, title, scan)
- `doujin/static/` — CSS and JavaScript
- `tests/` — pytest suite

Metadata (authors, titles, tags, page counts, paths) lives in SQLite at
`%APPDATA%/doujin/doujin.db`. Thumbnails are disk-cached in
`%APPDATA%/doujin/thumbs/`. No files in the library are ever moved or modified.

**See `docs/ARCHITECTURE.md`** for the load-bearing invariants (index-in-place,
the `search_manga` read chokepoint, the path-traversal guard, the schema
migration ladder) — read it before adding a feature.


## Conventions

- Commit messages: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`.
- Schema changes: append a migration to `MIGRATIONS` in `doujin/db.py`; never
  edit an existing migration (see `docs/ARCHITECTURE.md`).
- Lint/format clean before committing (`ruff check`, `ruff format`).

## Commands

- Run: `doujin` (or `python -m doujin`) — serves http://127.0.0.1:8765
- Test: `pytest` (or `.venv/Scripts/python.exe -m pytest`)
- Lint: `.venv/Scripts/python.exe -m ruff check doujin tests`
- Format: `.venv/Scripts/python.exe -m ruff format doujin tests`
- Config: `%APPDATA%/doujin/config.json` — set `library_roots` (list of paths)
  and optionally `port` (default 8765)
