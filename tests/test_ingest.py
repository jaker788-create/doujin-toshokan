# tests/test_ingest.py
import sqlite3

import pytest

from doujin.ingest import ingest_manga, normalize_tag


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
