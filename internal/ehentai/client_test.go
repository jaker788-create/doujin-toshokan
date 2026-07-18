package ehentai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"doujin/internal/source"
	"doujin/internal/tag"
)

// Client must satisfy the provider seam.
var _ source.Provider = (*Client)(nil)

// testClient points a fresh client at srv with a tiny throttle interval so the
// rate-limiter logic is exercised without slowing the suite.
func testClient(srv *httptest.Server) *Client {
	c := NewClient("TestAgent/1.0", srv.URL)
	c.interval = time.Millisecond
	return c
}

// galleryBody is a real api.php gdata response, trimmed to the fields we read but
// otherwise verbatim — including the HTML-escaped title, the *string* filecount next to
// the *number* gid, and the full namespace spread (artist/group/parody/character/language
// alongside the content namespaces male/female/other).
const galleryBody = `{"gmetadata":[{"gid":618395,"token":"0439fa3666",` +
	`"title":"[Handful☆Happiness!] Hey, aren&#039;t you my &quot;fap material&quot;?",` +
	`"title_jpn":"東方 &amp; 友達","category":"Doujinshi",` +
	`"thumb":"https://ehgt.org/w/00/310/49862-ddt1sawg.webp","uploader":"avexotsukaai",` +
	`"posted":"1376110208","filecount":"20","filesize":51210504,"expunged":false,` +
	`"rating":"4.55","torrentcount":"0","torrents":[],` +
	`"tags":["language:english","language:translated","parody:touhou project",` +
	`"character:hong meiling","group:handful happiness","artist:nanahara fuyuki",` +
	`"male:muscle","female:big breasts","other:artbook"]}]}`

// serve returns a server answering every request with body, plus a slot capturing the
// decoded request body of the last call.
func serve(body string) (*httptest.Server, *[]map[string]any) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = append(got, req)
		_, _ = w.Write([]byte(body))
	}))
	return srv, &got
}

// The headline mapping: e-hentai's namespaces land on our subjects, filecount becomes the
// page count, and the neutral id is the canonical gid/token pair.
func TestGalleryByIDMapsMetadata(t *testing.T) {
	srv, _ := serve(galleryBody)
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "618395/0439fa3666")
	if err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	if d.ID != "618395/0439fa3666" {
		t.Errorf("ID = %q, want 618395/0439fa3666", d.ID)
	}
	if want := "https://e-hentai.org/g/618395/0439fa3666/"; d.GalleryURL != want {
		t.Errorf("GalleryURL = %q, want %q", d.GalleryURL, want)
	}
	// filecount is a JSON *string* on the live API; a plain int field would not decode it.
	if d.NumPages != 20 {
		t.Errorf("NumPages = %d, want 20 (filecount)", d.NumPages)
	}
	want := []tag.Typed{
		{Name: "english", Type: tag.Language},
		{Name: "translated", Type: tag.Language},
		{Name: "nanahara fuyuki", Type: tag.Artist},
		{Name: "handful happiness", Type: tag.Group},
		{Name: "touhou project", Type: tag.Parody},
		{Name: "hong meiling", Type: tag.Character},
		{Name: "doujinshi", Type: tag.Category},
		{Name: "artbook", Type: tag.Tag},
		{Name: "big breasts", Type: tag.Tag},
		{Name: "muscle", Type: tag.Tag},
	}
	if got := d.Tags; !reflect.DeepEqual(got, tag.Sort(want)) {
		t.Errorf("Tags =\n%v\nwant\n%v", got, tag.Sort(want))
	}
}

// Titles arrive HTML-escaped. Left escaped they are what the matcher string-compares
// against the local folder name, so every entity is lost match score — and the UI would
// render the raw entity text.
func TestTitlesAreHTMLUnescaped(t *testing.T) {
	srv, _ := serve(galleryBody)
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "618395/0439fa3666")
	if err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	if want := `[Handful☆Happiness!] Hey, aren't you my "fap material"?`; d.EnglishTitle != want {
		t.Errorf("EnglishTitle = %q, want %q", d.EnglishTitle, want)
	}
	if d.PrettyTitle != d.EnglishTitle {
		t.Errorf("PrettyTitle = %q, want it to match EnglishTitle", d.PrettyTitle)
	}
	if want := "東方 & 友達"; d.JapaneseTitle != want {
		t.Errorf("JapaneseTitle = %q, want %q", d.JapaneseTitle, want)
	}
}

