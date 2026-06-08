import './theme.css';
import {
    Search, Count, GetManga, GetAuthor, SuggestTags, SuggestAuthors,
    UpdateTags, GetUnimported, Ingest, ImportAll, Rescan,
    CountMissing, RemoveMissing, DeleteManga,
    GetConfig, AddLibraryRoot, RemoveLibraryRoot,
    StashSave, StashList, StashGet, StashSetPage, StashRemove,
    GetSettings, SetNhentaiKey, MatchNhentai, ApplyNhentaiTags, ApplyNhentaiMerge,
    StartAutoTag, CancelAutoTag,
} from '../wailsjs/go/main/App';
import { main, search, stash, tag } from '../wailsjs/go/models';
import { EventsOn, BrowserOpenURL } from '../wailsjs/runtime/runtime';

type Manga = search.Manga;
type MangaDetail = main.MangaDetail;
type UnimportedPreview = main.UnimportedPreview;
type Typed = tag.Typed;

// ───── tag subjects (grouped display) ─────────────────────────────
// The backend returns tags already ordered by subject then name (see internal/tag).
// These helpers group them by subject for display; General and the generic "tag"
// subject both read "Tags".
const TAG_SUBJECT_LABEL: Record<string, string> = {
    language: 'Language', artist: 'Artist', group: 'Group', parody: 'Parody',
    character: 'Character', category: 'Category', tag: 'Tags', '': 'Tags',
};
function subjectLabel(type: string): string {
    return TAG_SUBJECT_LABEL[type] ?? 'Tags';
}

// groupBySubject buckets typed tags under their subject label, preserving the order
// they arrive in (first-seen label order).
function groupBySubject(tags: Typed[]): { label: string; tags: Typed[] }[] {
    const order: string[] = [];
    const byLabel = new Map<string, Typed[]>();
    for (const t of tags) {
        const label = subjectLabel(t.type);
        if (!byLabel.has(label)) { byLabel.set(label, []); order.push(label); }
        byLabel.get(label)!.push(t);
    }
    return order.map((label) => ({ label, tags: byLabel.get(label)! }));
}

// renderTagRow renders grouped tags as clickable filter links (#name), one labelled
// group per subject. Used in the reader's tag row.
function renderTagRow(tags: Typed[]): string {
    return groupBySubject(tags).map((g) =>
        `<span class="tag-group"><span class="tag-subject">${esc(g.label)}</span>`
        + g.tags.map((t) => `<a href="#/?tag=${encodeURIComponent(t.name)}">#${esc(t.name)}</a>`).join('')
        + `</span>`).join('');
}

// renderTagChips renders grouped tags as read-only chips (no links). Used in the scan
// preview and the nhentai match preview.
function renderTagChips(tags: Typed[]): string {
    return groupBySubject(tags).map((g) =>
        `<span class="tag-group"><span class="tag-subject">${esc(g.label)}</span>`
        + g.tags.map((t) => `<span class="chip">${esc(t.name)}</span>`).join('')
        + `</span>`).join('');
}

// tagNames extracts the plain names (for the freeform edit input).
function tagNames(tags: Typed[]): string[] {
    return tags.map((t) => t.name);
}
type StashEntry = stash.Entry;

const PAGE_SIZE = 60;

// Per-id author names, captured when an author link is clicked so the active
// filter chip can show the name even after a hash navigation drops it.
const authorNames: Record<string, string> = {};
// Cleanup for the current view's document-level listeners/observers; run before
// rendering the next view so nothing leaks across views.
let viewCleanup: (() => void) | null = null;
// Close handler for an open lightbox, so route changes can dismiss it.
let closeLightbox: (() => void) | null = null;
let uid = 0;

// ───── stash / navigation memory ──────────────────────────────────
// The most recent browse hash (a library or stash view). The reader's back link
// points here so leaving a title returns to the search you came from, not "/".
let lastBrowseHash = '#/';
// The most recent bulk-sweep review queue. Kept module-level so leaving a title to
// inspect its images (the local-title link) and returning to #/autotag restores the
// queue instead of forcing a re-run — which would re-hit the rate-limited nhentai API.
// Items are dropped as they're tagged, so a cleared review never reappears.
let reviewCache: { items: main.MatchResult[]; applied: number; cancelled: boolean } | null = null;
function removeFromReviewCache(mangaId: number): void {
    if (reviewCache) reviewCache.items = reviewCache.items.filter((r) => r.manga_id !== mangaId);
}
// Per-browse-hash scroll memory: where you were (scrollY) and how many cards were
// loaded (so an infinite-scroll list can be re-filled to the same depth on return).
const scrollMemory = new Map<string, { y: number; loaded: number }>();
// Set true by a view that restored its own scroll, so render()'s scroll-to-top reset
// doesn't clobber it.
let skipScrollReset = false;
// The currently open title, exposed so the header "save current page" button can
// stash the reader (with its live page) from outside renderReader's scope.
let readerState: { id: number; title: string; page: number } | null = null;

// ───── helpers ────────────────────────────────────────────────────
function esc(s: unknown): string {
    return String(s)
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;');
}
function imageURL(path: string): string {
    return `/image?path=${encodeURIComponent(path)}`;
}
function thumbURL(path: string, w = 240): string {
    return `/thumb?path=${encodeURIComponent(path)}&w=${w}`;
}

