// doujin/static/app.js
// Infinite-scroll library grid + tag autocomplete + reader nav + lightbox + toasts.

function esc(s) {
  return String(s)
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// ───── Toast ──────────────────────────────────────────────────────
// Slides in from the right; auto-dismisses after 3s. aria-live region
// in base.html makes the message announced to screen readers too.
const toastRegion = document.getElementById("toast-region");
function toast(msg, kind = "ok") {
  if (!toastRegion) return;
  const el = document.createElement("div");
  el.className = "toast" + (kind === "err" ? " toast-err" : "");
  el.textContent = msg;
  toastRegion.appendChild(el);
  // Two-frame defer so the initial transform applies before the .in class.
  requestAnimationFrame(() => requestAnimationFrame(() => el.classList.add("in")));
  setTimeout(() => {
    el.classList.remove("in");
    setTimeout(() => el.remove(), 400);
  }, 3000);
}

// ───── Library grid ───────────────────────────────────────────────
const grid = document.getElementById("grid");
const q = document.getElementById("q");
const sentinel = document.getElementById("scroll-sentinel");

function card(m) {
  const cover = m.cover_rel_path
    ? `<img loading="lazy" src="/thumb?path=${encodeURIComponent(m.folder_path + "/" + m.cover_rel_path)}&w=240" alt="">`
    : `<div class="nocover"></div>`;
  return `<a class="card" href="/manga/${m.id}">
    <div class="card-cover">${cover}</div>
    <div class="meta"><span class="t">${esc(m.title)}</span><span class="a">${esc(m.author)}</span></div>
  </a>`;
}

function removeSkeletons() {
  grid?.querySelectorAll(".card.skeleton").forEach((el) => el.remove());
}

if (grid) {
  const state = {
    pageSize: parseInt(grid.dataset.pageSize || "60", 10),
    offset: parseInt(grid.dataset.nextOffset || "0", 10),
    loading: false,
    done: false,
    errored: false,
  };
  state.done = state.offset < state.pageSize;

  function currentParams() {
    const p = new URLSearchParams();
    const sort = document.querySelector("[name=sort]")?.value || grid.dataset.sort || "title";
    const query = (q && q.value) ? q.value : (grid.dataset.q || "");
    if (query) p.set("q", query);
    p.set("sort", sort);
    if (grid.dataset.author) p.set("author", grid.dataset.author);
    (grid.dataset.tags ? grid.dataset.tags.split(",") : [])
      .filter(Boolean).forEach((t) => p.append("tag", t));
    return p;
  }

  function showRetryPill() {
    state.errored = true;
    if (grid.querySelector(".error-pill")) return;
    const pill = document.createElement("p");
    pill.className = "error-pill";
    pill.innerHTML = `Couldn't load more. <button type="button">Retry</button>`;
    pill.querySelector("button").addEventListener("click", () => {
      pill.remove();
      state.errored = false;
      loadMore();
    });
    grid.appendChild(pill);
  }

  async function loadMore() {
    if (state.loading || state.done || state.errored) return;
    state.loading = true;
    let ok = false;
    try {
      const p = currentParams();
      p.set("limit", state.pageSize);
      p.set("offset", state.offset);
      const res = await fetch(`/api/search?${p.toString()}`);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      removeSkeletons();
      if (state.offset === 0 && data.length === 0) {
        grid.innerHTML = '<p class="empty">No matches. <a href="/">clear filters</a></p>';
      } else {
        grid.insertAdjacentHTML("beforeend", data.map(card).join(""));
      }
      state.offset += data.length;
      if (data.length < state.pageSize) state.done = true;
      ok = true;
    } catch (_e) {
      showRetryPill();
    } finally {
      state.loading = false;
    }
    if (ok && !state.done && sentinel) {
      if (sentinel.getBoundingClientRect().top < window.innerHeight) loadMore();
    }
  }

  function reset() {
    grid.dataset.q = "";
    grid.dataset.author = "";
    grid.dataset.tags = "";
    state.offset = 0;
    state.done = false;
    state.errored = false;
    grid.innerHTML = "";
    loadMore();
  }

  let timer;
  if (q) {
    q.addEventListener("input", () => {
      clearTimeout(timer);
      timer = setTimeout(reset, 200);
    });
  }
  document.querySelector("[name=sort]")?.addEventListener("change", reset);

  if (sentinel && "IntersectionObserver" in window) {
    new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) loadMore();
    }).observe(sentinel);
  }

  // Rescan hijack — XHR + toast + refresh grid, no full page navigation.
  document.querySelector("form[data-rescan]")?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const form = e.currentTarget;
    const btn = form.querySelector("button");
    btn?.setAttribute("disabled", "true");
    try {
      const res = await fetch("/rescan", { method: "POST" });
      if (!res.ok && res.status !== 303) throw new Error();
      toast("Library rescanned");
      reset();
    } catch (_e) {
      toast("Rescan failed", "err");
    } finally {
      btn?.removeAttribute("disabled");
    }
  });
}

