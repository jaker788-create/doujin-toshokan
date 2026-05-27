# tests/test_paths.py
from doujin.paths import is_within_roots


def test_path_inside_root_allowed(tmp_path):
    root = tmp_path / "lib"
    (root / "a").mkdir(parents=True)
    target = root / "a" / "1.png"
    target.write_bytes(b"x")
    assert is_within_roots(str(target), [str(root)]) is True


def test_path_outside_root_rejected(tmp_path):
    root = tmp_path / "lib"
    root.mkdir()
    outside = tmp_path / "secret.txt"
    outside.write_bytes(b"x")
    assert is_within_roots(str(outside), [str(root)]) is False


def test_traversal_rejected(tmp_path):
    root = tmp_path / "lib"
    root.mkdir()
    sneaky = str(root / ".." / "secret.txt")
    assert is_within_roots(sneaky, [str(root)]) is False
