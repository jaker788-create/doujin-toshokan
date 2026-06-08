package main

// This file holds the nhentai auto-tagging surface of the frontend API: settings
// for the user-entered API key, a per-title match/apply flow, and a rate-limited
// bulk sweep that emits progress events and collects ambiguous titles for review.
// The matching logic lives in internal/autotag; the HTTP client in internal/nhentai.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"doujin/internal/autotag"
	"doujin/internal/config"
	"doujin/internal/doujin"
	"doujin/internal/ingest"
	"doujin/internal/nhentai"
	"doujin/internal/search"
	"doujin/internal/tag"
)

// defaultUserAgent is sent when the user hasn't configured one. nhentai asks for a
// descriptive User-Agent identifying the app.
const defaultUserAgent = "DoujinToshokan/0.4.0 (personal manga library; auto-tagger)"

// previewCount is how many top candidates MatchNhentai detail-fetches for tag
// previews. Each fetch costs one rate-limited request, so this is kept small.
const previewCount = 3

// shortlistMax caps how many ranked candidates are returned to the UI.
const shortlistMax = 8

// maxSearchQueries caps how many nhentai searches one title may trigger. Doujin
// folder names are too decorated to match as a whole, so we search by circle/artist
// and author anchors (which work) before falling back to title variants — but we
// stop early on a strong page-exact hit, so most titles cost only 1–2 searches.
const maxSearchQueries = 3

// strongTitleScore mirrors autotag's auto-apply title bar; once a page-exact
// candidate clears it we stop issuing more searches.
const strongTitleScore = 0.6

// matchInput is everything derived from a title's folder name + stored author that
// the matcher and the review UI need: the title variants (scoring), the search anchors
// (querying), the local language, and the local artist/parodies (to flag overlapping
// candidates). The folder *basename* is parsed — not the cleaned stored title — so
// these survive title cleaning, exactly as Rescan re-derives them.
type matchInput struct {
	variants []string
	anchors  []string
	lang     string          // local language ("" when the folder name implies none)
	artist   string          // lowercased local artist/author, for overlap flags
	parodies map[string]bool // lowercased local parody set, for overlap flags
}

// matchInputs parses the folder basename into a matchInput. The stored author (from
// the author folder) wins as the local artist when present; otherwise the artist is
// taken from the [Circle (Artist)] in the name.
func matchInputs(folderPath, fallbackTitle, authorName string) matchInput {
	p := doujin.ParseName(filepath.Base(folderPath))
	mi := matchInput{
		variants: p.TitleVariants(),
		anchors:  p.Anchors(),
		lang:     p.Language,
		parodies: map[string]bool{},
	}
	if len(mi.variants) == 0 {
		mi.variants = []string{fallbackTitle}
	}
	if a := strings.TrimSpace(authorName); a != "" {
		mi.anchors = append(mi.anchors, a)
		mi.artist = strings.ToLower(a)
	} else if a := p.Author(); a != "" {
		mi.artist = strings.ToLower(a)
	}
	for _, par := range p.Parodies {
		mi.parodies[strings.ToLower(strings.TrimSpace(par))] = true
	}
	return mi
}

// candLangResolver reads a candidate's language from its own title decorations using
// the same vocabulary as the local parser — no extra request, so it works in the bulk
// sweep too. The english title is tried first, then the japanese.
func candLangResolver(r nhentai.SearchResult) string {
	if l := doujin.DetectLanguage(r.EnglishTitle); l != "" {
		return l
	}
	return doujin.DetectLanguage(r.JapaneseTitle)
}

// maxCatalogPages bounds how many pages of an artist's catalog the bulk sweep pages
// through (25 results each). nhentai search is newest-first, so reaching an artist's older
// works needs paging; this caps the cost at ~250 works (~33s at the throttle) and flags
// truncation past it so a deep match isn't silently dropped.
const maxCatalogPages = 10

// truncatedNote annotates a per-title outcome whose artist catalog hit the page cap, so a
// review/none result is understood as "maybe a deeper match exists", not "definitely none".
const truncatedNote = "artist catalog truncated at 10 pages — a deeper match may exist"

// nhSearcher is the slice of the nhentai client the bulk sweep depends on. The concrete
// *nhentai.Client satisfies it; tests inject a counting fake (the client's base URL is
// unexported, so the package's own tests can't point a real client at a test server).
type nhSearcher interface {
	Search(ctx context.Context, query string, page int) (*nhentai.SearchResponse, error)
	GalleryByID(ctx context.Context, id int64) (*nhentai.GalleryDetail, error)
}

