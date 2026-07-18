package main

// This file is the provider registry: the small amount of glue that turns a stored
// SourceConfig into a live source.Provider, plus the bound methods the Settings UI uses
// to list/configure the available metadata sources and pick the active one. Adding a new
// site means adding one case to buildProvider and one entry to providerPresets — nothing
// above this layer changes.

import (
	"errors"
	"fmt"
	"strings"

	"doujin/internal/config"
	"doujin/internal/hitomi"
	"doujin/internal/mangadex"
	"doujin/internal/nhentai"
	"doujin/internal/source"
)

// errNoSource is surfaced to the UI when no metadata source is configured/selected.
var errNoSource = errors.New("no metadata source configured — add one in Settings")

// providerPreset describes a built-in source the user can enable. NeedsKey drives whether
// the Settings UI shows an API-key field (nhentai requires one; MangaDex does not).
//
// IDOnly marks a source with no free-text search (source.Provider allows it — see the
// package doc — and such a provider returns an empty Search). It exists purely so the UI
// can say so: without it, picking hitomi and running a bulk sweep would report "no match"
// on every title that has no id in its folder name, which looks like a broken app rather
// than the documented contract.
type providerPreset struct {
	Slug     string
	Label    string
	NeedsKey bool
	IDOnly   bool
}

// providerPresets is the registry of known sources, in display order.
var providerPresets = []providerPreset{
	{Slug: nhentai.Slug, Label: nhentai.Label, NeedsKey: true},
	{Slug: mangadex.Slug, Label: mangadex.Label, NeedsKey: false},
	{Slug: hitomi.Slug, Label: hitomi.Label, NeedsKey: false, IDOnly: true},
}

// providerLabel maps a slug to its human label for display, falling back to the slug
// itself for anything unregistered. The label is a presentation concern of the registry,
// which is why the matcher's nhSearcher interface carries Slug() but not Label().
func providerLabel(slug string) string {
	for _, p := range providerPresets {
		if p.Slug == slug {
			return p.Label
		}
	}
	return slug
}

// knownProvider reports whether slug names a built-in preset.
func knownProvider(slug string) bool {
	for _, p := range providerPresets {
		if p.Slug == slug {
			return true
		}
	}
	return false
}

// buildProvider constructs the concrete source.Provider for a SourceConfig, applying each
// provider's own auth requirement: nhentai needs an API key (errNoAPIKey without one),
// MangaDex and hitomi need none. An empty User-Agent falls back to the app default.
func buildProvider(sc config.SourceConfig) (source.Provider, error) {
	ua := strings.TrimSpace(sc.UserAgent)
	if ua == "" {
		ua = defaultUserAgent
	}
	switch sc.Provider {
	case nhentai.Slug:
		key := strings.TrimSpace(sc.APIKey)
		if key == "" {
			return nil, errNoAPIKey
		}
		return nhentai.NewClient(key, ua), nil
	case mangadex.Slug:
		return mangadex.NewClient(ua), nil
	case hitomi.Slug:
		// Empty BaseURL means the client's own default; the override exists so a data
		// domain move is recoverable from settings (see config.SourceConfig.BaseURL).
		return hitomi.NewClient(ua, strings.TrimSpace(sc.BaseURL)), nil
	default:
		return nil, fmt.Errorf("unknown source provider %q", sc.Provider)
	}
}

// activeProvider builds the provider for the currently-selected source in config. It is
// the single seam every tag-fetch path goes through, so switching sources is just a
// config change.
func (a *App) activeProvider() (source.Provider, error) {
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return nil, err
	}
	sc, ok := cfg.ActiveSourceConfig()
	if !ok {
		return nil, errNoSource
	}
	return buildProvider(sc)
}

// providerBySlug builds a specific configured provider by slug, falling back to the active
// source when slug is empty. Apply paths use it to go back to the provider a candidate
// actually came from: a gallery ref only means something to the site that issued it, so
// resolving one against whichever source happens to be active can fetch an unrelated
// gallery (two sites can use the same numeric id) and stamp the wrong source_slug.
//
// A slug that is not configured is an error rather than a silent fallback to the active
// source — that would be the exact mis-resolution this exists to prevent.
func (a *App) providerBySlug(slug string) (source.Provider, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return a.activeProvider()
	}
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return nil, err
	}
	for _, sc := range cfg.ResolveSources() {
		if sc.Provider == slug {
			return buildProvider(sc)
		}
	}
	// Not in the configured list, but a known preset: build it from its defaults so a
	// keyless source (mangadex, hitomi) still applies after, say, a config reset.
	if knownProvider(slug) {
		return buildProvider(config.SourceConfig{Provider: slug, Enabled: true})
	}
	return nil, fmt.Errorf("source %q is not configured", slug)
}

