package archive

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// buildCBZ writes a .cbz at path whose entries are exactly names (each holding its
// own name as bytes, so a round-trip read is verifiable).
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
		if _, err := w.Write([]byte(n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIsArchive(t *testing.T) {
	for _, name := range []string{"foo.cbz", "FOO.CBZ", "bar.zip", "bar.ZIP"} {
		if !IsArchive(name) {
			t.Errorf("IsArchive(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"foo.cbr", "foo.rar", "foo.png", "foo", "cbz"} {
		if IsArchive(name) {
			t.Errorf("IsArchive(%q) = true, want false", name)
		}
	}
}

func TestSplitArchivePath(t *testing.T) {
	cases := []struct {
		in            string
		wantArc, wEnt string
		wantOK        bool
	}{
		// Forward slash after the extension (the join ListPages produces).
		{`/lib/a/foo.cbz/page-001.jpg`, `/lib/a/foo.cbz`, `page-001.jpg`, true},
		// Backslash, as a Windows folder_path + '/' + entry can mix separators.
		{`C:\Anime\a\foo.cbz\sub\001.jpg`, `C:\Anime\a\foo.cbz`, `sub/001.jpg`, true},
		{`C:\Anime\a\foo.cbz/sub\001.jpg`, `C:\Anime\a\foo.cbz`, `sub/001.jpg`, true},
		// Case-insensitive extension match.
		{`/lib/FOO.ZIP/p.png`, `/lib/FOO.ZIP`, `p.png`, true},
		// Plain paths: no archive boundary -> ok=false.
		{`/lib/a/title/001.jpg`, ``, ``, false},
		// A bare archive with no entry after it is not a page reference.
		{`/lib/a/foo.cbz`, ``, ``, false},
	}
	for _, c := range cases {
		arc, ent, ok := SplitArchivePath(c.in)
		if ok != c.wantOK || arc != c.wantArc || ent != c.wEnt {
			t.Errorf("SplitArchivePath(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, arc, ent, ok, c.wantArc, c.wEnt, c.wantOK)
		}
	}
}

func TestListPagesOrdersAndFilters(t *testing.T) {
	cbz := filepath.Join(t.TempDir(), "book.cbz")
	// Deliberately out of order, with a non-image and a nested image entry.
	buildCBZ(t, cbz, "10.png", "2.png", "1.png", "notes.txt", "sub/3.png")

	pages, err := ListPages(cbz)
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	for _, p := range pages {
		_, e, ok := SplitArchivePath(p)
		if !ok {
			t.Fatalf("page %q is not a valid archive virtual path", p)
		}
		entries = append(entries, e)
	}
	want := []string{"1.png", "2.png", "10.png", "sub/3.png"}
	if len(entries) != len(want) {
		t.Fatalf("entries = %v, want %v", entries, want)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Fatalf("entries = %v, want %v (natural order, .txt excluded)", entries, want)
		}
	}
}

func TestOpenEntryRoundTrip(t *testing.T) {
	cbz := filepath.Join(t.TempDir(), "book.cbz")
	buildCBZ(t, cbz, "a.png", "sub/b.png")

	rc, err := OpenEntry(cbz, "sub/b.png")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !bytes.Equal(got, []byte("sub/b.png")) {
		t.Errorf("entry bytes = %q, want %q", got, "sub/b.png")
	}

	if _, err := OpenEntry(cbz, "missing.png"); err == nil {
		t.Error("OpenEntry of a missing entry should error")
	}
}

func TestListPagesBadArchive(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "broken.cbz")
	if err := os.WriteFile(bad, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ListPages(bad); err == nil {
		t.Error("ListPages of a non-zip should error")
	}
}
