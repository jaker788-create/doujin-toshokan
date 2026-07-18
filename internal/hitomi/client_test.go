package hitomi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"doujin/internal/source"
	"doujin/internal/tag"
)

// testClient points a fresh client at srv with a tiny throttle interval so the
// rate-limiter logic is exercised without slowing the suite.
func testClient(srv *httptest.Server) *Client {
	c := NewClient("TestAgent/1.0", srv.URL)
	c.interval = time.Millisecond
	return c
}

// galleryBody is a real /galleries/{id}.js response, trimmed to the fields we read but
// otherwise verbatim — including the "var galleryinfo =" wrapper, the *string* id newer
// galleries use, the null lists hitomi sends for absent categories, and a tag with no
// male/female keys at all alongside gendered ones.
const galleryBody = `var galleryinfo = {"scene_indexes":[],"date":"2026-07-16 08:20:00-05",` +
	`"id":"4056725","videofilename":null,"datepublished":"2018-01-14","blocked":0,` +
	`"characters":null,"artists":[{"artist":"gachonjirou","url":"/artist/gachonjirou-all.html"}],` +
	`"galleryurl":"/doujinshi/sadayo-ga-5-jikan-yarareru-manga-4056725.html",` +
	`"groups":[{"group":"chimamire yashiki","url":"/group/chimamire%20yashiki-all.html"}],` +
	`"language":"chinese","language_url":"/index-chinese.html",` +
	`"files":[{"name":"01.jpg","hash":"aa"},{"name":"02.png","hash":"bb"},{"name":"03.jpg","hash":"cc"}],` +
	`"japanese_title":"貞代が5時間ヤられるまんが",` +
	`"tags":[{"url":"/tag/female%3Aloli-all.html","female":"1","tag":"loli","male":""},` +
	`{"tag":"mosaic censorship","url":"/tag/mosaic%20censorship-all.html"},` +
	`{"tag":"stockings","male":"1","female":"","url":"/tag/male%3Astockings-all.html"}],` +
	`"related":[4055853,2113356],"parodys":[{"parody":"original","url":"/series/original-all.html"}],` +
	`"language_localname":"中文","type":"doujinshi","video":null,` +
	`"title":"Sadayo ga 5-jikan Yarareru Manga"}`

// serve returns a server answering every request with body, plus the paths it was asked for.
func serve(body string) (*httptest.Server, *[]string) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	return srv, &paths
}

// The headline mapping: hitomi's named lists, language and type all land on our subjects,
// and the page count comes from files[].
func TestGalleryByIDMapsMetadata(t *testing.T) {
	srv, paths := serve(galleryBody)
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "4056725")
	if err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	if len(*paths) != 1 || (*paths)[0] != "/galleries/4056725.js" {
		t.Errorf("requested %v, want [/galleries/4056725.js]", *paths)
	}
	if d.ID != "4056725" {
		t.Errorf("ID = %q, want 4056725", d.ID)
	}
	if d.EnglishTitle != "Sadayo ga 5-jikan Yarareru Manga" {
		t.Errorf("EnglishTitle = %q", d.EnglishTitle)
	}
	if d.JapaneseTitle != "貞代が5時間ヤられるまんが" {
		t.Errorf("JapaneseTitle = %q", d.JapaneseTitle)
	}
	if d.NumPages != 3 {
		t.Errorf("NumPages = %d, want 3 (len(files))", d.NumPages)
	}
	if want := "https://hitomi.la/doujinshi/sadayo-ga-5-jikan-yarareru-manga-4056725.html"; d.GalleryURL != want {
		t.Errorf("GalleryURL = %q, want %q", d.GalleryURL, want)
	}

	want := map[tag.Typed]bool{
		{Name: "chinese", Type: tag.Language}:        true,
		{Name: "gachonjirou", Type: tag.Artist}:      true,
		{Name: "chimamire yashiki", Type: tag.Group}: true,
		{Name: "original", Type: tag.Parody}:         true,
		{Name: "doujinshi", Type: tag.Category}:      true,
		{Name: "loli", Type: tag.Tag}:                true,
		{Name: "mosaic censorship", Type: tag.Tag}:   true,
		{Name: "stockings", Type: tag.Tag}:           true,
	}
	got := map[tag.Typed]bool{}
	for _, tg := range d.Tags {
		got[tg] = true
	}
	for tg := range want {
		if !got[tg] {
			t.Errorf("missing tag %+v (got %+v)", tg, d.Tags)
		}
	}
	for tg := range got {
		if !want[tg] {
			t.Errorf("unexpected tag %+v", tg)
		}
	}
}

