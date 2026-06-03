// Package stash is the read/write layer for saved "pages": persisted, browser-tab-
// like navigation states the user sets aside for later. A page is either a saved
// search (Kind "search", whose Hash captures the filter route) or a saved title
// (Kind "title", with MangaID and a LastPage resume point). All access goes through
// a store.Querier so helpers run standalone (autocommit) or inside a transaction,
// matching the convention in internal/search and internal/ingest.
package stash

import (
	"database/sql"
	"time"

	"doujin/internal/store"
)

// Kind values for a stash row.
const (
	KindSearch = "search"
	KindTitle  = "title"
)

// Entry is one saved page. The manga-derived fields (MangaID, Title, AuthorName,
// FolderPath, CoverRelPath) are populated only for Kind "title" via a LEFT JOIN and
// are zero values for "search" rows, so the frontend can render a cover card for
// titles and a filter-chip card for searches from the same payload.
type Entry struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Hash      string `json:"hash"`
	Label     string `json:"label"`
	LastPage  int    `json:"last_page"`
	DateAdded string `json:"date_added"`

	MangaID      *int64  `json:"manga_id"`
	Title        string  `json:"title"`
	AuthorName   string  `json:"author_name"`
	FolderPath   string  `json:"folder_path"`
	CoverRelPath *string `json:"cover_rel_path"`
}

// Add inserts a saved page and returns its new id. A zero MangaID is stored as NULL
// (searches have no manga). date_added is stamped RFC3339Nano to match the manga
// table, so List can order newest-first.
func Add(q store.Querier, e Entry) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := q.Exec(
		"INSERT INTO stash(kind, hash, label, manga_id, last_page, date_added) VALUES (?,?,?,?,?,?)",
		e.Kind, e.Hash, e.Label, nullableID(e.MangaID), e.LastPage, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Remove deletes a saved page by id.
func Remove(q store.Querier, id int64) error {
	_, err := q.Exec("DELETE FROM stash WHERE id = ?", id)
	return err
}

// SetPage updates a title page's resume point. A row that does not exist is a no-op.
func SetPage(q store.Querier, id int64, page int) error {
	_, err := q.Exec("UPDATE stash SET last_page = ? WHERE id = ?", page, id)
	return err
}

// Get returns one saved page by id, or nil when the id is unknown.
func Get(q store.Querier, id int64) (*Entry, error) {
	rows, err := q.Query(selectEntry+" WHERE s.id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	es, err := scanEntries(rows)
	if err != nil {
		return nil, err
	}
	if len(es) == 0 {
		return nil, nil
	}
	return &es[0], nil
}

// List returns every saved page, newest first.
func List(q store.Querier) ([]Entry, error) {
	rows, err := q.Query(selectEntry + " ORDER BY s.date_added DESC, s.id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// selectEntry joins each stash row to its manga (and author) when it has one. The
// LEFT JOIN keeps "search" rows, whose manga_id is NULL, in the result set.
const selectEntry = `
SELECT s.id, s.kind, s.hash, s.label, s.last_page, s.date_added,
       s.manga_id, m.title, a.name, m.folder_path, m.cover_rel_path
FROM stash s
LEFT JOIN manga m   ON m.id = s.manga_id
LEFT JOIN authors a ON a.id = m.author_id`

func scanEntries(rows *sql.Rows) ([]Entry, error) {
	out := []Entry{}
	for rows.Next() {
		var e Entry
		// All manga-derived columns are NULL for "search" rows (and for "title" rows
		// whose manga was somehow removed), so scan them through nullable holders.
		var mangaID sql.NullInt64
		var title, author, folder sql.NullString
		var cover sql.NullString
		if err := rows.Scan(
			&e.ID, &e.Kind, &e.Hash, &e.Label, &e.LastPage, &e.DateAdded,
			&mangaID, &title, &author, &folder, &cover,
		); err != nil {
			return nil, err
		}
		if mangaID.Valid {
			id := mangaID.Int64
			e.MangaID = &id
		}
		e.Title = title.String
		e.AuthorName = author.String
		e.FolderPath = folder.String
		if cover.Valid {
			c := cover.String
			e.CoverRelPath = &c
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// nullableID maps a nil/zero manga id to a NULL bind value.
func nullableID(id *int64) any {
	if id == nil || *id == 0 {
		return nil
	}
	return *id
}