// ───── nhentai cover (remote) ─────────────────────────────────────
// WebView2 loads nhentai's public CDN directly, so candidate covers are plain remote
// <img>s — no proxy. The cover's real extension isn't in the API, so wireCover walks
// the candidates (absolute thumbnail first, then jpg/webp/png/gif from media_id) on
// each load error and gives up to a neutral tile.
const COVER_EXTS = ['jpg', 'webp', 'png', 'gif'];
function coverCandidates(c: main.NhentaiCandidate): string[] {
    const srcs: string[] = [];
    if (c.thumbnail && /^https?:\/\//.test(c.thumbnail)) srcs.push(c.thumbnail);
    if (c.media_id) {
        const id = encodeURIComponent(c.media_id);
        for (const ext of COVER_EXTS) srcs.push(`https://t.nhentai.net/galleries/${id}/thumb.${ext}`);
    }
    return srcs;
}
function wireCover(img: HTMLImageElement, c: main.NhentaiCandidate): void {
    const srcs = coverCandidates(c);
    let i = 0;
    const next = () => {
        if (i >= srcs.length) { img.classList.add('nh-cover-missing'); img.removeAttribute('src'); return; }
        img.src = srcs[i++];
    };
    img.addEventListener('error', next);
    next(); // listener attached first, so a failed first src advances correctly
}

// renderMatchBadges turns a candidate's scoring signals into compact "why-match"
// badges: page match, title %, language match/mismatch, and artist/parody overlap.
function renderMatchBadges(c: main.NhentaiCandidate): string {
    const b: string[] = [];
    if (c.pages_exact) b.push(`<span class="nh-badge ok">✓ exact pages</span>`);
    else if (c.page_delta >= 0) b.push(`<span class="nh-badge">±${c.page_delta} pages</span>`);
    b.push(`<span class="nh-badge${c.title_score >= 0.85 ? ' ok' : ''}">title ${Math.round(c.title_score * 100)}%</span>`);
    if (c.lang_match) b.push(`<span class="nh-badge ok">✓ ${esc(c.language || 'language')}</span>`);
    else if (c.lang_mismatch) b.push(`<span class="nh-badge warn">≠ ${esc(c.language || 'language')}</span>`);
    if (c.artist_match) b.push(`<span class="nh-badge ok">✓ artist</span>`);
    if (c.parody_match) b.push(`<span class="nh-badge ok">✓ parody</span>`);
    return `<div class="nh-badges">${b.join('')}</div>`;
}
function viewEl(): HTMLElement {
    return document.getElementById('view')!;
}

// ───── toast ──────────────────────────────────────────────────────
const toastRegion = document.getElementById('toast-region')!;
function toast(msg: string, kind: 'ok' | 'err' = 'ok'): void {
    const el = document.createElement('div');
    el.className = 'toast' + (kind === 'err' ? ' toast-err' : '');
    el.textContent = msg;
    toastRegion.appendChild(el);
    requestAnimationFrame(() => requestAnimationFrame(() => el.classList.add('in')));
    setTimeout(() => {
        el.classList.remove('in');
        setTimeout(() => el.remove(), 400);
    }, 3000);
}

interface Filter {
    titleText: string;
    authorId: string;
    authorName: string;
    tags: string[];
    sort: string;
    seed: string; // only set when sort === 'random'; pins one stable shuffle
}

// ───── router ─────────────────────────────────────────────────────
type Route =
    | { name: 'reader'; id: number; stashId?: number }
    | { name: 'scan' }
    | { name: 'stash' }
    | { name: 'autotag' }
    | { name: 'library'; params: URLSearchParams };

function parseRoute(): Route {
    const raw = location.hash.replace(/^#/, '') || '/';
    const qi = raw.indexOf('?');
    const path = qi >= 0 ? raw.slice(0, qi) : raw;
    const query = qi >= 0 ? raw.slice(qi + 1) : '';
    if (path === '/scan') return { name: 'scan' };
    if (path === '/stash') return { name: 'stash' };
    if (path === '/autotag') return { name: 'autotag' };
    const m = path.match(/^\/manga\/(\d+)$/);
    if (m) {
        // A title may carry ?stash=<id> when opened from a saved title tab, so the
        // reader knows which entry to resume from and write progress back to.
        const sid = new URLSearchParams(query).get('stash');
        return { name: 'reader', id: parseInt(m[1], 10), stashId: sid ? parseInt(sid, 10) : undefined };
    }
    return { name: 'library', params: new URLSearchParams(query) };
}

async function render(): Promise<void> {
    if (closeLightbox) closeLightbox();
    if (viewCleanup) { viewCleanup(); viewCleanup = null; }
    const r = parseRoute();
    try {
        if (r.name === 'reader') await renderReader(r.id, r.stashId);
        else if (r.name === 'scan') await renderScan();
        else if (r.name === 'stash') await renderStash();
        else if (r.name === 'autotag') await renderAutotag();
        else await renderLibrary(r.params);
    } catch (e) {
        console.error(e);
        viewEl().innerHTML = `<p class="empty">Something went wrong. <a href="#/">back to the archive</a></p>`;
    }
    // Views that restore a remembered scroll position (e.g. returning from a title)
    // opt out of the reset so we don't yank the page back to the top.
    if (skipScrollReset) skipScrollReset = false;
    else window.scrollTo(0, 0);
}

// A fresh shuffle seed in [0, 2^31). Kept below 2^31 so the backend's seeded
// order key ((id + seed) * C) stays well within int64.
function newSeed(): string { return String(Math.floor(Math.random() * 2147483647)); }

function navigateToFilter(f: Filter): void {
    const p = new URLSearchParams();
    if (f.titleText) p.set('q', f.titleText);
    p.set('sort', f.sort);
    if (f.sort === 'random' && f.seed) p.set('seed', f.seed);
    if (f.authorId) p.set('author', f.authorId);
    f.tags.filter(Boolean).forEach((t) => p.append('tag', t));
    const target = '#/?' + p.toString();
    if (location.hash === target) render();
    else location.hash = target;
}

// ───── library view ───────────────────────────────────────────────
function cardHtml(m: Manga): string {
    const cover = m.cover_rel_path
        ? `<img loading="lazy" src="${thumbURL(m.folder_path + '/' + m.cover_rel_path)}" alt="">`
        : `<div class="nocover"></div>`;
    return `<div class="card${m.missing ? ' missing' : ''}">
        <a class="card-main" href="#/manga/${m.id}">
            <div class="card-cover">${cover}</div>
            <div class="meta"><span class="t">${esc(m.title)}</span></div>
        </a>
        <a class="a author-link" href="#/?author=${m.author_id}" data-author-name="${esc(m.author_name)}">${esc(m.author_name)}</a>
    </div>`;
}

function libraryMarkup(total: number, sort: string): string {
    const sel = (v: string) => (sort === v ? ' selected' : '');
    const skeletons = Array.from({ length: 6 }, () =>
        `<div class="card skeleton" aria-hidden="true"><div class="card-cover"></div><div class="meta"></div></div>`).join('');
    return `
    <section class="hero">
        <p class="eyebrow">The Archive</p>
        <h1 class="hero-title">${total} <span>volume${total === 1 ? '' : 's'}</span></h1>
    </section>
    <div class="builder">
        <div class="builder-row">
            <select class="builder-type" aria-label="Filter type">
                <option value="title">Title</option>
                <option value="author">Author</option>
                <option value="tag">Tag</option>
            </select>
            <input class="builder-value" type="text" autocomplete="off" placeholder="Add a filter…" aria-label="Filter value" list="builder-suggest">
            <datalist id="builder-suggest"></datalist>
            <button type="button" class="btn builder-add">Add</button>
            <button type="button" class="btn btn-primary builder-run">Search</button>
        </div>
        <div class="builder-foot">
            <div class="builder-chips" aria-live="polite"></div>
            <button type="button" class="btn builder-shuffle${sort === 'random' ? ' is-on' : ''}" aria-pressed="${sort === 'random'}" title="Shuffle results">
                <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M16 3h5v5M4 20 21 3M21 16v5h-5M15 15l6 6M4 4l5 5"/></svg>Shuffle
            </button>
            <label class="builder-sortwrap">Sort by
                <select class="builder-sort" aria-label="Sort by">
                    <option value="title"${sel('title')}>Title</option>
                    <option value="author"${sel('author')}>Author</option>
                    <option value="date"${sel('date')}>Newest</option>
                </select>
            </label>
        </div>
    </div>
    <div class="grid" id="grid">${skeletons}</div>
    <div id="scroll-sentinel"></div>`;
}

async function renderLibrary(params: URLSearchParams): Promise<void> {
    const filter: Filter = {
        titleText: params.get('q') || '',
        authorId: params.get('author') || '',
        authorName: '',
        tags: params.getAll('tag').filter(Boolean),
        sort: params.get('sort') || 'title',
        seed: params.get('seed') || '',
    };
    // A 'random' route must carry a seed so every infinite-scroll page (and a
    // scroll-restore on return) sees the same shuffle; mint one if absent.
    if (filter.sort === 'random' && !filter.seed) filter.seed = newSeed();
    if (filter.authorId) {
        filter.authorName = authorNames[filter.authorId] || '';
        if (!filter.authorName) {
            try {
                const a = await GetAuthor(parseInt(filter.authorId, 10));
                if (a) { filter.authorName = a.name; authorNames[filter.authorId] = a.name; }
            } catch { /* show the id */ }
        }
    }

    let total = 0;
    try { total = await Count(); } catch { /* ignore */ }

    viewEl().innerHTML = libraryMarkup(total, filter.sort);
    const grid = document.getElementById('grid')!;
    const sentinel = document.getElementById('scroll-sentinel')!;

    let offset = 0;
    let done = false;
    let errored = false;
    // One page load may be in flight at a time; both the observer and the restore
    // loop share the same promise so they never double-fetch the same offset.
    let inflight: Promise<void> | null = null;

    function showRetry(): void {
        if (grid.querySelector('.error-pill')) return;
        const pill = document.createElement('p');
        pill.className = 'error-pill';
        pill.innerHTML = `Couldn't load more. <button type="button">Retry</button>`;
        pill.querySelector('button')!.addEventListener('click', () => {
            pill.remove();
            errored = false;
            pump();
        });
        grid.appendChild(pill);
    }

    async function fetchPage(): Promise<void> {
        try {
            const data = await Search({
                q: filter.titleText,
                author_id: filter.authorId ? parseInt(filter.authorId, 10) : 0,
                tags: filter.tags,
                sort: filter.sort,
                seed: filter.seed ? parseInt(filter.seed, 10) : 0,
                limit: PAGE_SIZE,
                offset,
            });
            grid.querySelectorAll('.card.skeleton').forEach((el) => el.remove());
            if (offset === 0 && data.length === 0) {
                grid.innerHTML = `<p class="empty">No matches. <a href="#/">clear filters</a></p>`;
            } else {
                grid.insertAdjacentHTML('beforeend', data.map((m) => cardHtml(m)).join(''));
            }
            offset += data.length;
            if (data.length < PAGE_SIZE) done = true;
        } catch (e) {
            console.error(e);
            errored = true;
            showRetry();
        }
    }

    function loadMore(): Promise<void> {
        if (inflight) return inflight;
        if (done || errored) return Promise.resolve();
        inflight = fetchPage().finally(() => { inflight = null; });
        return inflight;
    }

    // Load until the sentinel is pushed below the fold (or the list is exhausted).
    async function fillViewport(): Promise<void> {
        while (!done && !errored && sentinel.getBoundingClientRect().top < window.innerHeight) {
            await loadMore();
        }
    }
    const pump = () => { loadMore().then(fillViewport); };

    const saved = scrollMemory.get(location.hash);
    if (saved) {
        scrollMemory.delete(location.hash);
        skipScrollReset = true; // we'll restore the scroll ourselves once re-filled
        void (async () => {
            await loadMore();
            while (offset < saved.loaded && !done && !errored) await loadMore();
            window.scrollTo(0, saved.y);
        })();
    } else {
        pump();
    }

    const io = new IntersectionObserver((entries) => {
        if (entries.some((e) => e.isIntersecting)) pump();
    });
    io.observe(sentinel);

    // Remember where we are the moment a title card is opened, so the reader's back
    // link can drop us right back here (filters + scroll depth).
    grid.addEventListener('click', (e) => {
        if ((e.target as HTMLElement).closest('.card-main')) {
            scrollMemory.set(location.hash, { y: window.scrollY, loaded: offset });
        }
    }, { capture: true });

    wireBuilder(filter);
    lastBrowseHash = location.hash;
    viewCleanup = () => io.disconnect();
}

function wireBuilder(filter: Filter): void {
    const builder = viewEl().querySelector('.builder')!;
    const typeSel = builder.querySelector('.builder-type') as HTMLSelectElement;
    const valueInput = builder.querySelector('.builder-value') as HTMLInputElement;
    const addBtn = builder.querySelector('.builder-add') as HTMLButtonElement;
    const runBtn = builder.querySelector('.builder-run') as HTMLButtonElement;
    const sortSel = builder.querySelector('.builder-sort') as HTMLSelectElement;
    const chipsTray = builder.querySelector('.builder-chips') as HTMLElement;
    const datalist = builder.querySelector('#builder-suggest') as HTMLDataListElement;
    let authorByName: Record<string, number> = {};
    let lastAuthorName = '';

    function chipHtml(kind: string, label: string, value?: string): string {
        const v = value === undefined ? '' : ` data-value="${esc(value)}"`;
        return `<span class="chip" data-kind="${esc(kind)}"${v}>${esc(label)}<a href="#" class="chip-x" aria-label="Remove filter">×</a></span>`;
    }
    function renderChips(): void {
        const out: string[] = [];
        if (filter.titleText) out.push(chipHtml('title', 'title: ' + filter.titleText));
        if (filter.authorId) out.push(chipHtml('author', 'author: ' + (filter.authorName || filter.authorId)));
        filter.tags.forEach((t) => out.push(chipHtml('tag', '#' + t, t)));
        chipsTray.innerHTML = out.join('');
    }
    renderChips();

    chipsTray.addEventListener('click', (e) => {
        const x = (e.target as HTMLElement).closest('.chip-x');
        if (!x) return;
        e.preventDefault();
        const ch = x.closest('.chip') as HTMLElement;
        const kind = ch.dataset.kind;
        if (kind === 'title') filter.titleText = '';
        else if (kind === 'author') { filter.authorId = ''; filter.authorName = ''; }
        else if (kind === 'tag') filter.tags = filter.tags.filter((t) => t !== ch.dataset.value);
        renderChips();
    });

    let suggestTimer: number | undefined;
    async function fetchSuggestions(type: string, token: string): Promise<void> {
        if (!token) return;
        try {
            if (type === 'author') {
                const data = await SuggestAuthors(token);
                authorByName = {};
                data.forEach((a) => { authorByName[a.name] = a.id; });
                datalist.innerHTML = data.map((a) => `<option value="${esc(a.name)}">`).join('');
            } else if (type === 'tag') {
                const data = await SuggestTags(token);
                datalist.innerHTML = data.map((n) => `<option value="${esc(n)}">`).join('');
            }
        } catch { /* suggestions are best-effort */ }
    }

    valueInput.addEventListener('input', () => {
        datalist.innerHTML = '';
        if (typeSel.value === 'title') return;
        window.clearTimeout(suggestTimer);
        suggestTimer = window.setTimeout(() => fetchSuggestions(typeSel.value, valueInput.value.trim()), 150);
    });
    typeSel.addEventListener('change', () => {
        datalist.innerHTML = '';
        valueInput.placeholder = typeSel.value === 'title' ? 'Title text…'
            : typeSel.value === 'author' ? 'Author name…' : 'Tag…';
        valueInput.focus();
    });

    async function resolveAuthorId(name: string): Promise<number | null> {
        if (authorByName[name] != null) { lastAuthorName = name; return authorByName[name]; }
        try {
            const data = await SuggestAuthors(name);
            const hit = data.find((a) => a.name.toLowerCase() === name.toLowerCase())
                || (data.length === 1 ? data[0] : null);
            if (hit) { lastAuthorName = hit.name; return hit.id; }
        } catch { /* fall through to "no match" */ }
        return null;
    }

    async function addCurrent(): Promise<void> {
        const type = typeSel.value;
        const raw = valueInput.value.trim();
        if (!raw) return;
        if (type === 'title') {
            filter.titleText = raw;
        } else if (type === 'tag') {
            const t = raw.toLowerCase();
            if (!filter.tags.includes(t)) filter.tags.push(t);
        } else if (type === 'author') {
            const id = await resolveAuthorId(raw);
            if (!id) { toast('No author matches “' + raw + '”', 'err'); return; }
            filter.authorId = String(id);
            filter.authorName = lastAuthorName || raw;
            authorNames[filter.authorId] = filter.authorName;
        }
        valueInput.value = '';
        datalist.innerHTML = '';
        renderChips();
    }
    addBtn.addEventListener('click', () => { addCurrent(); });
    valueInput.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') { e.preventDefault(); addCurrent(); }
    });

    async function commit(): Promise<void> {
        if (valueInput.value.trim()) await addCurrent();
        navigateToFilter(filter);
    }
    runBtn.addEventListener('click', () => { commit(); });
    // Picking an explicit sort exits shuffle (drop the seed so it leaves the URL).
    sortSel.addEventListener('change', () => { filter.sort = sortSel.value; filter.seed = ''; commit(); });
    // Shuffle: switch to random and mint a fresh seed. Clicking again re-rolls.
    const shuffleBtn = builder.querySelector('.builder-shuffle') as HTMLButtonElement;
    shuffleBtn.addEventListener('click', () => {
        filter.sort = 'random';
        filter.seed = newSeed();
        commit();
    });
}

