// Package tag defines the canonical tag subjects the app shares everywhere — the
// same vocabulary nhentai uses (language, artist, group, parody, character,
// category, tag) plus General ("") for untyped/manual tags. It is a dependency-free
// leaf package so every layer (the folder parser's mapping in app, ingest's write
// path, search's read path, and the nhentai type mapping) speaks one language with
// no import cycle.
package tag

import (
	"sort"
	"strings"
)

// Subjects. General is an untyped tag (manual, or pre-subjects data). The rest mirror
// nhentai's tag types. Values are the lowercase strings stored in the tags.type column.
const (
	General   = ""
	Language  = "language"
	Artist    = "artist"
	Group     = "group"
	Parody    = "parody"
	Character = "character"
	Category  = "category"
	Tag       = "tag"
)

// Typed is a tag name paired with its subject. Name is the normalized (lowercase)
// tag text; Type is one of the subjects above.
type Typed struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// rank is the display order of each subject (lower sorts first). General and the
// generic Tag subject share the same label ("Tags") and sit last.
var rank = map[string]int{
	Language: 0, Artist: 1, Group: 2, Parody: 3,
	Character: 4, Category: 5, Tag: 6, General: 7,
}

// Normalize maps arbitrary subject text (e.g. an nhentai tag "type", or a synonym
// like "circle") to a known subject, falling back to General for anything unknown.
func Normalize(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "language", "languages":
		return Language
	case "artist", "artists":
		return Artist
	case "group", "groups", "circle":
		return Group
	case "parody", "parodies":
		return Parody
	case "character", "characters":
		return Character
	case "category", "categories":
		return Category
	case "tag", "tags":
		return Tag
	default:
		return General
	}
}

// Rank returns the display order of a subject (unknown subjects sort last).
func Rank(s string) int {
	if r, ok := rank[Normalize(s)]; ok {
		return r
	}
	return len(rank)
}

// Label is the human heading for a subject. General and Tag both read "Tags" so
// generic content tags and untyped manual tags group together.
func Label(s string) string {
	switch Normalize(s) {
	case Language:
		return "Language"
	case Artist:
		return "Artist"
	case Group:
		return "Group"
	case Parody:
		return "Parody"
	case Character:
		return "Character"
	case Category:
		return "Category"
	default:
		return "Tags"
	}
}

// Sort orders typed tags by subject rank, then by name — the canonical order the read
// path returns and the UI groups on. It sorts in place and returns the slice.
func Sort(ts []Typed) []Typed {
	sort.SliceStable(ts, func(i, j int) bool {
		ri, rj := Rank(ts[i].Type), Rank(ts[j].Type)
		if ri != rj {
			return ri < rj
		}
		return ts[i].Name < ts[j].Name
	})
	return ts
}
