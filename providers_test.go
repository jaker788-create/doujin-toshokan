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
