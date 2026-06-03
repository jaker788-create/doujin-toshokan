package search

import (
	"database/sql"
	"path/filepath"
	"testing"

	"doujin/internal/ingest"
	"doujin/internal/store"
)

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

func seed(t *testing.T, db *sql.DB) (int64, int64) {
	t.Helper()
	a, err := ingest.IngestManga(db, ingest.MangaInput{
		Title: "Blue Sky", Author: "Aoi", FolderPath: "/p1", CoverRelPath: sp("1.png"),
		PageCount: 11, Tags: []string{"action", "scifi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ingest.IngestManga(db, ingest.MangaInput{
		Title: "Forest", Author: "Mori", FolderPath: "/p2", CoverRelPath: sp("1.png"),
		PageCount: 3, Tags: []string{"slice-of-life"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func authorID(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow("SELECT id FROM authors WHERE name=?", name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func titles(ms []Manga) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.Title)
	}
	return out
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

func TestSearchByTitle(t *testing.T) {
	db := newDB(t)
	seed(t, db)
	ms, err := SearchManga(db, SearchParams{Query: "blue"})
	if err != nil {
		t.Fatal(err)
	}
	if !eq(titles(ms), []string{"Blue Sky"}) {
		t.Errorf("titles = %v, want [Blue Sky]", titles(ms))
	}
}

func TestFilterByAuthor(t *testing.T) {
	db := newDB(t)
	seed(t, db)
	ms, err := SearchManga(db, SearchParams{AuthorID: authorID(t, db, "Mori")})
	if err != nil {
		t.Fatal(err)
	}
	if !eq(titles(ms), []string{"Forest"}) {
		t.Errorf("titles = %v, want [Forest]", titles(ms))
	}
}

func TestFilterByTagsRequiresAll(t *testing.T) {
	db := newDB(t)
	seed(t, db)
	action, _ := ingest.GetOrCreateTag(db, "action")
	scifi, _ := ingest.GetOrCreateTag(db, "scifi")
	ms, err := SearchManga(db, SearchParams{TagIDs: []int64{action, scifi}})
	if err != nil {
		t.Fatal(err)
	}
	if !eq(titles(ms), []string{"Blue Sky"}) {
		t.Errorf("titles = %v, want [Blue Sky]", titles(ms))
	}
}

func TestSortByDateDesc(t *testing.T) {
	db := newDB(t)
	seed(t, db)
	ms, err := SearchManga(db, SearchParams{Sort: "date"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 || ms[0].Title != "Forest" {
		t.Errorf("first by date = %v, want Forest (inserted second)", titles(ms))
	}
}

func TestSuggestTagsPrefix(t *testing.T) {
	db := newDB(t)
	seed(t, db)
	names, err := SuggestTags(db, "sc", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !eq(names, []string{"scifi"}) {
		t.Errorf("suggestions = %v, want [scifi]", names)
	}
}

func TestSuggestAuthorsSubstring(t *testing.T) {
	db := newDB(t)
	seed(t, db)
	got, err := SuggestAuthors(db, "or", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Mori" {
		t.Errorf("substring 'or' = %v, want [Mori]", got)
	}
	if got[0].ID <= 0 {
		t.Error("author id should be positive (needed for filtering)")
	}
	all, err := SuggestAuthors(db, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Name != "Aoi" || all[1].Name != "Mori" {
		t.Errorf("empty prefix = %v, want [Aoi Mori]", all)
	}
}

func TestGetAuthor(t *testing.T) {
	db := newDB(t)
	seed(t, db)
	mori := authorID(t, db, "Mori")
	a, err := GetAuthor(db, mori)
	if err != nil {
		t.Fatal(err)
	}
	if a == nil || a.Name != "Mori" {
		t.Errorf("GetAuthor(%d) = %v, want Mori", mori, a)
	}
	dangling, err := GetAuthor(db, 999999)
	if err != nil {
		t.Fatal(err)
	}
	if dangling != nil {
		t.Errorf("dangling id should return nil, got %v", dangling)
	}
}

func TestSearchCombinesAuthorQueryAndTags(t *testing.T) {
	db := newDB(t)
	seed(t, db)
	aoi := authorID(t, db, "Aoi")
	mori := authorID(t, db, "Mori")
	action, _ := ingest.GetOrCreateTag(db, "action")
	scifi, _ := ingest.GetOrCreateTag(db, "scifi")

	ms, err := SearchManga(db, SearchParams{Query: "blue", AuthorID: aoi, TagIDs: []int64{action, scifi}})
	if err != nil {
		t.Fatal(err)
	}
	if !eq(titles(ms), []string{"Blue Sky"}) {
		t.Errorf("combined filter = %v, want [Blue Sky]", titles(ms))
	}
	wrong, err := SearchManga(db, SearchParams{AuthorID: mori, TagIDs: []int64{action, scifi}})
	if err != nil {
		t.Fatal(err)
	}
	if len(wrong) != 0 {
		t.Errorf("wrong author with same tags = %v, want []", titles(wrong))
	}
}

func TestGetMangaAndTags(t *testing.T) {
	db := newDB(t)
	a, _ := seed(t, db)
	m, err := GetManga(db, a)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Title != "Blue Sky" || m.AuthorName != "Aoi" {
		t.Errorf("GetManga = %+v, want Blue Sky / Aoi", m)
	}
	tags, err := GetMangaTags(db, a)
	if err != nil {
		t.Fatal(err)
	}
	if !eq(tags, []string{"action", "scifi"}) { // ORDER BY name
		t.Errorf("tags = %v, want [action scifi]", tags)
	}
}

func TestListAuthorsAndTags(t *testing.T) {
	db := newDB(t)
	seed(t, db)
	authors, err := ListAuthors(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 || authors[0].Name != "Aoi" || authors[1].Name != "Mori" {
		t.Errorf("authors = %v, want [Aoi Mori]", authors)
	}
	tags, err := ListTags(db)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tg := range tags {
		got[tg.Name] = true
	}
	for _, want := range []string{"action", "scifi", "slice-of-life"} {
		if !got[want] {
			t.Errorf("missing tag %q in %v", want, tags)
		}
	}
}

func TestSearchLimitOffset(t *testing.T) {
	db := newDB(t)
	seed(t, db) // Blue Sky (Aoi), Forest (Mori)
	page1, _ := SearchManga(db, SearchParams{Sort: "title", Limit: 1, Offset: 0})
	page2, _ := SearchManga(db, SearchParams{Sort: "title", Limit: 1, Offset: 1})
	if !eq(titles(page1), []string{"Blue Sky"}) {
		t.Errorf("page1 = %v, want [Blue Sky]", titles(page1))
	}
	if !eq(titles(page2), []string{"Forest"}) {
		t.Errorf("page2 = %v, want [Forest]", titles(page2))
	}
	past, _ := SearchManga(db, SearchParams{Sort: "title", Limit: 1, Offset: 2})
	if len(past) != 0 {
		t.Errorf("past end = %v, want []", titles(past))
	}
	all, _ := SearchManga(db, SearchParams{Sort: "title"}) // Limit 0 -> unlimited
	if len(all) != 2 {
		t.Errorf("unlimited = %d rows, want 2", len(all))
	}
}
