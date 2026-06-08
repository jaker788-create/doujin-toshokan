package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"doujin/internal/config"
	"doujin/internal/doujin"
	"doujin/internal/ingest"
	"doujin/internal/scanner"
	"doujin/internal/search"
	"doujin/internal/stash"
	"doujin/internal/store"
	"doujin/internal/tag"
)

// pageSize is the default number of cards a search returns (matches the Python
// build's PAGE_SIZE; the frontend pages through with limit/offset).
const pageSize = 60

// App is the Wails-bound application. Its exported methods are the JSON API the
// frontend calls (Wails generates typed TypeScript bindings for them). It holds
// the open database and resolves the data dir on startup.
type App struct {
	ctx     context.Context
	dataDir string
	db      *sql.DB

	// autotagMu guards autotagCancel, the cancel func for an in-flight bulk
	// auto-tag run. nil means no run is active; see nhentai.go.
	autotagMu     sync.Mutex
	autotagCancel context.CancelFunc
}

// NewApp creates the App. The database is opened later in startup, once Wails has
// provided a context.
func NewApp() *App { return &App{} }

// startup resolves the data dir (DOUJIN_DATA_DIR override or %APPDATA%/doujin),
// opens the existing doujin.db, and brings the schema up to date. A failure here
// is fatal: nothing works without the database.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	dir := os.Getenv("DOUJIN_DATA_DIR")
	if dir == "" {
		d, err := config.DefaultDataDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fatal: cannot resolve data dir:", err)
			os.Exit(1)
		}
		dir = d
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "fatal: cannot create data dir:", err)
		os.Exit(1)
	}
	a.dataDir = dir

	db, err := store.Open(config.DBPath(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal: cannot open database:", err)
		os.Exit(1)
	}
	if err := store.Init(db); err != nil {
		fmt.Fprintln(os.Stderr, "fatal: cannot migrate database:", err)
		os.Exit(1)
	}
	a.db = db
}

// roots returns the configured library roots, re-read from config.json each call
// so changes take effect without a restart.
func (a *App) roots() []string {
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return nil
	}
	return cfg.LibraryRoots
}

// knownPaths is the set of folder_paths already imported, used to exclude them
// from scan results.
func (a *App) knownPaths() (map[string]bool, error) {
	rows, err := a.db.Query("SELECT folder_path FROM manga")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	known := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		known[p] = true
	}
	return known, rows.Err()
}

// isUniqueViolation reports whether err is a SQLite UNIQUE-constraint failure,
// used to silently skip a manga whose folder_path is already imported.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint")
}

// ── Bound methods (the frontend JSON API) ──────────────────────────────────

// SearchArgs are the optional browse/filter parameters. Tags are tag NAMES; they
// are resolved to ids here, mirroring the Python /api/search route.
type SearchArgs struct {
	Q        string   `json:"q"`
	AuthorID int64    `json:"author_id"`
	Tags     []string `json:"tags"`
	Sort     string   `json:"sort"`
	Seed     int64    `json:"seed"` // only used when Sort == "random"
	Limit    int      `json:"limit"`
	Offset   int      `json:"offset"`
}

// Search returns manga matching args. Unknown tag names yield no results (the AND
// filter cannot be satisfied). Paging is clamped to a sane range.
func (a *App) Search(args SearchArgs) ([]search.Manga, error) {
	var tagIDs []int64
	if len(args.Tags) > 0 {
		ids, err := search.TagIDsForNames(a.db, args.Tags)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []search.Manga{}, nil
		}
		tagIDs = ids
	}
	limit := args.Limit
	if limit <= 0 {
		limit = pageSize
	}
	if limit > 500 {
		limit = 500
	}
	offset := args.Offset
	if offset < 0 {
		offset = 0
	}
	return search.SearchManga(a.db, search.SearchParams{
		Query:    args.Q,
		AuthorID: args.AuthorID,
		TagIDs:   tagIDs,
		Sort:     args.Sort,
		Seed:     args.Seed,
		Limit:    limit,
		Offset:   offset,
	})
}

// MangaDetail is the title-page payload: the manga row, its on-disk page paths
// (empty when the folder is missing), its tags, and whether the folder is missing.
type MangaDetail struct {
	Manga   search.Manga `json:"manga"`
	Pages   []string     `json:"pages"`
	Tags    []tag.Typed  `json:"tags"`
	Missing bool         `json:"missing"`
}

