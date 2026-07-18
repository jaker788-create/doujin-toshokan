package main

// This file holds the source auto-tagging surface of the frontend API: settings for
// the user-entered API key, a per-title match/apply flow, and a rate-limited bulk
// sweep that emits progress events and collects ambiguous titles for review. The
// matching logic lives in internal/autotag; the concrete HTTP clients (nhentai,
// mangadex, …) live under internal/<provider> and satisfy source.Provider, so
// everything here speaks the neutral source.* types and a provider slug — never a
// single site's schema.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"doujin/internal/autotag"
	"doujin/internal/config"
	"doujin/internal/doujin"
	"doujin/internal/ingest"
	"doujin/internal/nhentai"
	"doujin/internal/scanner"
	"doujin/internal/search"
	"doujin/internal/source"
	"doujin/internal/tag"
)

// defaultUserAgent is sent when the user hasn't configured one. nhentai asks for a
// descriptive User-Agent identifying the app.
const defaultUserAgent = "DoujinToshokan/0.4.0 (personal manga library; auto-tagger)"

// previewCount is how many top candidates MatchSource detail-fetches for tag
// previews. Each fetch costs one rate-limited request, so this is kept small.
const previewCount = 3

// shortlistMax caps how many ranked candidates are returned to the UI.
const shortlistMax = 8

// reviewMax caps how many candidates a manual-review item shows. The review shortlist is
// the local artist's own catalog ranked by closeness, so a prolific artist is trimmed to
// the closest few rather than dumping the whole catalog on the user.
const reviewMax = 10

// maxSearchQueries caps how many searches one title may trigger. Doujin folder names are
// too decorated to match as a whole, so we search by circle/artist and author anchors
// (which work) before falling back to title variants — but we stop early on a strong
// page-exact hit, so most titles cost only 1–2 searches.
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
	variants   []string
	anchors    []string
	lang       string          // local language ("" when the folder name implies none)
	artist     string          // lowercased local artist/author, for overlap flags
	parodies   map[string]bool // lowercased local parody set, for overlap flags
	sourceSlug string          // provider slug from the folder name ("" if none) — gates the shortcut
	sourceRef  string          // provider gallery ref from the folder name ("" if none) — a direct-lookup shortcut
}

// matchInputs parses the folder basename into a matchInput. The stored author (from
// the author folder) wins as the local artist when present; otherwise the artist is
// taken from the [Circle (Artist)] in the name.
func matchInputs(folderPath, fallbackTitle, authorName string) matchInput {
	// TitleNameFor strips a .cbz/.zip extension for an archive title so its name parses
	// the same as a folder's; ParseName also peels a "<slug>-<ref>" prefix and exposes
	// the provider slug + gallery ref, which become a direct-lookup shortcut below.
	p := doujin.ParseName(scanner.TitleNameFor(folderPath))
	mi := matchInput{
		variants:   p.TitleVariants(),
		anchors:    p.Anchors(),
		lang:       p.Language,
		parodies:   map[string]bool{},
		sourceSlug: p.SourceSlug,
		sourceRef:  p.SourceRef,
	}
	if len(mi.variants) == 0 {
		mi.variants = []string{fallbackTitle}
	}
	// Clean a wrapping "(Artist)"/"[Artist]" folder name down to the bare artist tag,
	// so the catalog query and the artist-match compare both use "Rustle", not
	// "(Rustle)" (which matches no tag and never equals a parsed candidate artist).
	if a := doujin.CleanArtist(strings.TrimSpace(authorName)); a != "" {
		mi.anchors = append(mi.anchors, a)
		mi.artist = strings.ToLower(a)
	} else if a := doujin.CleanArtist(p.Author()); a != "" {
		mi.artist = strings.ToLower(a)
	}
	for _, par := range p.Parodies {
		mi.parodies[strings.ToLower(strings.TrimSpace(par))] = true
	}
	return mi
}

// candLangResolver reads a candidate's language, preferring a provider-supplied Language
// (e.g. MangaDex's originalLanguage, which carries no title decoration) and otherwise
// detecting it from the title decorations using the same vocabulary as the local parser.
// Either way it needs no extra request, so it works in the bulk sweep too. When falling
// back to detection the english title is tried first, then the japanese.
func candLangResolver(r source.SearchResult) string {
	if r.Language != "" {
		return r.Language
	}
	if l := doujin.DetectLanguage(r.EnglishTitle); l != "" {
		return l
	}
	return doujin.DetectLanguage(r.JapaneseTitle)
}

// maxCatalogPages bounds how many pages of an artist's catalog the bulk sweep pages
// through (25 results each). Provider search is newest-first, so reaching an artist's
// older works needs paging; this caps the cost at ~250 works (~33s at the throttle) and
// flags truncation past it so a deep match isn't silently dropped.
const maxCatalogPages = 10

// truncatedNote annotates a per-title outcome whose artist catalog hit the page cap, so a
// review/none result is understood as "maybe a deeper match exists", not "definitely none".
const truncatedNote = "artist catalog truncated at 10 pages — a deeper match may exist"

// nhSearcher is the slice of a source.Provider the bulk sweep depends on, plus Slug so
// the apply path can stamp which source a title's tags came from. The concrete provider
// clients satisfy it; tests inject a counting fake.
type nhSearcher interface {
	Slug() string
	Search(ctx context.Context, q source.SearchQuery) (*source.SearchResponse, error)
	GalleryByID(ctx context.Context, id string) (*source.GalleryDetail, error)
}

// cachedSearch is one query's accumulated results in the run cache. complete means every
// page (up to the cap) was fetched — a catalog page-through — so a sibling title can match
// against it with no further network; a single page fetch is not complete. truncated marks
// that the catalog had more pages than maxCatalogPages.
type cachedSearch struct {
	results   []source.SearchResult
	complete  bool
	truncated bool
}