// ───── Tag autocomplete (unchanged behavior, kept) ────────────────
document.querySelectorAll(".tag-input").forEach((input) => {
  input.addEventListener("input", async () => {
    const token = input.value.split(",").pop().trim();
    if (!token) return;
    const res = await fetch(`/api/tags/suggest?q=${encodeURIComponent(token)}`);
    const names = await res.json();
    let dl = input.list;
    if (!dl) {
      dl = document.createElement("datalist");
      dl.id = "tags-" + Math.random().toString(36).slice(2);
      input.setAttribute("list", dl.id);
      input.after(dl);
    }
    dl.innerHTML = names.map((n) => `<option value="${esc(n)}">`).join("");
  });
});

// ───── Reader (title page) ────────────────────────────────────────
// Active only on body[data-page="title"]. Adds keyboard nav, the bottom
// page counter (driven by IntersectionObserver), image preload, and an
// improved lightbox with prev/next.
if (document.body.dataset.page === "title") {
  const pageImgs = Array.from(document.querySelectorAll(".gallery img"));
  const counterCur = document.querySelector(".reader-counter .cur");
  const helpHint = document.querySelector(".reader-help");
  let currentIdx = 0;
  let helpShown = false;

  function scrollToPage(i) {
    const target = pageImgs[Math.max(0, Math.min(pageImgs.length - 1, i))];
    if (!target) return;
    target.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  function preloadNeighbors(i) {
    [i + 1, i + 2].forEach((j) => {
      const img = pageImgs[j];
      if (img && img.src) { const p = new Image(); p.src = img.src; }
    });
  }

  function showHelp() {
    if (helpShown || !helpHint) return;
    helpShown = true;
    helpHint.classList.add("visible");
    setTimeout(() => helpHint.classList.remove("visible"), 3500);
  }

  // Page counter: track the most-intersecting page.
  if (counterCur && "IntersectionObserver" in window) {
    let pending = false;
    const visibility = new Map();
    const io = new IntersectionObserver((entries) => {
      entries.forEach((e) => visibility.set(e.target, e.intersectionRatio));
      if (pending) return;
      pending = true;
      requestAnimationFrame(() => {
        pending = false;
        let bestRatio = 0, bestIdx = currentIdx;
        pageImgs.forEach((img, i) => {
          const r = visibility.get(img) || 0;
          if (r > bestRatio) { bestRatio = r; bestIdx = i; }
        });
        if (bestIdx !== currentIdx) {
          currentIdx = bestIdx;
          counterCur.textContent = String(bestIdx + 1);
          preloadNeighbors(bestIdx);
        }
      });
    }, { threshold: [0, 0.25, 0.5, 0.75, 1] });
    pageImgs.forEach((img) => io.observe(img));
  }

  // First scroll reveals the keyboard help once.
  window.addEventListener("scroll", showHelp, { once: true, passive: true });

  // Keyboard navigation. When lightbox is open, the lightbox handler
  // (further down) catches keys first and stops propagation.
  document.addEventListener("keydown", (e) => {
    if (document.querySelector(".lightbox")) return;
    if (e.target.matches?.("input, textarea, select")) return;
    if (e.key === "ArrowLeft" || e.key === "k" || e.key === "PageUp") {
      e.preventDefault(); scrollToPage(currentIdx - 1); showHelp();
    } else if (e.key === "ArrowRight" || e.key === "j" || e.key === "PageDown" || e.key === " ") {
      e.preventDefault(); scrollToPage(currentIdx + 1); showHelp();
    } else if (e.key === "f" || e.key === "F") {
      e.preventDefault();
      const next = document.body.dataset.fit === "height" ? "" : "height";
      if (next) document.body.dataset.fit = next; else delete document.body.dataset.fit;
      showHelp();
    }
  });
}

// ───── Lightbox (improved) ────────────────────────────────────────
// Click any gallery image → overlay with close button + prev/next.
// Esc closes; arrow keys cycle through the gallery list while open.
function openLightbox(startIdx) {
  const imgs = Array.from(document.querySelectorAll(".gallery img"));
  if (!imgs.length) return;
  let idx = Math.max(0, Math.min(imgs.length - 1, startIdx));
  const box = document.createElement("div");
  box.className = "lightbox";
  box.innerHTML = `
    <button class="lb-nav lb-prev" type="button" aria-label="Previous">‹</button>
    <img src="${imgs[idx].src}" alt="">
    <button class="lb-nav lb-next" type="button" aria-label="Next">›</button>
    <button class="lb-close" type="button" aria-label="Close">×</button>
  `;
  const imgEl = box.querySelector("img");
  function show(i) {
    idx = (i + imgs.length) % imgs.length;
    imgEl.src = imgs[idx].src;
  }
  function close() {
    box.remove();
    document.removeEventListener("keydown", onKey, true);
  }
  function onKey(e) {
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(); }
    else if (e.key === "ArrowLeft") { e.preventDefault(); e.stopPropagation(); show(idx - 1); }
    else if (e.key === "ArrowRight") { e.preventDefault(); e.stopPropagation(); show(idx + 1); }
  }
  box.addEventListener("click", (e) => {
    const t = e.target;
    if (t.classList.contains("lb-prev")) { e.stopPropagation(); show(idx - 1); }
    else if (t.classList.contains("lb-next")) { e.stopPropagation(); show(idx + 1); }
    else if (t.classList.contains("lb-close")) { e.stopPropagation(); close(); }
    else if (t === box) { close(); }
  });
  document.addEventListener("keydown", onKey, true);
  document.body.appendChild(box);
}
document.querySelectorAll(".gallery img").forEach((img, i) => {
  img.addEventListener("click", () => openLightbox(i));
});