// cachedSearch is one query's accumulated results in the run cache. complete means every
// page (up to the cap) was fetched — a catalog page-through — so a sibling title can match
// against it with no further network; a single page fetch is not complete. truncated marks
// that the catalog had more pages than maxCatalogPages.
type cachedSearch struct {
	results   []nhentai.SearchResult
	complete  bool
	truncated bool
}

// autoTagRun is the per-sweep state: the search client plus a run-scoped cache so an
// artist's catalog is fetched once and reused across all their local titles. langMode is
// the per-run language narrowing ("auto" follows the local tag / assumes all; "english" and
// "japanese" force a filter). artistCount drives the catalog-vs-title-first choice: only an
// artist with >=2 local titles is worth a full catalog page-through.
type autoTagRun struct {
	client      nhSearcher
	langMode    string
	artistCount map[string]int
	searchCache map[string]*cachedSearch
	detailCache map[int64]*nhentai.GalleryDetail
}

// newAutoTagRun builds a run with empty caches and a normalized language mode.
func newAutoTagRun(client nhSearcher, langMode string, artistCount map[string]int) *autoTagRun {
	if artistCount == nil {
		artistCount = map[string]int{}
	}
	return &autoTagRun{
		client:      client,
		langMode:    normLangMode(langMode),
		artistCount: artistCount,
		searchCache: map[string]*cachedSearch{},
		detailCache: map[int64]*nhentai.GalleryDetail{},
	}
}

// normLangMode validates the per-run language mode, defaulting empty/unknown to "auto".
func normLangMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "english":
		return "english"
	case "japanese":
		return "japanese"
	default:
		return "auto"
	}
}

// catalogLanguage resolves the concrete nhentai language slug to narrow a search by, or ""
// for no filter (all languages). Forced modes always win; "auto" follows the local language
// when concrete ("translated" is not concrete) and assumes all languages otherwise.
func catalogLanguage(langMode, localLang string) string {
	switch langMode {
	case "english":
		return "english"
	case "japanese":
		return "japanese"
	default:
		if localLang != "" && localLang != "translated" {
			return localLang
		}
		return ""
	}
}

// withLang appends nhentai's language: filter to a query when a concrete language resolves.
func withLang(query, lang string) string {
	if lang == "" {
		return query
	}
	return query + " language:" + lang
}

// artistCatalogQuery is the artist-tag catalog search, optionally language-narrowed.
func artistCatalogQuery(artist, lang string) string {
	return withLang(`artist:"`+artist+`"`, lang)
}

// catalog fetches an artist-catalog query, paging through every result page (capped at
// maxCatalogPages) and caching the complete set so sibling titles reuse it with no further
// network. A cached complete result is returned as-is. The bool reports the catalog had more
// pages than the cap (a deeper work may be unseen).
func (r *autoTagRun) catalog(ctx context.Context, query string) ([]nhentai.SearchResult, bool, error) {
	key := strings.ToLower(query)
	if cs := r.searchCache[key]; cs != nil && cs.complete {
		return cs.results, cs.truncated, nil
	}
	seen := map[int64]bool{}
	var acc []nhentai.SearchResult
	truncated := false
	for page := 1; page <= maxCatalogPages; page++ {
		resp, err := r.client.Search(ctx, query, page)
		if err != nil {
			return nil, false, err
		}
		for _, sr := range resp.Result {
			if !seen[sr.ID] {
				seen[sr.ID] = true
				acc = append(acc, sr)
			}
		}
		if page >= resp.NumPages {
			break
		}
		if page == maxCatalogPages && resp.NumPages > maxCatalogPages {
			truncated = true
		}
	}
	r.searchCache[key] = &cachedSearch{results: acc, complete: true, truncated: truncated}
	return acc, truncated, nil
}

// searchPage fetches a single search page, cached by query+page. Used by the title-first
// fallbacks; it never marks a cache entry complete (only catalog pages through).
func (r *autoTagRun) searchPage(ctx context.Context, query string, page int) ([]nhentai.SearchResult, error) {
	if page < 1 {
		page = 1
	}
	key := fmt.Sprintf("%s#%d", strings.ToLower(query), page)
	if cs := r.searchCache[key]; cs != nil {
		return cs.results, nil
	}
	resp, err := r.client.Search(ctx, query, page)
	if err != nil {
		return nil, err
	}
	r.searchCache[key] = &cachedSearch{results: resp.Result}
	return resp.Result, nil
}