// Older galleries send the id as a JSON *number* and newer ones as a *string* — both are
// live on the site today (id 5000 is a number, 4056725 is a string). A single-typed field
// would fail to decode half of hitomi, so both must round-trip.
func TestGalleryIDDecodesNumberOrString(t *testing.T) {
	for _, c := range []struct{ name, raw, want string }{
		{"string id", `var galleryinfo = {"id":"4056725","title":"T"}`, "4056725"},
		{"number id", `var galleryinfo = {"id":5000,"title":"T"}`, "5000"},
		{"absent id falls back to the requested one", `var galleryinfo = {"title":"T"}`, "4056725"},
	} {
		srv, _ := serve(c.raw)
		d, err := testClient(srv).GalleryByID(context.Background(), "4056725")
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if d.ID != c.want {
			t.Errorf("%s: ID = %q, want %q", c.name, d.ID, c.want)
		}
	}
}

// The gender markers on tags[] are typed as inconsistently as the gallery id: newer
// galleries send the string "1", older ones the number 1, ungendered tags omit the key
// entirely. All three shapes appear in one response below. We read only the tag name, so
// every variant must decode — declaring male/female as strings silently broke every
// pre-2015 gallery on the live site until this case existed.
func TestTagGenderMarkersOfEitherTypeDecode(t *testing.T) {
	const body = `var galleryinfo = {"id":"1","title":"T","files":[],"tags":[` +
		`{"tag":"loli","female":"1","male":""},` + // newer: strings
		`{"tag":"stockings","female":0,"male":1},` + // older: numbers
		`{"tag":"mosaic censorship"}]}` // ungendered: absent
	srv, _ := serve(body)
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "1")
	if err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	var names []string
	for _, tg := range d.Tags {
		if tg.Type == tag.Tag {
			names = append(names, tg.Name)
		}
	}
	sort.Strings(names)
	want := []string{"loli", "mosaic censorship", "stockings"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("tags = %v, want %v", names, want)
	}
}

// Some old ids are aliases: the live site serves /galleries/900.js from the gallery whose
// own id is 4646 (and 4646.js returns the same document). The canonical id must win, so
// the ref stamped on the local title is the durable one rather than the alias that
// happened to be in a folder name.
func TestAliasIDNormalizesToCanonical(t *testing.T) {
	srv, _ := serve(`var galleryinfo = {"id":4646,"title":"Anejiru The Animation 2","galleryurl":"/anime/anejiru-the-animation-2-english-4646.html"}`)
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "900")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "4646" {
		t.Errorf("ID = %q, want the canonical 4646 rather than the requested alias", d.ID)
	}
	if !strings.HasSuffix(d.GalleryURL, "-4646.html") {
		t.Errorf("GalleryURL = %q, want the canonical gallery", d.GalleryURL)
	}
}

// hitomi sends null (not []) for a category a gallery has none of. That must decode to no
// tags rather than failing the whole fetch.
func TestNullListsDecodeToNoTags(t *testing.T) {
	const body = `var galleryinfo = {"id":"1","title":"T","artists":null,"groups":null,` +
		`"parodys":null,"characters":null,"tags":null,"files":null,"language":"japanese","type":"manga"}`
	srv, _ := serve(body)
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "1")
	if err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	if d.NumPages != 0 {
		t.Errorf("NumPages = %d, want 0", d.NumPages)
	}
	if len(d.Tags) != 2 { // language + type only
		t.Errorf("Tags = %+v, want just language + category", d.Tags)
	}
}

// The wrapper is JavaScript, not JSON. Accept the spacing variants the site could emit
// without warning, and reject anything that is not this document — hitomi answers an
// unknown id with an HTML page, and a silent zero-value gallery would be far worse than
// an error.
func TestDecodeGalleryInfo(t *testing.T) {
	for _, c := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"canonical", `var galleryinfo = {"id":"1"}`, false},
		{"no spaces", `var galleryinfo={"id":"1"}`, false},
		{"trailing semicolon", `var galleryinfo = {"id":"1"};`, false},
		{"leading whitespace", "\n  var galleryinfo = {\"id\":\"1\"}\n", false},
		{"html 404 page", `<html><head><title>404 Not Found</title></head></html>`, true},
		{"bare json", `{"id":"1"}`, true},
		{"different variable", `var somethingelse = {"id":"1"}`, true},
		{"prefix but no assignment", `var galleryinfo {"id":"1"}`, true},
		{"empty", ``, true},
	} {
		g, err := decodeGalleryInfo([]byte(c.body))
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error, got %+v", c.name, g)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if string(g.ID) != "1" {
			t.Errorf("%s: ID = %q, want 1", c.name, g.ID)
		}
	}
}

