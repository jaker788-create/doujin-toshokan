from pathlib import Path

import pytest
from PIL import Image

from doujin.db import connect, init_db


def _png(path: Path, color=(120, 120, 120)) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    Image.new("RGB", (60, 90), color).save(path, "PNG")


@pytest.fixture
def library(tmp_path) -> Path:
    """A fake library: root/<author>/<title>/<pages>.png"""
    root = tmp_path / "lib"
    # Author "Aoi" -> title "Blue Sky" with 11 pages (tests natural sort 2<10)
    blue = root / "Aoi" / "Blue Sky"
    for i in range(1, 12):
        _png(blue / f"{i}.png")
    # Author "Mori" -> title "Forest" with 3 pages
    forest = root / "Mori" / "Forest"
    for i in range(1, 4):
        _png(forest / f"page{i}.png")
    # A non-image stray file and an empty dir (should be ignored)
    (root / "Aoi" / "Blue Sky" / "notes.txt").write_text("ignore me")
    (root / "Empty" / "Nothing").mkdir(parents=True)
    return root


@pytest.fixture
def conn(tmp_path):
    c = connect(tmp_path / "doujin.db")
    init_db(c)
    yield c
    c.close()
