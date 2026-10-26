// /voice-stats: one table of every voice's listening and feedback numbers,
// across languages, sortable. Reads the same summaries as /voices.
const out = document.getElementById("out");
const esc = (t) => String(t).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
const pct = (x) => (x == null ? "–" : `${Math.round(x * 100)}%`);
// Each column: header, how to read the sort value from a row, how to show it.
const COLUMNS = [
  { key: "lang", label: "Language", get: (r) => r.langName },
  { key: "voice", label: "Voice", get: (r) => r.voice },
  { key: "provider", label: "Provider", get: (r) => r.provider },
  { key: "plays", label: "Plays", get: (r) => r.plays, num: true },
  { key: "half", label: "Past 50%", get: (r) => r.half, num: true, show: pct },
  { key: "end", label: "To the end", get: (r) => r.end, num: true, show: pct },
  { key: "likes", label: "Likes", get: (r) => r.likes, num: true },
  { key: "dislikes", label: "Dislikes", get: (r) => r.dislikes, num: true },
  { key: "liked", label: "Liked", get: (r) => r.liked, num: true, show: pct },
  { key: "rating", label: "Rating", get: (r) => r.rating, num: true, show: (x, r) => (x == null ? (r.n ? `${r.n} so far` : "–") : `${x.toFixed(1)} (${r.n})`) },
];
let rows = [], sortKey = "plays", desc = true;

function build(data, sums) {
  rows = [];
  for (const l of data.languages) for (const v of l.voices) {
    const id = v.audio.split("/").pop().replace(/\.mp3$/, "");
    const s = sums[id] || { n: 0 }, st = s.listening || { plays: 0, reach: {}, likes: 0, dislikes: 0 };
    const votes = st.likes + st.dislikes;
    rows.push({
      lang: l.code, langName: l.name, voice: v.voice, provider: v.provider.replace(/\s*\((self-hosted|in testing)\)/, ""),
      plays: st.plays, half: st.plays ? Math.min(1, st.reach[50] / st.plays) : null, end: st.plays ? Math.min(1, st.reach[100] / st.plays) : null,
      likes: st.likes, dislikes: st.dislikes, liked: votes ? st.likes / votes : null,
      n: s.n, rating: s.n >= Ratings.MIN ? Object.values(s.avg).reduce((a, b) => a + b, 0) / Ratings.QUESTIONS.length : null,
    });
  }
}

function render() {
  const langSel = document.getElementById("langSel").value, only = document.getElementById("onlyActive").checked;
  const col = COLUMNS.find((c) => c.key === sortKey);
  const shown = rows.filter((r) => (langSel === "all" || r.lang === langSel) && (!only || r.plays || r.n))
    .sort((a, b) => {
      const x = col.get(a), y = col.get(b);
      if (x == null || y == null) return (x == null) - (y == null); // empty values last either way
      return (col.num ? x - y : String(x).localeCompare(String(y))) * (desc ? -1 : 1);
    });
  if (!shown.length) { out.innerHTML = '<p class="d-muted">Nothing yet: no voice has been played or rated.</p>'; return; }
  out.innerHTML = `<table class="s-table"><thead><tr>${COLUMNS.map((c) =>
    `<th scope="col" aria-sort="${c.key === sortKey ? (desc ? "descending" : "ascending") : "none"}"${c.num ? ' class="s-num"' : ""}><button type="button" data-key="${c.key}">${c.label}${c.key === sortKey ? (desc ? " ↓" : " ↑") : ""}</button></th>`).join("")}</tr></thead>
    <tbody>${shown.map((r) => `<tr>${COLUMNS.map((c) => {
      const x = c.get(r);
      return `<td${c.num ? ' class="s-num"' : ""}>${esc(c.show ? c.show(x, r) : x)}</td>`;
    }).join("")}</tr>`).join("")}</tbody></table>
    <p class="d-muted">${shown.length} voice${shown.length > 1 ? "s" : ""}. "Past 50%" and "To the end" are shares of plays; "Liked" is likes out of all votes; ratings average the five questions and show from ${Ratings.MIN} rating${Ratings.MIN > 1 ? "s" : ""}.</p>`;
  for (const b of out.querySelectorAll("th button")) b.addEventListener("click", () => {
    desc = b.dataset.key === sortKey ? !desc : !!COLUMNS.find((c) => c.key === b.dataset.key).num;
    sortKey = b.dataset.key;
    render();
  });
}

if (Ratings.mode === "off") {
  out.innerHTML = '<p class="d-muted">Stats appear once listening is being recorded.</p>';
} else {
  Promise.all([fetch("/media/voices/voices.json", { cache: "no-cache" }).then((r) => r.json()), Ratings.summary("all")]).then(([data, sums]) => {
    const sel = document.getElementById("langSel");
    for (const l of data.languages) sel.insertAdjacentHTML("beforeend", `<option value="${l.code}">${esc(l.name)}</option>`);
    document.getElementById("controls").hidden = false;
    sel.addEventListener("change", render);
    document.getElementById("onlyActive").addEventListener("change", render);
    build(data, sums);
    render();
  }).catch(() => { out.textContent = "Couldn't load the stats."; });
}
