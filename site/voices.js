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
        <div class="v-name"></div>
        <div class="v-meta"><span class="v-tag p p-${key}"></span>${v.model ? '<span class="v-tag m"></span>' : ""}${key === "voxie" ? '<span class="v-tag">self-hosted</span>' : ""}${v.testing ? '<span class="v-tag">not in Voxie yet</span>' : ""}</div>
      </div>
      <div class="v-bar"><span></span></div>`;
    card.querySelector(".v-name").textContent = v.voice;
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
}).catch(() => { $("grid").textContent = "Couldn't load the voices."; });