// ───── Tag editor (title page) ────────────────────────────────────
// Toggle reveals an inline form prefilled with the current tags. Submit
// posts over XHR and re-renders the chip row in place; the .tag-input gets
// autocomplete for free from the generic binding above. With JS off the form
// still posts and the 303 reloads the page showing the new tags.
const tagsBlock = document.querySelector(".tags-block");
if (tagsBlock) {
  const toggle = tagsBlock.querySelector(".tag-edit-toggle");
  const form = tagsBlock.querySelector(".tag-edit");
  const cancel = tagsBlock.querySelector(".tag-edit-cancel");
  const input = tagsBlock.querySelector(".tag-input");
  const row = tagsBlock.querySelector("#tagrow");

  function openEditor() {
    form.hidden = false;
    toggle.hidden = true;
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
  }
  function closeEditor() {
    form.hidden = true;
    toggle.hidden = false;
  }

  // Mirror the server's normalization (ingest.normalize_tag + dedupe + sort) so
  // the optimistic re-render matches what /manga/{id}/tags actually stored.
  function normalizeTags(raw) {
    const seen = [];
    raw.split(",").map((t) => t.trim().toLowerCase()).forEach((t) => {
      if (t && !seen.includes(t)) seen.push(t);
    });
    return seen.sort();
  }

  toggle?.addEventListener("click", openEditor);
  cancel?.addEventListener("click", closeEditor);

  form?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const btn = form.querySelector("button[type=submit]");
    btn?.setAttribute("disabled", "true");
    try {
      const fd = new FormData(form);
      const res = await fetch(form.action, { method: "POST", body: fd, redirect: "manual" });
      // 303 redirect = success; opaqueredirect/0 also count (manual redirect).
      if (!(res.type === "opaqueredirect" || res.ok || res.status === 303 || res.status === 0)) {
        throw new Error(`HTTP ${res.status}`);
      }
      const tags = normalizeTags(input.value);
      input.value = tags.join(", ");
      row.innerHTML = tags
        .map((t) => `<a href="/?tag=${encodeURIComponent(t)}">#${esc(t)}</a>`)
        .join("");
      toggle.textContent = tags.length ? "Edit tags" : "+ Add tags";
      closeEditor();
      toast("Tags saved");
    } catch (_e) {
      toast("Couldn't save tags", "err");
    } finally {
      btn?.removeAttribute("disabled");
    }
  });
}

// ───── Scan / Ingest form hijack ──────────────────────────────────
// Each ingest row submits over XHR so we can fade the row out and toast
// without a full page reload. Falls back to the native submit on error.
document.querySelectorAll("form.ingest-row").forEach((form) => {
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const status = form.querySelector(".row-status");
    const btn = form.querySelector("button[type=submit]");
    btn?.setAttribute("disabled", "true");
    form.classList.add("saving");
    if (status) { status.classList.remove("err"); status.textContent = "saving…"; }
    try {
      const fd = new FormData(form);
      const res = await fetch("/ingest", { method: "POST", body: fd, redirect: "manual" });
      // 303 redirect = success path; opaqueredirect status also counts.
      if (res.type === "opaqueredirect" || res.ok || res.status === 303 || res.status === 0) {
        form.classList.add("saved");
        const title = form.dataset.title || "manga";
        toast(`Saved “${title}”`);
        setTimeout(() => form.remove(), 400);
      } else {
        throw new Error(`HTTP ${res.status}`);
      }
    } catch (_err) {
      form.classList.remove("saving");
      if (status) { status.classList.add("err"); status.textContent = "save failed"; }
      btn?.removeAttribute("disabled");
      toast("Save failed", "err");
    }
  });
});
