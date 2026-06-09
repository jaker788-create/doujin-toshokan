// Package thumbs generates and disk-caches JPEG thumbnails on demand, ported from
// doujin/thumbnails.py. Decoding uses pure-Go image libraries; any source it
// cannot read (corrupt, or an unsupported format such as AVIF) degrades to a
// placeholder rather than failing — the webview still renders the full image.
package thumbs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"

	"github.com/disintegration/imaging"

	"doujin/internal/archive"

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

// archiveCacheKey derives a cache key for one image entry inside an archive. It
// keys on the archive file's identity (path/modtime/size) plus the entry name and
// width, so each entry caches independently and its thumbnail invalidates whenever
// the archive changes on disk.
func archiveCacheKey(archivePath, entry string, width int) (string, error) {
	st, err := os.Stat(archivePath)
	if err != nil {
		return "", err
	}
	raw := fmt.Sprintf("%s|%s|%d|%d|%d", archivePath, entry, st.ModTime().UnixNano(), st.Size(), width)
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
	return render(img, width, cacheDir, out)
}

// GetThumbnailArchive is GetThumbnail for an image stored inside a .cbz/.zip
// archive: it decodes the entry straight from the archive (no unpacking) and
// caches the JPEG exactly as for a loose file. An unreadable/corrupt/unsupported
// entry returns a placeholder of the requested width instead of an error.
func GetThumbnailArchive(archivePath, entry string, width int, cacheDir string) (string, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	key, err := archiveCacheKey(archivePath, entry, width)
	if err != nil {
		return placeholder(cacheDir, width)
	}
	out := filepath.Join(cacheDir, key+".jpg")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}

	rc, err := archive.OpenEntry(archivePath, entry)
	if err != nil {
		return placeholder(cacheDir, width)
	}
	defer rc.Close()
	img, err := imaging.Decode(rc)
	if err != nil {
		return placeholder(cacheDir, width)
	}
	return render(img, width, cacheDir, out)
}

// render resizes img to width (never upscaling — a source narrower than width is
// kept at its original width) and writes it as a JPEG to out. It writes to a temp
// file then atomically renames, so an interrupted write can never leave a corrupt
// thumbnail that later reads as a cache hit. Any failure degrades to a placeholder.
func render(img image.Image, width int, cacheDir, out string) (string, error) {
	if img.Bounds().Dx() > width {
		img = imaging.Resize(img, width, 0, imaging.Lanczos) // height 0 preserves aspect
	}
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
