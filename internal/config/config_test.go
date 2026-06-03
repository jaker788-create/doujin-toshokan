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
