// /voices: every voice Voxie can speak with, per language. The clips and
// media/voices/voices.json listing them are generated, not hand-written.
const $ = (id) => document.getElementById(id);
const INDIAN = ["hi", "bn", "gu", "kn", "ml", "mr", "od", "pa", "ta", "te"];
const FONTS = { hi: "Noto Sans Devanagari", mr: "Noto Sans Devanagari", bn: "Noto Sans Bengali", gu: "Noto Sans Gujarati",
  kn: "Noto Sans Kannada", ml: "Noto Sans Malayalam", od: "Noto Sans Oriya", pa: "Noto Sans Gurmukhi", ta: "Noto Sans Tamil",
  te: "Noto Sans Telugu", ja: "Noto Sans JP", zh: "Noto Sans SC" };
// How a voice's provider is shown and filtered. Kokoro and Piper are Voxie's own;
// voices marked "testing" are ones Voxie can't speak with yet. The provider
// pills come from the data, so a new provider needs no change here.
const providerName = (v) => v.provider.replace(/\s*\((self-hosted|in testing)\)/, "");
const providerKey = (v) => (/kokoro|piper/i.test(v.provider) ? "voxie" : providerName(v).toLowerCase().replace(/[^a-z0-9]+/g, "-"));
let PROVIDERS = [];
function buildProviders() {
  // Providers Voxie can use today first, then the ones it can't yet.
  const seen = new Map([["voxie", { label: "Voxie's own", ready: true }]]);
  for (const l of data.languages) for (const v of l.voices) {
    const p = seen.get(providerKey(v)) || { label: providerName(v), ready: false };
    p.ready ||= !v.testing;
    seen.set(providerKey(v), p);
  }
  const ordered = [...seen].sort(([ka, a], [kb, b]) => (kb === "voxie") - (ka === "voxie") || b.ready - a.ready || a.label.localeCompare(b.label));
  PROVIDERS = [{ key: "all", label: "All" }, ...ordered.map(([key, p]) => ({ key, label: p.label })), { key: "notyet", label: "Not in Voxie yet" }];
}
const matches = (v) => provider === "all" || (provider === "notyet" ? !!v.testing : providerKey(v) === provider);
const PLAY = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2.5v11l9-5.5z"/></svg>';
const STOP = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 3h3v10H4zM9 3h3v10H9z"/></svg>';

let data = null, lang = "en", provider = "all";
let scores = {}; // listener ratings for the shown language, by voice id
const voiceId = (v) => v.audio.split("/").pop().replace(/\.mp3$/, "");
const audio = new Audio();
let playingCard = null;

function loadFont(code, text) {
  const family = FONTS[code];
  if (!family || document.querySelector(`link[data-font="${code}"]`)) return;
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.dataset.font = code;
  // Only the glyphs this page shows: a CJK font is megabytes otherwise.
  link.href = `https://fonts.googleapis.com/css2?family=${encodeURIComponent(family)}:wght@400;500&display=swap&text=${encodeURIComponent(text)}`;
  document.head.appendChild(link);
}

function pill(label, count, selected, onClick, role) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "v-pill";
  b.setAttribute(role === "tab" ? "aria-selected" : "aria-pressed", String(selected));
  if (role === "tab") b.setAttribute("role", "tab");
  b.innerHTML = `${label}${count != null ? `<span class="n">${count}</span>` : ""}`;
  b.addEventListener("click", onClick);
  return b;
}

function stop() {
  audio.pause();
  if (playingCard) {
    playingCard.classList.remove("playing");
    playingCard.querySelector(".v-play").innerHTML = PLAY;
    playingCard.querySelector(".v-bar span").style.width = "0";
    playingCard = null;
  }
}

// The language names in their own scripts, for the pills: one small request
// for exactly these characters in every script's font.
let pillFontsLoaded = false;
function loadPillFonts() {
  if (pillFontsLoaded) return;
  pillFontsLoaded = true;
  const families = [...new Set(Object.values(FONTS))];
  const text = [...new Set(data.languages.map((l) => l.native).join(""))].join("");
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = "https://fonts.googleapis.com/css2?" + families.map((f) => `family=${encodeURIComponent(f)}`).join("&") +
    `&display=swap&text=${encodeURIComponent(text)}`;
  document.head.appendChild(link);
}

function renderPills() {
  loadPillFonts();
  for (const [id, codes] of [["indian", INDIAN], ["world", data.languages.map((l) => l.code).filter((c) => !INDIAN.includes(c))]]) {
    const box = $(id);
    box.replaceChildren(...codes.map((code) => {
      const l = data.languages.find((x) => x.code === code);
      return pill(`${l.native}${l.native !== l.name ? ` · ${l.name}` : ""}`, l.voices.length, code === lang, () => { lang = code; history.replaceState(null, "", `#${code}`); render(); }, "tab");
    }));
  }
  $("providers").replaceChildren(...PROVIDERS.map((p) => pill(p.label, null, p.key === provider, () => { provider = p.key; render(); })));
}

