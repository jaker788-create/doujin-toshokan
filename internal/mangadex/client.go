// Package mangadex is a rate-limited client for the public MangaDex API and one
// implementation of source.Provider. Unlike the doujin sites, MangaDex is a
// mainstream series aggregator: it has a real free-text search (GET /manga?title=…,
// no API key required) but a different data model — a *series* has no single page
// count (it has chapters), authors/artists arrive as relationships rather than tags,
// and its tags are genres/themes, not the booru namespaces. So the mapper here does
// "map what fits": author/artist → Artist, original language → Language, format/
// demographic → Category, genre/theme/content → Tag; parody/character/group have no
// MangaDex equivalent and come back absent.
//
// Searches arrive as a structured source.SearchQuery and are mapped onto MangaDex's
// own filters: Title -> title=, Language -> availableTranslatedLanguage[], and Artist
// -> authorOrArtist=<uuid> via a memoized /author?name= lookup, because MangaDex
// filters by author with a UUID and rejects a name outright. Folding the artist name
// into the title instead (which an earlier string-query contract forced) returned zero
// results every time — MangaDex titles, unlike a doujin site's decorated ones, never
// contain the author's name. Page count is 0 (unknown) for every result — that is fine,
// the scorer treats 0 as "no page signal" and leans on title + artist instead.
package mangadex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"doujin/internal/source"
	"doujin/internal/tag"
)

const (
	// Slug is the stable machine id stored in config + the manga.source_slug link
	// column; Label is the human name shown in the UI.
	Slug  = "mangadex"
	Label = "MangaDex"

	defaultBaseURL = "https://api.mangadex.org"
	coverBaseURL   = "https://uploads.mangadex.org/covers"
	titleBaseURL   = "https://mangadex.org/title"

	// defaultInterval spaces requests through the single limiter. MangaDex's global
	// limit is ~5 req/s; ~250ms keeps us comfortably under it.
	defaultInterval = 250 * time.Millisecond

	requestTimeout = 20 * time.Second
	searchLimit    = 25
	maxRetries     = 3
)

// contentRatings requests all ratings including adult content, which MangaDex omits by
// default. A doujin library is the point, so we opt into everything.
var contentRatings = []string{"safe", "suggestive", "erotica", "pornographic"}

// langNameToCode maps our language tag names to MangaDex ISO codes, used both to
// translate a "language:english" search filter and to label a result's language.
var langNameToCode = map[string]string{
	"english": "en", "japanese": "ja", "korean": "ko", "chinese": "zh",
	"spanish": "es", "french": "fr", "german": "de", "russian": "ru",
	"portuguese": "pt", "italian": "it", "vietnamese": "vi", "thai": "th",
	"indonesian": "id",
}

// langCodeToName inverts langNameToCode (plus common region variants) so a result's
// originalLanguage becomes a Language tag in our vocabulary.
var langCodeToName = map[string]string{
	"en": "english", "ja": "japanese", "ko": "korean", "zh": "chinese",
	"zh-hk": "chinese", "es": "spanish", "es-la": "spanish", "fr": "french",
	"de": "german", "ru": "russian", "pt": "portuguese", "pt-br": "portuguese",
	"it": "italian", "vi": "vietnamese", "th": "thai", "id": "indonesian",
}

// Client is a rate-limited MangaDex client implementing source.Provider. Build one
// with NewClient; the zero value is not usable.
type Client struct {
	userAgent string
	base      string
	interval  time.Duration
	http      *http.Client

	mu      sync.Mutex
	lastReq time.Time

	// authorIDs memoizes artist name -> MangaDex author UUID for the life of the client
	// (one sweep), so an artist's whole catalog costs a single /author lookup. A "" value
	// is a real answer: MangaDex knows no author by that name.
	authorMu  sync.Mutex
	authorIDs map[string]string
}

// NewClient returns a client sending the given descriptive User-Agent. MangaDex needs
// no API key for reads.
func NewClient(userAgent string) *Client {
	return &Client{
		userAgent: userAgent,
		base:      defaultBaseURL,
		interval:  defaultInterval,
		http:      &http.Client{Timeout: requestTimeout},
		authorIDs: map[string]string{},
	}
}