// ───── reader view ────────────────────────────────────────────────
function readerMarkup(d: MangaDetail, backHref: string, backLabel: string): string {
    const m = d.manga;
    const tagrow = renderTagRow(d.tags);
    const gallery = d.pages.map((p, i) =>
        `<img loading="lazy" src="${imageURL(p)}" alt="page ${i + 1}" data-page="${i + 1}">`).join('');
    const counter = d.pages.length
        ? `<div class="reader-counter" data-total="${d.pages.length}"><span class="cur">1</span><span class="sep">/</span><span class="tot">${d.pages.length}</span></div>
           <aside class="reader-help"><kbd>←</kbd><kbd>→</kbd> page · <kbd>F</kbd> fit · <kbd>⌫</kbd> back</aside>`
        : '';
    const notice = d.missing
        ? `<div class="notice">Folder is missing on disk: ${esc(m.folder_path)}
             <button type="button" class="btn btn-danger" data-remove-manga>Remove from library</button></div>`
        : '';
    return `
    <a class="back-link" href="${esc(backHref)}">${esc(backLabel)}</a>
    <a class="reader-back" href="${esc(backHref)}" aria-label="${esc(backLabel)}" title="${esc(backLabel)}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M19 12H5M11 6l-6 6 6 6"/></svg></a>
    <header class="title-header">
        <h1>${esc(m.title)}</h1>
        <p class="byline">by <a class="author author-link" href="#/?author=${m.author_id}" data-author-name="${esc(m.author_name)}">${esc(m.author_name)}</a><span class="sep">·</span>${m.page_count} pages</p>
        <div class="tags-block" data-manga="${m.id}">
            <p class="tagrow" id="tagrow">${tagrow}</p>
            <form class="tag-edit" hidden>
                <input class="tag-input" name="tags" value="${esc(tagNames(d.tags).join(', '))}" placeholder="comma, separated, tags" autocomplete="off" list="tag-suggest">
                <datalist id="tag-suggest"></datalist>
                <div class="tag-edit-actions">
                    <button type="submit" class="btn btn-primary">Save tags</button>
                    <button type="button" class="btn tag-edit-cancel">Cancel</button>
                </div>
            </form>
            <div class="tag-actions">
                <button type="button" class="tag-edit-toggle btn">${d.tags.length ? 'Edit tags' : '+ Add tags'}</button>
                <button type="button" class="nh-fetch btn">Fetch tags (nhentai)</button>
            </div>
            <div class="nh-panel" hidden></div>
        </div>
        ${notice}
    </header>
    <div class="gallery">${gallery}</div>
    ${counter}`;
}

