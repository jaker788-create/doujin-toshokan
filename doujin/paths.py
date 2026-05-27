# doujin/paths.py
from __future__ import annotations

from pathlib import Path


def is_within_roots(path: str, roots: list[str]) -> bool:
    try:
        target = Path(path).resolve()
    except OSError:
        return False
    for root in roots:
        try:
            rootp = Path(root).resolve()
        except OSError:
            continue
        if target == rootp or rootp in target.parents:
            return True
    return False