// autoTagRun is the per-sweep state: the search client plus a run-scoped cache so an
// artist's catalog is fetched once and reused across all their local titles. slug is the
// provider's slug (stamped on apply). langMode is the per-run language narrowing ("auto"
// follows the local tag / assumes all; "english" and "japanese" force a filter).
// artistCount drives the catalog-vs-title-first choice: only an artist with >=2 local
// titles is worth a full catalog page-through.
type autoTagRun struct {
	client      nhSearcher
	slug        string
	langMode    string
	artistCount map[string]int
	searchCache map[string]*cachedSearch
	detailCache map[string]*source.GalleryDetail
	trace       []string // per-title query diagnostic (query→count); reset each gatherCandidates
}

// newAutoTagRun builds a run with empty caches and a normalized language mode.
func newAutoTagRun(client nhSearcher, langMode string, artistCount map[string]int) *autoTagRun {
	if artistCount == nil {
		artistCount = map[string]int{}
	}
	return &autoTagRun{
		client:      client,
		slug:        client.Slug(),
		langMode:    normLangMode(langMode),
		artistCount: artistCount,
		searchCache: map[string]*cachedSearch{},
		detailCache: map[string]*source.GalleryDetail{},
	}
}

// sourceChain is the ordered set of providers one sweep may consult.
//
// It holds one fully-formed autoTagRun per provider rather than one run swapping clients,
// and that is load-bearing: a run's searchCache is keyed by SearchQuery.CacheKey() and its
// detailCache by bare gallery id, both of which are provider-scoped. hitomi gallery 12345
// is not nhentai gallery 12345, so a shared cache would serve one site's gallery for
// another's id. One run each keeps the keys apart for free.
//
// all is every member in priority order; fuzzy is the subset consulted for free-text
// matching (an id-only source is excluded — its Search is empty by contract). bySlug
// covers every member including the id-only ones, because the folder-id shortcut routes
// by slug and that is precisely how an id-only provider earns its place.
type sourceChain struct {
	all    []*autoTagRun
	fuzzy  []*autoTagRun
	bySlug map[string]*autoTagRun
}

// newSourceChain builds the chain from the resolved providers. Every run shares the one
// artistCount map: it is derived from the local library, so it says nothing about any
// provider. fallback=false keeps only the first (active) provider in the fuzzy list, which
// is what makes the sweep option a pure narrowing of behaviour rather than a second code
// path.
func newSourceChain(providers []chainedProvider, langMode string, artistCount map[string]int, fallback bool) *sourceChain {
	ch := &sourceChain{bySlug: map[string]*autoTagRun{}}
	for i, p := range providers {
		run := newAutoTagRun(p.provider, langMode, artistCount)
		ch.all = append(ch.all, run)
		ch.bySlug[run.slug] = run
		if p.idOnly {
			continue // no free-text search to offer
		}
		if i > 0 && !fallback {
			continue // fallback disabled: the active source alone answers searches
		}
		ch.fuzzy = append(ch.fuzzy, run)
	}
	return ch
}

