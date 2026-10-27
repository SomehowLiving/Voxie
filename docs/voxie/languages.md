# Languages: what works, measured

All results below come from real APIs. Test callers are Google TTS
voices, used both clean and degraded to a phone line (8 kHz μ-law plus
noise). Real human callers will score lower; nobody has measured that yet.

## Heard, understood by your agent, spoken

| Language | Heard by | Spoken by |
|---|---|---|
| English, Spanish, French, Italian, Portuguese, Japanese | Deepgram multi | Kokoro |
| German, Dutch, Russian | Deepgram multi | Piper (CPU) |
| Chinese | Deepgram multi | Kokoro |
| Polish, Ukrainian, Czech, Hungarian, Swedish, Romanian, Finnish, Danish, Vietnamese | Deepgram multi | Piper (CPU) |
| Korean, Arabic, Turkish | Deepgram multi | – (no voice; answer in English) |
| Hindi, Hinglish, Indian English | Deepgram multi | Sarvam (Kokoro fallback) |
| Tamil, Telugu, Bengali, Gujarati, Punjabi, Kannada, Marathi, Malayalam | Sarvam (after identification) | Sarvam (Marathi: Kokoro fallback) |
| Odia | Sarvam | Sarvam |

## Identifying a turn's language (24 languages, phone quality)

| Identifier | Correct | Speed | Notes |
|---|---|---|---|
| Groq Whisper large-v3 | 24/24 on clips | 0.5–1.1s | Live, on short turns it mislabelled Bengali (as Vietnamese), Gujarati (as Bengali) and Punjabi (as Hindi) |
| Groq Whisper large-v3-turbo | 18/24 | ~0.6s | Mislabelled 6 Indian languages (te, kn, ml, bn, pa, mr): not used |
| Sarvam saarika:v2.5 | Every Indian language | 0.6–2.9s | Calls every world language "en-IN" |
| Deepgram's own detection | – | – | Tamil → en, Telugu → hi, Arabic → fr |

## Live calls with the adaptive listener

One config, scripted callers over WebRTC. The Odia caller is Sarvam's own
voice (`bulbul:v3`, clean audio), since Google TTS has no Odia:

| Caller | What happened | Check time |
|---|---|---|
| es, fr, de, ja, en | Stayed on Deepgram multi, never checked | – |
| hi, Hinglish (Indian number) | First two Hindi turns checked, then settled | 0.4–1.2s |
| ta, te, bn, gu, pa, kn, ml, mr | Switched to Sarvam on the first real turn, that turn corrected | 0.46–0.98s |
| pl, ko (2026-10-26, after Deepgram's multi update) | Stayed on Deepgram multi, every turn heard; a short turn checked (pl@0.83, ko@0.78) and confirmed | 0.52–0.77s |
| ml on a phone line (2026-10-26) | Multi labelled it Telugu at 0.95; Sarvam identified Malayalam and the call switched on the first turn | 0.46s |
| Wrong language on record (fr, Tamil caller) | Corrected on the first turn | 0.58s |
| od on record (as `od` or ISO `or`) | Started on Sarvam, every turn heard in Odia | – |
| od, nothing on record | Heard as Hindi by multi; Sarvam identified Odia, switched on the first turn | 0.65s |
| de, nl, ru | Stayed on Deepgram multi; answered by Piper in the caller's language | – |
| ta on an Indian number (after adding vi) | Sarvam identified Tamil, switched on the first turn | 0.54s |
| Groq key rejected | Sarvam identified Telugu, and the call switched | 0.58s |
| Groq and Sarvam both failing | Turns kept as heard; the call completed | – |

## Voices

Kokoro was checked by transcribing its output back. All eight languages
were detected correctly at 0.95–0.99, amounts included. So was every
Sarvam `bulbul:v3` language. Sarvam takes 1.3–2s per sentence and Kokoro
~130ms, so Indian-language replies start later.

Piper (German, Dutch, Russian, Polish; CPU, 0.1–0.35s a sentence) was
checked the same way with Whisper large-v3: a payment line in each language
matched the text at 0.94–1.00, and amounts in € were read right in all four
languages, for both genders ("€1.249,50" → "1.249,50 Euro"). Deepgram
multi returns nothing for Polish; fixed to `pl` it heard Piper's Polish at
0.996.

The second Piper batch (Ukrainian, Czech, Hungarian, Swedish, Romanian,
Finnish, Danish, Vietnamese) read the same line back at 0.92–1.00; Deepgram
fixed to each heard it at 0.91–1.00, multi at 0.0–0.83. Piper's "high"
voices are slower: Ukrainian took 0.9–2.2s a sentence, the others 0.1–0.6s.
Greek was left out: its one licensed voice read back at 0.82.

## Known gaps

- **Only synthetic voices tested.** The next step is a pilot with real
  people on real phone lines.
- **No voice** for Korean, Arabic or Turkish. Piper has voices for them,
  but trained on non-commercial or unlicensed data.
- **One voice gender** for Russian, Romanian, Finnish and Danish (male)
  and Vietnamese (female): Piper has no other licensed voice.
- **Short Slavic greetings can be misidentified:** a Ukrainian "Алло, хто
  це?" was called Czech, and corrected on the next turn. Whisper names
  Bengali "vi" too, so on Indian calls a "vi" waits for Sarvam.
- **No Greek voice:** Piper's licensed one was too unclear (0.82).
- **A call that starts on Sarvam** (a regional Indian language on record)
  can't notice a caller who speaks a world language: Sarvam turns their
  words into English.
- **Free tiers run out.** Groq's daily token limits and Sarvam's credits
  both ran out during testing. The fallbacks kept calls going, but plan
  on paid tiers.
