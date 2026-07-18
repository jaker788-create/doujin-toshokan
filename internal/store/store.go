// Package store owns the SQLite connection and the schema-migration ladder. It is
// the Go port of doujin/db.py, backed by the pure-Go modernc.org/sqlite driver
// (no cgo, so the app still builds to a single static binary).
package store

import (
	"database/sql"
	"fmt"

	"doujin/internal/doujin"

	_ "modernc.org/sqlite"
)

// schema is the baseline table set, identical to the Python build's SCHEMA so the
// Go app opens an existing doujin.db unchanged. Uses CREATE ... IF NOT EXISTS so
// it is a safe no-op on databases created before the migration system existed.
const schema = `
CREATE TABLE IF NOT EXISTS authors (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS manga (
    id             INTEGER PRIMARY KEY,
    title          TEXT NOT NULL,
    author_id      INTEGER NOT NULL REFERENCES authors(id),
    folder_path    TEXT NOT NULL UNIQUE,
    cover_rel_path TEXT,
    page_count     INTEGER NOT NULL DEFAULT 0,
    date_added     TEXT NOT NULL,
    date_modified  TEXT NOT NULL,
    missing        INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS tags (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS manga_tags (
    manga_id INTEGER NOT NULL REFERENCES manga(id) ON DELETE CASCADE,
    tag_id   INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
    PRIMARY KEY (manga_id, tag_id)
);
CREATE INDEX IF NOT EXISTS idx_manga_author ON manga(author_id);
CREATE INDEX IF NOT EXISTS idx_manga_title  ON manga(title);
`

// Querier is satisfied by both *sql.DB and *sql.Tx. Read/query helpers in other
// packages accept this so they can run standalone (autocommit) or inside a
// transaction, mirroring how the Python code passed a single connection around.
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// Open opens (creating if absent) the SQLite database at path with foreign keys
// enabled and a busy timeout. MaxOpenConns(1) serializes access: SQLite is a
// single-writer store and one connection keeps per-connection PRAGMAs and write
// ordering simple for a local single-user app.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// migrations is the ordered ladder. The 1-based index of each entry is the schema
// version it produces (recorded in PRAGMA user_version). To evolve the schema,
// APPEND a new function here; never edit or reorder an existing one. It is a var
// (not const) so white-box tests can extend it.
var migrations = []func(*sql.Tx) error{
	migrate001Initial,
	migrate002Stash,
	migrate003NhentaiLink,
	migrate004TagType,
	migrate005CleanAuthorNames,
	migrate006DisplayTitle,
	migrate007SourceLink,
}

func migrate001Initial(tx *sql.Tx) error {
	_, err := tx.Exec(schema)
	return err
}

// migrate002Stash adds the stash table: a persisted list of saved "pages" (browser-
// tab-like navigation states). A row is either a saved search (kind='search', the
// route hash captures its filters) or a saved title (kind='title', manga_id set,
// last_page records the resume point). No UNIQUE constraint — cloning or
// opening-in-a-new-tab intentionally creates independent rows, each with its own
// last_page. ON DELETE CASCADE drops a title's saved pages if the manga is removed.
func migrate002Stash(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS stash (
    id         INTEGER PRIMARY KEY,
    kind       TEXT NOT NULL,
    hash       TEXT NOT NULL,
    label      TEXT NOT NULL,
    manga_id   INTEGER REFERENCES manga(id) ON DELETE CASCADE,
    last_page  INTEGER NOT NULL DEFAULT 0,
    date_added TEXT NOT NULL
);`)
	return err
}

// migrate003NhentaiLink records which nhentai gallery a title's tags were copied
// from. It lets the bulk auto-tagger skip already-linked titles (idempotent
// re-runs) and lets the UI show / re-sync the matched source. Nullable: titles that
// were never matched leave it NULL.
func migrate003NhentaiLink(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE manga ADD COLUMN nhentai_gallery_id INTEGER;`)
	return err
}

