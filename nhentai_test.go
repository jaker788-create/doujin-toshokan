package main

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"doujin/internal/autotag"
	"doujin/internal/ingest"
	"doujin/internal/nhentai"
	"doujin/internal/scanner"
	"doujin/internal/search"
	"doujin/internal/store"
	"doujin/internal/tag"
)

// gen wraps tag names as untyped/General typed tags. tagNames extracts the names from
// typed tags (for set comparisons). tagTypes maps name->subject.
func gen(names ...string) []tag.Typed {
	out := make([]tag.Typed, len(names))
	for i, n := range names {
		out[i] = tag.Typed{Name: n}
	}
	return out
}

func tagNames(ts []tag.Typed) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func tagTypes(ts []tag.Typed) map[string]string {
	m := map[string]string{}
	for _, t := range ts {
		m[t.Name] = t.Type
	}
	return m
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "doujin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Init(db); err != nil {
		t.Fatal(err)
	}
	return &App{dataDir: dir, db: db}
}

func TestGalleryTypedTagsMapSubjectsNormalizeAndSort(t *testing.T) {
	d := &nhentai.GalleryDetail{Tags: []nhentai.Tag{
		{Type: "category", Name: "Doujinshi"},
		{Type: "parody", Name: "Naruto"},
		{Type: "tag", Name: "  Compilation "},
		{Type: "artist", Name: "Some Artist"},
		{Type: "tag", Name: "compilation"}, // duplicate after normalize
		{Type: "language", Name: "english"},
	}}
	got := galleryTypedTags(d)
	// Ordered by subject rank then name: language, artist, parody, category, tag.
	want := []tag.Typed{
		{Name: "english", Type: tag.Language},
		{Name: "some artist", Type: tag.Artist},
		{Name: "naruto", Type: tag.Parody},
		{Name: "doujinshi", Type: tag.Category},
		{Name: "compilation", Type: tag.Tag},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("galleryTypedTags = %v, want %v", got, want)
	}
}

