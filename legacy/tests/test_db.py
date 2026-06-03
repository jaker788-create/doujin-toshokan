import doujin.db as db_module
from doujin.db import SCHEMA, connect, init_db


def test_init_creates_tables(tmp_path):
    conn = connect(tmp_path / "doujin.db")
    try:
        init_db(conn)
        names = {
            r["name"] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")
        }
        assert {"authors", "manga", "tags", "manga_tags"} <= names
    finally:
        conn.close()


def test_foreign_keys_enabled(tmp_path):
    conn = connect(tmp_path / "doujin.db")
    try:
        assert conn.execute("PRAGMA foreign_keys").fetchone()[0] == 1
    finally:
        conn.close()


def test_row_factory_returns_mapping(tmp_path):
    conn = connect(tmp_path / "doujin.db")
    try:
        init_db(conn)
        conn.execute("INSERT INTO authors(name) VALUES ('Aoi')")
        row = conn.execute("SELECT * FROM authors").fetchone()
        assert row["name"] == "Aoi"
    finally:
        conn.close()


def test_init_stamps_latest_user_version(tmp_path):
    conn = connect(tmp_path / "doujin.db")
    try:
        init_db(conn)
        version = conn.execute("PRAGMA user_version").fetchone()[0]
        assert version == len(db_module.MIGRATIONS)
    finally:
        conn.close()


def test_init_is_idempotent(tmp_path):
    # Running init twice must not error and must leave the version stable.
    conn = connect(tmp_path / "doujin.db")
    try:
        init_db(conn)
        init_db(conn)
        version = conn.execute("PRAGMA user_version").fetchone()[0]
        assert version == len(db_module.MIGRATIONS)
        names = {
            r["name"] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")
        }
        assert {"authors", "manga", "tags", "manga_tags"} <= names
    finally:
        conn.close()


def test_init_preserves_existing_data(tmp_path):
    # Re-running migrations on a populated DB must never drop rows.
    path = tmp_path / "doujin.db"
    conn = connect(path)
    try:
        init_db(conn)
        conn.execute("INSERT INTO authors(name) VALUES ('Aoi')")
        conn.commit()
    finally:
        conn.close()
    conn = connect(path)
    try:
        init_db(conn)
        row = conn.execute("SELECT name FROM authors").fetchone()
        assert row["name"] == "Aoi"
    finally:
        conn.close()


def test_legacy_db_at_version_zero_upgrades(tmp_path):
    # Simulate a database created before the migration system existed: the
    # baseline tables already exist but user_version is still 0. init_db must
    # detect the gap, run cleanly (the IF NOT EXISTS baseline is a no-op), and
    # stamp it up to the latest version without disturbing existing rows.
    path = tmp_path / "doujin.db"
    conn = connect(path)
    try:
        conn.executescript(SCHEMA)  # old-style direct create
        conn.execute("INSERT INTO authors(name) VALUES ('Legacy')")
        conn.commit()
        assert conn.execute("PRAGMA user_version").fetchone()[0] == 0
    finally:
        conn.close()
    conn = connect(path)
    try:
        init_db(conn)
        assert conn.execute("PRAGMA user_version").fetchone()[0] == len(db_module.MIGRATIONS)
        assert conn.execute("SELECT name FROM authors").fetchone()["name"] == "Legacy"
    finally:
        conn.close()


def test_runner_applies_only_pending_migrations(tmp_path, monkeypatch):
    # Mechanism test: append a second migration to the ladder and confirm the
    # runner applies just the pending one and advances user_version to match.
    applied = []

    def fake_second(conn):
        applied.append("second")
        conn.execute("CREATE TABLE IF NOT EXISTS extra (id INTEGER PRIMARY KEY)")

    path = tmp_path / "doujin.db"
    conn = connect(path)
    try:
        init_db(conn)  # ladder length 1 -> version 1
        assert conn.execute("PRAGMA user_version").fetchone()[0] == 1
    finally:
        conn.close()

    monkeypatch.setattr(db_module, "MIGRATIONS", db_module.MIGRATIONS + [fake_second])
    conn = connect(path)
    try:
        init_db(conn)  # only the new one should run
        assert applied == ["second"]
        assert conn.execute("PRAGMA user_version").fetchone()[0] == 2
        names = {
            r["name"] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")
        }
        assert "extra" in names
    finally:
        conn.close()
