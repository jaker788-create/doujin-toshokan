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
	"sort"
	"strings"
)

// Parsed is the decomposed form of a folder name. Title is already cleaned for
// display; use TitleVariants for matching and Tags for the implied tags.
type Parsed struct {
	Event      string   // leading (…) convention, e.g. "C97" — informational, not a tag
	Circle     string   // first […] group, e.g. "Eight PM" or "Circle (Artist)"
	Title      string   // cleaned title (separators normalized to " / ")
	Parodies   []string // (…) groups after the title, e.g. "naruto"
	Language   string   // from a […] language tag, e.g. "english"
	MiscTags   []string // known […] tags, e.g. "digital", "decensored"
	Translator string   // {…} credit — discarded from tags
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

// ParseName decomposes a folder name into its parts.
func ParseName(name string) Parsed {
	segs := tokenize(name)
	var p Parsed

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

// normalizeSeparators turns the various dual-language separators (" _ ", "|") into
// " / " and collapses whitespace, so a "romaji _ english" title reads cleanly.
func normalizeSeparators(s string) string {
	s = strings.ReplaceAll(s, " _ ", " / ")
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

// Author returns the best single author name from the circle group: the inner
// artist of a "Group (Artist)" form (the person, usually the better library author),
// else the whole circle. Empty when there is no circle group at all.
func (p Parsed) Author() string {
	if p.Circle == "" {
		return ""
	}
	if op := strings.IndexByte(p.Circle, '('); op >= 0 {
		if cp := strings.IndexByte(p.Circle, ')'); cp > op {
			if inner := strings.TrimSpace(p.Circle[op+1 : cp]); inner != "" {
				return inner
			}
		}
	}
	return strings.TrimSpace(p.Circle)
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