// SetRateLimit overrides the minimum spacing between requests, replacing defaultInterval;
// a non-positive duration is ignored. It backs config.SourceConfig.RateLimitMs, applied at
// build time before first use. The default is tuned to the site's tolerance, so lowering it
// risks a rate-limit ban.
func (c *Client) SetRateLimit(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.interval = d
	c.mu.Unlock()
}

// RateLimit reports the minimum spacing currently enforced between requests.
func (c *Client) RateLimit() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interval
}

// Slug and Label identify the provider (source.Provider).
func (c *Client) Slug() string  { return Slug }
func (c *Client) Label() string { return Label }

// ── wire types (private DTOs) ──────────────────────────────────────────────

type mdCollection struct {
	Data  []mdManga `json:"data"`
	Limit int       `json:"limit"`
	Total int       `json:"total"`
}

type mdEntity struct {
	Data mdManga `json:"data"`
}

type mdManga struct {
	ID         string `json:"id"`
	Attributes struct {
		Title                  map[string]string   `json:"title"`
		AltTitles              []map[string]string `json:"altTitles"`
		OriginalLanguage       string              `json:"originalLanguage"`
		PublicationDemographic *string             `json:"publicationDemographic"`
		Tags                   []mdTag             `json:"tags"`
	} `json:"attributes"`
	Relationships []mdRel `json:"relationships"`
}

type mdTag struct {
	Attributes struct {
		Name  map[string]string `json:"name"`
		Group string            `json:"group"`
	} `json:"attributes"`
}

// mdAuthorList is a GET /author?name= response — just enough to map a name to a UUID.
type mdAuthorList struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Name string `json:"name"`
		} `json:"attributes"`
	} `json:"data"`
}

type mdRel struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		Name     string `json:"name"`     // author / artist
		FileName string `json:"fileName"` // cover_art
	} `json:"attributes"`
}

// ── Provider methods ───────────────────────────────────────────────────────

// authorID resolves an artist name to a MangaDex author UUID. MangaDex filters by author
// with a UUID and never a name (authors[]=<name> is a 400 "must be at least 36 characters
// long"), so an artist-anchored search needs this lookup first. Results are memoized per
// client — including misses, which are a real answer — so a sweep over one artist's whole
// catalog costs one lookup rather than one per query. A transport error is NOT memoized: a
// transient failure must not poison the rest of the run. "" means no such author.
func (c *Client) authorID(ctx context.Context, name string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return "", nil
	}
	c.authorMu.Lock()
	id, ok := c.authorIDs[key]
	c.authorMu.Unlock()
	if ok {
		return id, nil
	}

	q := url.Values{}
	q.Set("name", name)
	q.Set("limit", "5")
	var out mdAuthorList
	if err := c.do(ctx, "/author?"+q.Encode(), &out); err != nil {
		return "", err
	}
	// Prefer an exact name match; otherwise take MangaDex's own best guess.
	for _, a := range out.Data {
		if strings.EqualFold(strings.TrimSpace(a.Attributes.Name), strings.TrimSpace(name)) {
			id = a.ID
			break
		}
	}
	if id == "" && len(out.Data) > 0 {
		id = out.Data[0].ID
	}
	c.authorMu.Lock()
	c.authorIDs[key] = id
	c.authorMu.Unlock()
	return id, nil
}

