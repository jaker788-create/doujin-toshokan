// Package ingest writes manga, authors, and tags into the database. Mutations run
// inside a single transaction so a failure rolls back cleanly, mirroring the
// `with conn:` blocks in doujin/ingest.py. Library files are never modified
// (index-in-place).
package ingest

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"doujin/internal/store"
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

// GetOrCreateTag returns the id of the tag named name (assumed already normalized
// by the caller), inserting it if new.
func GetOrCreateTag(q store.Querier, name string) (int64, error) {
	var id int64
	err := q.QueryRow("SELECT id FROM tags WHERE name=?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := q.Exec("INSERT INTO tags(name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetMangaTags replaces a manga's tags with tags (normalized + de-duplicated),
// atomically. This is a replace, not a merge: the new list is the complete tag
// set. Returns the saved tag names sorted to match search.GetMangaTags order.
func SetMangaTags(db *sql.DB, mangaID int64, tags []string) ([]string, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if _, err := tx.Exec("DELETE FROM manga_tags WHERE manga_id = ?", mangaID); err != nil {
		return nil, err
	}
	seen := []string{}
	seenSet := map[string]bool{}
	for _, raw := range tags {
		name := NormalizeTag(raw)
		if name == "" || seenSet[name] {
			continue
		}
		seenSet[name] = true
		seen = append(seen, name)
		tagID, err := GetOrCreateTag(tx, name)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(
			"INSERT OR IGNORE INTO manga_tags(manga_id, tag_id) VALUES (?,?)", mangaID, tagID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sort.Strings(seen)
	return seen, nil
}

// MangaInput is the data needed to ingest one title. CoverRelPath is nil when the
// folder has no detectable cover.
type MangaInput struct {
	Title        string
	Author       string
	FolderPath   string
	CoverRelPath *string
	PageCount    int
	Tags         []string
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
		name := NormalizeTag(raw)
		if name == "" {
			continue
		}
		tagID, err := GetOrCreateTag(tx, name)
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
