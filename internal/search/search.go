// Package search is the read layer and the single query chokepoint for browsing
// the library, ported from doujin/search.py. SearchManga composes the optional
// query/author/tag/sort/paging filters into one parameterized SQL statement;
// every browse and filter path goes through it.
package search

import (
	"database/sql"
	"errors"
	"strings"

	"doujin/internal/ingest"
	"doujin/internal/store"
	"doujin/internal/tag"
)

// Manga is a row of the manga table joined with its author name.
type Manga struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	AuthorID     int64   `json:"author_id"`
	FolderPath   string  `json:"folder_path"`
	CoverRelPath *string `json:"cover_rel_path"`
	PageCount    int     `json:"page_count"`
	DateAdded    string  `json:"date_added"`
	DateModified string  `json:"date_modified"`
	Missing      bool    `json:"missing"`
	// NhentaiGalleryID is the legacy nhentai gallery a title's tags were copied from, or
	// nil if it was never auto-tagged by nhentai. Kept for backward compatibility;
	// SourceSlug/SourceRef are the provider-neutral link the auto-tagger now writes.
	NhentaiGalleryID *int64 `json:"nhentai_gallery_id"`
	// DisplayTitle is a user-edited title override, or nil when none is set. The UI shows
	// it instead of Title; Title stays the canonical, matching/Rescan-owned value.
	DisplayTitle *string `json:"display_title"`
	// SourceSlug names which metadata source a title's tags were copied from ("nhentai",
	// "mangadex", …), and SourceRef is that source's own gallery id (string). Both nil
	// when the title was never auto-tagged. Lets the UI show / re-sync the linked source
	// across providers.
	SourceSlug *string `json:"source_slug"`
	SourceRef  *string `json:"source_ref"`
	AuthorName string  `json:"author_name"`
}

// Author is an author row.
type Author struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Tag is a tag row.
type Tag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// sorts allow-lists the only valid sort values; an unknown (or attacker-supplied)
// sort falls back to "m.title" and is NEVER interpolated into the SQL. "date"
// sorts newest-first with m.id DESC as a deterministic tiebreaker for rows sharing
// a date_added timestamp.
var sorts = map[string]string{
	"title":  "m.title",
	"author": "a.name",
	"date":   "m.date_added DESC, m.id DESC",
}

