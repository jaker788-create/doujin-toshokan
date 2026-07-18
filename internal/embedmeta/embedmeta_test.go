package embedmeta

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"doujin/internal/tag"
)

// writeCBZ writes a .cbz at path containing the given entries (name -> contents).
func writeCBZ(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// a realistic info.json as the userscript emits it.
const sampleInfo = `{
  "source": "akuma",
  "slug": "some-slug",
  "url": "https://akuma.moe/g/some-slug",
  "nhentai_id": "271687",
  "title": { "display": "Foo | Bar", "japanese": null },
  "tags": {
    "artist": ["Rustle"],
    "group": ["Eight PM"],
    "parody": ["original"],
    "character": ["Alice", "Bob"],
    "tag": ["Big Breasts", "sole female"],
    "language": ["english"],
    "category": ["doujinshi"]
  },
  "tags_raw": ["female:big breasts", "male:sole male"],
  "pages": 20,
  "uploaded_at": "2026-01-01T00:00:00Z",
  "downloaded_at": "2026-07-16T00:00:00Z"
}`

func TestTypedTagsFlattening(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gallery.cbz")
	writeCBZ(t, path, map[string]string{
		"info.json": sampleInfo,
		"001.jpg":   "img",
	})

	got := TypedTagsFor(path)
	want := []tag.Typed{
		{Name: "rustle", Type: tag.Artist},
		{Name: "eight pm", Type: tag.Group},
		{Name: "original", Type: tag.Parody},
		{Name: "alice", Type: tag.Character},
		{Name: "bob", Type: tag.Character},
		{Name: "doujinshi", Type: tag.Category},
		{Name: "big breasts", Type: tag.Tag},
		{Name: "sole female", Type: tag.Tag},
		{Name: "english", Type: tag.Language},
	}
	assertTags(t, got, tag.Sort(want))
}

func TestReadMissingInfoJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.cbz")
	writeCBZ(t, path, map[string]string{"001.jpg": "img"})

	info, err := Read(path)
	if err != nil {
		t.Fatalf("Read: unexpected error %v", err)
	}
	if info != nil {
		t.Fatalf("Read: want nil Info for archive without info.json, got %+v", info)
	}
	if tags := TypedTagsFor(path); tags != nil {
		t.Fatalf("TypedTagsFor: want nil, got %v", tags)
	}
}

func TestMalformedInfoJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.cbz")
	writeCBZ(t, path, map[string]string{"info.json": "{not json"})

	if _, err := Read(path); err == nil {
		t.Fatal("Read: want error for malformed info.json, got nil")
	}
	// TypedTagsFor swallows the parse error so a bad embed never blocks import.
	if tags := TypedTagsFor(path); tags != nil {
		t.Fatalf("TypedTagsFor: want nil on malformed embed, got %v", tags)
	}
}

func TestNonArchivePathYieldsNoTags(t *testing.T) {
	if tags := TypedTagsFor(t.TempDir()); tags != nil {
		t.Fatalf("TypedTagsFor(dir): want nil, got %v", tags)
	}
	if tags := TypedTagsFor(`C:\lib\author\title`); tags != nil {
		t.Fatalf("TypedTagsFor(folder): want nil, got %v", tags)
	}
}

func TestTypedTagsDedupAndBlankDrop(t *testing.T) {
	info := &Info{Tags: map[string][]string{
		"character": {"Alice", "alice", "  ", "Bob"},
		"circle":    {"Eight PM"}, // synonym subject → Group via tag.Normalize
	}}
	got := info.TypedTags()
	want := tag.Sort([]tag.Typed{
		{Name: "alice", Type: tag.Character},
		{Name: "bob", Type: tag.Character},
		{Name: "eight pm", Type: tag.Group},
	})
	assertTags(t, got, want)
}

func assertTags(t *testing.T, got, want []tag.Typed) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("tag count = %d, want %d\n got: %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tag[%d] = %+v, want %+v\n got: %+v\nwant: %+v", i, got[i], want[i], got, want)
		}
	}
}