async function renderReader(id: number, stashId?: number): Promise<void> {
    // If opened from a saved title tab, load its entry so we can resume at and write
    // back the last page read.
    const stashEntry = stashId ? await StashGet(stashId).catch(() => null) : null;
    const detail = await GetManga(id);
    if (!detail) {
        viewEl().innerHTML = `<p class="empty">Not found. <a href="#/">back to the archive</a></p>`;
        return;
    }
    authorNames[String(detail.manga.author_id)] = detail.manga.author_name;
    // The back link returns to wherever we last browsed (search results or the stash),
    // not a blanket "/". Bare-home gets the original "The Archive" wording.
    const backHref = lastBrowseHash || '#/';
    const backLabel = backHref === '#/' || backHref === '#'
        ? '← The Archive'
        : backHref.startsWith('#/stash') ? '← Back to stash'
        : backHref.startsWith('#/autotag') ? '← Back to review'
        : '← Back to results';
    viewEl().innerHTML = readerMarkup(detail, backHref, backLabel);

    // A missing title can be removed from the library here (its folder is gone); files
    // are never touched, so a present folder would just be re-offered for import.
    const removeBtn = viewEl().querySelector('[data-remove-manga]') as HTMLButtonElement | null;
    removeBtn?.addEventListener('click', async () => {
        removeBtn.disabled = true;
        try {
            await DeleteManga(id);
            toast('Removed from library');
            location.hash = backHref;
        } catch (err) {
            console.error(err);
            toast('Could not remove title', 'err');
            removeBtn.disabled = false;
        }
    });

    const pageImgs = Array.from(viewEl().querySelectorAll<HTMLImageElement>('.gallery img'));
    const counterCur = viewEl().querySelector('.reader-counter .cur') as HTMLElement | null;
    const helpHint = viewEl().querySelector('.reader-help') as HTMLElement | null;
    let currentIdx = 0;
    let helpShown = false;
    let io: IntersectionObserver | null = null;
    let saveTimer: number | undefined;

    // Track the open title so the header save button can stash it with its live page.
    readerState = { id, title: detail.manga.title, page: 0 };

    // Resume affordance for a saved title tab: a dismissable pill that jumps to the
    // page you left off on.
    if (stashEntry && stashEntry.last_page > 0 && pageImgs.length > 0) {
        const pill = document.createElement('button');
        pill.type = 'button';
        pill.className = 'resume-pill';
        pill.textContent = `↩ Resume page ${stashEntry.last_page + 1}`;
        pill.addEventListener('click', () => {
            scrollToPage(stashEntry.last_page);
            pill.remove();
        });
        viewEl().appendChild(pill);
        setTimeout(() => pill.classList.add('visible'), 50);
        setTimeout(() => pill.remove(), 8000);
    }

    const scrollToPage = (i: number) => {
        const target = pageImgs[Math.max(0, Math.min(pageImgs.length - 1, i))];
        if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
    };
    const preloadNeighbors = (i: number) => {
        [i + 1, i + 2].forEach((j) => {
            const img = pageImgs[j];
            if (img && img.src) { const p = new Image(); p.src = img.src; }
        });
    };
    const showHelp = () => {
        if (helpShown || !helpHint) return;
        helpShown = true;
        helpHint.classList.add('visible');
        setTimeout(() => helpHint.classList.remove('visible'), 3500);
    };

    if (counterCur && 'IntersectionObserver' in window) {
        const cur = counterCur;
        let pending = false;
        const visibility = new Map<Element, number>();
        io = new IntersectionObserver((entries) => {
            entries.forEach((e) => visibility.set(e.target, e.intersectionRatio));
            if (pending) return;
            pending = true;
            requestAnimationFrame(() => {
                pending = false;
                let bestRatio = 0;
                let bestIdx = currentIdx;
                pageImgs.forEach((img, i) => {
                    const r = visibility.get(img) || 0;
                    if (r > bestRatio) { bestRatio = r; bestIdx = i; }
                });
                if (bestIdx !== currentIdx) {
                    currentIdx = bestIdx;
                    cur.textContent = String(bestIdx + 1);
                    preloadNeighbors(bestIdx);
                    if (readerState) readerState.page = bestIdx;
                    // Persist reading progress for a saved title tab, debounced so a
                    // fast scroll doesn't spam the backend.
                    if (stashId) {
                        window.clearTimeout(saveTimer);
                        saveTimer = window.setTimeout(() => { StashSetPage(stashId, bestIdx); }, 500);
                    }
                }
            });
        }, { threshold: [0, 0.25, 0.5, 0.75, 1] });
        pageImgs.forEach((img) => io!.observe(img));
    }

    const onScrollOnce = () => showHelp();
    window.addEventListener('scroll', onScrollOnce, { once: true, passive: true });

    // Floating back icon: stays out of the way. It surfaces only while you're
    // actively scrolling (then fades a couple seconds after you stop) or while the
    // mouse is near the top edge. Faint by default, full opacity near the top/hover.
    const backFloat = viewEl().querySelector('.reader-back') as HTMLElement | null;
    let nearTop = false;
    let hideTimer: number | undefined;
    const setShown = (on: boolean) => {
        if (backFloat && backFloat.classList.contains('visible') !== on) {
            backFloat.classList.toggle('visible', on);
        }
    };
    // After scroll activity stops, fade out — unless the mouse is holding it open.
    const armHide = () => {
        window.clearTimeout(hideTimer);
        hideTimer = window.setTimeout(() => { if (!nearTop) setShown(false); }, 2500);
    };
    const onBackScroll = () => { setShown(true); armHide(); };
    const onBackMouse = (e: MouseEvent) => {
        const near = e.clientY < 120;
        if (near === nearTop) return;
        nearTop = near;
        backFloat?.classList.toggle('near', near);
        if (near) { window.clearTimeout(hideTimer); setShown(true); }
        else armHide(); // mouse left the top zone: fade unless scrolling resumes
    };
    window.addEventListener('scroll', onBackScroll, { passive: true });
    window.addEventListener('mousemove', onBackMouse, { passive: true });

    const onKey = (e: KeyboardEvent) => {
        if (document.querySelector('.lightbox')) return;
        const t = e.target as HTMLElement | null;
        if (t && t.matches && t.matches('input, textarea, select')) return;
        if (e.key === 'ArrowLeft' || e.key === 'k' || e.key === 'PageUp') {
            e.preventDefault(); scrollToPage(currentIdx - 1); showHelp();
        } else if (e.key === 'ArrowRight' || e.key === 'j' || e.key === 'PageDown' || e.key === ' ') {
            e.preventDefault(); scrollToPage(currentIdx + 1); showHelp();
        } else if (e.key === 'f' || e.key === 'F') {
            e.preventDefault();
            if (document.body.dataset.fit === 'height') delete document.body.dataset.fit;
            else document.body.dataset.fit = 'height';
            showHelp();
        } else if (e.key === 'Backspace') {
            // Return to the list/results. preventDefault stops the WebView's own
            // history-back; the input/lightbox guards above keep tag-editing safe.
            e.preventDefault();
            location.hash = backHref;
        }
    };
    document.addEventListener('keydown', onKey);

    pageImgs.forEach((img, i) => img.addEventListener('click', () => openLightbox(pageImgs, i)));
    wireTagEditor(id);

    viewCleanup = () => {
        document.removeEventListener('keydown', onKey);
        window.removeEventListener('scroll', onScrollOnce);
        window.removeEventListener('scroll', onBackScroll);
        window.removeEventListener('mousemove', onBackMouse);
        window.clearTimeout(hideTimer);
        io?.disconnect();
        window.clearTimeout(saveTimer);
        // Flush the final page so leaving mid-scroll doesn't lose the last move.
        if (stashId && readerState) StashSetPage(stashId, readerState.page);
        readerState = null;
        delete document.body.dataset.fit;
    };
}

function wireTagEditor(id: number): void {
    const block = viewEl().querySelector('.tags-block') as HTMLElement | null;
    if (!block) return;
    const toggle = block.querySelector('.tag-edit-toggle') as HTMLButtonElement;
    const form = block.querySelector('.tag-edit') as HTMLFormElement;
    const cancel = block.querySelector('.tag-edit-cancel') as HTMLButtonElement;
    const input = block.querySelector('.tag-input') as HTMLInputElement;
    const row = block.querySelector('#tagrow') as HTMLElement;
    const datalist = block.querySelector('#tag-suggest') as HTMLDataListElement;

    // Render a saved (subject-ordered) tag list into the row, the edit input, and the
    // toggle label. Shared by the manual save and the nhentai apply flows. The row
    // shows grouped subjects; the input stays a flat, editable list of names.
    const renderTags = (saved: Typed[]) => {
        input.value = tagNames(saved).join(', ');
        row.innerHTML = renderTagRow(saved);
        toggle.textContent = saved.length ? 'Edit tags' : '+ Add tags';
    };

    toggle.addEventListener('click', () => {
        form.hidden = false;
        toggle.hidden = true;
        input.focus();
        input.setSelectionRange(input.value.length, input.value.length);
    });
    cancel.addEventListener('click', () => { form.hidden = true; toggle.hidden = false; });

    let timer: number | undefined;
    input.addEventListener('input', () => {
        const token = (input.value.split(',').pop() || '').trim();
        if (!token) return;
        window.clearTimeout(timer);
        timer = window.setTimeout(async () => {
            try {
                const names = await SuggestTags(token);
                datalist.innerHTML = names.map((n) => `<option value="${esc(n)}">`).join('');
            } catch { /* best-effort */ }
        }, 150);
    });

    form.addEventListener('submit', async (e) => {
        e.preventDefault();
        const btn = form.querySelector('button[type=submit]') as HTMLButtonElement;
        btn.disabled = true;
        try {
            const requested = input.value.split(',').map((t) => t.trim()).filter(Boolean);
            const saved = await UpdateTags(id, requested); // server normalizes + sorts
            renderTags(saved);
            form.hidden = true;
            toggle.hidden = false;
            toast('Tags saved');
        } catch (err) {
            console.error(err);
            toast("Couldn't save tags", 'err');
        } finally {
            btn.disabled = false;
        }
    });

    // nhentai: fetch candidate galleries and let the user apply one's tags. The picker
    // renders inline below the buttons; applying merges the gallery's tags with any
    // existing ones (server-side union) and re-renders the row.
    const nhBtn = block.querySelector('.nh-fetch') as HTMLButtonElement | null;
    const nhPanel = block.querySelector('.nh-panel') as HTMLElement | null;
    if (nhBtn && nhPanel) {
        nhBtn.addEventListener('click', async () => {
            nhBtn.disabled = true;
            const label = nhBtn.textContent;
            nhBtn.textContent = 'Searching nhentai…';
            try {
                const res = await MatchNhentai(id);
                nhPanel.innerHTML = '';
                nhPanel.appendChild(buildMatchPicker(id, res, (saved) => {
                    renderTags(saved);
                    nhPanel.hidden = true;
                    nhPanel.innerHTML = '';
                }));
                nhPanel.hidden = false;
            } catch (err) {
                console.error(err);
                toast(nhErr(err), 'err');
            } finally {
                nhBtn.disabled = false;
                nhBtn.textContent = label;
            }
        });
    }
}