function render() {
  stop();
  renderPills();
  const l = data.languages.find((x) => x.code === lang);
  loadFont(l.code, l.text + l.native);
  const font = FONTS[l.code] ? `'${FONTS[l.code]}', var(--font)` : "var(--font)";
  $("langName").textContent = l.name;
  $("langNative").textContent = l.native !== l.name ? l.native : "";
  $("langNative").style.fontFamily = font;
  $("langText").textContent = l.text;
  $("langText").style.fontFamily = font;
  $("langText").lang = l.code;
  const voices = l.voices.filter(matches);
  $("count").textContent = `${voices.length} of ${l.voices.length} voices`;
  if (!voices.length) {
    const p = document.createElement("p");
    p.className = "v-empty";
    p.textContent = "No voice from this provider speaks this language.";
    $("grid").replaceChildren(p);
    return;
  }
  $("grid").replaceChildren(...voices.map((v) => {
    const card = document.createElement("div");
    card.className = "v-card";
    const key = providerKey(v);
    const name = providerName(v);
    card.innerHTML = `
      <button class="v-play" type="button" aria-label="Play ${l.name}: ${name}, ${v.voice}">${PLAY}</button>
      <div>
        <button class="v-name" type="button" aria-haspopup="dialog"></button>
        <div class="v-meta"><span class="v-tag p p-${key}"></span>${v.model ? '<span class="v-tag m"></span>' : ""}${key === "voxie" ? '<span class="v-tag">self-hosted</span>' : ""}${v.testing ? '<span class="v-tag">not in Voxie yet</span>' : ""}</div>
        <div class="v-score"></div>
      </div>
      <div class="v-bar"><span></span></div>`;
    card.dataset.voice = voiceId(v);
    card.querySelector(".v-name").textContent = v.voice;
    card.querySelector(".v-name").setAttribute("aria-label", `${v.voice}, ${name}: details and ratings`);
    card.querySelector(".v-name").addEventListener("click", () => openDetails(v, l));
    card.querySelector(".p").textContent = name;
    if (v.model) card.querySelector(".m").textContent = v.model;
    card.querySelector(".v-play").addEventListener("click", () => {
      if (playingCard === card) return stop();
      stop();
      playingCard = card;
      card.classList.add("playing");
      card.querySelector(".v-play").innerHTML = STOP;
      audio.src = v.audio;
      audio.play().catch(stop);
    });
    return card;
  }));
  scoreLines();
  const shown = lang;
  Ratings.summary(lang).then((s) => { if (shown === lang) { scores = s; scoreLines(); } }).catch(() => {});
}

// The one-line rating on each card: an average once enough people have rated.
function scoreLine(id) {
  const s = scores[id];
  if (!s) return "Not rated yet";
  if (s.n < Ratings.MIN) return `${s.n} rating${s.n > 1 ? "s" : ""} so far`;
  const avg = Object.values(s.avg).reduce((a, b) => a + b, 0) / Ratings.QUESTIONS.length;
  return `${Ratings.stars(avg)} <b>${avg.toFixed(1)}</b> · ${s.n} rating${s.n > 1 ? "s" : ""}${s.native ? `, ${s.native} native` : ""}`;
}
function scoreLines() {
  if (Ratings.mode === "off") return;
  for (const card of $("grid").querySelectorAll(".v-card")) card.querySelector(".v-score").innerHTML = scoreLine(card.dataset.voice);
}

const MONTH = new Intl.DateTimeFormat("en", { month: "long", year: "numeric" });
// The voice's detail panel: listener ratings, what we measured, and the form to rate it.
function openDetails(v, l) {
  const dlg = $("voiceDialog"), body = $("dBody"), id = voiceId(v), s = scores[id];
  const esc = (t) => String(t).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
  let ratings;
  if (Ratings.mode === "off") ratings = `<p class="d-muted">Listener ratings open soon.</p>`;
  else if (!s) ratings = `<p class="d-muted">No ratings yet.</p>`;
  else if (s.n < Ratings.MIN) ratings = `<p class="d-muted">${s.n} rating${s.n > 1 ? "s" : ""} so far; averages show from ${Ratings.MIN}.</p>`;
  else {
    const regions = Object.entries(s.regions).sort((a, b) => b[1] - a[1]).slice(0, 4).map(([r, n]) => `${esc(r)} ${n}`).join(", ");
    ratings = `<dl class="d-scores">${Ratings.QUESTIONS.map((q) =>
      `<div><dt title="${q.ask}">${q.label}</dt><dd>${Ratings.stars(s.avg[q.key])} <b>${s.avg[q.key].toFixed(1)}</b>/5</dd></div>`).join("")}</dl>
      <p class="d-muted">Rated by ${s.n} listener${s.n > 1 ? "s" : ""}${s.native ? `, ${s.native} native speaker${s.native > 1 ? "s" : ""} (self-declared)` : ""}${s.invited ? `, ${s.invited} invited` : ""}.${regions ? ` Accents: ${regions}.` : ""}${s.last ? ` Last rated ${MONTH.format(new Date(s.last))}.` : ""}</p>`;
  }
  const m = v.measured;
  const measured = m
    ? `<p>Read back ${Math.round(m.readback * 100)}% by speech-to-text (${esc(m.checked_with)}).</p><p>${m.rtf} seconds of compute per second of speech on our test GPU (a laptop RTX 5050)${m.rtf > 1 ? ", too slow for a live call there" : ""}.</p>`
    : `<p class="d-muted">Not measured yet.</p>`;
  body.innerHTML = `
    <p class="kicker">${esc(l.name)} · ${esc(providerName(v))}</p>
    <h2 id="dTitle">${esc(v.voice)}</h2>
    <p class="d-status">${v.testing ? "Not in Voxie yet: shown so you can compare." : providerKey(v) === "voxie" ? "Voxie's own, self-hosted." : "Plugs into Voxie with your own key."}</p>
    <button class="btn btn-dark btn-sm d-play" type="button">Play</button>
    <section><h3>Listener ratings</h3>${ratings}</section>
    <section><h3>Measured by us</h3>${measured}</section>
    <section class="d-rate" ${Ratings.mode === "off" ? "hidden" : ""}><h3>Rate this voice</h3></section>`;
  body.querySelector(".d-play").addEventListener("click", (e) => {
    const playing = !audio.paused && audio.src.endsWith(v.audio);
    stop();
    if (playing) return;
    audio.src = v.audio; audio.play().catch(() => {}); e.target.textContent = "Stop";
    audio.onended = audio.onpause = () => { e.target.textContent = "Play"; };
  });
  if (Ratings.mode !== "off") body.querySelector(".d-rate").append(rateForm(v, l, () => openDetails(v, l)));
  if (!dlg.open) dlg.showModal();
}