// The two request-shape facts the live API punishes silently. "gmetadata" is the response
// key, NOT the method — sending it as the method returns an "Unsupported method" error —
// and namespace:1 is what makes tags namespaced at all.
func TestRequestUsesGdataMethodWithNamespace(t *testing.T) {
	srv, reqs := serve(galleryBody)
	defer srv.Close()

	if _, err := testClient(srv).GalleryByID(context.Background(), "618395/0439fa3666"); err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("made %d requests, want 1", len(*reqs))
	}
	req := (*reqs)[0]
	if req["method"] != "gdata" {
		t.Errorf("method = %v, want gdata (gmetadata is the response key, not the method)", req["method"])
	}
	if req["namespace"] != float64(1) {
		t.Errorf("namespace = %v, want 1 — without it every tag comes back unnamespaced", req["namespace"])
	}
	want := []any{[]any{float64(618395), "0439fa3666"}}
	if !reflect.DeepEqual(req["gidlist"], want) {
		t.Errorf("gidlist = %#v, want %#v (gid must be a number, token a string)", req["gidlist"], want)
	}
}

// Without namespace:1 the response is structurally identical but every tag is bare. It
// must degrade to untyped tags rather than mis-assigning subjects — and this is the shape
// that proves the namespace assertion above is load-bearing.
func TestUnnamespacedTagsFallBackToGenericSubject(t *testing.T) {
	const bare = `{"gmetadata":[{"gid":1,"token":"aa","title":"T","category":"Doujinshi",` +
		`"filecount":"2","tags":["nanahara fuyuki","big breasts"]}]}`
	srv, _ := serve(bare)
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "1/aa")
	if err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	for _, tg := range d.Tags {
		if tg.Type == tag.Category {
			continue // the category is not a namespaced tag
		}
		if tg.Type != tag.Tag {
			t.Errorf("tag %q got subject %q, want the generic tag subject", tg.Name, tg.Type)
		}
	}
}

// An unknown gallery or a wrong token is HTTP 200 with a per-entry error. Missing it would
// apply a blank detail as a successful match.
func TestPerGalleryErrorIsSurfaced(t *testing.T) {
	const body = `{"gmetadata":[{"gid":618395,"error":"Key missing, or incorrect key provided."}]}`
	srv, _ := serve(body)
	defer srv.Close()

	_, err := testClient(srv).GalleryByID(context.Background(), "618395/deadbeefff")
	if err == nil {
		t.Fatal("want an error for a 200 carrying a per-gallery error, got nil")
	}
	if !strings.Contains(err.Error(), "incorrect key") {
		t.Errorf("error = %v, want it to quote the API's message", err)
	}
}

// A bad method name fails at the envelope level instead.
func TestTopLevelErrorIsSurfaced(t *testing.T) {
	srv, _ := serve(`{"error":"Unsupported method provided"}`)
	defer srv.Close()

	_, err := testClient(srv).GalleryByID(context.Background(), "1/aa")
	if err == nil || !strings.Contains(err.Error(), "Unsupported method") {
		t.Fatalf("err = %v, want the top-level API error surfaced", err)
	}
}

func TestEmptyMetadataListErrors(t *testing.T) {
	srv, _ := serve(`{"gmetadata":[]}`)
	defer srv.Close()

	if _, err := testClient(srv).GalleryByID(context.Background(), "1/aa"); err == nil {
		t.Fatal("want an error for an empty gmetadata array, got nil")
	}
}

// The folder-name shortcut yields "<gid>-<token>" (a slash is illegal in a filename), but
// the ref stamped on the title must be the canonical slash form either way.
func TestDashRefIsAcceptedAndCanonicalized(t *testing.T) {
	srv, reqs := serve(galleryBody)
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "618395-0439fa3666")
	if err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	if d.ID != "618395/0439fa3666" {
		t.Errorf("ID = %q, want the canonical slash form", d.ID)
	}
	if got := (*reqs)[0]["gidlist"]; !reflect.DeepEqual(got, []any{[]any{float64(618395), "0439fa3666"}}) {
		t.Errorf("gidlist = %#v — the dash form must split the same way", got)
	}
}