// nhErr maps a backend error to a friendly message, special-casing the missing-key
// case so the user knows where to fix it.
function nhErr(err: unknown): string {
    const msg = String((err as { message?: string } | undefined)?.message || err || '');
    if (msg.toLowerCase().includes('api key')) return 'Add your nhentai API key on the Scan page first';
    return 'nhentai request failed — try again';
}

// buildMatchPicker renders the local title's cover beside the ranked candidates for one
// title. onApplied fires with the saved tag list after a successful apply. Used by the
// reader and the bulk review queue alike. On an auto decision it shows a one-click
// "Apply matched tags" that merges the whole qualifying set; each candidate can still be
// applied on its own or opened on nhentai for a visual check.
function buildMatchPicker(
    mangaId: number,
    res: main.MatchResult,
    onApplied: (saved: Typed[]) => void,
): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'nh-picker';
    if (res.decision === 'none' || !res.candidates.length) {
        wrap.innerHTML = `<p class="nh-empty">No nhentai matches found.</p>`;
        return wrap;
    }
    const auto = res.decision === 'auto';

    const localCover = res.cover_rel_path
        ? `<img class="nh-cover nh-local-cover" src="${thumbURL(res.folder_path + '/' + res.cover_rel_path)}" alt="">`
        : `<div class="nh-cover nh-cover-missing"></div>`;
    const localLang = res.local_language ? `<span class="nh-chip">${esc(res.local_language)}</span>` : '';
    const localAuthor = res.local_author ? `<span class="nh-local-author">${esc(res.local_author)}</span>` : '';
    const localTags = (res.local_tags && res.local_tags.length)
        ? `<div class="nh-tags nh-local-tags">${renderTagChips(res.local_tags.slice(0, 24))}${
            res.local_tags.length > 24 ? `<span class="nh-more">+${res.local_tags.length - 24} more</span>` : ''}</div>`
        : `<div class="nh-local-tags nh-local-notags">no tags yet</div>`;
    const mergeCount = res.merge_gallery_ids?.length || 0;
    const headHTML = auto
        ? `<div class="nh-auto"><p class="nh-picker-head">Auto-matched — merges tags from ${mergeCount} galler${mergeCount === 1 ? 'y' : 'ies'}.</p>
             <button type="button" class="btn btn-primary nh-merge">Apply matched tags</button></div>`
        : `<p class="nh-picker-head">Multiple possible matches — pick the right one:</p>`;

    wrap.innerHTML = `
        <div class="nh-local">${localCover}
            <div class="nh-local-cap"><span class="nh-local-label">Your copy</span>
                ${localAuthor}
                <span class="nh-meta">${res.local_pages}p</span>${localLang}
                ${localTags}</div>
        </div>
        ${headHTML}
        <div class="nh-cands"></div>`;
    const list = wrap.querySelector('.nh-cands') as HTMLElement;
    const disableAll = (on: boolean) =>
        wrap.querySelectorAll('button').forEach((b) => ((b as HTMLButtonElement).disabled = on));

    // One-click merge-apply for the auto case.
    const mergeBtn = wrap.querySelector('.nh-merge') as HTMLButtonElement | null;
    if (mergeBtn) {
        mergeBtn.addEventListener('click', async () => {
            disableAll(true);
            mergeBtn.textContent = 'Applying…';
            try {
                const saved = await ApplyNhentaiMerge(mangaId, res.merge_gallery_ids);
                toast('Tags applied from nhentai');
                onApplied(saved);
            } catch (err) {
                console.error(err);
                toast(nhErr(err), 'err');
                disableAll(false);
                mergeBtn.textContent = 'Apply matched tags';
            }
        });
    }

    res.candidates.forEach((c) => {
        const merged = (res.merge_gallery_ids || []).includes(c.gallery_id);
        const pages = c.pages_exact
            ? `<span class="nh-pages ok">${c.num_pages}p · exact (vs ${res.local_pages}p)</span>`
            : `<span class="nh-pages">${c.num_pages}p (you have ${res.local_pages}p)</span>`;
        const jp = c.japanese_title ? `<span class="nh-jp">${esc(c.japanese_title)}</span>` : '';
        const tags = (c.tags && c.tags.length)
            ? `<div class="nh-tags">${renderTagChips(c.tags.slice(0, 24))}${
                c.tags.length > 24 ? `<span class="nh-more">+${c.tags.length - 24} more</span>` : ''}</div>`
            : '';
        const row = document.createElement('div');
        row.className = 'nh-cand'
            + (merged ? ' merged' : '')
            + (c.artist_match ? ' artist-match' : '')
            + (c.parody_match ? ' parody-match' : '');
        row.innerHTML = `
            <div class="nh-cover-wrap"><img class="nh-cover"></div>
            <div class="nh-cand-main">
                <button type="button" class="nh-en nh-link" title="Open on nhentai">${esc(c.english_title || c.japanese_title || ('gallery #' + c.gallery_id))}</button>
                ${jp}
                <span class="nh-meta">${pages} · ♥ ${c.num_favorites} · #${c.gallery_id}</span>
                ${renderMatchBadges(c)}
                ${tags}
            </div>
            <button type="button" class="btn nh-apply">${auto ? 'Use only this' : 'Apply tags'}</button>`;
        wireCover(row.querySelector('.nh-cover') as HTMLImageElement, c);
        (row.querySelector('.nh-link') as HTMLButtonElement).addEventListener('click', () => {
            if (c.gallery_url) BrowserOpenURL(c.gallery_url);
        });
        const applyBtn = row.querySelector('.nh-apply') as HTMLButtonElement;
        applyBtn.addEventListener('click', async () => {
            disableAll(true);
            applyBtn.textContent = 'Applying…';
            try {
                const saved = await ApplyNhentaiTags(mangaId, c.gallery_id);
                toast('Tags applied from nhentai');
                onApplied(saved);
            } catch (err) {
                console.error(err);
                toast(nhErr(err), 'err');
                disableAll(false);
                applyBtn.textContent = auto ? 'Use only this' : 'Apply tags';
            }
        });
        list.appendChild(row);
    });
    return wrap;
}

// ───── lightbox ───────────────────────────────────────────────────
function openLightbox(imgs: HTMLImageElement[], startIdx: number): void {
    if (!imgs.length) return;
    let idx = Math.max(0, Math.min(imgs.length - 1, startIdx));
    const box = document.createElement('div');
    box.className = 'lightbox';
    box.innerHTML = `
        <button class="lb-nav lb-prev" type="button" aria-label="Previous">‹</button>
        <img src="${imgs[idx].src}" alt="">
        <button class="lb-nav lb-next" type="button" aria-label="Next">›</button>
        <button class="lb-close" type="button" aria-label="Close">×</button>`;
    const imgEl = box.querySelector('img') as HTMLImageElement;
    const show = (i: number) => { idx = (i + imgs.length) % imgs.length; imgEl.src = imgs[idx].src; };
    const onKey = (e: KeyboardEvent) => {
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(); }
        else if (e.key === 'ArrowLeft') { e.preventDefault(); e.stopPropagation(); show(idx - 1); }
        else if (e.key === 'ArrowRight') { e.preventDefault(); e.stopPropagation(); show(idx + 1); }
    };
    function close(): void {
        box.remove();
        document.removeEventListener('keydown', onKey, true);
        closeLightbox = null;
    }
    box.addEventListener('click', (e) => {
        const t = e.target as HTMLElement;
        if (t.classList.contains('lb-prev')) { e.stopPropagation(); show(idx - 1); }
        else if (t.classList.contains('lb-next')) { e.stopPropagation(); show(idx + 1); }
        else if (t.classList.contains('lb-close')) { e.stopPropagation(); close(); }
        else if (t === box) close();
    });
    document.addEventListener('keydown', onKey, true);
    document.body.appendChild(box);
    closeLightbox = close;
}

