// Package store owns the SQLite connection and the schema-migration ladder. It is
// the Go port of doujin/db.py, backed by the pure-Go modernc.org/sqlite driver
// (no cgo, so the app still builds to a single static binary).
package store

import (
	"database/sql"
	"fmt"

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
