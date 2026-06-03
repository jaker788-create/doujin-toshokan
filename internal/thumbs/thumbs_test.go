package thumbs

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/disintegration/imaging"
)

// writePNG writes a w×h PNG, matching the 60×90 fixture pages from conftest.py.
func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func widthOf(t *testing.T, path string) int {
	t.Helper()
	img, err := imaging.Open(path)
	if err != nil {
		t.Fatalf("open %q: %v", path, err)
	}
	return img.Bounds().Dx()
}

func TestThumbnailGeneratedAndResized(t *testing.T) {
	src := filepath.Join(t.TempDir(), "1.png")
	writePNG(t, src, 60, 90)
	cache := filepath.Join(t.TempDir(), "thumbs")
	out, err := GetThumbnail(src, 30, cache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("thumbnail not created: %v", err)
	}
	if w := widthOf(t, out); w != 30 {
		t.Errorf("thumbnail width = %d, want 30", w)
	}
}

func TestThumbnailCacheHit(t *testing.T) {
	src := filepath.Join(t.TempDir(), "1.png")
	writePNG(t, src, 60, 90)
	cache := filepath.Join(t.TempDir(), "thumbs")
	first, err := GetThumbnail(src, 30, cache)
	if err != nil {
		t.Fatal(err)
	}
	fi1, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GetThumbnail(src, 30, cache)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("cache miss: %q != %q", second, first)
	}
	fi2, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Error("thumbnail was regenerated on a cache hit")
	}
}

func TestNoUpscaleWhenSourceNarrower(t *testing.T) {
	src := filepath.Join(t.TempDir(), "1.png")
	writePNG(t, src, 60, 90) // 60px wide
	cache := filepath.Join(t.TempDir(), "thumbs")
	out, err := GetThumbnail(src, 200, cache) // request larger
	if err != nil {
		t.Fatal(err)
	}
	if w := widthOf(t, out); w != 60 {
		t.Errorf("width = %d, want 60 (no upscaling)", w)
	}
}

func TestCorruptImageReturnsPlaceholder(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "broken.png")
	if err := os.WriteFile(bad, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "thumbs")
	out, err := GetThumbnail(bad, 30, cache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("placeholder not created: %v", err)
	}
	if w := widthOf(t, out); w != 30 { // placeholder is a valid image of the requested width
		t.Errorf("placeholder width = %d, want 30", w)
	}
}
