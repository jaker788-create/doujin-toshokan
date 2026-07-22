package main

// End-to-end test for the MatchSource bound method through the REAL provider wiring
// (roadmap 3.8). Existing tests cover the MangaDex client and the matcher separately, and
// chain_test.go drives matchThroughChain with stubs — but nothing exercised
// MatchSource -> chainProviders -> activeProvider's buildProvider -> a real mangadex.Client
// -> gatherCandidates -> Decide as one piece. This does, by pointing the real client at a
// fake MangaDex server through config.SourceConfig.BaseURL (the override 3.8 added to the
// mangadex client for exactly this, mirroring hitomi/e-hentai).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"doujin/internal/config"
	"doujin/internal/ingest"
)

// The fake answers the three endpoints the matcher's ladder reaches on MangaDex: the author
// lookup (name -> UUID), the /manga search, and the /manga/{id} detail. Every /manga search
// returns the one matching series regardless of query, so the test does not depend on the
// exact order of the query ladder — only on the wiring carrying results through.
const (
	mdAuthorJSON = `{"data":[{"id":"author-uuid-1","type":"author","attributes":{"name":"Sample Artist"}}]}`

	mdMangaJSON = `{"id":"manga-uuid-1","type":"manga","attributes":{` +
		`"title":{"en":"Sample Series"},"originalLanguage":"ja",` +
		`"tags":[{"attributes":{"name":{"en":"Action"},"group":"genre"}}]},` +
		`"relationships":[` +
		`{"id":"author-uuid-1","type":"author","attributes":{"name":"Sample Artist"}},` +
		`{"id":"c1","type":"cover_art","attributes":{"fileName":"cover.jpg"}}]}`

	mdSearchJSON = `{"data":[` + mdMangaJSON + `],"limit":25,"total":1}`
	mdDetailJSON = `{"data":` + mdMangaJSON + `}`
)

// mdFakeServer serves the MangaDex endpoints and counts hits so the test can assert the
// method actually reached the network (rather than short-circuiting before the wiring).
func mdFakeServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch {
		case strings.HasPrefix(r.URL.Path, "/author"):
			_, _ = w.Write([]byte(mdAuthorJSON))
		case strings.HasPrefix(r.URL.Path, "/manga/"): // detail: /manga/{id}
			_, _ = w.Write([]byte(mdDetailJSON))
		case r.URL.Path == "/manga": // search: /manga?title=...
			_, _ = w.Write([]byte(mdSearchJSON))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	return srv, &calls
}

// activateMangaDex points config at a fake MangaDex server as the sole active source. The
// tiny RateLimitMs keeps the throttle out of the test's way (and exercises the 3.9 override
// through the same buildProvider path).
func activateMangaDex(t *testing.T, a *App, baseURL string) {
	t.Helper()
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sources = []config.SourceConfig{{
		Provider: "mangadex", BaseURL: baseURL, RateLimitMs: 1, Enabled: true,
	}}
	cfg.ActiveSource = "mangadex"
	if err := config.Save(cfg, a.dataDir); err != nil {
		t.Fatal(err)
	}
}

// A local title with an exact-titled, same-artist MangaDex series must auto-apply, and the
// result must be attributed to MangaDex end to end: the built client, the search ladder, the
// scorer's auto decision, the detail-fetched preview tags, and the server-built cover all
// have to line up through the real bound method.
func TestMatchSourceMangaDexEndToEnd(t *testing.T) {
	a := newTestApp(t)
	a.ctx = context.Background()

	srv, calls := mdFakeServer(t)
	defer srv.Close()
	activateMangaDex(t, a, srv.URL)

	id, err := ingest.IngestManga(a.db, ingest.MangaInput{
		Title:      "Sample Series",
		Author:     "Sample Artist",
		FolderPath: "/lib/Sample Artist/Sample Series",
		PageCount:  20,
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := a.MatchSource(id)
	if err != nil {
		t.Fatalf("MatchSource: %v", err)
	}
	if atomic.LoadInt32(calls) == 0 {
		t.Fatal("the fake server was never hit — MatchSource did not reach the built client")
	}

	// The decision and its provenance: an exact-titled, artist-confirmed series with no page
	// count still auto-applies (route (d)/(b) in autotag.qualifies), and it is stamped MangaDex.
	if res.Decision != "auto" {
		t.Fatalf("Decision = %q, want auto (trace: exact title + confirmed artist)", res.Decision)
	}
	if res.SourceSlug != "mangadex" || res.SourceLabel != "MangaDex" {
		t.Errorf("provenance = %q/%q, want mangadex/MangaDex", res.SourceSlug, res.SourceLabel)
	}
	if len(res.MergeGalleryIDs) != 1 || res.MergeGalleryIDs[0] != "manga-uuid-1" {
		t.Errorf("MergeGalleryIDs = %v, want [manga-uuid-1]", res.MergeGalleryIDs)
	}

	if len(res.Candidates) == 0 {
		t.Fatal("no candidates returned")
	}
	c := res.Candidates[0]
	if c.GalleryID != "manga-uuid-1" || c.SourceSlug != "mangadex" {
		t.Errorf("candidate id/slug = %q/%q, want manga-uuid-1/mangadex", c.GalleryID, c.SourceSlug)
	}
	if c.TitleScore < 0.99 {
		t.Errorf("TitleScore = %.2f, want ~1.0 for an exact title", c.TitleScore)
	}
	if !c.ArtistMatch {
		t.Error("candidate should be artist-matched (author lookup + detail tags both confirm it)")
	}
	// The merge-set candidate is detail-fetched for preview, which populates its tags and the
	// server-built cover (roadmap 3.5) — both prove the GalleryByID leg of the wiring ran.
	if len(c.Tags) == 0 {
		t.Error("candidate tags are empty — the detail preview fetch did not run")
	}
	if want := "https://uploads.mangadex.org/covers/manga-uuid-1/cover.jpg.256.jpg"; c.Thumbnail != want {
		t.Errorf("cover = %q, want %q", c.Thumbnail, want)
	}
}
