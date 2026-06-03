# doujin/search.py
from __future__ import annotations

import sqlite3

from doujin.ingest import normalize_tag

# "date" sorts newest-first; m.id DESC is a deterministic tiebreaker for rows
# that share a date_added timestamp (e.g. a bulk import within the same second).
_SORTS = {"title": "m.title", "author": "a.name", "date": "m.date_added DESC, m.id DESC"}


def search_manga(
    conn: sqlite3.Connection,
    *,
    query: str | None = None,
    author_id: int | None = None,
    tag_ids: list[int] | None = None,
    sort: str = "title",
    limit: int | None = None,
    offset: int = 0,
) -> list[sqlite3.Row]:
    sql = ["SELECT m.*, a.name AS author_name FROM manga m JOIN authors a ON a.id = m.author_id"]
    params: list = []
    where: list[str] = []
    if tag_ids:
        marks = ",".join("?" * len(tag_ids))
        sql.append(f"JOIN manga_tags mt ON mt.manga_id = m.id AND mt.tag_id IN ({marks})")
        params.extend(tag_ids)
    if query:
        where.append("(m.title LIKE ? OR a.name LIKE ?)")
        params.extend([f"%{query}%", f"%{query}%"])
    if author_id:
        where.append("m.author_id = ?")
        params.append(author_id)
    if where:
        sql.append("WHERE " + " AND ".join(where))
    if tag_ids:
        sql.append("GROUP BY m.id HAVING COUNT(DISTINCT mt.tag_id) = ?")
        params.append(len(tag_ids))
    # _SORTS maps the only valid sort values; an unknown (or attacker-supplied)
    # sort falls back to "m.title" and is NEVER interpolated into the SQL.
    sql.append("ORDER BY " + _SORTS.get(sort, "m.title"))
    if limit is not None:
        sql.append("LIMIT ? OFFSET ?")
        params.extend([limit, offset])
    return conn.execute(" ".join(sql), params).fetchall()


def suggest_tags(conn: sqlite3.Connection, prefix: str, limit: int = 10) -> list[sqlite3.Row]:
    p = normalize_tag(prefix)
    return conn.execute(
        "SELECT name FROM tags WHERE name LIKE ? ORDER BY name LIMIT ?",
        (f"{p}%", limit),
    ).fetchall()


def tag_ids_for_names(conn: sqlite3.Connection, names: list[str]) -> list[int]:
    """Resolve tag names to their ids (normalized). Unknown names are skipped."""
    ids: list[int] = []
    for name in names:
        row = conn.execute("SELECT id FROM tags WHERE name = ?", (normalize_tag(name),)).fetchone()
        if row:
            ids.append(row["id"])
    return ids


def get_manga(conn: sqlite3.Connection, manga_id: int) -> sqlite3.Row | None:
    return conn.execute(
        "SELECT m.*, a.name AS author_name FROM manga m "
        "JOIN authors a ON a.id = m.author_id WHERE m.id = ?",
        (manga_id,),
    ).fetchone()


def get_manga_tags(conn: sqlite3.Connection, manga_id: int) -> list[str]:
    return [
        r["name"]
        for r in conn.execute(
            "SELECT t.name FROM tags t JOIN manga_tags mt ON mt.tag_id = t.id "
            "WHERE mt.manga_id = ? ORDER BY t.name",
            (manga_id,),
        )
    ]


def list_authors(conn: sqlite3.Connection) -> list[sqlite3.Row]:
    return conn.execute("SELECT * FROM authors ORDER BY name").fetchall()


def list_tags(conn: sqlite3.Connection) -> list[sqlite3.Row]:
    return conn.execute("SELECT * FROM tags ORDER BY name").fetchall()