// ───── stash view (saved pages / "tabs") ──────────────────────────
// A short human label for a saved search, built from its filter params. Reused by
// the header save button so the stash row reads like the active filter chips.
function searchLabel(params: URLSearchParams): string {
    const parts: string[] = [];
    const q = params.get('q');
    if (q) parts.push(`“${q}”`);
    const authorId = params.get('author');
    if (authorId) parts.push('author: ' + (authorNames[authorId] || ('#' + authorId)));
    params.getAll('tag').filter(Boolean).forEach((t) => parts.push('#' + t));
    if (parts.length === 0) return 'All volumes';
    return parts.join(' · ');
}

function stashSearchCard(e: StashEntry): string {
    const qi = e.hash.indexOf('?');
    const params = new URLSearchParams(qi >= 0 ? e.hash.slice(qi + 1) : '');
    const chips: string[] = [];
    const q = params.get('q');
    if (q) chips.push(`<span class="chip">“${esc(q)}”</span>`);
    const authorId = params.get('author');
    if (authorId) chips.push(`<span class="chip">author: ${esc(authorNames[authorId] || ('#' + authorId))}</span>`);
    params.getAll('tag').filter(Boolean).forEach((t) => chips.push(`<span class="chip">#${esc(t)}</span>`));
    const body = chips.length ? chips.join(' ') : '<span class="stash-allvol">all volumes</span>';
    // The stored hash already lacks the leading '#'; prepend it for the link.
    return `<div class="stash-card search" data-id="${e.id}">
        <a class="stash-card-main" href="#${esc(e.hash)}">
            <span class="stash-eyebrow">Search</span>
            <span class="stash-chips">${body}</span>
        </a>
        <button type="button" class="stash-remove" data-id="${e.id}" aria-label="Remove from stash" title="Remove">×</button>
    </div>`;
}

function stashTitleCard(e: StashEntry): string {
    const cover = e.cover_rel_path
        ? `<img loading="lazy" src="${thumbURL(e.folder_path + '/' + e.cover_rel_path)}" alt="">`
        : `<div class="nocover"></div>`;
    const resume = e.last_page > 0 ? `<span class="resume-hint">resume → p.${e.last_page + 1}</span>` : '';
    return `<div class="card stash-card title" data-id="${e.id}">
        <a class="card-main" href="#/manga/${e.manga_id}?stash=${e.id}">
            <div class="card-cover">${cover}${resume}</div>
            <div class="meta"><span class="t">${esc(e.title || e.label)}</span></div>
        </a>
        <span class="a">${esc(e.author_name)}</span>
        <button type="button" class="stash-remove" data-id="${e.id}" aria-label="Remove from stash" title="Remove">×</button>
    </div>`;
}

function stashEmptyHtml(): string {
    return `<p class="empty">Nothing stashed yet. Use the <span class="inline-ico">▮</span> bookmark button to save a search or a title for later.</p>`;
}

async function renderStash(): Promise<void> {
    let entries: StashEntry[] = [];
    try { entries = await StashList(); } catch (e) { console.error(e); }
    const count = entries.length;
    const cards = entries.map((e) => (e.kind === 'title' && e.manga_id ? stashTitleCard(e) : stashSearchCard(e))).join('');
    viewEl().innerHTML = `
    <section class="hero">
        <p class="eyebrow">The Stash</p>
        <h1 class="hero-title">${count} <span>saved page${count === 1 ? '' : 's'}</span></h1>
    </section>
    <div class="grid" id="stash-grid">${count ? cards : stashEmptyHtml()}</div>`;

    const grid = document.getElementById('stash-grid')!;

    function refreshHero(): void {
        const left = grid.querySelectorAll('[data-id]').length;
        const h = viewEl().querySelector('.hero-title');
        if (h) h.innerHTML = `${left} <span>saved page${left === 1 ? '' : 's'}</span>`;
        if (left === 0) grid.innerHTML = stashEmptyHtml();
    }

    // Remember scroll before opening a card, so the reader's back link returns here.
    grid.addEventListener('click', (e) => {
        if ((e.target as HTMLElement).closest('.card-main, .stash-card-main')) {
            scrollMemory.set(location.hash, { y: window.scrollY, loaded: 0 });
        }
    }, { capture: true });

    // Remove (×) a saved page.
    grid.addEventListener('click', async (e) => {
        const btn = (e.target as HTMLElement).closest('.stash-remove') as HTMLElement | null;
        if (!btn) return;
        e.preventDefault();
        const id = parseInt(btn.dataset.id!, 10);
        try {
            await StashRemove(id);
            const card = btn.closest('[data-id]') as HTMLElement;
            card.style.opacity = '0';
            setTimeout(() => { card.remove(); refreshHero(); }, 200);
        } catch (err) { console.error(err); toast("Couldn't remove", 'err'); }
    });

    const saved = scrollMemory.get(location.hash);
    if (saved) { scrollMemory.delete(location.hash); skipScrollReset = true; window.scrollTo(0, saved.y); }

    lastBrowseHash = location.hash;
    viewCleanup = null;
}

