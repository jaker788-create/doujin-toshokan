// Package thumbs generates and disk-caches JPEG thumbnails on demand, ported from
// doujin/thumbnails.py. Decoding uses pure-Go image libraries; any source it
// cannot read (corrupt, or an unsupported format such as AVIF) degrades to a
// placeholder rather than failing — the webview still renders the full image.
package thumbs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/color"
	"os"
	"path/filepath"

	"github.com/disintegration/imaging"

	// Register additional decoders with image.Decode (imaging already covers
	// jpeg/png/gif/bmp/tiff); webp is the common manga format it lacks.
	_ "golang.org/x/image/webp"
)

func cacheKey(src string, width int) (string, error) {
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	raw := fmt.Sprintf("%s|%d|%d|%d", src, st.ModTime().UnixNano(), st.Size(), width)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), nil
}

func placeholder(cacheDir string, width int) (string, error) {
	out := filepath.Join(cacheDir, fmt.Sprintf("_placeholder_%d.jpg", width))
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	h := int(float64(width) * 1.4)
	if h < 1 {
		h = 1
	}
	img := imaging.New(width, h, color.NRGBA{R: 40, G: 40, B: 40, A: 255})
	if err := imaging.Save(img, out, imaging.JPEGQuality(70)); err != nil {
		return "", err
	}
	return out, nil
}

// GetThumbnail returns a path to a cached JPEG thumbnail of src at width px,
// generating it on first request. Images are never upscaled: a source narrower
// than width is stored at its original width. Unreadable/corrupt/unsupported
// sources return a placeholder of the requested width instead of an error.
func GetThumbnail(src string, width int, cacheDir string) (string, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	key, err := cacheKey(src, width)
	if err != nil {
		return placeholder(cacheDir, width)
	}
	out := filepath.Join(cacheDir, key+".jpg")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}

	img, err := imaging.Open(src)
	if err != nil {
		return placeholder(cacheDir, width)
	}
	if img.Bounds().Dx() > width {
		img = imaging.Resize(img, width, 0, imaging.Lanczos) // height 0 preserves aspect
	}

	// Write to a temp file then atomically rename, so an interrupted write can
	// never leave a corrupt thumbnail that later reads as a cache hit.
	tmp, err := os.CreateTemp(cacheDir, "*.jpg")
	if err != nil {
		return placeholder(cacheDir, width)
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	if err := imaging.Save(img, tmpName, imaging.JPEGQuality(85)); err != nil {
		_ = os.Remove(tmpName)
		return placeholder(cacheDir, width)
	}
	if err := os.Rename(tmpName, out); err != nil {
		_ = os.Remove(tmpName)
		return placeholder(cacheDir, width)
	}
	return out, nil
}
