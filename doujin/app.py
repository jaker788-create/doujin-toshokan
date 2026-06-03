from __future__ import annotations

import sqlite3
from pathlib import Path
from typing import Annotated

from fastapi import Depends, FastAPI, Form, HTTPException, Query, Request
from fastapi.responses import FileResponse, HTMLResponse, JSONResponse, RedirectResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates

from doujin import ingest, scanner, search
from doujin.config import db_path, load_config, thumb_cache_dir
from doujin.db import connect, init_db
from doujin.paths import is_within_roots
from doujin.thumbnails import get_thumbnail

_HERE = Path(__file__).parent
PAGE_SIZE = 60


def create_app(data_dir: Path) -> FastAPI:
    dbp = db_path(data_dir)
    conn0 = connect(dbp)
    init_db(conn0)
    conn0.close()

    app = FastAPI(title="Doujin Toshokan")
    app.state.data_dir = data_dir
    templates = Jinja2Templates(directory=str(_HERE / "templates"))
    app.mount("/static", StaticFiles(directory=str(_HERE / "static")), name="static")

    def get_conn():
        c = connect(dbp)
        try:
            yield c
        finally:
            c.close()

    def roots() -> list[str]:
        return load_config(data_dir).library_roots

    # /image and /thumb take an OS-native filesystem path as the `path` query
    # param. is_within_roots() canonicalizes it via Path.resolve() and confirms
    # it lives under a configured library root before any file access. On
    # Windows both '/' and '\' separators resolve correctly, so the mixed-
    # separator URLs the templates build are safe.
    @app.get("/image")
    def image(path: str):
        if not is_within_roots(path, roots()):
            raise HTTPException(status_code=403, detail="forbidden")
        if not Path(path).is_file():
            raise HTTPException(status_code=404, detail="not found")
        return FileResponse(path)

    @app.get("/thumb")
    def thumb(path: str, w: int = 240):
        if not is_within_roots(path, roots()):
            raise HTTPException(status_code=403, detail="forbidden")
        if not Path(path).is_file():
            raise HTTPException(status_code=404, detail="not found")
        out = get_thumbnail(Path(path), width=w, cache_dir=thumb_cache_dir(data_dir))
        return FileResponse(out, media_type="image/jpeg")

    _register_pages(app, templates, get_conn, data_dir, roots)
    return app


