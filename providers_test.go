package main

import (
	"errors"
	"testing"
)

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
	if s, _ := a.GetSettings(); !s.ActiveSourceReady || !s.HasNhentaiKey {
		t.Errorf("settings after key = %+v, want ready + has key", s)
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
