// /try: demo video + a live call to a Voxie server, straight from the browser
// over WHIP (WebRTC). The server is the public one in try-config.js, or one the
// visitor runs themselves. No credentials live here: keys stay on the server.
const cfg = window.VOXIE_TRY || {};
const $ = (id) => document.getElementById(id);

// --- nav border on scroll, copy buttons (as on the home page) ---------------
const nav = document.querySelector(".nav");
addEventListener("scroll", () => nav.classList.toggle("scrolled", scrollY > 8), { passive: true });
for (const btn of document.querySelectorAll(".copy")) {
  btn.addEventListener("click", async () => {
    const code = btn.parentElement.querySelector("code").innerText.replace(/\s+#.*$/gm, "");
    try { await navigator.clipboard.writeText(code); btn.textContent = "Copied"; } catch { btn.textContent = "Select and copy"; }
    setTimeout(() => (btn.textContent = "Copy"), 1600);
  });
}

// --- demo video ------------------------------------------------------------
(function video() {
  const src = String(cfg.video || "").trim();
  if (!src) return;
  let url;
  try { url = new URL(src, location.href); } catch { return; }
  if (url.protocol !== "https:" && url.origin !== location.origin) return;
  const frame = $("videoFrame");
  $("videoEmpty").remove();
  if (/(^|\.)youtube(-nocookie)?\.com$/.test(url.hostname) && url.pathname.startsWith("/embed/")) {
    const f = document.createElement("iframe");
    f.src = url.href;
    f.title = "Voxie demo";
    f.allow = "encrypted-media; picture-in-picture; fullscreen";
    f.loading = "lazy";
    frame.appendChild(f);
  } else {
    const v = document.createElement("video");
    v.src = url.href;
    v.controls = true;
    v.preload = "metadata";
    v.playsInline = true;
    if (cfg.poster) v.poster = cfg.poster;
    // take the video's own shape (the demo is 1280x1000, not 16:9)
    v.addEventListener("loadedmetadata", () => { if (v.videoWidth) frame.style.aspectRatio = `${v.videoWidth} / ${v.videoHeight}`; });
    frame.appendChild(v);
  }
})();

// --- which Voxie server ----------------------------------------------------
const publicServer = String(cfg.server || "").trim();
const store = { get(k) { try { return localStorage.getItem(k); } catch { return null; } }, set(k, v) { try { localStorage.setItem(k, v); } catch {} } };
if (publicServer) {
  $("localSetup").hidden = true;
} else {
  $("server").value = store.get("voxie-try-server") || "";
}

function serverBase() {
  const raw = (publicServer || $("server").value.trim() || $("server").placeholder).replace(/\/+$/, "");
  let url;
  try { url = new URL(raw); } catch { throw new Error(`"${raw}" isn't a URL. Try http://localhost:8080`); }
  if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error("The server address must start with http:// or https://");
  const local = ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname);
  if (location.protocol === "https:" && url.protocol === "http:" && !local) {
    throw new Error("This page is HTTPS, so it can only reach your Voxie over HTTPS, or over http://localhost.");
  }
  if (!publicServer) store.set("voxie-try-server", $("server").value.trim());
  return url.origin;
}

// --- call ------------------------------------------------------------------
let pc = null, mic = null, sessionUrl = null, ended = true, meterStop = null, partialEl = null, agentEl = null;
const log = $("log");

function setStatus(text, state = "") {
  $("statusText").textContent = text;
  $("status").className = "status" + (state ? " " + state : "");
}
function addTurn(who, text, cls) {
  const el = document.createElement("div");
  el.className = "turn " + cls;
  const b = document.createElement("b");
  b.textContent = who;
  const t = document.createElement("span");
  t.className = "text";
  t.textContent = text;
  el.append(b, t);
  log.appendChild(el);
  el.scrollIntoView({ block: "nearest", behavior: "smooth" }); // only if it is out of view
  return el;
}
function addNote(text) {
  const el = document.createElement("div");
  el.className = "note";
  el.textContent = text;
  log.appendChild(el);
}
const clean = (s) => s.replace(/\[(lang|voice):[^\]]*\]\s*/g, "");