func TestApplyTagsUnionsExistingAndStampsGallery(t *testing.T) {
	a := newTestApp(t)

	id, err := ingest.IngestManga(a.db, ingest.MangaInput{
		Title:      "Test Title",
		Author:     "Test Author",
		FolderPath: "/tmp/test-title",
		PageCount:  20,
		Tags:       gen("manual-keep", "shared"),
	})
	if err != nil {
		t.Fatal(err)
	}

	detail := &nhentai.GalleryDetail{ID: 999, Tags: []nhentai.Tag{
		{Type: "tag", Name: "shared"},    // overlaps an existing tag
		{Type: "tag", Name: "new-one"},   // new
		{Type: "parody", Name: "Naruto"}, // new, normalized to lowercase
	}}

	saved, err := a.applyTags(id, 999, []*nhentai.GalleryDetail{detail})
	if err != nil {
		t.Fatal(err)
	}
	// Union, ordered by subject then name: naruto (parody), new-one/shared (tag),
	// manual-keep (general, last). The pre-existing "shared" is upgraded to the tag
	// subject; "manual-keep" stays general.
	if got := tagNames(saved); !reflect.DeepEqual(got, []string{"naruto", "new-one", "shared", "manual-keep"}) {
		t.Errorf("saved order = %v", got)
	}
	types := tagTypes(saved)
	if types["naruto"] != tag.Parody || types["shared"] != tag.Tag || types["manual-keep"] != tag.General {
		t.Errorf("subjects wrong: %v", types)
	}

	// The gallery link must be stamped on the manga row.
	m, err := search.GetManga(a.db, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.NhentaiGalleryID == nil || *m.NhentaiGalleryID != 999 {
		t.Errorf("nhentai_gallery_id = %v, want 999", m.NhentaiGalleryID)
	}
}

func TestApplyTagsPreservesExistingLanguage(t *testing.T) {
	a := newTestApp(t)
	// The title already has a language tag; a merge from a different-language gallery
	// must keep it and import no gallery language.
	id, err := ingest.IngestManga(a.db, ingest.MangaInput{
		Title: "T", Author: "A", FolderPath: "/tmp/lang-keep", PageCount: 10,
		Tags: []tag.Typed{{Name: "english", Type: tag.Language}, {Name: "manual", Type: tag.General}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &nhentai.GalleryDetail{ID: 1, Tags: []nhentai.Tag{
		{Type: "language", Name: "japanese"},
		{Type: "tag", Name: "newtag"},
	}}
	saved, err := a.applyTags(id, 1, []*nhentai.GalleryDetail{d})
	if err != nil {
		t.Fatal(err)
	}
	types := tagTypes(saved)
	if _, hasJP := types["japanese"]; hasJP {
		t.Errorf("gallery language leaked in: %v", tagNames(saved))
	}
	if types["english"] != tag.Language {
		t.Errorf("local language not preserved: %v", types)
	}
	if _, ok := types["newtag"]; !ok {
		t.Errorf("non-language gallery tag should still merge: %v", tagNames(saved))
	}
}

func TestApplyTagsAdoptsOnlyPrimaryLanguageWhenEmpty(t *testing.T) {
	a := newTestApp(t)
	// The title has no language; merging an English (primary) + Chinese variant adopts
	// only the primary's single language, and unions the rest of the tags.
	id, err := ingest.IngestManga(a.db, ingest.MangaInput{
		Title: "T", Author: "A", FolderPath: "/tmp/lang-fill", PageCount: 10,
		Tags: gen("manual"),
	})
	if err != nil {
		t.Fatal(err)
	}
	primary := &nhentai.GalleryDetail{ID: 1, Tags: []nhentai.Tag{
		{Type: "language", Name: "english"}, {Type: "tag", Name: "aaa"},
	}}
	secondary := &nhentai.GalleryDetail{ID: 2, Tags: []nhentai.Tag{
		{Type: "language", Name: "chinese"}, {Type: "tag", Name: "bbb"},
	}}
	saved, err := a.applyTags(id, 1, []*nhentai.GalleryDetail{primary, secondary})
	if err != nil {
		t.Fatal(err)
	}
	types := tagTypes(saved)
	if types["english"] != tag.Language {
		t.Errorf("primary language not adopted: %v", types)
	}
	if _, hasCN := types["chinese"]; hasCN {
		t.Errorf("secondary language should be dropped: %v", tagNames(saved))
	}
	for _, n := range []string{"aaa", "bbb", "manual"} {
		if _, ok := types[n]; !ok {
			t.Errorf("missing merged tag %q in %v", n, tagNames(saved))
		}
	}
}

func TestToCandidatePopulatesCoverLangAndURL(t *testing.T) {
	c := autotag.Candidate{
		Gallery: nhentai.SearchResult{
			ID: 42, MediaID: "777", Thumbnail: "https://t.nhentai.net/galleries/777/thumb.webp",
			EnglishTitle: "X", NumPages: 20, NumFavorites: 3,
		},
		TitleScore: 0.9, PagesExact: true, PageDelta: 0, Lang: "english", LangMatch: true,
	}
	nc := toCandidate(c)
	if nc.MediaID != "777" || nc.Thumbnail == "" {
		t.Errorf("cover fields not carried: %+v", nc)
	}
	if nc.GalleryURL != "https://nhentai.net/g/42/" {
		t.Errorf("gallery url = %q", nc.GalleryURL)
	}
	if nc.Language != "english" || !nc.LangMatch || nc.LangMismatch {
		t.Errorf("language flags wrong: %+v", nc)
	}
}

func TestShortlistFlagsArtistAndParodyOverlap(t *testing.T) {
	mi := matchInputs("/lib/[Group (Sanada)] T (Kemono Jihen) [English]", "T", "")
	ranked := []autotag.Candidate{
		{Gallery: nhentai.SearchResult{ID: 1, EnglishTitle: "[Group (Sanada)] Whatever (Kemono Jihen) [English]"}},
		{Gallery: nhentai.SearchResult{ID: 2, EnglishTitle: "[Other (Someone)] Whatever (Naruto)"}},
	}
	out := shortlist(ranked, 8, mi)
	if !out[0].ArtistMatch || !out[0].ParodyMatch {
		t.Errorf("candidate 0 should flag artist+parody overlap: %+v", out[0])
	}
	if out[1].ArtistMatch || out[1].ParodyMatch {
		t.Errorf("candidate 1 should not overlap: %+v", out[1])
	}
}

// Free-text title runs first; the reliable fallback is the artist tag narrowed by the
// first distinctive title word, then the bare artist catalog.
func TestSearchRequestsTitleFirstThenArtistNarrowed(t *testing.T) {
	reqs := searchRequests("some artist", []string{"A Little Sister's Warmth"}, []string{"Some Circle"}, "")
	if len(reqs) < 3 {
		t.Fatalf("want at least 3 requests, got %+v", reqs)
	}
	if reqs[0].query != "A Little Sister's Warmth" || reqs[0].page != 1 {
		t.Errorf("first = %+v, want the free-text title", reqs[0])
	}
	if reqs[1].query != `artist:"some artist" title:"little"` {
		t.Errorf("second = %+v, want the artist tag narrowed by the first distinctive title word", reqs[1])
	}
	if reqs[2].query != `artist:"some artist"` {
		t.Errorf("third = %+v, want the bare artist catalog", reqs[2])
	}
}

// A concrete language narrows the title free-text query, but NOT the artist-tag queries —
// the tag is constraint enough, and filtering it would hide an artist whose works are only
// in another language.
func TestSearchRequestsAppendLanguageFilter(t *testing.T) {
	reqs := searchRequests("some artist", []string{"Some Title"}, nil, "english")
	if len(reqs) < 3 {
		t.Fatalf("want title + 2 artist requests, got %+v", reqs)
	}
	if reqs[0].query != "Some Title language:english" {
		t.Errorf("free-text = %q, want the language filter appended", reqs[0].query)
	}
	if reqs[1].query != `artist:"some artist" title:"some"` {
		t.Errorf("artist-narrowed = %q, want NO language filter (tag is constraint enough)", reqs[1].query)
	}
	if reqs[2].query != `artist:"some artist"` {
		t.Errorf("bare artist = %q, want NO language filter", reqs[2].query)
	}
}

// With no artist there's no tag to lean on — just the free-text title(s).
func TestSearchRequestsWithoutArtistIsFreeTextOnly(t *testing.T) {
	reqs := searchRequests("", []string{"Some Title"}, nil, "")
	if len(reqs) != 1 || reqs[0].query != "Some Title" {
		t.Fatalf("want a single free-text title request, got %+v", reqs)
	}
}

// With several title variants the artist-tag queries must still land within the first few
// requests (before the extra variants) so the per-title budget reaches the bare catalog.
func TestSearchRequestsArtistQueriesPrecedeExtraVariants(t *testing.T) {
	reqs := searchRequests("some artist", []string{"Romaji Title", "English Subtitle"}, []string{"Some Circle"}, "")
	if len(reqs) < 4 {
		t.Fatalf("want at least 4 requests, got %+v", reqs)
	}
	if reqs[0].query != "Romaji Title" {
		t.Errorf("reqs[0] = %q, want the primary variant", reqs[0].query)
	}
	if reqs[1].query != `artist:"some artist" title:"romaji"` {
		t.Errorf("reqs[1] = %q, want the artist-narrowed query", reqs[1].query)
	}
	if reqs[2].query != `artist:"some artist"` {
		t.Errorf("reqs[2] = %q, want the bare artist catalog within budget", reqs[2].query)
	}
	if reqs[3].query != "English Subtitle" {
		t.Errorf("reqs[3] = %q, want the secondary variant after the artist queries", reqs[3].query)
	}
}

func TestFirstTitleWordSkipsShortWords(t *testing.T) {
	if got := firstTitleWord("A Little Sister's Warmth"); got != "little" {
		t.Errorf("firstTitleWord = %q, want %q", got, "little")
	}
	if got := firstTitleWord("X!"); got != "" {
		t.Errorf("firstTitleWord(no long word) = %q, want empty", got)
	}
}

// The early stop must not fire on a same-titled, same-page work by a different artist
// when we know the local artist; the right-artist candidate must satisfy it.
func TestConfidentMatchRequiresArtistWhenKnown(t *testing.T) {
	score := func(r nhentai.SearchResult) []autotag.Candidate {
		am := func(x nhentai.SearchResult) bool { return candidateArtistMatches(x, "some artist") }
		return autotag.ScoreAll([]string{"A Little Sister's Warmth"}, 19, "", []nhentai.SearchResult{r}, nil, am)
	}
	right := nhentai.SearchResult{ID: 1, EnglishTitle: "[Some Artist] A Little Sister's Warmth", NumPages: 19}
	wrong := nhentai.SearchResult{ID: 2, EnglishTitle: "[Other Artist] A Little Sister's Warmth", NumPages: 19}
	if confidentMatch(score(wrong), "some artist") {
		t.Error("wrong-artist candidate should not be a confident match")
	}
	if !confidentMatch(score(right), "some artist") {
		t.Error("right-artist candidate should be a confident match")
	}
	if !confidentMatch(score(wrong), "") {
		t.Error("with no known local artist, a full title + close pages should suffice")
	}
}

func TestMatchInputsCleansWrappedArtist(t *testing.T) {
	// An author folder stored with wrapping parens cleans to the bare artist tag for
	// searching/matching, and the anchor uses the clean form too.
	mi := matchInputs("/lib/(Rustle)/Some Title", "Some Title", "(Rustle)")
	if mi.artist != "rustle" {
		t.Errorf("artist = %q, want rustle", mi.artist)
	}
	clean := false
	for _, a := range mi.anchors {
		if a == "Rustle" {
			clean = true
		}
	}
	if !clean {
		t.Errorf("anchors = %v, want a clean 'Rustle'", mi.anchors)
	}
	// A hybrid name with no wrapping parens is preserved verbatim.
	if mi2 := matchInputs("/lib/x/y", "y", "A6-Kisho Muri"); mi2.artist != "a6-kisho muri" {
		t.Errorf("hybrid artist = %q, want a6-kisho muri", mi2.artist)
	}
}

func TestReviewPoolPrefersArtistMatches(t *testing.T) {
	ranked := []autotag.Candidate{
		{Gallery: nhentai.SearchResult{ID: 1}, ArtistMatch: false},
		{Gallery: nhentai.SearchResult{ID: 2}, ArtistMatch: true},
		{Gallery: nhentai.SearchResult{ID: 3}, ArtistMatch: true},
	}
	if got := applyGalleryIDs(reviewPool(ranked)); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("pool = %v, want the artist-matched [2 3] in order", got)
	}
	// No artist matches -> fall back to the full ranked list.
	none := []autotag.Candidate{{Gallery: nhentai.SearchResult{ID: 9}}}
	if got := applyGalleryIDs(reviewPool(none)); len(got) != 1 || got[0] != 9 {
		t.Errorf("fallback pool = %v, want the full list [9]", got)
	}
}

func TestGatherCandidatesCleanArtistHitsCatalog(t *testing.T) {
	// A cleaned artist makes the catalog query artist:"rustle", never artist:"(rustle)".
	cs := &countingSearcher{numPages: 1, perPage: 2}
	run := newAutoTagRun(cs, "auto", map[string]int{"rustle": 2})
	app := &App{}
	mi := matchInputs("/lib/(Rustle)/Some Title", "Some Title", "(Rustle)")
	if _, _, err := app.gatherCandidates(context.Background(), run, mi, 10, ""); err != nil {
		t.Fatal(err)
	}
	if countQueries(cs.searchCalls, `artist:"rustle"`) == 0 {
		t.Errorf(`expected an artist:"rustle" catalog query, got %v`, cs.searchCalls)
	}
	for _, c := range cs.searchCalls {
		if strings.Contains(c, "(rustle)") {
			t.Errorf("catalog query used the un-cleaned artist: %q", c)
		}
	}
}

// Forced-english prefers the language-narrowed catalog (keeping a prolific artist under the
// page cap); when it has results the all-language catalog is never fetched.
func TestGatherArtistCatalogPrefersLanguageNarrowed(t *testing.T) {
	cs := &stubSearcher{hits: map[string][]nhentai.SearchResult{
		`artist:"x" language:english`: {{ID: 6, EnglishTitle: "[Circle] EN Work"}},
		`artist:"x"`:                  {{ID: 7}}, // the all-language fallback — must stay unused
	}}
	run := newAutoTagRun(cs, "english", map[string]int{"x": 2})
	app := &App{}
	mi := matchInput{variants: []string{"Title"}, artist: "x"}
	cands, _, err := app.gatherCandidates(context.Background(), run, mi, 10, "english")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs.calls {
		if c == `artist:"x"` {
			t.Errorf("all-language catalog fetched despite the narrowed one having results")
		}
	}
	if len(cands) != 1 || cands[0].Gallery.ID != 6 {
		t.Errorf("want only the language-narrowed gallery 6, got %+v", cands)
	}
}

// When the language-narrowed catalog is empty (a Japanese-only artist under forced-english),
// it falls back to the all-language catalog and finds the works (flagged artist-matched).
func TestGatherArtistCatalogFallsBackToAllLanguages(t *testing.T) {
	cs := &stubSearcher{hits: map[string][]nhentai.SearchResult{
		`artist:"x"`: {{ID: 5, EnglishTitle: "[Circle] JP-only Work"}},
		// no `artist:"x" language:english` entry -> the narrowed query returns nothing
	}}
	run := newAutoTagRun(cs, "english", map[string]int{"x": 2})
	app := &App{}
	mi := matchInput{variants: []string{"Title"}, artist: "x"}
	cands, _, err := app.gatherCandidates(context.Background(), run, mi, 10, "english")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cands {
		if c.Gallery.ID == 5 && c.ArtistMatch {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the all-language fallback to find gallery 5; got %+v", cands)
	}
}

// ── Run cache, pagination, and language narrowing ──────────────────────────

// countingSearcher is an nhSearcher fake: it records every Search query+page and every
// GalleryByID id, and serves canned responses. numPages is the num_pages reported on every
// search; perPage ids are generated per page (id = page*1000 + index) so the page-through
// and dedup paths are exercised.
type countingSearcher struct {
	searchCalls []string
	detailCalls []int64
	numPages    int
	perPage     int
}

func (c *countingSearcher) Search(_ context.Context, query string, page int) (*nhentai.SearchResponse, error) {
	c.searchCalls = append(c.searchCalls, fmt.Sprintf("%s#%d", query, page))
	np := c.numPages
	if np < 1 {
		np = 1
	}
	var res []nhentai.SearchResult
	if page <= np {
		for i := 0; i < c.perPage; i++ {
			res = append(res, nhentai.SearchResult{ID: int64(page*1000 + i)})
		}
	}
	return &nhentai.SearchResponse{Result: res, NumPages: np, PerPage: c.perPage, Total: np * c.perPage}, nil
}

func (c *countingSearcher) GalleryByID(_ context.Context, id int64) (*nhentai.GalleryDetail, error) {
	c.detailCalls = append(c.detailCalls, id)
	return &nhentai.GalleryDetail{ID: id}, nil
}

func countQueries(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// catalog pages through every result page, caps at maxCatalogPages with truncated set, and
// a second call for the same query is served from cache (no new network).
func TestCatalogPagesThroughAndCaps(t *testing.T) {
	cs := &countingSearcher{numPages: 12, perPage: 2}
	run := newAutoTagRun(cs, "auto", nil)
	results, truncated, err := run.catalog(context.Background(), `artist:"x"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.searchCalls) != maxCatalogPages {
		t.Fatalf("fetched %d pages, want cap %d", len(cs.searchCalls), maxCatalogPages)
	}
	if !truncated {
		t.Error("12 pages under a 10 cap should report truncated")
	}
	if len(results) != maxCatalogPages*cs.perPage {
		t.Errorf("got %d results, want %d", len(results), maxCatalogPages*cs.perPage)
	}
	before := len(cs.searchCalls)
	if _, _, err := run.catalog(context.Background(), `artist:"x"`); err != nil {
		t.Fatal(err)
	}
	if len(cs.searchCalls) != before {
		t.Errorf("second catalog issued %d new calls, want 0 (cached)", len(cs.searchCalls)-before)
	}
}

// catalog stops at num_pages when the artist has fewer pages than the cap, and does not flag
// truncation.
func TestCatalogStopsAtNumPages(t *testing.T) {
	cs := &countingSearcher{numPages: 2, perPage: 3}
	run := newAutoTagRun(cs, "auto", nil)
	results, truncated, err := run.catalog(context.Background(), `artist:"x"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.searchCalls) != 2 {
		t.Fatalf("fetched %d pages, want 2 (num_pages)", len(cs.searchCalls))
	}
	if truncated {
		t.Error("2 pages under the cap should not be truncated")
	}
	if len(results) != 6 {
		t.Errorf("got %d results, want 6", len(results))
	}
}

// searchPage and detail both cache by key, so a repeat issues no new network call.
func TestSearchPageAndDetailCached(t *testing.T) {
	cs := &countingSearcher{numPages: 1, perPage: 2}
	run := newAutoTagRun(cs, "auto", nil)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := run.searchPage(ctx, "q", 1); err != nil {
			t.Fatal(err)
		}
	}
	if len(cs.searchCalls) != 1 {
		t.Errorf("searchPage made %d calls for the same query+page, want 1 (cached)", len(cs.searchCalls))
	}
	for i := 0; i < 2; i++ {
		if _, err := run.detail(ctx, 42); err != nil {
			t.Fatal(err)
		}
	}
	if len(cs.detailCalls) != 1 {
		t.Errorf("detail made %d calls for the same id, want 1 (cached)", len(cs.detailCalls))
	}
}

// A prolific artist (>=2 local titles) pages the catalog ONCE; a second sibling reuses the
// cache and issues no further artist-catalog pages.
func TestGatherCatalogFirstSharedAcrossSiblings(t *testing.T) {
	cs := &countingSearcher{numPages: 2, perPage: 2}
	run := newAutoTagRun(cs, "auto", map[string]int{"x": 2})
	app := &App{}
	mi1 := matchInput{variants: []string{"Title One"}, artist: "x"}
	mi2 := matchInput{variants: []string{"Title Two"}, artist: "x"}
	if _, _, err := app.gatherCandidates(context.Background(), run, mi1, 10, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.gatherCandidates(context.Background(), run, mi2, 10, ""); err != nil {
		t.Fatal(err)
	}
	if got := countQueries(cs.searchCalls, `artist:"x"#`); got != 2 {
		t.Errorf("artist catalog fetched %d pages across 2 siblings, want 2 (one page-through, cached)", got)
	}
}

// A single-title artist stays on the cheap title-first path: no catalog page-through (no
// page-2+ fetch), and the title is searched once by free-text.
func TestGatherSingletonStaysTitleFirst(t *testing.T) {
	cs := &countingSearcher{numPages: 5, perPage: 2}
	run := newAutoTagRun(cs, "auto", map[string]int{"x": 1})
	app := &App{}
	mi := matchInput{variants: []string{"Solo Title"}, artist: "x"}
	if _, _, err := app.gatherCandidates(context.Background(), run, mi, 10, ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range cs.searchCalls {
		if strings.HasSuffix(c, "#2") {
			t.Errorf("singleton artist should not page the catalog; saw %q", c)
		}
	}
	if got := countQueries(cs.searchCalls, "Solo Title#"); got != 1 {
		t.Errorf("want the title free-text searched once, calls=%v", cs.searchCalls)
	}
}

// A single-title artist's works come from the artist:"…" queries (not a catalog
// page-through); they must still be flagged artist-matched even though the fake's
// title-less results don't name the artist in a bracket.
func TestGatherSingletonFlagsArtistQueryResults(t *testing.T) {
	cs := &countingSearcher{numPages: 1, perPage: 2}
	run := newAutoTagRun(cs, "auto", map[string]int{"x": 1})
	app := &App{}
	mi := matchInput{variants: []string{"Solo Title"}, artist: "x"}
	cands, _, err := app.gatherCandidates(context.Background(), run, mi, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, c := range cands {
		if c.ArtistMatch {
			matched++
		}
	}
	if matched == 0 {
		t.Errorf(`expected the artist:"x" query results to be flagged ArtistMatch, got none`)
	}
}

// stubSearcher serves canned results keyed by the exact query string (page 1 only) and
// records every query, so a test can model "this exact tag/language returns nothing, that
// one returns the works" and assert which queries ran.
type stubSearcher struct {
	hits  map[string][]nhentai.SearchResult
	calls []string
}

func (s *stubSearcher) Search(_ context.Context, query string, _ int) (*nhentai.SearchResponse, error) {
	s.calls = append(s.calls, query)
	res := s.hits[query]
	return &nhentai.SearchResponse{Result: res, NumPages: 1, PerPage: len(res)}, nil
}

func (s *stubSearcher) GalleryByID(_ context.Context, id int64) (*nhentai.GalleryDetail, error) {
	return &nhentai.GalleryDetail{ID: id}, nil
}

func TestArtistTagVariants(t *testing.T) {
	// "%" becomes the word "percent"; the punctuation-collapsed form is also offered.
	if got := artistTagVariants("50% off"); len(got) != 2 || got[0] != "50 percent off" || got[1] != "50 off" {
		t.Errorf("artistTagVariants(50%% off) = %v, want [50 percent off, 50 off]", got)
	}
	// A clean name equal to its normalized form yields no alternates (exact already tried).
	if got := artistTagVariants("ayana rio"); len(got) != 0 {
		t.Errorf("artistTagVariants(ayana rio) = %v, want none", got)
	}
}

// A punctuated artist whose exact tag ("50% off") has no nhentai match falls back to the
// word form ("50 percent off"), and those results are flagged artist-matched.
func TestGatherCatalogNormalizedArtistFallback(t *testing.T) {
	cs := &stubSearcher{hits: map[string][]nhentai.SearchResult{
		`artist:"50 percent off"`: {{ID: 7, EnglishTitle: "[50% OFF] Best Friend"}},
	}}
	run := newAutoTagRun(cs, "auto", map[string]int{"50% off": 2})
	app := &App{}
	mi := matchInput{variants: []string{"Best Friend"}, artist: "50% off"}
	cands, _, err := app.gatherCandidates(context.Background(), run, mi, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cands {
		if c.Gallery.ID == 7 && c.ArtistMatch {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the 50 percent off catalog to return gallery 7 flagged artist-matched; got %+v", cands)
	}
}

// A forced language narrows the artist catalog query; Auto follows the local language and
// assumes all languages (no filter) when there is none. "translated" is not concrete.
func TestCatalogLanguageResolution(t *testing.T) {
	cases := []struct{ mode, local, want string }{
		{"auto", "english", "english"},
		{"auto", "", ""},
		{"auto", "translated", ""},
		{"english", "japanese", "english"},
		{"japanese", "english", "japanese"},
		{"english", "", "english"},
	}
	for _, c := range cases {
		if got := catalogLanguage(c.mode, c.local); got != c.want {
			t.Errorf("catalogLanguage(%q,%q) = %q, want %q", c.mode, c.local, got, c.want)
		}
	}
}

func TestWithLangAndArtistCatalogQuery(t *testing.T) {
	if got := withLang("foo", ""); got != "foo" {
		t.Errorf(`withLang("foo","") = %q, want "foo"`, got)
	}
	if got := withLang("foo", "english"); got != "foo language:english" {
		t.Errorf("withLang concrete = %q", got)
	}
	if got := artistCatalogQuery("kinomoto anzu", "english"); got != `artist:"kinomoto anzu" language:english` {
		t.Errorf("artistCatalogQuery = %q", got)
	}
	if got := artistCatalogQuery("kinomoto anzu", ""); got != `artist:"kinomoto anzu"` {
		t.Errorf("artistCatalogQuery no-lang = %q", got)
	}
}

func TestNormLangMode(t *testing.T) {
	for in, want := range map[string]string{
		"": "auto", "AUTO": "auto", "english": "english",
		"English": "english", "japanese": "japanese", "klingon": "auto",
	} {
		if got := normLangMode(in); got != want {
			t.Errorf("normLangMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMangaInputFromFolderCleansAndTags(t *testing.T) {
	// As the scanner produces it: Title == basename(FolderPath), the raw decorated name.
	name := "[Eight PM] Do Namaiki _ Teaching One Hell of a Lesson [English] {Chin²} [Digital]"
	d := scanner.DetectedFolder{
		Title:      name,
		Author:     "Eight PM",
		FolderPath: "/x/" + name,
		PageCount:  10,
	}
	in := mangaInputFromFolder(d, []string{"manual"})
	if in.Title != "Do Namaiki / Teaching One Hell of a Lesson" {
		t.Errorf("cleaned title = %q", in.Title)
	}
	// User tag preserved; language + digital pulled from the name; translator dropped.
	want := map[string]bool{"manual": true, "english": true, "digital": true}
	types := tagTypes(in.Tags)
	for _, tg := range in.Tags {
		delete(want, tg.Name)
		if tg.Name == "chin²" || tg.Name == "Chin²" {
			t.Errorf("translator credit leaked into tags: %v", in.Tags)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing tags %v in %v", want, in.Tags)
	}
	// The implied tags carry their subjects: english is a Language, digital a content Tag.
	if types["english"] != tag.Language || types["digital"] != tag.Tag {
		t.Errorf("subjects wrong: %v", types)
	}
}

func raw(name, author string) scanner.DetectedFolder {
	// Mirror the scanner: Title is the basename of FolderPath.
	return scanner.DetectedFolder{Title: name, Author: author, FolderPath: "/lib/" + name, PageCount: 1}
}

func TestMangaInputFromFolderDerivesAuthorForRawTitle(t *testing.T) {
	// A raw title in the root: the scanner leaves Author empty, so the importer
	// derives the artist from the [Circle (Artist)] in the decorated folder name.
	in := mangaInputFromFolder(raw("[bt-T Shounen (Sanada)] Ore-tachi no Hajimete Jihen (Kemono Jihen) [English] {Chin²}", ""), nil)
	if in.Author != "Sanada" {
		t.Errorf("derived author = %q, want Sanada (the artist)", in.Author)
	}
	if in.Title != "Ore-tachi no Hajimete Jihen" {
		t.Errorf("cleaned title = %q", in.Title)
	}
	gotTags := map[string]bool{}
	for _, tg := range in.Tags {
		gotTags[tg.Name] = true
	}
	if !gotTags["english"] || !gotTags["kemono jihen"] {
		t.Errorf("tags = %v, want english + kemono jihen", in.Tags)
	}

	// A solo circle (no inner artist) becomes the author verbatim.
	if got := mangaInputFromFolder(raw("[Eight PM] Some Raw Title [English]", ""), nil); got.Author != "Eight PM" {
		t.Errorf("author = %q, want Eight PM", got.Author)
	}

	// No circle and no author folder -> "Unknown" rather than an empty author.
	if got := mangaInputFromFolder(raw("just a plain title", ""), nil); got.Author != "Unknown" {
		t.Errorf("author = %q, want Unknown", got.Author)
	}

	// An organized title keeps its author-folder name even if the title has brackets.
	if got := mangaInputFromFolder(raw("[Eight PM] Title", "Real Author"), nil); got.Author != "Real Author" {
		t.Errorf("author = %q, want Real Author (folder wins)", got.Author)
	}
}

func TestMangaInputFromFolderHonorsTitleEdit(t *testing.T) {
	// When the Scan row changes the title away from the raw folder name, that edit
	// wins — but tags/author still come from the immutable folder name.
	d := raw("[Eight PM] Raw Name (Naruto) [English]", "")
	d.Title = "My Custom Title"
	in := mangaInputFromFolder(d, nil)
	if in.Title != "My Custom Title" {
		t.Errorf("title = %q, want the edit to win", in.Title)
	}
	if in.Author != "Eight PM" {
		t.Errorf("author = %q, want Eight PM (from folder name, not the edited title)", in.Author)
	}
	gotTags := map[string]bool{}
	for _, tg := range in.Tags {
		gotTags[tg.Name] = true
	}
	if !gotTags["english"] || !gotTags["naruto"] {
		t.Errorf("tags = %v, want english + naruto from the folder name", in.Tags)
	}
}

func TestIngestStoresCleanTitleAndParsedTags(t *testing.T) {
	a := newTestApp(t)
	name := "[Some Circle] Clean Me Please (Naruto) [English] [Digital]"
	d := scanner.DetectedFolder{
		Title:      name,
		Author:     "Some Circle",
		FolderPath: "/tmp/" + name,
		PageCount:  7,
	}
	if err := a.Ingest(d, nil); err != nil {
		t.Fatal(err)
	}
	ms, err := search.SearchManga(a.db, search.SearchParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("got %d manga, want 1", len(ms))
	}
	if ms[0].Title != "Clean Me Please" {
		t.Errorf("stored title = %q, want %q", ms[0].Title, "Clean Me Please")
	}
	tags, err := search.GetMangaTags(a.db, ms[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"english": true, "digital": true, "naruto": true}
	for _, tg := range tags {
		delete(want, tg)
	}
	if len(want) != 0 {
		t.Errorf("stored tags %v missing %v", tags, want)
	}
}

func TestRemoveMissingDeletesFlaggedAndPrunesAuthors(t *testing.T) {
	a := newTestApp(t)
	keep, err := ingest.IngestManga(a.db, ingest.MangaInput{
		Title: "Keep", Author: "Alice", FolderPath: "/lib/keep", PageCount: 5, Tags: gen("t1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := ingest.IngestManga(a.db, ingest.MangaInput{
		Title: "Gone", Author: "Bob", FolderPath: "/lib/gone", PageCount: 5, Tags: gen("t2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Flag "Gone" missing exactly as Rescan would when its folder disappears.
	if _, err := a.db.Exec("UPDATE manga SET missing=1 WHERE id=?", gone); err != nil {
		t.Fatal(err)
	}

	n, err := a.RemoveMissing()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if m, _ := search.GetManga(a.db, gone); m != nil {
		t.Errorf("missing title still present after removal")
	}
	if m, _ := search.GetManga(a.db, keep); m == nil {
		t.Errorf("present title was wrongly deleted")
	}
	// Bob is now orphaned (his only title is gone) and pruned; Alice remains.
	authors, err := search.ListAuthors(a.db)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, au := range authors {
		names[au.Name] = true
	}
	if names["Bob"] {
		t.Errorf("orphan author Bob should be pruned: %v", authors)
	}
	if !names["Alice"] {
		t.Errorf("Alice should remain: %v", authors)
	}
}

func TestDeleteMangaCascadesTagsAndPrunesAuthor(t *testing.T) {
	a := newTestApp(t)
	id, err := ingest.IngestManga(a.db, ingest.MangaInput{
		Title: "X", Author: "Solo", FolderPath: "/lib/x", PageCount: 3, Tags: gen("only"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteManga(id); err != nil {
		t.Fatal(err)
	}
	if m, _ := search.GetManga(a.db, id); m != nil {
		t.Errorf("manga not deleted")
	}
	var links int
	if err := a.db.QueryRow("SELECT COUNT(*) FROM manga_tags WHERE manga_id=?", id).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Errorf("manga_tags should cascade on delete, found %d", links)
	}
	if authors, _ := search.ListAuthors(a.db); len(authors) != 0 {
		t.Errorf("orphan author should be pruned, got %v", authors)
	}
}

func TestGetSettingsMasksKey(t *testing.T) {
	a := newTestApp(t)

	s, err := a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.HasNhentaiKey {
		t.Error("HasNhentaiKey = true before any key set")
	}

	if err := a.SetNhentaiKey("  secret-key  "); err != nil {
		t.Fatal(err)
	}
	s, err = a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasNhentaiKey {
		t.Error("HasNhentaiKey = false after setting a key")
	}
	// Settings must never carry the key value itself.
	if got := reflect.ValueOf(s); got.FieldByName("NhentaiAPIKey").IsValid() {
		t.Error("Settings exposes a key field; it must not")
	}
}