// detail fetches a gallery's full detail, cached by id (so a resync or an overlapping merge
// set doesn't refetch the same gallery).
func (r *autoTagRun) detail(ctx context.Context, id int64) (*nhentai.GalleryDetail, error) {
	if d := r.detailCache[id]; d != nil {
		return d, nil
	}
	d, err := r.client.GalleryByID(ctx, id)
	if err != nil {
		return nil, err
	}
	r.detailCache[id] = d
	return d, nil
}

// searchRequest is one nhentai search call: a query plus a 1-based result page.
type searchRequest struct {
	query string
	page  int
}

// searchRequests orders the nhentai searches to try. Free-text title goes first — for
// most titles nhentai matches it directly, and the caller verifies the artist on the
// results. But nhentai's free-text only indexes a gallery's primary (romaji/japanese)
// title, so a local name taken from the english subtitle finds nothing; the reliable
// fallback is the artist *tag*, narrowed by the first distinctive title word
// (artist:"name" title:"word") so a prolific artist's catalog collapses to a handful,
// then the bare catalog. Anchor free-text is the last resort (e.g. a circle with no
// artist tag). When lang is set every query is language-narrowed. De-duplicated by
// query+page; the caller bounds how many run.
func searchRequests(artist string, variants, anchors []string, lang string) []searchRequest {
	var reqs []searchRequest
	seen := map[string]bool{}
	add := func(q string, page int) {
		q = strings.TrimSpace(q)
		if q == "" || page < 1 {
			return
		}
		q = withLang(q, lang)
		k := fmt.Sprintf("%s#%d", strings.ToLower(q), page)
		if !seen[k] {
			seen[k] = true
			reqs = append(reqs, searchRequest{q, page})
		}
	}
	for _, v := range variants {
		add(v, 1)
	}
	if artist != "" {
		aq := `artist:"` + artist + `"`
		if len(variants) > 0 {
			if w := firstTitleWord(variants[0]); w != "" {
				add(aq+` title:"`+w+`"`, 1)
			}
		}
		add(aq, 1)
	}
	for _, a := range anchors {
		add(a, 1)
	}
	return reqs
}

// firstTitleWord returns the first distinctive word of a title for a title:"<word>"
// narrowing filter: the first normalized token of length >= 2 (so one-letter stopwords
// like "a"/"i" are skipped in favour of a word that actually narrows). "" when none.
func firstTitleWord(s string) string {
	for _, w := range strings.Fields(autotag.Normalize(s)) {
		if len(w) >= 2 {
			return w
		}
	}
	return ""
}

// gatherCandidates finds and scores nhentai candidates for one title through the run cache.
// For a prolific local artist (>=2 titles in this run) it pages the artist's whole catalog
// once — cached, so every sibling reuses it with no further network — and matches against
// that; only a catalog miss falls through to a title free-text fallback. For a single-title
// or unknown artist it runs the cheaper title-first ladder (bounded by maxSearchQueries),
// stopping early on a confident match. The bool reports a truncated artist catalog.
func (a *App) gatherCandidates(ctx context.Context, run *autoTagRun, mi matchInput, pages int, localLang string) ([]autotag.Candidate, bool, error) {
	lang := catalogLanguage(run.langMode, localLang)
	seen := map[int64]bool{}
	var acc []nhentai.SearchResult
	add := func(rs []nhentai.SearchResult) {
		for _, r := range rs {
			if !seen[r.ID] {
				seen[r.ID] = true
				acc = append(acc, r)
			}
		}
	}
	score := func() []autotag.Candidate {
		return autotag.ScoreAll(mi.variants, pages, localLang, acc, candLangResolver)
	}

	truncated := false
	catalogFirst := mi.artist != "" && run.artistCount[mi.artist] >= 2
	if catalogFirst {
		results, trunc, err := run.catalog(ctx, artistCatalogQuery(mi.artist, lang))
		if err != nil {
			return nil, false, err
		}
		truncated = trunc
		add(results)
		if confidentMatch(score(), mi.artist) {
			return score(), truncated, nil
		}
	}

	// Per-title searches. After a catalog page-through the artist tag is exhausted, so the
	// only useful fallback is the title by free-text (a work mis-tagged under a different
	// artist on nhentai); otherwise run the full title-first ladder.
	var reqs []searchRequest
	if catalogFirst {
		for _, v := range mi.variants {
			if v = strings.TrimSpace(v); v != "" {
				reqs = append(reqs, searchRequest{withLang(v, lang), 1})
			}
		}
	} else {
		reqs = searchRequests(mi.artist, mi.variants, mi.anchors, lang)
	}
	for i, rq := range reqs {
		if i >= maxSearchQueries {
			break
		}
		results, err := run.searchPage(ctx, rq.query, rq.page)
		if err != nil {
			return nil, false, err
		}
		add(results)
		if confidentMatch(score(), mi.artist) {
			break
		}
	}
	return score(), truncated, nil
}

