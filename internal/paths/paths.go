// Package paths holds the filesystem security guard. It is the single chokepoint
// that decides whether an OS path handed to the /image and /thumb routes is
// allowed to be read, mirroring doujin/paths.py from the Python build.
package paths

import (
	"path/filepath"
	"strings"
)

// IsWithinRoots reports whether path resolves to a location that is one of roots
// or a descendant of one. It canonicalizes both sides first (following symlinks
// when the path exists, like Python's Path.resolve()), so "<root>/../secret" is
// rejected and traversal cannot escape a configured library root.
//
// NOTE: on Windows the comparison is case-sensitive. In practice the app builds
// these paths from the exact folder_path stored in the DB, and EvalSymlinks
// canonicalizes case for existing paths, so casing matches. If a real
// mixed-case path ever slips through, add case-folding here (and a test).
func IsWithinRoots(path string, roots []string) bool {
	target, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	target = resolve(target)
	sep := string(filepath.Separator)
	for _, root := range roots {
		rp, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rp = resolve(rp)
		rel, err := filepath.Rel(rp, target)
		if err != nil {
			// Different volumes (e.g. C: vs D:) -> not relative -> not within.
			continue
		}
		// Within iff the relative path does not climb out with "..".
		if rel != ".." && !strings.HasPrefix(rel, ".."+sep) {
			return true
		}
	}
	return false
}

// resolve canonicalizes p like Python's Path.resolve(): follow symlinks when the
// path exists, otherwise fall back to the cleaned absolute path (resolve does not
// require the path to exist).
func resolve(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}
