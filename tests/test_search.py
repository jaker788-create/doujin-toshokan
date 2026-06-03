# tests/test_search.py
from doujin.ingest import get_or_create_tag, ingest_manga
from doujin.search import (
    get_author,
    get_manga,
    get_manga_tags,
    list_authors,
    list_tags,
    search_manga,
    suggest_authors,
    suggest_tags,
)


def _seed(conn):
    a = ingest_manga(
        conn,
        title="Blue Sky",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path="1.png",
        page_count=11,
        tags=["action", "scifi"],
    )
    b = ingest_manga(
        conn,
        title="Forest",
        author="Mori",
        folder_path="/p2",
        cover_rel_path="1.png",
        page_count=3,
        tags=["slice-of-life"],
    )
    return a, b


def test_search_by_title(conn):
    _seed(conn)
    rows = search_manga(conn, query="blue")
    assert [r["title"] for r in rows] == ["Blue Sky"]


def test_filter_by_author(conn):
    a, b = _seed(conn)
    mori = conn.execute("SELECT id FROM authors WHERE name='Mori'").fetchone()["id"]
    rows = search_manga(conn, author_id=mori)
    assert [r["title"] for r in rows] == ["Forest"]


def test_filter_by_tags_requires_all(conn):
    _seed(conn)
    action = get_or_create_tag(conn, "action")
    scifi = get_or_create_tag(conn, "scifi")
    conn.commit()
    rows = search_manga(conn, tag_ids=[action, scifi])
    assert [r["title"] for r in rows] == ["Blue Sky"]


def test_sort_by_date_desc(conn):
    _seed(conn)
    rows = search_manga(conn, sort="date")
    assert rows[0]["title"] == "Forest"  # inserted second, newest first


def test_suggest_tags_prefix(conn):
    _seed(conn)
    names = [r["name"] for r in suggest_tags(conn, "sc")]
    assert names == ["scifi"]


def test_suggest_authors_substring(conn):
    _seed(conn)
    # Substring (not prefix): "or" is inside "Mori" but not "Aoi".
    rows = suggest_authors(conn, "or")
    assert [r["name"] for r in rows] == ["Mori"]
    assert rows[0]["id"] > 0  # the id is needed for author filtering
    # Empty prefix returns everything, ordered by name.
    assert [r["name"] for r in suggest_authors(conn, "")] == ["Aoi", "Mori"]


def test_get_author(conn):
    _seed(conn)
    mori = conn.execute("SELECT id FROM authors WHERE name='Mori'").fetchone()["id"]
    assert get_author(conn, mori)["name"] == "Mori"
    assert get_author(conn, 999999) is None  # dangling id -> None, no crash


def test_search_combines_author_query_and_tags(conn):
    _seed(conn)
    aoi = conn.execute("SELECT id FROM authors WHERE name='Aoi'").fetchone()["id"]
    mori = conn.execute("SELECT id FROM authors WHERE name='Mori'").fetchone()["id"]
    action = get_or_create_tag(conn, "action")
    scifi = get_or_create_tag(conn, "scifi")
    conn.commit()
    # query + author + tags all AND together -> the one matching row.
    rows = search_manga(conn, query="blue", author_id=aoi, tag_ids=[action, scifi])
    assert [r["title"] for r in rows] == ["Blue Sky"]
    # A wrong author with the same tags yields nothing (guards the AND).
    assert search_manga(conn, author_id=mori, tag_ids=[action, scifi]) == []


def test_get_manga_and_tags(conn):
    a, _ = _seed(conn)
    m = get_manga(conn, a)
    assert m["title"] == "Blue Sky"
    assert m["author_name"] == "Aoi"
    assert set(get_manga_tags(conn, a)) == {"action", "scifi"}


def test_list_authors_and_tags(conn):
    _seed(conn)
    assert [r["name"] for r in list_authors(conn)] == ["Aoi", "Mori"]
    assert {r["name"] for r in list_tags(conn)} == {"action", "scifi", "slice-of-life"}


def test_search_limit_offset(conn):
    _seed(conn)  # Blue Sky (Aoi), Forest (Mori)
    page1 = search_manga(conn, sort="title", limit=1, offset=0)
    page2 = search_manga(conn, sort="title", limit=1, offset=1)
    assert [r["title"] for r in page1] == ["Blue Sky"]
    assert [r["title"] for r in page2] == ["Forest"]
    assert search_manga(conn, sort="title", limit=1, offset=2) == []  # past end
    assert len(search_manga(conn, sort="title")) == 2  # limit=None unchanged
