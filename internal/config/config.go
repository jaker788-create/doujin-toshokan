// Package config loads and saves the app's JSON config and resolves the data-dir
// layout (%APPDATA%/doujin on Windows). It mirrors doujin/config.py, minus the
// one-time "stash" legacy-dir migration, which has already run on this machine.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config mirrors the on-disk config.json schema. Port is kept for backward
// compatibility with existing config.json files; the native app no longer serves
// over HTTP, so it is otherwise unused.
type Config struct {
	LibraryRoots []string `json:"library_roots"`
	Port         int      `json:"port"`
	// NhentaiAPIKey is the user's personal nhentai API key, entered in-app (never
	// hardcoded or committed). Empty disables the auto-tag features. NhentaiUserAgent
	// is the descriptive User-Agent nhentai asks clients to send; empty falls back to
	// a built-in default in app.go.
	NhentaiAPIKey    string `json:"nhentai_api_key"`
	NhentaiUserAgent string `json:"nhentai_user_agent"`
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
		LibraryRoots     []string `json:"library_roots"`
		Port             *int     `json:"port"`
		NhentaiAPIKey    *string  `json:"nhentai_api_key"`
		NhentaiUserAgent *string  `json:"nhentai_user_agent"`
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
