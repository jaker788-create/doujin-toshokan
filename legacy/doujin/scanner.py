# doujin/scanner.py
from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import Path

IMAGE_EXTS = {".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".avif"}


def natural_key(s: str) -> list:
    return [int(t) if t.isdigit() else t.lower() for t in re.split(r"(\d+)", s)]


def list_pages(folder: Path) -> list[Path]:
    try:
        entries = list(folder.iterdir())
    except OSError:
        return []
    files = [p for p in entries if p.is_file() and p.suffix.lower() in IMAGE_EXTS]
    return sorted(files, key=lambda p: natural_key(p.name))


@dataclass
class DetectedFolder:
    folder_path: str
    author: str
    title: str
    page_count: int
    cover_rel_path: str | None


def detect_folder(folder: Path) -> DetectedFolder | None:
    pages = list_pages(folder)
    if not pages:
        return None
    return DetectedFolder(
        folder_path=str(folder),
        author=folder.parent.name,
        title=folder.name,
        page_count=len(pages),
        cover_rel_path=pages[0].name,
    )


def _sorted_subdirs(folder: Path) -> list[Path]:
    """Subdirectories of ``folder``, natural-sorted; [] if unreadable."""
    try:
        entries = [d for d in folder.iterdir() if d.is_dir()]
    except OSError:
        return []
    return sorted(entries, key=lambda p: natural_key(p.name))


def scan_root(root: Path) -> list[DetectedFolder]:
    """Walk root/<author>/<title>/ and detect title folders with images.

    Unreadable directories (e.g. permission-denied) are skipped rather than
    aborting the whole scan.
    """
    results: list[DetectedFolder] = []
    if not root.exists():
        return results
    for author_dir in _sorted_subdirs(root):
        for title_dir in _sorted_subdirs(author_dir):
            d = detect_folder(title_dir)
            if d:
                results.append(d)
    return results


def find_unimported(roots: list[str], known_paths: set[str]) -> list[DetectedFolder]:
    detected: list[DetectedFolder] = []
    for root in roots:
        detected.extend(scan_root(Path(root)))
    return [d for d in detected if d.folder_path not in known_paths]