def _register_pages(app, templates, get_conn, data_dir, roots):
    @app.get("/", response_class=HTMLResponse)
    def home(
        request: Request,
        q: str = "",
        author: int | None = None,
        tag: Annotated[list[str], Query()] = [],
        sort: str = "title",
        conn=Depends(get_conn),
    ):
        if tag:
            tag_ids = search.tag_ids_for_names(conn, tag)
            rows = (
                []
                if not tag_ids
                else search.search_manga(
                    conn,
                    query=q or None,
                    author_id=author,
                    tag_ids=tag_ids,
                    sort=sort,
                    limit=PAGE_SIZE,
                    offset=0,
                )
            )
        else:
            rows = search.search_manga(
                conn,
                query=q or None,
                author_id=author,
                tag_ids=None,
                sort=sort,
                limit=PAGE_SIZE,
                offset=0,
            )
        # Resolve the author's display name so the filter chip can read
        # "author: Jane Doe" instead of a bare "author". A dangling id (no row)
        # falls back to "" and the chip is suppressed — no crash.
        a = search.get_author(conn, author) if author else None
        author_name = a["name"] if a else ""
        total_count = conn.execute("SELECT COUNT(*) FROM manga").fetchone()[0]
        return templates.TemplateResponse(
            request=request,
            name="library.html",
            context={
                "manga": rows,
                "q": q,
                "sort": sort,
                "author": author or "",
                "author_name": author_name,
                "tags_selected": tag,
                "next_offset": len(rows),
                "page_size": PAGE_SIZE,
                "total_count": total_count,
            },
        )

    @app.get("/api/search")
    def api_search(
        q: str = "",
        author: int | None = None,
        tag: Annotated[list[str], Query()] = [],
        sort: str = "title",
        limit: int = PAGE_SIZE,
        offset: int = 0,
        conn=Depends(get_conn),
    ):
        # Clamp client-supplied paging: a negative OFFSET makes SQLite raise
        # (would surface as a 500), and an unbounded limit could pull everything.
        limit = max(1, min(limit, 500))
        offset = max(0, offset)
        if tag:
            tag_ids = search.tag_ids_for_names(conn, tag)
            rows = (
                []
                if not tag_ids
                else search.search_manga(
                    conn,
                    query=q or None,
                    author_id=author,
                    tag_ids=tag_ids,
                    sort=sort,
                    limit=limit,
                    offset=offset,
                )
            )
        else:
            rows = search.search_manga(
                conn,
                query=q or None,
                author_id=author,
                tag_ids=None,
                sort=sort,
                limit=limit,
                offset=offset,
            )
        return JSONResponse(
            [
                {
                    "id": r["id"],
                    "title": r["title"],
                    "author": r["author_name"],
                    "author_id": r["author_id"],
                    "folder_path": r["folder_path"],
                    "cover_rel_path": r["cover_rel_path"],
                }
                for r in rows
            ]
        )

    @app.get("/api/tags/suggest")
    def api_tag_suggest(q: str = "", conn=Depends(get_conn)):
        return JSONResponse([r["name"] for r in search.suggest_tags(conn, q)])

    @app.get("/api/authors/suggest")
    def api_author_suggest(q: str = "", conn=Depends(get_conn)):
        # Returns {id, name}: the builder needs the id because author filtering is
        # by integer id (m.author_id = ?), unlike tags which filter by name.
        return JSONResponse(
            [{"id": r["id"], "name": r["name"]} for r in search.suggest_authors(conn, q)]
        )

    @app.get("/manga/{manga_id}", response_class=HTMLResponse)
    def title_page(manga_id: int, request: Request, conn=Depends(get_conn)):
        m = search.get_manga(conn, manga_id)
        if not m:
            raise HTTPException(status_code=404, detail="not found")
        folder = Path(m["folder_path"])
        pages = [str(p) for p in scanner.list_pages(folder)] if folder.exists() else []
        return templates.TemplateResponse(
            request=request,
            name="title.html",
            context={
                "m": m,
                "pages": pages,
                "tags": search.get_manga_tags(conn, manga_id),
                "missing": not folder.exists(),
            },
        )

    @app.post("/manga/{manga_id}/tags")
    def update_tags(manga_id: int, tags: str = Form(""), conn=Depends(get_conn)):
        # 404 on an unknown id so a stale page can't create a tag set for a
        # manga row that no longer exists.
        if search.get_manga(conn, manga_id) is None:
            raise HTTPException(status_code=404, detail="not found")
        # Same comma-split as /ingest: trim each token, drop blanks. Normalizing
        # and de-duping happen inside set_manga_tags.
        tag_list = [t for t in (s.strip() for s in tags.split(",")) if t]
        ingest.set_manga_tags(conn, manga_id, tag_list)
        # Redirect back to the title page so the no-JS path reloads with the new
        # tags; app.js hijacks the submit to update the chips in place instead.
        return RedirectResponse(url=f"/manga/{manga_id}", status_code=303)

    @app.get("/scan", response_class=HTMLResponse)
    def scan_page(request: Request, conn=Depends(get_conn)):
        known = {r["folder_path"] for r in conn.execute("SELECT folder_path FROM manga")}
        found = scanner.find_unimported(roots(), known)
        return templates.TemplateResponse(
            request=request,
            name="scan.html",
            context={"found": found},
        )

    @app.post("/ingest")
    def do_ingest(
        folder_path: str = Form(...),
        author: str = Form(...),
        title: str = Form(...),
        cover_rel_path: str = Form(""),
        page_count: int = Form(0),
        tags: str = Form(""),
        conn=Depends(get_conn),
    ):
        tag_list = [t for t in (s.strip() for s in tags.split(",")) if t]
        try:
            ingest.ingest_manga(
                conn,
                title=title,
                author=author,
                folder_path=folder_path,
                cover_rel_path=cover_rel_path or None,
                page_count=page_count,
                tags=tag_list,
            )
        except sqlite3.IntegrityError:
            pass  # duplicate folder_path -> already imported; redirect silently
        return RedirectResponse(url="/scan", status_code=303)

    @app.get("/import-all", response_class=HTMLResponse)
    def import_all_confirm(request: Request, conn=Depends(get_conn)):
        known = {r["folder_path"] for r in conn.execute("SELECT folder_path FROM manga")}
        found = scanner.find_unimported(roots(), known)
        return templates.TemplateResponse(
            request=request, name="import_all.html", context={"count": len(found)}
        )

    @app.post("/import-all")
    def do_import_all(conn=Depends(get_conn)):
        known = {r["folder_path"] for r in conn.execute("SELECT folder_path FROM manga")}
        for d in scanner.find_unimported(roots(), known):
            try:
                ingest.ingest_manga(
                    conn,
                    title=d.title,
                    author=d.author,
                    folder_path=d.folder_path,
                    cover_rel_path=d.cover_rel_path,
                    page_count=d.page_count,
                    tags=[],
                )
            except sqlite3.IntegrityError:
                pass  # already imported -> skip
        return RedirectResponse(url="/", status_code=303)

    @app.post("/rescan")
    def rescan(conn=Depends(get_conn)):
        for row in conn.execute("SELECT id, folder_path FROM manga").fetchall():
            folder = Path(row["folder_path"])
            if not folder.exists():
                conn.execute("UPDATE manga SET missing=1 WHERE id=?", (row["id"],))
                continue
            pages = scanner.list_pages(folder)
            conn.execute(
                "UPDATE manga SET missing=0, page_count=? WHERE id=?", (len(pages), row["id"])
            )
        conn.commit()
        return RedirectResponse(url="/", status_code=303)
