# UX Polish Plan

> **Created**: 2026-07-18
> **Source**: Visual/UX audit of the running app (production build driven over CDP,
> all views + interaction states screenshotted), cross-checked against
> `frontend/src/theme.css` and `frontend/src/main.ts`.
> **Status**: implemented and GUI-verified 2026-07-18 (all four phases). `go test ./...`
> green, `go vet` clean, `wails build` succeeds, `tsc` clean. Verified by hand, not by the
> CDP rig in the appendix — that could not be rebuilt (the patched-loader build was
> blocked by the sandbox).
>
> **Follow-ups from use** (same day, not GUI-verified): touch-reachable card/stash buttons
> (`any-pointer: coarse`); stash removal now updates the grid without a reload (`closest()`
> was matching the × button, which carries its own `data-id`); toasts drop below the app
> bar; the filter builder remembers its type + caret across a search so Enter-to-search
> stacks filters instead of resetting to Title; and the ambiguous header bookmark is
> replaced by scope-named buttons — "Save search" in the builder foot, "Save title" in the
> reader toolbar. Every filter kind now stacks, not just tags: title terms AND (each
> narrows), authors OR (a title has one author, so ANDing two matches nothing) —
> `SearchParams.Query/AuthorID` became `Queries []string` / `AuthorIDs []int64`.

Ordered by priority. Each phase is independently shippable and ends with the
standard definition of done (`go test ./...`, `go vet ./...`, `wails build`),
plus a visual re-check of the affected views in the running app.

---

## Phase 1 — Rendering bugs

### 1.1 Global `header` selector leaks into page headers  ← root cause of two symptoms
- **Where**: `theme.css:94` (`header { display:flex; align-items:center; gap; padding; background; border-bottom }`)
- **What happens**: the reader's `.title-header` (`main.ts:670`) and the scan page's
  `.scan-header` (`main.ts:1592`) are also `<header>` elements, so they inherit the
  app-chrome flex row. On the reader this squeezes the intended stacked layout
  (title → byline → tags, per `theme.css:343`) into three cramped columns: the byline
  wraps to a ~140px sliver ("by / IdolMaster · 10 / pages"), the tags block floats
  right, the block re-centers vertically when the tag editor opens, and the Fetch
  Tags panel gets pinned into the right column with a blank left half.
- **Fix**: scope the chrome rule to `body > header` (the app bar is a direct child of
  `<body>`; `.title-header` / `.scan-header` live inside `#view`). No markup change.
- **Verify**: reader title page stacks title/byline/tags full-width; tag editor and
  Fetch Tags panel span the header column; scan header unaffected.

### 1.2 Scan page tag-source dropdown renders as a raw native `<select>`
- **Where**: `.src-select` (`main.ts:1562`) — not in the CSS opt-in skin list at
  `theme.css:164` (`#filters select, .builder-type, .builder-sort, .builder-source`).
  The comment there explicitly predicts this failure mode, and it happened.
- **Fix**: invert the pattern — style bare `select` with the shared dark skin as the
  default (every select in the app uses the same look), keep the opt-in list only if
  some select ever needs to differ. Removes the whole class of bug.
- **Verify**: Tag source dropdown on `#/scan` matches the sort/subject selects; check
  the tag-subject select in the reader's tag editor still looks right.

### 1.3 Stash empty-state "icon" is a literal `▮` character
- **Where**: `main.ts:1445` — `<span class="inline-ico">▮</span>` renders as a solid
  orange rectangle; the copy says "use the bookmark button" but looks nothing like
  the header's outline bookmark icon it points at. Reads as a broken glyph.
- **Fix**: inline the same bookmark SVG used by the header button (16px,
  `stroke: currentColor`), keep the `.inline-ico` vermilion color.
- **Note**: this empty-state copy also changes in Phase 2 (rename + discoverability).

---

## Phase 2 — Stash rename + save-for-later clarity (user-directed)

The right-click menu item "Open in new tab" (`main.ts:2059`) does not open
anything — it calls `StashSave` to save the title in the background and toasts
"Opened in new tab". The feature *is* save-for-later; the copy should say so.

### 2.1 Rename the user-facing feature to "Saved Stash"
Pure UI-copy rename. **Do not** rename internals (the `stash` table,
`internal/stash`, `StashSave`, `#/stash` hash, `stash-*` CSS classes) — no schema
or API churn, and the hash is persisted in saved entries.
- Nav link `STASH` → `SAVED STASH` (header nav, `index.html` or wherever the nav is built).
- Stash page hero: eyebrow `THE STASH` → `SAVED STASH` (keep `N SAVED PAGES` numeral line).
- Header bookmark button tooltip/aria-label: make it say "Save this page for later".
- Toasts that mention "stash" (e.g. "Couldn't stash title", "Couldn't save to stash"):
  reword to "save for later" language.

