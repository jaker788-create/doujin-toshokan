// Package archive reads comic archives (.cbz/.zip) as a page source, streaming
// images straight out of the zip without ever unpacking to disk — the archive
// counterpart of the folder scanner's loose-image listing, preserving the
// project's index-in-place invariant (library files are never moved or modified).
//
// A .cbz is just a renamed .zip, so both extensions are accepted and read with the
// standard library's archive/zip (no cgo, no third-party dependency). The rest of
// the app addresses an archive page by a "virtual path" that joins the archive's
// real filesystem path to the entry name inside it:
//
//	C:\Anime\author\foo.cbz/page-001.jpg
//	└──────── real .cbz file ────────┘ └── entry inside the zip ──┘
//
// SplitArchivePath reverses that join. The /image and /thumb asset handlers detect
// the boundary and serve the entry; everywhere else a page is still an opaque
// string, so the frontend needs no notion of archives.
package archive

import (
	"archive/zip"
	"io"
	"io/fs"
	"sort"
	"strings"

	"doujin/internal/imagefile"
)

// archiveExts are the extensions treated as comic archives. Both are read as zip.
var archiveExts = map[string]bool{".cbz": true, ".zip": true}

// IsArchive reports whether name has a recognized archive extension (.cbz/.zip),
// case-insensitively.
func IsArchive(name string) bool {
	return archiveExts[strings.ToLower(ext(name))]
}

// ext returns the lowercased extension of name including the dot, or "" if none.
// (We avoid path/filepath here so a virtual path's mixed separators don't matter.)
func ext(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

// SplitArchivePath splits a virtual path "<archive>.cbz/<entry>" into the archive's
// real filesystem path and the entry name inside it. It matches the first .cbz/.zip
// segment (case-insensitive) that is immediately followed by a path separator
// ('/' or '\\', since a stored folder_path on Windows uses backslashes). The
// returned entry has its separators normalized to '/', the zip convention. For a
// plain (non-archive) path it returns ok=false, leaving the caller on its existing
// loose-file code path.
func SplitArchivePath(p string) (archivePath, entry string, ok bool) {
	lower := strings.ToLower(p)
	for e := range archiveExts {
		for from := 0; ; {
			i := strings.Index(lower[from:], e)
			if i < 0 {
				break
			}
			pos := from + i + len(e) // index just past the extension
			if pos < len(p) && (p[pos] == '/' || p[pos] == '\\') {
				return p[:pos], strings.ReplaceAll(p[pos+1:], "\\", "/"), true
			}
			from += i + len(e)
		}
	}
	return "", "", false
}

// ListPages opens archivePath and returns the virtual paths of its image entries
// ("<archivePath>/<entry>"), natural-sorted by entry name so numbered pages order
// by value (2 before 10) — the same ordering scanner.ListPages gives a folder.
// Entries nested in internal subfolders are included. Directory entries and
// non-image files are skipped.
func ListPages(archivePath string) ([]string, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	names := []string{}
	for _, f := range r.File {
		if f.FileInfo().IsDir() || !imagefile.IsImage(f.Name) {
			continue
		}
		names = append(names, f.Name)
	}
	sort.Slice(names, func(i, j int) bool { return imagefile.NaturalLess(names[i], names[j]) })
	pages := make([]string, len(names))
	for i, n := range names {
		pages[i] = archivePath + "/" + n
	}
	return pages, nil
}

// OpenEntry opens a single entry inside archivePath for reading. The returned
// ReadCloser owns the underlying zip reader and closes it on Close, so callers
// just defer rc.Close(). Lookup is by exact entry name; a zip entry is not a
// filesystem path, so this cannot traverse out of the archive. A missing entry
// returns fs.ErrNotExist.
func OpenEntry(archivePath, entry string) (io.ReadCloser, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	for _, f := range r.File {
		if f.Name == entry {
			rc, err := f.Open()
			if err != nil {
				_ = r.Close()
				return nil, err
			}
			return &entryReader{rc: rc, zip: r}, nil
		}
	}
	_ = r.Close()
	return nil, fs.ErrNotExist
}

// entryReader couples an open entry to its parent zip reader so closing it frees
// both — the entry stream alone does not release the archive handle.
type entryReader struct {
	rc  io.ReadCloser
	zip *zip.ReadCloser
}

func (e *entryReader) Read(p []byte) (int, error) { return e.rc.Read(p) }

func (e *entryReader) Close() error {
	err := e.rc.Close()
	if zerr := e.zip.Close(); err == nil {
		err = zerr
	}
	return err
}