// confidentMatch reports whether some scored candidate is safe enough to stop searching:
// pages within tolerance and a full-enough title (autotag's auto-apply bar) AND — when
// the local artist is known — the candidate's own artist matches. That artist guard is
// what lets the title-only search run first without a same-titled work by a different
// artist ending the search before the artist-narrowed query gets its turn.
func confidentMatch(cands []autotag.Candidate, localArtist string) bool {
	for i := range cands {
		c := cands[i]
		if !c.PagesClose || c.TitleScore < strongTitleScore {
			continue
		}
		if localArtist == "" || candidateArtistMatches(c.Gallery, localArtist) {
			return true
		}
	}
	return false
}

// candidateArtistMatches reports whether a search result's title-parsed artist equals the
// local artist (case-insensitive). It mirrors markOverlap's title parse, so it works on
// bare search results before any detail fetch.
func candidateArtistMatches(r nhentai.SearchResult, localArtist string) bool {
	if localArtist == "" {
		return false
	}
	return strings.EqualFold(doujin.ParseName(r.EnglishTitle).Author(), localArtist)
}

// errNoAPIKey is surfaced to the UI when no key is configured.
var errNoAPIKey = errors.New("no nhentai API key set — add one in Settings")

// Settings is the safe, maskable view of API-related config. The key itself is
// never returned — only whether one is set.
type Settings struct {
	HasNhentaiKey    bool   `json:"has_nhentai_key"`
	NhentaiUserAgent string `json:"nhentai_user_agent"`
}

// GetSettings reports whether an nhentai key is configured (without revealing it)
// plus the configured User-Agent, so the UI can show configured/not-configured.
func (a *App) GetSettings() (Settings, error) {
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return Settings{}, err
	}
	return Settings{
		HasNhentaiKey:    strings.TrimSpace(cfg.NhentaiAPIKey) != "",
		NhentaiUserAgent: cfg.NhentaiUserAgent,
	}, nil
}

// SetNhentaiKey stores the user's API key in config.json (trimmed). Passing an
// empty string clears it, disabling the auto-tag features.
func (a *App) SetNhentaiKey(key string) error {
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return err
	}
	cfg.NhentaiAPIKey = strings.TrimSpace(key)
	return config.Save(cfg, a.dataDir)
}

// nhentaiClient builds a client from the current config, re-read each call so a key
// change takes effect immediately. Returns errNoAPIKey when no key is set. Each
// call returns a fresh client with its own limiter; the UI avoids running a bulk
// sweep and a per-title fetch at the same time, so the per-client limiters don't
// compound in practice.
func (a *App) nhentaiClient() (*nhentai.Client, error) {
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(cfg.NhentaiAPIKey)
	if key == "" {
		return nil, errNoAPIKey
	}
	ua := strings.TrimSpace(cfg.NhentaiUserAgent)
	if ua == "" {
		ua = defaultUserAgent
	}
	return nhentai.NewClient(key, ua), nil
}

// NhentaiCandidate is one ranked match shown to the UI. MediaID/Thumbnail build the
// cover image; GalleryURL opens the gallery in the browser. Language/LangMatch and
// ArtistMatch/ParodyMatch drive the why-match badges. Tags is populated only for
// detail-fetched candidates (the merge set or the top few); it is nil otherwise to
// avoid a detail fetch per candidate.
type NhentaiCandidate struct {
	GalleryID     int64       `json:"gallery_id"`
	MediaID       string      `json:"media_id"`
	Thumbnail     string      `json:"thumbnail"`
	GalleryURL    string      `json:"gallery_url"`
	EnglishTitle  string      `json:"english_title"`
	JapaneseTitle string      `json:"japanese_title"`
	NumPages      int         `json:"num_pages"`
	NumFavorites  int         `json:"num_favorites"`
	Score         float64     `json:"score"`
	TitleScore    float64     `json:"title_score"`
	PagesExact    bool        `json:"pages_exact"`
	PageDelta     int         `json:"page_delta"`
	Language      string      `json:"language"`
	LangMatch     bool        `json:"lang_match"`
	LangMismatch  bool        `json:"lang_mismatch"`
	ArtistMatch   bool        `json:"artist_match"`
	ParodyMatch   bool        `json:"parody_match"`
	Tags          []tag.Typed `json:"tags"`
}

