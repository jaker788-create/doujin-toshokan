from __future__ import annotations

import sqlite3
from pathlib import Path

SCHEMA = """
CREATE TABLE IF NOT EXISTS authors (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS manga (
    id             INTEGER PRIMARY KEY,
    title          TEXT NOT NULL,
    author_id      INTEGER NOT NULL REFERENCES authors(id),
    folder_path    TEXT NOT NULL UNIQUE,
    cover_rel_path TEXT,
    page_count     INTEGER NOT NULL DEFAULT 0,
    date_added     TEXT NOT NULL,
    date_modified  TEXT NOT NULL,
    missing        INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS tags (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS manga_tags (
    manga_id INTEGER NOT NULL REFERENCES manga(id) ON DELETE CASCADE,
    tag_id   INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
    PRIMARY KEY (manga_id, tag_id)
);
CREATE INDEX IF NOT EXISTS idx_manga_author ON manga(author_id);
CREATE INDEX IF NOT EXISTS idx_manga_title  ON manga(title);
"""


def connect(path: str | Path) -> sqlite3.Connection:
    conn = sqlite3.connect(str(path), check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON")
    return conn


def _migrate_001_initial(conn: sqlite3.Connection) -> None:
    """Baseline schema. Uses CREATE ... IF NOT EXISTS so it is a safe no-op on
    databases created before the migration system existed (those sit at
    user_version 0); running it simply stamps them as version 1."""
    conn.executescript(SCHEMA)  # executescript issues an implicit COMMIT


# Ordered migration ladder. The 1-based position of each function is the schema
# version it produces, so MIGRATIONS[0] -> version 1, MIGRATIONS[1] -> version 2,
# and so on. PRAGMA user_version records how far a given database has been
# migrated.
#
# To evolve the schema: APPEND a new function here, never edit or reorder an
# existing one (that would corrupt the version history). Write each migration to
# be safe if re-applied after an interrupted run — prefer IF NOT EXISTS, and
# guard ALTER TABLE with a column-existence check (see PRAGMA table_info).
MIGRATIONS = [
    _migrate_001_initial,
]


def init_db(conn: sqlite3.Connection) -> None:
    """Bring the database up to the latest schema version, applying any pending
    migrations in order. Idempotent: a database already at the latest version is
    left untouched, so this is safe to call on every startup."""
    version = conn.execute("PRAGMA user_version").fetchone()[0]
    for target, migrate in enumerate(MIGRATIONS, start=1):
        if version < target:
            migrate(conn)
            # PRAGMA does not accept bound parameters; `target` is a controlled
            # int from enumerate(), so the f-string is injection-safe.
            conn.execute(f"PRAGMA user_version = {target}")
            conn.commit()