function onEvent(raw) {
  let msg;
  try { msg = JSON.parse(raw); } catch { return; }
  if (msg.type === "transcript" && msg.text) {
    if (!partialEl) {
      // Only the latest exchange is shown: your words and Voxie's reply.
      log.innerHTML = "";
      agentEl = null;
      partialEl = addTurn("You", "", "you partial");
    }
    partialEl.querySelector(".text").textContent = msg.text;
    if (msg.final) { partialEl.classList.remove("partial"); partialEl = null; agentEl = null; }
  } else if (msg.type === "response" && msg.text) {
    if (!agentEl) agentEl = addTurn("Voxie", "", "agent");
    const t = agentEl.querySelector(".text");
    t.textContent = clean(t.textContent + msg.text);
  } else if (msg.type === "state") {
    const labels = { listening: "Listening", thinking: "Voxie is thinking", speaking: "Voxie is speaking" };
    if (labels[msg.state]) setStatus(labels[msg.state], msg.state);
    if (msg.state === "listening") agentEl = null;
  }
}

function startMeter(stream) {
  const ctx = new AudioContext();
  const an = ctx.createAnalyser();
  an.fftSize = 512;
  ctx.createMediaStreamSource(stream).connect(an);
  const buf = new Uint8Array(an.fftSize);
  let raf;
  (function tick() {
    an.getByteTimeDomainData(buf);
    let peak = 0;
    for (const v of buf) peak = Math.max(peak, Math.abs(v - 128));
    $("meterBar").style.width = Math.min(100, peak * 1.6) + "%";
    raf = requestAnimationFrame(tick);
  })();
  meterStop = () => { cancelAnimationFrame(raf); ctx.close(); $("meterBar").style.width = "0"; meterStop = null; };
}

async function waitForIce(conn) {
  if (conn.iceGatheringState === "complete") return;
  await new Promise((resolve) => {
    conn.addEventListener("icegatheringstatechange", () => conn.iceGatheringState === "complete" && resolve());
    setTimeout(resolve, 2000);
  });
}

async function call() {
  $("call").disabled = true;
  ended = false;
  log.innerHTML = "";
  try {
    const base = serverBase();
    setStatus("Asking for your microphone…");
    mic = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
    startMeter(mic);

    setStatus("Connecting…");
    pc = new RTCPeerConnection();
    mic.getTracks().forEach((t) => pc.addTrack(t, mic));
    pc.ontrack = (e) => { $("remote").srcObject = e.streams[0] || new MediaStream([e.track]); };
    const dc = pc.createDataChannel("events"); // before the offer, or no transcripts arrive
    dc.onmessage = (e) => onEvent(e.data);
    pc.onconnectionstatechange = () => {
      const s = pc && pc.connectionState;
      if (s === "connected") setStatus("Connected. Say hello, or wait for Voxie.", "live");
      if ((s === "disconnected" || s === "failed" || s === "closed") && !ended) {
        addNote(s === "failed" ? "The audio connection failed." : "Call ended.");
        cleanup();
      }
    };
    await pc.setLocalDescription(await pc.createOffer());
    await waitForIce(pc);

    let resp;
    try {
      resp = await fetch(base + "/whip", { method: "POST", headers: { "Content-Type": "application/sdp" }, body: pc.localDescription.sdp });
    } catch {
      const port = new URL(base).port;
      if (port === "8400" || port === "8410" || port === "9101") {
        throw new Error(`${base} is the quickstart's call page or demo agent, not the Voxie server. Use the server's address: http://localhost:8080 by default.`);
      }
      throw new Error(`Couldn't reach a Voxie server at ${base}. Is it running (port 8080 by default), and did you allow this page to reach it?`);
    }
    if (resp.status === 429) throw new Error("Too many calls right now. Try again in a minute.");
    if (resp.status === 503) throw new Error("Voxie is busy with other calls. Try again shortly.");
    if (resp.status !== 201) throw new Error(`The Voxie server answered ${resp.status}.`);
    const loc = resp.headers.get("Location");
    sessionUrl = loc ? new URL(loc, base).href : null;
    await pc.setRemoteDescription({ type: "answer", sdp: await resp.text() });
    $("hang").disabled = false;
  } catch (err) {
    addNote(String(err.message || err));
    cleanup();
  }
}

function cleanup() {
  ended = true;
  if (pc) { pc.close(); pc = null; }
  if (mic) { mic.getTracks().forEach((t) => t.stop()); mic = null; }
  if (meterStop) meterStop();
  partialEl = agentEl = null;
  $("call").disabled = false;
  $("hang").disabled = true;
  setStatus("Not connected");
}

function hangUp() {
  const url = sessionUrl;
  sessionUrl = null;
  addNote("You hung up.");
  cleanup();
  if (url) fetch(url, { method: "DELETE" }).catch(() => {});
}

$("call").addEventListener("click", call);
$("hang").addEventListener("click", hangUp);
// Leaving the page ends the call on the server too.
addEventListener("pagehide", () => { if (sessionUrl) fetch(sessionUrl, { method: "DELETE", keepalive: true }).catch(() => {}); });
