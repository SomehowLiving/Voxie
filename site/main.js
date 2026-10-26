// Voxie's landing page: the nav, language tabs, copy buttons, the GitHub
// star count and the talk-to-us form. No dependencies.

const API = "https://api-production-471b.up.railway.app/api";

// --- the nav gets a hairline once you scroll ------------------------------------
const nav = document.querySelector(".nav");
const onScroll = () => nav.classList.toggle("scrolled", window.scrollY > 10);
window.addEventListener("scroll", onScroll, { passive: true });
onScroll();

// --- language tabs ----------------------------------------------------------------
const tabs = [...document.querySelectorAll('[role="tab"]')];
function select(tab) {
  for (const t of tabs) {
    const on = t === tab;
    t.setAttribute("aria-selected", String(on));
    t.tabIndex = on ? 0 : -1;
    document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
  }
}
for (const t of tabs) {
  t.addEventListener("click", () => select(t));
  t.addEventListener("keydown", (e) => {
    if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
    const next = tabs[(tabs.indexOf(t) + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length];
    select(next);
    next.focus();
  });
}

// --- copy buttons -------------------------------------------------------------------
for (const btn of document.querySelectorAll(".copy")) {
  btn.addEventListener("click", async () => {
    const text = btn.parentElement.querySelector("code").innerText;
    try {
      await navigator.clipboard.writeText(text);
      btn.textContent = "Copied";
    } catch {
      btn.textContent = "Select and copy";
    }
    setTimeout(() => { btn.textContent = "Copy"; }, 1800);
  });
}

// --- the star count on the GitHub buttons: starring itself happens on GitHub --------
fetch("https://api.github.com/repos/SomehowLiving/Voxie")
  .then((r) => (r.ok ? r.json() : null))
  .then((repo) => {
    const n = repo?.stargazers_count;
    if (typeof n !== "number" || n < 1) return; // no badge until there is a star to show
    for (const el of document.querySelectorAll(".stars")) {
      el.textContent = n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n);
      el.hidden = false;
    }
  })
  .catch(() => {});

// --- talk to us -------------------------------------------------------------------------
const form = document.querySelector(".access-form");
const status = form.querySelector(".form-status");
form.addEventListener("submit", async (e) => {
  e.preventDefault();
  const data = Object.fromEntries(new FormData(form));
  if (!form.email.checkValidity()) {
    status.className = "form-status err";
    status.textContent = "Enter a valid work email.";
    form.email.focus();
    return;
  }
  const params = new URLSearchParams(location.search);
  let source = params.get("utm_source") || undefined;
  if (!source && document.referrer) { try { source = new URL(document.referrer).hostname; } catch { /* none */ } }
  const button = form.querySelector("button");
  button.disabled = true;
  status.className = "form-status";
  status.textContent = "Sending…";
  try {
    const res = await fetch(`${API}/v1/access-requests`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        product: "voxie",
        email: data.email,
        ...(data.company ? { company: data.company } : {}),
        ...(data.use_case ? { use_case: data.use_case } : {}),
        ...(source ? { source } : {}),
        ...(data.website ? { website: data.website } : {}),
      }),
    });
    if (!res.ok) throw new Error((await res.json().catch(() => ({}))).message || "Couldn't send that. Try again in a minute.");
    status.className = "form-status ok";
    status.textContent = "Thanks. We'll be in touch at that address soon.";
    form.reset();
  } catch (err) {
    status.className = "form-status err";
    status.textContent = err.message || "Couldn't send that. Try again in a minute.";
  } finally {
    button.disabled = false;
  }
});

