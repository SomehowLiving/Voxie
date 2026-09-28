// Voxie quickstart: talk to a voice agent from your browser, in any language.
//
// One process, three jobs, no dependencies (Node 20+):
//   - :8400  the call page, and a WHIP proxy to the Voxie server
//   - :9101  a demo agent answering Voxie's agent requests (greeting, chat,
//            listen, oneshot) -- the part you replace with your own app
//
//   node examples/quickstart/server.mjs      then open http://localhost:8400
//
// Why a proxy: Voxie learns who is on the call from the
// X-StreamCore-Resource-Id header (a phone bridge sends the dialled number).
// Browsers can't send that header cross-origin, so the page talks WHIP to
// this server, which forwards it with the header added. Audio itself goes
// straight to the Voxie server.
import { createServer } from "node:http";
import { readFileSync } from "node:fs";

const VOXIE = process.env.VOXIE_URL ?? "http://127.0.0.1:8080";
const PAGE_PORT = Number(process.env.PAGE_PORT ?? 8400);
const AGENT_PORT = Number(process.env.AGENT_PORT ?? 9101);
// Must match [agent] api_key / STREAMCORE_AGENT_API_KEY on the Voxie server.
const AGENT_KEY = process.env.STREAMCORE_AGENT_API_KEY ?? "";
const GROQ_KEY = process.env.GROQ_API_KEY ?? "";
const MODEL = process.env.QUICKSTART_MODEL ?? "openai/gpt-oss-20b";
// The voice router's health: which languages it can speak right now.
const VOICE_HEALTH = process.env.VOICE_HEALTH_URL ?? "http://127.0.0.1:8300/health";

const ts = () => new Date().toISOString().slice(11, 23);
const page = readFileSync(new URL("./index.html", import.meta.url), "utf8");

// --- live feed to the page (server-sent events) -----------------------------
const feeds = new Set();
function publish(event) {
  const line = `data: ${JSON.stringify(event)}\n\n`;
  for (const res of feeds) res.write(line);
}

// --- who's calling ------------------------------------------------------------
// A real app knows its caller from the dialled number. Here the page says who
// it is before connecting: a name, the language on record, and whether the
// number is Indian -- what Voxie's adaptive listener starts from.
const callers = new Map(); // resourceId -> { name, language, region }
let callNo = 0;
let currentResource = "";

// --- the demo agent -------------------------------------------------------------
// Voxie posts every turn here. The contract (docs/voxie/agent-contract.md):
//   listen   -> { language?, region? }     before the caller speaks
//   greeting -> { text }                   the opening line
//   chat     -> { text, end_call? }        answer what the caller said
//   oneshot  -> { text }                   background work; nothing to do here
const sessions = new Map(); // session_id -> { caller, history }

// Only answer in a language the voice can speak *now* (if Sarvam is down,
// Tamil can't be voiced): English beats silence.
let voiceLanguages = { list: null, checkedAt: 0 };
async function speakable() {
  if (Date.now() - voiceLanguages.checkedAt < 15_000) return voiceLanguages.list;
  let list = null;
  try {
    const res = await fetch(VOICE_HEALTH, { signal: AbortSignal.timeout(1000) });
    const body = await res.json();
    if (Array.isArray(body.languages)) list = body.languages;
  } catch {}
  voiceLanguages = { list, checkedAt: Date.now() };
  return list;
}

function systemPrompt(languages) {
  const voiced = languages ? languages.join(", ") : "any";
  return `You are Voxie, a friendly voice assistant on a live phone call.
- You're a demo: you have no real business data (hours, prices, bookings). If asked, say so kindly instead of making something up.
- Reply in the language the caller is speaking right now; if they switch, switch with them.
- Your voice can only speak these languages (ISO 639-1): ${voiced}. If the caller's language isn't one of them, reply in English.
- One or two short spoken sentences. No markdown, lists or emoji.
- Caller lines may start with [heard in: xx], the language speech recognition heard; trust it for real sentences.
- Begin every reply with [lang:xx], the ISO 639-1 code of your reply's language (e.g. [lang:hi]).
- If the caller says goodbye or wants to end the call, say a short goodbye and end with [end].`;
}

async function think(history) {
  const languages = await speakable();
  const reply = await ask(history, languages);
  // Models don't always obey the language list; a reply the voice can't
  // speak would be silence, so ask once more, for English.
  const lang = /^\s*\[lang:([a-z]{2,3})\]/i.exec(reply.text)?.[1]?.toLowerCase();
  if (languages && lang && !languages.includes(lang)) {
    return ask([...history, { role: "system", content: "Your voice can't speak that language. Reply in English, starting with [lang:en]." }], languages);
  }
  return reply;
}

