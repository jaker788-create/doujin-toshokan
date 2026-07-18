package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "doujin.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableNames(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names[n] = true
	}
	return names
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestInitCreatesTables(t *testing.T) {
	db := openTest(t)
	if err := Init(db); err != nil {
		t.Fatal(err)
	}
	names := tableNames(t, db)
	for _, want := range []string{"authors", "manga", "tags", "manga_tags", "stash"} {
		if !names[want] {
			t.Errorf("missing table %q", want)
		}
	}
}

func TestMigration003AddsNhentaiColumn(t *testing.T) {
	db := openTest(t)
	if err := Init(db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("PRAGMA table_info(manga)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "nhentai_gallery_id" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("manga.nhentai_gallery_id column missing after Init")
	}
}

func TestMigration006AddsDisplayTitleColumn(t *testing.T) {
	db := openTest(t)
	if err := Init(db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("PRAGMA table_info(manga)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "display_title" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("manga.display_title column missing after Init")
	}
}

func TestMigration004AddsTagTypeColumn(t *testing.T) {
	db := openTest(t)
	if err := Init(db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("PRAGMA table_info(tags)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "type" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("tags.type column missing after Init")
	}
}

// hasColumn reports whether a table has the named column (test helper).
func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	return false
}

// migrate007 adds the provider-neutral source_slug/source_ref link columns and backfills
// an existing nhentai_gallery_id into ('nhentai', "<id>"). Re-running it is a safe no-op on
// already-linked rows (guarded by source_ref IS NULL), which this also exercises.
func TestMigration007AddsSourceLinkAndBackfills(t *testing.T) {
	db := openTest(t)
	if err := Init(db); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"source_slug", "source_ref"} {
		if !hasColumn(t, db, "manga", col) {
			t.Errorf("manga.%s missing after Init", col)
		}
	}

	// A legacy nhentai link whose source_ref is still NULL must backfill on (idempotent) re-run.
	if _, err := db.Exec(`INSERT INTO authors(id, name) VALUES (1, 'A')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO manga(id, title, author_id, folder_path, date_added, date_modified, nhentai_gallery_id)
		VALUES (1, 'T', 1, '/x', '', '', 123)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate007SourceLink(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var slug, ref sql.NullString
	if err := db.QueryRow("SELECT source_slug, source_ref FROM manga WHERE id = 1").Scan(&slug, &ref); err != nil {
		t.Fatal(err)
	}
	if slug.String != "nhentai" || ref.String != "123" {
		t.Errorf("backfill = (%q, %q), want (nhentai, 123)", slug.String, ref.String)
	}
}

func TestMigrationLadderLength(t *testing.T) {
	if MigrationCount() != 7 {
		t.Errorf("MigrationCount() = %d, want 7", MigrationCount())
	}
}

// migrate005 strips wrapping parens from author names and merges a clean-name collision:
// "(Rustle)" + a separate "Rustle" become one "Rustle" owning both titles.
func TestMigrate005CleansAndMergesAuthors(t *testing.T) {
	db := openTest(t)
	if _, err := db.Exec(schema); err != nil { // baseline tables, migration 005 not yet run
		t.Fatal(err)
	}
	seed := func(name, mangaTitle, folder string) {
		res, err := db.Exec("INSERT INTO authors(name) VALUES (?)", name)
		if err != nil {
			t.Fatal(err)
		}
		aid, _ := res.LastInsertId()
		if _, err := db.Exec(
			"INSERT INTO manga(title, author_id, folder_path, page_count, date_added, date_modified) VALUES (?,?,?,?,?,?)",
			mangaTitle, aid, folder, 1, "t", "t"); err != nil {
			t.Fatal(err)
		}
	}
	seed("(Rustle)", "Diploma mill", "/lib/(Rustle)/Diploma mill")
	seed("Rustle", "Another", "/lib/Rustle/Another") // already clean — collides on merge
	seed("(Yoku)", "Datte", "/lib/(Yoku)/Datte")

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate005CleanAuthorNames(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	rows, err := db.Query("SELECT a.name, COUNT(m.id) FROM authors a LEFT JOIN manga m ON m.author_id=a.id GROUP BY a.id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		var c int
		if err := rows.Scan(&n, &c); err != nil {
			t.Fatal(err)
		}
		counts[n] = c
	}
	if len(counts) != 2 {
		t.Errorf("authors = %v, want exactly Rustle + Yoku", counts)
	}
	if counts["Rustle"] != 2 {
		t.Errorf("Rustle owns %d titles, want 2 (merged)", counts["Rustle"])
	}
	if counts["Yoku"] != 1 {
		t.Errorf("Yoku owns %d titles, want 1", counts["Yoku"])
	}
	if _, ok := counts["(Rustle)"]; ok {
		t.Errorf("parenthesized author survived: %v", counts)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	db := openTest(t)
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestInitStampsLatestUserVersion(t *testing.T) {
	db := openTest(t)
	if err := Init(db); err != nil {
		t.Fatal(err)
	}
	if got := userVersion(t, db); got != MigrationCount() {
		t.Errorf("user_version = %d, want %d", got, MigrationCount())
	}
}

func TestInitIsIdempotent(t *testing.T) {
	db := openTest(t)
	if err := Init(db); err != nil {
		t.Fatal(err)
	}
	if err := Init(db); err != nil {
		t.Fatal(err)
	}
	if got := userVersion(t, db); got != MigrationCount() {
		t.Errorf("user_version = %d, want %d", got, MigrationCount())
	}
	if !tableNames(t, db)["authors"] {
		t.Error("authors table missing after second Init")
	}
}

func TestInitPreservesExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doujin.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Init(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO authors(name) VALUES ('Aoi')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if err := Init(db2); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db2.QueryRow("SELECT name FROM authors").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Aoi" {
		t.Errorf("name = %q, want Aoi", name)
	}
}

// A database created before the migration system existed: baseline tables present
// but user_version still 0. Init must detect the gap, run cleanly (IF NOT EXISTS
// baseline is a no-op), and stamp it forward without disturbing rows.
func TestLegacyDBAtVersionZeroUpgrades(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doujin.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil { // old-style direct create
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO authors(name) VALUES ('Legacy')"); err != nil {
		t.Fatal(err)
	}
	if got := userVersion(t, db); got != 0 {
		t.Fatalf("precondition user_version = %d, want 0", got)
	}
	_ = db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if err := Init(db2); err != nil {
		t.Fatal(err)
	}
	if got := userVersion(t, db2); got != MigrationCount() {
		t.Errorf("user_version = %d, want %d", got, MigrationCount())
	}
	var name string
	if err := db2.QueryRow("SELECT name FROM authors").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Legacy" {
		t.Errorf("name = %q, want Legacy", name)
	}
}

// Mechanism test: append a second migration to the ladder and confirm Init applies
// only the pending one and advances user_version to match.
func TestRunnerAppliesOnlyPendingMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doujin.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Init(db); err != nil {
		t.Fatal(err)
	}
	base := MigrationCount() // the baseline ladder length, before we append below
	if got := userVersion(t, db); got != base {
		t.Fatalf("user_version = %d, want %d", got, base)
	}
	_ = db.Close()

	applied := []string{}
	orig := migrations
	migrations = append(append([]func(*sql.Tx) error{}, orig...), func(tx *sql.Tx) error {
		applied = append(applied, "second")
		_, err := tx.Exec("CREATE TABLE IF NOT EXISTS extra (id INTEGER PRIMARY KEY)")
		return err
	})
	defer func() { migrations = orig }()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if err := Init(db2); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "second" {
		t.Errorf("applied = %v, want [second]", applied)
	}
	if got := userVersion(t, db2); got != base+1 {
		t.Errorf("user_version = %d, want %d", got, base+1)
	}
	if !tableNames(t, db2)["extra"] {
		t.Error("extra table not created by pending migration")
	}
}
