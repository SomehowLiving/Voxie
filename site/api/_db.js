// Shared by the ratings API: the MongoDB connection, request checks and rate
// limits. Files starting with "_" aren't routes.
//
// Env: MONGODB_URL (required), MONGODB_DB (default "voxie"),
// RATINGS_SALT (hashes IP addresses for rate limits; never stored raw),
// RATINGS_INVITES (comma-separated invite codes; others count as uninvited).
import { MongoClient } from "mongodb";
import { createHash } from "node:crypto";

let clientPromise;
// One connection per function instance, reused across requests.
export async function db() {
  if (!process.env.MONGODB_URL) throw new Error("MONGODB_URL is not set");
  clientPromise ??= new MongoClient(process.env.MONGODB_URL, { maxPoolSize: 5, serverSelectionTimeoutMS: 5000 }).connect()
    .then(async (client) => {
      const d = client.db(process.env.MONGODB_DB || "voxie");
      await Promise.all([
        d.collection("ratings").createIndex({ rater: 1, voice: 1 }, { unique: true }),
        d.collection("ratings").createIndex({ lang: 1 }),
        d.collection("votes").createIndex({ rater: 1, voice: 1 }, { unique: true }),
        d.collection("votes").createIndex({ lang: 1 }),
        d.collection("listening").createIndex({ lang: 1 }),
        // Rate-limit hits expire on their own after an hour.
        d.collection("hits").createIndex({ at: 1 }, { expireAfterSeconds: 3600 }),
      ]);
      return client;
    })
    .catch((err) => { clientPromise = undefined; throw err; });
  return (await clientPromise).db(process.env.MONGODB_DB || "voxie");
}

// A voice id is its clip's file name: "hi-parler-divya". It starts with the language.
export const VOICE_RE = /^[a-z]{2}-[a-z0-9_-]{1,80}$/;
export const LANG_RE = /^[a-z]{2}$/;
export const QUESTIONS = ["naturalness", "pronunciation", "intelligibility", "accent", "overall"];
export const LISTEN_POINTS = [25, 50, 75, 100];

export function validVoice(voice, lang) {
  return typeof voice === "string" && typeof lang === "string" && VOICE_RE.test(voice) && LANG_RE.test(lang) && voice.startsWith(lang + "-");
}

export function send(res, status, body) {
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json");
  res.setHeader("Cache-Control", "no-store");
  res.end(JSON.stringify(body));
}

export async function readJSON(req) {
  if (req.body && typeof req.body === "object") return req.body;
  let raw = "";
  for await (const chunk of req) {
    raw += chunk;
    if (raw.length > 8192) throw new Error("too large");
  }
  return JSON.parse(raw || "{}");
}

function ipHash(req) {
  const ip = String(req.headers["x-forwarded-for"] || req.socket?.remoteAddress || "").split(",")[0].trim();
  return createHash("sha256").update((process.env.RATINGS_SALT || "voxie") + ip).digest("hex").slice(0, 24);
}

// At most `limit` requests of `kind` per IP address an hour. Returns false when over.
export async function withinLimit(d, req, kind, limit) {
  const who = ipHash(req);
  const n = await d.collection("hits").countDocuments({ who, kind });
  if (n >= limit) return false;
  await d.collection("hits").insertOne({ who, kind, at: new Date() });
  return true;
}

export function validInvite(code) {
  const codes = (process.env.RATINGS_INVITES || "").split(",").map((c) => c.trim()).filter(Boolean);
  return typeof code === "string" && codes.includes(code) ? code : "";
}