// GetManga returns the detail payload for one manga, or nil if the id is unknown.
func (a *App) GetManga(id int64) (*MangaDetail, error) {
	m, err := search.GetManga(a.db, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	pages := []string{}
	_, statErr := os.Stat(m.FolderPath)
	missing := statErr != nil
	if !missing {
		pages = scanner.ListPages(m.FolderPath)
	}
	tags, err := search.GetMangaTagsTyped(a.db, id)
	if err != nil {
		return nil, err
	}
	return &MangaDetail{Manga: *m, Pages: pages, Tags: tags, Missing: missing}, nil
}

// SuggestTags returns tag-name completions for the filter builder.
func (a *App) SuggestTags(q string) ([]string, error) {
	return search.SuggestTags(a.db, q, 10)
}

// SuggestAuthors returns author {id,name} matches for the filter builder.
func (a *App) SuggestAuthors(q string) ([]search.Author, error) {
	return search.SuggestAuthors(a.db, q, 10)
}

// UpdateTags replaces a manga's tags and returns the saved set (with subjects,
// ordered) so the UI can re-render its grouped chips. The incoming names are freeform
// manual edits (General subject); any that match an existing typed tag keep that tag's
// subject (GetOrCreateTag never downgrades), so editing tags by name doesn't strip the
// subjects that nhentai or the folder parser assigned. Errors if the manga is gone.
func (a *App) UpdateTags(id int64, tags []string) ([]tag.Typed, error) {
	m, err := search.GetManga(a.db, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("manga %d not found", id)
	}
	return ingest.SetMangaTags(a.db, id, generalTags(tags))
}

// GetUnimported returns title folders found under the library roots that are not
// yet in the database.
func (a *App) GetUnimported() ([]UnimportedPreview, error) {
	known, err := a.knownPaths()
	if err != nil {
		return nil, err
	}
	found := scanner.FindUnimported(a.roots(), known)
	out := make([]UnimportedPreview, 0, len(found))
	for _, d := range found {
		// Reuse the exact import path so the preview equals what Ingest will store.
		in := mangaInputFromFolder(d, nil)
		out = append(out, UnimportedPreview{
			Folder: d,
			Title:  in.Title,
			Author: in.Author,
			Tags:   in.Tags,
		})
	}
	return out, nil
}

// mangaInputFromFolder builds an ingest input from a detected folder. It parses the
// *on-disk folder name* (folder_path's basename — the immutable source of truth, the
// same string Rescan re-derives from) rather than d.Title, so the implied tags
// (language, parody, misc — see internal/doujin) and derived author are always taken
// from the real decorated name even when the UI has replaced d.Title with the cleaned
// display title. Tags merge with any user-supplied tags.
//
//   - Title: the cleaned display title, unless d.Title has been changed from the raw
//     folder name (i.e. edited in the Scan row), in which case that edit wins.
//   - Author: the author folder when present (organized layout / UI-provided),
//     otherwise the artist/circle parsed from the name, otherwise "Unknown".
func mangaInputFromFolder(d scanner.DetectedFolder, extraTags []string) ingest.MangaInput {
	rawName := filepath.Base(d.FolderPath)
	p := doujin.ParseName(rawName)

	title := p.DisplayTitle()
	if strings.TrimSpace(title) == "" {
		title = rawName
	}
	// Honor a deliberate title edit from the Scan row: d.Title differs from the raw
	// folder name only when the user (or the preview) supplied a different value.
	if t := strings.TrimSpace(d.Title); t != "" && t != rawName {
		title = t
	}

	author := strings.TrimSpace(d.Author)
	if author == "" {
		if author = p.Author(); author == "" {
			author = "Unknown"
		}
	}

	tags := append(parsedTypedTags(p), generalTags(extraTags)...)
	return ingest.MangaInput{
		Title:        title,
		Author:       author,
		FolderPath:   d.FolderPath,
		CoverRelPath: d.CoverRelPath,
		PageCount:    d.PageCount,
		Tags:         tags,
	}
}

// parsedTypedTags maps a parsed folder name's decorations onto subjected tags: the
// language as a Language tag, each parody as a Parody tag, and the misc content tags
// (digital, decensored, …) as generic Tag-subject tags. The doujin parser stays pure
// (it knows the parts but not our subject vocabulary); this is where the two meet.
func parsedTypedTags(p doujin.Parsed) []tag.Typed {
	var out []tag.Typed
	add := func(name, typ string) {
		if name = ingest.NormalizeTag(name); name != "" {
			out = append(out, tag.Typed{Name: name, Type: typ})
		}
	}
	add(p.Language, tag.Language)
	for _, parody := range p.Parodies {
		add(parody, tag.Parody)
	}
	for _, m := range p.MiscTags {
		add(m, tag.Tag)
	}
	return out
}

// generalTags wraps freeform tag names (from the UI) as untyped/General tags.
func generalTags(names []string) []tag.Typed {
	out := make([]tag.Typed, 0, len(names))
	for _, n := range names {
		out = append(out, tag.Typed{Name: n, Type: tag.General})
	}
	return out
}

// UnimportedPreview pairs a detected folder with the parse of its name, so the Scan
// page can show — and pre-fill — the cleaned title, derived author, and implied tags
// (with their subjects) that importing it will produce, instead of the raw decorated
// folder name. Folder is passed straight back to Ingest when the user saves the row.
type UnimportedPreview struct {
	Folder scanner.DetectedFolder `json:"folder"`
	Title  string                 `json:"title"`  // cleaned display title
	Author string                 `json:"author"` // derived author (folder, else artist/circle)
	Tags   []tag.Typed            `json:"tags"`   // subjected tags implied by the name
}

// Ingest imports one detected folder (optionally with tags). A folder already
// imported (duplicate folder_path) is silently skipped, as in the Python build.
func (a *App) Ingest(d scanner.DetectedFolder, tags []string) error {
	_, err := ingest.IngestManga(a.db, mangaInputFromFolder(d, tags))
	if isUniqueViolation(err) {
		return nil
	}
	return err
}

// ImportAll imports every currently-unimported folder, skipping duplicates.
func (a *App) ImportAll() error {
	known, err := a.knownPaths()
	if err != nil {
		return err
	}
	for _, d := range scanner.FindUnimported(a.roots(), known) {
		_, err := ingest.IngestManga(a.db, mangaInputFromFolder(d, nil))
		if err != nil && !isUniqueViolation(err) {
			return err
		}
	}
	return nil
}

// Rescan re-checks every imported title against the disk: folders gone missing are
// flagged, present ones have their page_count refreshed, their display title
// re-derived from the (raw) folder name, and the tags implied by that name added
// (additively — manual and nhentai tags are never removed). Rows are fully read
// before any UPDATE because the single shared connection cannot iterate and write
// at once.
func (a *App) Rescan() error {
	rows, err := a.db.Query("SELECT id, folder_path FROM manga")
	if err != nil {
		return err
	}
	type rec struct {
		id   int64
		path string
	}
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.path); err != nil {
			rows.Close()
			return err
		}
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, r := range recs {
		if _, statErr := os.Stat(r.path); statErr != nil {
			if _, err := a.db.Exec("UPDATE manga SET missing=1 WHERE id=?", r.id); err != nil {
				return err
			}
			continue
		}
		n := len(scanner.ListPages(r.path))
		p := doujin.ParseName(filepath.Base(r.path))
		title := p.DisplayTitle()
		if strings.TrimSpace(title) == "" {
			title = filepath.Base(r.path)
		}
		if _, err := a.db.Exec(
			"UPDATE manga SET missing=0, page_count=?, title=? WHERE id=?", n, title, r.id); err != nil {
			return err
		}
		// Additively apply the tags implied by the folder name, with their subjects.
		// INSERT OR IGNORE keeps it idempotent and never disturbs existing
		// (manual/nhentai) tags; GetOrCreateTag also backfills the subject onto any tag
		// row that was still untyped, so Rescan upgrades a pre-subjects library in place.
		for _, raw := range parsedTypedTags(p) {
			name := ingest.NormalizeTag(raw.Name)
			if name == "" {
				continue
			}
			tagID, err := ingest.GetOrCreateTag(a.db, name, raw.Type)
			if err != nil {
				return err
			}
			if _, err := a.db.Exec(
				"INSERT OR IGNORE INTO manga_tags(manga_id, tag_id) VALUES (?,?)", r.id, tagID); err != nil {
				return err
			}
		}
	}
	return nil
}