// migrate004TagType gives every tag a subject (the tags.type column): language,
// artist, group, parody, character, category, or tag — the same vocabulary nhentai
// uses (see internal/tag). Existing rows default to the empty General/untyped subject;
// they are enriched in place the next time their subject is known (an nhentai apply or a
// Rescan of a title whose folder name implies the subject). Nullable-by-default via
// the DEFAULT so older databases upgrade without touching existing tag rows.
func migrate004TagType(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE tags ADD COLUMN type TEXT NOT NULL DEFAULT '';`)
	return err
}

// migrate005CleanAuthorNames strips a wrapping "(Artist)"/"[Artist]" from stored author
// names (libraries organized by the parenthesized artist land in the authors table
// verbatim), so the displayed author and the nhentai auto-tagger both use the bare artist
// tag. authors.name is UNIQUE and manga.author_id is a foreign key, so a clean that would
// collide with an existing author must MERGE: the smallest id per cleaned name survives,
// the rest have their manga repointed and are deleted. Deletes happen before survivor
// renames so no transient UNIQUE violation occurs. The same doujin.CleanArtist used at
// ingest is reused so the migration and live cleaning never disagree.
func migrate005CleanAuthorNames(tx *sql.Tx) error {
	rows, err := tx.Query("SELECT id, name FROM authors ORDER BY id")
	if err != nil {
		return err
	}
	type author struct {
		id   int64
		name string
	}
	var authors []author
	for rows.Next() {
		var a author
		if err := rows.Scan(&a.id, &a.name); err != nil {
			rows.Close()
			return err
		}
		authors = append(authors, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// The id that survives for each cleaned name (smallest id wins — authors are id-ordered,
	// so the first seen is the smallest). Never clean to empty (keep the original instead).
	survivor := map[string]int64{}
	clean := make([]string, len(authors))
	for i, a := range authors {
		c := doujin.CleanArtist(a.name)
		if c == "" {
			c = a.name
		}
		clean[i] = c
		if _, ok := survivor[c]; !ok {
			survivor[c] = a.id
		}
	}

	// Pass A: repoint manga off the losers, then delete them.
	for i, a := range authors {
		if s := survivor[clean[i]]; a.id != s {
			if _, err := tx.Exec("UPDATE manga SET author_id=? WHERE author_id=?", s, a.id); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM authors WHERE id=?", a.id); err != nil {
				return err
			}
		}
	}
	// Pass B: rename the survivors to their cleaned name (losers already gone, so no clash).
	for i, a := range authors {
		if survivor[clean[i]] == a.id && a.name != clean[i] {
			if _, err := tx.Exec("UPDATE authors SET name=? WHERE id=?", clean[i], a.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// migrate006DisplayTitle adds a user-editable display-title override. The canonical
// `title` stays machine-derived from the folder name (and is refreshed by Rescan); when
// display_title is non-NULL the UI shows it instead. This lets a user rename or translate
// a title for display WITHOUT affecting nhentai matching (which re-parses the immutable
// folder name, not the stored title — see matchInputs in nhentai.go) and without Rescan
// clobbering the edit. NULL means "no override — show the canonical title".
func migrate006DisplayTitle(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE manga ADD COLUMN display_title TEXT;`)
	return err
}

// migrate007SourceLink generalizes the single nhentai link into a provider-neutral
// pair: source_slug names which metadata source a title's tags were copied from
// ("nhentai", "mangadex", …) and source_ref is that source's own gallery id as a string
// (nhentai's numeric id, mangadex's UUID, e-hentai's "gid/token"). Existing nhentai links
// are backfilled from the legacy nhentai_gallery_id column, which is kept in place so old
// data and the numeric-id UI paths still read. Both new columns are nullable — a title
// that was never matched leaves them NULL, which is what the bulk sweep's "skip already
// linked" filter (source_ref IS NULL) keys on. Guarded by a table_info existence check so
// an interrupted run can re-apply safely.
func migrate007SourceLink(tx *sql.Tx) error {
	has, err := columnExists(tx, "manga", "source_slug")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE manga ADD COLUMN source_slug TEXT;`); err != nil {
			return err
		}
	}
	has, err = columnExists(tx, "manga", "source_ref")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE manga ADD COLUMN source_ref TEXT;`); err != nil {
			return err
		}
	}
	// Backfill existing nhentai links: slug 'nhentai', ref = the numeric gallery id as text.
	_, err = tx.Exec(`UPDATE manga SET source_slug='nhentai', source_ref=CAST(nhentai_gallery_id AS TEXT)
		WHERE nhentai_gallery_id IS NOT NULL AND source_ref IS NULL;`)
	return err
}

// columnExists reports whether the named column is present on a table, via PRAGMA
// table_info — so an ADD COLUMN migration can be guarded against re-application.
func columnExists(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// MigrationCount is the number of migrations in the ladder, i.e. the latest schema
// version. Exposed for diagnostics and tests.
func MigrationCount() int { return len(migrations) }

// Init brings db up to the latest schema version, applying any pending migrations
// in order, each in its own transaction. Idempotent: a database already current is
// left untouched (safe to call on every startup), and a pre-migration database at
// user_version 0 is stamped forward without disturbing existing rows.
func Init(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i, migrate := range migrations {
		target := i + 1
		if version >= target {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if err := migrate(tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		// PRAGMA does not accept bound parameters; target is a controlled int from
		// the loop index, so the formatted statement is injection-safe.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", target)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