// Save the current page (search or title) into the stash — the "save / clone / open
// in new tab" primitive. Reads the live reader page when a title is open.
async function saveCurrentPage(): Promise<void> {
    const r = parseRoute();
    try {
        if (r.name === 'reader' && readerState) {
            await StashSave({
                kind: 'title', hash: `/manga/${readerState.id}`, label: readerState.title,
                manga_id: readerState.id, page: readerState.page,
            });
            toast('Title saved to stash');
        } else if (r.name === 'library') {
            const hash = location.hash.replace(/^#/, '') || '/';
            await StashSave({ kind: 'search', hash, label: searchLabel(r.params), manga_id: 0, page: 0 });
            toast('Search saved to stash');
        } else {
            toast('Nothing to stash on this screen', 'err');
        }
    } catch (e) { console.error(e); toast("Couldn't save to stash", 'err'); }
}

// ───── right-click context menu ───────────────────────────────────
// Used for "open in new tab" on a title card and "save this page to stash" on blank
// space. A single menu is open at a time; outside-click or Esc dismisses it.
function onMenuKey(e: KeyboardEvent): void { if (e.key === 'Escape') closeCardMenu(); }
function closeCardMenu(): void {
    document.querySelector('.card-menu')?.remove();
    document.removeEventListener('click', closeCardMenu);
    document.removeEventListener('keydown', onMenuKey, true);
}
function showContextMenu(x: number, y: number, items: { label: string; run: () => void }[]): void {
    closeCardMenu();
    const menu = document.createElement('div');
    menu.className = 'card-menu';
    items.forEach((it) => {
        const b = document.createElement('button');
        b.type = 'button';
        b.textContent = it.label;
        b.addEventListener('click', (ev) => { ev.stopPropagation(); closeCardMenu(); it.run(); });
        menu.appendChild(b);
    });
    menu.style.left = x + 'px';
    menu.style.top = y + 'px';
    document.body.appendChild(menu);
    // Defer so this same right-click's trailing events don't immediately close it.
    setTimeout(() => document.addEventListener('click', closeCardMenu), 0);
    document.addEventListener('keydown', onMenuKey, true);
}

// ───── scan / ingest view ─────────────────────────────────────────
function scanMarkup(count: number, roots: string[], hasKey: boolean, missing: number): string {
    const rootChips = roots.length
        ? roots.map((r) =>
            `<span class="chip">${esc(r)}<a href="#" class="chip-x" data-remove-root="${esc(r)}" aria-label="Remove folder">×</a></span>`).join(' ')
        : `<span class="roots">No library folders yet — add one to start scanning.</span>`;
    const folderSettings = `<div class="settings"><span class="label">Library folders</span> ${rootChips}
        <button type="button" class="btn" data-add-root>Add folder…</button></div>`;
    // nhentai API key: entered here (password field), stored in config; never echoed
    // back. When a key is set, the auto-tag sweep becomes available.
    const keyState = hasKey
        ? `<span class="nh-key-state ok">key configured</span> <a class="nh-autotag-link" href="#/autotag">Auto-tag library →</a>`
        : `<span class="nh-key-state">no key set — needed for auto-tagging</span>`;
    const keySettings = `<div class="settings nh-settings"><span class="label">nhentai API key</span>
        <input type="password" class="nh-key-input" placeholder="${hasKey ? '•••••••• (replace)' : 'paste your personal key'}" autocomplete="off" spellcheck="false">
        <button type="button" class="btn" data-save-key>Save key</button> ${keyState}</div>`;
    // Maintenance: titles whose folders vanished from disk are kept (never auto-deleted),
    // so moving/removing folders leaves "missing" rows. This is the one place to clear them.
    const maintenance = missing > 0
        ? `<div class="settings"><span class="label">Maintenance</span>
            <span class="missing-note">${missing} missing title${missing === 1 ? '' : 's'} — folders gone from disk</span>
            <button type="button" class="btn btn-danger" data-remove-missing>Remove missing</button></div>`
        : '';
    const settings = folderSettings + keySettings + maintenance;
    const header = `
    <header class="scan-header">
        <div>
            <h1>Folders to ingest</h1>
            <p class="count">${count ? count + ' found on disk' : 'nothing new'}</p>
        </div>
        ${count ? `<a class="import-all-link" href="#" data-import-all>Import all ${count} →</a>` : ''}
    </header>`;
    const empty = count ? '' : `<p class="empty">Nothing new found. Drop folders into a library root and reload.</p>`;
    return settings + header + empty + `<div id="scan-list"></div>`;
}

function ingestRow(p: UnimportedPreview): HTMLElement {
    const d = p.folder;
    const listId = `tag-suggest-scan-${uid++}`;
    const cover = d.cover_rel_path
        ? `<img src="${thumbURL(d.folder_path + '/' + d.cover_rel_path)}" alt="">`
        : `<div class="nocover"></div>`;
    // Tags implied by the folder name (language, parody, …) are applied automatically
    // on import — show them as read-only chips; the input below is for extra tags.
    const autoTags = (p.tags && p.tags.length)
        ? `<div class="auto-tags"><span class="auto-tags-label">auto</span>${renderTagChips(p.tags)}</div>`
        : '';
    const row = document.createElement('div');
    row.className = 'ingest-row';
    row.innerHTML = `
        <div class="cover">${cover}</div>
        <div class="fields">
            <label>Author <input class="f-author" value="${esc(p.author)}"></label>
            <label>Title <input class="f-title" value="${esc(p.title)}"></label>
            <label class="full">Extra tags <input class="tag-input f-tags" placeholder="comma, separated, tags" autocomplete="off" list="${listId}"></label>
            <datalist id="${listId}"></datalist>
            ${autoTags}
            <div class="footer-row">
                <span class="path">${d.page_count} pages · ${esc(d.folder_path)}</span>
                <span class="row-status" role="status"></span>
                <button type="button" class="btn btn-primary f-save">Save</button>
            </div>
        </div>`;
    const status = row.querySelector('.row-status') as HTMLElement;
    const saveBtn = row.querySelector('.f-save') as HTMLButtonElement;
    const authorInput = row.querySelector('.f-author') as HTMLInputElement;
    const titleInput = row.querySelector('.f-title') as HTMLInputElement;
    const tagsInput = row.querySelector('.f-tags') as HTMLInputElement;
    const datalist = row.querySelector('datalist') as HTMLDataListElement;

    let timer: number | undefined;
    tagsInput.addEventListener('input', () => {
        const token = (tagsInput.value.split(',').pop() || '').trim();
        if (!token) return;
        window.clearTimeout(timer);
        timer = window.setTimeout(async () => {
            try {
                const names = await SuggestTags(token);
                datalist.innerHTML = names.map((n) => `<option value="${esc(n)}">`).join('');
            } catch { /* best-effort */ }
        }, 150);
    });

    saveBtn.addEventListener('click', async () => {
        saveBtn.disabled = true;
        row.classList.add('saving');
        status.classList.remove('err');
        status.textContent = 'saving…';
        // Apply the (possibly edited) author/title onto the folder; tags implied by the
        // name are re-derived server-side, so the input only carries extra user tags.
        d.author = authorInput.value.trim() || p.author;
        d.title = titleInput.value.trim() || p.title;
        const tags = tagsInput.value.split(',').map((t) => t.trim()).filter(Boolean);
        try {
            await Ingest(d, tags);
            row.classList.add('saved');
            toast(`Saved “${d.title}”`);
            setTimeout(() => row.remove(), 400);
        } catch (err) {
            console.error(err);
            row.classList.remove('saving');
            status.classList.add('err');
            status.textContent = 'save failed';
            saveBtn.disabled = false;
            toast('Save failed', 'err');
        }
    });
    return row;
}

async function renderScan(): Promise<void> {
    const [found, cfg, settings, missing] = await Promise.all([
        GetUnimported(), GetConfig(), GetSettings(), CountMissing(),
    ]);
    viewEl().innerHTML = scanMarkup(found.length, cfg.library_roots, settings.has_nhentai_key, missing);

    // Remove missing titles. Two-step: the first click arms the button (since this drops
    // those titles' tags/nhentai links — recoverable only by re-scanning the folders).
    const rmMissingBtn = viewEl().querySelector('[data-remove-missing]') as HTMLButtonElement | null;
    if (rmMissingBtn) {
        let armed = false;
        let armTimer: number | undefined;
        rmMissingBtn.addEventListener('click', async () => {
            if (!armed) {
                armed = true;
                rmMissingBtn.classList.add('armed');
                rmMissingBtn.textContent = `Click again to remove ${missing}`;
                armTimer = window.setTimeout(() => {
                    armed = false;
                    rmMissingBtn.classList.remove('armed');
                    rmMissingBtn.textContent = 'Remove missing';
                }, 4000);
                return;
            }
            window.clearTimeout(armTimer);
            rmMissingBtn.disabled = true;
            try {
                const removed = await RemoveMissing();
                toast(`Removed ${removed} missing title${removed === 1 ? '' : 's'}`);
                render();
            } catch (err) {
                console.error(err);
                toast('Could not remove missing titles', 'err');
                rmMissingBtn.disabled = false;
            }
        });
    }

    const saveKeyBtn = viewEl().querySelector('[data-save-key]') as HTMLButtonElement | null;
    const keyInput = viewEl().querySelector('.nh-key-input') as HTMLInputElement | null;
    saveKeyBtn?.addEventListener('click', async () => {
        const v = (keyInput?.value || '').trim();
        if (!v) { toast('Enter a key first', 'err'); return; }
        saveKeyBtn.disabled = true;
        try {
            await SetNhentaiKey(v);
            toast('nhentai key saved');
            render();
        } catch (err) {
            console.error(err);
            toast('Could not save key', 'err');
            saveKeyBtn.disabled = false;
        }
    });

    const addBtn = viewEl().querySelector('[data-add-root]') as HTMLButtonElement | null;
    addBtn?.addEventListener('click', async () => {
        addBtn.disabled = true;
        try {
            const dir = await AddLibraryRoot();
            if (dir) { toast('Added ' + dir); render(); return; }
        } catch (err) {
            console.error(err);
            toast('Could not add folder', 'err');
        }
        addBtn.disabled = false;
    });

    viewEl().querySelectorAll('[data-remove-root]').forEach((node) => {
        node.addEventListener('click', async (e) => {
            e.preventDefault();
            try {
                await RemoveLibraryRoot((node as HTMLElement).dataset.removeRoot!);
                toast('Removed folder');
                render();
            } catch (err) {
                console.error(err);
                toast('Could not remove folder', 'err');
            }
        });
    });

    const importAll = viewEl().querySelector('[data-import-all]') as HTMLElement | null;
    importAll?.addEventListener('click', async (e) => {
        e.preventDefault();
        importAll.style.pointerEvents = 'none';
        try {
            await ImportAll();
            toast('Imported all folders');
            location.hash = '#/';
        } catch (err) {
            console.error(err);
            toast('Import failed', 'err');
            importAll.style.pointerEvents = '';
        }
    });
    const list = document.getElementById('scan-list')!;
    found.forEach((p) => list.appendChild(ingestRow(p)));
    viewCleanup = null;
}

// ───── auto-tag from nhentai (bulk sweep) ─────────────────────────
// Event payloads are emitted via Wails runtime events (not method returns), so Wails
// generates no model class for them — these mirror the Go structs in nhentai.go.
interface ATProgress {
    done: number;
    total: number;
    manga_id: number;
    title: string;
    outcome: string;
    detail: string;
}
interface ATDone {
    total: number;
    applied: number;
    needs_review: main.MatchResult[];
    cancelled: boolean;
}

function autotagMarkup(hasKey: boolean): string {
    if (!hasKey) {
        return `<section class="at-page">
            <a class="back-link" href="#/scan">← Scan &amp; settings</a>
            <h1>Auto-tag from nhentai</h1>
            <p class="empty">No nhentai API key set. Add your personal key on the
                <a href="#/scan">Scan</a> page to enable auto-tagging.</p>
        </section>`;
    }
    return `<section class="at-page">
        <a class="back-link" href="#/scan">← Scan &amp; settings</a>
        <h1>Auto-tag from nhentai</h1>
        <p class="at-intro">Searches nhentai for each title, auto-applies confident
            matches (exact page count + strong title match), and queues the rest for you
            to confirm below. Requests are rate-limited, so a large library takes a while.</p>
        <div class="at-controls">
            <label class="at-resync"><input type="checkbox" data-resync> Re-tag titles already linked</label>
            <label class="builder-sortwrap at-langmode">Language
                <select class="builder-sort" data-langmode aria-label="Language to match">
                    <option value="auto">Auto (follow each title)</option>
                    <option value="english">English only</option>
                    <option value="japanese">Japanese only</option>
                </select>
            </label>
            <button type="button" class="btn btn-primary" data-start>Start auto-tagging</button>
            <button type="button" class="btn" data-cancel hidden>Cancel</button>
        </div>
        <div class="at-progress" hidden>
            <div class="at-bar"><div class="at-bar-fill"></div></div>
            <p class="at-status" role="status"></p>
        </div>
        <div class="at-log" aria-live="polite"></div>
        <div class="at-review"></div>
    </section>`;
}

// renderReviewQueue lists the ambiguous titles from a sweep, each with its own
// candidate picker so the user can confirm a match.
function renderReviewQueue(
    container: HTMLElement,
    items: main.MatchResult[],
    onItemDone?: (mangaId: number) => void,
): void {
    if (!items.length) return;
    const head = document.createElement('h2');
    head.className = 'at-review-head';
    head.textContent = `Needs review (${items.length})`;
    container.appendChild(head);
    items.forEach((res) => {
        const card = document.createElement('div');
        card.className = 'at-review-card';
        card.innerHTML = `<div class="at-review-title">
            <a href="#/manga/${res.manga_id}">${esc(res.local_title)}</a>
            <span class="at-review-pages">${res.local_pages}p</span></div>`;
        card.appendChild(buildMatchPicker(res.manga_id, res, () => {
            card.classList.add('done');
            (card.querySelector('.at-review-title') as HTMLElement)
                .insertAdjacentHTML('beforeend', ' <span class="at-done">✓ tagged</span>');
            onItemDone?.(res.manga_id);
        }));
        container.appendChild(card);
    });
}

async function renderAutotag(): Promise<void> {
    const settings = await GetSettings();
    viewEl().innerHTML = autotagMarkup(settings.has_nhentai_key);
    // The reader's back link follows lastBrowseHash, so record this page: leaving a
    // review title to inspect it returns here (to the restored queue), not to "/".
    lastBrowseHash = '#/autotag';
    if (!settings.has_nhentai_key) { viewCleanup = null; return; }

    const startBtn = viewEl().querySelector('[data-start]') as HTMLButtonElement;
    const cancelBtn = viewEl().querySelector('[data-cancel]') as HTMLButtonElement;
    const resync = viewEl().querySelector('[data-resync]') as HTMLInputElement;
    const langMode = viewEl().querySelector('[data-langmode]') as HTMLSelectElement;
    const barWrap = viewEl().querySelector('.at-progress') as HTMLElement;
    const bar = viewEl().querySelector('.at-bar-fill') as HTMLElement;
    const statusEl = viewEl().querySelector('.at-status') as HTMLElement;
    const logEl = viewEl().querySelector('.at-log') as HTMLElement;
    const reviewEl = viewEl().querySelector('.at-review') as HTMLElement;

    // Remember the scroll spot when opening a local title from the queue, so returning
    // to the review page lands where we left off (same scrollMemory the library/reader
    // views use). The listener lives on the container, so it survives re-population.
    reviewEl.addEventListener('click', (e) => {
        if ((e.target as HTMLElement).closest('a[href^="#/manga/"]')) {
            scrollMemory.set('#/autotag', { y: window.scrollY, loaded: 0 });
        }
    }, { capture: true });

    // EventsOn returns its own unsubscribe; track them so leaving the view (or a new
    // run) detaches the listeners cleanly.
    let offProgress: (() => void) | null = null;
    let offDone: (() => void) | null = null;
    const detach = () => { offProgress?.(); offDone?.(); offProgress = offDone = null; };

    const logLine = (cls: string, text: string) => {
        const line = document.createElement('div');
        line.className = 'at-line at-' + cls;
        line.textContent = text;
        logEl.appendChild(line);
        logEl.scrollTop = logEl.scrollHeight;
    };

    // Restore a prior sweep's review queue when returning to this page (e.g. after
    // opening a local title to compare images), so a re-run — and another round of
    // rate-limited nhentai requests — isn't needed just to see the queue again.
    if (reviewCache && reviewCache.items.length) {
        barWrap.hidden = false;
        bar.style.width = '100%';
        statusEl.textContent = `${reviewCache.cancelled ? 'Cancelled' : 'Done'} — ${reviewCache.applied} auto-applied, ${reviewCache.items.length} need review`;
        renderReviewQueue(reviewEl, reviewCache.items, removeFromReviewCache);
        // Covers reserve height via CSS aspect-ratio, so the queue's full height exists
        // synchronously now — restore the scroll spot before render()'s default top-reset.
        const savedScroll = scrollMemory.get('#/autotag');
        if (savedScroll) {
            scrollMemory.delete('#/autotag');
            skipScrollReset = true;
            window.scrollTo(0, savedScroll.y);
        }
    }

    startBtn.addEventListener('click', async () => {
        detach();
        reviewCache = null; // a fresh run supersedes the remembered queue
        reviewEl.innerHTML = '';
        logEl.innerHTML = '';
        bar.style.width = '0%';
        barWrap.hidden = false;
        startBtn.disabled = true;
        resync.disabled = true;
        langMode.disabled = true;
        cancelBtn.hidden = false;
        cancelBtn.disabled = false;
        statusEl.textContent = 'Starting…';

        offProgress = EventsOn('autotag:progress', (p: ATProgress) => {
            const pct = p.total ? Math.round((p.done / p.total) * 100) : 0;
            bar.style.width = pct + '%';
            statusEl.textContent = `${p.done} / ${p.total} — ${p.title}`;
            const label = p.outcome === 'applied' ? `✓ ${p.title} → ${p.detail}`
                : p.outcome === 'review' ? `? ${p.title} — needs review`
                : p.outcome === 'none' ? `– ${p.title} — no match`
                : `✗ ${p.title} — ${p.detail}`;
            logLine(p.outcome, label);
        });
        offDone = EventsOn('autotag:done', (d: ATDone) => {
            detach();
            cancelBtn.hidden = true;
            startBtn.disabled = false;
            resync.disabled = false;
            langMode.disabled = false;
            bar.style.width = '100%';
            statusEl.textContent = `${d.cancelled ? 'Cancelled' : 'Done'} — ${d.applied} auto-applied, ${d.needs_review.length} need review`;
            reviewCache = { items: d.needs_review.slice(), applied: d.applied, cancelled: d.cancelled };
            renderReviewQueue(reviewEl, reviewCache.items, removeFromReviewCache);
            toast(d.cancelled ? 'Auto-tag cancelled' : 'Auto-tag finished');
        });

        try {
            await StartAutoTag({ resync: resync.checked, language_mode: langMode.value });
        } catch (err) {
            console.error(err);
            detach();
            cancelBtn.hidden = true;
            startBtn.disabled = false;
            resync.disabled = false;
            langMode.disabled = false;
            barWrap.hidden = true;
            toast(nhErr(err), 'err');
        }
    });

    cancelBtn.addEventListener('click', () => {
        cancelBtn.disabled = true;
        statusEl.textContent = 'Cancelling…';
        CancelAutoTag();
    });

    viewCleanup = () => { detach(); };
}

// ───── init ───────────────────────────────────────────────────────
// Capture author names from any author link before its hash navigation fires.
document.addEventListener('click', (e) => {
    const a = (e.target as HTMLElement).closest('.author-link') as HTMLElement | null;
    if (a && a.dataset.authorName) {
        const m = (a.getAttribute('href') || '').match(/author=(\d+)/);
        if (m) authorNames[m[1]] = a.dataset.authorName;
    }
});

// Rescan button in the header.
document.getElementById('rescan-btn')?.addEventListener('click', async () => {
    const btn = document.getElementById('rescan-btn') as HTMLButtonElement;
    btn.disabled = true;
    try {
        await Rescan();
        toast('Library rescanned');
        if (parseRoute().name === 'library') render();
    } catch (err) {
        console.error(err);
        toast('Rescan failed', 'err');
    } finally {
        btn.disabled = false;
    }
});

// Save-current-page button in the header (stash the current search or title).
document.getElementById('stash-save-btn')?.addEventListener('click', () => { saveCurrentPage(); });

// Right-click behaviour:
//  • on a title card → "Open in new tab" (stash the title in the background so you
//    keep your current view);
//  • on blank space of a saveable view → "Save this page to stash" (the current
//    search or title), mirroring the header bookmark button.
document.addEventListener('contextmenu', (e) => {
    const main = (e.target as HTMLElement).closest('.card-main, .stash-card-main') as HTMLAnchorElement | null;
    const titleMatch = main && (main.getAttribute('href') || '').match(/#\/manga\/(\d+)/);
    if (main && titleMatch) {
        e.preventDefault();
        const id = parseInt(titleMatch[1], 10);
        const title = (main.querySelector('.meta .t')?.textContent || 'Title').trim();
        showContextMenu(e.pageX, e.pageY, [{
            label: 'Open in new tab',
            run: async () => {
                try {
                    await StashSave({ kind: 'title', hash: `/manga/${id}`, label: title, manga_id: id, page: 0 });
                    toast('Opened in new tab');
                } catch (err) { console.error(err); toast("Couldn't stash title", 'err'); }
            },
        }]);
        return;
    }
    // Blank space: only library/reader have a page worth saving.
    const r = parseRoute();
    if (r.name === 'library' || r.name === 'reader') {
        e.preventDefault();
        const label = r.name === 'reader' ? 'Save this title to stash' : 'Save this search to stash';
        showContextMenu(e.pageX, e.pageY, [{ label, run: () => { saveCurrentPage(); } }]);
    }
});

window.addEventListener('hashchange', () => { render(); });
render();
