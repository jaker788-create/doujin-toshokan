package main

import (
	"testing"

	"doujin/internal/doujin"
	"doujin/internal/scanner"
	"doujin/internal/tag"
)

// hasTag reports whether ts contains a tag with the given name and subject.
func hasTag(ts []tag.Typed, name, typ string) bool {
	for _, t := range ts {
		if t.Name == name && t.Type == typ {
			return true
		}
	}
	return false
}

func TestParsedTypedTagsEmitsExtraArtists(t *testing.T) {
	got := parsedTypedTags(doujin.ParseName("[A] [B] Title [English]"))
	if !hasTag(got, "b", tag.Artist) {
		t.Errorf("expected artist tag 'b', got %v", got)
	}
	if !hasTag(got, "english", tag.Language) {
		t.Errorf("expected language tag 'english', got %v", got)
	}
	// The primary artist becomes the author, never an artist tag.
	for _, tt := range got {
		if tt.Name == "a" {
			t.Errorf("primary artist 'a' must not be emitted as a tag, got %v", got)
		}
	}
}

func TestMangaInputFromFolderPrimaryAuthorPlusExtraArtistTag(t *testing.T) {
	in := mangaInputFromFolder(scanner.DetectedFolder{
		FolderPath: "/lib/[A] [B] Title",
		PageCount:  3,
	}, nil)
	if in.Author != "A" {
		t.Errorf("Author = %q, want A (primary author from first circle)", in.Author)
	}
	if in.Title != "Title" {
		t.Errorf("Title = %q, want Title", in.Title)
	}
	if !hasTag(in.Tags, "b", tag.Artist) {
		t.Errorf("expected collaborating artist tag 'b', got %v", in.Tags)
	}
}
