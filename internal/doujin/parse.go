// Package doujin parses the conventional doujinshi folder-naming scheme into its
// parts. The convention, used by nhentai/e-hentai and most rippers, is roughly:
//
//	(Event) [Circle (Artist)] Title (Parody) [Language] [Misc] {Translator}
//
// The package extracts the circle/artist (a strong search anchor), a cleaned
// display title, the dual-language title variants (titles are often
// "romaji / english"), and the tags implied by the decorations (language, parody,
// and a small known set of misc tags like "digital"). The translator credit in
// {braces} is discarded. It is pure and dependency-free so it is fully testable.
package doujin

import (
	"regexp"
	"sort"
	"strings"
)

// Parsed is the decomposed form of a folder name. Title is already cleaned for
// display; use TitleVariants for matching and Tags for the implied tags.
type Parsed struct {
	Event        string   // leading (…) convention, e.g. "C97" — informational, not a tag
	Circle       string   // first […] group, e.g. "Eight PM" or "Circle (Artist)"
	ExtraArtists []string // additional leading […] groups after the circle (collab works)
	Title        string   // cleaned title (separators normalized to " / ")
	Parodies     []string // (…) groups after the title, e.g. "naruto"
	Language     string   // from a […] language tag, e.g. "english"
	MiscTags     []string // known […] tags, e.g. "digital", "decensored"
	Translator   string   // {…} credit — discarded from tags
	SourceSlug   string   // provider slug from a leading "<slug>-<ref>" prefix ("nhentai"); "" if none
	SourceRef    string   // provider's own gallery ref from that prefix (nhentai's id, mangadex's UUID); "" if none
}

// languages recognized in a […] group and treated as the Language tag.
var languages = map[string]bool{
	"english": true, "japanese": true, "chinese": true, "korean": true,
	"spanish": true, "french": true, "german": true, "russian": true,
	"italian": true, "portuguese": true, "vietnamese": true, "thai": true,
	"indonesian": true, "translated": true,
}

// miscTags is the small allow-list of […] groups that are real content tags
// (everything else after the title in brackets is ignored as noise).
var miscTags = map[string]bool{
	"digital": true, "decensored": true, "uncensored": true, "censored": true,
	"colorized": true, "color": true, "fullcolor": true,
}

type segKind int

const (
	segText segKind = iota
	segParen
	segSquare
	segBrace
)

type segment struct {
	kind segKind
	text string
}

func openKind(r rune) (segKind, rune, bool) {
	switch r {
	case '(':
		return segParen, ')', true
	case '[':
		return segSquare, ']', true
	case '{':
		return segBrace, '}', true
	}
	return segText, 0, false
}

// tokenize splits a name into ordered text / (paren) / [square] / {brace} segments.
// Same-type nesting is balanced; other bracket types inside a group are kept as
// literal text (so "[Circle (Artist)]" yields one square segment "Circle (Artist)").
func tokenize(s string) []segment {
	var segs []segment
	var buf strings.Builder
	flush := func() {
		if t := strings.TrimSpace(buf.String()); t != "" {
			segs = append(segs, segment{segText, t})
		}
		buf.Reset()
	}
	runes := []rune(s)
	for i := 0; i < len(runes); {
		r := runes[i]
		kind, closer, isOpen := openKind(r)
		if !isOpen {
			buf.WriteRune(r)
			i++
			continue
		}
		flush()
		depth, j := 1, i+1
		var inner strings.Builder
		for j < len(runes) && depth > 0 {
			c := runes[j]
			switch c {
			case r:
				depth++
				inner.WriteRune(c)
			case closer:
				depth--
				if depth > 0 {
					inner.WriteRune(c)
				}
			default:
				inner.WriteRune(c)
			}
			j++
		}
		segs = append(segs, segment{kind, strings.TrimSpace(inner.String())})
		i = j
	}
	flush()
	return segs
}