// SearchParams holds the optional filters. AuthorID 0 means "any author"; a Limit
// of 0 or less means "no limit" (matching the Python limit=None default).
type SearchParams struct {
	Query    string
	AuthorID int64
	TagIDs   []int64
	Sort     string
	Seed     int64 // only used when Sort == "random"; selects one stable shuffle
	Limit    int
	Offset   int
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func scanMangaRows(rows *sql.Rows) ([]Manga, error) {
	out := []Manga{}
	for rows.Next() {
		var m Manga
		var cover sql.NullString
		var missing int
		var nhentaiID sql.NullInt64
		var displayTitle, sourceSlug, sourceRef sql.NullString
		// Column order matches `SELECT m.*, a.name AS author_name` (manga columns in
		// table-definition order — nhentai_gallery_id was appended by migration 003,
		// display_title by 006, and source_slug/source_ref by 007 — then the appended
		// author_name). Appending a manga column REQUIRES adding its scan target here.
		if err := rows.Scan(
			&m.ID, &m.Title, &m.AuthorID, &m.FolderPath, &cover, &m.PageCount,
			&m.DateAdded, &m.DateModified, &missing, &nhentaiID, &displayTitle,
			&sourceSlug, &sourceRef, &m.AuthorName,
		); err != nil {
			return nil, err
		}
		if cover.Valid {
			c := cover.String
			m.CoverRelPath = &c
		}
		if nhentaiID.Valid {
			id := nhentaiID.Int64
			m.NhentaiGalleryID = &id
		}
		if displayTitle.Valid {
			dt := displayTitle.String
			m.DisplayTitle = &dt
		}
		if sourceSlug.Valid {
			s := sourceSlug.String
			m.SourceSlug = &s
		}
		if sourceRef.Valid {
			r := sourceRef.String
			m.SourceRef = &r
		}
		m.Missing = missing != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// SearchManga returns manga matching the given filters. Tag filtering requires ALL
// supplied tags (AND), enforced by GROUP BY m.id HAVING COUNT(DISTINCT)=len.
func SearchManga(q store.Querier, p SearchParams) ([]Manga, error) {
	parts := []string{
		"SELECT m.*, a.name AS author_name FROM manga m JOIN authors a ON a.id = m.author_id",
	}
	var args []any
	var where []string

	if len(p.TagIDs) > 0 {
		parts = append(parts,
			"JOIN manga_tags mt ON mt.manga_id = m.id AND mt.tag_id IN ("+placeholders(len(p.TagIDs))+")")
		for _, id := range p.TagIDs {
			args = append(args, id)
		}
	}
	if p.Query != "" {
		// Match the canonical title, the user's display override, or the author — so a
		// title is findable by its original romaji name OR by whatever it was renamed to.
		where = append(where, "(m.title LIKE ? OR m.display_title LIKE ? OR a.name LIKE ?)")
		like := "%" + p.Query + "%"
		args = append(args, like, like, like)
	}
	if p.AuthorID != 0 {
		where = append(where, "m.author_id = ?")
		args = append(args, p.AuthorID)
	}
	if len(where) > 0 {
		parts = append(parts, "WHERE "+strings.Join(where, " AND "))
	}
	if len(p.TagIDs) > 0 {
		parts = append(parts, "GROUP BY m.id HAVING COUNT(DISTINCT mt.tag_id) = ?")
		args = append(args, len(p.TagIDs))
	}
	if p.Sort == "random" {
		// Seeded, stable shuffle: each row gets a fixed key for a given seed, so
		// LIMIT/OFFSET paging stays consistent across pages (a naive RANDOM()
		// re-rolls per query and dupes/drops rows as you scroll). A new seed yields
		// a new order. The ORDER BY text is a constant — only the seed is a bound
		// arg — so the no-interpolation sort invariant holds. "random" is kept OUT
		// of the sorts allow-list on purpose so unknown sorts still fall back to
		// m.title.
		parts = append(parts, "ORDER BY ((m.id + ?) * 1103515245) % 2147483647")
		args = append(args, p.Seed)
	} else {
		order := sorts[p.Sort]
		if order == "" {
			order = "m.title"
		}
		parts = append(parts, "ORDER BY "+order)
	}
	if p.Limit > 0 {
		parts = append(parts, "LIMIT ? OFFSET ?")
		args = append(args, p.Limit, p.Offset)
	}

	rows, err := q.Query(strings.Join(parts, " "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMangaRows(rows)
}

// SuggestTags returns up to limit tag names matching prefix (normalized).
func SuggestTags(q store.Querier, prefix string, limit int) ([]string, error) {
	rows, err := q.Query(
		"SELECT name FROM tags WHERE name LIKE ? ORDER BY name LIMIT ?",
		ingest.NormalizeTag(prefix)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// SuggestTagsTyped returns up to limit tags (name + subject) matching prefix
// (normalized). Like SuggestTags but carrying each tag's stored subject, so the tag
// editor can auto-fill the subject when the user picks an existing tag.
func SuggestTagsTyped(q store.Querier, prefix string, limit int) ([]tag.Typed, error) {
	rows, err := q.Query(
		"SELECT name, type FROM tags WHERE name LIKE ? ORDER BY name LIMIT ?",
		ingest.NormalizeTag(prefix)+"%", limit)
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
	return out, rows.Err()
}

// SuggestAuthors returns up to limit authors whose name contains prefix (substring
// match: the memorable token is often not the first word).
func SuggestAuthors(q store.Querier, prefix string, limit int) ([]Author, error) {
	rows, err := q.Query(
		"SELECT id, name FROM authors WHERE name LIKE ? ORDER BY name LIMIT ?",
		"%"+prefix+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAuthors(rows)
}

// TagIDsForNames resolves tag names to ids (normalized). Unknown names are skipped.
func TagIDsForNames(q store.Querier, names []string) ([]int64, error) {
	ids := []int64{}
	for _, name := range names {
		var id int64
		err := q.QueryRow("SELECT id FROM tags WHERE name = ?", ingest.NormalizeTag(name)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// GetManga returns one manga by id, or nil when it does not exist.
func GetManga(q store.Querier, id int64) (*Manga, error) {
	rows, err := q.Query(
		"SELECT m.*, a.name AS author_name FROM manga m "+
			"JOIN authors a ON a.id = m.author_id WHERE m.id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ms, err := scanMangaRows(rows)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, nil
	}
	return &ms[0], nil
}

// GetAuthor returns one author by id, or nil when the id is dangling.
func GetAuthor(q store.Querier, id int64) (*Author, error) {
	var a Author
	err := q.QueryRow("SELECT id, name FROM authors WHERE id = ?", id).Scan(&a.ID, &a.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// GetMangaTags returns a manga's tag names, alphabetically.
func GetMangaTags(q store.Querier, mangaID int64) ([]string, error) {
	rows, err := q.Query(
		"SELECT t.name FROM tags t JOIN manga_tags mt ON mt.tag_id = t.id "+
			"WHERE mt.manga_id = ? ORDER BY t.name", mangaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// GetMangaTagsTyped returns a manga's tags with their subjects, ordered by subject
// (language, artist, group, parody, character, category, tag/general) then name — the
// order the UI groups them in. This is the read chokepoint for the title detail view.
func GetMangaTagsTyped(q store.Querier, mangaID int64) ([]tag.Typed, error) {
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

// ListAuthors returns all authors ordered by name.
func ListAuthors(q store.Querier) ([]Author, error) {
	rows, err := q.Query("SELECT id, name FROM authors ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAuthors(rows)
}

// ListTags returns all tags ordered by name.
func ListTags(q store.Querier) ([]Tag, error) {
	rows, err := q.Query("SELECT id, name FROM tags ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func scanAuthors(rows *sql.Rows) ([]Author, error) {
	authors := []Author{}
	for rows.Next() {
		var a Author
		if err := rows.Scan(&a.ID, &a.Name); err != nil {
			return nil, err
		}
		authors = append(authors, a)
	}
	return authors, rows.Err()
}
