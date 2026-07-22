// Package hitomi is a client for hitomi.la's gallery metadata and one implementation
// of source.Provider. It is the first ID-only provider: hitomi has no server-side
// search API, so Search returns an empty response and the site is reached purely
// through the folder-id shortcut ("hitomi-<id> - …") and manual apply.
//
// There is no official API. Every third-party client reads the same de-facto endpoint:
//
//	GET https://ltn.gold-usergeneratedcontent.net/galleries/{id}.js
//
// which serves JavaScript, not JSON — the body is "var galleryinfo = {…}", so the
// assignment prefix is stripped before decoding (see decodeGalleryInfo). No auth, no
// token, no account: a descriptive User-Agent and a hitomi.la Referer are enough.
//
// Its fields map almost 1:1 onto our tag subjects — artists/groups/parodys (sic)/
// characters/language/type — which makes it a better tag source than the search-less
// contract suggests. Two mappings are deliberate rather than obvious: hitomi namespaces
// some tags by gender ("female:loli"), which we flatten to the bare name because the
// local library's tags come from sites that do not namespace, and the gallery `type`
// (doujinshi/manga/cg/imageset/anime) becomes a Category, matching nhentai's own
// "doujinshi" category tag.
//
// Churn: the data domain has already moved once — ltn.hitomi.la stopped resolving
// entirely after the 2025-03 move to ltn.gold-usergeneratedcontent.net. That is why the
// base URL is configurable (SourceConfig.BaseURL) rather than a constant: the next move
// should be a settings edit, not a release.
package hitomi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	Slug  = "hitomi"
	Label = "Hitomi.la"

	// DefaultBaseURL is the current metadata host. Exported so the settings layer can
	// show it as the default when no override is configured.
	DefaultBaseURL = "https://ltn.gold-usergeneratedcontent.net"

	// siteBaseURL is the public site, used only to absolutize the relative gallery URL
	// the metadata carries. It has not moved with the data domain.
	siteBaseURL = "https://hitomi.la"

	// defaultInterval spaces requests through the single limiter. This is a static file
	// host with no published limit; ~400ms is politeness, not a documented ceiling.
	defaultInterval = 400 * time.Millisecond

	requestTimeout = 20 * time.Second
	maxRetries     = 3

	// maxBodyBytes caps a gallery document. Real ones run a few KB to a few hundred KB
	// (files[] dominates); this only stops a redirect-to-something-huge from being read
	// into memory in full.
	maxBodyBytes = 8 << 20

	// jsPrefix is the assignment the endpoint wraps its JSON in.
	jsPrefix = "var galleryinfo ="
)

// flexID decodes hitomi's gallery id, which is a JSON number on older galleries and a
// JSON string on newer ones — both shapes are live today (id 5000 is a number, id
// 4056725 is a string). A plain int64 or string field would fail to decode half the
// site, so the id is read as raw JSON and unquoted if needed.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexID(s)
		return nil
	}
	*f = flexID(string(b))
	return nil
}

// hiNamed covers hitomi's four parallel "list of one-key objects" shapes: artists[] has
// {"artist":…}, groups[] {"group":…}, parodys[] {"parody":…}, characters[] {"character":…}.
// Decoding all four into one struct lets a single mapping loop handle them; exactly one
// field is ever populated per entry.
type hiNamed struct {
	Artist    string `json:"artist"`
	Group     string `json:"group"`
	Parody    string `json:"parody"`
	Character string `json:"character"`
}

// name returns whichever of the four keys this entry carried.
func (n hiNamed) name() string {
	switch {
	case n.Artist != "":
		return n.Artist
	case n.Group != "":
		return n.Group
	case n.Parody != "":
		return n.Parody
	default:
		return n.Character
	}
}

// hiTag is one tag. Only the name is read — see the package doc on flattening hitomi's
// gender namespace.
//
// The "male"/"female" markers are deliberately NOT fields here. Beyond being unused, they
// are inconsistently typed across the site exactly the way the gallery id is: newer
// galleries send the string "1", older ones (id 100, 5000) send the number 1, and
// ungendered tags omit the key. Declaring them as string decodes today's galleries and
// fails every old one with "cannot unmarshal number into ... of type string". If they are
// ever wanted, they need flexID-style handling, not a plain field.
type hiTag struct {
	Tag string `json:"tag"`
}

