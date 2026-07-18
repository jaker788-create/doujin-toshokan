package search

import (
	"database/sql"
	"path/filepath"
	"testing"

	"doujin/internal/ingest"
	"doujin/internal/store"
	"doujin/internal/tag"
)

// gen wraps tag names as untyped/General typed tags for ingest in tests.
func gen(names ...string) []tag.Typed {
	out := make([]tag.Typed, len(names))
	for i, n := range names {
		out[i] = tag.Typed{Name: n}
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

func seed(t *testing.T, db *sql.DB) (int64, int64) {
	t.Helper()
	a, err := ingest.IngestManga(db, ingest.MangaInput{
		Title: "Blue Sky", Author: "Aoi", FolderPath: "/p1", CoverRelPath: sp("1.png"),
		PageCount: 11, Tags: gen("action", "scifi"),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ingest.IngestManga(db, ingest.MangaInput{
		Title: "Forest", Author: "Mori", FolderPath: "/p2", CoverRelPath: sp("1.png"),
		PageCount: 3, Tags: gen("slice-of-life"),
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
	action, _ := ingest.GetOrCreateTag(db, "action", tag.General)
	scifi, _ := ingest.GetOrCreateTag(db, "scifi", tag.General)
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

func TestSortRandomSeeded(t *testing.T) {
	db := newDB(t)
	// Enough rows that distinct seeds reliably produce distinct orderings.
	names := []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot", "Golf", "Hotel"}
	want := map[string]bool{}
	for _, name := range names {
		if _, err := ingest.IngestManga(db, ingest.MangaInput{
			Title: name, Author: name + "-author", FolderPath: "/" + name, PageCount: 1,
		}); err != nil {
			t.Fatal(err)
		}
		want[name] = true
	}

	// A given seed is stable across calls.
	a1, err := SearchManga(db, SearchParams{Sort: "random", Seed: 42})
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := SearchManga(db, SearchParams{Sort: "random", Seed: 42})
	if !eq(titles(a1), titles(a2)) {
		t.Errorf("same seed not stable:\n %v\n %v", titles(a1), titles(a2))
	}

	// The shuffle is a permutation of the full set: no dupes, nothing dropped.
	if len(a1) != len(want) {
		t.Fatalf("random returned %d rows, want %d", len(a1), len(want))
	}
	seen := map[string]bool{}
	for _, m := range a1 {
		if seen[m.Title] {
			t.Errorf("duplicate %q in shuffle", m.Title)
		}
		seen[m.Title] = true
		if !want[m.Title] {
			t.Errorf("unexpected title %q", m.Title)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("shuffle covered %d titles, want %d", len(seen), len(want))
	}

	// Paging with a fixed seed is consistent: concatenated pages equal the full
	// order (no dupes/gaps across LIMIT/OFFSET, which a naive RANDOM() would break).
	var paged []string
	for off := 0; off < len(names); off += 3 {
		pg, _ := SearchManga(db, SearchParams{Sort: "random", Seed: 42, Limit: 3, Offset: off})
		paged = append(paged, titles(pg)...)
	}
	if !eq(paged, titles(a1)) {
		t.Errorf("paged shuffle != full shuffle:\n paged %v\n full  %v", paged, titles(a1))
	}

	// Different seeds reorder: at least one other seed differs from seed 42's order.
	base := titles(a1)
	differs := false
	for _, s := range []int64{1, 2, 3, 7, 99, 12345} {
		other, _ := SearchManga(db, SearchParams{Sort: "random", Seed: s})
		if !eq(titles(other), base) {
			differs = true
			break
		}
	}
	if !differs {
		t.Error("no seed produced a different order; shuffle isn't seed-sensitive")
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
	action, _ := ingest.GetOrCreateTag(db, "action", tag.General)
	scifi, _ := ingest.GetOrCreateTag(db, "scifi", tag.General)

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

// setSource stamps a title's provenance the way the auto-tagger's applyTags does.
func setSource(t *testing.T, db *sql.DB, id int64, slug, ref string) {
	t.Helper()
	if _, err := db.Exec("UPDATE manga SET source_slug=?, source_ref=? WHERE id=?", slug, ref, id); err != nil {
		t.Fatal(err)
	}
}

func TestSearchFiltersBySource(t *testing.T) {
	db := newDB(t)
	blue, _ := seed(t, db) // Blue Sky (Aoi), Forest (Mori)
	setSource(t, db, blue, "hitomi", "5000")
	// Forest stays NULL — never auto-tagged.

	got, err := SearchManga(db, SearchParams{Sort: "title", SourceSlug: "hitomi"})
	if err != nil {
		t.Fatalf("SearchManga: %v", err)
	}
	if !eq(titles(got), []string{"Blue Sky"}) {
		t.Errorf("source=hitomi = %v, want [Blue Sky]", titles(got))
	}

	got, _ = SearchManga(db, SearchParams{Sort: "title", SourceSlug: SourceNone})
	if !eq(titles(got), []string{"Forest"}) {
		t.Errorf("source=none = %v, want [Forest]", titles(got))
	}

	// An empty slug must not filter at all — it is the "any source" default, and a
	// library view with no source filter has to keep showing untagged titles.
	got, _ = SearchManga(db, SearchParams{Sort: "title"})
	if len(got) != 2 {
		t.Errorf("no source filter = %v, want both titles", titles(got))
	}

	got, _ = SearchManga(db, SearchParams{Sort: "title", SourceSlug: "mangadex"})
	if len(got) != 0 {
		t.Errorf("source=mangadex = %v, want []", titles(got))
	}
}

// A blank source_slug must count and filter as untagged, not as its own source.
// Migration 007 can leave an empty string rather than NULL on some rows, and splitting
// the two spellings would hide those titles from every filter value at once.
func TestSearchSourceBlankCountsAsUntagged(t *testing.T) {
	db := newDB(t)
	blue, forest := seed(t, db)
	setSource(t, db, blue, "", "")
	setSource(t, db, forest, "nhentai", "177013")

	got, err := SearchManga(db, SearchParams{Sort: "title", SourceSlug: SourceNone})
	if err != nil {
		t.Fatalf("SearchManga: %v", err)
	}
	if !eq(titles(got), []string{"Blue Sky"}) {
		t.Errorf("source=none = %v, want [Blue Sky] (blank slug is untagged)", titles(got))
	}

	counts, err := SourceCounts(db)
	if err != nil {
		t.Fatalf("SourceCounts: %v", err)
	}
	for _, c := range counts {
		if c.Slug == "" {
			t.Fatalf("SourceCounts returned a blank slug bucket: %+v", counts)
		}
	}
}

// The facet list is ordered: real sources by descending count, untagged always last.
func TestSourceCountsOrderAndBuckets(t *testing.T) {
	db := newDB(t)
	blue, _ := seed(t, db)
	setSource(t, db, blue, "nhentai", "1")
	// Forest stays untagged; add two more nhentai and one hitomi so counts differ.
	for i, slug := range []string{"nhentai", "nhentai", "hitomi"} {
		id, err := ingest.IngestManga(db, ingest.MangaInput{
			Title: "T" + string(rune('a'+i)), Author: "A", FolderPath: "/x" + string(rune('a'+i)),
			PageCount: 1, Tags: gen("t"),
		})
		if err != nil {
			t.Fatal(err)
		}
		setSource(t, db, id, slug, "r")
	}

	counts, err := SourceCounts(db)
	if err != nil {
		t.Fatalf("SourceCounts: %v", err)
	}
	want := []SourceCount{
		{Slug: "nhentai", Count: 3},
		{Slug: "hitomi", Count: 1},
		{Slug: SourceNone, Count: 1},
	}
	if len(counts) != len(want) {
		t.Fatalf("SourceCounts = %+v, want %+v", counts, want)
	}
	for i, w := range want {
		if counts[i] != w {
			t.Errorf("counts[%d] = %+v, want %+v (full: %+v)", i, counts[i], w, counts)
		}
	}
}
