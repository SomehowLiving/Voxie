// GET  /api/ratings?lang=hi   (or lang=all): totals per voice, never single ratings or comments.
// POST /api/ratings           one listener's rating of one voice (see ratings.js for the shape).
import { db, send, readJSON, validVoice, validInvite, withinLimit, QUESTIONS, LANG_RE } from "../_db.js";

export default async function handler(req, res) {
  try {
    if (req.method === "GET") return await totals(req, res);
    if (req.method === "POST") return await rate(req, res);
    res.setHeader("Allow", "GET, POST");
    return send(res, 405, { error: "method not allowed" });
  } catch (err) {
    console.error("[ratings]", err.message);
    return send(res, 500, { error: "server error" });
  }
}

async function totals(req, res) {
  const lang = new URL(req.url, "http://x").searchParams.get("lang") || "";
  if (lang !== "all" && !LANG_RE.test(lang)) return send(res, 400, { error: "lang" });
  const d = await db();
  const match = lang === "all" ? {} : { lang };
  const [ratings, listening, votes] = await Promise.all([
    d.collection("ratings").aggregate([
      { $match: match },
      { $group: {
        _id: "$voice", n: { $sum: 1 },
        native: { $sum: { $cond: ["$native", 1, 0] } },
        invited: { $sum: { $cond: [{ $gt: ["$invite", ""] }, 1, 0] } },
        last: { $max: "$at" },
        regions: { $push: "$region" },
        ...Object.fromEntries(QUESTIONS.map((q) => [q, { $avg: `$scores.${q}` }])),
      } },
    ]).toArray(),
    d.collection("listening").find(match).toArray(),
    d.collection("votes").aggregate([
      { $match: match },
      { $group: { _id: "$voice", likes: { $sum: { $cond: [{ $eq: ["$value", 1] }, 1, 0] } }, dislikes: { $sum: { $cond: [{ $eq: ["$value", -1] }, 1, 0] } } } },
    ]).toArray(),
  ]);
  const voices = {};
  for (const r of ratings) {
    const regions = {};
    for (const g of r.regions) if (g) regions[g] = (regions[g] || 0) + 1;
    voices[r._id] = { n: r.n, native: r.native, invited: r.invited, last: r.last, regions,
      avg: Object.fromEntries(QUESTIONS.map((q) => [q, r[q]])) };
  }
  const listen = (id) => (voices[id] ||= { n: 0 }).listening ||= { plays: 0, reach: { 25: 0, 50: 0, 75: 0, 100: 0 }, likes: 0, dislikes: 0 };
  for (const l of listening) {
    const s = listen(l._id);
    s.plays = l.plays || 0;
    for (const p of [25, 50, 75, 100]) s.reach[p] = l[`reach${p}`] || 0;
  }
  for (const v of votes) Object.assign(listen(v._id), { likes: v.likes, dislikes: v.dislikes });
  return send(res, 200, { voices });
}

async function rate(req, res) {
  let b;
  try { b = await readJSON(req); } catch { return send(res, 400, { error: "body" }); }
  if (!validVoice(b.voice, b.lang) || typeof b.rater !== "string" || !/^[0-9a-f-]{36}$/.test(b.rater)) return send(res, 400, { error: "voice" });
  const scores = {};
  for (const q of QUESTIONS) {
    const v = b.scores?.[q];
    if (!Number.isInteger(v) || v < 1 || v > 5) return send(res, 400, { error: "scores" });
    scores[q] = v;
  }
  const d = await db();
  if (!(await withinLimit(d, req, "rating", 60))) return send(res, 429, { error: "too many" });
  const doc = {
    voice: b.voice, lang: b.lang, scores, native: b.native === true,
    region: typeof b.region === "string" ? b.region.trim().slice(0, 40) : "",
    comment: typeof b.comment === "string" ? b.comment.trim().slice(0, 1000) : "", // private: never returned
    invite: validInvite(b.invite), blind: b.blind === true, rater: b.rater, at: new Date(),
  };
  try {
    await d.collection("ratings").insertOne(doc);
  } catch (err) {
    if (err.code === 11000) return send(res, 409, { error: "already rated" });
    throw err;
  }
  return send(res, 200, { ok: true });
}
