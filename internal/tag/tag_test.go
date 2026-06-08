package tag

import (
	"reflect"
	"testing"
)

func TestNormalizeMapsSynonymsAndUnknown(t *testing.T) {
	cases := map[string]string{
		"Language":   Language,
		"languages":  Language,
		"Circle":     Group,
		"group":      Group,
		"PARODY":     Parody,
		"characters": Character,
		"category":   Category,
		"tags":       Tag,
		"":           General,
		"whatever":   General,
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelGroupsGeneralAndTag(t *testing.T) {
	if Label(General) != "Tags" || Label(Tag) != "Tags" {
		t.Errorf("General/Tag should both label %q", "Tags")
	}
	if Label(Language) != "Language" {
		t.Errorf("Label(Language) = %q", Label(Language))
	}
}

func TestSortOrdersBySubjectThenName(t *testing.T) {
	in := []Typed{
		{"zeta", Tag},
		{"english", Language},
		{"sanada", Artist},
		{"alpha", Tag},
		{"naruto", Parody},
		{"manual", General},
	}
	got := Sort(in)
	want := []Typed{
		{"english", Language},
		{"sanada", Artist},
		{"naruto", Parody},
		{"alpha", Tag},
		{"zeta", Tag},
		{"manual", General},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sort = %v, want %v", got, want)
	}
}
