# tests/test_ingest.py
import sqlite3

import pytest

from doujin.ingest import ingest_manga, normalize_tag, set_manga_tags


def test_normalize_tag():
    assert normalize_tag("  SciFi ") == "scifi"


def test_ingest_creates_rows_and_tags(conn):
    mid = ingest_manga(
        conn,
        title="Blue Sky",
        author="Aoi",
        folder_path="/lib/Aoi/Blue Sky",
        cover_rel_path="1.png",
        page_count=11,
        tags=["Action", "scifi", "Action"],
    )
    m = conn.execute("SELECT * FROM manga WHERE id=?", (mid,)).fetchone()
    assert m["title"] == "Blue Sky"
    assert m["page_count"] == 11
    author = conn.execute("SELECT name FROM authors WHERE id=?", (m["author_id"],)).fetchone()
    assert author["name"] == "Aoi"
    tags = {
        r["name"]
        for r in conn.execute(
            "SELECT t.name FROM tags t JOIN manga_tags mt ON mt.tag_id=t.id WHERE mt.manga_id=?",
            (mid,),
        )
    }
    assert tags == {"action", "scifi"}  # normalized + de-duplicated


def test_author_reused(conn):
    ingest_manga(
        conn, title="A", author="Aoi", folder_path="/p1", cover_rel_path=None, page_count=1, tags=[]
    )
    ingest_manga(
        conn, title="B", author="Aoi", folder_path="/p2", cover_rel_path=None, page_count=1, tags=[]
    )
    assert conn.execute("SELECT COUNT(*) c FROM authors").fetchone()["c"] == 1


def _tags_of(conn, mid):
    return [
        r["name"]
        for r in conn.execute(
            "SELECT t.name FROM tags t JOIN manga_tags mt ON mt.tag_id=t.id "
            "WHERE mt.manga_id=? ORDER BY t.name",
            (mid,),
        )
    ]


def test_set_manga_tags_normalizes_dedupes_and_sorts(conn):
    mid = ingest_manga(
        conn, title="A", author="Aoi", folder_path="/p1", cover_rel_path=None, page_count=1, tags=[]
    )
    saved = set_manga_tags(conn, mid, ["  SciFi ", "action", "Action", "", "  "])
    assert saved == ["action", "scifi"]  # normalized, de-duped, sorted, blanks dropped
    assert _tags_of(conn, mid) == ["action", "scifi"]


def test_set_manga_tags_replaces_existing(conn):
    mid = ingest_manga(
        conn,
        title="A",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=["old", "stale"],
    )
    set_manga_tags(conn, mid, ["fresh"])
    assert _tags_of(conn, mid) == ["fresh"]  # old tags gone, not merged


def test_set_manga_tags_empty_clears(conn):
    mid = ingest_manga(
        conn,
        title="A",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=["one", "two"],
    )
    assert set_manga_tags(conn, mid, []) == []
    assert _tags_of(conn, mid) == []


def test_set_manga_tags_reuses_existing_tag_rows(conn):
    a = ingest_manga(
        conn,
        title="A",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=["shared"],
    )
    b = ingest_manga(
        conn, title="B", author="Aoi", folder_path="/p2", cover_rel_path=None, page_count=1, tags=[]
    )
    set_manga_tags(conn, b, ["shared"])
    # both titles point at the SAME tag row — no duplicate "shared" tag created
    assert conn.execute("SELECT COUNT(*) c FROM tags WHERE name='shared'").fetchone()["c"] == 1
    assert _tags_of(conn, a) == ["shared"]
    assert _tags_of(conn, b) == ["shared"]


def test_duplicate_folder_path_rejected(conn):
    ingest_manga(
        conn,
        title="A",
        author="Aoi",
        folder_path="/dup",
        cover_rel_path=None,
        page_count=1,
        tags=[],
    )
    with pytest.raises(sqlite3.IntegrityError):
        ingest_manga(
            conn,
            title="A2",
            author="Aoi",
            folder_path="/dup",
            cover_rel_path=None,
            page_count=1,
            tags=[],
        )
