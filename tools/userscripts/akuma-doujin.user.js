// ==UserScript==
// @name         Akuma → Doujin Toshokan CBZ
// @namespace    doujin-toshokan
// @version      0.2.0
// @description  Download an akuma.moe gallery as a .cbz named for Doujin Toshokan's filename parser, with full scraped metadata embedded as info.json
// @author       Jaker
// @match        https://akuma.moe/g/*
// @exclude      https://akuma.moe/g/*/*
// @icon         https://www.google.com/s2/favicons?sz=64&domain=akuma.moe
// @grant        GM_xmlhttpRequest
// @grant        GM.xmlHttpRequest
// @connect      akuma.moe
// NOTE: if the image CDN is a different domain, Tampermonkey prompts to allow
// it on first run — pin it here with another @connect line once you know it.
// @run-at       document-idle
// ==/UserScript==

(function () {
	'use strict';

	const LOG = '[dtk]';

	// GM_xmlhttpRequest (Tampermonkey) / GM.xmlHttpRequest (Greasemonkey 4)
	const gmXhr = typeof GM_xmlhttpRequest !== 'undefined'
		? GM_xmlhttpRequest
		: (typeof GM !== 'undefined' && GM.xmlHttpRequest);

	// ==== Doujin Toshokan parser vocabulary (mirror of internal/doujin/parse.go) ====

	// Languages the app recognizes as a [Language] bracket.
	const APP_LANGUAGES = new Set([
		'english', 'japanese', 'chinese', 'korean', 'spanish', 'french',
		'german', 'russian', 'italian', 'portuguese', 'vietnamese', 'thai',
		'indonesian', 'translated',
	]);

	// Misc tags the app accepts as trailing [x] brackets.
	const APP_MISC = new Set([
		'digital', 'decensored', 'uncensored', 'censored',
		'colorized', 'color', 'fullcolor',
	]);

	// akuma.moe tag namespace → Doujin Toshokan tag subject (internal/tag).
	// male/female/other/misc are e-hentai-style namespaces; the app has one flat
	// "tag" subject, so they collapse into it (value only; namespaced form is
	// kept in info.json tags_raw).
	const NS_TO_SUBJECT = {
		artist: 'artist',
		group: 'group',
		circle: 'group',
		parody: 'parody',
		series: 'parody',
		character: 'character',
		language: 'language',
		category: 'category',
		male: 'tag',
		female: 'tag',
		other: 'tag',
		mixed: 'tag',
		misc: 'tag',
		tag: 'tag',
	};

	const MAX_BASENAME = 180; // keep paths comfortably under Windows limits

	// ==== State ====

	const state = {
		running: false,
		aborted: false,
		threads: 3,
		meta: null,
		fileName: '',
	};

	// ==== Helpers ====

	const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

	function slug() {
		const m = location.pathname.match(/^\/g\/([^/]+)/);
		return m ? m[1] : '';
	}

	function galleryUrl() {
		return `${location.origin}/g/${slug()}`;
	}

	// Strip Windows-forbidden characters, collapse whitespace.
	function sanitize(s) {
		return s.replace(/[\\/:*?"<>|]/g, ' ').replace(/\s+/g, ' ').trim();
	}

	function capitalize(s) {
		return s ? s[0].toUpperCase() + s.slice(1) : s;
	}

	async function withRetry(fn, tries = 3) {
		let lastErr;
		for (let attempt = 0; attempt < tries; attempt++) {
			if (state.aborted) throw new Error('cancelled');
			try {
				return await fn();
			} catch (e) {
				lastErr = e;
				if (attempt < tries - 1) await sleep(1000 * Math.pow(3, attempt));
			}
		}
		throw lastErr;
	}

	// Run worker(item, idx) over items with bounded concurrency.
	// Collects per-item errors instead of failing fast.
	async function pool(items, limit, worker) {
		const results = new Array(items.length);
		const errors = [];
		let next = 0;
		async function lane() {
			for (;;) {
				if (state.aborted) return;
				const idx = next++;
				if (idx >= items.length) return;
				try {
					results[idx] = await worker(items[idx], idx);
				} catch (e) {
					errors.push({ item: items[idx], error: e });
				}
			}
		}
		await Promise.all(
			Array.from({ length: Math.min(limit, items.length) }, lane)
		);
		return { results, errors };
	}

	// ==== Metadata scraping ====

	// Parse one tag link: prefer the ?q=namespace:"value" search param, fall
	// back to the row label + link text.
	function parseTagLink(a, fallbackNs) {
		let ns = '';
		let value = '';
		try {
			const q = new URL(a.href, location.href).searchParams.get('q') || '';
			const m = q.match(/^([a-z][a-z ]*):(.+)$/i);
			if (m) {
				ns = m[1].trim().toLowerCase();
				value = m[2].trim().replace(/^"(.*)"$/, '$1').trim();
			}
		} catch (e) { /* not a URL we understand */ }
		if (!value) value = a.textContent.replace(/\s+/g, ' ').trim();
		if (!ns) ns = fallbackNs;
		return { ns, value };
	}

	function scrapeMeta() {
		const tags = {
			artist: [], group: [], parody: [], character: [],
			tag: [], language: [], category: [],
		};
		const tagsRaw = [];
		let pages = 0;
		let uploadedAt = null;

		document.querySelectorAll('.info-list li.meta-data').forEach((li) => {
			const dataEl = li.querySelector('.data');
			const label = (dataEl ? dataEl.textContent : '').replace(/\s+/g, ' ').trim().toLowerCase();

			if (label === 'pages') {
				const v = li.querySelector('.value');
				pages = parseInt(v ? v.textContent : '', 10) || 0;
				return;
			}
			if (label === 'date') {
				const t = li.querySelector('time');
				uploadedAt = t ? t.getAttribute('datetime') : null;
				return;
			}

			// Fallback namespace: the extra class on span.data (e.g. "data artist"),
			// else the label text itself.
			let fallbackNs = label;
			if (dataEl) {
				const cls = [...dataEl.classList].find((c) => c !== 'data');
				if (cls) fallbackNs = cls.toLowerCase();
			}

			li.querySelectorAll('.value a').forEach((a) => {
				const { ns, value } = parseTagLink(a, fallbackNs);
				if (!value) return;
				const subject = NS_TO_SUBJECT[ns];
				if (!subject) {
					console.warn(`${LOG} unknown tag namespace "${ns}" (value "${value}") — recorded in tags_raw only`);
					tagsRaw.push(`${ns}:${value}`);
					return;
				}
				if (!tags[subject].includes(value)) tags[subject].push(value);
				tagsRaw.push(`${ns}:${value}`);
			});
		});

		// Title: the gallery h1 (the site nav header has no h1, so a document-wide
		// query lands on the article title).
		const h1 = document.querySelector('article header h1') || document.querySelector('header h1') || document.querySelector('h1');
		const displayTitle = h1 ? h1.textContent.replace(/\s+/g, ' ').trim() : '';
		const h2 = document.querySelector('article header h2');
		const japaneseTitle = h2 ? h2.textContent.replace(/\s+/g, ' ').trim() : '';

		// Optional nhentai source link → exact-match auto-tagging in the app.
		let nhentaiId = null;
		const nh = document.querySelector('a[href*="nhentai.net/g/"]');
		if (nh) {
			const m = nh.href.match(/nhentai\.net\/g\/(\d+)/);
			if (m) nhentaiId = m[1];
		}

		return {
			slug: slug(),
			url: galleryUrl(),
			displayTitle,
			japaneseTitle,
			bareTitle: extractBareTitle(displayTitle) || slug(),
			nhentaiId,
			tags,
			tagsRaw,
			pages,
			uploadedAt,
		};
	}

	// Tokenize a raw gallery name the way doujin.ParseName does — (), [], {}
	// groups and bare text — and return the first bare-text segment (the naked
	// title, stripped of event/circle/parody/language/translator groups).
	function extractBareTitle(raw) {
		const OPEN = { '(': ')', '[': ']', '{': '}' };
		const tokens = [];
		let buf = '';
		let i = 0;
		while (i < raw.length) {
			const c = raw[i];
			if (OPEN[c]) {
				if (buf.trim()) tokens.push({ kind: 'text', v: buf.trim() });
				buf = '';
				const close = OPEN[c];
				let depth = 1;
				let j = i + 1;
				let inner = '';
				while (j < raw.length && depth > 0) {
					if (raw[j] === c) depth++;
					else if (raw[j] === close) {
						depth--;
						if (depth === 0) break;
					}
					inner += raw[j];
					j++;
				}
				tokens.push({ kind: c, v: inner.trim() });
				i = j + 1;
			} else {
				buf += c;
				i++;
			}
		}
		if (buf.trim()) tokens.push({ kind: 'text', v: buf.trim() });
		const first = tokens.find((t) => t.kind === 'text');
		return first ? first.v : '';
	}

	// ==== Filename builder (emits the exact grammar the app parses) ====
	// nhentai-<id> - [Circle (Artist)] [ExtraArtist] Title (Parody) [Language] [Misc]

	function buildFileName(meta) {
		const artists = meta.tags.artist.map(sanitize).filter(Boolean);
		const groups = meta.tags.group.map(sanitize).filter(Boolean);

		let credit = '';
		if (groups.length && artists.length) credit = `[${groups[0]} (${artists[0]})]`;
		else if (artists.length) credit = `[${artists[0]}]`;
		else if (groups.length) credit = `[${groups[0]}]`;
		const extraArtists = artists.slice(1).map((a) => `[${a}]`);

		const parodies = meta.tags.parody
			.filter((p) => p.toLowerCase() !== 'original')
			.map((p) => `(${sanitize(p)})`);

		const langs = meta.tags.language
			.map((l) => l.toLowerCase())
			.filter((l) => APP_LANGUAGES.has(l));
		const lang = langs.find((l) => l !== 'translated') || langs[0] || '';

		const misc = meta.tags.tag
			.filter((t) => APP_MISC.has(t.toLowerCase()))
			.map((t) => `[${capitalize(t.toLowerCase())}]`);

		// " | " is both a dual-title separator and a forbidden char on Windows;
		// " - " is the app's other recognized separator, so swap.
		let title = sanitize(meta.bareTitle.replace(/\s*\|\s*/g, ' - '));

		const head = [];
		if (meta.nhentaiId) head.push(`nhentai-${meta.nhentaiId} -`);
		if (credit) head.push(credit);
		head.push(...extraArtists);

		const tail = [...parodies];
		if (lang) tail.push(`[${capitalize(lang)}]`);
		tail.push(...misc);

		// Truncate the title (never the metadata brackets) to fit the cap.
		const fixedLen =
			head.join(' ').length + tail.join(' ').length +
			(head.length ? 1 : 0) + (tail.length ? 1 : 0);
		const room = MAX_BASENAME - fixedLen;
		if (title.length > room) title = title.slice(0, Math.max(room, 20)).trim();

		const base = [...head, title, ...tail].join(' ')
			.replace(/\s+/g, ' ')
			.replace(/[. ]+$/, '')
			.trim();
		return `${base}.cbz`;
	}

	// ==== Downloading ====

	// Fetch a /g/{slug}/{n} page view (same-origin — DDoS-Guard cookies ride
	// along) and pull the full-size image URL out of #image-container.
	async function fetchPageImageUrl(n) {
		const pageUrl = `${galleryUrl()}/${n}`;
		const res = await fetch(pageUrl, { credentials: 'same-origin' });
		if (!res.ok) throw new Error(`page ${n}: HTTP ${res.status}`);
		const html = await res.text();
		const doc = new DOMParser().parseFromString(html, 'text/html');
		const img = doc.querySelector('#image-container img');
		if (!img) throw new Error(`page ${n}: no #image-container img in response`);
		return new URL(img.getAttribute('src'), pageUrl).href;
	}

	// Fetch the image binary via GM xhr (the CDN is likely cross-origin).
	function gmFetchImage(url, referer) {
		return new Promise((resolve, reject) => {
			gmXhr({
				method: 'GET',
				url,
				responseType: 'arraybuffer',
				timeout: 120000,
				headers: { Referer: referer, Accept: 'image/*,*/*' },
				onload: (r) => {
					if (r.status >= 200 && r.status < 300 && r.response) resolve(r);
					else reject(new Error(`HTTP ${r.status} for ${url}`));
				},
				onerror: () => reject(new Error(`network error for ${url}`)),
				ontimeout: () => reject(new Error(`timeout for ${url}`)),
			});
		});
	}

	function extFor(url, responseHeaders) {
		const m = url.match(/\.(jpe?g|png|webp|gif|bmp|avif)(?:[?#]|$)/i);
		if (m) return '.' + m[1].toLowerCase().replace('jpeg', 'jpg');
		const ctMatch = /content-type:\s*([^\s;]+)/i.exec(responseHeaders || '');
		const ct = ctMatch ? ctMatch[1].toLowerCase() : '';
		const map = {
			'image/jpeg': '.jpg', 'image/png': '.png', 'image/webp': '.webp',
			'image/gif': '.gif', 'image/bmp': '.bmp', 'image/avif': '.avif',
		};
		return map[ct] || '.jpg';
	}

	// ==== Minimal STORE-only ZIP writer ====
	// JSZip's generateAsync hangs in Firefox userscript sandboxes (its promise
	// shim's async scheduling never fires there), and we don't compress anyway —
	// so write the zip bytes ourselves, fully synchronously.

	const CRC_TABLE = (() => {
		const t = new Uint32Array(256);
		for (let n = 0; n < 256; n++) {
			let c = n;
			for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
			t[n] = c >>> 0;
		}
		return t;
	})();

	function crc32(bytes) {
		let c = 0xffffffff;
		for (let i = 0; i < bytes.length; i++) c = CRC_TABLE[(c ^ bytes[i]) & 0xff] ^ (c >>> 8);
		return (c ^ 0xffffffff) >>> 0;
	}

	// entries: [{ name: string, data: Uint8Array }] → zip Blob (method STORE).
	function buildZip(entries) {
		const encoder = new TextEncoder();
		const parts = [];
		const central = [];
		let offset = 0;

		const now = new Date();
		const dosTime = ((now.getHours() << 11) | (now.getMinutes() << 5) | (now.getSeconds() >> 1)) & 0xffff;
		const dosDate = ((((now.getFullYear() - 1980) & 0x7f) << 9) | ((now.getMonth() + 1) << 5) | now.getDate()) & 0xffff;

		for (const e of entries) {
			const name = encoder.encode(e.name);
			const crc = crc32(e.data);

			const local = new DataView(new ArrayBuffer(30));
			local.setUint32(0, 0x04034b50, true); // local file header signature
			local.setUint16(4, 20, true);         // version needed to extract
			local.setUint16(6, 0x0800, true);     // flags: UTF-8 filenames
			local.setUint16(8, 0, true);          // method: STORE
			local.setUint16(10, dosTime, true);
			local.setUint16(12, dosDate, true);
			local.setUint32(14, crc, true);
			local.setUint32(18, e.data.length, true); // compressed size
			local.setUint32(22, e.data.length, true); // uncompressed size
			local.setUint16(26, name.length, true);
			local.setUint16(28, 0, true);         // extra field length
			parts.push(new Uint8Array(local.buffer), name, e.data);

			const cd = new DataView(new ArrayBuffer(46));
			cd.setUint32(0, 0x02014b50, true);    // central directory signature
			cd.setUint16(4, 20, true);            // version made by
			cd.setUint16(6, 20, true);            // version needed
			cd.setUint16(8, 0x0800, true);        // flags: UTF-8 filenames
			cd.setUint16(10, 0, true);            // method: STORE
			cd.setUint16(12, dosTime, true);
			cd.setUint16(14, dosDate, true);
			cd.setUint32(16, crc, true);
			cd.setUint32(20, e.data.length, true);
			cd.setUint32(24, e.data.length, true);
			cd.setUint16(28, name.length, true);
			// extra/comment lengths, disk, attrs (offsets 30-41) stay zero
			cd.setUint32(42, offset, true);       // local header offset
			central.push(new Uint8Array(cd.buffer), name);

			offset += 30 + name.length + e.data.length;
		}

		let centralSize = 0;
		for (const c of central) centralSize += c.length;

		const eocd = new DataView(new ArrayBuffer(22));
		eocd.setUint32(0, 0x06054b50, true);      // end of central directory
		eocd.setUint16(8, entries.length, true);  // entries on this disk
		eocd.setUint16(10, entries.length, true); // total entries
		eocd.setUint32(12, centralSize, true);
		eocd.setUint32(16, offset, true);         // central directory offset
		parts.push(...central, new Uint8Array(eocd.buffer));

		return new Blob(parts, { type: 'application/zip' });
	}

	function buildInfoJson(meta) {
		return JSON.stringify({
			source: 'akuma',
			slug: meta.slug,
			url: meta.url,
			nhentai_id: meta.nhentaiId,
			title: { display: meta.displayTitle, japanese: meta.japaneseTitle || null },
			tags: meta.tags,       // keyed by Doujin Toshokan tag subjects
			tags_raw: meta.tagsRaw, // original namespaced akuma tags
			pages: meta.pages,
			uploaded_at: meta.uploadedAt,
			downloaded_at: new Date().toISOString(),
		}, null, 2);
	}

	async function run() {
		const meta = state.meta;
		const total = meta.pages;
		if (!total) {
			setStatus('Error: could not read page count from .meta-data.pages', true);
			return;
		}
		if (!gmXhr) {
			setStatus('Error: GM_xmlhttpRequest unavailable — check @grant lines', true);
			return;
		}

		state.running = true;
		state.aborted = false;
		window.addEventListener('beforeunload', unloadGuard);
		setButtonRunning(true);

		let pagesDone = 0;
		let imagesDone = 0;
		const pageNums = Array.from({ length: total }, (_, i) => i + 1);

		try {
			const { results, errors } = await pool(pageNums, state.threads, async (n) => {
				const imgUrl = await withRetry(() => fetchPageImageUrl(n));
				pagesDone++;
				setStatus(`Fetching pages ${pagesDone}/${total} · downloading images ${imagesDone}/${total}`);
				const resp = await withRetry(() => gmFetchImage(imgUrl, `${galleryUrl()}/${n}`));
				imagesDone++;
				setStatus(`Fetching pages ${pagesDone}/${total} · downloading images ${imagesDone}/${total}`);
				return { n, data: resp.response, ext: extFor(imgUrl, resp.responseHeaders) };
			});

			if (state.aborted) {
				setStatus('Cancelled.', true);
				return;
			}
			if (errors.length) {
				const failed = errors.map((e) => e.item).sort((a, b) => a - b).join(', ');
				errors.forEach((e) => console.error(`${LOG} page ${e.item}:`, e.error));
				setStatus(`Failed — pages ${failed} errored (see console). Nothing was saved.`, true);
				return;
			}

			setStatus('Zipping…');
			const entries = [
				{ name: 'info.json', data: new TextEncoder().encode(buildInfoJson(meta)) },
			];
			const width = Math.max(3, String(total).length);
			for (const page of results) {
				const data = page.data instanceof Uint8Array ? page.data : new Uint8Array(page.data);
				entries.push({ name: `${String(page.n).padStart(width, '0')}${page.ext}`, data });
			}
			const blob = buildZip(entries);

			const a = document.createElement('a');
			a.href = URL.createObjectURL(blob);
			a.download = state.fileName;
			document.body.appendChild(a);
			a.click();
			a.remove();
			setTimeout(() => URL.revokeObjectURL(a.href), 60000);

			setStatus(`Done — saved ${state.fileName} (${total} pages)`);
		} catch (e) {
			console.error(`${LOG} fatal:`, e);
			setStatus(`Error: ${e.message}`, true);
		} finally {
			state.running = false;
			window.removeEventListener('beforeunload', unloadGuard);
			setButtonRunning(false);
		}
	}

	function unloadGuard(e) {
		e.preventDefault();
		e.returnValue = '';
	}

	// ==== UI ====

	let ui = {};

	function setStatus(msg, isError) {
		if (ui.status) {
			ui.status.textContent = msg;
			ui.status.style.color = isError ? '#e05555' : '';
		}
	}

	function setButtonRunning(running) {
		if (!ui.button) return;
		ui.button.disabled = running;
		ui.button.textContent = running ? 'Working…' : 'Download CBZ';
		if (ui.cancel) ui.cancel.style.display = running ? '' : 'none';
		if (ui.threads) ui.threads.disabled = running;
	}

	function installUI() {
		const host =
			document.querySelector('.side-info') ||
			(document.querySelector('.info-list') || {}).parentElement ||
			document.body;

		const panel = document.createElement('div');
		panel.id = 'dtk-panel';
		panel.style.cssText =
			'margin:10px 0;padding:10px;border:1px solid #888;border-radius:6px;' +
			'font-size:0.85rem;line-height:1.5;max-width:100%;overflow-wrap:anywhere;';

		const preview = document.createElement('div');
		preview.style.cssText = 'font-family:monospace;margin-bottom:8px;opacity:0.9;';
		preview.textContent = state.fileName;
		preview.title = 'Filename the .cbz will be saved as (Doujin Toshokan parses it)';
		panel.appendChild(preview);

		const row = document.createElement('div');
		row.style.cssText = 'display:flex;gap:8px;align-items:center;flex-wrap:wrap;';

		const button = document.createElement('button');
		button.type = 'button';
		button.className = 'btn btn-secondary';
		button.textContent = 'Download CBZ';
		button.onclick = () => { if (!state.running) run(); };
		row.appendChild(button);

		const cancel = document.createElement('button');
		cancel.type = 'button';
		cancel.className = 'btn btn-outline-secondary';
		cancel.textContent = 'Cancel';
		cancel.style.display = 'none';
		cancel.onclick = () => { state.aborted = true; };
		row.appendChild(cancel);

		const threadsLabel = document.createElement('label');
		threadsLabel.textContent = 'Threads:';
		threadsLabel.style.margin = '0';
		row.appendChild(threadsLabel);

		const threads = document.createElement('select');
		for (let i = 1; i <= 6; i++) {
			const o = document.createElement('option');
			o.value = i;
			o.text = String(i);
			threads.appendChild(o);
		}
		threads.value = String(state.threads);
		threads.onchange = () => { state.threads = parseInt(threads.value, 10); };
		row.appendChild(threads);

		panel.appendChild(row);

		const status = document.createElement('div');
		status.style.marginTop = '6px';
		status.textContent = 'Ready.';
		panel.appendChild(status);

		host.appendChild(panel);
		ui = { panel, preview, button, cancel, threads, status };
	}

	// ==== Init ====

	function init() {
		if (!document.querySelector('.info-list')) {
			console.warn(`${LOG} no .info-list found — not a gallery page? aborting.`);
			return;
		}
		const meta = scrapeMeta();
		state.meta = meta;
		state.fileName = buildFileName(meta);

		console.groupCollapsed(`${LOG} scrape report for ${meta.slug}`);
		console.log('display title:', meta.displayTitle || '(EMPTY — check h1 selector)');
		console.log('bare title:', meta.bareTitle);
		console.log('japanese title:', meta.japaneseTitle || '(none)');
		console.log('nhentai id:', meta.nhentaiId || '(none)');
		console.log('pages:', meta.pages || '(EMPTY — check .meta-data.pages)');
		console.log('uploaded at:', meta.uploadedAt || '(none)');
		console.table(meta.tags);
		console.log('tags_raw:', meta.tagsRaw);
		console.log('filename:', state.fileName);
		console.groupEnd();

		installUI();
	}

	// Slight delay like the reference script, in case the page hydrates late.
	setTimeout(init, 200);
})();
