// Package search is the read layer and the single query chokepoint for browsing
// the library, ported from doujin/search.py. SearchManga composes the optional
// query/author/tag/sort/paging filters into one parameterized SQL statement;
// every browse and filter path goes through it.
package search

import (
	"database/sql"
	"errors"
	"fmt"
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

// sorts allow-lists the only valid sort values; an unknown (or attacker-supplied)
// sort falls back to "m.title" and is NEVER interpolated into the SQL. "date"
// sorts newest-first with m.id DESC as a deterministic tiebreaker for rows sharing
// a date_added timestamp.
var sorts = map[string]string{
	"title":  "m.title",
	"author": "a.name",
	"date":   "m.date_added DESC, m.id DESC",
}

// SourceNone is the SearchParams.SourceSlug sentinel meaning "never auto-tagged" —
// rows whose source_slug is NULL (or blank). It is deliberately a value no provider
// may register as its own slug; providers_test.go pins that against providerPresets,
// because this package is a leaf that cannot import the registry to check itself.
const SourceNone = "none"

// SearchParams holds the optional filters. A Limit of 0 or less means "no limit"
// (matching the Python limit=None default).
type SearchParams struct {
	// Queries are free-text terms, ANDed: every term must match the title, the display
	// override, or the author. Stacking them narrows, the way stacking tags does.
	Queries []string
	// AuthorIDs are ORed, unlike every other filter here. A title has exactly one
	// author (artists beyond the first are artist-subject tags), so requiring two at
	// once could only ever return nothing. Empty (or all-zero) means any author.
	AuthorIDs []int64
	TagIDs    []int64
	// SourceSlug filters by which metadata source a title's tags came from: "" means
	// any source (including untagged), SourceNone means untagged only, and any other
	// value matches manga.source_slug exactly.
	SourceSlug string
	Sort       string
	Seed       int64 // only used when Sort == "random"; selects one stable shuffle
	Limit      int
	Offset     int
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// nonZero drops 0 ids, so a caller passing an unset id (or a stale empty slot) gets
// "any author" rather than a clause that matches nothing.
func nonZero(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id != 0 {
			out = append(out, id)
		}
	}
	return out
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
	for _, term := range p.Queries {
		if term == "" {
			continue
		}
		// Match the canonical title, the user's display override, or the author — so a
		// title is findable by its original romaji name OR by whatever it was renamed to.
		// One clause per term, all ANDed, so terms narrow rather than replace.
		where = append(where, "(m.title LIKE ? OR m.display_title LIKE ? OR a.name LIKE ?)")
		like := "%" + term + "%"
		args = append(args, like, like, like)
	}
	if ids := nonZero(p.AuthorIDs); len(ids) > 0 {
		where = append(where, "m.author_id IN ("+placeholders(len(ids))+")")
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if p.SourceSlug != "" {
		// A title backfilled by migration 007 but never re-tagged can hold '' rather
		// than NULL, so "untagged" has to cover both spellings or those rows would
		// vanish from every filter value at once.
		if p.SourceSlug == SourceNone {
			where = append(where, "(m.source_slug IS NULL OR m.source_slug = '')")
		} else {
			where = append(where, "m.source_slug = ?")
			args = append(args, p.SourceSlug)
		}
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

// SourceCount is one row of the source-provenance facet: how many titles carry a
// given source_slug. Slug is SourceNone for the never-auto-tagged bucket.
type SourceCount struct {
	Slug  string `json:"slug"`
	Count int    `json:"count"`
}

// SourceCounts returns the library's source-provenance breakdown, ordered by
// descending count then slug, with the untagged bucket (SourceNone) last regardless
// of size — it is the "everything else" row, not a source.
//
// The options come from what the library actually CONTAINS, not from the provider
// registry: a title keeps its source_slug after that source is disabled or removed
// from config, and a filter built off the enabled sources would silently offer no way
// to find those titles.
func SourceCounts(q store.Querier) ([]SourceCount, error) {
	// NULLIF folds a blank slug into the NULL bucket so untagged is counted once.
	// The ORDER BY's leading term is the untagged-last rule: `slug = ?` is 0/1 in
	// SQLite, so ASC sorts the real sources ahead of it whatever the counts are.
	rows, err := q.Query(
		"SELECT COALESCE(NULLIF(source_slug, ''), ?) AS slug, COUNT(*) AS n FROM manga "+
			"GROUP BY slug ORDER BY (slug = ?) ASC, n DESC, slug ASC",
		SourceNone, SourceNone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceCount{}
	for rows.Next() {
		var sc SourceCount
		if err := rows.Scan(&sc.Slug, &sc.Count); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// FilterOption is one pickable value in the library's filter builder: what to filter
// by (Value — a tag name, an author id, a title), what to show (Label), and how many
// titles it matches. Subject carries a tag's subject where there is one, so #touhou
// the parody is distinguishable from an artist of the same name.
type FilterOption struct {
	Value   string `json:"value"`
	Label   string `json:"label"`
	Subject string `json:"subject"`
	Count   int    `json:"count"`
}

// ListFilterOptions returns everything the builder can filter by for one chip kind
// ("tag", "author" or "title"), most-used first so opening the field shows what the
// library actually contains rather than an empty box.
//
// The whole list comes back — no limit — because the caller narrows it as the user
// types: a cap here would silently hide matches that typing should have found. A
// local library is small enough for that to be cheap.
//
// Tags and authors are INNER JOINed to their titles on purpose: a tag or author with
// nothing attached would filter down to an empty grid, so it is not offered.
func ListFilterOptions(q store.Querier, kind string) ([]FilterOption, error) {
	var query string
	switch kind {
	case "tag":
		query = `SELECT t.name, t.type, COUNT(mt.manga_id) FROM tags t
			JOIN manga_tags mt ON mt.tag_id = t.id
			GROUP BY t.id ORDER BY COUNT(mt.manga_id) DESC, t.name`
	case "author":
		query = `SELECT CAST(a.id AS TEXT), a.name, '', COUNT(m.id) FROM authors a
			JOIN manga m ON m.author_id = a.id
			GROUP BY a.id ORDER BY COUNT(m.id) DESC, a.name`
	case "title":
		// Titles are their own filter value (the query is a substring match), and each
		// stands for exactly one volume, so there is no count worth showing.
		query = `SELECT COALESCE(NULLIF(m.display_title, ''), m.title) AS t, '', 0
			FROM manga m ORDER BY t`
	default:
		return nil, fmt.Errorf("unknown filter kind %q", kind)
	}
	rows, err := q.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FilterOption{}
	for rows.Next() {
		var o FilterOption
		var err error
		if kind == "author" {
			err = rows.Scan(&o.Value, &o.Label, &o.Subject, &o.Count)
		} else {
			// Tag and title rows carry one text column that is both value and label.
			err = rows.Scan(&o.Value, &o.Subject, &o.Count)
			o.Label = o.Value
		}
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
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

// ListAuthors returns all authors ordered by name. Nothing in the UI calls this —
// the filter builder's author list comes from ListFilterOptions, which carries counts
// — but it is how the orphan-author pruning tests read back the author table.
func ListAuthors(q store.Querier) ([]Author, error) {
	rows, err := q.Query("SELECT id, name FROM authors ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