// sourceDef declares one provider's folder-prefix. leadingRef returns the ref token at
// the start of the post-separator remainder, or "" if it is not that provider's id shape.
// The slug MUST equal the provider package's Slug const (nhentai.Slug, mangadex.Slug) — a
// leaf package can't import them, but those slugs are frozen (they are persisted in
// manga.source_slug). To support a new source's folder-id shortcut, add a row here.
type sourceDef struct {
	slug       string
	leadingRef func(string) string
}

var sourceDefs = []sourceDef{
	{"nhentai", leadingDigits},   // numeric gallery id
	{"mangadex", leadingUUID},    // UUID
	{"hitomi", leadingDigits},    // numeric gallery id (the trailing number in a gallery URL)
	{"ehentai", leadingGidToken}, // "<gid>-<token>" pair — a slash is illegal in a filename
}

// leadingDigits returns the run of ASCII digits at the start of s (nhentai/hitomi ids).
func leadingDigits(s string) string {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return s[:n]
}

// uuidRe matches a canonical 8-4-4-4-12 hex UUID anchored at the start (mangadex ids).
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// leadingUUID returns a canonical UUID at the start of s, or "".
func leadingUUID(s string) string { return uuidRe.FindString(s) }

// gidTokenRe matches e-hentai's "<gid>-<token>" pair at the start of s, capturing just the
// pair. E-Hentai identifies a gallery by a number plus a 10-hex-character token (the token
// is a capability — the right gid with the wrong token is refused), and it is written with
// a dash because the canonical "gid/token" form cannot appear in a filename. Requiring the
// hex run to END at 10 is what stops a title that happens to open with hex text from being
// swallowed into the ref.
var gidTokenRe = regexp.MustCompile(`^([0-9]+-[0-9a-fA-F]{10})(?:[^0-9a-fA-F]|$)`)

