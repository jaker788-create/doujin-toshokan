# tests/test_app.py
import pytest
from fastapi.testclient import TestClient

from doujin.app import create_app
from doujin.config import Config, save_config


@pytest.fixture
def client(tmp_path, library):
    data_dir = tmp_path / "data"
    save_config(Config(library_roots=[str(library)], port=8765), data_dir)
    app = create_app(data_dir)
    return TestClient(app)


def test_home_ok(client):
    r = client.get("/")
    assert r.status_code == 200


def test_image_inside_root_served(client, library):
    p = str(library / "Aoi" / "Blue Sky" / "1.png")
    r = client.get("/image", params={"path": p})
    assert r.status_code == 200
    assert r.headers["content-type"].startswith("image/")


def test_image_outside_root_forbidden(client, tmp_path):
    secret = tmp_path / "secret.txt"
    secret.write_text("nope")
    r = client.get("/image", params={"path": str(secret)})
    assert r.status_code == 403


def test_thumb_inside_root_served(client, library):
    p = str(library / "Aoi" / "Blue Sky" / "1.png")
    r = client.get("/thumb", params={"path": p, "w": 40})
    assert r.status_code == 200
    assert r.headers["content-type"] == "image/jpeg"


def test_thumb_outside_root_forbidden(client, tmp_path):
    secret = tmp_path / "secret.txt"
    secret.write_text("nope")
    r = client.get("/thumb", params={"path": str(secret), "w": 40})
    assert r.status_code == 403


def test_search_query_param_filters(client, library, tmp_path):
    # seed directly through the DB the app uses
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    c = connect(db_path(tmp_path / "data"))
    ingest_manga(
        c,
        title="Blue Sky",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=["scifi"],
    )
    ingest_manga(
        c,
        title="Forest",
        author="Mori",
        folder_path="/p2",
        cover_rel_path=None,
        page_count=1,
        tags=["slice"],
    )
    c.close()
    r = client.get("/", params={"q": "blue"})
    assert "Blue Sky" in r.text and "Forest" not in r.text


