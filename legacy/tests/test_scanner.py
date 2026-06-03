# tests/test_scanner.py
from doujin.scanner import detect_folder, find_unimported, list_pages


def test_natural_key_orders_numbers(library):
    folder = library / "Aoi" / "Blue Sky"
    names = [p.name for p in list_pages(folder)]
    # "2.png" must come before "10.png"
    assert names.index("2.png") < names.index("10.png")
    # the stray .txt is excluded
    assert "notes.txt" not in names
    assert len(names) == 11


def test_detect_folder_reads_author_title(library):
    folder = library / "Aoi" / "Blue Sky"
    d = detect_folder(folder)
    assert d.author == "Aoi"
    assert d.title == "Blue Sky"
    assert d.page_count == 11
    assert d.cover_rel_path == "1.png"
    assert d.folder_path == str(folder)


def test_detect_folder_none_when_no_images(library):
    assert detect_folder(library / "Empty" / "Nothing") is None


def test_find_unimported_excludes_known(library):
    all_found = find_unimported([str(library)], known_paths=set())
    assert {d.title for d in all_found} == {"Blue Sky", "Forest"}
    known = {str(library / "Aoi" / "Blue Sky")}
    remaining = find_unimported([str(library)], known_paths=known)
    assert {d.title for d in remaining} == {"Forest"}
