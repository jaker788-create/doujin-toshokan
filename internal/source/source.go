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
//   - Queries are structured, not strings. Callers describe *what* they are looking
//     for (SearchQuery{Title, Artist, Language}); each provider renders that into its
//     own wire format. Search syntax is a site's private business — nhentai's
//     artist:"x" title:"y" language:z is spoken only inside internal/nhentai, and
//     MangaDex turns the same query into real API filters instead of guessing at
//     somebody else's syntax.
//
// Tag taxonomy is each provider's responsibility: a provider maps its own tag
// vocabulary onto the shared tag.Subject set (see internal/tag) before returning,
// so nhentai's typed tags, mangadex's author relationships + genre tags, etc. all
// arrive already spoken in one language. Subjects a site has no concept of
// (mangadex has no parody/character/group) simply come back absent.
package source

import (
	"context"
	"strconv"
	"strings"

	"doujin/internal/tag"
)

// SearchQuery is one structured, provider-neutral search request.
//
// Title is free text — a title variant, or a circle/artist *anchor* the matcher falls
// back to. Artist is a structured constraint on the work's artist/author, which a
// provider is expected to satisfy with a real filter (nhentai's artist: tag, MangaDex's
// authorOrArtist UUID) rather than by folding the name into the text. Language narrows;
// it never searches on its own.
//
// A query with neither Title nor Artist is Empty and must not be sent: a bare language
// filter would match a site's entire catalog. Both the caller and every provider guard
// against it.
type SearchQuery struct {
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Language string `json:"language"`
	Page     int    `json:"page"` // 1-based; providers clamp anything below 1 to 1
}

// String renders the query in a canonical, provider-neutral form for diagnostics — the
// per-title trace the sweep log shows the user. It is deliberately NOT any site's query
// syntax: the one place nhentai's artist:"…" language:… form is spoken is inside the
// nhentai client, and a trace written in it would be a lie under any other provider.
func (q SearchQuery) String() string {
	var parts []string
	if a := strings.TrimSpace(q.Artist); a != "" {
		parts = append(parts, "artist="+strconv.Quote(a))
	}
	if t := strings.TrimSpace(q.Title); t != "" {
		parts = append(parts, "title="+strconv.Quote(t))
	}
	if l := strings.TrimSpace(q.Language); l != "" {
		parts = append(parts, "lang="+l)
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, " ")
}

// CacheKey is a query's case-insensitive identity ignoring the page — the key a complete
// catalog page-through is cached under, and the key the request ladder de-duplicates on.
// It must lowercase: two title variants differing only in case are one search.
func (q SearchQuery) CacheKey() string { return strings.ToLower(q.String()) }

// PageCacheKey identifies one *page* of a query. The "#<page>" suffix is unconditional so
// it can never collide with the page-less CacheKey a complete catalog is stored under —
// otherwise a single-page fetch could be served back as a complete artist catalog.
func (q SearchQuery) PageCacheKey() string {
	return q.CacheKey() + "#" + strconv.Itoa(q.Page)
}

// Empty reports a query with nothing to search for. Language alone is a filter, not a
// search — sending it would match a site's whole catalog — so callers skip an empty query.
func (q SearchQuery) Empty() bool {
	return strings.TrimSpace(q.Title) == "" && strings.TrimSpace(q.Artist) == ""
}

// SearchResult is one gallery in a Search response — the lightweight list item the
// matcher scores. Tags is usually nil for list items (names cost a detail fetch on
// most sites); Thumbnail and GalleryURL are absolute URLs the provider builds, so the
// frontend never reconstructs a per-site CDN path. Language, when a provider can supply
// it (e.g. MangaDex's originalLanguage mapped onto our tag vocabulary), lets the matcher
// rank by language without the title-decoration heuristic; "" means unknown.
type SearchResult struct {
	ID            string      `json:"id"`
	MediaID       string      `json:"media_id"`
	EnglishTitle  string      `json:"english_title"`
	JapaneseTitle string      `json:"japanese_title"`
	Language      string      `json:"language"`
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
// Label is the human name for the UI ("nhentai"). Search takes a structured
// SearchQuery (the provider renders it into its own wire format) and is
// best-effort — a detail-only site may return an empty response.
type Provider interface {
	Slug() string
	Label() string
	Search(ctx context.Context, q SearchQuery) (*SearchResponse, error)
	GalleryByID(ctx context.Context, id string) (*GalleryDetail, error)
}