// Search runs a structured search. sq.Page is 1-based. The artist is resolved to a
// MangaDex author UUID and filtered with authorOrArtist (which matches either role — for
// a doujin they are the same person); the title is a plain title filter. The artist name
// is never folded into the title: MangaDex titles do not carry the author's name, so that
// search reliably returns nothing.
func (c *Client) Search(ctx context.Context, sq source.SearchQuery) (*source.SearchResponse, error) {
	page := sq.Page
	if page < 1 {
		page = 1
	}
	title := strings.TrimSpace(sq.Title)
	authorUUID := ""
	if artist := strings.TrimSpace(sq.Artist); artist != "" {
		id, err := c.authorID(ctx, artist)
		if err != nil {
			return nil, err
		}
		authorUUID = id
	}
	// Nothing left to filter on — an artist MangaDex has never heard of, and no title. An
	// unconstrained /manga would hand the matcher MangaDex's front page as candidates, so
	// return nothing instead. Search is best-effort (see the source package doc), and this
	// reads as a clean "no match" in the sweep log rather than an error.
	if title == "" && authorUUID == "" {
		return &source.SearchResponse{Result: []source.SearchResult{}}, nil
	}

	q := url.Values{}
	if title != "" {
		q.Set("title", title)
	}
	if authorUUID != "" {
		q.Set("authorOrArtist", authorUUID)
	}
	q.Set("limit", fmt.Sprintf("%d", searchLimit))
	q.Set("offset", fmt.Sprintf("%d", (page-1)*searchLimit))
	for _, r := range contentRatings {
		q.Add("contentRating[]", r)
	}
	for _, inc := range []string{"author", "artist", "cover_art"} {
		q.Add("includes[]", inc)
	}
	// NOTE the SINGULAR parameter name. MangaDex rejects the plural spelling outright:
	// availableTranslatedLanguages[]=en -> 400 "The property availableTranslatedLanguages
	// is not defined and the definition does not allow additional properties". Do not
	// "correct" this to the plural — TestSearchLanguageFilterParamName guards it.
	if code := langNameToCode[strings.ToLower(strings.TrimSpace(sq.Language))]; code != "" {
		q.Add("availableTranslatedLanguage[]", code)
	}

	var out mdCollection
	if err := c.do(ctx, "/manga?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	res := make([]source.SearchResult, 0, len(out.Data))
	for _, m := range out.Data {
		res = append(res, mapSearchResult(m))
	}
	numPages := 1
	if out.Limit > 0 {
		numPages = (out.Total + out.Limit - 1) / out.Limit
	}
	return &source.SearchResponse{Result: res, NumPages: numPages, Total: out.Total}, nil
}

// GalleryByID fetches one title's full detail. id is the MangaDex UUID.
func (c *Client) GalleryByID(ctx context.Context, id string) (*source.GalleryDetail, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("mangadex: empty id")
	}
	q := url.Values{}
	for _, inc := range []string{"author", "artist", "cover_art"} {
		q.Add("includes[]", inc)
	}
	var out mdEntity
	if err := c.do(ctx, "/manga/"+url.PathEscape(id)+"?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	sr := mapSearchResult(out.Data)
	return &source.GalleryDetail{
		ID:            sr.ID,
		MediaID:       sr.MediaID,
		EnglishTitle:  sr.EnglishTitle,
		JapaneseTitle: sr.JapaneseTitle,
		PrettyTitle:   sr.EnglishTitle,
		GalleryURL:    sr.GalleryURL,
		// includes[]=cover_art is requested above, so the cover URL is already built
		// (mapSearchResult) — carry it so a detail-fetched candidate shows a cover too.
		Thumbnail: sr.Thumbnail,
		NumPages:  sr.NumPages,
		Tags:      sr.Tags,
	}, nil
}

// ── mapping ────────────────────────────────────────────────────────────────

// mapSearchResult flattens a MangaDex manga entity onto the neutral SearchResult,
// including its subject-mapped tags and cover URL. NumPages is left 0 (a series has no
// single page count).
func mapSearchResult(m mdManga) source.SearchResult {
	english, japanese := titles(m)
	return source.SearchResult{
		ID:            m.ID,
		Thumbnail:     coverURL(m),
		GalleryURL:    titleBaseURL + "/" + m.ID,
		EnglishTitle:  english,
		JapaneseTitle: japanese,
		Language:      langCodeToName[strings.ToLower(m.Attributes.OriginalLanguage)],
		Tags:          mapTags(m),
	}
}

// titles picks the best English + Japanese titles. English prefers attributes.title.en,
// then the romaji/japanese as a fallback; Japanese prefers a ja / ja-ro alt-title.
func titles(m mdManga) (english, japanese string) {
	english = pick(m.Attributes.Title, "en")
	japanese = pick(m.Attributes.Title, "ja", "ja-ro")
	for _, alt := range m.Attributes.AltTitles {
		if japanese == "" {
			japanese = pick(alt, "ja", "ja-ro")
		}
		if english == "" {
			english = pick(alt, "en")
		}
	}
	if english == "" {
		english = anyValue(m.Attributes.Title)
	}
	return english, japanese
}

// pick returns the first non-empty value among the given keys of a localized-string map.
func pick(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(m[k]); v != "" {
			return v
		}
	}
	return ""
}

