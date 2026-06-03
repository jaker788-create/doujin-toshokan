import './theme.css';
import {
    Search, Count, GetManga, GetAuthor, SuggestTags, SuggestAuthors,
    UpdateTags, GetUnimported, Ingest, ImportAll, Rescan,
    GetConfig, AddLibraryRoot, RemoveLibraryRoot,
    StashSave, StashList, StashGet, StashSetPage, StashRemove,
} from '../wailsjs/go/main/App';
import { main, search, scanner, stash } from '../wailsjs/go/models';

type Manga = search.Manga;
type MangaDetail = main.MangaDetail;
type DetectedFolder = scanner.DetectedFolder;
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
    | { name: 'library'; params: URLSearchParams };

function parseRoute(): Route {
    const raw = location.hash.replace(/^#/, '') || '/';
    const qi = raw.indexOf('?');
    const path = qi >= 0 ? raw.slice(0, qi) : raw;
    const query = qi >= 0 ? raw.slice(qi + 1) : '';
    if (path === '/scan') return { name: 'scan' };
    if (path === '/stash') return { name: 'stash' };
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
    const tagrow = d.tags.map((t) => `<a href="#/?tag=${encodeURIComponent(t)}">#${esc(t)}</a>`).join('');
    const gallery = d.pages.map((p, i) =>
        `<img loading="lazy" src="${imageURL(p)}" alt="page ${i + 1}" data-page="${i + 1}">`).join('');
    const counter = d.pages.length
        ? `<div class="reader-counter" data-total="${d.pages.length}"><span class="cur">1</span><span class="sep">/</span><span class="tot">${d.pages.length}</span></div>
           <aside class="reader-help"><kbd>←</kbd><kbd>→</kbd> page · <kbd>F</kbd> fit · <kbd>⌫</kbd> back</aside>`
        : '';
    const notice = d.missing ? `<p class="notice">Folder is missing on disk: ${esc(m.folder_path)}</p>` : '';
    return `
    <a class="back-link" href="${esc(backHref)}">${esc(backLabel)}</a>
    <a class="reader-back" href="${esc(backHref)}" aria-label="${esc(backLabel)}" title="${esc(backLabel)}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M19 12H5M11 6l-6 6 6 6"/></svg></a>
    <header class="title-header">
        <h1>${esc(m.title)}</h1>
        <p class="byline">by <a class="author author-link" href="#/?author=${m.author_id}" data-author-name="${esc(m.author_name)}">${esc(m.author_name)}</a><span class="sep">·</span>${m.page_count} pages</p>
        <div class="tags-block" data-manga="${m.id}">
            <p class="tagrow" id="tagrow">${tagrow}</p>
            <form class="tag-edit" hidden>
                <input class="tag-input" name="tags" value="${esc(d.tags.join(', '))}" placeholder="comma, separated, tags" autocomplete="off" list="tag-suggest">
                <datalist id="tag-suggest"></datalist>
                <div class="tag-edit-actions">
                    <button type="submit" class="btn btn-primary">Save tags</button>
                    <button type="button" class="btn tag-edit-cancel">Cancel</button>
                </div>
            </form>
            <button type="button" class="tag-edit-toggle btn">${d.tags.length ? 'Edit tags' : '+ Add tags'}</button>
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
        : backHref.startsWith('#/stash') ? '← Back to stash' : '← Back to results';
    viewEl().innerHTML = readerMarkup(detail, backHref, backLabel);

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
            input.value = saved.join(', ');
            row.innerHTML = saved.map((t) => `<a href="#/?tag=${encodeURIComponent(t)}">#${esc(t)}</a>`).join('');
            toggle.textContent = saved.length ? 'Edit tags' : '+ Add tags';
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
function scanMarkup(count: number, roots: string[]): string {
    const rootChips = roots.length
        ? roots.map((r) =>
            `<span class="chip">${esc(r)}<a href="#" class="chip-x" data-remove-root="${esc(r)}" aria-label="Remove folder">×</a></span>`).join(' ')
        : `<span class="roots">No library folders yet — add one to start scanning.</span>`;
    const settings = `<div class="settings"><span class="label">Library folders</span> ${rootChips}
        <button type="button" class="btn" data-add-root>Add folder…</button></div>`;
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

function ingestRow(d: DetectedFolder): HTMLElement {
    const listId = `tag-suggest-scan-${uid++}`;
    const cover = d.cover_rel_path
        ? `<img src="${thumbURL(d.folder_path + '/' + d.cover_rel_path)}" alt="">`
        : `<div class="nocover"></div>`;
    const row = document.createElement('div');
    row.className = 'ingest-row';
    row.innerHTML = `
        <div class="cover">${cover}</div>
        <div class="fields">
            <label>Author <input class="f-author" value="${esc(d.author)}"></label>
            <label>Title <input class="f-title" value="${esc(d.title)}"></label>
            <label class="full">Tags <input class="tag-input f-tags" placeholder="comma, separated, tags" autocomplete="off" list="${listId}"></label>
            <datalist id="${listId}"></datalist>
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
        d.author = authorInput.value.trim() || d.author;
        d.title = titleInput.value.trim() || d.title;
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
    const [found, cfg] = await Promise.all([GetUnimported(), GetConfig()]);
    viewEl().innerHTML = scanMarkup(found.length, cfg.library_roots);

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
    found.forEach((d) => list.appendChild(ingestRow(d)));
    viewCleanup = null;
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
