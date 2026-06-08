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
)

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
	".gif": true, ".bmp": true, ".avif": true,
}

// DetectedFolder describes a title folder found on disk. JSON tags match the
// shape the Python build exposed and the frontend consumes.
type DetectedFolder struct {
	FolderPath   string  `json:"folder_path"`
	Author       string  `json:"author"`
	Title        string  `json:"title"`
	PageCount    int     `json:"page_count"`
	CoverRelPath *string `json:"cover_rel_path"`
}

// natTok is one token of a natural-sort key: either a run of digits or a run of
// (lowercased) non-digits, mirroring Python's re.split(r"(\d+)", s) + int/str map.
type natTok struct {
	isNum  bool
	digits string // raw digit run when isNum
	text   string // lowercased text run otherwise
}

func tokenize(s string) []natTok {
	var toks []natTok
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && !(s[i] >= '0' && s[i] <= '9') {
			i++
		}
		toks = append(toks, natTok{text: strings.ToLower(s[start:i])})
		if i >= len(s) {
			break
		}
		start = i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		toks = append(toks, natTok{isNum: true, digits: s[start:i]})
	}
	return toks
}

// compareDigits compares two digit runs by integer value, ignoring leading zeros,
// without overflow (handles arbitrarily long numbers, like Python's int()).
func compareDigits(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// naturalLess orders strings so embedded numbers sort by value. A shorter key that
// is a prefix of a longer one sorts first (matching Python list comparison).
func naturalLess(a, b string) bool {
	ta, tb := tokenize(a), tokenize(b)
	n := len(ta)
	if len(tb) < n {
		n = len(tb)
	}
	for i := 0; i < n; i++ {
		x, y := ta[i], tb[i]
		switch {
		case x.isNum && y.isNum:
			if c := compareDigits(x.digits, y.digits); c != 0 {
				return c < 0
			}
		case !x.isNum && !y.isNum:
			if x.text != y.text {
				return x.text < y.text
			}
		default:
			// Token kinds differ at this position (should not happen for aligned
			// splits); define a stable order so sorting never panics.
			return x.isNum
		}
	}
	return len(ta) < len(tb)
}

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
		if imageExts[strings.ToLower(filepath.Ext(e.Name()))] {
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

// hasSubdirs reports whether folder contains at least one child directory. It is the
// signal ScanRoot uses to tell an author folder (has title subfolders) from a raw
// title folder (only image files). An unreadable folder is treated as having none.
func hasSubdirs(folder string) bool {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			return true
		}
	}
	return false
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

// ScanRoot detects importable title folders under root, supporting two layouts in
// the same root at once:
//
//   - Organized: root/<author>/<title>/images — a top-level folder that contains
//     subfolders is an author; each subfolder is a title (its author is the folder).
//   - Raw: root/<title>/images — a top-level folder with no subfolders is itself a
//     title dropped straight in the root. It has no author folder, so its Author is
//     left empty for the importer to derive from the (decorated) folder name.
//
// The two are told apart by whether the top-level folder has subfolders, matching
// how the library is actually organized on disk. Unreadable directories are skipped
// rather than aborting the whole scan.
func ScanRoot(root string) []DetectedFolder {
	results := []DetectedFolder{}
	if _, err := os.Stat(root); err != nil {
		return results
	}
	for _, entry := range sortedSubdirs(root) {
		if hasSubdirs(entry) {
			// Organized author folder: each subfolder is a title.
			for _, titleDir := range sortedSubdirs(entry) {
				if d := DetectFolder(titleDir); d != nil {
					results = append(results, *d)
				}
			}
			continue
		}
		// Raw title sitting directly in the root: no author folder above it.
		if d := detect(entry, ""); d != nil {
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
