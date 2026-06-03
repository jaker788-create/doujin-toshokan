# Legacy Python implementation (archived)

This directory holds the **original** Doujin Toshokan — a Python / FastAPI / Jinja
web app — superseded by the native **Go + Wails** desktop app at the repository
root (archived 2026-06-03). It is kept for reference and as a fallback. It reads
the **same** `%APPDATA%/doujin/doujin.db`, so both can run against the same
library.

It is **not** maintained. New work happens in the Go app — see the repo-root
`CLAUDE.md` and `docs/ARCHITECTURE.md`.

## Run it

From this `legacy/` directory:

```powershell
python -m venv .venv
.venv\Scripts\Activate.ps1     # macOS/Linux: source .venv/bin/activate
pip install .
doujin                          # serves http://127.0.0.1:8765
```

## Layout

- `doujin/` — the application package (`config`, `db`, `scanner`, `thumbnails`,
  `ingest`, `search`, `paths`, `app`, `cli`) plus `templates/` and `static/`
- `tests/` — the pytest suite
- `pyproject.toml` — package metadata + ruff/pytest config
- `doujin.spec` — PyInstaller spec (single-file ARM64 build)
