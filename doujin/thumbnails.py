# doujin/thumbnails.py
from __future__ import annotations

import hashlib
import os
import tempfile
from pathlib import Path

from PIL import Image


def cache_key(src: Path, width: int) -> str:
    st = src.stat()
    raw = f"{src}|{st.st_mtime_ns}|{st.st_size}|{width}"
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


def _placeholder(cache_dir: Path, width: int) -> Path:
    out = cache_dir / f"_placeholder_{width}.jpg"
    if not out.exists():
        h = max(1, int(width * 1.4))
        Image.new("RGB", (width, h), (40, 40, 40)).save(out, "JPEG", quality=70)
    return out


def get_thumbnail(src: Path, width: int, cache_dir: Path) -> Path:
    """Return a path to a cached JPEG thumbnail of ``src`` at ``width`` px.

    Generated on first request and cached on disk; later requests with an
    unchanged source return the cached file. Images are never upscaled: if the
    source is already narrower than ``width`` it is stored at its original
    width. Unreadable/corrupt sources return a placeholder image of the
    requested width instead of raising.
    """
    cache_dir.mkdir(parents=True, exist_ok=True)
    try:
        key = cache_key(src, width)
    except OSError:
        return _placeholder(cache_dir, width)
    out = cache_dir / f"{key}.jpg"
    if out.exists():
        return out
    try:
        with Image.open(src) as im:
            im = im.convert("RGB")
            w, h = im.size
            if w > width:
                im = im.resize((width, max(1, int(h * width / w))))
            # Write to a temp file then atomically replace, so an interrupted
            # write can never leave a corrupt thumbnail that later reads as a
            # cache hit.
            fd, tmp = tempfile.mkstemp(dir=cache_dir, suffix=".jpg")
            try:
                os.close(fd)
                im.save(tmp, "JPEG", quality=85)
                os.replace(tmp, out)
            except Exception:
                try:
                    os.unlink(tmp)
                except OSError:
                    pass
                raise
        return out
    except Exception:
        return _placeholder(cache_dir, width)
