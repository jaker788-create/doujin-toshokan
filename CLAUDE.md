# CLAUDE.md - Claude Code Instructions

> **Project**: Doujin Toshokan
> **Created**: 2026-05-27
> **Migrated to Go + Wails**: 2026-06-03

---

## Project

Doujin Toshokan is a local, offline, single-user manga library and viewer. It
indexes a collection of `author/title/*.images` folders on disk, lets you search
and filter by title, author, and tags, and read titles as a scrollable image
gallery — without ever moving your files (index-in-place).

It is a **native desktop app**: a Wails (Go) core renders an embedded TypeScript
frontend inside the OS WebView2 window and ships as a single binary — no browser,
no local server to start.

**Stack:** Go 1.25+, Wails v2 (WebView2), SQLite via `modernc.org/sqlite`
(pure Go, **no cgo**), `disintegration/imaging` for thumbnails, TypeScript + Vite
frontend (no framework).

**Key directories:**
- `main.go` — Wails entry: window options, embeds `frontend/dist`, wires the bound methods + the asset handler.
- `app.go` — the `App` struct; its **exported methods are the frontend JSON API** (Wails generates typed TS bindings under `frontend/wailsjs/`).
- `assets.go` — the `/image` + `/thumb` HTTP handler (path-guarded) that serves library files to `<img>` tags.
- `internal/` — backend packages: `config`, `store` (db + migrations), `scanner`, `ingest`, `search`, `thumbs`, `paths`.
- `frontend/src/` — the TypeScript SPA (`main.ts`) + `theme.css`. `frontend/public/` holds fonts + the noise texture; `frontend/wailsjs/` is generated bindings.
- `build/` — Wails build config (icons, manifests, NSIS installer template).

Metadata (authors, titles, tags, page counts, paths) lives in SQLite at
`%APPDATA%/doujin/doujin.db`, opened in place. Thumbnails are disk-cached in
`%APPDATA%/doujin/thumbs/`. No files in the library are ever moved or modified.

**See `docs/ARCHITECTURE.md`** for the load-bearing invariants (index-in-place,
the `search.SearchManga` read chokepoint, the path-traversal guard, the migration
ladder, the typed bound-method seam, the single-connection rule) — read it before
adding a feature.

## Conventions

- Commit messages: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`.
- Schema changes: **APPEND** a migration to the `migrations` slice in
  `internal/store/store.go`; never edit or reorder an existing one (see
  `docs/ARCHITECTURE.md`).
- `gofmt` + `go vet` clean before committing. The frontend is strict TypeScript
  (`tsc` runs as part of the Wails frontend build).
- Definition of done (every feature): (1) `go test ./...` green, (2) `go vet ./...`
  clean + `gofmt` applied, (3) `wails build` succeeds (it regenerates the bindings
  and the embedded frontend). The shipped binary is `build/bin/doujin.exe`.

## Commands

Go lives at `C:\Program Files\Go`; the `wails` CLI at `%USERPROFILE%\go\bin`. If a
fresh shell can't find them, prepend both to `PATH`.

- Run (hot reload): `wails dev`
- Build: `wails build` (native ARM64 on this machine) — or
  `wails build -platform windows/arm64`; add `-nsis` for an installer →
  `build/bin/doujin.exe`
- Test: `go test ./...`
- Vet / format: `go vet ./...` · `gofmt -w .`
- Config: `%APPDATA%/doujin/config.json` — `library_roots` (list of paths), now
  editable in-app via the Scan page's folder picker. `port` is kept for
  backward-compatible config files but is unused (no HTTP server).