// MatchResult is the per-title match payload. Decision is "auto", "review", or "none"
// (no candidates at all). On an auto decision MergeGalleryIDs lists the variants whose
// tags merge (the UI's one-click apply); the local cover is drawn from FolderPath +
// CoverRelPath so the bulk review queue can show it.
type MatchResult struct {
	MangaID         int64              `json:"manga_id"`
	LocalTitle      string             `json:"local_title"`
	LocalAuthor     string             `json:"local_author"`
	LocalPages      int                `json:"local_pages"`
	LocalLanguage   string             `json:"local_language"`
	LocalTags       []tag.Typed        `json:"local_tags"`
	FolderPath      string             `json:"folder_path"`
	CoverRelPath    *string            `json:"cover_rel_path"`
	Decision        string             `json:"decision"`
	MergeGalleryIDs []int64            `json:"merge_gallery_ids"`
	Candidates      []NhentaiCandidate `json:"candidates"`
}

// MatchNhentai searches nhentai for one title, ranks the results, and returns the
// auto/review decision plus a shortlist. The top previewCount candidates are
// detail-fetched so the UI can show their would-be tags. This makes several
// rate-limited requests, so it can take a few seconds.
func (a *App) MatchNhentai(id int64) (*MatchResult, error) {
	m, err := search.GetManga(a.db, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("manga %d not found", id)
	}
	client, err := a.nhentaiClient()
	if err != nil {
		return nil, err
	}
	mi := matchInputs(m.FolderPath, m.Title, m.AuthorName)
	localLang := mi.lang
	if localLang == "" {
		localLang = a.localLanguageTag(id)
	}
	// A single manual fetch: an empty artistCount keeps it on the title-first path (no
	// full catalog page-through for one title), with Auto language narrowing.
	run := newAutoTagRun(client, "auto", nil)
	scored, _, err := a.gatherCandidates(a.ctx, run, mi, m.PageCount, localLang)
	if err != nil {
		return nil, err
	}
	dec := autotag.Decide(scored)
	localTags, _ := search.GetMangaTagsTyped(a.db, id) // display only; nil on error is fine
	res := &MatchResult{
		MangaID:         id,
		LocalTitle:      m.Title,
		LocalAuthor:     m.AuthorName,
		LocalPages:      m.PageCount,
		LocalLanguage:   localLang,
		LocalTags:       localTags,
		FolderPath:      m.FolderPath,
		CoverRelPath:    m.CoverRelPath,
		Decision:        string(dec.Action),
		MergeGalleryIDs: applyGalleryIDs(dec.Apply),
		Candidates:      []NhentaiCandidate{},
	}
	if len(dec.Ranked) == 0 {
		res.Decision = "none"
		return res, nil
	}
	res.Candidates = shortlist(dec.Ranked, shortlistMax, mi)

	// Detail-fetch the candidates worth previewing: on auto, the merge set (so the UI
	// previews the union it will apply); on review, the top few. Each fetch refines the
	// candidate's tags + artist/parody overlap with authoritative detail data.
	toFetch := map[int64]bool{}
	if dec.Action == autotag.ActionAuto {
		for _, gid := range res.MergeGalleryIDs {
			toFetch[gid] = true
		}
	} else {
		for i := 0; i < len(res.Candidates) && i < previewCount; i++ {
			toFetch[res.Candidates[i].GalleryID] = true
		}
	}
	for i := range res.Candidates {
		if !toFetch[res.Candidates[i].GalleryID] {
			continue
		}
		if d, derr := run.detail(a.ctx, res.Candidates[i].GalleryID); derr == nil {
			res.Candidates[i].Tags = galleryTypedTags(d)
			markOverlap(&res.Candidates[i], mi.artist, mi.parodies, d)
		}
	}
	return res, nil
}

// localLanguageTag returns the manga's stored language-typed tag name (or ""), used as
// a fallback when the folder name doesn't imply a language.
func (a *App) localLanguageTag(mangaID int64) string {
	tags, err := search.GetMangaTagsTyped(a.db, mangaID)
	if err != nil {
		return ""
	}
	for _, t := range tags {
		if t.Type == tag.Language {
			return t.Name
		}
	}
	return ""
}

// ApplyNhentaiTags applies one explicitly chosen gallery (a manual pick in the review
// list). It fetches the gallery, unions its tags with the title's existing tags
// (preserving the local language), records the link, and returns the saved tag set.
func (a *App) ApplyNhentaiTags(mangaID, galleryID int64) ([]tag.Typed, error) {
	return a.ApplyNhentaiMerge(mangaID, []int64{galleryID})
}