// SourceState is the maskable UI view of one configurable source: never the key itself,
// only whether one is set. Active marks the currently-selected source.
type SourceState struct {
	Slug      string `json:"slug"`
	Label     string `json:"label"`
	NeedsKey  bool   `json:"needs_key"`
	IDOnly    bool   `json:"id_only"`
	HasKey    bool   `json:"has_key"`
	Enabled   bool   `json:"enabled"`
	Active    bool   `json:"active"`
	UserAgent string `json:"user_agent"`
}

// GetSources returns every built-in source with its configured state (key masked) and
// which one is active, so the Settings page can render the source picker. Legacy configs
// (only nhentai_api_key set) surface as a configured, enabled nhentai source.
func (a *App) GetSources() ([]SourceState, error) {
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return nil, err
	}
	bySlug := map[string]config.SourceConfig{}
	for _, s := range cfg.ResolveSources() {
		bySlug[s.Provider] = s
	}
	active, hasActive := cfg.ActiveSourceConfig()
	out := make([]SourceState, 0, len(providerPresets))
	for _, p := range providerPresets {
		st := SourceState{Slug: p.Slug, Label: p.Label, NeedsKey: p.NeedsKey, IDOnly: p.IDOnly}
		if sc, ok := bySlug[p.Slug]; ok {
			st.HasKey = strings.TrimSpace(sc.APIKey) != ""
			st.Enabled = sc.Enabled
			st.UserAgent = sc.UserAgent
		}
		st.Active = hasActive && active.Provider == p.Slug
		out = append(out, st)
	}
	return out, nil
}

// SetSourceConfig upserts one source's credentials + enabled flag and persists it. It
// preserves any legacy nhentai entry (via ResolveSources) and, for nhentai, keeps the
// legacy flat fields in sync so an older build still reads the key. The first source
// enabled becomes active when none is selected yet.
func (a *App) SetSourceConfig(slug, apiKey, userAgent string, enabled bool) error {
	if !knownProvider(slug) {
		return fmt.Errorf("unknown source provider %q", slug)
	}
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return err
	}
	srcs := cfg.ResolveSources()
	key := strings.TrimSpace(apiKey)
	ua := strings.TrimSpace(userAgent)
	found := false
	for i := range srcs {
		if srcs[i].Provider == slug {
			srcs[i].APIKey = key
			srcs[i].UserAgent = ua
			srcs[i].Enabled = enabled
			found = true
			break
		}
	}
	if !found {
		srcs = append(srcs, config.SourceConfig{Provider: slug, APIKey: key, UserAgent: ua, Enabled: enabled})
	}
	cfg.Sources = srcs
	if slug == nhentai.Slug {
		cfg.NhentaiAPIKey = key
		cfg.NhentaiUserAgent = ua
	}
	if cfg.ActiveSource == "" && enabled {
		cfg.ActiveSource = slug
	}
	return config.Save(cfg, a.dataDir)
}

// SetActiveSource selects which configured source the auto-tagger fetches from. It
// ensures the chosen provider is present and enabled first (a source not yet in the
// list — e.g. MangaDex, which needs no key — is created enabled), so picking it in the
// UI takes effect immediately instead of falling back to the first enabled source.
func (a *App) SetActiveSource(slug string) error {
	if !knownProvider(slug) {
		return fmt.Errorf("unknown source provider %q", slug)
	}
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return err
	}
	srcs := cfg.ResolveSources()
	found := false
	for i := range srcs {
		if srcs[i].Provider == slug {
			srcs[i].Enabled = true
			found = true
			break
		}
	}
	if !found {
		srcs = append(srcs, config.SourceConfig{Provider: slug, Enabled: true})
	}
	cfg.Sources = srcs
	cfg.ActiveSource = slug
	return config.Save(cfg, a.dataDir)
}