### 2.2 Rename card right-click action to "Save for later"
- `main.ts:2059`: label `Open in new tab` → `Save for later`.
- Its toast `Opened in new tab` → `Saved for later` (consider appending a
  plain link to `#/stash` in the toast so the result is one click away).
- Blank-space right-click labels ("Save this title/search to stash") → "Save this
  title for later" / "Save this search for later" for consistency.
- Code comments around `main.ts:2046` describing the old metaphor should be updated.

### 2.3 Expose saving upfront (right-click is undiscoverable)
Right-click is never explained anywhere in the UI. Add a visible affordance:
- **Card hover bookmark button**: small icon button overlaid on the card cover
  (top-right), hidden until hover/focus — the exact pattern `.stash-remove` already
  uses on stash cards (`theme.css:610`). Click = same `StashSave` call as the
  context menu. Title cards in the library grid and search results get it.
- **Stash empty state**: extend the copy to mention both paths — the header
  bookmark button for the current page, and the card bookmark / right-click for
  any title. (Combines with fix 1.3.)
- Keep the context menu — it becomes the power-user shortcut, no longer the only door.

---

## Phase 3 — UX friction

### 3.1 Enter in the filter builder should apply the search
- **Now**: typing + Enter (or Add) only stages a chip; the grid doesn't update until
  SEARCH is clicked. Verified: typed "sweet", chip staged in DOM, grid still showed
  all 9 volumes.
- **Fix**: Enter = stage chip + run search. Keep Add for composing multi-filter
  queries before one search. (Alternatively auto-apply on every chip add/remove —
  decide when implementing; Enter-applies is the smaller change.)

### 3.2 Tag editor shows duplicate tag rows in different formats
- **Now**: opening Edit Tags leaves the read-only grouped row visible
  ("PARODY `#MORITAMA`") directly above the editable chips ("PARODY `moritama ×`") —
  same data, different casing and chrome.
- **Fix**: hide `#tagrow` (and the `.tag-actions` row) while `.tag-edit` is open;
  restore on save/cancel.

### 3.3 Reader chrome overlays metadata work
- **Now**: the fixed page counter and help pill stay on screen while the tag editor
  or fetch-match picker is open.
- **Fix**: hide `.reader-counter` / `.reader-help` (and suppress the resume pill)
  while any editor panel is open — same mechanism as `body[data-reader="grid"]`
  already uses to hide them in contents view (`theme.css:472`).

### 3.4 Scan page double empty state
- **Now**: "NOTHING NEW" count label + centered "Nothing new found…" message
  (`main.ts:1599`) both render.
- **Fix**: when the ingest list is empty, show only the italic empty-state line;
  render the count label only when count > 0.

### 3.5 Fetch Tags panel layout — recheck after 1.1
The blank-left-column layout is expected to be a symptom of 1.1. After the header
scoping fix, re-screenshot; if the picker is still cramped, give `.nh-panel` the
full header-column width.

---

## Phase 4 — Consistency polish

- **4.1 Scan page header hierarchy**: Library and Stash share the eyebrow +
  italic-numeral hero ("THE ARCHIVE / *9* VOLUMES"); Scan uses a bare serif
  "Folders to ingest". Give Scan the same treatment (e.g. eyebrow `INGEST`,
  numeral = pending-folder count) so all three top-level views read as one app.
- **4.2 Card author link focus ring** spans the full column width (`.card .a` is
  `display:block` for ellipsis). `width: fit-content; max-width: 100%` keeps the
  ellipsis and shrinks the ring to the text.

---

## Explicitly good — don't regress
Skeleton loading cards, empty states with recovery links, `prefers-reduced-motion`
support, `:focus-visible` rings, auto-hiding reader chrome, the fullscreen page
viewer, and the mono/serif type system all verified working as designed.

---

## Appendix — how to re-run the visual audit
- `wails dev` currently fails on this machine: the debug build (`-gcflags all=-N -l`)
  trips a `nosplit stack over 792 byte limit` linker error on windows/arm64
  (Go toolchain issue, not app code). Production `wails build` is unaffected.
- Workaround used for driving the real app: the stock Wails loader deliberately
  blanks `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS` (`preventEnvAndRegistryOverrides`
  in `go-webview2`), so a stock binary can never open a CDP port. The audit build
  used a scratchpad copy of `go-webview2` with a 3-line patch appending
  `DOUJIN_AUDIT_BROWSER_ARGS` to the browser args + a temporary `go.mod` replace
  (reverted afterward). Launch with
  `DOUJIN_AUDIT_BROWSER_ARGS='--remote-debugging-port=9222'`, then drive with
  `playwright-core` `connectOverCDP` + Edge channel (no browser download needed).
