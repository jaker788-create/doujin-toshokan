package main

// Tests for the provider chain (roadmap 2.2): cross-provider folder-id routing and ordered
// fallback. The sweep loop these exercise had no test coverage at all before this — the
// folder-id shortcut in particular was untested end to end — so everything here is new
// ground rather than an adjustment to existing expectations.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"doujin/internal/source"
)

// chainStub is a provider whose behaviour a chain test dictates: which results its searches
// return (or none at all), and whether detail fetches fail. Every call is recorded so a test
// can assert a provider was consulted — or, just as importantly, was not.
type chainStub struct {
	slug        string
	results     []source.SearchResult
	detailErr   bool
	searchCalls int
	detailCalls []string
}

func (c *chainStub) Slug() string { return c.slug }

func (c *chainStub) Search(_ context.Context, _ source.SearchQuery) (*source.SearchResponse, error) {
	c.searchCalls++
	return &source.SearchResponse{Result: c.results, NumPages: 1}, nil
}

func (c *chainStub) GalleryByID(_ context.Context, id string) (*source.GalleryDetail, error) {
	c.detailCalls = append(c.detailCalls, id)
	if c.detailErr {
		return nil, errors.New("not found")
	}
	return &source.GalleryDetail{ID: id, EnglishTitle: c.slug + " gallery " + id}, nil
}

// buildChain assembles a chain from stubs in priority order, mirroring newSourceChain's
// contract (id-only members never enter the fuzzy list). It builds the runs directly
// because newSourceChain takes source.Provider while a run only ever uses the nhSearcher
// slice of it — TestNewSourceChainWiring covers the real constructor.
func buildChain(fallback bool, idOnly map[string]bool, stubs ...*chainStub) *sourceChain {
	ch := &sourceChain{bySlug: map[string]*autoTagRun{}}
	for i, s := range stubs {
		run := newAutoTagRun(s, "auto", nil)
		ch.all = append(ch.all, run)
		ch.bySlug[run.slug] = run
		if idOnly[s.slug] || (i > 0 && !fallback) {
			continue
		}
		ch.fuzzy = append(ch.fuzzy, run)
	}
	return ch
}

func chainInput(folder string) matchInput { return matchInputs(folder, "Some Title", "") }

// hit is a search result that scores as a confident auto-apply against a 20-page
// "Some Title" local entry.
func hit(id, title string, pages int) source.SearchResult {
	return source.SearchResult{ID: id, EnglishTitle: title, NumPages: pages}
}

