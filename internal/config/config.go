// Package config loads and saves the app's JSON config and resolves the data-dir
// layout (%APPDATA%/doujin on Windows). It mirrors doujin/config.py, minus the
// one-time "stash" legacy-dir migration, which has already run on this machine.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SourceConfig is one configured metadata source the auto-tagger can use. Provider is
// the source slug ("nhentai", "mangadex", …). APIKey/UserAgent are the credentials that
// source needs — some (mangadex) require no key, so an empty APIKey does not by itself
// mean "disabled"; Enabled does. Secrets carries any extra per-provider auth material
// (e.g. e-hentai session cookies) without widening the struct per site.
//
// BaseURL overrides the provider's API host; empty means the provider's own default.
// It exists because a site can move its data domain out from under a shipped binary —
// hitomi did exactly that (ltn.hitomi.la stopped resolving after the 2025-03 move), and
// recovering from the next one should be a settings edit, not a release.
type SourceConfig struct {
	Provider  string            `json:"provider"`
	APIKey    string            `json:"api_key"`
	UserAgent string            `json:"user_agent"`
	BaseURL   string            `json:"base_url,omitempty"`
	Secrets   map[string]string `json:"secrets,omitempty"`
	Enabled   bool              `json:"enabled"`
}

// Config mirrors the on-disk config.json schema. Port is kept for backward
// compatibility with existing config.json files; the native app no longer serves
// over HTTP, so it is otherwise unused.
type Config struct {
	LibraryRoots []string `json:"library_roots"`
	Port         int      `json:"port"`
	// NhentaiAPIKey/NhentaiUserAgent are the LEGACY single-source fields. They are still
	// read + written so an existing config.json keeps working, and Load synthesizes a
	// Sources entry from them when Sources is empty (see resolveSources). New code should
	// go through Sources + ActiveSource instead.
	NhentaiAPIKey    string `json:"nhentai_api_key"`
	NhentaiUserAgent string `json:"nhentai_user_agent"`
	// Sources is the multi-provider list; ActiveSource is the slug of the one the UI
	// currently tags with (empty falls back to the first enabled source).
	Sources      []SourceConfig `json:"sources"`
	ActiveSource string         `json:"active_source"`
}

// ResolveSources returns the effective source list, synthesizing a legacy nhentai entry
// from NhentaiAPIKey/NhentaiUserAgent when Sources is empty — so an existing config.json
// (which only has the flat nhentai_* fields) behaves exactly as before with no rewrite.
func (c Config) ResolveSources() []SourceConfig {
	if len(c.Sources) > 0 {
		return c.Sources
	}
	if c.NhentaiAPIKey != "" {
		return []SourceConfig{{
			Provider:  "nhentai",
			APIKey:    c.NhentaiAPIKey,
			UserAgent: c.NhentaiUserAgent,
			Enabled:   true,
		}}
	}
	return nil
}

// ActiveSourceConfig resolves the currently-selected source: ActiveSource by slug when
// set and present, else the first enabled source, else a zero SourceConfig with ok=false.
func (c Config) ActiveSourceConfig() (SourceConfig, bool) {
	srcs := c.ResolveSources()
	if c.ActiveSource != "" {
		for _, s := range srcs {
			if s.Provider == c.ActiveSource {
				return s, true
			}
		}
	}
	for _, s := range srcs {
		if s.Enabled {
			return s, true
		}
	}
	return SourceConfig{}, false
}

// DefaultDataDir returns %APPDATA%/doujin on Windows. os.UserConfigDir reports the
// Roaming AppData directory, matching the Python build's
// platformdirs.user_data_dir("doujin", roaming=True).
func DefaultDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "doujin"), nil
}

func configFile(dataDir string) string { return filepath.Join(dataDir, "config.json") }

// Load reads config.json from dataDir, returning defaults (no roots, port 8765)
// when the file does not exist. Missing keys keep their defaults.
func Load(dataDir string) (Config, error) {
	cfg := Config{LibraryRoots: []string{}, Port: 8765}
	data, err := os.ReadFile(configFile(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	var raw struct {
		LibraryRoots     []string       `json:"library_roots"`
		Port             *int           `json:"port"`
		NhentaiAPIKey    *string        `json:"nhentai_api_key"`
		NhentaiUserAgent *string        `json:"nhentai_user_agent"`
		Sources          []SourceConfig `json:"sources"`
		ActiveSource     *string        `json:"active_source"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return cfg, err
	}
	if raw.LibraryRoots != nil {
		cfg.LibraryRoots = raw.LibraryRoots
	}
	if raw.Port != nil {
		cfg.Port = *raw.Port
	}
	if raw.NhentaiAPIKey != nil {
		cfg.NhentaiAPIKey = *raw.NhentaiAPIKey
	}
	if raw.NhentaiUserAgent != nil {
		cfg.NhentaiUserAgent = *raw.NhentaiUserAgent
	}
	if raw.Sources != nil {
		cfg.Sources = raw.Sources
	}
	if raw.ActiveSource != nil {
		cfg.ActiveSource = *raw.ActiveSource
	}
	return cfg, nil
}

// Save writes config.json (pretty-printed) to dataDir, creating the dir if needed.
func Save(cfg Config, dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	if cfg.LibraryRoots == nil {
		cfg.LibraryRoots = []string{}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configFile(dataDir), data, 0o644)
}

// DBPath is the SQLite database path for a data dir.
func DBPath(dataDir string) string { return filepath.Join(dataDir, "doujin.db") }

// ThumbCacheDir is the on-disk thumbnail cache directory for a data dir.
func ThumbCacheDir(dataDir string) string { return filepath.Join(dataDir, "thumbs") }