// The pair the response reports wins over the one we asked for, so a gallery answering
// under a different canonical identity is normalized before it is stamped.
func TestResponsePairWinsOverRequestedOne(t *testing.T) {
	const body = `{"gmetadata":[{"gid":4646,"token":"ffffffffff","title":"T","filecount":"1"}]}`
	srv, _ := serve(body)
	defer srv.Close()

	d, err := testClient(srv).GalleryByID(context.Background(), "900/0439fa3666")
	if err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	if d.ID != "4646/ffffffffff" {
		t.Errorf("ID = %q, want the pair the document reports", d.ID)
	}
	if want := "https://e-hentai.org/g/4646/ffffffffff/"; d.GalleryURL != want {
		t.Errorf("GalleryURL = %q, want %q", d.GalleryURL, want)
	}
}

// gid and filecount are inconsistently typed across the API (gid number + filecount
// string in one response). Both shapes must decode for either field.
func TestNumbersDecodeAsNumberOrString(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"number gid, string filecount", `{"gmetadata":[{"gid":7,"token":"ab","filecount":"5","title":"T"}]}`},
		{"string gid, number filecount", `{"gmetadata":[{"gid":"7","token":"ab","filecount":5,"title":"T"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := serve(tc.body)
			defer srv.Close()

			d, err := testClient(srv).GalleryByID(context.Background(), "7/ab")
			if err != nil {
				t.Fatalf("GalleryByID: %v", err)
			}
			if d.ID != "7/ab" {
				t.Errorf("ID = %q, want 7/ab", d.ID)
			}
			if d.NumPages != 5 {
				t.Errorf("NumPages = %d, want 5", d.NumPages)
			}
		})
	}
}

// A malformed ref must be rejected before any request is made.
func TestBadRefRejectedWithoutRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(galleryBody))
	}))
	defer srv.Close()
	c := testClient(srv)

	for _, ref := range []string{"", "   ", "618395", "abc/0439fa3666", "618395/zzzz", "618395/", "0/aa", "-5/aa"} {
		if _, err := c.GalleryByID(context.Background(), ref); err == nil {
			t.Errorf("GalleryByID(%q) = nil error, want a rejection", ref)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("made %d requests for malformed refs, want 0", n)
	}
}

// Search is a no-op by contract: it must not touch the network, and it must return an
// empty response rather than an error so a sweep reads as "no match", not as failures.
func TestSearchReturnsEmptyWithoutRequesting(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	resp, err := testClient(srv).Search(context.Background(), source.SearchQuery{Title: "anything", Artist: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp == nil || len(resp.Result) != 0 {
		t.Errorf("Search returned %+v, want an empty result set", resp)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("Search made %d requests, want 0", n)
	}
}

func TestBaseURLOverride(t *testing.T) {
	if got := NewClient("UA", "").base; got != DefaultBaseURL {
		t.Errorf("empty baseURL gave %q, want %q", got, DefaultBaseURL)
	}
	if got := NewClient("UA", "  https://example.test/api.php  ").base; got != "https://example.test/api.php" {
		t.Errorf("override gave %q", got)
	}
}

func TestRequestHeadersAndMethod(t *testing.T) {
	var method, ua, ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, ua, ct = r.Method, r.Header.Get("User-Agent"), r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(galleryBody))
	}))
	defer srv.Close()

	if _, err := testClient(srv).GalleryByID(context.Background(), "618395/0439fa3666"); err != nil {
		t.Fatalf("GalleryByID: %v", err)
	}
	if method != http.MethodPost {
		t.Errorf("method = %s, want POST", method)
	}
	if ua != "TestAgent/1.0" {
		t.Errorf("User-Agent = %q", ua)
	}
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestSlugAndLabel(t *testing.T) {
	c := NewClient("UA", "")
	if c.Slug() != "ehentai" || c.Label() != "E-Hentai" {
		t.Errorf("Slug/Label = %q/%q", c.Slug(), c.Label())
	}
}
