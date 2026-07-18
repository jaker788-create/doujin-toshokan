// Package nhentai is a small, rate-limited REST client for the nhentai v2 API and
// one implementation of source.Provider. The app uses it to look up galleries by
// free-text query and read their typed tags, so those tags can be copied onto local
// titles (see internal/autotag for the matching logic and the root tagging.go for
// the bound methods that drive it).
//
// Two endpoints matter: /search returns lightweight list items (titles, page count,
// and tag *ids* only), and /galleries/{id} returns the full gallery with named,
// typed tags. Matching uses search; applying tags needs the detail call. Both are
// decoded into private DTOs and mapped onto the neutral source.* types, with each
// nhentai tag "type" normalized to a tag.Subject on the way out.
//
// The client throttles every request through one shared limiter to stay under
// nhentai's authenticated ceiling (search 20/min, detail 45/min) and honours
// HTTP 429 + Retry-After. It never logs the API key.
package nhentai

import (
	"context"
	"encoding/json"
	"errors"
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
	Slug  = "nhentai"
	Label = "nhentai"

	defaultBaseURL = "https://nhentai.net/api/v2"

	// defaultInterval spaces all requests through the single limiter. The tightest
	// authenticated limit is search at 20/min; ~3.3s spacing keeps the whole app
	// under ~18 req/min regardless of the search/detail mix.
	defaultInterval = 3300 * time.Millisecond

	requestTimeout = 20 * time.Second
	maxRetries     = 3
)

// ErrNoKey is returned by request methods when no API key is configured. The
// caller surfaces this to the UI as "enter your key in Settings".
var ErrNoKey = errors.New("nhentai: no API key configured")

// nhSearchResult is one gallery in a /search response. Tags arrive here as ids only —
// names require a detail fetch — so the mapped source.SearchResult carries no tags.
type nhSearchResult struct {
	ID            int64   `json:"id"`
	MediaID       string  `json:"media_id"`
	EnglishTitle  string  `json:"english_title"`
	JapaneseTitle string  `json:"japanese_title"`
	NumPages      int     `json:"num_pages"`
	NumFavorites  int     `json:"num_favorites"`
	Thumbnail     string  `json:"thumbnail"`
	TagIDs        []int64 `json:"tag_ids"`
}

// nhSearchResponse is the /search envelope.
type nhSearchResponse struct {
	Result   []nhSearchResult `json:"result"`
	NumPages int              `json:"num_pages"`
	PerPage  int              `json:"per_page"`
	Total    int              `json:"total"`
}

// nhTag is one typed tag on a gallery detail. Type is one of
// tag, artist, group, parody, character, language, category.
type nhTag struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Count int    `json:"count"`
}

// nhGalleryDetail is a /galleries/{id} response. Unlike search list items, its title
// is an object and its tags carry names + types.
type nhGalleryDetail struct {
	ID    int64 `json:"id"`
	Title struct {
		English  string `json:"english"`
		Japanese string `json:"japanese"`
		Pretty   string `json:"pretty"`
	} `json:"title"`
	MediaID   string  `json:"media_id"`
	NumPages  int     `json:"num_pages"`
	Scanlator string  `json:"scanlator"`
	Tags      []nhTag `json:"tags"`
}

// Client is a rate-limited nhentai v2 client implementing source.Provider. The zero
// value is not usable; build one with NewClient. It is safe for concurrent use,
// though the shared limiter serializes requests.
type Client struct {
	apiKey    string
	userAgent string
	base      string
	interval  time.Duration
	http      *http.Client

	mu      sync.Mutex
	lastReq time.Time
}

// NewClient returns a client authenticating as apiKey with the given User-Agent.
// nhentai requires a descriptive User-Agent (e.g.
// "DoujinToshokan/0.4.0 (contact)"). An empty apiKey makes every request fail
// with ErrNoKey.
func NewClient(apiKey, userAgent string) *Client {
	return &Client{
		apiKey:    apiKey,
		userAgent: userAgent,
		base:      defaultBaseURL,
		interval:  defaultInterval,
		http:      &http.Client{Timeout: requestTimeout},
	}
}

// Slug and Label identify the provider (source.Provider).
func (c *Client) Slug() string  { return Slug }
func (c *Client) Label() string { return Label }

// galleryURL builds the public gallery page URL for a gallery id.
func galleryURL(id int64) string { return fmt.Sprintf("https://nhentai.net/g/%d/", id) }

