// POST /api/ratings/events  {voice, lang, point: "play" | 25 | 50 | 75 | 100}
// Listening counters per voice. Not tied to anyone: no rater, no IP stored.
import { db, send, readJSON, validVoice, withinLimit } from "../_db.js";

export default async function handler(req, res) {
  if (req.method !== "POST") return send(res, 405, { error: "method not allowed" });
  try {
    let b;
    try { b = await readJSON(req); } catch { return send(res, 400, { error: "body" }); }
    const field = b.point === "play" ? "plays" : [25, 50, 75, 100].includes(b.point) ? `reach${b.point}` : null;
    if (!field || !validVoice(b.voice, b.lang)) return send(res, 400, { error: "event" });
    const d = await db();
    if (!(await withinLimit(d, req, "event", 1500))) return send(res, 429, { error: "too many" });
    await d.collection("listening").updateOne({ _id: b.voice }, { $inc: { [field]: 1 }, $set: { lang: b.lang } }, { upsert: true });
    return send(res, 200, { ok: true });
  } catch (err) {
    console.error("[ratings/events]", err.message);
    return send(res, 500, { error: "server error" });
  }
}
