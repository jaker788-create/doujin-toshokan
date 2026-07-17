package doujin

import (
	"reflect"
	"slices"
	"testing"
)

func TestCleanArtist(t *testing.T) {
	cases := []struct{ in, want string }{
		{"(Rustle)", "Rustle"},
		{"[Yoku]", "Yoku"},
		{"  (Airandou)  ", "Airandou"},
		{"A6-Kisho Muri", "A6-Kisho Muri"},     // hybrid, no wrap
		{"A6 (Kisho Muri)", "A6 (Kisho Muri)"}, // parens don't enclose the whole string
		{"(a) (b)", "(a) (b)"},                 // two groups, not one wrap
		{"((Nested))", "(Nested)"},             // peel one layer
		{"(Rustle", "(Rustle"},                 // unbalanced
		{"()", "()"},                           // never clean to empty
		{"Rustle", "Rustle"},                   // already clean
		{"", ""},
	}
	for _, c := range cases {
		if got := CleanArtist(c.in); got != c.want {
			t.Errorf("CleanArtist(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseStripsNhentaiPrefix(t *testing.T) {
	// The downloaded-file prefix "nhentai-<id> - " must be peeled so it neither
	// pollutes the title nor (as leading text before the first "[") suppresses circle
	// detection, while the id is captured for the direct gallery lookup.
	name := "nhentai-271687 - [Kisho-Muri (A6)] Himitsu no Suiyoubi - Secret Wednesdays [English] {Shotachan} [Digital]"
	p := ParseName(name)

	if p.SourceSlug != "nhentai" || p.SourceRef != "271687" {
		t.Errorf("Source = (%q,%q), want (nhentai,271687)", p.SourceSlug, p.SourceRef)
	}
	if p.Circle != "Kisho-Muri (A6)" {
		t.Errorf("Circle = %q, want %q", p.Circle, "Kisho-Muri (A6)")
	}
	if p.Author() != "A6" {
		t.Errorf("Author() = %q, want A6", p.Author())
	}
	// The " - " is the romaji/english separator (a Windows-safe stand-in for "|"), so it
	// normalizes to " / " and the english half is a standalone matchable variant.
	if p.DisplayTitle() != "Himitsu no Suiyoubi / Secret Wednesdays" {
		t.Errorf("Title = %q, want %q", p.DisplayTitle(), "Himitsu no Suiyoubi / Secret Wednesdays")
	}
	if !slices.Contains(p.TitleVariants(), "Secret Wednesdays") {
		t.Errorf("TitleVariants %v missing english half %q", p.TitleVariants(), "Secret Wednesdays")
	}
	if p.Language != "english" {
		t.Errorf("Language = %q, want english", p.Language)
	}
	if !reflect.DeepEqual(p.MiscTags, []string{"digital"}) {
		t.Errorf("MiscTags = %v, want [digital]", p.MiscTags)
	}
}

func TestDashSeparatorSplitsButKeepsHyphens(t *testing.T) {
	// " - " (spaced) is a dual-language separator and becomes " / "; hyphens inside
	// words/names (no surrounding spaces) are preserved.
	cases := []struct{ name, wantTitle string }{
		{"[A] Romaji Title - English Title [English]", "Romaji Title / English Title"},
		{"[bt-T Shounen (Sanada)] Ore-tachi no Jihen", "Ore-tachi no Jihen"}, // intra-word hyphens kept
		{"[A] Juma-kun Wakaraseru - Teach Juma-kun", "Juma-kun Wakaraseru / Teach Juma-kun"},
	}
	for _, c := range cases {
		if got := ParseName(c.name).DisplayTitle(); got != c.wantTitle {
			t.Errorf("DisplayTitle(%q) = %q, want %q", c.name, got, c.wantTitle)
		}
	}
}

func TestSourcePrefixVariants(t *testing.T) {
	const uuid = "550e8400-e29b-41d4-a716-446655440000"
	cases := []struct {
		in                         string
		wantSlug, wantRef, wantRem string
	}{
		{"nhentai-271687 - [A] T", "nhentai", "271687", "[A] T"},
		{"nhentai_99 [A] T", "nhentai", "99", "[A] T"},               // underscore join, no dash
		{"NHENTAI-5 - Title", "nhentai", "5", "Title"},               // case-insensitive slug
		{"mangadex-" + uuid + " - Title", "mangadex", uuid, "Title"}, // UUID ref
		{"mangadex-" + uuid + "[A] T", "mangadex", uuid, "[A] T"},    // bracket-terminated ref
		{"[A] Real Title", "", "", "[A] Real Title"},                 // no prefix: name untouched
		{"nhentai- - [A] T", "", "", "nhentai- - [A] T"},             // "nhentai-" with no digits
		{"mangadex-1234 - T", "", "", "mangadex-1234 - T"},           // not a UUID: not the pattern
		{"foo-123 T", "", "", "foo-123 T"},                           // unregistered slug: untouched
	}
	for _, c := range cases {
		slug, ref, rem := sourcePrefix(c.in)
		if slug != c.wantSlug || ref != c.wantRef || rem != c.wantRem {
			t.Errorf("sourcePrefix(%q) = (%q,%q,%q), want (%q,%q,%q)", c.in, slug, ref, rem, c.wantSlug, c.wantRef, c.wantRem)
		}
	}
}

func TestParseRealExample(t *testing.T) {
	name := "[Eight PM] Do Namaiki na Juma-kun o Mechakucha Wakaraseru _ Teaching the Super Cheeky Juma-kun One Hell of a Lesson [English] {Chin²} [Digital]"
	p := ParseName(name)

	if p.Circle != "Eight PM" {
		t.Errorf("Circle = %q, want %q", p.Circle, "Eight PM")
	}
	wantTitle := "Do Namaiki na Juma-kun o Mechakucha Wakaraseru / Teaching the Super Cheeky Juma-kun One Hell of a Lesson"
	if p.DisplayTitle() != wantTitle {
		t.Errorf("Title = %q, want %q", p.DisplayTitle(), wantTitle)
	}
	if p.Language != "english" {
		t.Errorf("Language = %q, want english", p.Language)
	}
	if p.Translator != "Chin²" {
		t.Errorf("Translator = %q, want Chin²", p.Translator)
	}
	if !reflect.DeepEqual(p.MiscTags, []string{"digital"}) {
		t.Errorf("MiscTags = %v, want [digital]", p.MiscTags)
	}
	if !reflect.DeepEqual(p.Anchors(), []string{"Eight PM"}) {
		t.Errorf("Anchors = %v, want [Eight PM]", p.Anchors())
	}
	if !reflect.DeepEqual(p.Tags(), []string{"digital", "english"}) {
		t.Errorf("Tags = %v, want [digital english]", p.Tags())
	}
	// The english half must be a standalone matching variant.
	wantVariant := "Teaching the Super Cheeky Juma-kun One Hell of a Lesson"
	found := false
	for _, v := range p.TitleVariants() {
		if v == wantVariant {
			found = true
		}
	}
	if !found {
		t.Errorf("TitleVariants %v missing english half %q", p.TitleVariants(), wantVariant)
	}
}

func TestParseEventMagazineArtistAndParody(t *testing.T) {
	// A leading (convention/magazine) is not the author; the artist is the inner name
	// of the circle group; the trailing (parody) is a tag, not part of the title.
	name := "(GOOD COMIC CITY 28) [bt-T Shounen (Sanada)] Ore-tachi no Hajimete Jihen (Kemono Jihen) [English] {Chin²}"
	p := ParseName(name)

	if p.Event != "GOOD COMIC CITY 28" {
		t.Errorf("Event = %q, want the magazine (not the author)", p.Event)
	}
	if p.Circle != "bt-T Shounen (Sanada)" {
		t.Errorf("Circle = %q", p.Circle)
	}
	if p.Author() != "Sanada" {
		t.Errorf("Author() = %q, want Sanada (the artist)", p.Author())
	}
	if p.Title != "Ore-tachi no Hajimete Jihen" {
		t.Errorf("Title = %q", p.Title)
	}
	if !reflect.DeepEqual(p.Parodies, []string{"Kemono Jihen"}) {
		t.Errorf("Parodies = %v, want [Kemono Jihen]", p.Parodies)
	}
	if p.Translator != "Chin²" {
		t.Errorf("Translator = %q, want Chin²", p.Translator)
	}
	if !reflect.DeepEqual(p.Tags(), []string{"english", "kemono jihen"}) {
		t.Errorf("Tags = %v, want [english, kemono jihen]", p.Tags())
	}
}

func TestAuthorPrefersArtistThenCircle(t *testing.T) {
	// Inner artist wins; a solo circle becomes the author verbatim; no circle is empty.
	cases := []struct{ name, want string }{
		{"[Group (Artist)] Title", "Artist"},
		{"[Solo Circle] Title", "Solo Circle"},
		{"Plain Title [English]", ""},
		{"[bt-T Shounen (Sanada)] X", "Sanada"},
	}
	for _, c := range cases {
		if got := ParseName(c.name).Author(); got != c.want {
			t.Errorf("Author(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDetectLanguage(t *testing.T) {
	// Reads the language from a recognized […] group anywhere in the string — the
	// same vocabulary ParseName uses — so it works on bare online candidate titles too.
	cases := []struct{ in, want string }{
		{"[Artist] Title [English]", "english"},
		{"(Kemono Jihen) [Chinese]", "chinese"},
		{"Title [Translated]", "translated"},
		{"[Circle (Artist)] Title (Parody) [Korean] [Digital]", "korean"},
		{"Title with no brackets", ""},
		{"[Artist] Title [Digital]", ""}, // a misc tag is not a language
		{"", ""},
	}
	for _, c := range cases {
		if got := DetectLanguage(c.in); got != c.want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseCircleWithArtist(t *testing.T) {
	p := ParseName("(C97) [Circle Name (Artist Name)] Cute Story (Naruto) [English]")
	if p.Event != "C97" {
		t.Errorf("Event = %q, want C97", p.Event)
	}
	if p.Circle != "Circle Name (Artist Name)" {
		t.Errorf("Circle = %q", p.Circle)
	}
	// Artist first, then circle.
	if got := p.Anchors(); !reflect.DeepEqual(got, []string{"Artist Name", "Circle Name", "Circle Name (Artist Name)"}) {
		t.Errorf("Anchors = %v", got)
	}
	if p.Title != "Cute Story" {
		t.Errorf("Title = %q, want Cute Story", p.Title)
	}
	if !reflect.DeepEqual(p.Tags(), []string{"english", "naruto"}) {
		t.Errorf("Tags = %v, want [english naruto]", p.Tags())
	}
	// A single circle has no extra collaborating artists (no regression).
	if got := p.ExtraArtistNames(); got != nil {
		t.Errorf("ExtraArtistNames = %v, want nil for a single circle", got)
	}
}

func TestExtraArtists(t *testing.T) {
	// A collaborative work names several artists in consecutive leading […] groups.
	// The first becomes the primary author (Author()); the rest are ExtraArtistNames(),
	// each reduced to its artist the same way the circle is, de-duped, primary excluded.
	cases := []struct {
		name       string
		wantCircle string
		wantAuthor string
		wantExtras []string
		wantEvent  string
		wantTitle  string
		wantLang   string
	}{
		{
			name:       "[ArtistA] [ArtistB] Title",
			wantCircle: "ArtistA", wantAuthor: "ArtistA",
			wantExtras: []string{"ArtistB"}, wantTitle: "Title",
		},
		{
			name:       "[A] [B] [C] Title [English]",
			wantCircle: "A", wantAuthor: "A",
			wantExtras: []string{"B", "C"}, wantTitle: "Title", wantLang: "english",
		},
		{
			name:       "[Circle Name (ArtistA)] [Other Circle (ArtistB)] Title",
			wantCircle: "Circle Name (ArtistA)", wantAuthor: "ArtistA",
			wantExtras: []string{"ArtistB"}, wantTitle: "Title",
		},
		{
			name:       "(C97) [A] [B] Title",
			wantCircle: "A", wantAuthor: "A", wantEvent: "C97",
			wantExtras: []string{"B"}, wantTitle: "Title",
		},
		{
			// Same artist twice collapses to a single primary, no extras.
			name:       "[Same] [Same] Title",
			wantCircle: "Same", wantAuthor: "Same",
			wantExtras: nil, wantTitle: "Title",
		},
		{
			// A language bracket before the title is not an artist (loop stops at it).
			name:       "[A] [English] Title",
			wantCircle: "A", wantAuthor: "A",
			wantExtras: nil, wantTitle: "Title",
		},
		{
			// No circle at all: nothing is an extra artist.
			name:       "Plain Title [English]",
			wantCircle: "", wantAuthor: "",
			wantExtras: nil, wantTitle: "Plain Title", wantLang: "english",
		},
	}
	for _, c := range cases {
		p := ParseName(c.name)
		if p.Circle != c.wantCircle {
			t.Errorf("%q: Circle = %q, want %q", c.name, p.Circle, c.wantCircle)
		}
		if p.Author() != c.wantAuthor {
			t.Errorf("%q: Author() = %q, want %q", c.name, p.Author(), c.wantAuthor)
		}
		if got := p.ExtraArtistNames(); !reflect.DeepEqual(got, c.wantExtras) {
			t.Errorf("%q: ExtraArtistNames() = %v, want %v", c.name, got, c.wantExtras)
		}
		if c.wantEvent != "" && p.Event != c.wantEvent {
			t.Errorf("%q: Event = %q, want %q", c.name, p.Event, c.wantEvent)
		}
		if p.Title != c.wantTitle {
			t.Errorf("%q: Title = %q, want %q", c.name, p.Title, c.wantTitle)
		}
		if p.Language != c.wantLang {
			t.Errorf("%q: Language = %q, want %q", c.name, p.Language, c.wantLang)
		}
	}
}

func TestParseNoCircleLeadingTitle(t *testing.T) {
	// No leading bracket: the text before the first […] is the title, not a circle.
	p := ParseName("Just A Title [English] [Digital]")
	if p.Circle != "" {
		t.Errorf("Circle = %q, want empty", p.Circle)
	}
	if p.Title != "Just A Title" {
		t.Errorf("Title = %q", p.Title)
	}
	if p.Anchors() != nil {
		t.Errorf("Anchors = %v, want nil", p.Anchors())
	}
	if p.Language != "english" {
		t.Errorf("Language = %q", p.Language)
	}
}

func TestParsePlainTitle(t *testing.T) {
	p := ParseName("A Perfectly Plain Title")
	if p.Title != "A Perfectly Plain Title" {
		t.Errorf("Title = %q", p.Title)
	}
	if len(p.Tags()) != 0 || p.Anchors() != nil {
		t.Errorf("expected no tags/anchors, got tags=%v anchors=%v", p.Tags(), p.Anchors())
	}
	if got := p.TitleVariants(); !reflect.DeepEqual(got, []string{"A Perfectly Plain Title"}) {
		t.Errorf("TitleVariants = %v", got)
	}
}

func TestUnknownBracketsIgnored(t *testing.T) {
	// An unknown trailing […] (not a language/misc tag) is dropped, not tagged.
	p := ParseName("[Artist] Title [SomeRandomGroup]")
	if len(p.Tags()) != 0 {
		t.Errorf("Tags = %v, want empty (unknown bracket ignored)", p.Tags())
	}
	if p.Title != "Title" {
		t.Errorf("Title = %q", p.Title)
	}
}
