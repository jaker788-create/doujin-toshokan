package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildLibrary recreates the conftest.py fixture: Aoi/Blue Sky has 11 numeric
// pages (tests natural sort 2<10), Mori/Forest has 3, plus a stray .txt and an
// empty dir that must be ignored. Files need only the right extension here.
func buildLibrary(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "lib")
	mk := func(parts ...string) {
		full := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 11; i++ {
		mk("Aoi", "Blue Sky", fmt.Sprintf("%d.png", i))
	}
	for i := 1; i <= 3; i++ {
		mk("Mori", "Forest", fmt.Sprintf("page%d.png", i))
	}
	mk("Aoi", "Blue Sky", "notes.txt")
	if err := os.MkdirAll(filepath.Join(root, "Empty", "Nothing"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func indexOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

func TestNaturalKeyOrdersNumbers(t *testing.T) {
	root := buildLibrary(t)
	var names []string
	for _, p := range ListPages(filepath.Join(root, "Aoi", "Blue Sky")) {
		names = append(names, filepath.Base(p))
	}
	if indexOf(names, "2.png") >= indexOf(names, "10.png") {
		t.Errorf("expected 2.png before 10.png, got %v", names)
	}
	if indexOf(names, "notes.txt") != -1 {
		t.Error("stray notes.txt should be excluded")
	}
	if len(names) != 11 {
		t.Errorf("page count = %d, want 11", len(names))
	}
}

func TestNaturalLessUnit(t *testing.T) {
	cases := [][2]string{
		{"2.png", "10.png"},
		{"img2", "img10"},
		{"a", "a1"},
		{"file9", "file09a"}, // 9 == 09 numerically, then "" < "a"
		{"Chapter 2", "Chapter 11"},
	}
	for _, c := range cases {
		if !naturalLess(c[0], c[1]) {
			t.Errorf("naturalLess(%q,%q) = false, want true", c[0], c[1])
		}
		if naturalLess(c[1], c[0]) {
			t.Errorf("naturalLess(%q,%q) = true, want false", c[1], c[0])
		}
	}
}

func TestDetectFolderReadsAuthorTitle(t *testing.T) {
	root := buildLibrary(t)
	folder := filepath.Join(root, "Aoi", "Blue Sky")
	d := DetectFolder(folder)
	if d == nil {
		t.Fatal("expected a detected folder")
	}
	if d.Author != "Aoi" || d.Title != "Blue Sky" {
		t.Errorf("author/title = %q/%q, want Aoi/Blue Sky", d.Author, d.Title)
	}
	if d.PageCount != 11 {
		t.Errorf("page_count = %d, want 11", d.PageCount)
	}
	if d.CoverRelPath == nil || *d.CoverRelPath != "1.png" {
		t.Errorf("cover_rel_path = %v, want 1.png", d.CoverRelPath)
	}
	if d.FolderPath != folder {
		t.Errorf("folder_path = %q, want %q", d.FolderPath, folder)
	}
}

func TestDetectFolderNoneWhenNoImages(t *testing.T) {
	root := buildLibrary(t)
	if d := DetectFolder(filepath.Join(root, "Empty", "Nothing")); d != nil {
		t.Errorf("expected nil for image-less folder, got %+v", d)
	}
}

func TestFindUnimportedExcludesKnown(t *testing.T) {
	root := buildLibrary(t)
	titles := func(ds []DetectedFolder) map[string]bool {
		m := map[string]bool{}
		for _, d := range ds {
			m[d.Title] = true
		}
		return m
	}
	all := titles(FindUnimported([]string{root}, map[string]bool{}))
	if !all["Blue Sky"] || !all["Forest"] || len(all) != 2 {
		t.Errorf("all titles = %v, want {Blue Sky, Forest}", all)
	}
	known := map[string]bool{filepath.Join(root, "Aoi", "Blue Sky"): true}
	rest := titles(FindUnimported([]string{root}, known))
	if rest["Blue Sky"] || !rest["Forest"] || len(rest) != 1 {
		t.Errorf("remaining titles = %v, want {Forest}", rest)
	}
}
