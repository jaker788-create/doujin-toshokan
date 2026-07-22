package main

import (
	"errors"
	"strings"
	"testing"

	"doujin/internal/doujin"
	"doujin/internal/ingest"
	"doujin/internal/search"
)

// refHintSamples is a real gallery ref per provider, in the shape its folder-name prefix
// takes. Adding a provider without adding a row here fails the test below on purpose.
var refHintSamples = map[string]string{
	"nhentai":  "271687",
	"mangadex": "550e8400-e29b-41d4-a716-446655440000",
	"hitomi":   "4056725",
	"ehentai":  "618395-0439fa3666",
}

// Every preset's RefHint is shown to the user as the folder name that routes a title to
// that source, so it has to describe what internal/doujin's sourceDefs actually accepts.
// The two live in different packages (a leaf parser cannot import the provider registry),
// which is exactly the kind of pair that drifts silently — the failure mode being a UI
// that documents a folder name producing no match at all.
//
// The placeholder count is the assertion that matters: a single-<id> hint on e-hentai,
// whose ref is a gid AND a token, looks entirely plausible and is wrong.
func TestRefHintsMatchTheFolderPrefixParser(t *testing.T) {
	for _, p := range providerPresets {
		sample, ok := refHintSamples[p.Slug]
		if !ok {
			t.Errorf("provider %q has no refHintSamples row — add one", p.Slug)
			continue
		}
		if p.RefHint == "" {
			t.Errorf("provider %q has no RefHint; the id_only note would guess a form", p.Slug)
			continue
		}
		if !strings.HasPrefix(p.RefHint, p.Slug+"-") {
			t.Errorf("RefHint %q must start with %q-", p.RefHint, p.Slug)
		}
		// The sample must actually route through the parser to this provider.
		parsed := doujin.ParseName(p.Slug + "-" + sample + " - [Circle] Title")
		if parsed.SourceSlug != p.Slug || parsed.SourceRef != sample {
			t.Errorf("ParseName of a %s folder gave (%q,%q), want (%q,%q)",
				p.Slug, parsed.SourceSlug, parsed.SourceRef, p.Slug, sample)
		}
		// One <placeholder> per dash-separated component of the real ref. mangadex's UUID
		// contains dashes but is one component, so it is compared as a whole.
		wantParts := 1
		if p.Slug != "mangadex" {
			wantParts = strings.Count(sample, "-") + 1
		}
		if got := strings.Count(p.RefHint, "<"); got != wantParts {
			t.Errorf("RefHint %q has %d placeholder(s), but a %s ref has %d component(s)",
				p.RefHint, got, p.Slug, wantParts)
		}
	}
}

// Picking MangaDex (which needs no key) must create + enable it and make it the active
// provider immediately — not silently fall back to a legacy nhentai entry.
func TestSetActiveSourceEnablesMangadex(t *testing.T) {
	a := newTestApp(t)
	if err := a.SetActiveSource("mangadex"); err != nil {
		t.Fatal(err)
	}
	p, err := a.activeProvider()
	if err != nil {
		t.Fatalf("activeProvider: %v", err)
	}
	if p.Slug() != "mangadex" {
		t.Errorf("active provider = %q, want mangadex", p.Slug())
	}
	s, err := a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSource != "mangadex" || !s.ActiveSourceReady {
		t.Errorf("settings = %+v, want active mangadex + ready (no key needed)", s)
	}
}

// nhentai is not usable until a key is set: activeProvider errors with errNoAPIKey, then
// succeeds once SetSourceConfig stores a key. GetSettings reflects readiness both ways.
func TestNhentaiRequiresKey(t *testing.T) {
	a := newTestApp(t)
	if err := a.SetActiveSource("nhentai"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.activeProvider(); !errors.Is(err, errNoAPIKey) {
		t.Errorf("activeProvider without key = %v, want errNoAPIKey", err)
	}
	if s, _ := a.GetSettings(); s.ActiveSourceReady {
		t.Error("nhentai without a key should not be ready")
	}

	if err := a.SetSourceConfig("nhentai", "secret", "", true); err != nil {
		t.Fatal(err)
	}
	p, err := a.activeProvider()
	if err != nil {
		t.Fatalf("activeProvider after key: %v", err)
	}
	if p.Slug() != "nhentai" {
		t.Errorf("active = %q, want nhentai", p.Slug())
	}
	if s, _ := a.GetSettings(); !s.ActiveSourceReady {
		t.Errorf("settings after key = %+v, want ready", s)
	}
	// The stored key surfaces (masked) through GetSources, not GetSettings — see roadmap 3.6.
	srcs, _ := a.GetSources()
	for _, s := range srcs {
		if s.Slug == "nhentai" && !s.HasKey {
			t.Errorf("nhentai source after key = %+v, want has_key", s)
		}
	}
}

// hitomi needs no key either, and is the first id-only source: the UI must be told it has
// no free-text search, or a bulk sweep reporting "no match" on every title reads as a bug
// rather than the documented contract.
func TestHitomiIsKeylessAndIDOnly(t *testing.T) {
	a := newTestApp(t)
	if err := a.SetActiveSource("hitomi"); err != nil {
		t.Fatal(err)
	}
	p, err := a.activeProvider()
	if err != nil {
		t.Fatalf("activeProvider: %v", err)
	}
	if p.Slug() != "hitomi" {
		t.Errorf("active provider = %q, want hitomi", p.Slug())
	}
	if s, _ := a.GetSettings(); !s.ActiveSourceReady {
		t.Error("hitomi needs no key, so it should be ready immediately")
	}
	srcs, err := a.GetSources()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		switch s.Slug {
		case "hitomi":
			if s.NeedsKey || !s.IDOnly {
				t.Errorf("hitomi state = %+v, want needs_key=false id_only=true", s)
			}
		case "nhentai", "mangadex":
			if s.IDOnly {
				t.Errorf("%s should not be marked id_only — it has a real search", s.Slug)
			}
		}
	}
}

