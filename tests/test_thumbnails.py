# tests/test_thumbnails.py
from PIL import Image

from doujin.thumbnails import get_thumbnail


def test_thumbnail_generated_and_resized(library, tmp_path):
    src = library / "Aoi" / "Blue Sky" / "1.png"
    cache = tmp_path / "thumbs"
    out = get_thumbnail(src, width=30, cache_dir=cache)
    assert out.exists()
    with Image.open(out) as im:
        assert im.width == 30


def test_thumbnail_cache_hit(library, tmp_path):
    src = library / "Aoi" / "Blue Sky" / "1.png"
    cache = tmp_path / "thumbs"
    first = get_thumbnail(src, width=30, cache_dir=cache)
    mtime1 = first.stat().st_mtime_ns
    second = get_thumbnail(src, width=30, cache_dir=cache)
    assert second == first
    assert second.stat().st_mtime_ns == mtime1  # not regenerated


def test_no_upscale_when_source_narrower(library, tmp_path):
    # Fixture pages are 60px wide; requesting a larger width must NOT upscale.
    src = library / "Aoi" / "Blue Sky" / "1.png"
    cache = tmp_path / "thumbs"
    out = get_thumbnail(src, width=200, cache_dir=cache)
    with Image.open(out) as im:
        assert im.width == 60


def test_corrupt_image_returns_placeholder(tmp_path):
    bad = tmp_path / "broken.png"
    bad.write_bytes(b"not an image")
    cache = tmp_path / "thumbs"
    out = get_thumbnail(bad, width=30, cache_dir=cache)
    assert out.exists()
    with Image.open(out) as im:  # placeholder is a valid image
        assert im.width == 30
