package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"doujin/internal/config"
	"doujin/internal/ingest"
	"doujin/internal/scanner"
	"doujin/internal/search"
	"doujin/internal/stash"
	"doujin/internal/store"
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
		Limit:    limit,
		Offset:   offset,
	})
}

// MangaDetail is the title-page payload: the manga row, its on-disk page paths
// (empty when the folder is missing), its tags, and whether the folder is missing.
type MangaDetail struct {
	Manga   search.Manga `json:"manga"`
	Pages   []string     `json:"pages"`
	Tags    []string     `json:"tags"`
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
	tags, err := search.GetMangaTags(a.db, id)
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

// UpdateTags replaces a manga's tags and returns the saved (normalized, sorted)
// set so the UI can re-render its chips. Errors if the manga no longer exists.
func (a *App) UpdateTags(id int64, tags []string) ([]string, error) {
	m, err := search.GetManga(a.db, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("manga %d not found", id)
	}
	return ingest.SetMangaTags(a.db, id, tags)
}

// GetUnimported returns title folders found under the library roots that are not
// yet in the database.
func (a *App) GetUnimported() ([]scanner.DetectedFolder, error) {
	known, err := a.knownPaths()
	if err != nil {
		return nil, err
	}
	return scanner.FindUnimported(a.roots(), known), nil
}

// Ingest imports one detected folder (optionally with tags). A folder already
// imported (duplicate folder_path) is silently skipped, as in the Python build.
func (a *App) Ingest(d scanner.DetectedFolder, tags []string) error {
	_, err := ingest.IngestManga(a.db, ingest.MangaInput{
		Title:        d.Title,
		Author:       d.Author,
		FolderPath:   d.FolderPath,
		CoverRelPath: d.CoverRelPath,
		PageCount:    d.PageCount,
		Tags:         tags,
	})
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
		_, err := ingest.IngestManga(a.db, ingest.MangaInput{
			Title:        d.Title,
			Author:       d.Author,
			FolderPath:   d.FolderPath,
			CoverRelPath: d.CoverRelPath,
			PageCount:    d.PageCount,
		})
		if err != nil && !isUniqueViolation(err) {
			return err
		}
	}
	return nil
}

// Rescan re-checks every imported title against the disk: folders gone missing are
// flagged, present ones have their page_count refreshed. Rows are fully read
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
		if _, err := a.db.Exec("UPDATE manga SET missing=0, page_count=? WHERE id=?", n, r.id); err != nil {
			return err
		}
	}
	return nil
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
