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
// The matcher (see the root tagging.go) emits nhentai-style query syntax
// (artist:"x", title:"word", "… language:english"); MangaDex can't parse that, so
// parseQuery translates it into a plain title search plus an optional language
// filter. Page count is 0 (unknown) for every result — that is fine, the scorer
// treats 0 as "no page signal" and leans on title + artist instead.
package mangadex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
}

// NewClient returns a client sending the given descriptive User-Agent. MangaDex needs
// no API key for reads.
func NewClient(userAgent string) *Client {
	return &Client{
		userAgent: userAgent,
		base:      defaultBaseURL,
		interval:  defaultInterval,
		http:      &http.Client{Timeout: requestTimeout},
	}
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

type mdRel struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		Name     string `json:"name"`     // author / artist
		FileName string `json:"fileName"` // cover_art
	} `json:"attributes"`
}

// ── Provider methods ───────────────────────────────────────────────────────

// Search runs a free-text title search. page is 1-based. The query may carry the
// matcher's nhentai-style syntax, which parseQuery translates into a plain title
// search plus a language filter.
func (c *Client) Search(ctx context.Context, query string, page int) (*source.SearchResponse, error) {
	if page < 1 {
		page = 1
	}
	title, lang := parseQuery(query)

	q := url.Values{}
	q.Set("title", title)
	q.Set("limit", fmt.Sprintf("%d", searchLimit))
	q.Set("offset", fmt.Sprintf("%d", (page-1)*searchLimit))
	for _, r := range contentRatings {
		q.Add("contentRating[]", r)
	}
	for _, inc := range []string{"author", "artist", "cover_art"} {
		q.Add("includes[]", inc)
	}
	if code := langNameToCode[lang]; code != "" {
		q.Add("availableTranslatedLanguages[]", code)
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
		NumPages:      sr.NumPages,
		Tags:          sr.Tags,
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

// parseQuery translates the matcher's nhentai-style query into a MangaDex title search
// plus an optional language name. It pulls out a "language:<name>" token, strips the
// artist:/title:/parody:/character: field markers (keeping their quoted values as search
// words), and unquotes the rest — so `artist:"kinomoto anzu" title:"best"` becomes
// ("kinomoto anzu best", "") and `Some Title language:english` becomes ("Some Title",
// "english").
func parseQuery(raw string) (text, lang string) {
	var words []string
	for _, f := range strings.Fields(raw) {
		low := strings.ToLower(f)
		if strings.HasPrefix(low, "language:") {
			lang = strings.TrimPrefix(low, "language:")
			continue
		}
		for _, field := range []string{"artist:", "title:", "parody:", "character:"} {
			if strings.HasPrefix(low, field) {
				f = f[len(field):]
				break
			}
		}
		f = strings.Trim(f, `"`)
		if f != "" {
			words = append(words, f)
		}
	}
	return strings.Join(words, " "), lang
}

// ── transport ──────────────────────────────────────────────────────────────

// do performs a throttled GET against base+path and decodes the JSON body into out.
func (c *Client) do(ctx context.Context, path string, out any) error {
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
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("mangadex: %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("mangadex: decoding %s: %w", path, err)
	}
	return nil
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
