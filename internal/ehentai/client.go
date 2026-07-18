// Package ehentai is a client for E-Hentai's gallery metadata API and one
// implementation of source.Provider. Like hitomi it is an ID-only provider: the API has
// no free-text search, so Search returns an empty response and the site is reached
// through the folder-id shortcut ("ehentai-<gid>-<token> - …") and manual apply.
//
// The endpoint is E-Hentai's one public API:
//
//	POST https://api.e-hentai.org/api.php
//	{"method":"gdata","gidlist":[[618395,"0439fa3666"]],"namespace":1}
//
// No key, no account, no cookies — plain HTTP against the public API. (ExHentai-exclusive
// galleries would need session cookies; SourceConfig.Secrets exists for that and it is
// deliberately not wired up yet — see roadmap 2.4.)
//
// A gallery is identified by a (gid, token) PAIR, not a single id — the token is a
// capability, and the right gid with a wrong token is refused. The neutral string id is
// therefore "<gid>/<token>", which is what lands in manga.source_ref.
//
// Four things the live API does that the obvious implementation gets wrong — all found by
// probing it, none visible against a fake server built from the field names alone:
//
//  1. The method is "gdata". "gmetadata" — the name that reads as the obvious one, since
//     it is what the response object is keyed by — answers {"error":"Unsupported method
//     provided"}.
//  2. "namespace":1 is load-bearing. Without it tags come back bare ("touhou project",
//     "nanahara fuyuki") instead of namespaced ("parody:touhou project",
//     "artist:nanahara fuyuki"), and the entire subject mapping below silently collapses
//     to untyped General tags. The response still looks perfectly healthy.
//  3. Titles are HTML-escaped. A real title comes back as "Hey, aren&#039;t you using me
//     as your &quot;fap material&quot;?" — left as-is, that is what gets string-compared
//     against the local folder name, so every apostrophe and quote costs match score.
//  4. Errors ride inside a 200. An unknown gallery or a bad token returns HTTP 200 with
//     {"gmetadata":[{"gid":…,"error":"Key missing, or incorrect key provided."}]}, so a
//     client that only checks the status code decodes a blank success.
//
// Tag mapping is close to 1:1 — e-hentai's namespaces are artist/group/parody/character/
// language plus the content namespaces male/female/mixed/other. The first five land on the
// matching subjects; the rest flatten to the generic Tag subject, dropping the namespace,
// because the local library's tags come from sites that do not namespace (the same call
// hitomi's female:/male: flattening makes). The gallery `category` ("Doujinshi", "Manga",
// "Artist CG", …) becomes a Category, mirroring nhentai's own category tag.
package ehentai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
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
	Slug  = "ehentai"
	Label = "E-Hentai"

	// DefaultBaseURL is the full API endpoint (the API is a single URL, not a host with
	// paths). Exported so the settings layer can show it as the default.
	DefaultBaseURL = "https://api.e-hentai.org/api.php"

	// siteBaseURL builds the human gallery URL from the (gid, token) pair.
	siteBaseURL = "https://e-hentai.org"

	// defaultInterval spaces requests through the single limiter. E-Hentai publishes no
	// rate for this endpoint, but it does ban for abuse and this provider only ever makes
	// detail fetches (Search is a no-op), so a full second between requests is politeness
	// bought cheaply rather than a documented ceiling.
	defaultInterval = 1 * time.Second

	requestTimeout = 20 * time.Second
	maxRetries     = 3

	// maxBodyBytes caps one response. A gallery document is a few KB; torrents[] is the
	// only unbounded part. This only stops a redirect to something huge being read in full.
	maxBodyBytes = 4 << 20
)

// tokenRe matches a gallery token: hex, and in practice exactly 10 characters. The client
// accepts any hex length so a format change does not need a release, while still refusing
// obvious junk before it reaches the request body.
var tokenRe = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// flexNum decodes a field E-Hentai may send as either a JSON number or a JSON string.
// This is not defensive padding: one response carries "gid":618395 (number) next to
// "filecount":"20" (string), and hitomi — the same class of site — types its gallery id
// inconsistently across its own catalog. Reading both shapes costs nothing; guessing wrong
// fails to decode the gallery entirely.
type flexNum string

