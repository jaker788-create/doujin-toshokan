package scanner

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildCBZ writes a .cbz at path containing the named entries (image extensions
// matter; contents do not for detection).
func buildCBZ(t *testing.T, path string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

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

func TestScanRootDetectsBothLayouts(t *testing.T) {
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
	// Organized author/title folder.
	for i := 1; i <= 3; i++ {
		mk("Aoi", "Blue Sky", fmt.Sprintf("%d.png", i))
	}
	// Raw title sitting directly in the root, no author folder, with images only.
	raw := "[Eight PM] Raw Title [English]"
	for i := 1; i <= 5; i++ {
		mk(raw, fmt.Sprintf("%d.png", i))
	}

	byTitle := map[string]DetectedFolder{}
	for _, d := range ScanRoot(root) {
		byTitle[d.Title] = d
	}

	org, ok := byTitle["Blue Sky"]
	if !ok {
		t.Fatal("organized title 'Blue Sky' not detected")
	}
	if org.Author != "Aoi" {
		t.Errorf("organized author = %q, want Aoi", org.Author)
	}

	rt, ok := byTitle[raw]
	if !ok {
		t.Fatalf("raw root title %q not detected", raw)
	}
	if rt.Author != "" {
		t.Errorf("raw title author = %q, want empty (no author folder)", rt.Author)
	}
	if rt.PageCount != 5 {
		t.Errorf("raw title page_count = %d, want 5", rt.PageCount)
	}
	if rt.FolderPath != filepath.Join(root, raw) {
		t.Errorf("raw title folder_path = %q, want %q", rt.FolderPath, filepath.Join(root, raw))
	}
}

func TestTitleNameFor(t *testing.T) {
	cases := map[string]string{
		filepath.Join("a", "b", "Blue Sky"):       "Blue Sky",   // folder: base name
		filepath.Join("a", "b", "[Artist] T.cbz"): "[Artist] T", // archive: ext stripped
		filepath.Join("a", "b", "Book.ZIP"):       "Book",       // case-insensitive
	}
	for in, want := range cases {
		if got := TitleNameFor(in); got != want {
			t.Errorf("TitleNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPagesForArchive(t *testing.T) {
	cbz := filepath.Join(t.TempDir(), "book.cbz")
	buildCBZ(t, cbz, "2.png", "10.png", "1.png", "notes.txt")
	pages := PagesFor(cbz)
	if len(pages) != 3 {
		t.Fatalf("PagesFor returned %d pages, want 3 (.txt excluded): %v", len(pages), pages)
	}
	// Natural order: 1 before 2 before 10, each a "<cbz>/<entry>" virtual path.
	wantSuffix := []string{"/1.png", "/2.png", "/10.png"}
	for i, suf := range wantSuffix {
		if filepath.Base(pages[i]) != suf[1:] {
			t.Errorf("page[%d] = %q, want entry %q", i, pages[i], suf[1:])
		}
		if pages[i] != cbz+suf {
			t.Errorf("page[%d] = %q, want %q", i, pages[i], cbz+suf)
		}
	}
}

func TestScanRootDetectsArchives(t *testing.T) {
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
	// Organized folder title.
	for i := 1; i <= 3; i++ {
		mk("Aoi", "Blue Sky", fmt.Sprintf("%d.png", i))
	}
	// Archive inside an author folder (author has only a .cbz, no subdirs).
	buildCBZ(t, filepath.Join(root, "Mori", "Forest.cbz"), "1.png", "2.png")
	// Archive dropped straight in the root (no author folder).
	buildCBZ(t, filepath.Join(root, "Raw Tank.cbz"), "01.png", "02.png", "03.png")

	byTitle := map[string]DetectedFolder{}
	for _, d := range ScanRoot(root) {
		byTitle[d.Title] = d
	}

	if d, ok := byTitle["Blue Sky"]; !ok || d.Author != "Aoi" {
		t.Errorf("folder title Blue Sky/Aoi not detected: %+v (%v)", d, ok)
	}

	forest, ok := byTitle["Forest"]
	if !ok {
		t.Fatal("archive title 'Forest' inside author folder not detected")
	}
	if forest.Author != "Mori" {
		t.Errorf("archive author = %q, want Mori", forest.Author)
	}
	if forest.PageCount != 2 {
		t.Errorf("archive page_count = %d, want 2", forest.PageCount)
	}
	if forest.FolderPath != filepath.Join(root, "Mori", "Forest.cbz") {
		t.Errorf("archive folder_path = %q", forest.FolderPath)
	}
	if forest.CoverRelPath == nil || *forest.CoverRelPath != "1.png" {
		t.Errorf("archive cover = %v, want 1.png", forest.CoverRelPath)
	}

	raw, ok := byTitle["Raw Tank"]
	if !ok {
		t.Fatal("raw root archive 'Raw Tank' not detected")
	}
	if raw.Author != "" {
		t.Errorf("raw archive author = %q, want empty", raw.Author)
	}
	if raw.PageCount != 3 {
		t.Errorf("raw archive page_count = %d, want 3", raw.PageCount)
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
