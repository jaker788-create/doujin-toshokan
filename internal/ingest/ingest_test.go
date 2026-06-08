package ingest

import (
	"database/sql"
	"path/filepath"
	"sort"
	"testing"

	"doujin/internal/store"
	"doujin/internal/tag"
)

// gen wraps tag names as untyped/General typed tags (what the manual edit path sends).
func gen(names ...string) []tag.Typed {
	out := make([]tag.Typed, len(names))
	for i, n := range names {
		out[i] = tag.Typed{Name: n}
	}
	return out
}

// names extracts the names from typed tags, for comparison against expected slices.
func names(ts []tag.Typed) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "doujin.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Init(db); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sp(s string) *string { return &s }

func tagsOf(t *testing.T, db *sql.DB, mangaID int64) []string {
	t.Helper()
	rows, err := db.Query(
		"SELECT t.name FROM tags t JOIN manga_tags mt ON mt.tag_id=t.id "+
			"WHERE mt.manga_id=? ORDER BY t.name", mangaID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNormalizeTag(t *testing.T) {
	if got := NormalizeTag("  SciFi "); got != "scifi" {
		t.Errorf("NormalizeTag = %q, want scifi", got)
	}
}

func TestIngestCreatesRowsAndTags(t *testing.T) {
	db := newDB(t)
	mid, err := IngestManga(db, MangaInput{
		Title: "Blue Sky", Author: "Aoi", FolderPath: "/lib/Aoi/Blue Sky",
		CoverRelPath: sp("1.png"), PageCount: 11,
		Tags: gen("Action", "scifi", "Action"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var title string
	var pageCount int
	var authorID int64
	if err := db.QueryRow("SELECT title, page_count, author_id FROM manga WHERE id=?", mid).
		Scan(&title, &pageCount, &authorID); err != nil {
		t.Fatal(err)
	}
	if title != "Blue Sky" || pageCount != 11 {
		t.Errorf("title/page_count = %q/%d", title, pageCount)
	}
	var author string
	if err := db.QueryRow("SELECT name FROM authors WHERE id=?", authorID).Scan(&author); err != nil {
		t.Fatal(err)
	}
	if author != "Aoi" {
		t.Errorf("author = %q, want Aoi", author)
	}
	got := tagsOf(t, db, mid)
	sort.Strings(got)
	if !eq(got, []string{"action", "scifi"}) {
		t.Errorf("tags = %v, want [action scifi]", got)
	}
}

func TestAuthorReused(t *testing.T) {
	db := newDB(t)
	for _, p := range []string{"/p1", "/p2"} {
		if _, err := IngestManga(db, MangaInput{Title: "A", Author: "Aoi", FolderPath: p, PageCount: 1}); err != nil {
			t.Fatal(err)
		}
	}
	var c int
	if err := db.QueryRow("SELECT COUNT(*) FROM authors").Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Errorf("author count = %d, want 1", c)
	}
}

func TestSetMangaTagsNormalizesDedupesAndSorts(t *testing.T) {
	db := newDB(t)
	mid, err := IngestManga(db, MangaInput{Title: "A", Author: "Aoi", FolderPath: "/p1", PageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SetMangaTags(db, mid, gen("  SciFi ", "action", "Action", "", "  "))
	if err != nil {
		t.Fatal(err)
	}
	if !eq(names(saved), []string{"action", "scifi"}) {
		t.Errorf("saved = %v, want [action scifi]", saved)
	}
	if !eq(tagsOf(t, db, mid), []string{"action", "scifi"}) {
		t.Errorf("stored = %v", tagsOf(t, db, mid))
	}
}

func TestSetMangaTagsReplacesExisting(t *testing.T) {
	db := newDB(t)
	mid, err := IngestManga(db, MangaInput{
		Title: "A", Author: "Aoi", FolderPath: "/p1", PageCount: 1,
		Tags: gen("old", "stale"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetMangaTags(db, mid, gen("fresh")); err != nil {
		t.Fatal(err)
	}
	if !eq(tagsOf(t, db, mid), []string{"fresh"}) {
		t.Errorf("tags = %v, want [fresh]", tagsOf(t, db, mid))
	}
}

func TestSetMangaTagsEmptyClears(t *testing.T) {
	db := newDB(t)
	mid, err := IngestManga(db, MangaInput{
		Title: "A", Author: "Aoi", FolderPath: "/p1", PageCount: 1,
		Tags: gen("one", "two"),
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SetMangaTags(db, mid, gen())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 0 || len(tagsOf(t, db, mid)) != 0 {
		t.Errorf("expected cleared tags, got saved=%v stored=%v", saved, tagsOf(t, db, mid))
	}
}

func TestSetMangaTagsReusesExistingTagRows(t *testing.T) {
	db := newDB(t)
	a, err := IngestManga(db, MangaInput{
		Title: "A", Author: "Aoi", FolderPath: "/p1", PageCount: 1, Tags: gen("shared"),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := IngestManga(db, MangaInput{Title: "B", Author: "Aoi", FolderPath: "/p2", PageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetMangaTags(db, b, gen("shared")); err != nil {
		t.Fatal(err)
	}
	var c int
	if err := db.QueryRow("SELECT COUNT(*) FROM tags WHERE name='shared'").Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Errorf("shared tag rows = %d, want 1", c)
	}
	if !eq(tagsOf(t, db, a), []string{"shared"}) || !eq(tagsOf(t, db, b), []string{"shared"}) {
		t.Error("both manga should point at the same shared tag")
	}
}

func tagType(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var typ string
	if err := db.QueryRow("SELECT type FROM tags WHERE name=?", name).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	return typ
}

func TestGetOrCreateTagUpgradesButNeverDowngrades(t *testing.T) {
	db := newDB(t)
	id1, err := GetOrCreateTag(db, "english", tag.General)
	if err != nil {
		t.Fatal(err)
	}
	// A meaningful subject upgrades the same row in place.
	id2, err := GetOrCreateTag(db, "english", tag.Language)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("expected the same tag row, got %d then %d", id1, id2)
	}
	if got := tagType(t, db, "english"); got != tag.Language {
		t.Errorf("type = %q, want language after upgrade", got)
	}
	// Re-saving as General must NOT strip the subject.
	if _, err := GetOrCreateTag(db, "english", tag.General); err != nil {
		t.Fatal(err)
	}
	if got := tagType(t, db, "english"); got != tag.Language {
		t.Errorf("type = %q, want language preserved (no downgrade)", got)
	}
}

func TestSetMangaTagsPreservesSubjectOnFreeformEdit(t *testing.T) {
	db := newDB(t)
	mid, err := IngestManga(db, MangaInput{
		Title: "A", Author: "Aoi", FolderPath: "/p1", PageCount: 1,
		Tags: []tag.Typed{{Name: "english", Type: tag.Language}, {Name: "naruto", Type: tag.Parody}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the reader's freeform editor: every tag re-saved as a plain name, plus a
	// new manual one. The subjects of the existing typed tags must survive.
	saved, err := SetMangaTags(db, mid, gen("english", "naruto", "myfav"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tt := range saved {
		got[tt.Name] = tt.Type
	}
	if got["english"] != tag.Language || got["naruto"] != tag.Parody || got["myfav"] != tag.General {
		t.Errorf("subjects not preserved: %v", got)
	}
	// Saved order is by subject rank then name: language, parody, then general.
	if names(saved)[0] != "english" || names(saved)[len(saved)-1] != "myfav" {
		t.Errorf("order = %v, want language first, general last", names(saved))
	}
}

func TestDuplicateFolderPathRejected(t *testing.T) {
	db := newDB(t)
	if _, err := IngestManga(db, MangaInput{Title: "A", Author: "Aoi", FolderPath: "/dup", PageCount: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := IngestManga(db, MangaInput{Title: "A2", Author: "Aoi", FolderPath: "/dup", PageCount: 1}); err == nil {
		t.Error("expected an error inserting a duplicate folder_path")
	}
}
