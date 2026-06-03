package stash

import (
	"database/sql"
	"path/filepath"
	"testing"

	"doujin/internal/store"
)

// newDB opens a migrated temp database seeded with one author and one manga, so
// title entries have a row to LEFT JOIN against.
func newDB(t *testing.T) (*sql.DB, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "doujin.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Init(db); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := db.Exec("INSERT INTO authors(id, name) VALUES (1, 'Aoi')"); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(
		"INSERT INTO manga(title, author_id, folder_path, cover_rel_path, page_count, date_added, date_modified) "+
			"VALUES ('Foo', 1, '/lib/aoi/foo', 'p1.jpg', 20, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')",
	)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return db, id
}

func TestAddAndListSearchEntry(t *testing.T) {
	db, _ := newDB(t)
	id, err := Add(db, Entry{Kind: KindSearch, Hash: "/?author=1&tag=x", Label: "author: Aoi"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id == 0 {
		t.Fatal("Add returned id 0")
	}
	list, err := List(db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	e := list[0]
	if e.Kind != KindSearch || e.Hash != "/?author=1&tag=x" || e.Label != "author: Aoi" {
		t.Errorf("unexpected entry %+v", e)
	}
	if e.MangaID != nil {
		t.Errorf("search entry MangaID = %v, want nil", *e.MangaID)
	}
}

func TestAddTitleEntryJoinsManga(t *testing.T) {
	db, mangaID := newDB(t)
	if _, err := Add(db, Entry{
		Kind: KindTitle, Hash: "/manga/1", Label: "Foo", MangaID: &mangaID, LastPage: 5,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	list, err := List(db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	e := list[0]
	if e.MangaID == nil || *e.MangaID != mangaID {
		t.Fatalf("MangaID = %v, want %d", e.MangaID, mangaID)
	}
	if e.Title != "Foo" || e.AuthorName != "Aoi" || e.FolderPath != "/lib/aoi/foo" {
		t.Errorf("joined manga fields wrong: %+v", e)
	}
	if e.CoverRelPath == nil || *e.CoverRelPath != "p1.jpg" {
		t.Errorf("CoverRelPath = %v, want p1.jpg", e.CoverRelPath)
	}
	if e.LastPage != 5 {
		t.Errorf("LastPage = %d, want 5", e.LastPage)
	}
}

func TestGetAndSetPage(t *testing.T) {
	db, mangaID := newDB(t)
	id, err := Add(db, Entry{Kind: KindTitle, Hash: "/manga/1", Label: "Foo", MangaID: &mangaID})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := SetPage(db, id, 12); err != nil {
		t.Fatalf("SetPage: %v", err)
	}
	got, err := Get(db, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.LastPage != 12 {
		t.Errorf("LastPage = %d, want 12", got.LastPage)
	}
}

func TestGetUnknownReturnsNil(t *testing.T) {
	db, _ := newDB(t)
	got, err := Get(db, 999)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get(999) = %+v, want nil", got)
	}
}

func TestRemove(t *testing.T) {
	db, _ := newDB(t)
	id, err := Add(db, Entry{Kind: KindSearch, Hash: "/?q=foo", Label: "title: foo"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := Remove(db, id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	list, err := List(db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List len = %d after remove, want 0", len(list))
	}
}

func TestCascadeOnMangaDelete(t *testing.T) {
	db, mangaID := newDB(t)
	if _, err := Add(db, Entry{Kind: KindTitle, Hash: "/manga/1", Label: "Foo", MangaID: &mangaID}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := db.Exec("DELETE FROM manga WHERE id = ?", mangaID); err != nil {
		t.Fatalf("delete manga: %v", err)
	}
	list, err := List(db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("title stash entry survived manga delete: %+v", list)
	}
}
