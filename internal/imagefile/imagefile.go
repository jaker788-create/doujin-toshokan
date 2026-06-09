// Package imagefile holds two things shared by the two page sources — the folder
// scanner (internal/scanner) and the archive reader (internal/archive): the set of
// recognized image extensions, and the natural-sort ordering ("2" before "10") the
// page reader depends on. It lives in its own leaf package (importing nothing
// internal) so both sources can share these without an import cycle, and so the
// recognized-format list and the page ordering can never drift apart between a
// title stored as a folder of files and one stored as a .cbz/.zip archive.
package imagefile

import (
	"path/filepath"
	"strings"
)

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
	".gif": true, ".bmp": true, ".avif": true,
}

// IsImage reports whether name has a recognized image extension (case-insensitive).
func IsImage(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
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

// NaturalLess orders strings so embedded numbers sort by value. A shorter key that
// is a prefix of a longer one sorts first (matching Python list comparison).
func NaturalLess(a, b string) bool {
	ta, tb := tokenize(a), tokenize(b)
	n := min(len(ta), len(tb))
	for i := range n {
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