// E-Hentai is the second ID-only source and, like hitomi, needs no credentials: the public
// gdata API answers unauthenticated. (Cookies would only buy ExHentai-exclusive galleries —
// see roadmap 2.4.) If this ever starts failing because a key is required, the preset's
// NeedsKey is what has to change, not this test.
func TestEHentaiIsKeylessAndIDOnly(t *testing.T) {
	a := newTestApp(t)
	if err := a.SetActiveSource("ehentai"); err != nil {
		t.Fatal(err)
	}
	p, err := a.activeProvider()
	if err != nil {
		t.Fatalf("activeProvider: %v", err)
	}
	if p.Slug() != "ehentai" {
		t.Errorf("active provider = %q, want ehentai", p.Slug())
	}
	if s, _ := a.GetSettings(); !s.ActiveSourceReady {
		t.Error("e-hentai needs no key, so it should be ready immediately")
	}
	srcs, err := a.GetSources()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range srcs {
		if s.Slug != "ehentai" {
			continue
		}
		found = true
		if s.NeedsKey || !s.IDOnly {
			t.Errorf("ehentai state = %+v, want needs_key=false id_only=true", s)
		}
	}
	if !found {
		t.Error("ehentai missing from GetSources — it must be a registered preset")
	}
	// An ID-only provider must be excluded from the fuzzy phase of a sweep, or every title
	// without an embedded id costs a guaranteed-empty pass.
	if !providerIsIDOnly("ehentai") {
		t.Error("providerIsIDOnly(ehentai) = false — the chain would consult its empty Search")
	}
}

// An unknown provider slug is rejected by both setters.
func TestUnknownProviderRejected(t *testing.T) {
	a := newTestApp(t)
	if err := a.SetActiveSource("bogus"); err == nil {
		t.Error("SetActiveSource(bogus) should error")
	}
	if err := a.SetSourceConfig("bogus", "", "", true); err == nil {
		t.Error("SetSourceConfig(bogus) should error")
	}
}

// search.SourceNone is the library filter's "never auto-tagged" sentinel, carried in the
// same field as a real provider slug (SearchArgs.Source). If a provider ever registered
// that slug, filtering for it would silently return the untagged titles instead of that
// source's. internal/search is a leaf package that cannot import this registry to check
// itself, so the pin lives here.
func TestSourceNoneSentinelIsNotAProviderSlug(t *testing.T) {
	for _, p := range providerPresets {
		if p.Slug == search.SourceNone {
			t.Errorf("provider %q uses the reserved untagged sentinel %q — "+
				"rename the slug or change search.SourceNone", p.Label, search.SourceNone)
		}
	}
}

// The facet list must label every slug the library actually holds, including one whose
// provider is no longer registered — those titles still have to be findable.
func TestSourceFacetsLabelUnknownAndUntagged(t *testing.T) {
	a := newTestApp(t)
	id, err := ingest.IngestManga(a.db, ingest.MangaInput{
		Title: "Orphan", Author: "Nobody", FolderPath: "/orphan", PageCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE manga SET source_slug=? WHERE id=?", "defunct-site", id); err != nil {
		t.Fatal(err)
	}
	// A second title left untagged, so both buckets are exercised.
	if _, err := ingest.IngestManga(a.db, ingest.MangaInput{
		Title: "Plain", Author: "Nobody", FolderPath: "/plain", PageCount: 1,
	}); err != nil {
		t.Fatal(err)
	}

	facets, err2 := a.GetSourceFacets()
	if err2 != nil {
		t.Fatalf("GetSourceFacets: %v", err2)
	}
	byS := map[string]SourceFacet{}
	for _, f := range facets {
		byS[f.Slug] = f
	}
	if got, ok := byS["defunct-site"]; !ok || got.Label != "defunct-site" || got.Count != 1 {
		t.Errorf("unregistered slug facet = %+v (all: %+v), want label=slug count=1", got, facets)
	}
	if got, ok := byS[search.SourceNone]; !ok || got.Label != "Untagged" || got.Count != 1 {
		t.Errorf("untagged facet = %+v (all: %+v), want label=Untagged count=1", got, facets)
	}
	if facets[len(facets)-1].Slug != search.SourceNone {
		t.Errorf("untagged facet must sort last, got %+v", facets)
	}
}

// The detail payload resolves source_slug to a display label, because the slug→label
// registry lives here and not in the frontend. An unregistered slug labels as itself
// rather than blank, and a never-tagged title carries no label at all.
func TestGetMangaSourceLabel(t *testing.T) {
	a := newTestApp(t)
	cases := []struct {
		name, slug, want string
	}{
		{"registered", "ehentai", "E-Hentai"},
		{"unregistered", "defunct-site", "defunct-site"},
		{"blank slug", "", ""},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := "/lbl" + string(rune('a'+i))
			id, err := ingest.IngestManga(a.db, ingest.MangaInput{
				Title: "T", Author: "A", FolderPath: path, PageCount: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if c.slug != "" {
				if _, err := a.db.Exec("UPDATE manga SET source_slug=? WHERE id=?", c.slug, id); err != nil {
					t.Fatal(err)
				}
			}
			d, err := a.GetManga(id)
			if err != nil {
				t.Fatalf("GetManga: %v", err)
			}
			if d.SourceLabel != c.want {
				t.Errorf("source_label = %q, want %q", d.SourceLabel, c.want)
			}
		})
	}
}
