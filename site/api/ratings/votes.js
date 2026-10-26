// POST /api/ratings/votes  {voice, lang, value: 1 | -1 | 0, rater}
// One vote per browser per voice; 0 takes it back.
import { db, send, readJSON, validVoice, withinLimit } from "../_db.js";

export default async function handler(req, res) {
  if (req.method !== "POST") return send(res, 405, { error: "method not allowed" });
  try {
    let b;
    try { b = await readJSON(req); } catch { return send(res, 400, { error: "body" }); }
    if (!validVoice(b.voice, b.lang) || ![1, -1, 0].includes(b.value) || typeof b.rater !== "string" || !/^[0-9a-f-]{36}$/.test(b.rater)) {
      return send(res, 400, { error: "vote" });
    }
    const d = await db();
    if (!(await withinLimit(d, req, "vote", 300))) return send(res, 429, { error: "too many" });
    const key = { rater: b.rater, voice: b.voice };
    if (b.value === 0) await d.collection("votes").deleteOne(key);
    else await d.collection("votes").updateOne(key, { $set: { lang: b.lang, value: b.value, at: new Date() } }, { upsert: true });
    return send(res, 200, { ok: true });
  } catch (err) {
    console.error("[ratings/votes]", err.message);
    return send(res, 500, { error: "server error" });
  }
}
