from fastapi import FastAPI

from doujin.cli import build_app_for_cli


def test_build_app_creates_data_dir(tmp_path, monkeypatch):
    monkeypatch.setenv("DOUJIN_DATA_DIR", str(tmp_path / "data"))
    app = build_app_for_cli()
    assert isinstance(app, FastAPI)
    assert (tmp_path / "data").exists()