// The rating form, or a thank-you once this browser has rated the voice.
function rateForm(v, l, done) {
  const id = voiceId(v);
  const wrap = document.createElement("div");
  if (Ratings.hasRated(id)) {
    wrap.innerHTML = `<p class="d-muted">Thanks, you've rated this voice.</p>`;
    return wrap;
  }
  const about = Ratings.about();
  const form = document.createElement("form");
  form.className = "r-form";
  form.append(Ratings.questionFields(`d-${id}`));
  form.insertAdjacentHTML("beforeend", `
    <fieldset class="r-about"><legend>Are you a native ${l.name} speaker?</legend>
      <label><input type="radio" name="native" value="yes" ${about.native === true ? "checked" : ""} required> Yes</label>
      <label><input type="radio" name="native" value="no" ${about.native === false ? "checked" : ""}> No</label></fieldset>
    <label class="r-field">Where is your accent from? <span class="d-muted">(optional)</span><input name="region" maxlength="40" placeholder="e.g. Mumbai, North India, Lyon"></label>
    <label class="r-field">Anything else? <span class="d-muted">(optional; only the Voxie team reads it)</span><textarea name="comment" rows="2" maxlength="1000"></textarea></label>
    ${Ratings.mode === "local" ? '<p class="d-muted">Preview: ratings are saved in this browser only.</p>' : ""}
    <p class="r-error" role="alert" hidden></p>
    <button class="btn btn-dark btn-sm" type="submit">Send rating</button>`);
  form.region.value = about.region || "";
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const scoresIn = Ratings.readScores(form, `d-${id}`);
    const err = form.querySelector(".r-error");
    if (!scoresIn) { err.textContent = "Please answer all five questions."; err.hidden = false; return; }
    const native = form.native.value === "yes";
    Ratings.setAbout({ native, region: form.region.value.trim() });
    form.querySelector("button[type=submit]").disabled = true;
    const r = await Ratings.submit({ voice: id, lang: l.code, scores: scoresIn, native, region: form.region.value, comment: form.comment.value, blind: false });
    if (!r.ok) { err.textContent = r.error; err.hidden = false; form.querySelector("button[type=submit]").disabled = false; return; }
    scores = await Ratings.summary(l.code).catch(() => scores);
    scoreLines();
    done();
  });
  wrap.append(form);
  return wrap;
}

audio.addEventListener("timeupdate", () => {
  if (playingCard && audio.duration) playingCard.querySelector(".v-bar span").style.width = `${(audio.currentTime / audio.duration) * 100}%`;
});
audio.addEventListener("ended", stop);

// Revalidated every load: the list changes whenever voices are added, and a
// cached copy would show new page code with old voices.
fetch("/media/voices/voices.json", { cache: "no-cache" }).then((r) => r.json()).then((d) => {
  data = d;
  buildProviders();
  const fromHash = location.hash.slice(1);
  if (data.languages.some((l) => l.code === fromHash)) lang = fromHash;
  const count = (testing) => data.languages.reduce((n, l) => n + l.voices.filter((v) => !!v.testing === testing).length, 0);
  const testing = count(true);
  $("lede").textContent = `${data.languages.length} languages, ${count(false)} voices${testing ? `, plus ${testing} not in Voxie yet` : ""}. Every voice reads the same two lines in its language. Voxie's own voices run on your hardware; the others are providers you plug in with a key.`;
  render();
  if (Ratings.mode !== "off") $("rateLink").hidden = false;
}).catch(() => { $("grid").textContent = "Couldn't load the voices."; });