async function ask(history, languages) {
  if (!GROQ_KEY) {
    // No model configured: prove the pipeline works by echoing.
    const last = history.at(-1)?.content ?? "";
    return { text: `You said: ${last}`, endCall: /\b(bye|goodbye)\b/i.test(last) };
  }
  const res = await fetch("https://api.groq.com/openai/v1/chat/completions", {
    method: "POST",
    headers: { Authorization: `Bearer ${GROQ_KEY}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      model: MODEL,
      messages: [{ role: "system", content: systemPrompt(languages) }, ...history],
      max_completion_tokens: 400,
      ...(MODEL.startsWith("openai/gpt-oss") ? { reasoning_effort: "low" } : {}),
    }),
    signal: AbortSignal.timeout(8000),
  });
  if (!res.ok) throw new Error(`Groq ${res.status}: ${(await res.text()).slice(0, 200)}`);
  const reply = (await res.json()).choices?.[0]?.message?.content?.trim() ?? "";
  const endCall = /\[end\]\s*$/i.test(reply);
  return { text: reply.replace(/\[end\]\s*$/i, "").trim(), endCall };
}

// The [lang:xx] tag tells the voice router which voice to use; the page and
// Voxie's transcripts show the words without it.
const untagged = (text) => text.replace(/^\s*\[lang:[a-z]{2,3}\]\s*/i, "");

async function handleAgent(req) {
  const call = sessions.get(req.session_id) ?? {
    caller: callers.get(req.resource_id ?? "") ?? { name: "there", language: "", region: "" },
    history: [],
  };
  sessions.set(req.session_id, call);

  switch (req.type) {
    case "listen":
      return { language: call.caller.language || undefined, region: call.caller.region || undefined };
    case "greeting": {
      const text = `Hi ${call.caller.name}, this is Voxie. How can I help you today?`;
      call.history.push({ role: "assistant", content: text });
      publish({ kind: "agent", text });
      return { text };
    }
    case "chat": {
      const said = (req.text ?? "").trim();
      if (!said) return { text: "" };
      publish({ kind: "caller", text: said, language: req.language });
      // Voxie says which language it heard; the model gets it as a hint --
      // only on a real sentence: a one-word "No," may be labelled English
      // whatever the caller speaks.
      const hint = req.language && said.split(/\s+/).length >= 3;
      call.history.push({ role: "user", content: hint ? `[heard in: ${req.language}] ${said}` : said });
      let reply;
      try {
        reply = await think(call.history);
      } catch (err) {
        console.error(`${ts()} [agent] ${err}`);
        reply = { text: "Sorry, I had trouble with that. Could you say it again?", endCall: false };
      }
      // Models sometimes add [end] to a first reply; a call isn't over before
      // the caller has said more than one thing.
      if (call.history.filter((m) => m.role === "user").length < 2) reply.endCall = false;
      call.history.push({ role: "assistant", content: reply.text });
      publish({ kind: "agent", text: untagged(reply.text), endCall: reply.endCall });
      return { text: reply.text, ...(reply.endCall ? { end_call: true } : {}) };
    }
    default: // "oneshot": Voxie's own background summaries
      return { text: "" };
  }
}

createServer(async (req, res) => {
  if (req.method !== "POST") return res.writeHead(404).end();
  if (AGENT_KEY && req.headers.authorization !== `Bearer ${AGENT_KEY}`) {
    return res.writeHead(401, { "Content-Type": "application/json" }).end('{"error":"unauthorized"}');
  }
  let body = "";
  for await (const chunk of req) body += chunk;
  try {
    const out = await handleAgent(JSON.parse(body));
    res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify(out));
  } catch (err) {
    console.error(`${ts()} [agent] ${err}`);
    res.writeHead(500, { "Content-Type": "application/json" }).end('{"error":"internal"}');
  }
}).listen(AGENT_PORT, () => console.log(`${ts()} demo agent on :${AGENT_PORT}${GROQ_KEY ? ` (Groq ${MODEL})` : " (echo mode: set GROQ_API_KEY for real replies)"}`));

// --- the page, and WHIP forwarded to Voxie with the caller's identity ---------
async function forwardWhip(req, res) {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  const headers = { "X-StreamCore-Resource-Id": currentResource };
  for (const h of ["content-type", "if-match"]) {
    if (typeof req.headers[h] === "string") headers[h] = req.headers[h];
  }
  const upstream = await fetch(`${VOXIE}${req.url}`, {
    method: req.method,
    headers,
    body: chunks.length ? Buffer.concat(chunks) : undefined,
  });
  const out = {};
  for (const h of ["content-type", "etag", "accept-patch", "x-resume-token"]) {
    const v = upstream.headers.get(h);
    if (v) out[h] = v;
  }
  const location = upstream.headers.get("location");
  if (location) out["location"] = new URL(location, VOXIE).pathname;
  res.writeHead(upstream.status, out).end(Buffer.from(await upstream.arrayBuffer()));
}

createServer(async (req, res) => {
  const url = req.url ?? "/";
  if (req.method === "GET" && url === "/") {
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" }).end(page);
  } else if (req.method === "GET" && url === "/api/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache", Connection: "keep-alive" });
    res.write(": connected\n\n");
    feeds.add(res);
    req.on("close", () => feeds.delete(res));
  } else if (req.method === "POST" && url === "/api/call") {
    let body = "";
    for await (const chunk of req) body += chunk;
    let caller = {};
    try {
      caller = JSON.parse(body);
    } catch {}
    callNo += 1;
    // A fresh identity per call, so a new call never picks up an old one's state.
    currentResource = `caller-${Date.now()}-${callNo}`;
    callers.set(currentResource, {
      name: String(caller.name || "there").slice(0, 60),
      language: String(caller.language || "").slice(0, 10),
      region: caller.region === "IN" ? "IN" : "",
    });
    console.log(`${ts()} [call] ${currentResource} ${JSON.stringify(callers.get(currentResource))}`);
    res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ id: currentResource }));
  } else if (url.startsWith("/whip")) {
    forwardWhip(req, res).catch((err) => {
      console.error(`${ts()} [whip] ${err}`);
      res.writeHead(502, { "Content-Type": "text/plain" }).end(`Voxie server unreachable at ${VOXIE}: ${err}`);
    });
  } else {
    res.writeHead(404).end();
  }
}).listen(PAGE_PORT, () => console.log(`${ts()} open http://localhost:${PAGE_PORT} to talk to Voxie`));