// primary is the run for the active source. It is always all[0] — the chain is built
// active-first — and an active source that is id-only still needs a run, for the shortcut
// and for stamping, even though it never appears in fuzzy.
func (c *sourceChain) primary() *autoTagRun {
	if len(c.all) == 0 {
		return nil
	}
	return c.all[0]
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

// catalogLanguage resolves the concrete language slug to narrow a search by, or ""
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

// artistCatalogQuery is the artist-tag catalog search, optionally language-narrowed.
func artistCatalogQuery(artist, lang string) source.SearchQuery {
	return source.SearchQuery{Artist: artist, Language: lang}
}

// artistTagVariants returns alternate spellings of an artist name to try when the exact
// tag yields nothing, since a folder name and the site's tag can differ in punctuation:
// some symbols are spelled as words ("50% OFF" -> "50 percent off") and otherwise
// punctuation collapses to spaces. The exact form is omitted (already tried), and an
// empty/identical variant is skipped, so a clean name like "ayana rio" yields none.
func artistTagVariants(artist string) []string {
	var out []string
	seen := map[string]bool{artist: true}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(autotag.Normalize(strings.ReplaceAll(artist, "%", " percent ")))
	add(autotag.Normalize(artist))
	return out
}

// catalog fetches an artist-catalog query, paging through every result page (capped at
// maxCatalogPages) and caching the complete set so sibling titles reuse it with no further
// network. A cached complete result is returned as-is. The bool reports the catalog had more
// pages than the cap (a deeper work may be unseen).
func (r *autoTagRun) catalog(ctx context.Context, query source.SearchQuery) ([]source.SearchResult, bool, error) {
	// Page-less key: this entry stands for the *whole* catalog, not one page of it.
	key := query.CacheKey()
	if cs := r.searchCache[key]; cs != nil && cs.complete {
		return cs.results, cs.truncated, nil
	}
	seen := map[string]bool{}
	var acc []source.SearchResult
	truncated := false
	for page := 1; page <= maxCatalogPages; page++ {
		query.Page = page
		resp, err := r.client.Search(ctx, query)
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
func (r *autoTagRun) searchPage(ctx context.Context, query source.SearchQuery) ([]source.SearchResult, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	// The "#<page>" suffix is unconditional, so a single-page fetch can never be served
	// back through catalog's page-less key as if it were a complete catalog.
	key := query.PageCacheKey()
	if cs := r.searchCache[key]; cs != nil {
		return cs.results, nil
	}
	resp, err := r.client.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	r.searchCache[key] = &cachedSearch{results: resp.Result}
	return resp.Result, nil
}

// detail fetches a gallery's full detail, cached by id (so a resync or an overlapping merge
// set doesn't refetch the same gallery).
func (r *autoTagRun) detail(ctx context.Context, id string) (*source.GalleryDetail, error) {
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

// searchRequests orders the searches to try. The primary title by free-text goes first —
// for most titles the site matches it directly, and the caller verifies the artist on the
// results. Then come the artist *tag* queries (narrowed by the first distinctive title
// word, then the bare catalog): free-text only indexes a gallery's primary
// (romaji/japanese) title, so a local name taken from the english subtitle finds nothing,
// and the artist tag is the reliable fallback. The artist queries deliberately precede the
// *remaining* title variants so they stay within the per-title query budget even when a
// title has several variants. Anchor free-text is the last resort (e.g. a circle with no
// artist tag). When lang is set the *title/anchor* free-text queries are language-narrowed,
// but the artist-tag queries are NOT — the tag is constraint enough, and a language filter
// would only hide an artist whose works are in another language. De-duplicated; page 1.
func searchRequests(artist string, variants, anchors []string, lang string) []source.SearchQuery {
	var reqs []source.SearchQuery
	seen := map[string]bool{}
	// add appends one query, de-duplicated case-insensitively on its full identity — the
	// language included, since two rungs differing only by the filter are two searches.
	// The empty check runs BEFORE the language is applied: a bare language filter is not a
	// search, it matches a site's whole catalog.
	add := func(q source.SearchQuery, filtered bool) {
		q.Title = strings.TrimSpace(q.Title)
		q.Artist = strings.TrimSpace(q.Artist)
		if q.Empty() {
			return
		}
		if filtered {
			q.Language = lang
		}
		q.Page = 1
		if k := q.CacheKey(); !seen[k] {
			seen[k] = true
			reqs = append(reqs, q)
		}
	}
	if len(variants) > 0 {
		add(source.SearchQuery{Title: variants[0]}, true)
	}
	if artist != "" {
		if len(variants) > 0 {
			if w := firstTitleWord(variants[0]); w != "" {
				add(source.SearchQuery{Artist: artist, Title: w}, false)
			}
		}
		add(source.SearchQuery{Artist: artist}, false)
	}
	for i := 1; i < len(variants); i++ {
		add(source.SearchQuery{Title: variants[i]}, true)
	}
	for _, a := range anchors {
		add(source.SearchQuery{Title: a}, true)
	}
	return reqs
}

// isArtistQuery reports whether a search query is constrained to the *local* artist, so
// its results are that artist's by construction — letting the title-first ladder flag
// artist matches even for galleries whose title omits the artist. Note it compares against
// the local artist rather than merely asking "is any artist set": that equivalence holds
// only while searchRequests is the sole producer of these queries, and collapsing it would
// pre-break the multi-source routing in roadmap 2.2.
func isArtistQuery(q source.SearchQuery, artist string) bool {
	if artist == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(q.Artist), artist)
}

// firstTitleWord returns the first distinctive word of a title, to narrow an artist-
// constrained search: the first normalized token of length >= 2 (so one-letter stopwords
// like "a"/"i" are skipped in favour of a word that actually narrows). "" when none.
func firstTitleWord(s string) string {
	for _, w := range strings.Fields(autotag.Normalize(s)) {
		if len(w) >= 2 {
			return w
		}
	}
	return ""
}

// gatherCandidates finds and scores candidates for one title through the run cache. For a
// prolific local artist (>=2 titles in this run) it pages the artist's whole catalog once —
// cached, so every sibling reuses it with no further network — and matches against that;
// only a catalog miss falls through to a title free-text fallback. For a single-title or
// unknown artist it runs the cheaper title-first ladder (bounded by maxSearchQueries),
// stopping early on a confident match. Either way, every result that came from an artist:"…"
// query is recorded as the artist's (artistIDs) so it is recognized as a match even when its
// title omits the artist. The bool reports a truncated artist catalog.
func (a *App) gatherCandidates(ctx context.Context, run *autoTagRun, mi matchInput, pages int, localLang string) ([]autotag.Candidate, bool, error) {
	lang := catalogLanguage(run.langMode, localLang)
	seen := map[string]bool{}
	artistIDs := map[string]bool{} // ids from any artist:"…" query — artist-matched by construction
	var acc []source.SearchResult
	add := func(rs []source.SearchResult) {
		for _, r := range rs {
			if !seen[r.ID] {
				seen[r.ID] = true
				acc = append(acc, r)
			}
		}
	}
	// A candidate is the local artist's when it came back from an artist:"…" query (the site
	// asserts the tag) or when its own decorated title parses to the same artist (a work
	// mis-tagged under a different circle that the title-free-text search happened to find).
	artistMatch := func(r source.SearchResult) bool {
		return artistIDs[r.ID] || candidateArtistMatches(r, mi.artist)
	}
	score := func() []autotag.Candidate {
		return autotag.ScoreAll(mi.variants, pages, localLang, acc, candLangResolver, artistMatch)
	}
	// Per-title diagnostic: each query and how many results it returned, so a "no artist
	// matches" review can be traced to the exact query that came back empty.
	run.trace = run.trace[:0]
	note := func(q string, n int) { run.trace = append(run.trace, fmt.Sprintf("%s→%d", q, n)) }

	truncated := false
	catalogFirst := mi.artist != "" && run.artistCount[mi.artist] >= 2
	if catalogFirst {
		// Catalog attempts, in order, stopping at the first non-empty. Language-narrowed
		// first: an all-language catalog for a prolific artist can exceed the page cap and
		// truncate away the match, so narrowing keeps it in range. Then all-languages, so a
		// Japanese-only artist isn't missed under a forced-english run. Within each, the exact
		// tag then punctuation variants ("50% OFF" -> "50 percent off"). Language only narrows
		// the fetch; it still ranks, and the chosen catalog's works are all artist-matched.
		forms := append([]string{mi.artist}, artistTagVariants(mi.artist)...)
		var attempts []source.SearchQuery
		attemptSeen := map[string]bool{}
		addAttempt := func(q source.SearchQuery) {
			if k := q.CacheKey(); !attemptSeen[k] {
				attemptSeen[k] = true
				attempts = append(attempts, q)
			}
		}
		if lang != "" {
			for _, f := range forms {
				addAttempt(artistCatalogQuery(f, lang))
			}
		}
		for _, f := range forms {
			addAttempt(artistCatalogQuery(f, ""))
		}
		var results []source.SearchResult
		for _, q := range attempts {
			res, trunc, cerr := run.catalog(ctx, q)
			if cerr != nil {
				return nil, false, cerr
			}
			note(q.String(), len(res))
			if len(res) > 0 {
				results, truncated = res, trunc
				break
			}
		}
		for _, r := range results {
			artistIDs[r.ID] = true
		}
		add(results)
		if confidentMatch(score(), mi.artist) {
			return score(), truncated, nil
		}
	}

	// Per-title searches. After a catalog page-through the artist tag is exhausted, so the
	// only useful fallback is the title by free-text (a work mis-tagged under a different
	// artist on the site); otherwise run the full title-first ladder.
	var reqs []source.SearchQuery
	if catalogFirst {
		for _, v := range mi.variants {
			if v = strings.TrimSpace(v); v != "" {
				reqs = append(reqs, source.SearchQuery{Title: v, Language: lang, Page: 1})
			}
		}
	} else {
		reqs = searchRequests(mi.artist, mi.variants, mi.anchors, lang)
	}
	for i, rq := range reqs {
		if i >= maxSearchQueries {
			break
		}
		results, err := run.searchPage(ctx, rq)
		if err != nil {
			return nil, false, err
		}
		note(rq.String(), len(results))
		if isArtistQuery(rq, mi.artist) {
			for _, r := range results {
				artistIDs[r.ID] = true
			}
		}
		add(results)
		if confidentMatch(score(), mi.artist) {
			break
		}
	}
	return score(), truncated, nil
}

// chainReview is one provider's non-confident result, kept so a review shortlist can pool
// candidates from every source that found something.
type chainReview struct {
	run       *autoTagRun
	dec       autotag.Decision
	truncated bool
}

// chainMatch is the chain's verdict on one title: which run produced it and what it found.
//
// shortcut is non-nil when a folder-name gallery ref resolved directly, in which case dec
// is unset — the ref is authoritative and never went through scoring. Otherwise dec holds
// the winning provider's decision, and reviews holds every provider that returned
// candidates without clearing the auto bar, in chain order.
//
// On an auto-apply, reviews is empty and dec is the single winning source's — applying
// never spans providers. On a review, dec is the first contributing provider's (it decides
// the outcome and the primary source) while reviews carries them all for the shortlist.
type chainMatch struct {
	run       *autoTagRun
	shortcut  *source.GalleryDetail
	dec       autotag.Decision
	reviews   []chainReview
	truncated bool
	trace     string // per-title query trace across every provider consulted
}

// matchThroughChain resolves one title against the provider chain and returns the verdict
// to act on, plus the trace of every provider consulted.
//
// Two phases. First the folder-id shortcut: a "<slug>-<ref>" in the folder name is an
// exact pointer, so it routes to *that* provider — even when another source is active —
// and skips searching entirely. That is strictly cheaper than what it replaces, one exact
// fetch instead of a doomed multi-query fuzzy search, and it is the only way an id-only
// source (hitomi) can match at all.
//
// Then the fuzzy chain, in priority order, advancing on anything short of an auto-apply —
// including a provider that returned nothing at all.
//
// An **auto-apply never spans providers**: the first source to clear the bar wins outright
// and its decision is used whole. That is not squeamishness. gatherCandidates dedupes by
// bare gallery id with no provider namespace, and applyTags stamps a single slug for a
// whole merge set, so a merge set drawn from two sites would silently drop colliding ids
// and mis-record where the tags came from.
//
// A **review pools across providers**: every source that found candidates contributes to
// the shortlist (see pooledReviewCandidates), because nothing is being applied yet — the
// user is choosing, and hiding the other sources' candidates just to keep one slug per
// result would be withholding the answer. dec/run stay the first contributing provider's:
// they decide the outcome and the primary source, and chain order is the only honest
// ranking between providers whose scores are not comparable.
func (a *App) matchThroughChain(ctx context.Context, chain *sourceChain, mi matchInput, pages int, localLang string) (chainMatch, error) {
	// Phase 1: an exact ref in the folder name, routed to its own provider.
	if mi.sourceRef != "" {
		if run := chain.bySlug[mi.sourceSlug]; run != nil {
			if d, err := run.detail(ctx, mi.sourceRef); err == nil {
				return chainMatch{run: run, shortcut: d}, nil
			} else if ctx.Err() != nil {
				return chainMatch{}, ctx.Err()
			}
			// A stale or bad ref (404) falls through to the fuzzy chain below.
		}
	}

	// Phase 2: the fuzzy chain.
	var reviews []chainReview
	var traces []string
	for _, run := range chain.fuzzy {
		scored, truncated, err := a.gatherCandidates(ctx, run, mi, pages, localLang)
		if err != nil {
			return chainMatch{}, err
		}
		traces = append(traces, run.slug+": "+strings.Join(run.trace, "  "))
		dec := autotag.Decide(scored)
		if dec.Action == autotag.ActionAuto {
			// A confident match ends the chain and is applied alone — see the doc comment
			// on why an apply never spans providers.
			return chainMatch{
				run: run, dec: dec, truncated: truncated,
				trace: strings.Join(traces, "  |  "),
			}, nil
		}
		if len(dec.Ranked) > 0 {
			reviews = append(reviews, chainReview{run: run, dec: dec, truncated: truncated})
		}
	}

	cm := chainMatch{reviews: reviews, trace: strings.Join(traces, "  |  ")}
	if len(reviews) > 0 {
		// The first contributing provider decides the outcome and the primary source; the
		// rest still contribute candidates to the shortlist.
		cm.run, cm.dec, cm.truncated = reviews[0].run, reviews[0].dec, reviews[0].truncated
		// Any source hitting its catalog cap means a deeper match may exist somewhere.
		for _, r := range reviews {
			if r.truncated {
				cm.truncated = true
			}
		}
		return cm, nil
	}
	// Nothing anywhere: report against the primary so the outcome still names a source.
	cm.run = chain.primary()
	return cm, nil
}

// confidentMatch reports whether some scored candidate is safe enough to stop searching:
// pages within tolerance and a full-enough title (autotag's auto-apply bar) AND — when
// the local artist is known — the candidate is by that artist (the ArtistMatch the scorer
// already resolved). That artist guard is what lets the title-only search run first
// without a same-titled work by a different artist ending the search before the
// artist-narrowed query gets its turn. Note it keeps the page gate even though Decide no
// longer requires it: a loose page match shouldn't end the search prematurely.
func confidentMatch(cands []autotag.Candidate, localArtist string) bool {
	for i := range cands {
		c := cands[i]
		if !c.PagesClose || c.TitleScore < strongTitleScore {
			continue
		}
		if localArtist == "" || c.ArtistMatch {
			return true
		}
	}
	return false
}

// candidateArtistMatches reports whether a search result's title-parsed artist equals the
// local artist (case-insensitive). It mirrors markOverlap's title parse, so it works on
// bare search results before any detail fetch.
func candidateArtistMatches(r source.SearchResult, localArtist string) bool {
	if localArtist == "" {
		return false
	}
	return strings.EqualFold(doujin.ParseName(r.EnglishTitle).Author(), localArtist)
}

// errNoAPIKey is surfaced to the UI when no key is configured.
var errNoAPIKey = errors.New("no nhentai API key set — add one in Settings")

// Settings is the safe, maskable view of API-related config. No key is ever returned —
// only whether one is set. ActiveSource/Label/Ready describe the currently-selected
// metadata source so the UI can gate the fetch/sweep features and label them per source
// (not just "nhentai"). HasNhentaiKey/NhentaiUserAgent are kept for the legacy key input.
type Settings struct {
	HasNhentaiKey     bool   `json:"has_nhentai_key"`
	NhentaiUserAgent  string `json:"nhentai_user_agent"`
	ActiveSource      string `json:"active_source"`
	ActiveSourceLabel string `json:"active_source_label"`
	ActiveSourceReady bool   `json:"active_source_ready"`
}

// GetSettings reports the configured-source state without revealing any key: the legacy
// nhentai key presence + User-Agent, plus which source is active, its human label, and
// whether it can actually build a client (nhentai needs a key; MangaDex is always ready).
func (a *App) GetSettings() (Settings, error) {
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return Settings{}, err
	}
	s := Settings{
		HasNhentaiKey:    strings.TrimSpace(cfg.NhentaiAPIKey) != "",
		NhentaiUserAgent: cfg.NhentaiUserAgent,
	}
	if sc, ok := cfg.ActiveSourceConfig(); ok {
		s.ActiveSource = sc.Provider
		for _, p := range providerPresets {
			if p.Slug == sc.Provider {
				s.ActiveSourceLabel = p.Label
			}
		}
		if _, err := buildProvider(sc); err == nil {
			s.ActiveSourceReady = true
		}
	}
	return s, nil
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

// SourceCandidate is one ranked match shown to the UI. MediaID/Thumbnail build the cover
// image; GalleryURL opens the gallery in the browser. Language/LangMatch and
// ArtistMatch/ParodyMatch drive the why-match badges. Tags is populated only for
// detail-fetched candidates (the merge set or the top few); it is nil otherwise to
// avoid a detail fetch per candidate. GalleryID is the provider's string id.
// SourceSlug/SourceLabel name the provider this candidate came from. A review shortlist
// can pool candidates from several sources, so provenance has to ride per candidate and
// not just per result: a gallery ref is only meaningful to the site that issued it, and
// two sites can use the same numeric id.
type SourceCandidate struct {
	SourceSlug    string      `json:"source_slug"`
	SourceLabel   string      `json:"source_label"`
	GalleryID     string      `json:"gallery_id"`
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
//
// SourceSlug/SourceLabel record which provider produced Candidates. Every candidate in one
// result comes from exactly one provider, and a gallery ref only means anything to the
// provider that issued it — so applying must go back to *this* source, not to whichever
// one happens to be active when the user clicks (see ApplySourceMerge).
type MatchResult struct {
	MangaID         int64             `json:"manga_id"`
	LocalTitle      string            `json:"local_title"`
	LocalAuthor     string            `json:"local_author"`
	LocalPages      int               `json:"local_pages"`
	LocalLanguage   string            `json:"local_language"`
	LocalTags       []tag.Typed       `json:"local_tags"`
	FolderPath      string            `json:"folder_path"`
	CoverRelPath    *string           `json:"cover_rel_path"`
	Decision        string            `json:"decision"`
	SourceSlug      string            `json:"source_slug"`
	SourceLabel     string            `json:"source_label"`
	MergeGalleryIDs []string          `json:"merge_gallery_ids"`
	Candidates      []SourceCandidate `json:"candidates"`
}

// MatchSource matches one title through the provider chain, ranks the results, and returns
// the auto/review decision plus a shortlist. The top previewCount candidates are
// detail-fetched so the UI can show their would-be tags. This makes several rate-limited
// requests, so it can take a few seconds.
//
// It walks the same chain as the bulk sweep so the interactive path and a sweep cannot
// disagree about which source wins a title — with fallback on, since a deliberate
// single-title fetch is the case where trying harder is most clearly wanted.
func (a *App) MatchSource(id int64) (*MatchResult, error) {
	m, err := search.GetManga(a.db, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("manga %d not found", id)
	}
	providers, err := a.chainProviders()
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
	chain := newSourceChain(providers, "auto", nil, true)

	cm, err := a.matchThroughChain(a.ctx, chain, mi, m.PageCount, localLang)
	if err != nil {
		return nil, err
	}
	// An exact gallery ref from the folder name is authoritative — present it as a
	// confident match without scoring it.
	if cm.shortcut != nil {
		localTags, _ := search.GetMangaTagsTyped(a.db, id)
		return &MatchResult{
			MangaID:         id,
			LocalTitle:      m.Title,
			LocalAuthor:     m.AuthorName,
			LocalPages:      m.PageCount,
			LocalLanguage:   localLang,
			LocalTags:       localTags,
			FolderPath:      m.FolderPath,
			CoverRelPath:    m.CoverRelPath,
			Decision:        string(autotag.ActionAuto),
			SourceSlug:      cm.run.slug,
			SourceLabel:     providerLabel(cm.run.slug),
			MergeGalleryIDs: []string{cm.shortcut.ID},
			Candidates:      []SourceCandidate{galleryIDCandidate(cm.shortcut, m.PageCount, mi)},
		}, nil
	}
	run, dec := cm.run, cm.dec
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
		SourceSlug:      run.slug,
		SourceLabel:     providerLabel(run.slug),
		MergeGalleryIDs: applyGalleryIDs(dec.Apply),
		Candidates:      []SourceCandidate{},
	}
	if len(dec.Ranked) == 0 {
		res.Decision = "none"
		return res, nil
	}
	// On review, pool every source's artist-narrowed works (closest first within each,
	// capped); on auto, keep the winning source's full ranked list so the merge set stays
	// visible for the one-click preview.
	if dec.Action == autotag.ActionAuto {
		res.Candidates = shortlist(dec.Ranked, shortlistMax, mi, run.slug)
	} else {
		res.Candidates = pooledReviewCandidates(cm.reviews, mi)
	}

	// Detail-fetch the candidates worth previewing: on auto, the merge set (so the UI
	// previews the union it will apply); on review, the top few. Each fetch refines the
	// candidate's tags + artist/parody overlap with authoritative detail data.
	//
	// Selection is by INDEX, not gallery id: a pooled shortlist can hold the same numeric
	// id from two different sites, and each candidate must be fetched from its own
	// provider's run.
	fetch := make([]bool, len(res.Candidates))
	if dec.Action == autotag.ActionAuto {
		merge := map[string]bool{}
		for _, gid := range res.MergeGalleryIDs {
			merge[gid] = true
		}
		for i := range res.Candidates {
			fetch[i] = merge[res.Candidates[i].GalleryID]
		}
	} else {
		for i := 0; i < len(res.Candidates) && i < previewCount; i++ {
			fetch[i] = true
		}
	}
	for i := range res.Candidates {
		if !fetch[i] {
			continue
		}
		cRun := chain.bySlug[res.Candidates[i].SourceSlug]
		if cRun == nil {
			continue
		}
		if d, derr := cRun.detail(a.ctx, res.Candidates[i].GalleryID); derr == nil {
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

// ApplySourceTags applies one explicitly chosen gallery (a manual pick in the review
// list). It fetches the gallery, unions its tags with the title's existing tags
// (preserving the local language), records the link, and returns the saved tag set.
// slug names the provider the gallery ref belongs to (MatchResult.SourceSlug); "" means
// the active source.
func (a *App) ApplySourceTags(mangaID int64, slug, galleryID string) ([]tag.Typed, error) {
	return a.ApplySourceMerge(mangaID, slug, []string{galleryID})
}

// ApplySourceMerge applies a set of galleries at once — the variants of one work that
// the matcher merged. It fetches each, unions their tags (preserving manual tags + the
// local language), stamps the primary (galleryIDs[0]) as the link, and returns the
// saved, subject-ordered tag set so the UI can re-render its grouped chips.
//
// slug is the provider the refs came from (MatchResult.SourceSlug), NOT the active source.
// A gallery ref is only meaningful to the provider that issued it: a review queue can
// outlive a source switch, and two sites can use the same numeric id, so resolving against
// whatever is active risks fetching an unrelated gallery and stamping the wrong
// source_slug. An empty slug falls back to the active provider, which keeps the method
// usable from callers that have no provenance to hand.
func (a *App) ApplySourceMerge(mangaID int64, slug string, galleryIDs []string) ([]tag.Typed, error) {
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
	client, err := a.providerBySlug(slug)
	if err != nil {
		return nil, err
	}
	galleries := make([]*source.GalleryDetail, 0, len(galleryIDs))
	for _, gid := range galleryIDs {
		d, err := client.GalleryByID(a.ctx, gid)
		if err != nil {
			return nil, err
		}
		galleries = append(galleries, d)
	}
	return a.applyTags(mangaID, client.Slug(), galleryIDs[0], galleries)
}

// applyTags merges the galleries' subjected tags into the manga's existing tags
// (preserving manual tags), persists the union, and stamps the source link with the
// primary gallery ref. Tags union by name; where a name is new it brings its provider
// subject, where it already exists the existing row keeps (or is upgraded to) the right
// subject via SetMangaTags → GetOrCreateTag.
//
// Language is preserved, never changed: if the title already has a language tag, all
// gallery language tags are dropped; otherwise only the primary gallery's single
// language is adopted (so merging a Japanese + English variant can't add two languages).
func (a *App) applyTags(mangaID int64, slug, primaryRef string, galleries []*source.GalleryDetail) ([]tag.Typed, error) {
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
	// provider subject; existing tags then fill in the rest, keeping the local language.
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
	if err := a.stampSourceLink(mangaID, slug, primaryRef); err != nil {
		return nil, err
	}
	return saved, nil
}

// stampSourceLink records which provider gallery a title's tags came from. It writes the
// provider-neutral source_slug/source_ref pair, and — for nhentai — the legacy
// nhentai_gallery_id integer column too, so older UI paths and re-sync keep working.
func (a *App) stampSourceLink(mangaID int64, slug, ref string) error {
	if _, err := a.db.Exec("UPDATE manga SET source_slug=?, source_ref=? WHERE id=?", slug, ref, mangaID); err != nil {
		return err
	}
	if slug == nhentai.Slug {
		if gid, err := strconv.ParseInt(ref, 10, 64); err == nil {
			if _, err := a.db.Exec("UPDATE manga SET nhentai_gallery_id=? WHERE id=?", gid, mangaID); err != nil {
				return err
			}
		}
	}
	return nil
}

// galleryTypedTags maps all of a gallery's tags to normalized, de-duplicated tags
// carrying their subject. The provider already mapped each tag onto our subject
// vocabulary (see internal/tag); here the names are normalized, de-duplicated by name,
// and sorted by subject then name.
func galleryTypedTags(d *source.GalleryDetail) []tag.Typed {
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

func toCandidate(c autotag.Candidate) SourceCandidate {
	return SourceCandidate{
		GalleryID:     c.Gallery.ID,
		MediaID:       c.Gallery.MediaID,
		Thumbnail:     c.Gallery.Thumbnail,
		GalleryURL:    c.Gallery.GalleryURL,
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
		ArtistMatch:   c.ArtistMatch,
	}
}

// galleryIDCandidate builds the single UI candidate for a title whose folder name
// carried an exact gallery id (e.g. "nhentai-271687 - …"). The fetched detail is
// authoritative, so it is presented as a confident match with its tags already populated
// and its cover wired (MediaID). Page badges are computed against the local count;
// artist/parody overlap is marked from the detail's own typed tags.
func galleryIDCandidate(d *source.GalleryDetail, localPages int, mi matchInput) SourceCandidate {
	c := SourceCandidate{
		GalleryID:     d.ID,
		MediaID:       d.MediaID,
		GalleryURL:    d.GalleryURL,
		EnglishTitle:  d.EnglishTitle,
		JapaneseTitle: d.JapaneseTitle,
		NumPages:      d.NumPages,
		Score:         1,
		TitleScore:    1,
		PageDelta:     -1,
		Tags:          galleryTypedTags(d),
	}
	if localPages > 0 && d.NumPages > 0 {
		delta := localPages - d.NumPages
		if delta < 0 {
			delta = -delta
		}
		c.PageDelta = delta
		c.PagesExact = delta == 0
	}
	markOverlap(&c, mi.artist, mi.parodies, d)
	return c
}

// reviewPool narrows the ranked candidates for a manual-review item to the local artist's
// own works when any are present, so a same-titled work by a *different* artist (pulled in
// by the title free-text fallback) doesn't clutter the picker. It falls back to the full
// ranked list when no candidate is artist-matched (a hybrid author name whose catalog came
// back empty), and preserves Decide's best-first order so a later cap keeps the closest.
func reviewPool(ranked []autotag.Candidate) []autotag.Candidate {
	var artistOnly []autotag.Candidate
	for _, c := range ranked {
		if c.ArtistMatch {
			artistOnly = append(artistOnly, c)
		}
	}
	if len(artistOnly) > 0 {
		return artistOnly
	}
	return ranked
}

// shortlist turns the top n ranked candidates into UI candidates, flagging each whose
// artist/parody overlaps the local title (from the candidate's own title decorations —
// no detail fetch, so it works in the bulk sweep).
func shortlist(ranked []autotag.Candidate, n int, mi matchInput, slug string) []SourceCandidate {
	n = min(n, len(ranked))
	out := make([]SourceCandidate, 0, n)
	for i := range n {
		c := toCandidate(ranked[i])
		c.SourceSlug, c.SourceLabel = slug, providerLabel(slug)
		markOverlap(&c, mi.artist, mi.parodies, nil)
		out = append(out, c)
	}
	return out
}

// pooledReviewMax caps a pooled review shortlist. Each contributing source offers up to
// reviewMax of its own (so a single-source review is unchanged), but a three-provider chain
// dumping thirty rows onto one card helps nobody. Trimming happens at the tail, which
// respects chain priority.
const pooledReviewMax = 16

// pooledReviewCandidates builds the review shortlist from every source that found
// something, grouped by provider in chain order and ranked within each group.
//
// It deliberately does NOT interleave the groups by score. Scores are not comparable across
// providers — MangaDex reports NumPages 0 for every series, so its candidates can never earn
// the page bonus — and a merged sort would bury them under a doujin site's every time. Chain
// order is an honest ordering; a cross-provider score ranking would be a fiction.
func pooledReviewCandidates(reviews []chainReview, mi matchInput) []SourceCandidate {
	var out []SourceCandidate
	for _, r := range reviews {
		out = append(out, shortlist(reviewPool(r.dec.Ranked), reviewMax, mi, r.run.slug)...)
	}
	if len(out) > pooledReviewMax {
		out = out[:pooledReviewMax]
	}
	return out
}

// applyGalleryIDs is the gallery ids of a merge set, in primary-first order.
func applyGalleryIDs(cands []autotag.Candidate) []string {
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.Gallery.ID)
	}
	return ids
}

// markOverlap flags a candidate whose artist/parody matches the local title's — a
// strong corroborating signal for review. It reads the candidate's own title
// decorations and, when a detail is supplied, its authoritative artist/parody tags. It
// only ever sets a flag true, so a title-only pass can be refined by a later detail pass.
func markOverlap(c *SourceCandidate, localArtist string, localParodies map[string]bool, detail *source.GalleryDetail) {
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
		switch t.Type {
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
// the title free-text searches by language ("auto" follows each title's local language,
// assuming all languages when untagged; "english"/"japanese" force that language) and ranks
// same-language matches higher. For an artist catalog the narrowed query is tried first
// (keeping a prolific artist under the page cap) but falls back to all languages when it is
// empty, so an artist's own works are never hidden — only fetched in a smaller batch first.
//
// Fallback walks the other enabled sources when one falls short of an auto-apply, instead
// of consulting only the active source. It costs one extra pass per provider on every
// title that does not match confidently, which is why it is a per-sweep choice rather than
// a setting. NOTE it has no "unset" state: a bare {} from a caller that predates the field
// decodes to false, so the frontend sends it explicitly.
type AutoTagOptions struct {
	Resync       bool   `json:"resync"`
	LanguageMode string `json:"language_mode"`
	Fallback     bool   `json:"fallback"`
}

// AutoTagProgress is emitted as "autotag:progress" once per processed title. Source names
// the provider that produced the outcome — with a chain in play, "applied" alone no longer
// says which site the tags came from.
type AutoTagProgress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	MangaID int64  `json:"manga_id"`
	Title   string `json:"title"`
	Outcome string `json:"outcome"` // "applied" | "review" | "none" | "error"
	Source  string `json:"source"`
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
	// The active provider failing to build is still a hard error even with fallback on:
	// sweeping quietly with the other sources would hide the misconfiguration the user
	// needs to fix.
	providers, err := a.chainProviders()
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
	chain := newSourceChain(providers, opts.LanguageMode, artistCount, opts.Fallback)

	go a.runAutoTag(ctx, chain, targets)
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
// cannot iterate a cursor and write tag updates at the same time). Titles already linked
// to any source (source_ref set) are skipped unless resync is requested.
func (a *App) autotagTargets(resync bool) ([]autotagTarget, error) {
	q := "SELECT m.id, m.title, m.page_count, m.folder_path, m.cover_rel_path, a.name " +
		"FROM manga m JOIN authors a ON a.id = m.author_id"
	if !resync {
		q += " WHERE m.source_ref IS NULL"
	}
	// Group by artist so each artist's titles process consecutively (warming the catalog
	// cache) and arrive in the review queue already grouped.
	q += " ORDER BY a.name, m.title"
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

// runAutoTag is the background loop. For each title it matches through the provider chain,
// decides, and either auto-applies (fetching the chosen gallery) or queues the title for
// review. A cancelled context ends the loop and emits a final cancelled event.
func (a *App) runAutoTag(ctx context.Context, chain *sourceChain, targets []autotagTarget) {
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

		cm, err := a.matchThroughChain(ctx, chain, mi, t.pages, localLang)
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
		run, dec, truncated := cm.run, cm.dec, cm.truncated
		if run != nil {
			prog.Source = providerLabel(run.slug)
		}

		// An exact gallery ref in the folder name is authoritative — apply it without
		// scoring, which is both cheaper and exact.
		if cm.shortcut != nil {
			d := cm.shortcut
			if _, aerr := a.applyTags(t.id, run.slug, d.ID, []*source.GalleryDetail{d}); aerr != nil {
				prog.Outcome, prog.Detail = "error", aerr.Error()
				a.emit(prog)
				continue
			}
			done.Applied++
			prog.Outcome = "applied"
			prog.Detail = fmt.Sprintf("gallery #%s (from name): %s", d.ID, d.EnglishTitle)
			a.emit(prog)
			continue
		}

		if len(dec.Ranked) == 0 {
			prog.Outcome = "none"
			prog.Detail = reviewDetail(cm.trace, truncated)
			a.emit(prog)
			continue
		}

		if dec.Action == autotag.ActionAuto {
			// Fetch the whole merge set (the variants of this work) and union their tags.
			galleries := make([]*source.GalleryDetail, 0, len(dec.Apply))
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
			if _, aerr := a.applyTags(t.id, run.slug, primary, galleries); aerr != nil {
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
			SourceSlug:    run.slug,
			SourceLabel:   providerLabel(run.slug),
			Candidates:    pooledReviewCandidates(cm.reviews, mi),
		})
		prog.Outcome = "review"
		prog.Detail = reviewDetail(cm.trace, truncated)
		a.emit(prog)
	}
	a.emitDone(done)
}

// reviewDetail builds the diagnostic detail for a review/none outcome: the per-title query
// trace (each query→result-count, one slug-prefixed section per provider consulted) plus
// the truncation note when the catalog hit its cap. It is what surfaces, in the sweep log,
// exactly which query came back empty for an artist that "isn't matching" — and, with a
// chain, which sources were tried before giving up.
func reviewDetail(trace string, truncated bool) string {
	d := trace
	if truncated {
		if d != "" {
			d += "  "
		}
		d += "[" + truncatedNote + "]"
	}
	return d
}

func (a *App) emit(p AutoTagProgress) { wailsruntime.EventsEmit(a.ctx, "autotag:progress", p) }
func (a *App) emitDone(d AutoTagDone) { wailsruntime.EventsEmit(a.ctx, "autotag:done", d) }
