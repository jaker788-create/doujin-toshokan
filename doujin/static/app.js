// doujin/static/app.js
// Infinite-scroll library grid + tag autocomplete + gallery lightbox.
function esc(s) {
  return String(s)
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}
// Infinite-scroll library grid: the server renders page 0 and stamps the grid
// with the active filters + how many cards it rendered (data-next-offset). We
// append further pages from /api/search as a sentinel scrolls into view. A
// filter change (typing / sort) is a hard reset; a scroll is an append.
const grid = document.getElementById("grid");
const q = document.getElementById("q");
const sentinel = document.getElementById("scroll-sentinel");

function card(m) {
  const cover = m.cover_rel_path
    ? `<img loading="lazy" src="/thumb?path=${encodeURIComponent(m.folder_path + "/" + m.cover_rel_path)}&w=240">`
    : `<div class="nocover"></div>`;
  return `<a class="card" href="/manga/${m.id}">${cover}
    <div class="meta"><span class="t">${esc(m.title)}</span><span class="a">${esc(m.author)}</span></div></a>`;
}

if (grid) {
  const state = {
    pageSize: parseInt(grid.dataset.pageSize || "60", 10),
    offset: parseInt(grid.dataset.nextOffset || "0", 10),
    loading: false,
    done: false,
  };
  // If the server already rendered fewer than a full page, there is no more.
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

  async function loadMore() {
    if (state.loading || state.done) return;
    state.loading = true;
    let ok = false;
    try {
      const p = currentParams();
      p.set("limit", state.pageSize);
      p.set("offset", state.offset);
      const res = await fetch(`/api/search?${p.toString()}`);
      const data = await res.json();
      if (state.offset === 0 && data.length === 0) {
        grid.innerHTML = '<p class="empty">No matches.</p>';
      } else {
        grid.insertAdjacentHTML("beforeend", data.map(card).join(""));
      }
      state.offset += data.length;
      if (data.length < state.pageSize) state.done = true;
      ok = true;
    } finally {
      // Always clear the flag — a thrown fetch must not deadlock the scroll.
      state.loading = false;
    }
    // IntersectionObserver only fires on a visibility CHANGE. If the page we
    // appended didn't push the sentinel off-screen, keep filling. Only on
    // success, so a failing fetch can't spin in a tight retry loop.
    if (ok && !state.done && sentinel) {
      if (sentinel.getBoundingClientRect().top < window.innerHeight) loadMore();
    }
  }

  function reset() {
    // The live query box wins over the server-rendered filter bias.
    grid.dataset.q = "";
    grid.dataset.author = "";
    grid.dataset.tags = "";
    state.offset = 0;
    state.done = false;
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
}

// Tag autocomplete: show a datalist of existing tags for the last comma-token.
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

// Lightbox: click any gallery image to view fullscreen; click again to close.
document.querySelectorAll(".gallery img").forEach((img) => {
  img.addEventListener("click", () => {
    const box = document.createElement("div");
    box.className = "lightbox";
    box.innerHTML = `<img src="${img.src}">`;
    box.addEventListener("click", () => box.remove());
    document.body.appendChild(box);
  });
});