// A 404 (unknown/stale id) must surface as an error, not as an empty gallery. The
// folder-id shortcut relies on this: it falls through to a fuzzy search when the detail
// fetch fails, which it cannot do if a bad id decodes to a blank success.
func TestUnknownGalleryErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><head><title>404 Not Found</title></head></html>"))
	}))
	defer srv.Close()

	if _, err := testClient(srv).GalleryByID(context.Background(), "999999999"); err == nil {
		t.Fatal("expected an error for a 404 gallery")
	} else if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %v, want it to mention the status", err)
	}
}

// A non-numeric ref must never reach the request path.
func TestBadGalleryIDRejectedWithoutRequest(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	c := testClient(srv)
	for _, id := range []string{"", "  ", "abc", "../secrets", "12ab"} {
		if _, err := c.GalleryByID(context.Background(), id); err == nil {
			t.Errorf("GalleryByID(%q) should error", id)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("made %d requests for invalid ids, want 0", n)
	}
}

// Search is a deliberate no-op: hitomi's search is client-side over binary index files,
// so the provider is id-only. It must return an EMPTY response and no error — an error
// would turn every title in a hitomi sweep into a failure instead of a clean "no match" —
// and it must not touch the network.
func TestSearchReturnsEmptyWithoutRequesting(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	c := testClient(srv)
	for _, q := range []source.SearchQuery{
		{},
		{Title: "some title"},
		{Artist: "gachonjirou", Language: "english", Page: 2},
	} {
		res, err := c.Search(context.Background(), q)
		if err != nil {
			t.Fatalf("Search(%+v): %v", q, err)
		}
		if res == nil {
			t.Fatalf("Search(%+v) returned a nil response", q)
		}
		if len(res.Result) != 0 || res.Total != 0 {
			t.Errorf("Search(%+v) = %+v, want empty", q, res)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("Search made %d requests, want 0", n)
	}
}

// The base URL is configurable so a data-domain move (hitomi has already had one —
// ltn.hitomi.la no longer resolves) is a settings edit rather than a release. An empty
// override must still fall back to the shipped default.
func TestBaseURLOverride(t *testing.T) {
	if got := NewClient("UA", "").base; got != DefaultBaseURL {
		t.Errorf("empty override = %q, want %q", got, DefaultBaseURL)
	}
	if got := NewClient("UA", "  ").base; got != DefaultBaseURL {
		t.Errorf("blank override = %q, want %q", got, DefaultBaseURL)
	}
	if got := NewClient("UA", "https://elsewhere.example/").base; got != "https://elsewhere.example" {
		t.Errorf("override = %q, want the trailing slash trimmed", got)
	}
}

// hitomi needs no auth, but the site expects to be identified; both headers ride on every
// request (the image CDN requires the Referer, and uniformity is free).
func TestRequestHeaders(t *testing.T) {
	var ua, ref string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua, ref = r.Header.Get("User-Agent"), r.Header.Get("Referer")
		_, _ = w.Write([]byte(galleryBody))
	}))
	defer srv.Close()

	if _, err := testClient(srv).GalleryByID(context.Background(), "4056725"); err != nil {
		t.Fatal(err)
	}
	if ua != "TestAgent/1.0" {
		t.Errorf("User-Agent = %q", ua)
	}
	if ref != "https://hitomi.la/" {
		t.Errorf("Referer = %q, want https://hitomi.la/", ref)
	}
}

// A gallery with no galleryurl still needs a working link.
func TestGalleryURLFallback(t *testing.T) {
	srv, _ := serve(`var galleryinfo = {"id":"4056725","title":"T"}`)
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "4056725")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://hitomi.la/galleries/4056725.html"; d.GalleryURL != want {
		t.Errorf("GalleryURL = %q, want %q", d.GalleryURL, want)
	}
}

// Compile-time proof the client satisfies the seam.
var _ source.Provider = (*Client)(nil)
