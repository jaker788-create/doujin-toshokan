package config

import (
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "lib")
	if err := Save(Config{LibraryRoots: []string{want}, Port: 9000}, dir); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LibraryRoots) != 1 || got.LibraryRoots[0] != want {
		t.Errorf("library_roots = %v, want [%q]", got.LibraryRoots, want)
	}
	if got.Port != 9000 {
		t.Errorf("port = %d, want 9000", got.Port)
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.LibraryRoots) != 0 {
		t.Errorf("library_roots = %v, want []", cfg.LibraryRoots)
	}
	if cfg.Port != 8765 {
		t.Errorf("port = %d, want 8765", cfg.Port)
	}
}

func TestPathHelpers(t *testing.T) {
	dir := t.TempDir()
	if got, want := DBPath(dir), filepath.Join(dir, "doujin.db"); got != want {
		t.Errorf("DBPath = %q, want %q", got, want)
	}
	if got, want := ThumbCacheDir(dir), filepath.Join(dir, "thumbs"); got != want {
		t.Errorf("ThumbCacheDir = %q, want %q", got, want)
	}
}

// A legacy config (only the flat nhentai_* fields) must resolve to a single enabled
// nhentai source and select it as active — so an existing install keeps auto-tagging with
// no config rewrite.
func TestResolveSourcesLegacySynth(t *testing.T) {
	cfg := Config{NhentaiAPIKey: "secret", NhentaiUserAgent: "UA/1.0"}
	srcs := cfg.ResolveSources()
	if len(srcs) != 1 || srcs[0].Provider != "nhentai" || srcs[0].APIKey != "secret" || !srcs[0].Enabled {
		t.Fatalf("legacy synth = %+v, want one enabled nhentai source", srcs)
	}
	active, ok := cfg.ActiveSourceConfig()
	if !ok || active.Provider != "nhentai" {
		t.Errorf("active = %+v (ok=%v), want nhentai", active, ok)
	}

	// No key at all -> no sources, no active.
	if _, ok := (Config{}).ActiveSourceConfig(); ok {
		t.Error("empty config should have no active source")
	}
}

// An explicit Sources list wins over the legacy fields, and ActiveSource selects by slug.
func TestActiveSourceConfigPrefersExplicit(t *testing.T) {
	cfg := Config{
		NhentaiAPIKey: "legacy", // must be ignored once Sources is set
		Sources: []SourceConfig{
			{Provider: "nhentai", APIKey: "k", Enabled: true},
			{Provider: "mangadex", Enabled: true},
		},
		ActiveSource: "mangadex",
	}
	if srcs := cfg.ResolveSources(); len(srcs) != 2 {
		t.Fatalf("explicit Sources should win, got %+v", srcs)
	}
	active, ok := cfg.ActiveSourceConfig()
	if !ok || active.Provider != "mangadex" {
		t.Errorf("active = %+v, want mangadex (by ActiveSource slug)", active)
	}
}
