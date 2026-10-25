// Listener ratings for voices: the five questions (a 1-5 opinion score per
// question, as in a MOS listening test), where ratings are stored, and the
// star widgets. Shared by /voices and /rate.
//
// Storage: the API named in <meta name="voxie-ratings-api"> when there is
// one. Without it, on localhost, ratings stay in this browser so the flow can
// be tried; anywhere else rating is off and the page shows only what we
// measured ourselves.
const Ratings = (() => {
  const QUESTIONS = [
    { key: "naturalness", label: "Naturalness", ask: "Does this sound like natural human speech?" },
    { key: "pronunciation", label: "Pronunciation", ask: "Are the words pronounced correctly?" },
    { key: "intelligibility", label: "Intelligibility", ask: "Can you understand everything clearly?" },
    { key: "accent", label: "Accent", ask: "Does the accent sound right for the language?" },
    { key: "overall", label: "Would use it", ask: "Would you use this voice in a real application?" },
  ];
  const api = document.querySelector('meta[name="voxie-ratings-api"]')?.content || "";
  const local = !api && ["localhost", "127.0.0.1"].includes(location.hostname);
  const mode = api ? "api" : local ? "local" : "off";
  // Below this many ratings a voice shows a count, not an average. In the
  // local preview one person rates each voice once, so it's 1 there.
  const MIN = mode === "local" ? 1 : 5;
  const PREVIEW_KEY = "voxie-ratings-preview";

  // Storage can be missing or blocked (private windows): every access is guarded.
  const store = {
    get(key, fallback) { try { return JSON.parse(localStorage.getItem(key)) ?? fallback; } catch { return fallback; } },
    set(key, value) { try { localStorage.setItem(key, JSON.stringify(value)); } catch { /* not remembered */ } },
  };
  // An anonymous id for this browser, so one browser rates a voice once.
  function rater() {
    let id = store.get("voxie-rater", null);
    if (!id) { id = crypto.randomUUID(); store.set("voxie-rater", id); }
    return id;
  }
  const rated = () => new Set(store.get("voxie-rated", []));
  const hasRated = (voice) => rated().has(voice);
  const invite = new URLSearchParams(location.search).get("invite") || store.get("voxie-invite", "");
  if (invite) store.set("voxie-invite", invite);

  // Per voice: { n, native, invited, avg: {question: mean}, regions: {name: count}, last }.
  function summarize(list) {
    const out = {};
    for (const r of list) {
      const s = (out[r.voice] ||= { n: 0, native: 0, invited: 0, sums: {}, regions: {}, last: "" });
      s.n++;
      if (r.native) s.native++;
      if (r.invite) s.invited++;
      if (r.region) s.regions[r.region] = (s.regions[r.region] || 0) + 1;
      if (r.at > s.last) s.last = r.at;
      for (const q of QUESTIONS) s.sums[q.key] = (s.sums[q.key] || 0) + r.scores[q.key];
    }
    for (const s of Object.values(out)) {
      s.avg = Object.fromEntries(QUESTIONS.map((q) => [q.key, s.sums[q.key] / s.n]));
      delete s.sums;
    }
    return out;
  }

  async function summary(lang) {
    if (mode === "off") return {};
    if (mode === "local") return summarize(store.get(PREVIEW_KEY, []).filter((r) => r.lang === lang));
    const r = await fetch(`${api}?lang=${encodeURIComponent(lang)}`, { cache: "no-cache" });
    return r.ok ? (await r.json()).voices || {} : {};
  }

  // rating: { voice, lang, scores: {question: 1-5}, native, region, comment, blind }
  async function submit(rating) {
    if (mode === "off") return { ok: false, error: "Rating isn't open yet." };
    if (hasRated(rating.voice)) return { ok: false, error: "You've already rated this voice." };
    const r = { ...rating, region: (rating.region || "").trim().slice(0, 40), comment: (rating.comment || "").trim().slice(0, 1000),
      invite, rater: rater(), at: new Date().toISOString() };
    if (mode === "local") {
      store.set(PREVIEW_KEY, [...store.get(PREVIEW_KEY, []), r]);
    } else {
      const res = await fetch(api, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(r) });
      if (!res.ok) return { ok: false, error: res.status === 429 ? "Too many ratings at once; try again in a minute." : "Couldn't save your rating; try again." };
    }
    store.set("voxie-rated", [...rated(), rating.voice]);
    return { ok: true };
  }

  // The rater's last answers about themselves, to prefill the next form.
  const about = () => store.get("voxie-rater-about", { native: null, region: "" });
  const setAbout = (a) => store.set("voxie-rater-about", a);

  // A read-only score: five stars filled to the average.
  function stars(avg) {
    return `<span class="r-show" style="--p:${(avg / 5) * 100}%" role="img" aria-label="${avg.toFixed(1)} out of 5">★★★★★</span>`;
  }

  // A 1-5 star input per question, as radio buttons (keyboard and screen
  // reader friendly). Returns the fieldset; read it with readScores().
  function questionFields(prefix) {
    const box = document.createElement("div");
    box.className = "r-questions";
    for (const q of QUESTIONS) {
      const fs = document.createElement("fieldset");
      fs.className = "r-q";
      fs.innerHTML = `<legend><span class="r-q-label">${q.label}</span><span class="r-q-ask">${q.ask}</span></legend><div class="r-stars"></div>`;
      const row = fs.querySelector(".r-stars");
      // Highest first, laid out right-to-left, so CSS can fill the stars up to a checked or hovered one.
      for (let n = 5; n >= 1; n--) {
        const id = `${prefix}-${q.key}-${n}`;
        row.insertAdjacentHTML("beforeend", `<input type="radio" id="${id}" name="${prefix}-${q.key}" value="${n}" required><label for="${id}" title="${n} of 5"><span class="sr">${n} of 5</span>★</label>`);
      }
      box.appendChild(fs);
    }
    return box;
  }
  function readScores(form, prefix) {
    const scores = {};
    for (const q of QUESTIONS) {
      const c = form.querySelector(`input[name="${prefix}-${q.key}"]:checked`);
      if (!c) return null;
      scores[q.key] = Number(c.value);
    }
    return scores;
  }

  return { QUESTIONS, MIN, mode, summary, submit, hasRated, about, setAbout, stars, questionFields, readScores, invite };
})();
