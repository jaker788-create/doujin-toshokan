// Package ingest writes manga, authors, and tags into the database. Mutations run
// inside a single transaction so a failure rolls back cleanly, mirroring the
// `with conn:` blocks in doujin/ingest.py. Library files are never modified
// (index-in-place).
package ingest

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"doujin/internal/store"
	"doujin/internal/tag"
)

// NormalizeTag lowercases and trims a tag, the canonical form stored and queried.
func NormalizeTag(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// GetOrCreateAuthor returns the id of the author named name, inserting it if new.
func GetOrCreateAuthor(q store.Querier, name string) (int64, error) {
	var id int64
	err := q.QueryRow("SELECT id FROM authors WHERE name=?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := q.Exec("INSERT INTO authors(name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetOrCreateTag returns the id of the tag named name (assumed already normalized by
// the caller), inserting it with subject typ if new. If the tag already exists with
// an untyped (General) subject and a meaningful typ is supplied, its subject is
// upgraded in place; an existing meaningful subject is never downgraded. That
// upgrade-not-downgrade rule is what lets the freeform tag editor re-save typed tags
// by name without stripping their subjects (and lets Rescan backfill subjects).
func GetOrCreateTag(q store.Querier, name, typ string) (int64, error) {
	typ = tag.Normalize(typ)
	var id int64
	var existing string
	err := q.QueryRow("SELECT id, type FROM tags WHERE name=?", name).Scan(&id, &existing)
	if err == nil {
		if existing == tag.General && typ != tag.General {
			if _, err := q.Exec("UPDATE tags SET type=? WHERE id=?", typ, id); err != nil {
				return 0, err
			}
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := q.Exec("INSERT INTO tags(name, type) VALUES (?, ?)", name, typ)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetMangaTags replaces a manga's tags with tags (normalized + de-duplicated by
// name), atomically. This is a replace, not a merge: the new list is the complete tag
// set. Each tag's subject enriches its tag row via GetOrCreateTag (upgrade, never
// downgrade), so re-saving an existing typed tag by name keeps its subject. Returns
// the saved tags with their *effective* subjects, ordered by subject then name.
func SetMangaTags(db *sql.DB, mangaID int64, tags []tag.Typed) ([]tag.Typed, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if _, err := tx.Exec("DELETE FROM manga_tags WHERE manga_id = ?", mangaID); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, raw := range tags {
		name := NormalizeTag(raw.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		tagID, err := GetOrCreateTag(tx, name, raw.Type)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(
			"INSERT OR IGNORE INTO manga_tags(manga_id, tag_id) VALUES (?,?)", mangaID, tagID,
		); err != nil {
			return nil, err
		}
	}
	// Read back the effective subjects (an existing tag may have kept its own) before
	// committing — same connection, so rows are fully drained first.
	saved, err := mangaTypedTags(tx, mangaID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

// mangaTypedTags reads a manga's tags with their subjects, ordered by subject rank
// then name. Used by the write path to return the saved set; the read chokepoint
// (search.GetMangaTagsTyped) mirrors this query for the detail view.
func mangaTypedTags(q store.Querier, mangaID int64) ([]tag.Typed, error) {
	rows, err := q.Query(
		"SELECT t.name, t.type FROM tags t JOIN manga_tags mt ON mt.tag_id = t.id "+
			"WHERE mt.manga_id = ?", mangaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tag.Typed{}
	for rows.Next() {
		var tt tag.Typed
		if err := rows.Scan(&tt.Name, &tt.Type); err != nil {
			return nil, err
		}
		out = append(out, tt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tag.Sort(out), nil
}

// MangaInput is the data needed to ingest one title. CoverRelPath is nil when the
// folder has no detectable cover.
type MangaInput struct {
	Title        string
	Author       string
	FolderPath   string
	CoverRelPath *string
	PageCount    int
	Tags         []tag.Typed
}

// IngestManga inserts a manga (creating/linking its author and tags) atomically.
// A duplicate folder_path violates the UNIQUE constraint and returns an error with
// the transaction rolled back, leaving no orphan author/tag rows.
func IngestManga(db *sql.DB, in MangaInput) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	authorID, err := GetOrCreateAuthor(tx, in.Author)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(
		"INSERT INTO manga(title, author_id, folder_path, cover_rel_path, "+
			"page_count, date_added, date_modified, missing) VALUES (?,?,?,?,?,?,?,0)",
		in.Title, authorID, in.FolderPath, in.CoverRelPath, in.PageCount, now, now,
	)
	if err != nil {
		return 0, err
	}
	mangaID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, raw := range in.Tags {
		name := NormalizeTag(raw.Name)
		if name == "" {
			continue
		}
		tagID, err := GetOrCreateTag(tx, name, raw.Type)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(
			"INSERT OR IGNORE INTO manga_tags(manga_id, tag_id) VALUES (?,?)", mangaID, tagID,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return mangaID, nil
}