func (f *flexNum) UnmarshalJSON(b []byte) error {
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
		*f = flexNum(s)
		return nil
	}
	*f = flexNum(string(b))
	return nil
}

// int returns the value as an int, or 0 when absent/unparseable.
func (f flexNum) int() int {
	n, err := strconv.Atoi(strings.TrimSpace(string(f)))
	if err != nil {
		return 0
	}
	return n
}

// ehGallery is one entry of the gmetadata array. Error is set — with HTTP 200 — when the
// gallery is unknown or the token is wrong (see the package doc), so it is checked before
// anything else is trusted.
//
// Deliberately absent: uploader, posted, filesize, rating, torrents, parent_gid/first_gid.
// Nothing above this package consumes them, and each one is a field that could change type
// under us for no benefit.
type ehGallery struct {
	GID       flexNum  `json:"gid"`
	Token     string   `json:"token"`
	Title     string   `json:"title"`
	TitleJpn  string   `json:"title_jpn"`
	Category  string   `json:"category"`
	FileCount flexNum  `json:"filecount"`
	Tags      []string `json:"tags"`
	Error     string   `json:"error"`
}

// ehResponse is the API envelope. Error is the top-level failure (a bad method name);
// per-gallery failures live on the entries instead.
type ehResponse struct {
	GMetadata []ehGallery `json:"gmetadata"`
	Error     string      `json:"error"`
}