// CountMissing reports how many imported titles point at a folder that is gone from
// disk (flagged by Rescan). Drives the Scan-page cleanup affordance.
func (a *App) CountMissing() (int, error) {
	var n int
	err := a.db.QueryRow("SELECT COUNT(*) FROM manga WHERE missing=1").Scan(&n)
	return n, err
}

// RemoveMissing forgets every title flagged missing (its row, tag links, and saved
// pages cascade via the schema's ON DELETE CASCADE), then prunes any author left with
// no titles. Returns how many titles were removed. Files on disk are never touched —
// this only clears DB rows whose folders you deliberately moved or deleted, which the
// index-in-place rule otherwise keeps around forever (so an unplugged drive can't erase
// your metadata).
func (a *App) RemoveMissing() (int, error) {
	res, err := a.db.Exec("DELETE FROM manga WHERE missing=1")
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := a.pruneOrphanAuthors(); err != nil {
		return int(n), err
	}
	return int(n), nil
}

// DeleteManga forgets one title by id (row + tag links + saved pages cascade), then
// prunes a now-empty author. Files are untouched; if the folder still exists it will be
// offered for import again on the next scan.
func (a *App) DeleteManga(id int64) error {
	if _, err := a.db.Exec("DELETE FROM manga WHERE id=?", id); err != nil {
		return err
	}
	return a.pruneOrphanAuthors()
}