// leadingGidToken returns an e-hentai "<gid>-<token>" pair at the start of s, or "". The
// pair is returned as written; the ehentai client canonicalizes it to "gid/token".
func leadingGidToken(s string) string {
	m := gidTokenRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// sourcePrefix peels a leading "<slug>-<ref>" decoration that rippers prepend to the
// conventional name, e.g. "nhentai-271687 - [Circle] Title …" or
// "mangadex-<uuid> - Title". It returns the recognized provider slug, its gallery ref,
// and the remaining name; ("", "", name unchanged) when no known prefix is present.
//
// Stripping matters two ways: the ref is noise in the displayed title, and — worse —
// left in place it is text before the first […], which makes ParseName treat the first
// bracket as NOT the circle and mis-read the ref as the title. The (slug, ref) pair is
// worth keeping, though: it points at the exact gallery, a far stronger auto-tag signal
// than a fuzzy artist/title search (see nhentai.go). Only a leading "<slug>-<ref>" token
// for a registered provider (case-insensitive slug, '-' or '_' joining the word, ref,
// and the name) is matched; a ref appearing mid-name is left untouched.
func sourcePrefix(name string) (slug, ref, remainder string) {
	s := strings.TrimSpace(name)
	for _, def := range sourceDefs {
		w := def.slug
		if len(s) <= len(w) || !strings.EqualFold(s[:len(w)], w) {
			continue
		}
		i := len(w)
		if s[i] != '-' && s[i] != '_' {
			continue
		}
		i++
		r := def.leadingRef(s[i:])
		if r == "" {
			continue // "<slug>-" not followed by that provider's id shape: not this pattern
		}
		i += len(r)
		// Drop the separator run (spaces / dashes / underscores) joining ref to the name.
		for i < len(s) && (s[i] == ' ' || s[i] == '-' || s[i] == '_') {
			i++
		}
		return def.slug, r, s[i:]
	}
	return "", "", name
}

// ParseName decomposes a folder name into its parts.
func ParseName(name string) Parsed {
	slug, ref, rest := sourcePrefix(name)
	segs := tokenize(rest)
	var p Parsed
	p.SourceSlug = slug
	p.SourceRef = ref

	firstSquare := -1
	for i, s := range segs {
		if s.kind == segSquare {
			firstSquare = i
			break
		}
	}

	start := 0
	if firstSquare != -1 {
		textBefore := false
		for i := 0; i < firstSquare; i++ {
			if segs[i].kind == segText {
				textBefore = true
			} else if segs[i].kind == segParen && p.Event == "" {
				p.Event = segs[i].text
			}
		}
		// The first […] is the circle only when nothing but an event precedes it.
		if !textBefore {
			p.Circle = segs[firstSquare].text
			start = firstSquare + 1
		}
	}

	// After the circle, any further consecutive leading […] groups (before the title
	// text) are additional artists/circles of a collaborative work, e.g.
	// "[A] [B] Title". A recognized language/misc-tag bracket is not an artist, so the
	// loop stops there and leaves it to normal handling. The single-circle case skips
	// this loop entirely (no regression).
	if p.Circle != "" {
		for start < len(segs) && segs[start].kind == segSquare {
			t := strings.TrimSpace(segs[start].text)
			if low := strings.ToLower(t); languages[low] || miscTags[low] {
				break
			}
			if t != "" {
				p.ExtraArtists = append(p.ExtraArtists, t)
			}
			start++
		}
	}

	titleIdx := -1
	for i := start; i < len(segs); i++ {
		if segs[i].kind == segText {
			p.Title = segs[i].text
			titleIdx = i
			break
		}
	}

	tailStart := titleIdx
	if tailStart == -1 {
		tailStart = start - 1
	}
	for i := tailStart + 1; i < len(segs); i++ {
		s := segs[i]
		switch s.kind {
		case segParen:
			if t := strings.TrimSpace(s.text); t != "" {
				p.Parodies = append(p.Parodies, t)
			}
		case segBrace:
			if p.Translator == "" {
				p.Translator = s.text
			}
		case segSquare:
			low := strings.ToLower(strings.TrimSpace(s.text))
			switch {
			case languages[low]:
				if p.Language == "" {
					p.Language = low
				}
			case miscTags[low]:
				p.MiscTags = append(p.MiscTags, low)
			}
		}
	}

	p.Title = normalizeSeparators(p.Title)
	return p
}

// DetectLanguage returns the canonical lowercase language implied by a recognized
// […] group anywhere in s (e.g. "[Artist] Title [English]" -> "english"), or "" when
// none is present. It shares the `languages` vocabulary with ParseName, so a local
// folder name and an online candidate title are read by the same rules — letting the
// matcher compare languages without a full parse or any network call.
func DetectLanguage(s string) string {
	for _, seg := range tokenize(s) {
		if seg.kind != segSquare {
			continue
		}
		if low := strings.ToLower(strings.TrimSpace(seg.text)); languages[low] {
			return low
		}
	}
	return ""
}

// normalizeSeparators turns the various dual-language separators (" _ ", "|", " - ")
// into " / " and collapses whitespace, so a "romaji <sep> english" title reads cleanly
// and TitleVariants can split it into matchable halves. The " - " form is what a
// pipe-separated title becomes once Windows strips the forbidden "|" from a filename;
// it is matched only with surrounding spaces so intra-word hyphens (Juma-kun,
// Kisho-Muri, bt-T) and hyphenated names are left intact.
func normalizeSeparators(s string) string {
	s = strings.ReplaceAll(s, " _ ", " / ")
	s = strings.ReplaceAll(s, " - ", " / ")
	s = strings.ReplaceAll(s, "|", "/")
	return strings.Join(strings.Fields(s), " ")
}

// DisplayTitle is the cleaned title for the library (falls back to the raw name if
// parsing left nothing).
func (p Parsed) DisplayTitle() string { return p.Title }

// TitleVariants returns the strings to match against online titles: the full
// cleaned title plus each language half (split on "/"). Used so a "romaji / english"
// title can match an english-only online title without the other half diluting it.
func (p Parsed) TitleVariants() []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}
	add(p.Title)
	for part := range strings.SplitSeq(p.Title, "/") {
		add(part)
	}
	return out
}

