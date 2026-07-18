package mangadex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"doujin/internal/source"
	"doujin/internal/tag"
)

const authorBody = `{"data":[{"id":"aaaa-1111","type":"author","attributes":{"name":"Kinomoto Anzu"}}]}`

// authorRouter serves /author and /manga separately, counting hits on each, so a test can
// assert both what the /manga request carried and how many author lookups it took.
func authorRouter(t *testing.T, authorJSON string) (*httptest.Server, *url.Values, *int32, *int32) {
	t.Helper()
	var mangaQuery url.Values
	var authorHits, mangaHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/author") {
			atomic.AddInt32(&authorHits, 1)
			_, _ = w.Write([]byte(authorJSON))
			return
		}
		atomic.AddInt32(&mangaHits, 1)
		mangaQuery = r.URL.Query()
		_, _ = w.Write([]byte(searchBody))
	}))
	return srv, &mangaQuery, &authorHits, &mangaHits
}

func testClient(srv *httptest.Server) *Client {
	c := NewClient("TestAgent/1.0")
	c.base = srv.URL
	c.interval = time.Millisecond
	return c
}

// mapTags implements "map what fits": author/artist -> Artist (deduped), originalLanguage
// -> Language, demographic + the format group -> Category, genre/theme/content -> Tag.
// MangaDex has no parody/character/group, so none are produced.
func TestMapTags(t *testing.T) {
	var m mdManga
	m.Attributes.OriginalLanguage = "ja"
	demo := "shounen"
	m.Attributes.PublicationDemographic = &demo
	m.Relationships = []mdRel{
		{Type: "author", Attributes: relAttr("Kishimoto")},
		{Type: "artist", Attributes: relAttr("Kishimoto")}, // duplicate of the author
	}
	m.Attributes.Tags = []mdTag{
		mkTag("genre", "Action"),
		mkTag("format", "Oneshot"),
		mkTag("theme", "Ninja"),
	}
	got := map[string]string{} // name -> subject
	for _, tt := range mapTags(m) {
		if prev, ok := got[tt.Name]; ok {
			t.Errorf("duplicate tag %q (subjects %q, %q)", tt.Name, prev, tt.Type)
		}
		got[tt.Name] = tt.Type
	}
	want := map[string]string{
		"kishimoto": tag.Artist,
		"japanese":  tag.Language,
		"shounen":   tag.Category,
		"oneshot":   tag.Category,
		"action":    tag.Tag,
		"ninja":     tag.Tag,
	}
	for name, subj := range want {
		if got[name] != subj {
			t.Errorf("tag %q subject = %q, want %q", name, got[name], subj)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d tags, want %d: %v", len(got), len(want), got)
	}
}

func relAttr(name string) (a struct {
	Name     string `json:"name"`
	FileName string `json:"fileName"`
}) {
	a.Name = name
	return a
}

func mkTag(group, en string) mdTag {
	var t mdTag
	t.Attributes.Group = group
	t.Attributes.Name = map[string]string{"en": en}
	return t
}

const searchBody = `{
  "data": [
    {
      "id": "abc-123",
      "type": "manga",
      "attributes": {
        "title": {"en": "Naruto"},
        "altTitles": [{"ja": "ナルト"}],
        "originalLanguage": "ja",
        "publicationDemographic": "shounen",
        "tags": [
          {"attributes": {"name": {"en": "Action"}, "group": "genre"}}
        ]
      },
      "relationships": [
        {"id": "a1", "type": "author", "attributes": {"name": "Masashi Kishimoto"}},
        {"id": "c1", "type": "cover_art", "attributes": {"fileName": "cover.jpg"}}
      ]
    }
  ],
  "limit": 25, "offset": 0, "total": 1
}`

// Search must build the request (title, content ratings, includes) and map the collection
// onto neutral results with a cover URL, gallery URL, and subject-typed tags.
func TestSearchMapsResults(t *testing.T) {
	var gotTitle string
	var gotRatings, gotIncludes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotTitle = q.Get("title")
		gotRatings = len(q["contentRating[]"])
		gotIncludes = len(q["includes[]"])
		_, _ = w.Write([]byte(searchBody))
	}))
	defer srv.Close()

	resp, err := testClient(srv).Search(context.Background(), source.SearchQuery{Title: "Naruto", Page: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotTitle != "Naruto" {
		t.Errorf("title param = %q", gotTitle)
	}
	if gotRatings == 0 || gotIncludes == 0 {
		t.Errorf("expected contentRating[] + includes[] params (got %d, %d)", gotRatings, gotIncludes)
	}
	if len(resp.Result) != 1 {
		t.Fatalf("got %d results, want 1", len(resp.Result))
	}
	g := resp.Result[0]
	if g.ID != "abc-123" || g.EnglishTitle != "Naruto" || g.JapaneseTitle != "ナルト" {
		t.Errorf("titles/id wrong: %+v", g)
	}
	// originalLanguage "ja" is promoted onto the neutral Language field (in our vocabulary)
	// so the matcher can rank by language without a title decoration.
	if g.Language != "japanese" {
		t.Errorf("language = %q, want japanese", g.Language)
	}
	if g.GalleryURL != "https://mangadex.org/title/abc-123" {
		t.Errorf("gallery url = %q", g.GalleryURL)
	}
	if g.Thumbnail != "https://uploads.mangadex.org/covers/abc-123/cover.jpg.256.jpg" {
		t.Errorf("thumbnail = %q", g.Thumbnail)
	}
	// Tags come mapped: the author relationship and the genre tag, both present.
	subjects := map[string]string{}
	for _, tt := range g.Tags {
		subjects[tt.Name] = tt.Type
	}
	if subjects["masashi kishimoto"] != tag.Artist || subjects["action"] != tag.Tag {
		t.Errorf("mapped tags wrong: %+v", g.Tags)
	}
}

// The language filter must go out under MangaDex's SINGULAR parameter name. The plural
// spelling is not merely ignored — it fails the whole request with a 400 ("The property
// availableTranslatedLanguages is not defined and the definition does not allow additional
// properties"), so every language-narrowed search errored out.
func TestSearchLanguageFilterParamName(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(searchBody))
	}))
	defer srv.Close()

	if _, err := testClient(srv).Search(context.Background(), source.SearchQuery{Title: "Naruto", Language: "english", Page: 1}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if v := got["availableTranslatedLanguage[]"]; len(v) != 1 || v[0] != "en" {
		t.Errorf("availableTranslatedLanguage[] = %v, want [en]", v)
	}
	if v := got["availableTranslatedLanguages[]"]; len(v) != 0 {
		t.Errorf("plural availableTranslatedLanguages[] sent (%v) — MangaDex 400s on it", v)
	}
}