// --- liquid images: tap and the picture ripples like water ---------------------------
// Each .liquid element's picture (data-img) is drawn by WebGL with "cover" fit
// (data-pos: focal point, data-zoom: extra scale). A tap sends out a ring; moving
// the pointer leaves softer ones; the surface drifts a little on its own.
// No WebGL, a small screen, or reduced motion: the plain CSS image stays.
const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
const VERT = `attribute vec2 p; void main(){ gl_Position = vec4(p, 0.0, 1.0); }`;
const FRAG = `precision mediump float;
uniform sampler2D img; uniform vec2 res, isz, pos; uniform float zoom, time;
uniform vec4 rip[8];            // x, y (px), start time, strength
vec2 cover(vec2 px) {
  float s = max(res.x / isz.x, res.y / isz.y) * zoom;
  vec2 drawn = isz * s;
  vec2 off = (res - drawn) * pos;
  return (px - off) / drawn;
}
void main() {
  vec2 px = vec2(gl_FragCoord.x, res.y - gl_FragCoord.y);
  vec2 disp = vec2(sin(px.y * 0.011 + time * 0.7), cos(px.x * 0.009 + time * 0.55)) * 1.6;
  float light = 0.0;
  for (int i = 0; i < 8; i++) {
    vec4 r = rip[i];
    float age = time - r.z;
    if (r.w <= 0.0 || age < 0.0 || age > 3.0) continue;
    vec2 d = px - r.xy;
    float dist = length(d);
    float front = age * 420.0;
    float band = exp(-pow((dist - front) / 70.0, 2.0)) * exp(-age * 1.5) * r.w;
    float wave = sin((dist - front) * 0.085);
    disp += normalize(d + 0.0001) * wave * band * 22.0;
    light += wave * band * 0.07;
  }
  vec2 uv = cover(px + disp);
  vec4 c = texture2D(img, clamp(uv, 0.001, 0.999));
  gl_FragColor = vec4(c.rgb + light, c.a);
}`;

function liquid(el) {
  const canvas = document.createElement("canvas");
  canvas.className = "liquid-canvas";
  const gl = canvas.getContext("webgl", { premultipliedAlpha: false, antialias: false });
  if (!gl) return;
  const sh = (type, src) => { const s = gl.createShader(type); gl.shaderSource(s, src); gl.compileShader(s); return s; };
  const prog = gl.createProgram();
  gl.attachShader(prog, sh(gl.VERTEX_SHADER, VERT));
  gl.attachShader(prog, sh(gl.FRAGMENT_SHADER, FRAG));
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) return;
  gl.useProgram(prog);
  gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer());
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
  const loc = gl.getAttribLocation(prog, "p");
  gl.enableVertexAttribArray(loc);
  gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
  const u = (n) => gl.getUniformLocation(prog, n);
  const [px, py] = (el.dataset.pos || "0.5 0.5").split(" ").map(Number);
  gl.uniform2f(u("pos"), px, py);
  gl.uniform1f(u("zoom"), Number(el.dataset.zoom || 1));

  const image = new Image();
  image.src = el.dataset.img;
  const ripples = Array.from({ length: 8 }, () => [0, 0, -99, 0]);
  let next = 0, visible = true, lastMove = 0;
  const t0 = performance.now();
  const now = () => (performance.now() - t0) / 1000;
  const DPR = Math.min(window.devicePixelRatio || 1, 1.5);

  function size() {
    const r = el.getBoundingClientRect();
    canvas.width = Math.max(1, Math.round(r.width * DPR));
    canvas.height = Math.max(1, Math.round(r.height * DPR));
    gl.viewport(0, 0, canvas.width, canvas.height);
    gl.uniform2f(u("res"), canvas.width, canvas.height);
  }
  function ripple(e, strength) {
    const r = canvas.getBoundingClientRect();
    const x = (e.clientX - r.left) * DPR, y = (e.clientY - r.top) * DPR;
    if (x < 0 || y < 0 || x > canvas.width || y > canvas.height) return;
    ripples[next] = [x, y, now(), strength];
    next = (next + 1) % ripples.length;
  }
  function frame() {
    if (visible) {
      gl.uniform1f(u("time"), now());
      gl.uniform4fv(u("rip[0]"), ripples.flat());
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    }
    requestAnimationFrame(frame);
  }
  image.onload = () => {
    const tex = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, tex);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, image);
    for (const [k, v] of [[gl.TEXTURE_MIN_FILTER, gl.LINEAR], [gl.TEXTURE_MAG_FILTER, gl.LINEAR], [gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE], [gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE]]) gl.texParameteri(gl.TEXTURE_2D, k, v);
    gl.uniform2f(u("isz"), image.naturalWidth, image.naturalHeight);
    el.prepend(canvas);
    size();
    el.classList.add("liquid-on");
    requestAnimationFrame(frame);
  };
  // taps anywhere on the section ripple the picture, text included
  const area = el.closest("section") || el;
  area.addEventListener("pointerdown", (e) => ripple(e, 1));
  area.addEventListener("pointermove", (e) => {
    if (e.pointerType !== "mouse") return;
    const t = performance.now();
    if (t - lastMove < 110) return;
    lastMove = t;
    ripple(e, 0.28);
  }, { passive: true });
  new IntersectionObserver(([entry]) => { visible = entry.isIntersecting; }).observe(el);
  window.addEventListener("resize", size);
}
// The globe only ripples where it sits beside the text (wide screens); below
// that it's a plain picture under the language list.
if (!reducedMotion && window.innerWidth >= 720) {
  document.querySelectorAll(".liquid").forEach((el) => { if (!(el.classList.contains("langs") && window.innerWidth < 1080)) liquid(el); });
}

