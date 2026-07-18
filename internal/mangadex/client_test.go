package mangadex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"doujin/internal/tag"
)

func testClient(srv *httptest.Server) *Client {
	c := NewClient("TestAgent/1.0")
	c.base = srv.URL
	c.interval = time.Millisecond
	return c
}

// parseQuery must translate the matcher's nhentai-style query syntax into a plain title
// search plus an optional language name — MangaDex can't parse artist:/title:/language:.
func TestParseQuery(t *testing.T) {
	cases := []struct{ raw, wantText, wantLang string }{
		{`Naruto`, "Naruto", ""},
		{`Some Title language:english`, "Some Title", "english"},
		{`artist:"kinomoto anzu" title:"best"`, "kinomoto anzu best", ""},
		{`artist:"x"`, "x", ""},
		{`artist:"x" title:"word" language:japanese`, "x word", "japanese"},
	}
	for _, c := range cases {
		text, lang := parseQuery(c.raw)
		if text != c.wantText || lang != c.wantLang {
			t.Errorf("parseQuery(%q) = (%q, %q), want (%q, %q)", c.raw, text, lang, c.wantText, c.wantLang)
		}
	}
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

	resp, err := testClient(srv).Search(context.Background(), "Naruto", 1)
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

	if _, err := testClient(srv).Search(context.Background(), "Naruto language:english", 1); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if v := got["availableTranslatedLanguage[]"]; len(v) != 1 || v[0] != "en" {
		t.Errorf("availableTranslatedLanguage[] = %v, want [en]", v)
	}
	if v := got["availableTranslatedLanguages[]"]; len(v) != 0 {
		t.Errorf("plural availableTranslatedLanguages[] sent (%v) — MangaDex 400s on it", v)
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
	resp, err := testClient(srv).Search(ctx, "naruto", 1)
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