// ApplyNhentaiMerge applies a set of galleries at once — the variants of one work that
// the matcher merged. It fetches each, unions their tags (preserving manual tags + the
// local language), stamps the primary (galleryIDs[0]) as the link, and returns the
// saved, subject-ordered tag set so the UI can re-render its grouped chips.
func (a *App) ApplyNhentaiMerge(mangaID int64, galleryIDs []int64) ([]tag.Typed, error) {
	if len(galleryIDs) == 0 {
		return nil, errors.New("no galleries to apply")
	}
	m, err := search.GetManga(a.db, mangaID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("manga %d not found", mangaID)
	}
	client, err := a.nhentaiClient()
	if err != nil {
		return nil, err
	}
	galleries := make([]*nhentai.GalleryDetail, 0, len(galleryIDs))
	for _, gid := range galleryIDs {
		d, err := client.GalleryByID(a.ctx, gid)
		if err != nil {
			return nil, err
		}
		galleries = append(galleries, d)
	}
	return a.applyTags(mangaID, galleryIDs[0], galleries)
}

// applyTags merges the galleries' subjected tags into the manga's existing tags
// (preserving manual tags), persists the union, and stamps nhentai_gallery_id with the
// primary (galleries[0]). Tags union by name; where a name is new it brings its nhentai
// subject, where it already exists the existing row keeps (or is upgraded to) the right
// subject via SetMangaTags → GetOrCreateTag.
//
// Language is preserved, never changed: if the title already has a language tag, all
// gallery language tags are dropped; otherwise only the primary gallery's single
// language is adopted (so merging a Japanese + English variant can't add two languages).
func (a *App) applyTags(mangaID, primaryGalleryID int64, galleries []*nhentai.GalleryDetail) ([]tag.Typed, error) {
	existing, err := search.GetMangaTagsTyped(a.db, mangaID)
	if err != nil {
		return nil, err
	}
	localHasLang := false
	for _, t := range existing {
		if t.Type == tag.Language {
			localHasLang = true
			break
		}
	}

	seen := map[string]bool{}
	all := make([]tag.Typed, 0, len(existing)+8)
	add := func(t tag.Typed) {
		if t.Name != "" && !seen[t.Name] {
			seen[t.Name] = true
			all = append(all, t)
		}
	}
	// Gallery tags first (primary first) so a name new to this title lands with its
	// nhentai subject; existing tags then fill in the rest, keeping the local language.
	keptGalleryLang := false
	for gi, d := range galleries {
		if d == nil {
			continue
		}
		for _, t := range galleryTypedTags(d) {
			if t.Type == tag.Language {
				if localHasLang || keptGalleryLang || gi != 0 {
					continue // preserve local language / adopt only the primary's
				}
				keptGalleryLang = true
			}
			add(t)
		}
	}
	for _, t := range existing {
		add(t)
	}
	saved, err := ingest.SetMangaTags(a.db, mangaID, all)
	if err != nil {
		return nil, err
	}
	if _, err := a.db.Exec("UPDATE manga SET nhentai_gallery_id=? WHERE id=?", primaryGalleryID, mangaID); err != nil {
		return nil, err
	}
	return saved, nil
}

// galleryTypedTags maps all of a gallery's tags to normalized, de-duplicated tags
// carrying their subject. Every nhentai type — tag, artist, group, parody, character,
// language, category — is imported (the user's choice), each mapped onto our subject
// vocabulary (see internal/tag). De-duplicated by name and sorted by subject then name.
func galleryTypedTags(d *nhentai.GalleryDetail) []tag.Typed {
	seen := map[string]bool{}
	var out []tag.Typed
	for _, t := range d.Tags {
		name := ingest.NormalizeTag(t.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, tag.Typed{Name: name, Type: tag.Normalize(t.Type)})
	}
	return tag.Sort(out)
}

func toCandidate(c autotag.Candidate) NhentaiCandidate {
	return NhentaiCandidate{
		GalleryID:     c.Gallery.ID,
		MediaID:       c.Gallery.MediaID,
		Thumbnail:     c.Gallery.Thumbnail,
		GalleryURL:    fmt.Sprintf("https://nhentai.net/g/%d/", c.Gallery.ID),
		EnglishTitle:  c.Gallery.EnglishTitle,
		JapaneseTitle: c.Gallery.JapaneseTitle,
		NumPages:      c.Gallery.NumPages,
		NumFavorites:  c.Gallery.NumFavorites,
		Score:         c.Score,
		TitleScore:    c.TitleScore,
		PagesExact:    c.PagesExact,
		PageDelta:     c.PageDelta,
		Language:      c.Lang,
		LangMatch:     c.LangMatch,
		LangMismatch:  c.LangMismatch,
	}
}

