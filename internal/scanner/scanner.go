// Package scanner walks a library on disk and detects importable title folders. It
// supports both the organized author/title/*.images layout and raw title folders
// dropped straight in a root (see ScanRoot). It is the Go port of doujin/scanner.py,
// including the natural-sort ordering ("2" before "10") that the page reader depends on.
package scanner

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"doujin/internal/archive"
	"doujin/internal/imagefile"
)

// DetectedFolder describes a title found on disk — either a folder of loose images
// or a .cbz/.zip archive (FolderPath is then the archive file). JSON tags match the
// shape the Python build exposed and the frontend consumes.
type DetectedFolder struct {
	FolderPath   string  `json:"folder_path"`
	Author       string  `json:"author"`
	Title        string  `json:"title"`
	PageCount    int     `json:"page_count"`
	CoverRelPath *string `json:"cover_rel_path"`
}

// naturalLess orders strings so embedded numbers sort by value (2 before 10). It is
// the shared imagefile ordering; kept as a package alias because folder sorting and
// the existing tests call it directly.
func naturalLess(a, b string) bool { return imagefile.NaturalLess(a, b) }

// ListPages returns the image files directly inside folder, natural-sorted by
// filename. An unreadable folder yields an empty slice rather than an error.
func ListPages(folder string) []string {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return []string{}
	}
	files := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if imagefile.IsImage(e.Name()) {
			files = append(files, filepath.Join(folder, e.Name()))
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return naturalLess(filepath.Base(files[i]), filepath.Base(files[j]))
	})
	return files
}

// detect builds a DetectedFolder for a single title folder, returning nil when it
// holds no images. The caller supplies the author (the parent dir for the organized
// layout, or "" for a raw title that has no author folder above it).
func detect(folder, author string) *DetectedFolder {
	pages := ListPages(folder)
	if len(pages) == 0 {
		return nil
	}
	cover := filepath.Base(pages[0])
	return &DetectedFolder{
		FolderPath:   folder,
		Author:       author,
		Title:        filepath.Base(folder),
		PageCount:    len(pages),
		CoverRelPath: &cover,
	}
}

// DetectFolder inspects a single title folder, returning nil when it holds no
// images. Author is the parent directory name, title the folder name, and the
// cover is the first page.
func DetectFolder(folder string) *DetectedFolder {
	return detect(folder, filepath.Base(filepath.Dir(folder)))
}

// TitleNameFor returns the basis for a title's display name: a folder's base name,
// or an archive file's base name with its .cbz/.zip extension stripped. The name
// parser (doujin.ParseName) derives the title and implied tags from this, so a
// title stored as a folder and one stored as an archive are named consistently
// (used by detectArchive here and by app.go's import + rescan paths).
func TitleNameFor(path string) string {
	base := filepath.Base(path)
	if archive.IsArchive(base) {
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return base
}

// detectArchive builds a DetectedFolder for a single .cbz/.zip file, returning nil
// when it holds no image entries. FolderPath is the archive file itself; the cover
// is the first image entry's name (so folder_path + "/" + cover_rel_path is the
// entry's virtual path, exactly as for a folder title). Author is supplied by the
// caller (the parent dir for the organized layout, or "" for one dropped raw in a
// root).
func detectArchive(archivePath, author string) *DetectedFolder {
	pages, err := archive.ListPages(archivePath)
	if err != nil || len(pages) == 0 {
		return nil
	}
	_, cover, _ := archive.SplitArchivePath(pages[0])
	return &DetectedFolder{
		FolderPath:   archivePath,
		Author:       author,
		Title:        TitleNameFor(archivePath),
		PageCount:    len(pages),
		CoverRelPath: &cover,
	}
}

// PagesFor lists a title's page paths regardless of how it is stored: the image
// entries inside a .cbz/.zip (as virtual paths), or the loose image files in a
// folder. It is the single page-source chokepoint app.go reads through.
func PagesFor(path string) []string {
	if archive.IsArchive(path) {
		pages, err := archive.ListPages(path)
		if err != nil {
			return []string{}
		}
		return pages
	}
	return ListPages(path)
}

func sortedSubdirs(folder string) []string {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(folder, e.Name()))
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		return naturalLess(filepath.Base(dirs[i]), filepath.Base(dirs[j]))
	})
	return dirs
}

// sortedArchiveFiles returns the .cbz/.zip files directly inside folder, natural-
// sorted by name. It is the archive analogue of sortedSubdirs: each such file is a
// title in its own right. An unreadable folder yields none.
func sortedArchiveFiles(folder string) []string {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && archive.IsArchive(e.Name()) {
			files = append(files, filepath.Join(folder, e.Name()))
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return naturalLess(filepath.Base(files[i]), filepath.Base(files[j]))
	})
	return files
}

// ScanRoot detects importable titles under root, supporting these layouts in the
// same root at once. A title can be a folder of images or a .cbz/.zip archive:
//
//   - Organized: root/<author>/<title>/images or root/<author>/<title>.cbz — a
//     top-level folder that contains subfolders OR archive files is an author; each
//     subfolder and each archive in it is a title (its author is the folder).
//   - Raw: root/<title>/images or root/<title>.cbz — a top-level folder with only
//     loose images, or an archive dropped straight in the root, is itself a title
//     with no author folder above it. Its Author is left empty for the importer to
//     derive from the (decorated) name.
//
// A folder is treated as an author exactly when it holds subfolders or archives;
// otherwise its loose images make it a raw title. Unreadable directories are
// skipped rather than aborting the whole scan.
func ScanRoot(root string) []DetectedFolder {
	results := []DetectedFolder{}
	if _, err := os.Stat(root); err != nil {
		return results
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return results
	}
	sort.Slice(entries, func(i, j int) bool { return naturalLess(entries[i].Name(), entries[j].Name()) })
	for _, entry := range entries {
		full := filepath.Join(root, entry.Name())
		if !entry.IsDir() {
			// A bare archive dropped directly in the root is a raw title.
			if archive.IsArchive(entry.Name()) {
				if d := detectArchive(full, ""); d != nil {
					results = append(results, *d)
				}
			}
			continue
		}
		subdirs := sortedSubdirs(full)
		archives := sortedArchiveFiles(full)
		if len(subdirs) > 0 || len(archives) > 0 {
			// Organized author folder: its subfolders and archives are its titles.
			for _, titleDir := range subdirs {
				if d := DetectFolder(titleDir); d != nil {
					results = append(results, *d)
				}
			}
			for _, arc := range archives {
				if d := detectArchive(arc, entry.Name()); d != nil {
					results = append(results, *d)
				}
			}
			continue
		}
		// Raw title sitting directly in the root: no author folder above it.
		if d := detect(full, ""); d != nil {
			results = append(results, *d)
		}
	}
	return results
}

// FindUnimported scans all roots and returns detected folders whose paths are not
// already in known.
func FindUnimported(roots []string, known map[string]bool) []DetectedFolder {
	out := []DetectedFolder{}
	for _, root := range roots {
		for _, d := range ScanRoot(root) {
			if !known[d.FolderPath] {
				out = append(out, d)
			}
		}
	}
	return out
}
