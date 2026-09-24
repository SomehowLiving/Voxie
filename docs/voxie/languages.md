# Languages: what works, measured

All results below come from real APIs. Test callers are Google TTS
voices, used both clean and degraded to a phone line (8 kHz μ-law plus
noise). Real human callers will score lower; nobody has measured that yet.

## Heard, understood by your agent, spoken

| Language | Heard by | Spoken by |
|---|---|---|
| English, Spanish, French, Italian, Portuguese, Japanese | Deepgram multi | Kokoro |
| German, Dutch, Russian | Deepgram multi | – (no voice; answer in English) |
| Chinese | Deepgram fixed `zh` (after identification) | Kokoro |
| Korean, Arabic, Turkish | Deepgram fixed (after identification) | – |
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

One config, scripted callers over WebRTC:

| Caller | What happened | Check time |
|---|---|---|
| es, fr, de, ja, en | Stayed on Deepgram multi, never checked | – |
| hi, Hinglish (Indian number) | First two Hindi turns checked, then settled | 0.4–1.2s |
| ta, te, bn, gu, pa, kn, ml, mr | Switched to Sarvam on the first real turn, that turn corrected | 0.46–0.98s |
| zh (clean and phone) | Identified on the first turn → Deepgram fixed zh | 0.48–0.55s |
| ko, ar, tr | Identified on the first turn → Deepgram fixed | 0.40–0.42s |
| Wrong language on record (fr, Tamil caller) | Corrected on the first turn | 0.58s |
| Groq key rejected | Sarvam identified Telugu, and the call switched | 0.58s |
| Groq and Sarvam both failing | Turns kept as heard; the call completed | – |

## Voices

Kokoro was checked by transcribing its output back. All eight languages
were detected correctly at 0.95–0.99, amounts included. So was every
Sarvam `bulbul:v3` language. Sarvam takes 1.3–2s per sentence and Kokoro
~130ms, so Indian-language replies start later.

## Known gaps

- **Only synthetic voices tested.** The next step is a pilot with real
  people on real phone lines.
- **No voice** for German, Dutch, Russian, Korean, Arabic or Turkish.
- **A call that starts on Sarvam** (a regional Indian language on record)
  can't notice a caller who speaks a world language: Sarvam turns their
  words into English.
- **Free tiers run out.** Groq's daily token limits and Sarvam's credits
  both ran out during testing. The fallbacks kept calls going, but plan
  on paid tiers.
