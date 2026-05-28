from __future__ import annotations

import os
import webbrowser
from pathlib import Path

import uvicorn
from fastapi import FastAPI

from doujin.app import create_app
from doujin.config import default_data_dir, load_config, migrate_legacy_data_dir


def _data_dir() -> Path:
    env = os.environ.get("DOUJIN_DATA_DIR")
    return Path(env) if env else default_data_dir()


def _ensure_data_dir() -> Path:
    d = _data_dir()
    # Only auto-migrate the default location; an explicit DOUJIN_DATA_DIR is
    # taken as-is.
    if not os.environ.get("DOUJIN_DATA_DIR"):
        migrate_legacy_data_dir(d)
    d.mkdir(parents=True, exist_ok=True)
    return d


def build_app_for_cli() -> FastAPI:
    return create_app(_ensure_data_dir())


def main() -> None:
    data_dir = _ensure_data_dir()
    cfg = load_config(data_dir)
    app = create_app(data_dir)
    url = f"http://127.0.0.1:{cfg.port}"
    print(f"Doujin Toshokan running at {url}  (data dir: {data_dir})")
    if not cfg.library_roots:
        print(
            "No library roots configured yet. Edit "
            f"{data_dir / 'config.json'} to add 'library_roots', then restart."
        )
    try:
        webbrowser.open(url)
    except Exception:
        pass
    uvicorn.run(app, host="127.0.0.1", port=cfg.port)
