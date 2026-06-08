package doujin

import (
	"reflect"
	"testing"
)

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
