// Package source is the provider-neutral seam between the tag-matcher and the
// metadata sites the app can fetch tags from (nhentai, mangadex, e-hentai, …).
//
// Everything above this package — the autotag scorer, the bulk sweep, the apply
// path — speaks these neutral types and the Provider interface; everything below
// (each site's HTTP client) maps that site's JSON onto them. Two design choices
// make the seam hold across very different sites:
//
//   - IDs are strings. nhentai uses an int gallery id, e-hentai a "gid/token"
//     pair, mangadex a UUID — a string is the common denominator, and the DB link
//     column (source_ref) stores it verbatim.
//   - Search is best-effort. Some sites (e-hentai) can only resolve a gallery by
//     id, not free-text search; such a provider returns an empty/errored Search and
//     is still fully useful via the id-in-folder-name shortcut and manual apply.
//
// Tag taxonomy is each provider's responsibility: a provider maps its own tag
// vocabulary onto the shared tag.Subject set (see internal/tag) before returning,
// so nhentai's typed tags, mangadex's author relationships + genre tags, etc. all
// arrive already spoken in one language. Subjects a site has no concept of
// (mangadex has no parody/character/group) simply come back absent.
package source

import (
	"context"

	"doujin/internal/tag"
)

// SearchResult is one gallery in a Search response — the lightweight list item the
// matcher scores. Tags is usually nil for list items (names cost a detail fetch on
// most sites); Thumbnail and GalleryURL are absolute URLs the provider builds, so the
// frontend never reconstructs a per-site CDN path.
type SearchResult struct {
	ID            string      `json:"id"`
	MediaID       string      `json:"media_id"`
	EnglishTitle  string      `json:"english_title"`
	JapaneseTitle string      `json:"japanese_title"`
	NumPages      int         `json:"num_pages"`
	NumFavorites  int         `json:"num_favorites"`
	Thumbnail     string      `json:"thumbnail"`
	GalleryURL    string      `json:"gallery_url"`
	Tags          []tag.Typed `json:"tags"`
}

// SearchResponse is a page of search results plus the total page/result counts the
// catalog page-through uses to know when to stop.
type SearchResponse struct {
	Result   []SearchResult `json:"result"`
	NumPages int            `json:"num_pages"`
	Total    int            `json:"total"`
}

// GalleryDetail is one gallery's full metadata, including its named, subject-typed
// tags. Titles are flattened (not nested) so every provider fills the same fields.
type GalleryDetail struct {
	ID            string      `json:"id"`
	MediaID       string      `json:"media_id"`
	EnglishTitle  string      `json:"english_title"`
	JapaneseTitle string      `json:"japanese_title"`
	PrettyTitle   string      `json:"pretty_title"`
	GalleryURL    string      `json:"gallery_url"`
	NumPages      int         `json:"num_pages"`
	Tags          []tag.Typed `json:"tags"`
}

// Provider is a metadata source the auto-tagger can query. Slug is the stable
// machine id stored in config + the manga.source_slug link column ("nhentai");
// Label is the human name for the UI ("nhentai"). Search is best-effort — a
// detail-only site may return an empty response.
type Provider interface {
	Slug() string
	Label() string
	Search(ctx context.Context, query string, page int) (*SearchResponse, error)
	GalleryByID(ctx context.Context, id string) (*GalleryDetail, error)
}
