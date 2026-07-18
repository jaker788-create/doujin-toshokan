package nhentai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"doujin/internal/source"
)

// testClient points a fresh client at srv with a tiny throttle interval so the
// rate-limiter logic is exercised without slowing the suite.
func testClient(srv *httptest.Server) *Client {
	c := NewClient("test-key", "TestAgent/1.0")
	c.base = srv.URL
	c.interval = 5 * time.Millisecond
	return c
}

const searchBody = `{
  "result": [
    {"id": 653427, "media_id": "123", "english_title": "Karakishi Youhei-dan Compilation",
     "japanese_title": "", "num_pages": 50, "num_favorites": 12,
     "thumbnail": "https://t.nhentai.net/galleries/123/thumb.jpg", "tag_ids": [33172, 13159]}
  ],
  "num_pages": 1, "per_page": 25, "total": 1
}`

const detailBody = `{
  "id": 653427,
  "title": {"english": "Karakishi Youhei-dan Compilation", "japanese": "", "pretty": "Karakishi"},
  "num_pages": 50,
  "scanlator": "",
  "tags": [
    {"id": 33172, "type": "category", "name": "doujinshi", "slug": "doujinshi", "count": 485878},
    {"id": 13159, "type": "parody", "name": "naruto", "slug": "naruto", "count": 2142},
    {"id": 7584, "type": "tag", "name": "compilation", "slug": "compilation", "count": 1454}
  ]
}`

// buildQuery is the only place in the app that speaks nhentai's search syntax, so it must
// reproduce every shape the matcher's ladder emits, byte for byte. The table below IS that
// ladder: primary title variant, artist narrowed by the first title word, the bare artist
// catalog, and each of those under a language filter.
//
// The last case is load-bearing rather than cosmetic: a lone "language:english" is not a
// search, it matches every English gallery on the site, so an otherwise-empty query must
// render empty and never be sent.
func TestBuildQueryRendersNhentaiSyntax(t *testing.T) {
	cases := []struct {
		name string
		q    source.SearchQuery
		want string
	}{
		{"bare title", source.SearchQuery{Title: "A Little Sister's Warmth"}, `A Little Sister's Warmth`},
		{"title + language", source.SearchQuery{Title: "Some Title", Language: "english"}, `Some Title language:english`},
		{"artist + title word", source.SearchQuery{Artist: "kinomoto anzu", Title: "little"}, `artist:"kinomoto anzu" title:"little"`},
		{"bare artist catalog", source.SearchQuery{Artist: "kinomoto anzu"}, `artist:"kinomoto anzu"`},
		{"artist + language", source.SearchQuery{Artist: "kinomoto anzu", Language: "english"}, `artist:"kinomoto anzu" language:english`},
		{"language alone renders empty", source.SearchQuery{Language: "english"}, ``},
		{"whitespace only renders empty", source.SearchQuery{Title: "   ", Language: "english"}, ``},
	}
	for _, c := range cases {
		if got := buildQuery(c.q); got != c.want {
			t.Errorf("%s: buildQuery(%+v) = %q, want %q", c.name, c.q, got, c.want)
		}
	}
}

// The rendered syntax must actually reach the wire — buildQuery being correct in isolation
// is no use if Search sends something else.
func TestSearchSendsBuiltQuery(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(searchBody))
	}))
	defer srv.Close()

	q := source.SearchQuery{Artist: "some artist", Title: "little", Language: "english", Page: 1}
	if _, err := testClient(srv).Search(context.Background(), q); err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := `artist:"some artist" title:"little" language:english`
	if gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
}

func TestSearchDecodesAndSetsHeaders(t *testing.T) {
	var gotAuth, gotUA, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotQuery = r.URL.Query().Get("query")
		if !strings.HasPrefix(r.URL.Path, "/search") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(searchBody))
	}))
	defer srv.Close()

	resp, err := testClient(srv).Search(context.Background(), source.SearchQuery{Title: "naruto", Page: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAuth != "Key test-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Key test-key")
	}
	if gotUA != "TestAgent/1.0" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if gotQuery != "naruto" {
		t.Errorf("query = %q", gotQuery)
	}
	if len(resp.Result) != 1 {
		t.Fatalf("got %d results, want 1", len(resp.Result))
	}
	g := resp.Result[0]
	if g.ID != "653427" || g.NumPages != 50 || g.EnglishTitle == "" {
		t.Errorf("decoded result wrong: %+v", g)
	}
	// The cover identifiers must decode so the review UI can show a thumbnail.
	if g.MediaID != "123" {
		t.Errorf("media_id = %q, want 123", g.MediaID)
	}
	if g.Thumbnail != "https://t.nhentai.net/galleries/123/thumb.jpg" {
		t.Errorf("thumbnail = %q", g.Thumbnail)
	}
	// The provider builds an absolute gallery URL (search returns tag ids only, so no
	// tag names come back on a list item).
	if g.GalleryURL != "https://nhentai.net/g/653427/" {
		t.Errorf("gallery_url = %q", g.GalleryURL)
	}
}

func TestGalleryByIDDecodesTypedTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/galleries/653427" {
			t.Errorf("path = %q, want /galleries/653427", r.URL.Path)
		}
		_, _ = w.Write([]byte(detailBody))
	}))
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "653427")
	if err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	if d.EnglishTitle != "Karakishi Youhei-dan Compilation" {
		t.Errorf("english title = %q", d.EnglishTitle)
	}
	if len(d.Tags) != 3 {
		t.Fatalf("got %d tags, want 3", len(d.Tags))
	}
	// Tags arrive already mapped onto our subject vocabulary (nhentai's "parody" type
	// normalizes to tag.Parody, which is the same "parody" string).
	if d.Tags[1].Type != "parody" || d.Tags[1].Name != "naruto" {
		t.Errorf("tag[1] = %+v, want parody/naruto", d.Tags[1])
	}
}

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

	c := testClient(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	resp, err := c.Search(ctx, source.SearchQuery{Title: "naruto", Page: 1})
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

func TestThrottleSpacesRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(searchBody))
	}))
	defer srv.Close()

	c := testClient(srv)
	c.interval = 60 * time.Millisecond
	start := time.Now()
	for i := range 3 {
		if _, err := c.Search(context.Background(), source.SearchQuery{Title: "x", Page: 1}); err != nil {
			t.Fatalf("Search %d: %v", i, err)
		}
	}
	// 3 requests => 2 enforced gaps of 60ms => at least ~120ms total.
	if elapsed := time.Since(start); elapsed < 110*time.Millisecond {
		t.Errorf("3 requests took %v, expected >=110ms from throttling", elapsed)
	}
}

func TestNoKeyReturnsErrNoKey(t *testing.T) {
	c := NewClient("", "TestAgent/1.0")
	if _, err := c.Search(context.Background(), source.SearchQuery{Title: "x", Page: 1}); !errors.Is(err, ErrNoKey) {
		t.Errorf("err = %v, want ErrNoKey", err)
	}
}

func TestNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := testClient(srv).Search(context.Background(), source.SearchQuery{Title: "x", Page: 1})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v, want a 403 error", err)
	}
}