def test_api_search_json(client, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    c = connect(db_path(tmp_path / "data"))
    ingest_manga(
        c,
        title="Blue Sky",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=["scifi"],
    )
    c.close()
    r = client.get("/api/search", params={"q": "blue"})
    assert r.status_code == 200
    data = r.json()
    assert any(m["title"] == "Blue Sky" for m in data)


def test_api_tag_suggest(client, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    c = connect(db_path(tmp_path / "data"))
    ingest_manga(
        c,
        title="Blue Sky",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=["scifi"],
    )
    c.close()
    r = client.get("/api/tags/suggest", params={"q": "sc"})
    assert r.status_code == 200
    assert "scifi" in r.json()


def _seed_two(tmp_path):
    """Seed Aoi/Blue Sky (scifi) + Mori/Forest (slice); return (aoi_id, mori_id)."""
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    c = connect(db_path(tmp_path / "data"))
    ingest_manga(
        c,
        title="Blue Sky",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=["scifi"],
    )
    ingest_manga(
        c,
        title="Forest",
        author="Mori",
        folder_path="/p2",
        cover_rel_path=None,
        page_count=1,
        tags=["slice"],
    )
    aoi = c.execute("SELECT id FROM authors WHERE name='Aoi'").fetchone()["id"]
    mori = c.execute("SELECT id FROM authors WHERE name='Mori'").fetchone()["id"]
    c.close()
    return aoi, mori


def test_api_author_suggest(client, tmp_path):
    _seed_two(tmp_path)
    r = client.get("/api/authors/suggest", params={"q": "ao"})
    assert r.status_code == 200
    data = r.json()
    hit = next(a for a in data if a["name"] == "Aoi")
    assert isinstance(hit["id"], int)  # id is needed to filter by author


def test_home_author_filter_shows_name(client, tmp_path):
    aoi, _ = _seed_two(tmp_path)
    r = client.get("/", params={"author": aoi})
    assert r.status_code == 200
    assert "Blue Sky" in r.text and "Forest" not in r.text  # only Aoi's manga
    assert "author: Aoi" in r.text  # chip shows the resolved name, not a bare id


def test_home_author_filter_unknown_id(client, tmp_path):
    _seed_two(tmp_path)
    r = client.get("/", params={"author": 999999})  # dangling id
    assert r.status_code == 200  # no 500
    assert "Blue Sky" not in r.text and "Forest" not in r.text  # empty grid


def test_api_search_includes_author_id(client, tmp_path):
    aoi, _ = _seed_two(tmp_path)
    r = client.get("/api/search", params={"q": "blue"})
    assert r.status_code == 200
    item = next(m for m in r.json() if m["title"] == "Blue Sky")
    assert item["author_id"] == aoi  # cards need this to build /?author=ID links


def test_api_search_author_and_tag(client, tmp_path):
    aoi, _ = _seed_two(tmp_path)
    r = client.get("/api/search", params={"author": aoi, "tag": "scifi"})
    assert r.status_code == 200
    data = r.json()
    assert [m["title"] for m in data] == ["Blue Sky"]


def test_card_author_link_present(client, tmp_path):
    _seed_two(tmp_path)
    r = client.get("/")
    assert 'href="/?author=' in r.text  # author names are clickable filters


def test_title_gallery_lists_pages(client, library, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    folder = str(library / "Aoi" / "Blue Sky")
    c = connect(db_path(tmp_path / "data"))
    mid = ingest_manga(
        c,
        title="Blue Sky",
        author="Aoi",
        folder_path=folder,
        cover_rel_path="1.png",
        page_count=11,
        tags=["scifi"],
    )
    c.close()
    r = client.get(f"/manga/{mid}")
    assert r.status_code == 200
    assert "Blue Sky" in r.text
    # 11 page <img> tags pointing at /image
    assert r.text.count("/image?path=") == 11


def test_title_404(client):
    assert client.get("/manga/99999").status_code == 404


def test_title_page_has_tag_editor(client, library, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    folder = str(library / "Aoi" / "Blue Sky")
    c = connect(db_path(tmp_path / "data"))
    mid = ingest_manga(
        c,
        title="Blue Sky",
        author="Aoi",
        folder_path=folder,
        cover_rel_path="1.png",
        page_count=11,
        tags=["scifi"],
    )
    c.close()
    html = client.get(f"/manga/{mid}").text
    assert f'action="/manga/{mid}/tags"' in html  # editor form present
    assert 'value="scifi"' in html  # input prefilled with current tags


def test_update_tags_adds_and_persists(client, library, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga
    from doujin.search import get_manga_tags

    folder = str(library / "Aoi" / "Blue Sky")
    c = connect(db_path(tmp_path / "data"))
    mid = ingest_manga(
        c,
        title="Blue Sky",
        author="Aoi",
        folder_path=folder,
        cover_rel_path="1.png",
        page_count=11,
        tags=[],
    )
    c.close()
    r = client.post(
        f"/manga/{mid}/tags", data={"tags": "SciFi, Action , scifi"}, follow_redirects=False
    )
    assert r.status_code == 303
    c = connect(db_path(tmp_path / "data"))
    assert get_manga_tags(c, mid) == ["action", "scifi"]  # normalized + de-duped
    c.close()


def test_update_tags_replaces_then_filters(client, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    c = connect(db_path(tmp_path / "data"))
    mid = ingest_manga(
        c,
        title="Blue Sky",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=["old"],
    )
    c.close()
    client.post(f"/manga/{mid}/tags", data={"tags": "scifi"}, follow_redirects=False)
    # new tag now filters the library; the replaced one no longer matches
    assert "Blue Sky" in client.get("/", params={"tag": "scifi"}).text
    assert "Blue Sky" not in client.get("/", params={"tag": "old"}).text


def test_update_tags_empty_clears(client, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga
    from doujin.search import get_manga_tags

    c = connect(db_path(tmp_path / "data"))
    mid = ingest_manga(
        c,
        title="Blue Sky",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=["one", "two"],
    )
    c.close()
    r = client.post(f"/manga/{mid}/tags", data={"tags": ""}, follow_redirects=False)
    assert r.status_code == 303
    c = connect(db_path(tmp_path / "data"))
    assert get_manga_tags(c, mid) == []
    c.close()


def test_update_tags_unknown_manga_404(client):
    assert client.post("/manga/99999/tags", data={"tags": "x"}).status_code == 404


def test_scan_lists_unimported(client, library):
    r = client.get("/scan")
    assert r.status_code == 200
    assert "Blue Sky" in r.text and "Forest" in r.text


def test_ingest_creates_then_hidden_from_scan(client, library):
    folder = str(library / "Aoi" / "Blue Sky")
    r = client.post(
        "/ingest",
        data={
            "folder_path": folder,
            "author": "Aoi",
            "title": "Blue Sky",
            "cover_rel_path": "1.png",
            "page_count": "11",
            "tags": "scifi, action",
        },
        follow_redirects=False,
    )
    assert r.status_code in (302, 303)
    # now it appears in library and not in scan
    assert "Blue Sky" in client.get("/").text
    assert "Blue Sky" not in client.get("/scan").text  # already imported


def test_rescan_flags_missing_and_refreshes_count(client, library, tmp_path):
    import shutil

    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    folder = str(library / "Mori" / "Forest")
    c = connect(db_path(tmp_path / "data"))
    mid = ingest_manga(
        c,
        title="Forest",
        author="Mori",
        folder_path=folder,
        cover_rel_path="1.png",
        page_count=999,
        tags=[],
    )
    c.close()
    # delete the folder on disk, then rescan
    shutil.rmtree(folder)
    r = client.post("/rescan", follow_redirects=False)
    assert r.status_code in (302, 303)
    c = connect(db_path(tmp_path / "data"))
    row = c.execute("SELECT missing FROM manga WHERE id=?", (mid,)).fetchone()
    c.close()
    assert row["missing"] == 1


def test_rescan_refreshes_page_count(client, library, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    folder = str(library / "Mori" / "Forest")
    c = connect(db_path(tmp_path / "data"))
    mid = ingest_manga(
        c,
        title="Forest",
        author="Mori",
        folder_path=folder,
        cover_rel_path="page1.png",
        page_count=999,
        tags=[],
    )
    c.close()
    r = client.post("/rescan", follow_redirects=False)
    assert r.status_code in (302, 303)
    c = connect(db_path(tmp_path / "data"))
    row = c.execute("SELECT page_count, missing FROM manga WHERE id=?", (mid,)).fetchone()
    c.close()
    assert row["page_count"] == 3  # refreshed from disk
    assert row["missing"] == 0


def test_filter_by_tag_name(client, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    c = connect(db_path(tmp_path / "data"))
    ingest_manga(
        c,
        title="Blue Sky",
        author="Aoi",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=["scifi"],
    )
    ingest_manga(
        c,
        title="Forest",
        author="Mori",
        folder_path="/p2",
        cover_rel_path=None,
        page_count=1,
        tags=["slice"],
    )
    c.close()
    # HTML grid filtered by tag name
    html = client.get("/", params={"tag": "scifi"}).text
    assert "Blue Sky" in html and "Forest" not in html
    # unknown tag -> no results
    none = client.get("/", params={"tag": "doesnotexist"}).text
    assert "Blue Sky" not in none and "Forest" not in none
    # api
    data = client.get("/api/search", params={"tag": "scifi"}).json()
    assert [m["title"] for m in data] == ["Blue Sky"]


def test_api_search_pagination(client, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    c = connect(db_path(tmp_path / "data"))
    for i in range(5):
        ingest_manga(
            c,
            title=f"T{i}",
            author="A",
            folder_path=f"/p{i}",
            cover_rel_path=None,
            page_count=1,
            tags=[],
        )
    c.close()
    p0 = client.get("/api/search", params={"limit": 2, "offset": 0, "sort": "title"}).json()
    p1 = client.get("/api/search", params={"limit": 2, "offset": 2, "sort": "title"}).json()
    assert [m["title"] for m in p0] == ["T0", "T1"]
    assert [m["title"] for m in p1] == ["T2", "T3"]
    assert len(client.get("/api/search", params={"limit": 2, "offset": 4}).json()) == 1


def test_home_grid_has_pagination_attrs(client, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    c = connect(db_path(tmp_path / "data"))
    ingest_manga(
        c,
        title="Only One",
        author="A",
        folder_path="/p1",
        cover_rel_path=None,
        page_count=1,
        tags=[],
    )
    c.close()
    html = client.get("/").text
    assert 'id="grid"' in html
    assert 'data-page-size="60"' in html
    assert 'data-next-offset="1"' in html  # one card rendered
    assert 'id="scroll-sentinel"' in html


def test_import_all_confirm_shows_count(client, library):
    r = client.get("/import-all")
    assert r.status_code == 200
    assert "Import all 2" in r.text  # library fixture: Blue Sky + Forest


def test_import_all_imports_everything(client, library, tmp_path):
    r = client.post("/import-all", follow_redirects=False)
    assert r.status_code == 303
    home = client.get("/").text
    assert "Blue Sky" in home and "Forest" in home
    assert "Nothing new" in client.get("/import-all").text  # idempotent: none left
    client.post("/import-all", follow_redirects=False)  # second run is a no-op
    from doujin.config import db_path
    from doujin.db import connect

    c = connect(db_path(tmp_path / "data"))
    n = c.execute("SELECT COUNT(*) AS c FROM manga").fetchone()["c"]
    c.close()
    assert n == 2  # no duplicates


def test_api_search_clamps_negative_offset(client, tmp_path):
    from doujin.config import db_path
    from doujin.db import connect
    from doujin.ingest import ingest_manga

    c = connect(db_path(tmp_path / "data"))
    ingest_manga(
        c, title="T0", author="A", folder_path="/p0", cover_rel_path=None, page_count=1, tags=[]
    )
    c.close()
    r = client.get("/api/search", params={"offset": -5})  # must not 500
    assert r.status_code == 200
    assert [m["title"] for m in r.json()] == ["T0"]