// anyValue returns an arbitrary non-empty value (used as a last-resort title when there
// is no english/romaji entry — e.g. a title only in its original script).
func anyValue(m map[string]string) string {
	for _, v := range m {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// mapTags maps a MangaDex manga onto our subject vocabulary ("map what fits"):
// author/artist relationships → Artist, originalLanguage → Language, publication
// demographic + the "format" tag group → Category, and the genre/theme/content tag
// groups → Tag. De-duplicated by (subject, name). Parody/character/group are absent
// from MangaDex and simply not produced.
func mapTags(m mdManga) []tag.Typed {
	var out []tag.Typed
	seen := map[string]bool{}
	add := func(name, subject string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return
		}
		key := subject + "\x00" + name
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, tag.Typed{Name: name, Type: subject})
	}

	for _, r := range m.Relationships {
		if r.Type == "author" || r.Type == "artist" {
			add(r.Attributes.Name, tag.Artist)
		}
	}
	if name := langCodeToName[strings.ToLower(m.Attributes.OriginalLanguage)]; name != "" {
		add(name, tag.Language)
	}
	if d := m.Attributes.PublicationDemographic; d != nil {
		add(*d, tag.Category)
	}
	for _, t := range m.Attributes.Tags {
		name := pick(t.Attributes.Name, "en")
		if name == "" {
			name = anyValue(t.Attributes.Name)
		}
		switch t.Attributes.Group {
		case "format":
			add(name, tag.Category)
		default: // genre, theme, content
			add(name, tag.Tag)
		}
	}
	return tag.Sort(out)
}

// coverURL builds a 256px cover thumbnail URL from the cover_art relationship, or "".
func coverURL(m mdManga) string {
	for _, r := range m.Relationships {
		if r.Type == "cover_art" && r.Attributes.FileName != "" {
			return fmt.Sprintf("%s/%s/%s.256.jpg", coverBaseURL, m.ID, r.Attributes.FileName)
		}
	}
	return ""
}

// ── transport ──────────────────────────────────────────────────────────────

// do performs a throttled GET against base+path and decodes the JSON body into out. It
// retries on 429 (respecting Retry-After) up to maxRetries, and aborts promptly if ctx is
// cancelled (used by the bulk sweep's Cancel button). MangaDex returns 429 under load.
func (c *Client) do(ctx context.Context, path string, out any) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.throttle(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("mangadex: rate limited (429) on %s", path)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			_ = resp.Body.Close()
			return fmt.Errorf("mangadex: %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
		}

		err = json.NewDecoder(resp.Body).Decode(out)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("mangadex: decoding %s: %w", path, err)
		}
		return nil
	}
	return lastErr
}

// throttle blocks until at least c.interval has elapsed since the previous request, or
// ctx is cancelled. Holding the mutex across the wait serializes requests.
func (c *Client) throttle(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastReq.IsZero() {
		if wait := c.interval - time.Since(c.lastReq); wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	c.lastReq = time.Now()
	return nil
}

// parseRetryAfter reads a Retry-After header expressed in seconds, clamped to a sane
// window. Missing or unparseable values fall back to 5s.
func parseRetryAfter(h string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && secs > 0 {
		if secs > 60 {
			secs = 60
		}
		return time.Duration(secs) * time.Second
	}
	return 5 * time.Second
}
