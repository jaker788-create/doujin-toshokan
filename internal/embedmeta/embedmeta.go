// Package embedmeta reads the optional info.json a ripper embeds in a .cbz (see
// tools/userscripts/akuma-doujin.user.js) and turns it into the app's canonical
// tag vocabulary. The filename grammar (internal/doujin) can only carry the
// language, parody, and a handful of misc tags; info.json carries the *full*
// scraped set — every artist and group, all characters, category, and the
// general male/female/other tags — already keyed by our tag subjects.
//
// Absence of the file is not an error: a plain archive (or a loose-image folder)
// simply yields no embedded tags, and import falls back to the filename parse. It
// is a leaf package (depends only on archive + tag), so import, preview, and
// rescan can all apply the embedded tags without an import cycle.
package embedmeta

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"doujin/internal/archive"
	"doujin/internal/tag"
)

// InfoEntry is the zip entry name the userscript writes the metadata to.
const InfoEntry = "info.json"

// Info mirrors the info.json the userscript emits. Only the fields the app
// consumes are modeled; unknown fields are ignored by encoding/json. Tags is
// keyed by our tag subjects ("artist", "group", "parody", "character", "tag",
// "language", "category"), each holding that subject's values.
type Info struct {
	Source    string              `json:"source"`
	Slug      string              `json:"slug"`
	URL       string              `json:"url"`
	NhentaiID string              `json:"nhentai_id"`
	Tags      map[string][]string `json:"tags"`
	Pages     int                 `json:"pages"`
}

// TypedTags flattens the subject-keyed tag map into canonical typed tags, one per
// value: each subject key is normalized to a known tag subject (unknown keys fall
// back to General via tag.Normalize) and each value is lowercased and trimmed.
// Blank values are dropped; the result is de-duplicated by (subject, name) and
// returned in tag.Sort order so the output is deterministic.
func (i *Info) TypedTags() []tag.Typed {
	if i == nil || len(i.Tags) == 0 {
		return nil
	}
	// Sort the subject keys so iteration is deterministic regardless of the JSON
	// object's key order.
	subjects := make([]string, 0, len(i.Tags))
	for subj := range i.Tags {
		subjects = append(subjects, subj)
	}
	sort.Strings(subjects)

	var out []tag.Typed
	seen := map[string]bool{}
	for _, subj := range subjects {
		typ := tag.Normalize(subj)
		for _, v := range i.Tags[subj] {
			name := strings.ToLower(strings.TrimSpace(v))
			if name == "" {
				continue
			}
			key := typ + "\x00" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, tag.Typed{Name: name, Type: typ})
		}
	}
	return tag.Sort(out)
}

// Read returns the Info embedded in a .cbz/.zip, or (nil, nil) when the archive
// has no info.json (a plain archive). A malformed info.json returns an error so a
// corrupt embed is surfaced rather than silently dropped.
func Read(archivePath string) (*Info, error) {
	rc, err := archive.OpenEntry(archivePath, InfoEntry)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read-only entry
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("embedmeta: parsing %s in %s: %w", InfoEntry, archivePath, err)
	}
	return &info, nil
}

// TypedTagsFor is the best-effort import hook: it reads path's embedded info.json
// (when path is an archive that carries one) and returns its tags as canonical
// typed tags. A non-archive path, an archive with no info.json, or any read/parse
// error yields no tags — import never fails because of a missing or malformed
// embed; it just falls back to the filename parse.
func TypedTagsFor(path string) []tag.Typed {
	if !archive.IsArchive(path) {
		return nil
	}
	info, err := Read(path)
	if err != nil || info == nil {
		return nil
	}
	return info.TypedTags()
}
