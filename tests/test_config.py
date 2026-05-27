from doujin.config import (
    Config,
    db_path,
    load_config,
    migrate_legacy_data_dir,
    save_config,
    thumb_cache_dir,
)


def test_save_and_load_roundtrip(tmp_path):
    cfg = Config(library_roots=[str(tmp_path / "lib")], port=9000)
    save_config(cfg, tmp_path)
    loaded = load_config(tmp_path)
    assert loaded.library_roots == [str(tmp_path / "lib")]
    assert loaded.port == 9000


def test_load_missing_returns_default(tmp_path):
    cfg = load_config(tmp_path)
    assert cfg.library_roots == []
    assert cfg.port == 8765


def test_path_helpers(tmp_path):
    assert db_path(tmp_path) == tmp_path / "doujin.db"
    assert thumb_cache_dir(tmp_path) == tmp_path / "thumbs"


def test_migrate_moves_legacy_dir_and_renames_db(tmp_path):
    legacy = tmp_path / "stash"
    legacy.mkdir()
    (legacy / "stash.db").write_text("dbdata", encoding="utf-8")
    (legacy / "config.json").write_text("{}", encoding="utf-8")
    (legacy / "thumbs").mkdir()
    new = tmp_path / "doujin"

    migrate_legacy_data_dir(new)

    assert new.is_dir()
    assert not legacy.exists()
    assert (new / "doujin.db").read_text(encoding="utf-8") == "dbdata"
    assert not (new / "stash.db").exists()
    assert (new / "config.json").exists()
    assert (new / "thumbs").is_dir()


def test_migrate_is_noop_when_new_dir_exists(tmp_path):
    legacy = tmp_path / "stash"
    legacy.mkdir()
    (legacy / "stash.db").write_text("legacy", encoding="utf-8")
    new = tmp_path / "doujin"
    new.mkdir()
    (new / "doujin.db").write_text("current", encoding="utf-8")

    migrate_legacy_data_dir(new)

    # Existing data must not be clobbered, and the legacy dir is left untouched.
    assert (new / "doujin.db").read_text(encoding="utf-8") == "current"
    assert legacy.exists()


def test_migrate_is_noop_when_no_legacy_dir(tmp_path):
    new = tmp_path / "doujin"
    migrate_legacy_data_dir(new)
    assert not new.exists()