// shortlist turns the top n ranked candidates into UI candidates, flagging each whose
// artist/parody overlaps the local title (from the candidate's own title decorations —
// no detail fetch, so it works in the bulk sweep).
func shortlist(ranked []autotag.Candidate, n int, mi matchInput) []NhentaiCandidate {
	n = min(n, len(ranked))
	out := make([]NhentaiCandidate, 0, n)
	for i := range n {
		c := toCandidate(ranked[i])
		markOverlap(&c, mi.artist, mi.parodies, nil)
		out = append(out, c)
	}
	return out
}

// applyGalleryIDs is the gallery ids of a merge set, in primary-first order.
func applyGalleryIDs(cands []autotag.Candidate) []int64 {
	ids := make([]int64, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.Gallery.ID)
	}
	return ids
}

// markOverlap flags a candidate whose artist/parody matches the local title's — a
// strong corroborating signal for review. It reads the candidate's own title
// decorations and, when a detail is supplied, its authoritative artist/parody tags. It
// only ever sets a flag true, so a title-only pass can be refined by a later detail pass.
func markOverlap(c *NhentaiCandidate, localArtist string, localParodies map[string]bool, detail *nhentai.GalleryDetail) {
	cp := doujin.ParseName(c.EnglishTitle)
	if localArtist != "" && strings.EqualFold(cp.Author(), localArtist) {
		c.ArtistMatch = true
	}
	for _, par := range cp.Parodies {
		if localParodies[strings.ToLower(strings.TrimSpace(par))] {
			c.ParodyMatch = true
		}
	}
	if detail == nil {
		return
	}
	for _, t := range detail.Tags {
		name := strings.ToLower(strings.TrimSpace(t.Name))
		switch tag.Normalize(t.Type) {
		case tag.Artist:
			if localArtist != "" && name == localArtist {
				c.ArtistMatch = true
			}
		case tag.Parody:
			if localParodies[name] {
				c.ParodyMatch = true
			}
		}
	}
}

// ── Bulk auto-tag (async, event-driven, rate-limited) ──────────────────────

// AutoTagOptions controls the bulk sweep. Resync re-tags titles already linked to a
// gallery; otherwise linked titles are skipped (idempotent re-runs). LanguageMode narrows
// every search by language: "auto" follows each title's local language (assuming all
// languages when untagged), "english"/"japanese" force that language.
type AutoTagOptions struct {
	Resync       bool   `json:"resync"`
	LanguageMode string `json:"language_mode"`
}

// AutoTagProgress is emitted as "autotag:progress" once per processed title.
type AutoTagProgress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	MangaID int64  `json:"manga_id"`
	Title   string `json:"title"`
	Outcome string `json:"outcome"` // "applied" | "review" | "none" | "error"
	Detail  string `json:"detail"`
}

// AutoTagDone is emitted as "autotag:done" when the sweep ends. NeedsReview holds
// the ambiguous titles for the manual review queue.
type AutoTagDone struct {
	Total       int           `json:"total"`
	Applied     int           `json:"applied"`
	NeedsReview []MatchResult `json:"needs_review"`
	Cancelled   bool          `json:"cancelled"`
}

type autotagTarget struct {
	id           int64
	title        string
	pages        int
	folderPath   string
	coverRelPath *string
	author       string
}

