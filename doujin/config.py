from __future__ import annotations

import json
import os
from dataclasses import asdict, dataclass, field
from pathlib import Path


def _appdata_base() -> str:
    return os.environ.get("APPDATA") or os.path.expanduser("~/.config")


def default_data_dir() -> Path:
    return Path(_appdata_base()) / "doujin"


def migrate_legacy_data_dir(new_dir: Path) -> None:
    """One-time move of the pre-rename data dir to the new location.

    When the app was called "Stash" its data lived in a sibling ``stash/``
    directory (e.g. ``%APPDATA%/stash``). If that sibling exists but the new
    ``doujin/`` dir doesn't, move it over and rename the DB file to match the
    new brand. A no-op once the new dir exists, so it's safe to call on every
    startup. The legacy dir is resolved as a sibling of ``new_dir`` rather than
    from global state, which keeps this unit-testable.
    """
    if new_dir.exists():
        return
    legacy = new_dir.parent / "stash"
    if legacy == new_dir or not legacy.exists():
        return
    new_dir.parent.mkdir(parents=True, exist_ok=True)
    legacy.rename(new_dir)
    old_db = new_dir / "stash.db"
    new_db = new_dir / "doujin.db"
    if old_db.exists() and not new_db.exists():
        old_db.rename(new_db)


@dataclass
class Config:
    library_roots: list[str] = field(default_factory=list)
    port: int = 8765


def _config_file(data_dir: Path) -> Path:
    return data_dir / "config.json"


def load_config(data_dir: Path) -> Config:
    f = _config_file(data_dir)
    if not f.exists():
        return Config()
    data = json.loads(f.read_text(encoding="utf-8"))
    return Config(
        library_roots=list(data.get("library_roots", [])),
        port=int(data.get("port", 8765)),
    )


def save_config(config: Config, data_dir: Path) -> None:
    data_dir.mkdir(parents=True, exist_ok=True)
    _config_file(data_dir).write_text(json.dumps(asdict(config), indent=2), encoding="utf-8")


def db_path(data_dir: Path) -> Path:
    return data_dir / "doujin.db"


def thumb_cache_dir(data_dir: Path) -> Path:
    return data_dir / "thumbs"