// Anchors returns circle/artist strings to search nhentai by. For a
// "Circle (Artist)" group it yields the artist first (usually the better anchor),
// then the circle. Empty when there is no circle group.
func (p Parsed) Anchors() []string {
	if p.Circle == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}
	if op := strings.IndexByte(p.Circle, '('); op >= 0 {
		if cp := strings.IndexByte(p.Circle, ')'); cp > op {
			add(p.Circle[op+1 : cp])              // inner artist
			add(strings.TrimSpace(p.Circle[:op])) // outer circle
		}
	}
	add(p.Circle)
	return out
}

// artistFromGroup returns the artist name implied by a single "[…]" group's text:
// the inner artist of a "Circle (Artist)" form (the person, usually the better
// library author), else the whole group trimmed.
func artistFromGroup(group string) string {
	if op := strings.IndexByte(group, '('); op >= 0 {
		if cp := strings.IndexByte(group, ')'); cp > op {
			if inner := strings.TrimSpace(group[op+1 : cp]); inner != "" {
				return inner
			}
		}
	}
	return strings.TrimSpace(group)
}

// Author returns the best single author name from the circle group: the inner
// artist of a "Group (Artist)" form (the person, usually the better library author),
// else the whole circle. Empty when there is no circle group at all.
func (p Parsed) Author() string {
	if p.Circle == "" {
		return ""
	}
	return artistFromGroup(p.Circle)
}

// ExtraArtistNames returns the additional collaborating artists of a multi-circle
// folder name like "[A] [B] Title" (each extra group reduced to its artist the same
// way Author() reduces the circle, so "[Circle (B)]" yields "B"). The primary author
// (Author()) is excluded and the list is de-duplicated case-insensitively. Empty for
// the common single-circle name. Names are not comma-split inside one group, so a
// "[Circle (A, B)]" form is left to Author() as today (out of scope here).
func (p Parsed) ExtraArtistNames() []string {
	if len(p.ExtraArtists) == 0 {
		return nil
	}
	primary := strings.ToLower(strings.TrimSpace(p.Author()))
	var out []string
	seen := map[string]bool{}
	for _, group := range p.ExtraArtists {
		name := artistFromGroup(group)
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || key == primary || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	return out
}

// CleanArtist strips a single fully-wrapping balanced () or [] pair from an artist /
// author name and trims surrounding space, so a library folder named "(Rustle)" or
// "[Yoku]" yields the bare nhentai artist tag "Rustle"/"Yoku" used for searching and
// matching. It strips only when the whole string is one balanced group (so a hybrid
// name like "A6 (Kisho Muri)", where the parens don't enclose the whole string, is
// left intact), peels just one layer ("((x))" -> "(x)"), leaves an unbalanced or
// unwrapped name untouched, and never cleans down to empty ("()" stays "()").
func CleanArtist(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) < 2 {
		return s
	}
	var closer rune
	switch r[0] {
	case '(':
		closer = ')'
	case '[':
		closer = ']'
	default:
		return s
	}
	depth := 0
	for i, c := range r {
		switch c {
		case r[0]:
			depth++
		case closer:
			depth--
		}
		// A wrap must close exactly at the final rune; an earlier return to depth 0
		// means the group doesn't enclose the whole string (e.g. "(a) (b)").
		if depth == 0 && i != len(r)-1 {
			return s
		}
	}
	if depth != 0 {
		return s // unbalanced
	}
	if inner := strings.TrimSpace(string(r[1 : len(r)-1])); inner != "" {
		return inner
	}
	return s
}

// Tags returns the lowercased tags implied by the name: language, misc tags, and
// parodies. De-duplicated and sorted. They are normalized again by ingest before
// storage; this just gathers them.
func (p Parsed) Tags() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if p.Language != "" {
		add(p.Language)
	}
	for _, t := range p.MiscTags {
		add(t)
	}
	for _, t := range p.Parodies {
		add(t)
	}
	sort.Strings(out)
	return out
}