// A gallery ref in the folder name routes to ITS provider even when another source is
// active — the gap decision 2.1 explicitly left open. Before this, a hitomi-prefixed folder
// swept under nhentai fell through to a fuzzy search that could not possibly match.
func TestChainRoutesFolderIDToItsOwnProvider(t *testing.T) {
	active := &chainStub{slug: "nhentai"}
	other := &chainStub{slug: "hitomi"}
	chain := buildChain(true, map[string]bool{"hitomi": true}, active, other)

	cm, err := (&App{}).matchThroughChain(context.Background(), chain,
		chainInput("/lib/hitomi-4056725 - [Circle] Some Title"), 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if cm.shortcut == nil {
		t.Fatal("expected the folder-id shortcut to resolve")
	}
	if cm.run.slug != "hitomi" {
		t.Errorf("routed to %q, want hitomi (the slug in the folder name)", cm.run.slug)
	}
	if got := other.detailCalls; len(got) != 1 || got[0] != "4056725" {
		t.Errorf("hitomi detail calls = %v, want [4056725]", got)
	}
	if active.searchCalls != 0 || len(active.detailCalls) != 0 {
		t.Errorf("the active source should not have been consulted at all, got %+v", active)
	}
}

// Routing is limited to chain members. A folder naming a source that is not enabled must
// fall through to the normal search rather than reaching for a provider we never built.
func TestChainDoesNotRouteToAnAbsentProvider(t *testing.T) {
	active := &chainStub{slug: "nhentai"}
	chain := buildChain(true, nil, active)

	cm, err := (&App{}).matchThroughChain(context.Background(), chain,
		chainInput("/lib/hitomi-4056725 - [Circle] Some Title"), 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if cm.shortcut != nil {
		t.Error("shortcut fired for a provider that is not in the chain")
	}
	if active.searchCalls == 0 {
		t.Error("expected a fall-through search on the active source")
	}
}

// A stale ref (the provider 404s) must not strand the title — it falls through to the fuzzy
// chain, exactly as the single-source shortcut always did.
func TestChainStaleFolderIDFallsThroughToSearch(t *testing.T) {
	active := &chainStub{slug: "nhentai", detailErr: true}
	chain := buildChain(true, nil, active)

	cm, err := (&App{}).matchThroughChain(context.Background(), chain,
		chainInput("/lib/nhentai-999 - [Circle] Some Title"), 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if cm.shortcut != nil {
		t.Error("a failed detail fetch must not count as a shortcut hit")
	}
	if active.searchCalls == 0 {
		t.Error("expected the fuzzy chain to run after the stale ref")
	}
}

// The headline fallback: the first source finds nothing, the second matches.
func TestChainFallsBackWhenFirstSourceFindsNothing(t *testing.T) {
	first := &chainStub{slug: "nhentai"} // no results
	second := &chainStub{slug: "mangadex", results: []source.SearchResult{hit("77", "Some Title", 20)}}
	chain := buildChain(true, nil, first, second)

	cm, err := (&App{}).matchThroughChain(context.Background(), chain,
		chainInput("/lib/[Circle] Some Title"), 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.searchCalls == 0 || second.searchCalls == 0 {
		t.Errorf("both sources should have been searched, got %d and %d", first.searchCalls, second.searchCalls)
	}
	if cm.run == nil || cm.run.slug != "mangadex" {
		t.Fatalf("winner = %v, want mangadex", cm.run)
	}
	if len(cm.dec.Ranked) == 0 {
		t.Error("expected the second source's candidates")
	}
	// The trace must name every source consulted — that is what explains, in the sweep log,
	// why the chain moved on.
	if !strings.Contains(cm.trace, "nhentai") || !strings.Contains(cm.trace, "mangadex") {
		t.Errorf("trace = %q, want both sources named", cm.trace)
	}
}

// With fallback off, only the active source is ever consulted.
func TestChainWithoutFallbackConsultsOneSource(t *testing.T) {
	first := &chainStub{slug: "nhentai"}
	second := &chainStub{slug: "mangadex", results: []source.SearchResult{hit("77", "Some Title", 20)}}
	chain := buildChain(false, nil, first, second)

	if _, err := (&App{}).matchThroughChain(context.Background(), chain,
		chainInput("/lib/[Circle] Some Title"), 20, ""); err != nil {
		t.Fatal(err)
	}
	if first.searchCalls == 0 {
		t.Error("the active source should still be searched")
	}
	if second.searchCalls != 0 {
		t.Errorf("fallback is off, so mangadex must not be searched (got %d calls)", second.searchCalls)
	}
}

// An id-only source has no free-text search by contract, so it must never be searched — a
// guaranteed-empty pass per title is pure cost. It stays eligible for id routing, which
// TestChainRoutesFolderIDToItsOwnProvider covers.
func TestChainSkipsIDOnlySourceInFuzzyPhase(t *testing.T) {
	first := &chainStub{slug: "nhentai"}
	idOnly := &chainStub{slug: "hitomi"}
	chain := buildChain(true, map[string]bool{"hitomi": true}, first, idOnly)

	if _, err := (&App{}).matchThroughChain(context.Background(), chain,
		chainInput("/lib/[Circle] Some Title"), 20, ""); err != nil {
		t.Fatal(err)
	}
	if idOnly.searchCalls != 0 {
		t.Errorf("an id-only source must not be searched, got %d calls", idOnly.searchCalls)
	}
}

// When two sources both fall short of an auto-apply, the EARLIER one wins. Picking by score
// would look smarter and be wrong: MangaDex reports NumPages 0 for every series, so its
// candidates can never earn the page bonus and would systematically lose the comparison.
func TestChainKeepsTheFirstReviewWhenNoneAutoApply(t *testing.T) {
	// Loosely related titles with far-off page counts and no artist to corroborate: enough
	// to rank, not enough for autotag.qualifies. (A near-identical title would auto-apply
	// on the strongTitleBar rule and end the chain, which is a different test.)
	first := &chainStub{slug: "nhentai", results: []source.SearchResult{hit("1", "Some Entirely Different Work", 99)}}
	second := &chainStub{slug: "mangadex", results: []source.SearchResult{hit("2", "Some Other Unrelated Book", 98)}}
	chain := buildChain(true, nil, first, second)

	cm, err := (&App{}).matchThroughChain(context.Background(), chain,
		chainInput("/lib/[Circle] Some Title"), 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if cm.run == nil {
		t.Fatal("expected a run to be reported")
	}
	if cm.run.slug != "nhentai" {
		t.Errorf("kept %q, want the first source's result", cm.run.slug)
	}
	// Both were still consulted — advancing on "review" is the whole point of the choice.
	if second.searchCalls == 0 {
		t.Error("the chain should still have tried the second source")
	}
}

// Nothing anywhere still reports a source, so the outcome can be attributed in the log.
func TestChainWithNoMatchAnywhereStillNamesASource(t *testing.T) {
	first := &chainStub{slug: "nhentai"}
	second := &chainStub{slug: "mangadex"}
	chain := buildChain(true, nil, first, second)

	cm, err := (&App{}).matchThroughChain(context.Background(), chain,
		chainInput("/lib/[Circle] Some Title"), 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cm.dec.Ranked) != 0 {
		t.Error("expected no candidates")
	}
	if cm.run == nil || cm.run.slug != "nhentai" {
		t.Errorf("run = %v, want the primary source reported", cm.run)
	}
}

// The reason the chain holds one run per provider: a run's searchCache is keyed by query and
// its detailCache by bare gallery id, both provider-scoped. Two sources using the SAME
// numeric id — nhentai and hitomi both use numbers — must not serve each other's galleries.
func TestChainCachesDoNotCollideAcrossProviders(t *testing.T) {
	a := &chainStub{slug: "nhentai"}
	b := &chainStub{slug: "hitomi"}
	chain := buildChain(true, nil, a, b)

	da, err := chain.bySlug["nhentai"].detail(context.Background(), "12345")
	if err != nil {
		t.Fatal(err)
	}
	db, err := chain.bySlug["hitomi"].detail(context.Background(), "12345")
	if err != nil {
		t.Fatal(err)
	}
	if da.EnglishTitle == db.EnglishTitle {
		t.Fatalf("both providers returned %q for id 12345 — the caches collided", da.EnglishTitle)
	}
	if len(a.detailCalls) != 1 || len(b.detailCalls) != 1 {
		t.Errorf("each provider should have been asked exactly once, got %d and %d",
			len(a.detailCalls), len(b.detailCalls))
	}
}

// newSourceChain's own wiring: priority order is preserved, id-only members are excluded
// from fuzzy but present for routing, and primary() is deterministic — it must not read
// from a map.
func TestNewSourceChainWiring(t *testing.T) {
	mk := func(slug string, idOnly bool) chainedProvider {
		return chainedProvider{provider: &chainProviderStub{chainStub{slug: slug}}, idOnly: idOnly}
	}
	ps := []chainedProvider{mk("nhentai", false), mk("hitomi", true), mk("mangadex", false)}

	ch := newSourceChain(ps, "auto", nil, true)
	if got := len(ch.fuzzy); got != 2 {
		t.Fatalf("fuzzy members = %d, want 2 (hitomi is id-only)", got)
	}
	if ch.fuzzy[0].slug != "nhentai" || ch.fuzzy[1].slug != "mangadex" {
		t.Errorf("fuzzy order = %q,%q", ch.fuzzy[0].slug, ch.fuzzy[1].slug)
	}
	if ch.bySlug["hitomi"] == nil {
		t.Error("an id-only source must still be routable by slug")
	}
	for range 20 { // primary must not depend on map iteration order
		if ch.primary().slug != "nhentai" {
			t.Fatalf("primary() = %q, want nhentai every time", ch.primary().slug)
		}
	}
	if got := len(newSourceChain(ps, "auto", nil, false).fuzzy); got != 1 {
		t.Errorf("fuzzy members without fallback = %d, want 1", got)
	}
}

// chainProviderStub adapts chainStub to the full source.Provider interface, which
// newSourceChain takes (the runs themselves only need the nhSearcher slice).
type chainProviderStub struct{ chainStub }

func (c *chainProviderStub) Label() string { return c.slug }