// --- entrance animations -------------------------------------------------------------
const revealables = document.querySelectorAll(".providers, .cards4 > *, .real-copy, .callouts li, .head, .flow > li, .uc, .step, .tips > *, .langs-inner, .results-inner > *, .stats > div, .limits li, .access-card");
revealables.forEach((el) => {
  el.classList.add("reveal");
  const i = [...el.parentElement.children].indexOf(el);
  el.style.setProperty("--d", `${Math.min(i, 5) * 0.07}s`);
});
const io = new IntersectionObserver((entries) => {
  for (const e of entries) if (e.isIntersecting) { e.target.classList.add("in"); io.unobserve(e.target); }
}, { rootMargin: "0px 0px -8% 0px" });
revealables.forEach((el) => io.observe(el));

// --- the page's liquid surface: soft flowing light, and ripples wherever you tap ------
(function surface() {
  if (reducedMotion) return; // the CSS gradient stays
  const canvas = document.createElement("canvas");
  canvas.className = "bg-liquid";
  const gl = canvas.getContext("webgl", { antialias: false, alpha: false });
  if (!gl) return;
  const FRAG_BG = `precision mediump float;
  uniform vec2 res; uniform float time, scroll; uniform vec4 rip[8];
  void main() {
    vec2 px = vec2(gl_FragCoord.x, res.y - gl_FragCoord.y);
    vec2 disp = vec2(0.0); float light = 0.0;
    for (int i = 0; i < 8; i++) {
      vec4 r = rip[i]; float age = time - r.z;
      if (r.w <= 0.0 || age < 0.0 || age > 3.5) continue;
      vec2 d = px - r.xy; float dist = length(d); float front = age * 380.0;
      float band = exp(-pow((dist - front) / 90.0, 2.0)) * exp(-age * 1.3) * r.w;
      float wave = sin((dist - front) * 0.06);
      disp += normalize(d + 0.0001) * wave * band * 30.0;
      light += wave * band * 0.05;
    }
    vec2 p = (px + disp) / res.y + vec2(0.0, scroll * 0.00025);
    float t = time * 0.08;
    float a = sin(p.x * 2.1 + t * 1.3 + sin(p.y * 1.7 + t) * 1.6);
    float b = sin(p.y * 2.4 - t * 1.1 + sin(p.x * 1.3 - t * 0.8) * 1.9);
    vec3 base = vec3(0.965, 0.973, 0.988);
    vec3 blue = vec3(0.82, 0.88, 0.985);
    vec3 peach = vec3(0.995, 0.935, 0.885);
    vec3 col = mix(base, blue, smoothstep(0.1, 1.0, a) * 0.6);
    col = mix(col, peach, smoothstep(0.35, 1.0, b) * 0.42);
    col += light;
    gl_FragColor = vec4(col, 1.0);
  }`;
  const sh = (type, src) => { const s = gl.createShader(type); gl.shaderSource(s, src); gl.compileShader(s); return s; };
  const prog = gl.createProgram();
  gl.attachShader(prog, sh(gl.VERTEX_SHADER, VERT));
  gl.attachShader(prog, sh(gl.FRAGMENT_SHADER, FRAG_BG));
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) return;
  gl.useProgram(prog);
  gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer());
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
  const loc = gl.getAttribLocation(prog, "p");
  gl.enableVertexAttribArray(loc);
  gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
  const u = (n) => gl.getUniformLocation(prog, n);
  // drawn at reduced resolution: it's soft anyway, and it keeps phones cool
  const SCALE = window.innerWidth < 720 ? 0.4 : 0.6;
  const ripples = Array.from({ length: 8 }, () => [0, 0, -99, 0]);
  let next = 0, lastMove = 0;
  const t0 = performance.now();
  const now = () => (performance.now() - t0) / 1000;
  function size() {
    canvas.width = Math.max(1, Math.round(window.innerWidth * SCALE));
    canvas.height = Math.max(1, Math.round(window.innerHeight * SCALE));
    gl.viewport(0, 0, canvas.width, canvas.height);
    gl.uniform2f(u("res"), canvas.width, canvas.height);
  }
  function ripple(e, strength) {
    ripples[next] = [e.clientX * SCALE, e.clientY * SCALE, now(), strength];
    next = (next + 1) % ripples.length;
  }
  window.addEventListener("pointerdown", (e) => ripple(e, 1), { passive: true });
  window.addEventListener("pointermove", (e) => {
    if (e.pointerType !== "mouse") return;
    const t = performance.now();
    if (t - lastMove < 140) return;
    lastMove = t;
    ripple(e, 0.25);
  }, { passive: true });
  window.addEventListener("resize", size);
  document.body.prepend(canvas);
  size();
  (function frame() {
    if (!document.hidden) {
      gl.uniform1f(u("time"), now());
      gl.uniform1f(u("scroll"), window.scrollY * SCALE);
      gl.uniform4fv(u("rip[0]"), ripples.flat());
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    }
    requestAnimationFrame(frame);
  })();
})();

