# doujin/ingest.py
from __future__ import annotations

import sqlite3
from datetime import UTC, datetime


def normalize_tag(name: str) -> str:
    return name.strip().lower()


def get_or_create_author(conn: sqlite3.Connection, name: str) -> int:
    row = conn.execute("SELECT id FROM authors WHERE name=?", (name,)).fetchone()
    if row:
        return row["id"]
    return conn.execute("INSERT INTO authors(name) VALUES (?)", (name,)).lastrowid


def get_or_create_tag(conn: sqlite3.Connection, name: str) -> int:
    row = conn.execute("SELECT id FROM tags WHERE name=?", (name,)).fetchone()
    if row:
        return row["id"]
    return conn.execute("INSERT INTO tags(name) VALUES (?)", (name,)).lastrowid


def set_manga_tags(conn: sqlite3.Connection, manga_id: int, tags: list[str]) -> list[str]:
    """Replace a manga's tags with ``tags`` (normalized + de-duplicated), atomically.

    This is a *replace*, not a merge: the new list is the complete tag set, so
    dropping a tag is just leaving it out. The delete + re-insert runs inside one
    ``with conn:`` transaction, so a failure mid-way rolls back to the previous
    tag set rather than leaving the title half-tagged.

    Only the DB is touched — the library files are never modified (index-in-place,
    see docs/ARCHITECTURE.md). Tag rows that become unused are deliberately left
    behind: they cost nothing and keep tag autocomplete useful for re-applying.

    Returns the saved tag names, sorted to match ``search.get_manga_tags`` order.
    """
    seen: list[str] = []
    with conn:
        conn.execute("DELETE FROM manga_tags WHERE manga_id = ?", (manga_id,))
        for raw in tags:
            name = normalize_tag(raw)
            if not name or name in seen:
                continue
            seen.append(name)
            tag_id = get_or_create_tag(conn, name)
            conn.execute(
                "INSERT OR IGNORE INTO manga_tags(manga_id, tag_id) VALUES (?,?)",
                (manga_id, tag_id),
            )
    return sorted(seen)


def ingest_manga(
    conn: sqlite3.Connection,
    *,
    title: str,
    author: str,
    folder_path: str,
    cover_rel_path: str | None,
    page_count: int,
    tags: list[str],
) -> int:
    """Insert a manga (creating/linking its author and tags) atomically.

    The whole operation runs inside ``with conn:`` so it commits on success and
    rolls back on any error — e.g. a duplicate ``folder_path`` raising
    ``sqlite3.IntegrityError`` leaves no orphan author/tag rows, and the
    exception propagates to the caller. The caller owns the connection; this
    function only manages this single transaction.
    """
    now = datetime.now(UTC).isoformat()
    with conn:
        author_id = get_or_create_author(conn, author)
        manga_id = conn.execute(
            "INSERT INTO manga(title, author_id, folder_path, cover_rel_path, "
            "page_count, date_added, date_modified, missing) "
            "VALUES (?,?,?,?,?,?,?,0)",
            (title, author_id, folder_path, cover_rel_path, page_count, now, now),
        ).lastrowid
        for raw in tags:
            name = normalize_tag(raw)
            if not name:
                continue
            tag_id = get_or_create_tag(conn, name)
            conn.execute(
                "INSERT OR IGNORE INTO manga_tags(manga_id, tag_id) VALUES (?,?)",
                (manga_id, tag_id),
            )
    return manga_id
