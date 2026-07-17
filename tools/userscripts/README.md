# Userscripts

Companion browser scripts for getting content into Doujin Toshokan. These are
standalone Tampermonkey userscripts — they touch no Go/frontend code.

## akuma-doujin.user.js

Adds a **Download CBZ** button to akuma.moe gallery pages (`/g/{slug}`). It
scrapes the gallery metadata, downloads every full-size page image, and saves a
`.cbz` whose filename follows the exact grammar `internal/doujin/parse.go`
understands:

```
nhentai-<id> - [Circle (Artist)] Title (Parody) [Language] [Digital].cbz
```

Drop the file into a library root (raw, or inside an author folder) and rescan —
author, title, parody/language/misc tags are picked up from the name. The
`nhentai-<id>` prefix is added only when the page links an nhentai source, and
gives the app's auto-tagger an exact match.

The cbz also embeds an `info.json` with the **full** scraped tag set (including
`male:`/`female:`/`other:` namespaced tags the filename can't carry), keyed by
the app's tag subjects. On import (and on rescan), Doujin Toshokan reads this
`info.json` and applies every subject — artist, group, parody, character,
category, language, and the general tags — additively, so characters and general
tags come in without needing the nhentai auto-tagger. A `.cbz` with no
`info.json` still imports fine, tagged from the filename alone.

### Install

1. Install [Tampermonkey](https://www.tampermonkey.net/).
2. Open `akuma-doujin.user.js` → Tampermonkey offers to install it (or paste it
   into a new script in the dashboard).
3. Visit a gallery page. The panel appears with a filename preview; check it,
   then click **Download CBZ**.
4. If the image CDN is on a different domain, Tampermonkey asks to allow it on
   first run — allow it, then pin it in the script header with a
   `@connect <host>` line.

The script has no external dependencies — the zip is written by a built-in
STORE-only writer (JSZip hangs in Firefox userscript sandboxes).

A per-field scrape report is logged to the DevTools console (filter on `[dtk]`)
— if any field comes back empty, that's the selector to fix.