// Client is a rate-limited E-Hentai metadata client implementing source.Provider. Build
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
// the API endpoint — pass "" for DefaultBaseURL. E-Hentai needs no API key.
func NewClient(userAgent, baseURL string) *Client {
	base := strings.TrimSpace(baseURL)
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

// Slug and Label identify the provider (source.Provider).
func (c *Client) Slug() string  { return Slug }
func (c *Client) Label() string { return Label }

// Search is a no-op returning no results. E-Hentai's API resolves galleries by id only —
// there is no JSON free-text search to call, and scraping the HTML listing is explicitly
// out of scope. The source.Provider contract is best-effort about exactly this. Returning
// an empty response rather than an error keeps a sweep reading as a clean "no match" per
// title instead of a wall of failures; the id shortcut and manual apply are the intended
// paths. The structured query is ignored by design, not overlooked.
func (c *Client) Search(ctx context.Context, q source.SearchQuery) (*source.SearchResponse, error) {
	return &source.SearchResponse{Result: []source.SearchResult{}}, nil
}

// GalleryByID fetches one gallery's metadata and maps it onto the neutral detail type.
// id is a "<gid>/<token>" pair; the "<gid>-<token>" form the folder-name shortcut produces
// is accepted too, and the returned detail always carries the canonical slash form so the
// ref stamped on the local title is stable regardless of which spelling arrived.
func (c *Client) GalleryByID(ctx context.Context, id string) (*source.GalleryDetail, error) {
	gid, token, err := parseRef(id)
	if err != nil {
		return nil, err
	}
	g, err := c.fetchGallery(ctx, gid, token)
	if err != nil {
		return nil, err
	}
	// Prefer the pair the response reports over the one we asked for, so a gallery that
	// answers under a different canonical identity is normalized before being stamped.
	outGID, outToken := g.GID.int(), strings.TrimSpace(g.Token)
	if outGID == 0 {
		outGID = gid
	}
	if outToken == "" {
		outToken = token
	}
	// Titles arrive HTML-escaped (see the package doc); unescaping here means every
	// consumer — the matcher's string comparison and the UI alike — sees the real title.
	title := strings.TrimSpace(html.UnescapeString(g.Title))
	return &source.GalleryDetail{
		ID:            ref(outGID, outToken),
		EnglishTitle:  title,
		JapaneseTitle: strings.TrimSpace(html.UnescapeString(g.TitleJpn)),
		PrettyTitle:   title,
		GalleryURL:    fmt.Sprintf("%s/g/%d/%s/", siteBaseURL, outGID, outToken),
		NumPages:      g.FileCount.int(),
		Tags:          mapTags(g),
		// Thumbnail: the response carries an absolute `thumb` URL and it would be genuinely
		// useful — but source.GalleryDetail has no Thumbnail field yet. Adding one is its
		// own change (roadmap 3.5), not something to smuggle in here.
	}, nil
}

// ref renders the canonical neutral id for a gallery.
func ref(gid int, token string) string { return strconv.Itoa(gid) + "/" + token }

// parseRef splits a "<gid>/<token>" (or "<gid>-<token>") pair. Both halves are validated
// before they reach the request body: a gid must be a number because the API's gidlist
// entry is [<number>,"<string>"], and a token must be hex.
func parseRef(id string) (int, string, error) {
	s := strings.TrimSpace(id)
	if s == "" {
		return 0, "", fmt.Errorf("ehentai: empty gallery id")
	}
	sep := strings.IndexAny(s, "/-")
	if sep < 0 {
		return 0, "", fmt.Errorf("ehentai: gallery id %q is not a gid/token pair", id)
	}
	gid, err := strconv.Atoi(strings.TrimSpace(s[:sep]))
	if err != nil || gid <= 0 {
		return 0, "", fmt.Errorf("ehentai: bad gid in %q", id)
	}
	token := strings.TrimSpace(s[sep+1:])
	if !tokenRe.MatchString(token) {
		return 0, "", fmt.Errorf("ehentai: bad token in %q", id)
	}
	return gid, token, nil
}

// namespaceSubjects maps e-hentai's tag namespaces onto our subjects. Namespaces absent
// here (male, female, mixed, other, cosplayer, reclass, temp) are content tags and flatten
// to the generic Tag subject — see the package doc.
var namespaceSubjects = map[string]string{
	"artist":    tag.Artist,
	"group":     tag.Group,
	"parody":    tag.Parody,
	"character": tag.Character,
	"language":  tag.Language,
}

// mapTags maps a gallery's namespaced tags plus its category onto our subject vocabulary,
// de-duplicated by (subject, name). A tag with no namespace at all — which is every tag if
// the request forgot namespace:1 — lands as a generic Tag.
func mapTags(g *ehGallery) []tag.Typed {
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

	add(g.Category, tag.Category)
	for _, t := range g.Tags {
		name, subject := t, tag.Tag
		if ns, bare, ok := strings.Cut(t, ":"); ok {
			if s, known := namespaceSubjects[strings.ToLower(strings.TrimSpace(ns))]; known {
				subject = s
			}
			name = bare
		}
		add(name, subject)
	}
	return tag.Sort(out)
}

// fetchGallery performs the throttled POST and unwraps the single-entry response.
func (c *Client) fetchGallery(ctx context.Context, gid int, token string) (*ehGallery, error) {
	// namespace:1 is required for the tag mapping to mean anything — see the package doc.
	body, err := json.Marshal(map[string]any{
		"method":    "gdata",
		"gidlist":   [][]any{{gid, token}},
		"namespace": 1,
	})
	if err != nil {
		return nil, err
	}
	raw, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var resp ehResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("ehentai: decoding gdata response: %w", err)
	}
	if e := strings.TrimSpace(resp.Error); e != "" {
		return nil, fmt.Errorf("ehentai: api error: %s", e)
	}
	if len(resp.GMetadata) == 0 {
		return nil, fmt.Errorf("ehentai: gallery %d/%s: empty response", gid, token)
	}
	g := resp.GMetadata[0]
	// A wrong token or unknown gallery arrives here as HTTP 200 with a per-entry error;
	// surfacing it is what stops a blank detail being applied as a successful match.
	if e := strings.TrimSpace(g.Error); e != "" {
		return nil, fmt.Errorf("ehentai: gallery %d/%s: %s", gid, token, e)
	}
	return &g, nil
}

// post performs a throttled POST of body to the API endpoint and returns the raw response.
// It retries on 429 (respecting Retry-After) up to maxRetries, and aborts promptly if ctx
// is cancelled (used by the bulk sweep's Cancel button).
func (c *Client) post(ctx context.Context, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.throttle(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("ehentai: rate limited (429)")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("ehentai: api returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
		}

		out, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("ehentai: reading response: %w", err)
		}
		return out, nil
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
