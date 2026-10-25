// /rate: blind rating. The listener picks a language, then hears that
// language's voices in a random order with names and providers hidden, and
// rates each on the five questions. The reveal comes at the end, so what
// they know about a provider can't colour what they hear.
const stage = document.getElementById("stage");
const INDIAN = ["hi", "bn", "gu", "kn", "ml", "mr", "od", "pa", "ta", "te"];
const audio = new Audio();
const voiceId = (v) => v.audio.split("/").pop().replace(/\.mp3$/, "");
const esc = (t) => String(t).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
let data = null;

function shuffle(a) {
  for (let i = a.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [a[i], a[j]] = [a[j], a[i]]; }
  return a;
}

function pickLanguage() {
  const group = (title, codes) => `<div class="v-group"><span class="v-label">${title}</span><div class="v-pills">${codes.map((c) => {
    const l = data.languages.find((x) => x.code === c);
    const left = l.voices.filter((v) => !Ratings.hasRated(voiceId(v))).length;
    return `<button class="v-pill" type="button" data-lang="${c}" ${left ? "" : "disabled"}>${esc(l.native)}${l.native !== l.name ? ` · ${esc(l.name)}` : ""}<span class="n">${left}</span></button>`;
  }).join("")}</div></div>`;
  stage.innerHTML = `<h2 class="rate-h">Which language do you speak?</h2>
    <div class="v-controls">${group("Indian languages", INDIAN)}${group("World languages", data.languages.map((l) => l.code).filter((c) => !INDIAN.includes(c)))}</div>
    ${Ratings.invite ? '<p class="d-muted">You were invited: thank you. Your ratings are marked as invited.</p>' : ""}
    ${Ratings.mode === "local" ? '<p class="d-muted">Preview: ratings are saved in this browser only.</p>' : ""}`;
  for (const b of stage.querySelectorAll("[data-lang]")) b.addEventListener("click", () => aboutYou(b.dataset.lang));
}

// Asked once per session: native or not, and where the accent is from.
function aboutYou(code) {
  const l = data.languages.find((x) => x.code === code);
  const about = Ratings.about();
  stage.innerHTML = `<h2 class="rate-h">${esc(l.name)}: before we start</h2>
    <form class="r-form rate-about">
      <fieldset class="r-about"><legend>Are you a native ${esc(l.name)} speaker?</legend>
        <label><input type="radio" name="native" value="yes" ${about.native === true ? "checked" : ""} required> Yes</label>
        <label><input type="radio" name="native" value="no" ${about.native === false ? "checked" : ""}> No</label></fieldset>
      <label class="r-field">Where is your accent from? <span class="d-muted">(optional)</span><input name="region" maxlength="40" placeholder="e.g. Mumbai, North India, Lyon"></label>
      <div><button class="btn btn-dark btn-sm" type="submit">Start</button> <button class="btn btn-line btn-sm rate-back" type="button">Back</button></div>
    </form>`;
  const form = stage.querySelector("form");
  form.region.value = about.region || "";
  form.querySelector(".rate-back").addEventListener("click", pickLanguage);
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    Ratings.setAbout({ native: form.native.value === "yes", region: form.region.value.trim() });
    const queue = shuffle(l.voices.filter((v) => !Ratings.hasRated(voiceId(v))));
    rateNext(l, queue, 0, []);
  });
}

function rateNext(l, queue, i, done) {
  audio.pause();
  if (i >= queue.length) return reveal(l, done);
  const v = queue[i], id = voiceId(v), prefix = `b${i}`;
  stage.innerHTML = `<div class="rate-top"><h2 class="rate-h">Voice ${i + 1} of ${queue.length}</h2><div class="rate-progress"><span style="width:${(i / queue.length) * 100}%"></span></div></div>
    <p class="v-text" lang="${l.code}">${esc(l.text)}</p>
    <button class="btn btn-dark rate-play" type="button">Play</button>
    <form class="r-form"></form>`;
  const play = stage.querySelector(".rate-play");
  play.addEventListener("click", () => {
    if (!audio.paused) { audio.pause(); return; }
    audio.src = v.audio; audio.play().catch(() => {});
  });
  audio.onplay = () => { play.textContent = "Stop"; };
  audio.onpause = audio.onended = () => { play.textContent = "Play again"; };
  const form = stage.querySelector("form");
  form.append(Ratings.questionFields(prefix));
  form.insertAdjacentHTML("beforeend", `
    <label class="r-field">Anything else? <span class="d-muted">(optional; only the Voxie team reads it)</span><textarea name="comment" rows="2" maxlength="1000"></textarea></label>
    <p class="r-error" role="alert" hidden></p>
    <div><button class="btn btn-dark btn-sm" type="submit">Send and next</button> <button class="btn btn-line btn-sm rate-skip" type="button">Skip this voice</button></div>`);
  form.querySelector(".rate-skip").addEventListener("click", () => rateNext(l, queue, i + 1, done));
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const scores = Ratings.readScores(form, prefix);
    const err = form.querySelector(".r-error");
    if (!scores) { err.textContent = "Please answer all five questions."; err.hidden = false; return; }
    const about = Ratings.about();
    form.querySelector("button[type=submit]").disabled = true;
    const r = await Ratings.submit({ voice: id, lang: l.code, scores, native: about.native, region: about.region, comment: form.comment.value, blind: true });
    if (!r.ok) { err.textContent = r.error; err.hidden = false; form.querySelector("button[type=submit]").disabled = false; return; }
    rateNext(l, queue, i + 1, [...done, { v, scores }]);
  });
  window.scrollTo({ top: stage.offsetTop - 80, behavior: "smooth" });
}

// The reveal: which voice was which, with the listener's own scores.
function reveal(l, done) {
  const rows = done.map(({ v, scores }, n) => {
    const avg = Object.values(scores).reduce((a, b) => a + b, 0) / Ratings.QUESTIONS.length;
    return { v, avg, n };
  }).sort((a, b) => b.avg - a.avg);
  stage.innerHTML = `<h2 class="rate-h">Thank you! Here's who you heard</h2>
    ${rows.length ? `<ol class="rate-reveal">${rows.map(({ v, avg }) =>
      `<li><span>${Ratings.stars(avg)} <b>${avg.toFixed(1)}</b></span> <span>${esc(v.voice)}</span> <span class="v-tag">${esc(v.provider.replace(/\s*\((self-hosted|in testing)\)/, ""))}</span>${v.testing ? ' <span class="v-tag">not in Voxie yet</span>' : ""}</li>`).join("")}</ol>`
      : '<p class="d-muted">You skipped every voice; nothing was sent.</p>'}
    <p><a href="/voices#${l.code}">See every ${esc(l.name)} voice</a> · <button class="btn btn-line btn-sm rate-again" type="button">Rate another language</button></p>`;
  stage.querySelector(".rate-again").addEventListener("click", pickLanguage);
}

if (Ratings.mode === "off") {
  stage.innerHTML = `<h2 class="rate-h">Rating opens soon</h2><p class="d-muted">Meanwhile you can <a href="/voices">hear every voice</a>.</p>`;
} else {
  fetch("/media/voices/voices.json", { cache: "no-cache" }).then((r) => r.json()).then((d) => { data = d; pickLanguage(); })
    .catch(() => { stage.textContent = "Couldn't load the voices."; });
}