// StartAutoTag launches the bulk sweep in the background and returns immediately.
// Setup errors (no key, a run already in progress) are returned synchronously;
// per-title outcomes arrive via events. Only one run may be active at a time.
func (a *App) StartAutoTag(opts AutoTagOptions) error {
	client, err := a.nhentaiClient()
	if err != nil {
		return err
	}

	a.autotagMu.Lock()
	if a.autotagCancel != nil {
		a.autotagMu.Unlock()
		return errors.New("an auto-tag run is already in progress")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.autotagCancel = cancel
	a.autotagMu.Unlock()

	targets, err := a.autotagTargets(opts.Resync)
	if err != nil {
		a.clearAutotag()
		return err
	}

	// Count how many targets share each artist so the run can page (and cache) the catalog
	// of a prolific artist once instead of re-searching it per sibling title.
	artistCount := map[string]int{}
	for _, t := range targets {
		if name := strings.ToLower(strings.TrimSpace(t.author)); name != "" {
			artistCount[name]++
		}
	}
	run := newAutoTagRun(client, opts.LanguageMode, artistCount)

	go a.runAutoTag(ctx, run, targets)
	return nil
}

// CancelAutoTag stops an in-flight sweep. No-op if none is running.
func (a *App) CancelAutoTag() {
	a.autotagMu.Lock()
	if a.autotagCancel != nil {
		a.autotagCancel()
	}
	a.autotagMu.Unlock()
}

func (a *App) clearAutotag() {
	a.autotagMu.Lock()
	a.autotagCancel = nil
	a.autotagMu.Unlock()
}

// autotagTargets reads the titles to process up-front (the single shared connection
// cannot iterate a cursor and write tag updates at the same time).
func (a *App) autotagTargets(resync bool) ([]autotagTarget, error) {
	q := "SELECT m.id, m.title, m.page_count, m.folder_path, m.cover_rel_path, a.name " +
		"FROM manga m JOIN authors a ON a.id = m.author_id"
	if !resync {
		q += " WHERE m.nhentai_gallery_id IS NULL"
	}
	q += " ORDER BY m.title"
	rows, err := a.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []autotagTarget
	for rows.Next() {
		var t autotagTarget
		var cover sql.NullString
		if err := rows.Scan(&t.id, &t.title, &t.pages, &t.folderPath, &cover, &t.author); err != nil {
			return nil, err
		}
		if cover.Valid {
			c := cover.String
			t.coverRelPath = &c
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// runAutoTag is the background loop. For each title it searches, decides, and either
// auto-applies (fetching the chosen gallery) or queues the title for review. A
// cancelled context ends the loop and emits a final cancelled event.
func (a *App) runAutoTag(ctx context.Context, run *autoTagRun, targets []autotagTarget) {
	defer a.clearAutotag()

	done := AutoTagDone{Total: len(targets), NeedsReview: []MatchResult{}}
	for i, t := range targets {
		if ctx.Err() != nil {
			done.Cancelled = true
			a.emitDone(done)
			return
		}
		prog := AutoTagProgress{Done: i + 1, Total: len(targets), MangaID: t.id, Title: t.title}

		mi := matchInputs(t.folderPath, t.title, t.author)
		localLang := mi.lang
		if localLang == "" {
			localLang = a.localLanguageTag(t.id)
		}
		scored, truncated, err := a.gatherCandidates(ctx, run, mi, t.pages, localLang)
		if err != nil {
			if ctx.Err() != nil {
				done.Cancelled = true
				a.emitDone(done)
				return
			}
			prog.Outcome, prog.Detail = "error", err.Error()
			a.emit(prog)
			continue
		}

		dec := autotag.Decide(scored)
		if len(dec.Ranked) == 0 {
			prog.Outcome = "none"
			if truncated {
				prog.Detail = truncatedNote
			}
			a.emit(prog)
			continue
		}

		if dec.Action == autotag.ActionAuto {
			// Fetch the whole merge set (the variants of this work) and union their tags.
			galleries := make([]*nhentai.GalleryDetail, 0, len(dec.Apply))
			failed := false
			for _, c := range dec.Apply {
				d, derr := run.detail(ctx, c.Gallery.ID)
				if derr != nil {
					if ctx.Err() != nil {
						done.Cancelled = true
						a.emitDone(done)
						return
					}
					prog.Outcome, prog.Detail = "error", derr.Error()
					a.emit(prog)
					failed = true
					break
				}
				galleries = append(galleries, d)
			}
			if failed {
				continue
			}
			primary := dec.Apply[0].Gallery.ID
			if _, aerr := a.applyTags(t.id, primary, galleries); aerr != nil {
				prog.Outcome, prog.Detail = "error", aerr.Error()
				a.emit(prog)
				continue
			}
			done.Applied++
			prog.Outcome, prog.Detail = "applied", dec.Apply[0].Gallery.EnglishTitle
			a.emit(prog)
			continue
		}

		// Review: queue candidate metadata (no tag previews here — applying fetches them).
		// The local author + existing tags ride along so the review card can show what
		// the user already has, not just the stripped title.
		localTags, _ := search.GetMangaTagsTyped(a.db, t.id)
		done.NeedsReview = append(done.NeedsReview, MatchResult{
			MangaID:       t.id,
			LocalTitle:    t.title,
			LocalAuthor:   t.author,
			LocalPages:    t.pages,
			LocalLanguage: localLang,
			LocalTags:     localTags,
			FolderPath:    t.folderPath,
			CoverRelPath:  t.coverRelPath,
			Decision:      "review",
			Candidates:    shortlist(dec.Ranked, shortlistMax, mi),
		})
		prog.Outcome = "review"
		if truncated {
			prog.Detail = truncatedNote
		}
		a.emit(prog)
	}
	a.emitDone(done)
}

func (a *App) emit(p AutoTagProgress) { wailsruntime.EventsEmit(a.ctx, "autotag:progress", p) }
func (a *App) emitDone(d AutoTagDone) { wailsruntime.EventsEmit(a.ctx, "autotag:done", d) }