// hiGallery is the galleryinfo document. Every list field is nullable (a gallery with no
// parodies sends "parodys":null, not []), which decodes to a nil slice and ranges zero
// times — so no explicit nil checks are needed below.
type hiGallery struct {
	ID            flexID    `json:"id"`
	Title         string    `json:"title"`
	JapaneseTitle string    `json:"japanese_title"`
	Type          string    `json:"type"`
	Language      string    `json:"language"`
	GalleryURL    string    `json:"galleryurl"`
	Artists       []hiNamed `json:"artists"`
	Groups        []hiNamed `json:"groups"`
	Parodys       []hiNamed `json:"parodys"`
	Characters    []hiNamed `json:"characters"`
	Tags          []hiTag   `json:"tags"`
	Files         []struct {
		Name string `json:"name"`
	} `json:"files"`
}

// Client is a rate-limited hitomi metadata client implementing source.Provider. Build
// one with NewClient; the zero value is not usable. Safe for concurrent use, though the
// shared limiter serializes requests.
type Client struct {
	userAgent string
	base      string
	interval  time.Duration
	http      *http.Client

	mu      sync.Mutex
	lastReq time.Time
}

// NewClient returns a client sending the given descriptive User-Agent. baseURL overrides
// the metadata host — pass "" for DefaultBaseURL. hitomi needs no API key.
func NewClient(userAgent, baseURL string) *Client {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		userAgent: userAgent,
		base:      base,
		interval:  defaultInterval,
		http:      &http.Client{Timeout: requestTimeout},
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

// Search is a no-op returning no results. hitomi's search runs client-side over binary
// .nozomi index files rather than a query endpoint, so there is nothing to ask; the
// source.Provider contract is explicitly best-effort about this. Returning an empty
// response rather than an error keeps a hitomi sweep reading as a clean "no match" per
// title instead of a wall of failures — the id shortcut and manual apply are the
// intended paths. The structured query is ignored by design, not overlooked.
func (c *Client) Search(ctx context.Context, q source.SearchQuery) (*source.SearchResponse, error) {
	return &source.SearchResponse{Result: []source.SearchResult{}}, nil
}

// GalleryByID fetches one gallery's metadata and maps it onto the neutral detail type.
// id is the trailing number in any hitomi gallery URL (".../…-中文-4056725.html").
func (c *Client) GalleryByID(ctx context.Context, id string) (*source.GalleryDetail, error) {
	gid := strings.TrimSpace(id)
	if gid == "" {
		return nil, fmt.Errorf("hitomi: empty gallery id")
	}
	// Ids are numeric; rejecting anything else keeps a stray ref from being pasted into
	// the request path.
	if _, err := strconv.ParseInt(gid, 10, 64); err != nil {
		return nil, fmt.Errorf("hitomi: bad gallery id %q: %w", id, err)
	}
	g, err := c.fetchGallery(ctx, gid)
	if err != nil {
		return nil, err
	}
	// Prefer the id the document reports over the one we asked for. Some old ids are
	// aliases: /galleries/900.js serves the gallery whose own id is 4646, and 4646.js
	// serves the same document. Taking the reported id normalizes an alias to the
	// canonical gallery, so the ref stamped on the local title (manga.source_ref) is the
	// durable one. A document that omits the id at all is still the one we requested.
	outID := string(g.ID)
	if outID == "" {
		outID = gid
	}
	return &source.GalleryDetail{
		ID:            outID,
		EnglishTitle:  strings.TrimSpace(g.Title),
		JapaneseTitle: strings.TrimSpace(g.JapaneseTitle),
		PrettyTitle:   strings.TrimSpace(g.Title),
		GalleryURL:    galleryURL(g, outID),
		NumPages:      len(g.Files),
		Tags:          mapTags(g),
		// MediaID/Thumbnail: hitomi's image URLs are derived from files[].hash through
		// the site's own URL-shuffling script (gg.js), which churns independently of
		// this endpoint. Shipping without a thumbnail beats shipping a broken one.
	}, nil
}

// galleryURL absolutizes the relative "galleryurl" the metadata carries, falling back to
// the canonical /galleries/<id>.html form when it is absent.
func galleryURL(g *hiGallery, id string) string {
	rel := strings.TrimSpace(g.GalleryURL)
	if rel == "" {
		return siteBaseURL + "/galleries/" + id + ".html"
	}
	if strings.HasPrefix(rel, "http://") || strings.HasPrefix(rel, "https://") {
		return rel
	}
	return siteBaseURL + "/" + strings.TrimPrefix(rel, "/")
}

// mapTags maps a hitomi gallery onto our subject vocabulary. The four named lists and
// the language line up 1:1 with our subjects; `type` becomes a Category (mirroring
// nhentai's "doujinshi" category tag) and the gender namespace on tags[] is flattened
// (see the package doc). De-duplicated by (subject, name).
func mapTags(g *hiGallery) []tag.Typed {
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

	for _, list := range []struct {
		items   []hiNamed
		subject string
	}{
		{g.Artists, tag.Artist},
		{g.Groups, tag.Group},
		{g.Parodys, tag.Parody},
		{g.Characters, tag.Character},
	} {
		for _, n := range list.items {
			add(n.name(), list.subject)
		}
	}
	add(g.Language, tag.Language)
	add(g.Type, tag.Category)
	for _, t := range g.Tags {
		add(t.Tag, tag.Tag)
	}
	return tag.Sort(out)
}

// fetchGallery performs the throttled GET and decodes the JS document.
func (c *Client) fetchGallery(ctx context.Context, id string) (*hiGallery, error) {
	path := "/galleries/" + id + ".js"
	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	g, err := decodeGalleryInfo(body)
	if err != nil {
		return nil, fmt.Errorf("hitomi: decoding %s: %w", path, err)
	}
	return g, nil
}

// decodeGalleryInfo strips the "var galleryinfo =" assignment the endpoint wraps its
// JSON in and decodes the rest. The prefix is matched leniently (leading whitespace, any
// spacing around "=", an optional trailing semicolon) so a cosmetic change to how the
// site emits the file does not break parsing; a body that is not this document at all —
// hitomi answers an unknown id with an HTML 404 page — fails with its opening bytes
// quoted, which is what makes the difference visible in a sweep log.
func decodeGalleryInfo(body []byte) (*hiGallery, error) {
	s := bytes.TrimSpace(body)
	if !bytes.HasPrefix(s, []byte("var")) {
		return nil, fmt.Errorf("not a galleryinfo document (starts with %q)", snippet(s))
	}
	rest := bytes.TrimSpace(s[len("var"):])
	if !bytes.HasPrefix(rest, []byte("galleryinfo")) {
		return nil, fmt.Errorf("not a galleryinfo document (starts with %q)", snippet(s))
	}
	rest = bytes.TrimSpace(rest[len("galleryinfo"):])
	if len(rest) == 0 || rest[0] != '=' {
		return nil, fmt.Errorf("not a galleryinfo document (starts with %q)", snippet(s))
	}
	rest = bytes.TrimSpace(rest[1:])
	rest = bytes.TrimRight(rest, "; \t\r\n")

	var g hiGallery
	if err := json.Unmarshal(rest, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// snippet quotes the first bytes of an unexpected body for an error message.
func snippet(b []byte) string {
	const n = 40
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

// get performs a throttled GET against base+path and returns the raw body. It retries on
// 429 (respecting Retry-After) up to maxRetries, and aborts promptly if ctx is cancelled
// (used by the bulk sweep's Cancel button).
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.throttle(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		// The image CDN requires a hitomi.la Referer; metadata does not, but sending it
		// keeps every request to the site uniform and costs nothing.
		req.Header.Set("Referer", siteBaseURL+"/")
		req.Header.Set("Accept", "*/*")

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("hitomi: rate limited (429) on %s", path)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// An unknown gallery id answers 404 with an HTML error page; the status is
			// the signal, so the body is not worth quoting beyond a fragment.
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("hitomi: %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("hitomi: reading %s: %w", path, err)
		}
		return body, nil
	}
	return nil, lastErr
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