// pruneOrphanAuthors removes authors left with no titles so the author filter stays
// free of dead entries. An author is re-created automatically on the next import, so
// this is safe to run after any deletion.
func (a *App) pruneOrphanAuthors() error {
	_, err := a.db.Exec("DELETE FROM authors WHERE id NOT IN (SELECT author_id FROM manga)")
	return err
}

// GetConfig returns the current configuration (library roots, port).
func (a *App) GetConfig() (config.Config, error) {
	return config.Load(a.dataDir)
}

// Count returns the total number of manga in the library (for the hero header).
func (a *App) Count() (int, error) {
	var n int
	err := a.db.QueryRow("SELECT COUNT(*) FROM manga").Scan(&n)
	return n, err
}

// GetAuthor resolves an author id to its row (or null), used to label the active
// author filter chip when only the id is known (e.g. restored from a hash route).
func (a *App) GetAuthor(id int64) (*search.Author, error) {
	return search.GetAuthor(a.db, id)
}

// AddLibraryRoot opens a native folder picker and, if a directory is chosen, adds
// it to the configured library roots (de-duplicated) and saves. Returns the chosen
// path, or "" if the dialog was cancelled. This is the native replacement for
// hand-editing config.json that the Python build required.
func (a *App) AddLibraryRoot() (string, error) {
	dir, err := wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "Choose a library folder",
	})
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", nil // cancelled
	}
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return "", err
	}
	for _, r := range cfg.LibraryRoots {
		if r == dir {
			return dir, nil // already configured
		}
	}
	cfg.LibraryRoots = append(cfg.LibraryRoots, dir)
	if err := config.Save(cfg, a.dataDir); err != nil {
		return "", err
	}
	return dir, nil
}

// RemoveLibraryRoot removes a path from the configured library roots and saves.
func (a *App) RemoveLibraryRoot(path string) error {
	cfg, err := config.Load(a.dataDir)
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(cfg.LibraryRoots))
	for _, r := range cfg.LibraryRoots {
		if r != path {
			kept = append(kept, r)
		}
	}
	cfg.LibraryRoots = kept
	return config.Save(cfg, a.dataDir)
}

// ── Stash: saved pages ("tabs") ────────────────────────────────────────────

// StashInput is the create payload for a saved page. Kind is "search" or "title";
// Hash is the route to restore (e.g. "/?author=3&tag=x" or "/manga/5"); MangaID 0
// means "no manga" (a search) and is stored as NULL. Page is the initial resume
// point for a title.
type StashInput struct {
	Kind    string `json:"kind"`
	Hash    string `json:"hash"`
	Label   string `json:"label"`
	MangaID int64  `json:"manga_id"`
	Page    int    `json:"page"`
}

// StashSave persists a page and returns its new id. Save, clone, and open-in-new-tab
// all funnel here — each call is an independent row (duplicates are intentional).
func (a *App) StashSave(in StashInput) (int64, error) {
	e := stash.Entry{
		Kind:     in.Kind,
		Hash:     in.Hash,
		Label:    in.Label,
		LastPage: in.Page,
	}
	if in.MangaID != 0 {
		id := in.MangaID
		e.MangaID = &id
	}
	return stash.Add(a.db, e)
}

// StashList returns every saved page, newest first, with title rows enriched by their
// manga (cover/title/author) for the Stash screen's cards.
func (a *App) StashList() ([]stash.Entry, error) {
	return stash.List(a.db)
}

// StashGet returns one saved page by id (or null), used by the reader to find a title
// tab's resume point.
func (a *App) StashGet(id int64) (*stash.Entry, error) {
	return stash.Get(a.db, id)
}

// StashSetPage records the page reached in a saved title tab, so reopening it resumes
// where the user left off. Unknown ids are a no-op.
func (a *App) StashSetPage(id int64, page int) error {
	if page < 0 {
		page = 0
	}
	return stash.SetPage(a.db, id, page)
}

// StashRemove deletes a saved page.
func (a *App) StashRemove(id int64) error {
	return stash.Remove(a.db, id)
}