// The headline of the structured-query refactor. MangaDex filters by author with a UUID
// and rejects a name, so the old string contract folded the artist name into the title
// instead — `title=kinomoto anzu best`, which returns zero results against the live API
// because MangaDex titles never contain the author's name. The artist must now travel as
// authorOrArtist=<uuid> and leave the title untouched.
func TestSearchArtistDoesNotCorruptTitle(t *testing.T) {
	srv, mangaQuery, _, _ := authorRouter(t, authorBody)
	defer srv.Close()

	q := source.SearchQuery{Artist: "kinomoto anzu", Title: "best", Page: 1}
	if _, err := testClient(srv).Search(context.Background(), q); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := mangaQuery.Get("title"); got != "best" {
		t.Errorf("title = %q, want %q — the artist name must not leak into the title", got, "best")
	}
	if got := mangaQuery.Get("authorOrArtist"); got != "aaaa-1111" {
		t.Errorf("authorOrArtist = %q, want the resolved UUID", got)
	}
}

// An artist MangaDex has never heard of, with no title to fall back on, must NOT become an
// unconstrained /manga request — that would hand the matcher MangaDex's front page as
// candidates. Search is best-effort, so the right answer is an empty response, not an error.
func TestSearchUnknownArtistNoTitleReturnsEmpty(t *testing.T) {
	srv, _, _, mangaHits := authorRouter(t, `{"data":[]}`)
	defer srv.Close()

	resp, err := testClient(srv).Search(context.Background(), source.SearchQuery{Artist: "nobody", Page: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Result) != 0 {
		t.Errorf("got %d results, want 0", len(resp.Result))
	}
	if n := atomic.LoadInt32(mangaHits); n != 0 {
		t.Errorf("issued %d /manga requests, want 0 (nothing to filter on)", n)
	}
}

// A sweep pages one artist's whole catalog, so the author lookup must be memoized: one
// extra request per artist per run, not one per query.
func TestAuthorLookupMemoized(t *testing.T) {
	srv, _, authorHits, _ := authorRouter(t, authorBody)
	defer srv.Close()

	c := testClient(srv)
	for i := 1; i <= 2; i++ {
		if _, err := c.Search(context.Background(), source.SearchQuery{Artist: "kinomoto anzu", Page: i}); err != nil {
			t.Fatalf("Search %d: %v", i, err)
		}
	}
	if n := atomic.LoadInt32(authorHits); n != 1 {
		t.Errorf("%d /author lookups for the same artist, want 1 (memoized)", n)
	}
}

// A plain title search must cost no author lookup at all.
func TestSearchPlainTitleSkipsAuthorLookup(t *testing.T) {
	srv, _, authorHits, _ := authorRouter(t, authorBody)
	defer srv.Close()

	if _, err := testClient(srv).Search(context.Background(), source.SearchQuery{Title: "Naruto", Page: 1}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if n := atomic.LoadInt32(authorHits); n != 0 {
		t.Errorf("issued %d /author lookups for an artist-less query, want 0", n)
	}
}

// A 429 (which MangaDex returns under load) must be retried honoring Retry-After, not
// failed outright — the same contract nhentai's client already meets.
func TestRetriesOn429ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "1") // shortest realistic backoff -> ~1s
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(searchBody))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	resp, err := testClient(srv).Search(ctx, source.SearchQuery{Title: "naruto", Page: 1})
	if err != nil {
		t.Fatalf("Search after 429: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("server called %d times, want 2 (429 then 200)", calls)
	}
}