// --- touch screens: a tap ripples across the page, drawn on top of it ----------------
(function tapLayer() {
  if (reducedMotion || !window.matchMedia("(pointer: coarse)").matches) return;
  const c = document.createElement("canvas");
  c.className = "tap-layer";
  document.body.appendChild(c);
  const g = c.getContext("2d");
  let rings = [], running = false;
  const DPR = Math.min(window.devicePixelRatio || 1, 2);
  const size = () => { c.width = innerWidth * DPR; c.height = innerHeight * DPR; };
  size();
  addEventListener("resize", size);
  function draw(t) {
    g.clearRect(0, 0, c.width, c.height);
    rings = rings.filter((r) => t - r.t0 < 1400);
    for (const r of rings) {
      const k = (t - r.t0) / 1400; // 0..1
      for (let i = 0; i < 3; i++) {
        const kk = k - i * 0.12;
        if (kk <= 0) continue;
        const rad = (20 + kk * 220) * DPR;
        const a = (1 - kk) ** 2 * (0.55 - i * 0.15);
        const grad = g.createRadialGradient(r.x, r.y, rad * 0.86, r.x, r.y, rad);
        grad.addColorStop(0, "rgba(255,255,255,0)");
        grad.addColorStop(0.6, `rgba(255,255,255,${a})`);
        grad.addColorStop(0.85, `rgba(120,150,230,${a * 0.35})`);
        grad.addColorStop(1, "rgba(255,255,255,0)");
        g.fillStyle = grad;
        g.beginPath(); g.arc(r.x, r.y, rad, 0, Math.PI * 2); g.fill();
      }
    }
    if (rings.length) requestAnimationFrame(draw); else running = false;
  }
  addEventListener("pointerdown", (e) => {
    rings.push({ x: e.clientX * DPR, y: e.clientY * DPR, t0: performance.now() });
    if (!running) { running = true; requestAnimationFrame(draw); }
  }, { passive: true });
})();