// buildQuery renders a neutral source.SearchQuery into nhentai's own search syntax
// (quoted phrases, artist:/title:/language: filters). This is the only place in the app
// that speaks it — everything above internal/source describes what it wants instead.
//
// The shape follows what the site actually indexes. Free text only matches a gallery's
// primary (romaji/japanese) title, so a query with no artist goes out as bare free text;
// once an artist tag constrains the search, the title is sent as the title:"…" field
// filter it should be. That rule is deliberate, not incidental — it is what lets one
// struct reproduce both shapes the matcher's ladder emits.
//
// A language never rides alone: "language:english" with nothing to search for would match
// every English gallery on the site, so an empty query stays empty.
func buildQuery(q source.SearchQuery) string {
	var parts []string
	artist := strings.TrimSpace(q.Artist)
	title := strings.TrimSpace(q.Title)
	switch {
	case artist != "":
		parts = append(parts, `artist:"`+artist+`"`)
		if title != "" {
			parts = append(parts, `title:"`+title+`"`)
		}
	case title != "":
		parts = append(parts, title)
	}
	if lang := strings.TrimSpace(q.Language); lang != "" && len(parts) > 0 {
		parts = append(parts, "language:"+lang)
	}
	return strings.Join(parts, " ")
}

// Search runs a gallery search. sq.Page is 1-based; values below 1 are clamped to 1.
// The neutral query is rendered into nhentai's own syntax by buildQuery.
func (c *Client) Search(ctx context.Context, sq source.SearchQuery) (*source.SearchResponse, error) {
	page := sq.Page
	if page < 1 {
		page = 1
	}
	q := url.Values{}
	q.Set("query", buildQuery(sq))
	q.Set("page", strconv.Itoa(page))
	var out nhSearchResponse
	if err := c.do(ctx, "/search?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	res := make([]source.SearchResult, 0, len(out.Result))
	for _, r := range out.Result {
		res = append(res, source.SearchResult{
			ID:            strconv.FormatInt(r.ID, 10),
			MediaID:       r.MediaID,
			EnglishTitle:  r.EnglishTitle,
			JapaneseTitle: r.JapaneseTitle,
			NumPages:      r.NumPages,
			NumFavorites:  r.NumFavorites,
			Thumbnail:     r.Thumbnail,
			GalleryURL:    galleryURL(r.ID),
			// Tags: nil — search returns tag ids only; names come from GalleryByID.
		})
	}
	return &source.SearchResponse{Result: res, NumPages: out.NumPages, Total: out.Total}, nil
}

// GalleryByID fetches one gallery's full detail, including its named, typed tags.
// id is the string gallery id (source.Provider); a non-numeric id is an error.
func (c *Client) GalleryByID(ctx context.Context, id string) (*source.GalleryDetail, error) {
	gid, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("nhentai: bad gallery id %q: %w", id, err)
	}
	var out nhGalleryDetail
	if err := c.do(ctx, "/galleries/"+strconv.FormatInt(gid, 10), &out); err != nil {
		return nil, err
	}
	tags := make([]tag.Typed, 0, len(out.Tags))
	for _, t := range out.Tags {
		// Map nhentai's tag "type" onto our subject vocabulary; names are left as the
		// site supplies them (the apply path normalizes + dedupes them).
		tags = append(tags, tag.Typed{Name: t.Name, Type: tag.Normalize(t.Type)})
	}
	return &source.GalleryDetail{
		ID:            strconv.FormatInt(out.ID, 10),
		MediaID:       out.MediaID,
		EnglishTitle:  out.Title.English,
		JapaneseTitle: out.Title.Japanese,
		PrettyTitle:   out.Title.Pretty,
		GalleryURL:    galleryURL(out.ID),
		NumPages:      out.NumPages,
		Tags:          tags,
	}, nil
}

// do performs a throttled GET against base+path and decodes the JSON body into
// out. It retries on 429 (respecting Retry-After) up to maxRetries, and aborts
// promptly if ctx is cancelled (used by the bulk sweep's Cancel button).
func (c *Client) do(ctx context.Context, path string, out any) error {
	if c.apiKey == "" {
		return ErrNoKey
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.throttle(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Key "+c.apiKey)
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("nhentai: rate limited (429) on %s", path)
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
			return fmt.Errorf("nhentai: %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
		}

		err = json.NewDecoder(resp.Body).Decode(out)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("nhentai: decoding %s: %w", path, err)
		}
		return nil
	}
	return lastErr
}

// throttle blocks until at least c.interval has elapsed since the previous
// request, or ctx is cancelled. Holding the mutex across the wait serializes
// requests, which is the intended single-limiter behaviour.
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

// parseRetryAfter reads a Retry-After header expressed in seconds, clamped to a
// sane window. Missing or unparseable values fall back to 5s.
func parseRetryAfter(h string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && secs > 0 {
		if secs > 60 {
			secs = 60
		}
		return time.Duration(secs) * time.Second
	}
	return 5 * time.Second
}
